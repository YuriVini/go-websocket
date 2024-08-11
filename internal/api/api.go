package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/YuriVini/go-websocket/internal/store/pgstore"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

type apiHandler struct {
	q           *pgstore.Queries
	r           *chi.Mux
	upgrader    websocket.Upgrader
	subscribers map[string]map[*websocket.Conn]context.CancelFunc
	mu          *sync.Mutex
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.r.ServeHTTP(w, r)
}

func NewHandler(q *pgstore.Queries) http.Handler {
	a := apiHandler{
		q:           q,
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc),
		mu:          &sync.Mutex{},
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, middleware.Logger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/subscribe/{room_id}", a.handleSubscribe)

	r.Route("/api", func(r chi.Router) {
		r.Route("/rooms", func(r chi.Router) {
			r.Post("/", a.handleCreateRoom)
			r.Get("/", a.handleGetRooms)

			r.Route("/{room_id}/messages", func(r chi.Router) {
				r.Post("/", a.handleCreateRoomMesssages)
				r.Get("/", a.handleGetRoomMesssages)

				r.Route("/{message_id}", func(r chi.Router) {
					r.Get("/", a.handleGetRoomMessage)
					r.Patch("/react", a.handleReactToMessage)
					r.Delete("/react", a.handleRemoveReactFromMessage)
					r.Patch("/answer", a.handleMarkMessageAsAnswered)
				})
			})
		})
	})

	a.r = r
	return a
}

const (
	MessageKindMessageCreated     = "message_created"
	MessageKindMessageReacted     = "message_reacted"
	MessageKindMessageAnswered    = "message_answered"
	MessageKindMessageRemoveReact = "message_remove_react"
)

type MessageMessageCreated struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

type MessageWS struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

type Message struct {
	ID            string `json:"id"`
	RoomID        string `json:"room_id"`
	Message       string `json:"message"`
	ReactionCount int64  `json:"reaction_count"`
	Answered      bool   `json:"answered"`
}

func (h apiHandler) notifyClients(msg MessageWS) {
	h.mu.Lock()

	defer h.mu.Unlock()

	subscribers, ok := h.subscribers[msg.RoomID]
	if !ok || len(subscribers) == 0 {
		return
	}

	for conn, cancel := range subscribers {
		if err := conn.WriteJSON(msg); err != nil {
			slog.Error("Failed to send message to client", "error", err)
			cancel()
		}
	}
}

func (h apiHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	roomID, err := uuid.Parse(rawRoomID)

	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}

	_, err = h.q.GetRoom(r.Context(), roomID)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Room not found", http.StatusBadRequest)
			return
		}

		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	c, err := h.upgrader.Upgrade(w, r, nil)

	if err != nil {
		slog.Warn("Failed to upgrade connection", "error", err)
		http.Error(w, "Failed to upgrade to websocket connection", http.StatusBadRequest)
		return
	}

	defer c.Close()

	h.mu.Lock()

	ctx, cancel := context.WithCancel(r.Context())

	if _, ok := h.subscribers[rawRoomID]; !ok {
		h.subscribers[rawRoomID] = make(map[*websocket.Conn]context.CancelFunc)
	}
	slog.Info("new client connection", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
	h.subscribers[rawRoomID][c] = cancel
	h.mu.Unlock()

	<-ctx.Done()

	h.mu.Lock()
	delete(h.subscribers[rawRoomID], c)
	h.mu.Unlock()
}

func (h apiHandler) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	type _body struct {
		Theme string `json:"theme"`
	}
	var body _body

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid json", http.StatusBadRequest)
		return
	}

	roomID, err := h.q.InsertRoom(r.Context(), body.Theme)
	if err != nil {
		slog.Error("Failed to insert room", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, err := json.Marshal(response{ID: roomID.String()})
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
}

func (h apiHandler) handleGetRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := h.q.GetRooms(r.Context())
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(rooms)
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
}

func (h apiHandler) handleGetRoomMesssages(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")

	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}

	messages, err := h.q.GetRoomMessages(r.Context(), roomID)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(messages)
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
}

func (h apiHandler) handleCreateRoomMesssages(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	roomID, err := uuid.Parse(rawRoomID)

	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}

	_, err = h.q.GetRoom(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Room not found", http.StatusBadRequest)
			return
		}

		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type _body struct {
		Message string `json:"message"`
	}
	var body _body

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid json", http.StatusBadRequest)
		return
	}

	messageID, err := h.q.InsertMessage(r.Context(), pgstore.InsertMessageParams{RoomID: roomID, Message: body.Message})
	if err != nil {
		slog.Error("Failed to insert message", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, err := json.Marshal(response{ID: messageID.String()})
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	go h.notifyClients(MessageWS{
		Kind:   MessageKindMessageCreated,
		RoomID: rawRoomID,
		Value: MessageMessageCreated{
			ID:      messageID.String(),
			Message: body.Message,
		},
	})
}

func (h apiHandler) handleGetRoomMessage(w http.ResponseWriter, r *http.Request) {
	rawMessageID := chi.URLParam(r, "message_id")

	messageID, err := uuid.Parse(rawMessageID)
	if err != nil {
		http.Error(w, "Invalid message ID", http.StatusBadRequest)
		return
	}

	message, err := h.q.GetMessage(r.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Message not found", http.StatusBadRequest)
			return
		}
		slog.Error("Failed to GetMessage", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(Message{
		ID:            message.ID.String(),
		RoomID:        message.RoomID.String(),
		Message:       message.Message,
		ReactionCount: message.ReactionCount,
		Answered:      message.Answered,
	})
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
}

func (h apiHandler) handleReactToMessage(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	_, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}

	rawMessageID := chi.URLParam(r, "message_id")
	messageID, err := uuid.Parse(rawMessageID)
	if err != nil {
		http.Error(w, "Invalid message ID", http.StatusBadRequest)
		return
	}

	reaction_count, err := h.q.ReactToMessage(r.Context(), messageID)
	if err != nil {
		slog.Error("Failed to react message", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		Count int64 `json:"count"`
	}

	data, err := json.Marshal(response{Count: reaction_count})
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	go h.notifyClients(MessageWS{
		Kind:   MessageKindMessageReacted,
		RoomID: rawRoomID,
		Value: MessageMessageCreated{
			ID:    messageID.String(),
			Count: reaction_count,
		},
	})
}

func (h apiHandler) handleRemoveReactFromMessage(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	_, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}

	rawMessageID := chi.URLParam(r, "message_id")
	messageID, err := uuid.Parse(rawMessageID)
	if err != nil {
		http.Error(w, "Invalid message ID", http.StatusBadRequest)
		return
	}

	message, err := h.q.GetMessage(r.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Message not found", http.StatusBadRequest)
			return
		}

		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	reaction_count, err := h.q.RemoveReactionFromMessage(r.Context(), messageID)
	if err != nil {
		slog.Error("Failed to remove reaction from message", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ReactionCount int64 `json:"reaction_count"`
	}

	data, err := json.Marshal(response{ReactionCount: reaction_count})
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	go h.notifyClients(MessageWS{
		Kind:   MessageKindMessageRemoveReact,
		RoomID: rawRoomID,
		Value: MessageMessageCreated{
			ID:      messageID.String(),
			Message: message.Message,
		},
	})
}

func (h apiHandler) handleMarkMessageAsAnswered(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	_, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}

	rawMessageID := chi.URLParam(r, "message_id")
	messageID, err := uuid.Parse(rawMessageID)
	if err != nil {
		http.Error(w, "Invalid message ID", http.StatusBadRequest)
		return
	}

	message, err := h.q.GetMessage(r.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Message not found", http.StatusBadRequest)
			return
		}

		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	err = h.q.MarkMessageAsAnswered(r.Context(), messageID)
	if err != nil {
		slog.Error("Failed to remove reaction from message", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		Message string `json:"message"`
	}

	data, err := json.Marshal(response{Message: "message marked as answered"})
	if err != nil {
		slog.Error("Failed to Marshal", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		slog.Error("Failed to Write", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	go h.notifyClients(MessageWS{
		Kind:   MessageKindMessageAnswered,
		RoomID: rawRoomID,
		Value: MessageMessageCreated{
			ID:      messageID.String(),
			Message: message.Message,
		},
	})
}

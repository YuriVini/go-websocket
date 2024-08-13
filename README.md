# AMA - ASK ME ANYTHING

AMA is a Web and Mobile application designed to help people in their questions. Therefore, this application offers users the possibility of registering a room, being able to add questions, reactions and mark as answered. The objective of backend it was to create a connection with website through websocket to get results in real time.

### :computer: Technologies
This project was made using the follow technologies:

* [GOlang](https://go.dev)      
* [Gorilla/websocket](https://pkg.go.dev/github.com/gorilla/websocket)      
* [Docker](https://www.docker.com)
* [Go-Chi](https://github.com/go-chi)
* [Tern](https://github.com/jackc/tern)

### :construction_worker: How to run
```bash
# Clone Repository
$ git clone git@github.com:YuriVini/go-websocket.git
```
### 📦 Run API

```bash
# Go to server folder
$ cd go-websocket

# Install Dependencies
$ go mod tidy

# Fill the .env file
DATABASE_PORT=""
DATABASE_NAME=""
DATABASE_USER=""
DATABASE_PASSWORD=""
DATABASE_HOST=""

# Run Aplication
$ go run cmd/websocket/main.go
```
Access API at http://localhost:XXXX and start your websocket connection at ws://localhost:XXXX

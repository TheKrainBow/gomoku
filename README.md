# Gomoku

## Overview
This project is a Go-based web Gomoku application:
- Go backend (game logic + API/WebSocket)
- React frontend (Vite)
- Nginx reverse proxy

The game supports captures, capture win conditions, forbidden double-three, and alignment wins with capture-break checks.

## Requirements
- Docker
- Docker Compose

## Quick Start
```bash
./gomoku
# or
make start
```

Open the app at `http://localhost`.

## Lifecycle Commands
```bash
make start
make stop
make restart
make clean
make fclean
```

## Project Structure
- `backend/` Go server and game engine
- `frontend/` React UI
- `nginx/` reverse proxy config
- `docker-compose.yml` stack definition
- `gomoku` launcher script

## Backend (Go)
- Entrypoint: `backend/main.go`
- Internal listening port: `:8080`
- Runtime config defaults: `backend/config.go`

## Optional Local Development (No Docker)
- Backend:
  ```bash
  cd backend
  go run .
  ```
- Frontend:
  ```bash
  cd frontend
  npm install
  npm run dev
  ```

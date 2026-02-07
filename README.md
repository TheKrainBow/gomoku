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

## AI Trainer Container (Standalone)
The AI trainer is a separate container (not in compose) that loops:
1. start AI vs AI game
2. wait for game end
3. wait for analyze queue to become empty
4. repeat

It logs to `/logs/AITrainer.log`, counts boards sent for analysis, and stops when:
- backend TT cache is full
- a full game generated zero new boards

Build:
```bash
docker build -t gomoku-ai-trainer ./ai-trainer
```

Run (uses backend through host port 8080):
```bash
docker run -d --name gomoku-ai-trainer \
  --add-host host.docker.internal:host-gateway \
  -e BACKEND_URL=http://host.docker.internal:8080 \
  -v "$(pwd)/logs:/logs" \
  gomoku-ai-trainer
```

Stop/remove:
```bash
docker rm -f gomoku-ai-trainer
```

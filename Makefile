LAUNCHER := ./gomoku
TRAINER_IMAGE := gomoku-ai-trainer
TRAINER_CONTAINER := gomoku-ai-trainer
TRAINER_BACKEND_URL ?= http://host.docker.internal:8080
TRAINER_API_BASE ?= http://localhost:8080/api/trainer
TRAINER_NETWORK ?= gomoku_gomoku-net
TRAINER_POLL_INTERVAL_MS ?= 2000
HEURISTIC_MATCHES_PER_ROUND ?= 1
HEURISTIC_MUTATION_STRENGTH ?= 0.08
HEURISTIC_GAME_TIMEOUT_SEC ?= 180
TRAINER_AI_TIME_BUDGET_MS ?= 800
TRAINER_API_ADDR ?= :8090
TRAINER_AUTOSTART_MODE ?=
HEURISTIC_POPULATION_SIZE ?= 8
HEURISTIC_ELITE_COUNT ?= 2
HEURISTIC_HISTORICAL_POOL_SIZE ?= 4
HEURISTIC_TRAINING_OPENINGS ?= 6
HEURISTIC_VALIDATION_OPENINGS ?= 4
HEURISTIC_OPENING_PLIES ?= 4
HEURISTIC_ELO_K ?= 20
HEURISTIC_VALIDATION_PASS_RATE ?= 0.52

all: $(LAUNCHER)

$(LAUNCHER):
	@printf '%s\n' '#!/bin/sh' 'set -e' 'if docker compose version >/dev/null 2>&1; then' '  exec docker compose up -d --build' 'elif command -v docker-compose >/dev/null 2>&1; then' '  exec docker-compose up -d --build' 'else' '  echo "Error: Docker Compose is not available."' '  exit 1' 'fi' > $(LAUNCHER)
	@chmod +x $(LAUNCHER)

stop:
	@if docker compose version >/dev/null 2>&1; then \
		docker compose down; \
	elif command -v docker-compose >/dev/null 2>&1; then \
		docker-compose down; \
	else \
		echo "Error: Docker Compose is not available."; \
		exit 1; \
	fi
	@rm -f $(LAUNCHER)

re:
	@$(MAKE) stop
	@$(MAKE) all

clean:
	@$(MAKE) stop

fclean:
	@if docker compose version >/dev/null 2>&1; then \
		docker compose down --volumes --rmi local --remove-orphans; \
	elif command -v docker-compose >/dev/null 2>&1; then \
		docker-compose down --volumes --rmi local --remove-orphans; \
	else \
		echo "Error: Docker Compose is not available."; \
		exit 1; \
	fi
	@rm -f $(LAUNCHER)

trainer-start: trainer-start-heuristic

trainer-build:
	@docker build -t $(TRAINER_IMAGE) ./ai-trainer

trainer-start-service:
	@curl -sS -X POST "$(TRAINER_API_BASE)/start" \
		-H "Content-Type: application/json" \
		-d '{"mode":"$(if $(TRAINER_AUTOSTART_MODE),$(TRAINER_AUTOSTART_MODE),heuristic)"}'

trainer-start-cache:
	@curl -sS -X POST "$(TRAINER_API_BASE)/start" \
		-H "Content-Type: application/json" \
		-d '{"mode":"cache"}'

trainer-start-heuristic:
	@curl -sS -X POST "$(TRAINER_API_BASE)/start" \
		-H "Content-Type: application/json" \
		-d '{"mode":"heuristic"}'

trainer-stop:
	@curl -sS -X POST "$(TRAINER_API_BASE)/stop"

trainer-logs:
	@docker logs -f $(TRAINER_CONTAINER)

trainer-status:
	@curl -sS "$(TRAINER_API_BASE)/status"

.PHONY: all stop re clean fclean trainer-build trainer-start trainer-start-service trainer-start-cache trainer-start-heuristic trainer-stop trainer-logs trainer-status

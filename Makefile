LAUNCHER := ./gomoku
TRAINER_IMAGE := gomoku-ai-trainer
TRAINER_CONTAINER := gomoku-ai-trainer
TRAINER_BACKEND_URL ?= http://host.docker.internal:8080

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

trainer-build:
	@docker build -t $(TRAINER_IMAGE) ./ai-trainer

trainer-start: trainer-build
	@mkdir -p ./logs
	@docker rm -f $(TRAINER_CONTAINER) >/dev/null 2>&1 || true
	@docker run -d --name $(TRAINER_CONTAINER) \
		--add-host host.docker.internal:host-gateway \
		-e BACKEND_URL=$(TRAINER_BACKEND_URL) \
		-v "$(PWD)/logs:/logs" \
		$(TRAINER_IMAGE)

trainer-stop:
	@docker rm -f $(TRAINER_CONTAINER) >/dev/null 2>&1 || true

trainer-logs:
	@docker logs -f $(TRAINER_CONTAINER)

trainer-status:
	@docker ps -a --filter "name=$(TRAINER_CONTAINER)"

.PHONY: all stop re clean fclean trainer-build trainer-start trainer-stop trainer-logs trainer-status

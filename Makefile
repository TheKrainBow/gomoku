LAUNCHER := ./gomoku

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

.PHONY: all stop re clean fclean

LAUNCHER := ./gomoku

all: start

start:
	$(LAUNCHER)

stop:
	@if docker compose version >/dev/null 2>&1; then \
		docker compose down; \
	elif command -v docker-compose >/dev/null 2>&1; then \
		docker-compose down; \
	else \
		echo "Error: Docker Compose is not available."; \
		exit 1; \
	fi

re:
	@$(MAKE) stop
	@$(MAKE) start

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

.PHONY: all start stop re clean fclean

COMPOSE ?= docker compose

.DEFAULT_GOAL := help

.PHONY: help setup run start logs stop clean

help:
	@echo "Targets:"
	@echo "  make setup   Create .env from .env.example without overwriting it"
	@echo "  make run     Build and run the demo in the foreground"
	@echo "  make start   Build and run the demo in the background"
	@echo "  make logs    Follow agentgo and chat-ui logs"
	@echo "  make stop    Stop and remove the demo containers"
	@echo "  make clean   Stop, remove containers, and delete local demo volumes"

setup:
	@if [ -f .env ]; then \
		echo ".env already exists; no changes were made."; \
	else \
		cp .env.example .env; \
		echo "Created .env. Set AGENTGO_LLM_API_KEY before running."; \
	fi

run:
	@if [ ! -f .env ]; then \
		echo ".env not found. Run 'make setup' first." >&2; \
		exit 1; \
	fi
	$(COMPOSE) up --build

start:
	@if [ ! -f .env ]; then \
		echo ".env not found. Run 'make setup' first." >&2; \
		exit 1; \
	fi
	$(COMPOSE) up --build -d

logs:
	$(COMPOSE) logs --follow

stop:
	$(COMPOSE) down

clean:
	$(COMPOSE) down --volumes --remove-orphans

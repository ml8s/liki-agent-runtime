COMPOSE ?= docker compose
NODE_VERSION := $(shell cat ../../.nvmrc 2>/dev/null || echo 24.14.1)
NPM_FROM_PATH := $(shell command -v npm 2>/dev/null)
NPM ?= $(if $(NPM_FROM_PATH),$(NPM_FROM_PATH),$(HOME)/.nvm/versions/node/v$(NODE_VERSION)/bin/npm)
export PATH := $(dir $(NPM)):$(PATH)

.DEFAULT_GOAL := help

.PHONY: help check test setup run start logs stop clean

help:
	@echo "Targets:"
	@echo "  make check   Check chat UI JavaScript and contracts"
	@echo "  make test    Run chat UI tests"
	@echo "  make setup   Create .env from .env.example without overwriting it"
	@echo "  make run     Build and run the chat demo in the foreground"
	@echo "  make start   Build and run the chat demo in the background"
	@echo "  make logs    Follow agentgo and chat UI logs"
	@echo "  make stop    Stop and remove the demo containers"
	@echo "  make clean   Stop, remove containers, and delete local demo volumes"

check:
	$(NPM) --prefix ui run check

test: check
	$(NPM) --prefix ui test

setup:
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "Created .env. Set AGENTGO_LLM_API_KEY before running."; \
	else \
		echo ".env already exists; no changes were made."; \
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

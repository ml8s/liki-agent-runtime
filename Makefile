# 统一语义（四仓一致）：
#   make lint   静态检查聚合
#   make check  纯静态契约检查（lint+格式+vet+validate；不跑测试、不写盘）
#   make test   本仓全部测试（单元/集成；不含 e2e）
#   make smoke  本仓镜像启动 + operational/public endpoint 冒烟
#   make build  本仓产物（liki-agents 二进制；镜像由 CI release 构建）
#   make image  本地镜像（调试用，非发布路径）
#   make e2e    系统 E2E 聚合（liki-deploy 编排）

GO ?= go
NPM ?= npm
NODE_BIN := $(HOME)/.nvm/versions/node/v$(shell cat .nvmrc)/bin
PYTHON_BIN := $(HOME)/app/python3.12.14/bin
export PATH := $(PYTHON_BIN):$(HOME)/app/go/bin:$(HOME)/app/go-tools/bin:$(HOME)/go/bin:$(NODE_BIN):$(PATH)
export GOTOOLCHAIN ?= go1.26.9
VERSION ?= $(shell git describe --tags --always --dirty --match "v*" 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOPROXY ?= https://goproxy.cn,direct
LDFLAGS ?= -s -w -X github.com/ml8s/liki-agents/internal/platform/buildinfo.Version=$(VERSION) -X github.com/ml8s/liki-agents/internal/platform/buildinfo.Commit=$(COMMIT) -X github.com/ml8s/liki-agents/internal/platform/buildinfo.BuildTime=$(BUILD_TIME)

TEST_PACKAGES ?= ./...
COVER_PROFILE ?= $(CURDIR)/coverage.out
COVER_MIN ?= 75
COMPOSE_ENV_FILE := $(if $(wildcard .env),--env-file .env)

.PHONY: help fmt fmt-check vet lint-engine lint-readme test test-coverage check check-actions test-db db-backup db-verify build run dev dev-down validate gate image smoke
help:
	@echo "make check    - static checks: docs, formatting, lint, vet, and artifact"
	@echo "make lint-readme - lint and test developer documentation"
	@echo "make test     - run isolated tests (no deployed Engine dependency)"
	@echo "make test-coverage - run tests and enforce the coverage floor"
	@echo "make test-db  - run SQLite persistence tests"
	@echo "make smoke    - smoke the local liki-agents image"
	@echo "make db-backup - create an online SQLite backup"
	@echo "make db-verify - verify the latest SQLite backup"
	@echo "make build    - build ./bin/liki-agents"
	@echo "make image    - build the liki-agents Docker image locally (debug; CI builds for release)"
	@echo "make dev      - run the development Agent and MCP containers in the foreground"
	@echo "make dev-down - remove the development Agent workload"
	@echo "make validate - validate the AgentDeployment artifact without starting the server"

fmt:
	golangci-lint fmt ./...

fmt-check:
	golangci-lint fmt --diff ./...

lint-readme:
	$(NPM) run lint:docs
	$(NPM) run test:docs

lint-engine:
	golangci-lint run ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test -race -count=1 -coverprofile=$(COVER_PROFILE) $(TEST_PACKAGES)

test-coverage: test
	@COV=$$($(GO) tool cover -func=$(COVER_PROFILE) | tail -1 | awk '{print $$3}' | sed 's/%//'); \
	echo "coverage: $$COV% (floor $(COVER_MIN)%)"; \
	if awk -v c="$$COV" -v m="$(COVER_MIN)" 'BEGIN {exit !(c < m)}'; then \
		echo "❌ coverage $$COV% < $(COVER_MIN)%" >&2; exit 1; \
	fi

check: lint-readme fmt-check lint-engine vet validate check-actions check-config-surface

test-db:
	$(GO) test -race -count=1 ./internal/audit/sqlite/...

db-backup:
	@bash -eu -o pipefail -c '\
		DB_PATH="$${LIKI_AGENTS_DB_PATH:-./data/liki-agents-audit.db}"; \
		BACKUP_DIR="$${LIKI_BACKUP_DIR:-./backups}"; \
		mkdir -p "$$BACKUP_DIR"; \
		OUT="$$BACKUP_DIR/liki-agents-audit-$$(date +%Y%m%d-%H%M%S).db"; \
		sqlite3 "$$DB_PATH" ".backup '"'"'$$OUT'"'"'"; \
		echo "$$OUT"'

db-verify:
	@bash -eu -o pipefail -c '\
		LATEST="$$(ls -1t "$${LIKI_BACKUP_DIR:-./backups}"/liki-agents-*.db 2>/dev/null | head -1)"; \
		if [ -z "$$LATEST" ]; then echo "no backup found" >&2; exit 1; fi; \
		sqlite3 "$$LATEST" "PRAGMA integrity_check;"'

build: ## Build the local liki-agents binary. 镜像由 CI release 构建，非本地。
	$(GO) build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/liki-agents ./cmd/liki-agents

# ── 镜像（本地调试用，非发布路径）──
# 生产镜像由 CI release 构建并推送 GHCR；此目标仅用于本地构建/调试。
IMAGE_REGISTRY ?= ghcr.io/ml8s
IMAGE_TAG ?= dev

image: ## Build liki-agents image locally (debug; production uses CI release)
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg GOPROXY=$(GOPROXY) \
		-t $(IMAGE_REGISTRY)/liki-agents:$(IMAGE_TAG) .

smoke: image ## Smoke the local runtime image with the development deployment
	IMAGE_TAG=$(IMAGE_TAG) scripts/smoke-image.sh

run:
	./scripts/dev.sh

gate: check test-coverage smoke

check-actions: ## Reject mutable reusable workflow/action references
	python3 scripts/check_actions_pinned.py

check-config-surface: ## Fail when config keys drift across code, docs, and deployment
	python3 scripts/check_config_surface.py

validate:
	@$(GO) run ./cmd/liki-agents validate -deployment $(if $(LIKI_AGENTS_DEPLOYMENT_FILE),$(LIKI_AGENTS_DEPLOYMENT_FILE),./dev/agent-deployment/deployment.json)

dev-down:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) -f dev/docker-compose.yml down --remove-orphans

dev:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) --profile mcp -f dev/docker-compose.yml up --build --abort-on-container-exit

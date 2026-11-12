# 统一语义：
#   make check  静态检查（docs、format、lint、vet、artifact、action pinning）
#   make test   本仓全部测试（unit/integration，race + coverage floor）
#   make gate   check + test

GO ?= go
PYTHON ?= python3
NODE_BIN := $(HOME)/.nvm/versions/node/v$(shell cat .nvmrc)/bin
NPM_FROM_PATH := $(shell command -v npm 2>/dev/null)
NPM_FALLBACK := $(NODE_BIN)/npm
NPM ?= $(if $(NPM_FROM_PATH),$(NPM_FROM_PATH),$(NPM_FALLBACK))
NPM_BIN_DIR := $(if $(NPM_FROM_PATH),$(dir $(NPM_FROM_PATH)),$(NODE_BIN))
export PATH := $(NPM_BIN_DIR):$(PATH)
export GOTOOLCHAIN := auto
# A stale host GOROOT can force old standard-library objects onto the new toolchain.
unexport GOROOT
# Official user defaults are single-sourced by the Go command; stale host
# developer overrides must not redirect the build to retired directories.
unexport GOPATH GOBIN GOMODCACHE GOCACHE GOLANGCI_LINT_CACHE
GIT_SHA ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOPROXY ?= https://goproxy.cn,direct
LDFLAGS ?= -s -w -X github.com/ml8s/agent-runtime/internal/platform/buildinfo.BuildID=$(GIT_SHA) -X github.com/ml8s/agent-runtime/internal/platform/buildinfo.Commit=$(COMMIT) -X github.com/ml8s/agent-runtime/internal/platform/buildinfo.BuildTime=$(BUILD_TIME)

TEST_PACKAGES ?= ./...
BENCH_PACKAGES ?= ./internal/audit/sqlite/... ./internal/agent/...
COVER_PROFILE ?= $(CURDIR)/coverage.out
COVER_MIN ?= 75
COMPOSE_ENV_FILE := $(if $(wildcard .env),--env-file .env)

.PHONY: help check test conformance gate security-go setup-hooks bench build run dev dev-down image smoke
help:
	@echo "make check    - static checks: docs, Go lint/formatters, vet, artifact, and action pinning"
	@echo "make test     - run tests with race detection and the coverage floor"
	@echo "make conformance - run official-client protocol conformance checks"
	@echo "make bench    - run focused performance benchmarks"
	@echo "make gate     - run the full pre-push source gate"
	@echo "make setup-hooks - install the repository pre-push gate"
	@echo "make smoke    - smoke the local agent-runtime image"
	@echo "make build    - build ./bin/agent-runtime"
	@echo "make image    - build a local image (tag from git)"
	@echo "make dev      - run the development Agent and MCP containers in the foreground"
	@echo "make dev-down - remove the development Agent workload"

test: ## Run tests and enforce the coverage floor
	$(GO) test -race -count=1 -coverprofile=$(COVER_PROFILE) $(TEST_PACKAGES)
	@# Coverage policy is part of `make test`; there is no separate coverage target.
	@COV=$$($(GO) tool cover -func=$(COVER_PROFILE) | tail -1 | awk '{print $$3}' | sed 's/%//'); \
		echo "coverage: $$COV% (floor $(COVER_MIN)%)"; \
		if awk -v c="$$COV" -v m="$(COVER_MIN)" 'BEGIN {exit !(c < m)}'; then \
			echo "❌ coverage $$COV% < $(COVER_MIN)%" >&2; exit 1; \
		fi

bench: ## Run focused performance benchmarks with allocation stats
	$(GO) test -run '^$$' -bench . -benchmem -count=1 $(BENCH_PACKAGES)

check: ## Run static checks.
	$(NPM) run lint:docs
	$(PYTHON) scripts/check_docs_paths.py
	$(PYTHON) scripts/check_generic_boundary.py
	$(PYTHON) scripts/check-lint-toolchain.py
	scripts/lint.sh run ./...
	$(GO) vet ./...
	$(GO) run ./cmd/agent-runtime validate -deployment $(if $(AGENT_RUNTIME_DEPLOYMENT_FILE),$(AGENT_RUNTIME_DEPLOYMENT_FILE),./dev/agent-deployment/deployment.json)
	$(PYTHON) scripts/check_actions_pinned.py

build: ## Build the local agent-runtime binary.
	$(GO) build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/agent-runtime ./cmd/agent-runtime

# ── 镜像 ──
# make image/smoke 只构建本地镜像；部署由 deployment system 按 sha 组装。
IMAGE_REGISTRY ?= agent-runtime-local
IMAGE_TAG ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo local)

image: ## Build a local image for development
	docker build --build-arg GIT_SHA=$(GIT_SHA) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg GOPROXY=$(GOPROXY) \
		-t $(IMAGE_REGISTRY)/agent-runtime:$(IMAGE_TAG) .

smoke: image ## Smoke the local runtime image with the development deployment
	IMAGE_TAG=$(IMAGE_TAG) scripts/smoke-image.sh

run:
	./scripts/dev.sh

conformance: ## Run official-client protocol conformance checks
	$(GO) test -race -count=1 ./internal/protocol/... ./cmd/mcp-fixture

security-go: ## Run Go vulnerability scan
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

gate: check test

setup-hooks: ## Configure Git to run the repository gate before push
	git config core.hooksPath .githooks
	test -x .githooks/pre-commit
	test -x .githooks/pre-push
	@echo "✓ Git hooks installed; pre-push now runs make gate"

dev-down:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) -f dev/docker-compose.yml down --remove-orphans

dev:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) --profile mcp -f dev/docker-compose.yml up --build --abort-on-container-exit

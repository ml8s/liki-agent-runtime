# 统一语义：
#   make check  静态检查（docs、format、lint、vet、artifact、action pinning）
#   make test   本仓全部测试（unit/integration，race + coverage floor）
#   make gate   check + test

RELEASE_VERSION ?=
export RELEASE_VERSION

GO ?= go
NPM ?= npm
NODE_BIN := $(HOME)/.nvm/versions/node/v$(shell cat .nvmrc)/bin
PYTHON_BIN := $(HOME)/app/python3.12.14/bin
GOPATH := $(HOME)/app/go-tools
GOBIN := $(GOPATH)/bin
GOMODCACHE := $(GOPATH)/pkg/mod
GOCACHE := $(GOPATH)/build-cache
GOLANGCI_LINT_CACHE := $(GOPATH)/golangci-lint-cache
export PATH := $(PYTHON_BIN):$(HOME)/app/go/bin:$(GOBIN):$(NODE_BIN):$(PATH)
export GOPATH GOBIN GOMODCACHE GOCACHE GOLANGCI_LINT_CACHE
export GOTOOLCHAIN ?= go1.26.10
VERSION ?= $(shell git describe --tags --always --dirty --match "v*" 2>/dev/null || echo local)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOPROXY ?= https://goproxy.cn,direct
LDFLAGS ?= -s -w -X github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo.Version=$(VERSION) -X github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo.Commit=$(COMMIT) -X github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo.BuildTime=$(BUILD_TIME)

TEST_PACKAGES ?= ./...
BENCH_PACKAGES ?= ./internal/audit/sqlite/... ./internal/agent/...
COVER_PROFILE ?= $(CURDIR)/coverage.out
COVER_MIN ?= 75
COMPOSE_ENV_FILE := $(if $(wildcard .env),--env-file .env)

.PHONY: help check test gate bench build run dev dev-down image smoke
help:
	@echo "make check    - static checks: docs, formatting, lint, vet, artifact, and action pinning"
	@echo "make test     - run tests with race detection and the coverage floor"
	@echo "make bench    - run focused performance benchmarks"
	@echo "make gate     - run the full pre-push source gate"
	@echo "make smoke    - smoke the local liki-agent-runtime image"
	@echo "make build    - build ./bin/liki-agent-runtime"
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
	golangci-lint fmt --diff ./...
	golangci-lint run ./...
	$(GO) vet ./...
	$(GO) run ./cmd/liki-agent-runtime validate -deployment $(if $(LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE),$(LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE),./dev/agent-deployment/deployment.json)
	python3 scripts/check_actions_pinned.py

build: ## Build the local liki-agent-runtime binary.
	$(GO) build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/liki-agent-runtime ./cmd/liki-agent-runtime

# ── 镜像 ──
# make image/smoke 只构建本地镜像；部署由 liki-deploy 按 sha 组装。
IMAGE_REGISTRY ?= liki-local
IMAGE_TAG ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo local)

image: ## Build a local image for development
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg GOPROXY=$(GOPROXY) \
		-t $(IMAGE_REGISTRY)/liki-agent-runtime:$(IMAGE_TAG) .

smoke: image ## Smoke the local runtime image with the development deployment
	IMAGE_TAG=$(IMAGE_TAG) scripts/smoke-image.sh

run:
	./scripts/dev.sh

gate: check test

dev-down:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) -f dev/docker-compose.yml down --remove-orphans

dev:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) --profile mcp -f dev/docker-compose.yml up --build --abort-on-container-exit

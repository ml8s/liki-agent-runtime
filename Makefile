# 统一语义（四仓一致）：
#   make lint   静态检查聚合
#   make check  纯静态检查（lint+格式+vet+validate；不跑测试、不写盘）
#   make test   本仓全部测试（单元/集成；不含 e2e）
#   make smoke  本仓镜像启动 + operational/public endpoint 冒烟
#   make build  本仓产物（liki-agent-runtime 二进制）
#   make e2e    系统 E2E 聚合（liki-deploy 编排）

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
COVER_PROFILE ?= $(CURDIR)/coverage.out
COVER_MIN ?= 75
COMPOSE_ENV_FILE := $(if $(wildcard .env),--env-file .env)

.PHONY: help fmt fmt-check vet lint-engine lint-readme test test-coverage check check-actions test-coverage-report build run dev dev-down validate gate image smoke
help:
	@echo "make check    - static checks: docs, formatting, lint, vet, and artifact"
	@echo "make lint-readme - lint and test developer documentation"
	@echo "make test     - run isolated tests (no deployed Engine dependency)"
	@echo "make test-coverage - run tests and enforce the coverage floor"
	@echo "make gate     - run the full pre-push source gate"
	@echo "make smoke    - smoke the local liki-agent-runtime image"
	@echo "make build    - build ./bin/liki-agent-runtime"
	@echo "make image    - build a local image (tag from git)"
	@echo "make dev      - run the development Agent and MCP containers in the foreground"
	@echo "make dev-down - remove the development Agent workload"
	@echo "make validate - validate the AgentDeployment artifact without starting the server"

fmt:
	golangci-lint fmt ./...

fmt-check:
	golangci-lint fmt --diff ./...

lint-readme:
	$(NPM) run lint:docs

lint-engine:
	golangci-lint run ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test -race -count=1 -coverprofile=$(COVER_PROFILE) $(TEST_PACKAGES)

test-coverage: test ## Run tests and enforce the coverage floor
	@COV=$$($(GO) tool cover -func=$(COVER_PROFILE) | tail -1 | awk '{print $$3}' | sed 's/%//'); \
		echo "coverage: $$COV% (floor $(COVER_MIN)%)"; \
		if awk -v c="$$COV" -v m="$(COVER_MIN)" 'BEGIN {exit !(c < m)}'; then \
			echo "❌ coverage $$COV% < $(COVER_MIN)%" >&2; exit 1; \
		fi

test-coverage-report: test-coverage ## Run tests, enforce coverage, and emit the HTML report
	$(GO) tool cover -html=$(COVER_PROFILE) -o coverage.html

check: lint-readme fmt-check lint-engine vet validate check-actions

build: ## Build the local liki-agent-runtime binary.
	$(GO) build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/liki-agent-runtime ./cmd/liki-agent-runtime

# ── 镜像 ──
# make image/smoke 只构建本地镜像；部署由 liki-deploy 按 sha 组装。
IMAGE_REGISTRY ?= liki-local
IMAGE_TAG ?= local

image: ## Build a local image for development
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg GOPROXY=$(GOPROXY) \
		-t $(IMAGE_REGISTRY)/liki-agent-runtime:$(IMAGE_TAG) .

smoke: image ## Smoke the local runtime image with the development deployment
	IMAGE_TAG=$(IMAGE_TAG) scripts/smoke-image.sh

run:
	./scripts/dev.sh

gate: check test-coverage

check-actions: ## Reject mutable reusable workflow/action references
	python3 scripts/check_actions_pinned.py

validate:
	@$(GO) run ./cmd/liki-agent-runtime validate -deployment $(if $(LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE),$(LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE),./dev/agent-deployment/deployment.json)

dev-down:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) -f dev/docker-compose.yml down --remove-orphans

dev:
	docker compose --project-directory . $(COMPOSE_ENV_FILE) --profile mcp -f dev/docker-compose.yml up --build --abort-on-container-exit

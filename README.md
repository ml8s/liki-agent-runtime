# Liki Agents

`liki-agent-runtime` is a generic multi-agent runtime for externally defined
`AgentDeployment` artifacts. It executes an ADK Agent tree, orchestrates model
calls and MCP tools, and exposes standard machine and browser protocols.

```text
machine client ──A2A JSON-RPC──┐
                               ↓
liki-web ──AG-UI SSE→ liki-agent-runtime ──MCP→ external tools
```

The runtime is domain-neutral: deployments supply instructions, output schemas,
delegation topology, and tool allowlists; `liki-agent-runtime` provides
execution, protocol adaptation, security, evidence, and observability.

## Install

Requires Go 1.26+, Node.js 24.14.1 (see `.nvmrc`), Docker Compose, and
golangci-lint 2.x. Clone the repository and run `make build`; the binary is
written to `bin/liki-agent-runtime`.

## Quick start

Copy `.env.example` to `.env`; set `LIKI_LLM_API_KEY` and every endpoint
variable named by `mcpServers`. Local development may leave the internal token
empty to disable bearer authentication. Then run:

```bash
make run
curl -sS http://127.0.0.1:8085/readyz | jq   # ops listener (LIKI_AGENT_RUNTIME_OPS_ADDR)
```

`make run` starts only the runtime; MCP servers are external dependencies.
`make dev` also starts the MCP fixtures (`liki-engine`/`liki-counsel` images
from the Liki project) and loads `.env`. Browser testing uses
`scripts/dev-chat-ui.sh` at <http://127.0.0.1:8084> (development-only).

## Runtime model

An `AgentDeployment` is the sole source of Agent behavior and topology: each
Agent has an ADK mode, instruction file, optional output schema, and an explicit
`tools.allow`; `mcpServers` binds logical servers to endpoint/token environment
variables; the unique root (no incoming `sub_agents` edge) is the entrypoint and
ADK owns delegation and execution.

Prompts are external files and plain text is the default; typed output declares
a JSON Schema plus a JSON Pointer to the user-facing string. Tool access is
denied unless the tool is listed in the Agent's server-scoped `tools.allow`.

The artifact is validated by this runtime's strict JSON decoder and deployment
invariants; external boundaries follow [`docs/STANDARDS.md`](docs/STANDARDS.md).
Its digest covers the manifest, referenced instruction and schema files, and the
ADK graph semantics.
Audit provenance uses the immutable deployment version and digest already
carried by the artifact. Deployment integrity is owned by the immutable
assembly image digest pinned by the liki/liki-deploy release chain; no
duplicate artifact pin is configured by the environment.

## Protocols

| Consumer | Protocol | Endpoint | Purpose |
|---|---|---|---|
| Machine client | A2A JSON-RPC | `POST /a2a` | discovery, invocation, streaming |
| Browser / gateway | AG-UI SSE | `POST /ag-ui` | chat, tool events, sub-agent lifecycle |
| Machine discovery | A2A Agent Card | `GET /.well-known/agent-card.json` | standard capability metadata |
| Tool server | MCP Streamable HTTP | outbound | deterministic tools |

Operational endpoints are public `/readyz` and `/version`, plus `/metrics`
(token-gated when an internal token is configured). There is no private
application API or ADK Launcher control plane.

## Security boundary

Production protocol calls require the internal service token:

```text
Authorization: Bearer <LIKI_AGENT_RUNTIME_INTERNAL_TOKEN>
```

AG-UI additionally requires `liki-web` to authenticate the browser user and
inject `X-Liki-User-ID`. The header is a browser-only trust signal: it is
adopted on `/ag-ui` only and never on A2A, so a machine client sharing the
internal token cannot spoof a user identity into audit/provenance.

The runtime is never directly public and never authenticates end users, issues
sessions, stores conversations, or trusts unverified identities. Internet-facing
rate limiting is the gateway's job; the internal per-source limiter only resists
token brute force, while `LIKI_MAX_CONCURRENT_RUNS` bounds run concurrency.

Local development may leave `LIKI_AGENT_RUNTIME_INTERNAL_TOKEN` empty, disabling bearer
authentication, while AG-UI still requires `X-Liki-User-ID`. Invalid attempts
are throttled per socket source and return HTTP 429 (`rate_limited`).

MCP access is allowlisted per Agent and server, and evidence never contains
prompts, raw model output, or tool payloads. Outside development the LLM
endpoint is HTTPS-only without embedded credentials.

## Configuration

`.env.example` is the complete configuration surface. Important groups are:

| Prefix / variable | Purpose |
|---|---|
| `LIKI_AGENT_RUNTIME_*` | service address, token, audit DB path, topology, deployment file, ops address (`OPS_ADDR`, default `:8085`), mutual-TLS listener (`TLS_ADDR` + `TLS_CERT`/`TLS_KEY`/`TLS_CLIENT_CA`), run callback (`CALLBACK_URL`/`CALLBACK_SECRET`) |
| `LIKI_MCP_*` | endpoint/token bindings for logical MCP servers (`LIKI_MCP_<NAME>_URL` / `LIKI_MCP_<NAME>_TOKEN`) |
| `LIKI_MAX_CONCURRENT_RUNS` | bounded concurrent executions |
| `LIKI_LLM_*` | OpenAI-compatible client; provider is an audit label, base URL/model/provider required |
| `LIKI_LOG_*` | structured logging |
| `OTEL_*` | standard OpenTelemetry tracing settings |

MCP endpoint values are complete URLs; the gateway may own service prefixes
(`/engine/mcp`, `/counsel/mcp/bazi`) while services expose `/mcp` and
`/mcp/{domain}`. The runtime rewrites no MCP paths and adds no private headers.

Empty `LIKI_LLM_STRUCTURED_OUTPUT` derives the mode from the deployment and
provider; explicit modes fail closed without an output schema. Per-Agent model
settings live in the artifact. Concurrency is bounded to 1–1024; development
alone permits local HTTP LLMs.

## Operations

Use `make run` for the host and `make dev` for Compose; `make dev-down`
removes the workload. The ops surface (`/readyz`, `/version`, `/metrics`)
listens on `LIKI_AGENT_RUNTIME_OPS_ADDR` (default `:8085`) so protocol mutual
TLS never gates healthchecks; `/metrics` requires the service token. Release
images probe `/readyz` via the built-in `healthcheck` subcommand; the runtime
is single-replica (see
[`docs/STANDARDS.md`](docs/STANDARDS.md) for scaling).

## Observability

Prometheus exposes protocol latency, active streams, model calls and tokens,
tool calls and durations, delegation activity, and dependency readiness. Logs
use `log/slog` with stable event names. Distributed tracing uses W3C context
propagation and standard OTLP export; MCP and LLM outbound requests carry the
same trace context. Tracing is disabled unless a standard OTLP endpoint is
configured:

```text
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://otel-collector:4318/v1/traces
```

SQLite stores append-only audit events (metadata, versions, digests, duration,
error codes). On startup the single-process runtime closes interrupted run,
model, tool, and delegation lifecycles with synthetic `runtime_interrupted`
failures, never editing history; records exclude conversation or domain payloads.

## Documentation

| Document | Purpose |
|---|---|
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | runtime architecture and protocol mapping |
| [`docs/DOMAIN.md`](docs/DOMAIN.md) | stable runtime domain invariants |
| [`docs/SYSTEM.md`](docs/SYSTEM.md) | 系统全况与本仓对齐（产什么、谁消费、边界在哪） |
| [`dev/agent-deployment/deployment.json`](dev/agent-deployment/deployment.json) | runtime validation fixture |
| [`SECURITY.md`](SECURITY.md) | security policy and required controls |

## Development

After installing the pinned Node/Go toolchains, run `npm ci` once; Markdown
linting uses the locked local development dependency. Run `make check`,
`make validate`, `make test`, `make gate`, and `make build`.

`make gate` is the pre-push gate. It runs documentation linting, Go static
checks, `golangci-lint`, isolated race tests, the coverage floor, and deployment
validation. It does not call a real model provider or external MCP. CI
additionally runs `govulncheck` and production npm dependency auditing.
Images are built locally (`make image`) with the tag derived from git; CI does
not build or publish images. `liki` consumes the local image as its experts
base, and `liki-deploy` assembles and deploys the set.

### Contributing

Follow [`CONTRIBUTING.md`](CONTRIBUTING.md) and run `make check`/`make gate`
before submitting a change.

## Project boundary

`liki-agent-runtime` is not an Agent management control plane. It does not provide
Agent CRUD APIs, deployment registries, tenant management, quota enforcement,
approval workflows, or a management console.

`liki-web` owns users, products, entitlements, quotas, and durable product
conversations. A Liki domain release owns prompts, skills, workflow policy, and
tool semantics; Engine and Counsel own deterministic MCP implementations.

### License

Released under the [MIT License](LICENSE) by ml8s and the Liki Agents contributors.

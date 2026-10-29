# Agent Runtime

`agent-runtime` is a generic multi-agent runtime for externally defined
`AgentDeployment` artifacts. It executes an ADK Agent tree, orchestrates model
calls and MCP tools, and exposes standard machine and browser protocols.

```text
machine client ──A2A JSON-RPC──┐
                               ↓
caller ──AG-UI SSE→ agent-runtime ──MCP→ external tools
```

The runtime is domain-neutral: deployments supply instructions, output schemas,
delegation topology, and tool allowlists; `agent-runtime` provides
execution, protocol adaptation, security, evidence, and observability.

## Install

Requires Go 1.26+, Node.js 24.14.1 (see `.nvmrc`), Docker Compose, and
golangci-lint 2.x. Clone the repository and run `make build`; the binary is
written to `bin/agent-runtime`.

## Quick start

Copy `.env.example` to `.env`; set `AGENT_RUNTIME_LLM_API_KEY` and every
endpoint/token variable named by `mcpServers` (a token variable may be present
but empty). Local development may leave the internal token empty to disable
bearer authentication. Then run:

```bash
make dev
curl -sS http://127.0.0.1:8085/readyz | jq   # ops listener (AGENT_RUNTIME_OPS_ADDR)
```

`make dev` is the supported development path: it mounts the deployment and
skill fixtures, then builds one domain-neutral MCP fixture image and starts `mcp-tools-a` / `mcp-tools-b` instances with `.env`. `make run` is an advanced host-mode
entry point; because the fixture artifact pins `/skills`, use it only with a
host-specific artifact whose skill roots are accessible to that process.
Browser testing uses
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
carried by the artifact. Deployment integrity is owned by deployment system, which
assembles and deploys the images by commit sha; no duplicate artifact pin is
configured by the environment.

## Protocols

| Consumer | Protocol | Endpoint | Purpose |
|---|---|---|---|
| Machine client | A2A JSON-RPC | `POST /a2a` | discovery, invocation, streaming |
| Browser / gateway | AG-UI SSE | `POST /ag-ui` | chat, tool events, sub-agent lifecycle |
| Machine discovery | A2A Agent Card | `GET /.well-known/agent-card.json` | standard capability metadata |
| Tool server | MCP Streamable HTTP | outbound | deterministic tools |

Operational endpoints are public `/readyz` and `/version`, plus `/metrics` and
`/debug/pprof`. Metrics are internal and credential-free; profiling is disabled
unless `AGENT_RUNTIME_PPROF_ENABLED=1`.
There is no private application API or ADK Launcher control plane.

## Security boundary

The default trusted-header mode requires production protocol callers to present
the internal service token:

```text
Authorization: Bearer <AGENT_RUNTIME_PROTOCOL_TOKEN>
```

AG-UI additionally requires `caller` to authenticate the browser user and
inject `X-Agent-User-ID`. The header is a browser-only trust signal: it is
adopted on `/ag-ui` only and never on A2A, so a machine client sharing the
internal token cannot spoof a user identity into audit/provenance.

Set `AGENT_RUNTIME_IDENTITY_SOURCE=oidc` to replace internal bearer + trusted
headers with one standard OIDC ID-token credential. The runtime performs
discovery and verifies signature, issuer, audience, and expiry through
`coreos/go-oidc`; `sub` (or the configured claim) becomes the verified identity
on both AG-UI and A2A.

The transport consumes a pluggable `identity.RequestSource`; the bundled
trusted-header adapter is channel-aware and accepts only headers owned by the
authenticated channel, and the bundled OIDC and certificate-identity adapters
implement the same port without changing protocol handlers.

On the optional mutual-TLS listener, the default
`AGENT_RUNTIME_MTLS_IDENTITY_SOURCE=certificate` maps the identity from the
verified client certificate (first URI SAN, then email SAN, then common name;
certificate organization maps to the tenant). A certificate holder therefore
cannot claim another user through `Remote-User` headers. Deployments whose mTLS
peer is a gateway that has already authenticated the end user may opt into
`AGENT_RUNTIME_MTLS_IDENTITY_SOURCE=header`, which restores the relay-header
contract on that listener; the opt-in requires the TLS listener and documents
that trust assumption explicitly.

The runtime is never directly public and never authenticates end users, issues
sessions, stores conversations, or trusts unverified identities. Internet-facing
rate limiting is the gateway's job; the internal per-source limiter only resists
token brute force, while `AGENT_RUNTIME_MAX_CONCURRENT_RUNS` bounds run concurrency.

Local development may leave `AGENT_RUNTIME_PROTOCOL_TOKEN` empty, disabling bearer
authentication, while AG-UI still requires `X-Agent-User-ID`. Invalid attempts
are throttled per socket source and return HTTP 429 (`rate_limited`).

MCP access is allowlisted per Agent and server, and evidence never contains
prompts, raw model output, or tool payloads. Outside development the LLM
endpoint is HTTPS-only without embedded credentials.

## Configuration

`.env.example` is the complete configuration surface. Important groups are:

| Prefix / variable | Purpose |
|---|---|
| `AGENT_RUNTIME_*` | service address, token, audit DB path, topology, deployment file, ops address (`OPS_ADDR`, default `:8085`), mutual-TLS listener (`TLS_ADDR` + `TLS_CERT`/`TLS_KEY`/`TLS_CLIENT_CA`) |
| `AGENT_RUNTIME_PROTOCOL_TOKEN` / `AGENT_RUNTIME_PPROF_ENABLED` | protocol service credential; explicit opt-in for `/debug/pprof` |
| `AGENT_RUNTIME_MCP_*` | endpoint/token bindings for logical MCP servers (`AGENT_RUNTIME_MCP_<NAME>_URL` / `AGENT_RUNTIME_MCP_<NAME>_TOKEN`) |
| `AGENT_RUNTIME_MAX_CONCURRENT_RUNS` | bounded concurrent executions |
| `AGENT_RUNTIME_LLM_*` | OpenAI-compatible client; provider is an audit label, base URL/model/provider required |
| `AGENT_RUNTIME_LOG_*` | structured logging |
| `AGENT_RUNTIME_IDENTITY_SOURCE` | `trusted_header` (default) or `oidc` |
| `AGENT_RUNTIME_MTLS_IDENTITY_SOURCE` | mutual-TLS identity mapping: `certificate` (default, fail-closed) or explicit `header` gateway-relay opt-in |
| `AGENT_RUNTIME_OIDC_*` | OIDC issuer, audience, and verified claim mapping |
| `AGENT_RUNTIME_A2A_TASK_RETENTION_SECONDS` | optional terminal A2A task retention; unset/zero disables cleanup |
| `AGENT_RUNTIME_ADK_SESSION_RETENTION_SECONDS` | optional idle ADK session retention; unset/zero disables, explicit value must be at least run timeout |
| `OTEL_*` | standard OpenTelemetry tracing settings |

MCP endpoint values are complete URLs; the gateway may own service prefixes
(`/tools-a/mcp`, `/tools-b/mcp`) while services expose `/mcp` and
`/mcp/{domain}`. The runtime rewrites no MCP paths and adds no private headers.

Empty `AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT` derives the mode from the deployment and
provider; explicit modes fail closed without an output schema. Per-Agent model
settings live in the artifact. Concurrency is bounded to 1–1024; development
alone permits local HTTP LLMs.

## Operations

Use `make dev` for Compose; `make dev-down` removes the workload. `make run`
is an advanced host-mode command and requires a host-specific deployment
artifact. The ops surface (`/readyz`, `/version`, `/metrics`) listens on
`AGENT_RUNTIME_OPS_ADDR` (default `:8085`) so protocol mutual TLS never gates
healthchecks; metrics are internal and credential-free, while profiling is an
explicit opt-in. `/readyz` reuses one dependency-check round for 10 seconds
and fails closed, so probes never amplify into outbound MCP discovery storms.
Release images probe `/readyz` via the built-in `healthcheck` subcommand; the
runtime is single-replica (see
[`docs/STANDARDS.md`](docs/STANDARDS.md) for scaling).

## Observability

Prometheus exposes protocol latency, active streams, model calls, tokens,
model-call durations, tool calls and durations, delegation activity, audit
append outcomes, durations, batch sizes and queue depth, MCP catalog cache
outcomes, run rejections, durable record growth, and dependency readiness. Logs use `log/slog`
with stable event names. Distributed tracing uses W3C context propagation and
standard OTLP export; MCP and LLM outbound requests carry the same trace
context. Tracing is disabled unless a standard OTLP endpoint is configured:

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
| [`dev/agent-deployment/deployment.json`](dev/agent-deployment/deployment.json) | runtime validation fixture |
| [`SECURITY.md`](SECURITY.md) | security policy and required controls |

## Development

After installing the pinned Node/Go toolchains, run `npm ci` once; Markdown
linting uses the locked local development dependency. Run `make check`,
`make test`, `make gate`, and `make build`.

`make gate` is the pre-push gate. It runs documentation linting, Go static
checks, race tests, and the coverage floor. `make check` includes deployment
validation. Neither command calls a real model provider or external MCP. CI
additionally runs `govulncheck` and production npm dependency auditing.
Images are built locally (`make image`) with the tag derived from git; CI does
not build or publish images. Callers consume the local image as their runtime base and assemble their own
deployment set.

### Contributing

Follow [`CONTRIBUTING.md`](CONTRIBUTING.md) and run `make check`/`make gate`
before submitting a change.

## Project boundary

`agent-runtime` is not an Agent management control plane. It does not provide
Agent CRUD APIs, deployment registries, tenant management, quota enforcement,
approval workflows, or a management console.

`caller` owns users, products, entitlements, quotas, and durable product
conversations. A domain content repository owns prompts, skills, workflow policy, and
tool semantics; external MCP tool services own deterministic MCP implementations.

### License

Released under the [MIT License](LICENSE) by ml8s and the Agent Runtime contributors.

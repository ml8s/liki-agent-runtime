<p align="center">
  <img src="assets/logo.svg" alt="agentgo logo" width="160">
</p>

# agentgo

**Turn an `AgentDeployment` artifact into a self-hosted, protocol-native agent service — no agent infrastructure to build.**

agentgo is a standalone deployment runtime for agent graphs. You write an
`AgentDeployment` artifact (JSON + prompt files); agentgo runs the agent tree,
orchestrates LLM calls and MCP tools, and serves the result over AG-UI, A2A,
and Agent Card protocols. Your product code never touches Go, agents, or
protocol plumbing.

agentgo is deployment-first, not a chat workspace: callers own users, business
context, durable conversations, and the user experience.

<p align="center">
  [![CI](https://github.com/ml8s/agentgo/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ml8s/agentgo/actions/workflows/ci.yml)
  [![Security scans](https://github.com/ml8s/agentgo/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/ml8s/agentgo/actions/workflows/security.yml)
  [![Release](https://img.shields.io/github/v/release/ml8s/agentgo?style=flat-square)](https://github.com/ml8s/agentgo/releases)
  [![Go version](https://img.shields.io/github/go-mod/go-version/ml8s/agentgo?style=flat-square)](https://github.com/ml8s/agentgo/blob/main/go.mod)
  [![License](https://img.shields.io/github/license/ml8s/agentgo?style=flat-square)](./LICENSE)
  [![Go reference](https://pkg.go.dev/badge/github.com/ml8s/agentgo.svg)](https://pkg.go.dev/github.com/ml8s/agentgo)
</p>

<p align="center">
  <img alt="No Node.js runtime" src="https://img.shields.io/badge/runtime-no_Node.js-16A34A?style=flat-square">
  <img alt="CI Python" src="https://img.shields.io/badge/CI_Python-3.12.14-3776AB?style=flat-square&logo=python&logoColor=white">
  <img alt="Docker Compose" src="https://img.shields.io/badge/Docker_Compose-ready-2496ED?style=flat-square&logo=docker&logoColor=white">
  <img alt="A2A SDK" src="https://img.shields.io/badge/A2A_SDK-v2.9.0-4F46E5?style=flat-square">
  <img alt="AG-UI SDK" src="https://img.shields.io/badge/AG--UI_SDK-compatible-FF4D00?style=flat-square">
  <img alt="MCP SDK" src="https://img.shields.io/badge/MCP_SDK-v1.8.0-0891B2?style=flat-square">
  <img alt="Google ADK" src="https://img.shields.io/badge/Google_ADK-v2.8.0-4285F4?style=flat-square">
  <img alt="OpenTelemetry" src="https://img.shields.io/badge/OpenTelemetry-v1.47.0-425CC7?style=flat-square">
  <img alt="Prometheus" src="https://img.shields.io/badge/Prometheus-ready-E6522C?style=flat-square&logo=prometheus&logoColor=white">
</p>

<p align="center">
  <a href="./README.md"><strong>English</strong></a> ·
  <a href="./docs/i18n/README.zh-CN.md">简体中文</a> ·
  <a href="./docs/i18n/README.zh-TW.md">繁體中文</a> ·
  <a href="./docs/i18n/README.ja.md">日本語</a> ·
  <a href="./docs/i18n/README.ko.md">한국어</a> ·
  <a href="./docs/i18n/README.es.md">Español</a> ·
  <a href="./docs/i18n/README.pt-BR.md">Português (BR)</a> ·
  <a href="./docs/i18n/README.fr.md">Français</a> ·
  <a href="./docs/i18n/README.de.md">Deutsch</a> ·
  <a href="./docs/i18n/README.ru.md">Русский</a> ·
  <a href="./docs/i18n/README.id.md">Bahasa Indonesia</a>
</p>

```mermaid
%%{init: {
  "theme": "base",
  "themeVariables": {
    "fontFamily": "ui-sans-serif, -apple-system, Segoe UI, Roboto, sans-serif",
    "fontSize": "16px",
    "primaryColor": "#eef2ff",
    "primaryBorderColor": "#4f46e5",
    "lineColor": "#64748b",
    "clusterBkg": "#f8fafc",
    "clusterBorder": "#94a3b8"
  }
}}%%
flowchart TB
    browser["Browser / gateway"]
    machine["Machine client"]

    subgraph runtime["agentgo runtime"]
        direction TB
        agui["AG-UI adapter<br/>SSE"]
        a2a["A2A adapter<br/>JSON-RPC"]
        card["Agent Card<br/>discovery"]
        agents["Agent graph<br/>orchestration"]

        agui --> agents
        a2a --> agents
        card -.-> agents
    end

    provider["LLM provider<br/>OpenAI-compatible /<br/>Anthropic / Gemini"]
    tools["External MCP tools<br/>allowlisted"]

    browser -- "POST /ag-ui" --> agui
    machine -- "POST /a2a" --> a2a
    machine -. "GET /.well-known/<br/>agent-card.json" .-> card
    agents -- "model API" --> provider
    agents -- "MCP Streamable HTTP" --> tools

    classDef client fill:#fff7ed,stroke:#ea580c,stroke-width:2px,color:#7c2d12
    classDef protocol fill:#eef2ff,stroke:#4f46e5,stroke-width:2px,color:#312e81
    classDef core fill:#ecfeff,stroke:#0891b2,stroke-width:2px,color:#164e63
    classDef dependency fill:#f0fdf4,stroke:#16a34a,stroke-width:2px,color:#14532d

    class browser,machine client
    class agui,a2a,card protocol
    class agents core
    class provider,tools dependency
```

## Why agentgo

Building a production agent system from scratch means implementing, testing,
and maintaining all of this yourself:

| Concern | What you'd build | What agentgo gives you |
|---|---|---|
| Protocol serving | A2A JSON-RPC server, AG-UI SSE stream, Agent Card discovery | Standard protocol adapters over one shared runtime |
| Agent orchestration | Multi-agent delegation graph, tool routing, model selection | ADK Agent tree from your deployment artifact |
| Tool access | MCP client, tool allowlist, deny-by-default authorization | Server-scoped MCP allowlists; denied unless explicitly listed |
| Security | Bearer auth, OIDC verification, mutual TLS, identity mapping | Built-in, fail-closed, with explicit per-channel trust |
| Structured output | JSON Schema validation, provider-specific wire formats, text extraction | Declare a schema + JSON Pointer; agentgo handles the rest |
| Audit evidence | Immutable execution records with digests and error codes | Append-only audit with hashes, provenance, durations, and error codes; raw prompts and tool/model payloads are not stored |
| Observability | Metrics, tracing, structured logs, health probes | Prometheus, OpenTelemetry, `log/slog` with stable event names |

agentgo is not a framework you import into your code. It is a runtime you
deploy: write the artifact, point it at your LLM and MCP tools, and it serves.

## Runtime positioning

agentgo is a self-hosted runtime for deploying agent services. It is not a
mini chat platform or an alternative to user-facing AI workspaces such as Open
WebUI. The comparison below is an explicit boundary, not a feature backlog:

| Concern | Chat UI platform | agentgo |
|---|---|---|
| Primary input | An application workspace and UI | An `AgentDeployment` artifact |
| Users and groups | Product-level user management and access control | Caller-owned identity, mapped at runtime boundaries |
| Conversations | Durable product state | Caller-owned; bundled examples are in-memory demos |
| Knowledge and RAG | Often built into the product | Caller-owned content and MCP-provided tools |
| Tools | Platform extensions and plugins | External MCP services under explicit allowlists |
| UI | The product | Examples only; production callers own their UX |

Use agentgo when your application needs a self-hosted runtime for protocol
serving, agent orchestration, tool authorization, audit evidence, and
observability. Use your own product—or a platform like Open WebUI—when you need
a full user-facing workspace.

## Quick start and examples

The examples are self-contained agentgo consumers. They demonstrate the public
runtime surface without adding product capabilities to agentgo.

| Example | Purpose | Port |
|---|---|---|
| [`simple-chat`](examples/simple-chat/README.md) | Smallest browser-to-agent AG-UI path | `8080` |
| [`agentgo-assistant`](examples/agentgo-assistant/README.md) | Caller-owned embedded page-aware assistant | `8081` |

Both examples require Docker Compose and GNU Make; no local Go or Node.js setup
is needed. Provider credentials stay inside agentgo; the browser consumes only
public protocols.

```bash
git clone https://github.com/ml8s/agentgo.git
cd agentgo
```

### `simple-chat` — AG-UI conversation and task state

![agentgo simple chat animated demo](examples/simple-chat/assets/simple-chat-demo.gif)

This is the smallest browser-to-agent path. Use it to see:

- one `AgentDeployment` served over AG-UI;
- streaming assistant text through SSE;
- runtime task state mapped to `RunStarted`, streaming, `RunFinished`, and errors;
- an in-memory browser conversation with no localStorage or session management.

```bash
cd examples/simple-chat
make setup
# Edit .env: set AGENTGO_LLM_API_KEY.
make run
```

Open <http://localhost:8080/>.

### `agentgo-assistant` — click-to-explain page context

![agentgo assistant animated demo](examples/agentgo-assistant/assets/assistant-demo.gif)

This demonstrates a caller-owned embedded assistant. Use it to see:

- a floating assistant opening beside normal page content;
- click-to-explain interaction without activating links or buttons;
- only the visible selected page content and explicit browser response language;
- explicit `responseLanguage` from the browser, shown as **Browser language**;
- follow-up conversation through the same AG-UI endpoint.

```bash
cd examples/agentgo-assistant
make setup
# Edit .env: set AGENTGO_LLM_API_KEY.
make run
```

Open <http://localhost:8081/>.

### Local runtime development

For local runtime development with MCP fixtures, run:

```bash
cd ../..
cp .env.example .env
# Edit .env: set AGENTGO_LLM_API_KEY and MCP endpoint/token variables.
make dev
curl -sS http://127.0.0.1:8085/readyz | jq
```

## Deployment model

Copy `dev/agent-deployment/` as a starting point. The artifact declares:

- one or more agents (instruction file, optional output schema, tool allowlist);
- logical MCP servers bound to environment variables;
- delegation edges (`sub_agents`) for multi-agent trees.

MCP allowlists use logical tool names. At runtime, ADK namespaces exposed tools
as `<server>_<name>` so two servers can safely expose the same name; audit
records map those model-facing names back to their logical server.

The unique root agent is the entrypoint. Prompts are external files; plain
text is the default. Typed output declares a JSON Schema plus a JSON Pointer
to the user-facing string.

## LLM providers

`AGENTGO_LLM_PROVIDER` selects the model implementation. The provider value
determines both the SDK adapter and the structured-output wire mode:

| Provider | Value | Structured output |
|---|---|---|
| OpenAI (and any backend that supports json_schema) | `openai` | `json_schema` (native) |
| Anthropic Claude | `anthropic` | `native` (ADK translates genai.Schema) |
| Google Gemini | `gemini` | `native` (ADK translates genai.Schema) |
| Other OpenAI-compatible (e.g. DeepSeek, Zhipu) | any other value | `json_object` (compatibility shim) |

The structured-output mode is derived automatically from the provider; no
operator override is needed.

For example, DeepSeek exposes an OpenAI-compatible endpoint. Set
`AGENTGO_LLM_PROVIDER=openai` with
`AGENTGO_LLM_BASE_URL=https://api.deepseek.com/v1` when its endpoint and model
support OpenAI JSON Schema; use `AGENTGO_LLM_PROVIDER=deepseek` for the
OpenAI-compatible `json_object` fallback. When an Agent has no output schema,
agentgo does not send a structured-output request.

Per-Agent artifact settings (`model`, `temperature`, `maxOutputTokens`) override
the environment defaults. Priority: per-agent artifact > env default > provider default.

## Protocols

| Consumer | Protocol | Endpoint | Purpose |
|---|---|---|---|
| Machine client | A2A JSON-RPC | `POST /a2a` | discovery, invocation, streaming |
| Browser / gateway | AG-UI SSE | `POST /ag-ui` | chat, tool events, sub-agent lifecycle |
| Machine discovery | A2A Agent Card | `GET /.well-known/agent-card.json` | standard capability metadata |
| Tool server | MCP Streamable HTTP | outbound | externally owned tools |

Operational endpoints: `/livez`, `/readyz`, `/metrics`, `/debug/pprof`
(opt-in via `AGENTGO_PPROF_ENABLED=1`).

## Security

- **Deny-by-default tool access** — a tool call succeeds only if the tool is
  listed in the Agent's server-scoped `tools.allow`.
- **Trusted-header mode** — production requires
  `Authorization: Bearer <AGENTGO_PROTOCOL_TOKEN>`; AG-UI additionally requires
  a gateway-verified user identity header.
- **OIDC mode** — set `AGENTGO_IDENTITY_SOURCE=oidc` to verify standard ID
  tokens on both AG-UI and A2A.
- **Mutual TLS** — optional listener maps identity from client certificates.
- **Audit evidence** — append-only records with deployment digests, durations,
  and error codes; raw prompts and tool/model payloads are not stored.
- **No direct public exposure** — the runtime sits behind your gateway.

See [`SECURITY.md`](SECURITY.md) for the full security policy.

## Configuration

`.env.example` is the complete configuration surface. Key groups:

| Prefix / variable | Purpose |
|---|---|
| `AGENTGO_*` | service address, token, audit DB path, topology, deployment file, ops address |
| `AGENTGO_PROTOCOL_TOKEN` | internal service credential for production callers |
| `AGENTGO_MCP_*` | endpoint/token bindings for logical MCP servers |
| `AGENTGO_MAX_CONCURRENT_RUNS` | bounded concurrent executions (1–1024) |
| `AGENTGO_LLM_*` | provider, base URL, model, API key, temperature, max output tokens, timeout |
| `AGENTGO_IDENTITY_SOURCE` | `trusted_header` (default) or `oidc` |
| `AGENTGO_MTLS_IDENTITY_SOURCE` | mutual-TLS identity mapping |
| `AGENTGO_OIDC_*` | OIDC issuer, audience, and verified claim mapping |
| `OTEL_*` | standard OpenTelemetry tracing settings |

## Operations

Use `make dev` for Compose; `make dev-down` removes the workload. The ops
surface (`/livez`, `/readyz`, `/metrics`) listens on `AGENTGO_OPS_ADDR`
(default `:8085`). Release images probe `/readyz` via the built-in
`healthcheck` subcommand.

## Observability

Prometheus metrics cover protocol latency, active streams, model calls, tool
calls, delegation activity, and dependency readiness. Logs use `log/slog`
with stable event names. Distributed tracing uses W3C context propagation and
OTLP export (disabled unless configured).

SQLite stores append-only audit events. On startup the runtime closes
interrupted lifecycles with synthetic `runtime_interrupted` failures, never
editing history.

## Documentation

| Document | Purpose |
|---|---|
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | runtime architecture and protocol mapping |
| [`docs/DOMAIN.md`](docs/DOMAIN.md) | stable runtime domain invariants |
| [`docs/STANDARDS.md`](docs/STANDARDS.md) | protocol and infrastructure review standards |
| [`examples/simple-chat`](examples/simple-chat/README.md) | minimal in-memory browser chat with AG-UI task state |
| [`examples/agentgo-assistant`](examples/agentgo-assistant/README.md) | minimal click-to-explain assistant example |
| [`dev/agent-deployment/deployment.json`](dev/agent-deployment/deployment.json) | runtime validation fixture |
| [`SECURITY.md`](SECURITY.md) | security policy and required controls |

## Development

Install Node.js from `.nvmrc`; Go resolves its toolchain from `go.mod`. Run
`npm ci` once so Markdown linting uses the locked local development dependency.
Run `make setup-hooks` once so every push runs the local gate. Then use `make
check`, `make test`, `make gate`, and `make build`.

`make gate` is the pre-push gate. It runs documentation linting, Go static
checks, race tests, and the coverage floor.

### Validate a deployment artifact

```bash
go run ./cmd/agentgo validate -deployment ./path/to/deployment.json
```

### Contributing

Follow [`CONTRIBUTING.md`](CONTRIBUTING.md) and run `make check`/`make gate`
before submitting a change.

## Project boundary

agentgo is not an Agent management control plane. It does not provide Agent
CRUD APIs, deployment registries, tenant management, quota enforcement,
approval workflows, or a management console.

It is also not a chat platform or mini AI workspace. It does not own product
users, durable conversations, chat history, document libraries, RAG pipelines,
model catalogs, plugins, calendars, notes, or a user-facing workspace.

`caller` owns users, products, entitlements, quotas, and durable product
conversations. A domain content repository owns prompts, skills, workflow
policy, and tool semantics; external MCP tool services own MCP tool
implementations.

### License

Released under the [MIT License](LICENSE).

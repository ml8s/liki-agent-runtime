# Architecture

## System boundary

`agent-runtime` is a generic multi-agent runtime. `caller` owns users,
authentication, products, payments, entitlements, quota, and durable product
conversations. The domain content repository owns prompts, skills, domain policy, and
tool semantics. External MCP servers expose deterministic and judgment tools.

```text
Server / machine client
  ↓ A2A JSON-RPC
┌────────────┐   MCP Streamable HTTP
│ agent-runtime │──────────────────────→ MCP tools (MCP tools, MCP tools, ...)
└────────────┘
  ↑ AG-UI SSE
caller gateway
```

There is one execution path and no private `/v1` application API.

## AgentDeployment

The runtime starts from an external `AgentDeployment` artifact. The artifact is
an ADK Agent tree: it may contain one or more Agent definitions, and its unique
incoming-edge-free Agent is the entrypoint. Each Agent definition supplies:

- instruction text file;
- optional JSON Schema for structured output;
- paired JSON Pointer selecting the user-facing text when structured output is
  declared;
- logical MCP server declarations with endpoint/token environment references;
- server-scoped MCP tool allowlists;
- standard ADK delegation mode and `sub_agents`;
- per-Agent model settings (`model`, `temperature`, `maxOutputTokens`) that
  fall back to the runtime-wide configuration;
- version.

Deployment metadata carries the producer/runtime version. Audit provenance uses
that version, the artifact digest, instruction digest, and runtime graph label;
it does not require a separate tool version.

Prompt, output schema, and tool semantics are not built into Go.
Instructions are installed through ADK's `InstructionProvider` path because
AgentDeployment prompts are immutable text artifacts and may contain literal
JSON/code braces. ADK must not interpret those braces as session-state
placeholders.
The manifest is decoded strictly by the runtime, then graph, file, MCP, and
model-setting invariants are evaluated before startup.
The deployment digest covers the manifest plus resolved instruction and output
schema artifacts and the ADK graph semantics; it is an audit provenance label,
not a startup gate. Deployment integrity is owned by deployment system: the content build
the assembly image from the sha-tagged local runtime base, and deployment system
assembles and deploys the images by commit sha, so runtime binary, manifest,
instructions, schemas, and skills roll out and roll back as one unit.

## Principles

1. A2A and AG-UI are standard protocol adapters over one shared runtime.
2. Google ADK Go v2 owns the model/tool execution graph.
3. MCP is the only boundary to external deterministic tools.
4. Every tool call is constrained by the Agent's explicit allowlist.
5. Audit is append-only execution evidence.
6. `internal/domain` remains free of transport, provider, adapter, and storage
   imports.
7. Standard libraries and framework APIs are preferred over local equivalents.
8. Mapping, validation, visibility, and policy rules are pure functions with
   explicit inputs and outputs where practical. Mutable state is confined to
   named runtime components, injected through configuration, scoped to a run or
   component lifetime, and protected where shared. Startup-derived lookup and
   SQL plans are immutable after construction.

## Dependencies and modules

```text
cmd/agent-runtime
  → internal/transport
  → internal/protocol/a2a
  → internal/protocol/agui
  → internal/agent
  → MCP servers / model provider

internal/audit/sqlite
  → internal/audit
  → internal/domain

internal/adksession/sqlite
  → official ADK relational session service

internal/protocolstate/sqlite
  → A2A SDK stores

internal/observability/prometheus
  → observability interfaces
```

Protocol adapters never import each other. Database technology is confined to
`internal/audit/sqlite` for append-only evidence,
`internal/protocolstate/sqlite` for mutable standard protocol state, and
`internal/adksession/sqlite` for the official ADK relational session adapter.

| Module | Responsibility |
|---|---|
| `internal/protocol/a2a` | Agent Card and official A2A JSON-RPC binding |
| `internal/protocol/agui` | Official AG-UI request decoding and SSE binding |
| `internal/deployment` | AgentDeployment aggregate, strict loading, validation, and content identity |
| `internal/agent` | ADK graph, model adapter, MCP tools, audit hooks |
| `internal/adksession/sqlite` | Official ADK relational session service with safe SQLite configuration |
| `internal/domain` | Runtime audit and validation invariants |
| `internal/audit` | Immutable audit event format and recorder port |
| `internal/audit/sqlite` | Append-only audit persistence |
| `internal/protocolstate/sqlite` | A2A task aggregates and push notification configs |
| `internal/execution` | Runtime-neutral run identity, event projection, output rules, and metrics port |
| `internal/observability` | Metric interfaces |
| `internal/observability/prometheus` | Prometheus implementation |
| `internal/platform` | Configuration, identity, health interfaces, build metadata |
| `internal/transport` | Routes, bearer auth, identity propagation, body limit, readiness |

## Runtime execution

AG-UI requests are mapped to a generic `RunRequest`. History and opaque
protocol context may be supplied by the caller; the runtime does not store
product conversation state. History is rebuilt per run as native multi-turn
events in the run-scoped ADK session (user and entrypoint-authored model turns,
latest message last), so the model receives chronological turns and providers
can prefix-cache every earlier turn; the seeded events are deleted with the
session at terminal execution and the caller stays the owner of durable
history.

Session storage follows the protocol surface. Runtime.Run (AG-UI) executes on
an ephemeral runner backed by ADK's in-process session service: run-scoped
working state is seeded from caller history, deleted at terminal execution,
and never outlives the process, so it pays no durable-store I/O. The A2A
executor uses the durable runner backed by the injected session service
(SQLite in production), because ContextID-owned sessions must survive
attempts. Splitting the runners structurally — rather than routing on
session-ID prefixes — removes any heuristic a client-controlled ContextID
could abuse. The idle-session retention sweep covers only the durable store.

A2A follows the official ADK mapping instead: ContextID owns the ADK session,
TaskID owns the protocol aggregate, and a deterministic attempt ID owns runtime
concurrency and audit scope. Attempt audit ends when the execution attempt ends;
the ContextID session can survive an attempt.

Run, thread, and user identifiers are required. A RunID/attempt ID cannot start
while it is active. Terminal AG-UI execution removes its run-scoped working
session; a successful A2A attempt preserves the ContextID-owned session so the
standard protocol state can continue.
Both protocol adapters enforce the configured total run deadline, and the
runtime bounds concurrent model/MCP executions with
`AGENT_RUNTIME_MAX_CONCURRENT_RUNS`.

The entrypoint `AgentDefinition` supplies instruction, an optional output
schema, and the tool allowlist. Plain text is the default. When structured
output is declared, the runtime uses ADK's OpenAI-compatible model integration
and selects either native JSON schema or provider-safe JSON object mode.

MCP tools are consumed through MCP Streamable HTTP and filtered by the
server-scoped AgentDefinition allowlist. Tool calls and responses become
protocol facts and execution evidence; model assertions do not create tool
provenance.
Standard `tools/list` discovery is memoized per logical server for sixty
seconds with single-flight refresh: without it, every agent flow (including
every sub-agent invocation) repeats an outbound discovery round before its
first model call. A failed refresh serves the last known catalog — declarations
stay available — while every tool call still uses the live fast-fail transport,
so a removed server-side tool fails its call rather than being hidden. The
allowlist is applied after the memoized discovery, never bypassed by it
(`agent_runtime_mcp_catalog_requests_total` exposes hit/refresh/stale/error).
ADK's built-in `transfer_to_agent` call is not an MCP tool. It remains visible
on AG-UI as a tool event and is audited through Agent delegation lifecycle
events rather than MCP tool provenance.
The same exemption covers ADK's framework delegation tools: the task /
single_turn tool installed on a parent for a sub-agent (the tool is named after
the sub-agent), and the `finish_task` tool installed on a task Agent. A
task-mode sub-agent stops its event iterator as soon as `finish_task` succeeds,
so the sub-agent's AfterAgent callback never runs; the runtime records that
delegation's completion from the synthesized task-tool function response
instead.

Raw structured model output is not emitted to protocol observers. Partial
events are suppressed. Model-text visibility is defined once
(`agent.ExposesModelText`) and shared by both protocol adapters. A structured
Agent returns validated generic JSON plus
the user-facing text selected by its configured JSON Pointer; a plain-text
Agent returns final model text directly. In a delegated plain-text tree, the
last terminal expert model response is the root user-facing answer. Structured
output state keys are
Agent-scoped, so delegated structured Agents cannot overwrite the entrypoint
result.

## Protocols

Protocol choices are governed by [`STANDARDS.md`](STANDARDS.md); a private wire
protocol must not replace a reviewed standard.

Protocol clients own product persistence and business follow-up. AG-UI protocol
clients consume the standard SSE terminal event; A2A protocol clients consume
task lifecycle events, standard task reads, or configured standard A2A push
notifications.
The runtime does not send private run-completed callbacks, CloudEvents, or
product webhooks.

A2A is a standard wire protocol, not an agent or persistence API. The runtime
exposes an A2A protocol server and stores only protocol-owned task/push-config
state. A2A uses the official A2A Go SDK JSON-RPC binding and ADK A2A executor.
The runtime uses the executor's standard ContextID-to-ADK-session mapping. Task
aggregates, client-registered push callbacks, and official ADK sessions live in
separate SQLite databases from append-only audit evidence; push is enabled only through its two official dependencies and sends
the standard A2A `StreamResponse` payload. No private A2A extension, RPC
method, or state machine is advertised. Model-text visibility is resolved per
event author against the deployment by both adapters (via
`agent.ExposesModelText`), so a structured expert delegated under a plain
entrypoint never leaks raw model JSON on either protocol.

MCP endpoint bindings are deployment-owned complete URLs. A gateway may expose
service prefixes such as `/tools-a` and `/tools-b`; the MCP service owns its
standard `/mcp` and `/mcp/{domain}` routes. The runtime consumes the configured
URL verbatim through the official MCP SDK and does not translate paths.
Development uses one domain-neutral MCP fixture image; its `tools-a` and
`tools-b` instances expose configured generic tool families over official
Streamable HTTP and exist only to exercise discovery, allowlists, readiness,
and tool execution.

Google ADK is the internal execution runtime, not the public control plane. The
ADK Launcher/REST API is intentionally not exposed. External clients use only
the standard protocol surfaces above; MCP readiness is exposed through
`/readyz`.

AG-UI decodes the official `RunAgentInput` and emits official SSE events. The
supported profile is text chat; unsupported capabilities are rejected rather
than silently ignored.
Official clients may send an absent, null, or empty `state` value; the stateless
runtime ignores it. Non-empty client state is rejected with a stable error code.

Event mapping:

| Runtime fact | AG-UI event |
|---|---|
| accepted run | `RUN_STARTED` |
| sub-agent start | `SUBAGENT_STARTED` |
| sub-agent end | `SUBAGENT_FINISHED` |
| sub-agent failure | `SUBAGENT_ERROR` |
| MCP tool call | `TOOL_CALL_START` / `TOOL_CALL_ARGS` with sub-agent attribution |
| MCP tool response | `TOOL_CALL_RESULT` / `TOOL_CALL_END` with sub-agent attribution |
| final answer | `TEXT_MESSAGE_START` / `TEXT_MESSAGE_CONTENT` / `TEXT_MESSAGE_END` |
| success | `RUN_FINISHED` |
| failure | `RUN_ERROR` |

`RUN_FINISHED` contains the AgentDefinition reference, validated output when
structured output is declared, and the authoritative user-facing `answer` text
for the domain content product profile. A run never emits `RUN_ERROR` after
`RUN_FINISHED`.

The A2A Agent Card uses curated `AgentDefinition` descriptions, mode, tool
allowlist tags, and deployment provenance. It never includes instructions,
prompts, raw model output, or tool payloads.

Structured output is decoded once per run. The runtime consumes the final
event's state delta directly, and both protocol adapters reuse the decoded
document alongside the canonical JSON bytes instead of re-parsing them; the
canonical JSON on the wire is unchanged.

## Functional core, imperative shell

The runtime separates pure decision code from stateful execution. The
functional core is `internal/domain` (stable error codes, identifier rules,
opaque IDs) and `internal/execution` (run requests/results, pure event state
transitions, structured-output parsing, and the `LLMCall` lifecycle value):
these packages have no pointer receivers that mutate shared state, and their
transition functions return new values instead of editing inputs. The
imperative shell is `internal/agent` plus the protocol/transport adapters: it
owns bounded process state (attempt registry, lifecycle maps, pending audit
records, caches) and releases it only after terminal audit evidence is
durable. New capabilities join the core as pure functions and the shell as
explicit, lock-annotated state — do not move mutation into `domain` or
`execution`.

## Operations

The operational surface (`/readyz`, `/version`, `/metrics`, `/debug/pprof`) is
served on a dedicated ops listener (`AGENT_RUNTIME_OPS_ADDR`, default `:8085`)
so protocol mutual TLS never gates healthchecks or scrapers; `/metrics` keeps
the same bearer-token boundary as protocol calls whenever an internal token is
configured, or is owned by the dedicated `AGENT_RUNTIME_OPS_TOKEN` when that
separation is configured (mandatory in production with OIDC identity, whose
user ID tokens are unsuitable for long-lived scraper credentials). The
protocol listener can additionally expose a mutual-TLS endpoint
(`AGENT_RUNTIME_TLS_ADDR` plus the certificate trio, client certificates
verified against the CA). All POST endpoints enforce a 2 MB body limit.

Bearer-token failures and missing verified identities are counted in a bounded
fixed window per TCP socket source. Untrusted forwarding headers never change
the source. A source that exceeds ten failures in one minute receives HTTP 429
and the stable `rate_limited` code.
Service authentication and end-user identity are separate ports. A service
token authenticates the caller; a pluggable `identity.RequestSource` supplies
the verified end-user identity. The bundled trusted-header adapter maps only
channel-owned headers. The OIDC adapter verifies one standard bearer ID token
through a mature OIDC/JWKS library and authenticates both protocol surfaces.
On the mutual-TLS listener the default `CertificateSource` maps the verified
client-certificate subject (URI SAN, then email SAN, then common name;
organization maps to the tenant); the relay-header mapping on that listener is
an explicit `AGENT_RUNTIME_MTLS_IDENTITY_SOURCE=header` deployment opt-in.

Readiness checks SQLite and every declared MCP dependency through the official
MCP discovery RPC. It requires the configured MCP revision, server identity,
tool capability, and every allowlisted tool.
During process drain, readiness reports `draining` before the standard HTTP
server shutdown deadline waits for active protocol requests.

Prometheus exposes protocol request count and duration, active streams, LLM
calls and tokens, tool calls and duration, durable record growth, and
dependency readiness. Storage gauges are sampled outside request paths. The
audit ledger is append-only and has no automatic deletion. Terminal A2A tasks
and their push configs have an optional runtime retention sweep; wait states
are preserved. Idle ADK sessions also have an optional retention sweep that
uses the official session service, protects non-terminal A2A ContextIDs, and
cannot run below the configured run timeout. Structured logging uses `log/slog`
with stable event names. Logs do not contain user prompts, raw model output, or
tool payloads.

Audit events are append-only SQLite records. They contain execution metadata,
protocol, trace correlation, agent and deployment digests, versions, status,
duration, and error codes—not conversation or domain payloads. Both execution
paths append the terminal run event before releasing process state (session,
concurrency slot); the A2A path additionally remains retryable by the executor's
cleanup callback when the terminal append fails, while the AG-UI (Run) path
relies on startup recovery for that residual failure. Reconciled terminal facts
(interrupted runs, model calls, tools, delegations) carry the same trace and
definition provenance as their started facts.

Before accepting traffic, the single-process runtime transactionally finds
started run, LLM, tool, and delegation events without terminal evidence and
appends synthetic failures coded `runtime_interrupted`. Recovery uses stable
lifecycle IDs, preserves original provenance, is idempotent, and never updates
or deletes history. Execution is still not resumed after a crash; clients start
a new run. The `multi` topology remains fail-closed until audit ownership is
transactional across replicas.

## Observability and audit implementation

The system separates telemetry from durable evidence:

- Prometheus exports aggregate metrics.
- `log/slog` emits structured operational logs.
- OpenTelemetry emits distributed traces over the standard OTLP protocol.
- SQLite stores immutable audit events.

The runtime emits an `agent.run` span and propagates W3C trace context. Trace
IDs and span IDs are also stored on audit events so a compliance record can be
correlated with its distributed trace without duplicating payloads.

OpenTelemetry is enabled only when a standard OTLP endpoint is present:

```text
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://otel-collector:4318/v1/traces
```

Sampling follows standard `OTEL_TRACES_SAMPLER` and
`OTEL_TRACES_SAMPLER_ARG` variables.

Stable error codes live in `internal/domain/error_codes.go`.

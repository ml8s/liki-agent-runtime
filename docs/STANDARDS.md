# Standards

This document is the review gate for protocol and infrastructure choices. The
rule is:

1. use an existing external standard when one exists;
2. use that standard's official/reference Go implementation when one exists;
3. use an established ecosystem library for a standardized format;
4. implement product/domain-specific behavior in the caller or deployment content,
   not in the generic runtime;
5. never invent a private wire protocol or redefine standard protocol fields.

## Protocol matrix

| Concern | Standard | Implementation used | Review result |
|---|---|---|---|
| Agent-to-agent discovery and tasks | [A2A 1.0](https://a2a-protocol.org/v1.0.0/specification/) | `github.com/a2aproject/a2a-go/v2` and ADK `server/adka2a/v2` | Keep. Use the SDK's JSON-RPC, Agent Card, task, and ContextID semantics. |
| A2A ContextID/session mapping | A2A ContextID groups related tasks; ADK A2A executor maps ContextID to its ADK session | ADK executor defaults | Keep the official mapping. The rejected TaskID-backed session mapping changed standard context behavior. |
| A2A extensions | Agent Card `AgentExtension` with a published URI specification | none advertised | Keep the no-extension policy unless an extension has a published specification and an SDK-supported compatibility suite. |
| Frontend agent events | [AG-UI events](https://docs.ag-ui.com/concepts/events) | `github.com/ag-ui-protocol/ag-ui/sdks/community/go` | Keep official event objects and lifecycle rules. |
| Tool access | [MCP Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) | `github.com/modelcontextprotocol/go-sdk` | Keep official `tools/list` and `tools/call`; filtering is application authorization after standard discovery. Discovery results are memoized per server (60s TTL, stale-on-error) without changing the wire protocol or the authorization order. Readiness accepts any SDK-supported protocol revision reached through negotiation (`2025-03-26` and later), not only the newest one. Stand-alone SSE delivery is disabled: the tool surface is deployment-fixed at startup. |
| A2A protocol state | A2A Task, TaskVersion, and push-config semantics | `github.com/a2aproject/a2a-go/v2` official store ports | Keep. This is protocol state only; it must not be treated as product conversation history. |
| Typed output | JSON Schema 2020-12 | `github.com/google/jsonschema-go/jsonschema` | Keep. |
| Output text location | RFC 6901 JSON Pointer | `github.com/qri-io/jsonpointer` plus static JSON Schema traversal | Keep. Pointer parsing must use the RFC library; only schema traversal is local. |
| Content integrity | SHA-256 (`crypto/sha256`) with a lower-case `sha256:<64-hex>` value | Go standard library | Keep. Deployment pinning is configuration trust, not protocol. |
| Trace correlation | W3C Trace Context / OpenTelemetry | OpenTelemetry Go SDK and `otelhttp` | Keep correlation and export through OTLP. |
| Internal bearer authentication | HTTP `Authorization: Bearer` semantics | standard HTTP middleware and A2A security declaration | Keep internal token verification; do not place credentials in protocol payloads. |
| OIDC identity verification | OpenID Connect Discovery and ID Token validation | `github.com/coreos/go-oidc/v3` | Keep provider discovery, JWKS lookup, signature/issuer/audience/expiry validation in the library; adapters only map verified claims. |

## Deliberate non-protocols

The following are private application artifacts rather than wire protocols.
They must not be exposed through A2A, AG-UI, or MCP fields:

- `AgentDeployment` is a local deployment artifact validated by the runtime
  loader.
  It configures ADK and MCP authorization; it is not an agent-to-agent message.
- The SQLite audit ledger is internal execution evidence. Its schema is not an
  event-delivery protocol. OpenTelemetry supplies trace IDs, but does not
  replace transactional append/conflict semantics required by evidence.
- Stable domain error codes are internal observability identifiers. Protocol
  adapters map them through their official SDK error types.
- Tool allowlists are authorization policy consumed after standard MCP
  discovery. MCP remains unchanged on the wire.

## Deployment model

### Current supported topology

The service is deliberately **single-process/single-replica**:

- ADK uses the official relational session service backed by local SQLite.
- the active-run registry and run concurrency semaphore are process-local;
- A2A task state and registered callbacks use the local SQLite protocol-state
  database;
- audit evidence is in a local SQLite file;
- MCP toolsets and the ADK graph are process-local.

`AGENT_RUNTIME_TOPOLOGY` makes that mode explicit. Its default and only
supported value is `single`; `multi` is recognized but fails closed so an
orchestrator cannot accidentally scale an unsafe process.

A container orchestrator may restart one replica for availability, but scaling
`replicas > 1` is not supported and must not be configured. SQLite must not be
shared by multiple nodes.

### Idempotent run boundary

The runtime keeps an execution attempt idempotent: a given attempt/RunID
executes at most once and crashes are never resumed. Startup recovery only
appends `runtime_interrupted` terminal evidence.

The two protocol profiles own session state differently:

- **AG-UI** uses a run-scoped ADK session (`run:<RunID>`) that is deleted at
  terminal execution. Durable multi-turn history remains owned by `caller`.
- **A2A** uses the official ADK mapping in which ContextID owns the ADK
  session. An attempt audit can end while the ContextID session remains
  usable by later standard operations.

At startup, process-owned task states (`unspecified`, `submitted`, and
`working`) are marked failed because their in-process execution cannot resume.
Client-owned wait states (`input-required` and `auth-required`) are preserved
against the durable ContextID session. This is still single-replica: SQLite is
not shared, and `multi` remains fail-closed.

### Configuration ownership

Every setting lives in exactly one of the three product layers:

Audit labels (provider and prompt digest) travel with the assembly artifact;
they are recorded verbatim into audit evidence but never gate runtime behavior.

- **image** — protocol, security, and audit behavior; never configurable
  (HTTP timeouts, audit durability, rate limits stay at the gateway);
- **AgentDeployment artifact** — capability: Agents, tools, skills, output
  schemas, and per-Agent model settings (`model`, `temperature`,
  `maxOutputTokens`), pinned by digest;
- **environment** — wiring: addresses, tokens, MCP endpoints, the LLM client
  (base URL, model, provider label), and timeouts.

A value that duplicates another layer fails closed rather than being ignored.

### Readiness

The runtime keeps official extension points without enabling an experimental
or multi-replica mode:

- `agent.Config.SessionService` accepts any official ADK `session.Service`;
  nil still selects ADK's official in-memory implementation.
- A2A adapter configuration accepts the SDK's official
  `a2asrv/taskstore.Store`; nil still selects the SDK default.
- On `SIGINT`/`SIGTERM`, the HTTP server marks `/readyz` as `draining`, then
  uses the standard `http.Server.Shutdown` deadline to wait for in-flight
  requests. Drain does not cancel active protocol streams.
- The A2A SDK's experimental cluster mode is not enabled.

## Change checklist

A change that touches an external boundary must answer:

- Which specification section defines this behavior?
- Which official/reference SDK type is used?
- Which standard fields are omitted or rejected, and where is that mapping
  tested?
- Is any private field visible on a standard wire? If yes, stop.
- Does deployment still assume one process? If not, where are sessions, tasks,
  audit, and routing shared?

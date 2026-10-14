# Standards and Library Audit

This document is the review gate for protocol and infrastructure choices. The
rule is:

1. use an existing external standard when one exists;
2. use that standard's official/reference Go implementation when one exists;
3. use an established ecosystem library for a standardized format;
4. implement private logic only for Liki-owned business rules, behind a port;
5. never invent a private wire protocol or redefine standard protocol fields.

## Protocol matrix

| Concern | Standard | Implementation used | Review result |
|---|---|---|---|
| Agent-to-agent discovery and tasks | [A2A 1.0](https://a2a-protocol.org/v1.0.0/specification/) | `github.com/a2aproject/a2a-go/v2` and ADK `server/adka2a/v2` | Keep. Use the SDK's JSON-RPC, Agent Card, task, and ContextID semantics. |
| A2A ContextID/session mapping | A2A ContextID groups related tasks; ADK A2A executor maps ContextID to its ADK session | ADK executor defaults | Keep the official mapping. The rejected TaskID-backed session mapping changed standard context behavior. |
| A2A extensions | Agent Card `AgentExtension` with a published URI specification | none advertised | Keep the no-extension policy unless an extension has a published specification and an SDK-supported compatibility suite. |
| Frontend agent events | [AG-UI events](https://docs.ag-ui.com/concepts/events) | `github.com/ag-ui-protocol/ag-ui/sdks/community/go` | Keep official event objects and lifecycle rules. |
| Tool access | [MCP Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) | `github.com/modelcontextprotocol/go-sdk` | Keep official `tools/list` and `tools/call`; filtering is application authorization after standard discovery. Readiness accepts any SDK-supported protocol revision reached through negotiation (`2025-03-26` and later), not only the newest one. Stand-alone SSE delivery is disabled: the tool surface is deployment-fixed at startup. |
| Typed output | JSON Schema 2020-12 | `github.com/google/jsonschema-go/jsonschema` | Keep. |
| Output text location | RFC 6901 JSON Pointer | `github.com/qri-io/jsonpointer` plus static JSON Schema traversal | Keep. Pointer parsing must use the RFC library; only schema traversal is local. |
| Content integrity | SHA-256 (`crypto/sha256`) with a lower-case `sha256:<64-hex>` value | Go standard library | Keep. Deployment pinning is configuration trust, not protocol. |
| Trace correlation | W3C Trace Context / OpenTelemetry | OpenTelemetry Go SDK and `otelhttp` | Keep correlation and export through OTLP. |
| Internal bearer authentication | HTTP `Authorization: Bearer` semantics | standard HTTP middleware and A2A security declaration | Keep internal token verification; do not place credentials in protocol payloads. |

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

Deployment-description candidates were also reviewed. A2A Agent Card describes
discovery, not a private executable graph; Google Cloud Agent Registry schemas
are cloud discovery records; and the JSON Agents Portable Agent Manifest is an
explicit draft without standards-body adoption or community consensus. No
reviewed candidate replaces this service's local loader behavior.

## Rejected custom designs

| Rejected design | Root problem | Standards-based resolution |
|---|---|---|
| Mapping every A2A TaskID to a separate ADK session while preserving ContextID only as correlation | Contradicted the SDK/ADK ContextID mapping and silently changed multi-task context behavior | Use ADK's default ContextID session mapping. |
| A private AgentDeployment `tools.visibility` field that emitted empty tool arguments/results | No A2A/AG-UI/MCP semantics for selective payload redaction; AG-UI args/result values are required to be non-empty | Do not emit the feature. If opaque tools are required later, omit the complete standard tool-call event sequence rather than redefine its fields. |
| Unconditional multi-replica startup reconciliation | No external audit-recovery standard; concurrent owners could race or rewrite evidence | Keep `multi` fail-closed. Single-process startup uses one transaction, stable lifecycle IDs, and append-only synthetic failures before traffic is accepted. |
| Publishing the private deployment manifest as an A2A extension | A2A extensions require an identified, published specification and client opt-in behavior | Advertise no private extension. Standard Agent Card fields and internal audit already carry required identity and provenance. |
| Carrying the structured user-facing text in a private A2A `Part.Metadata` key (`liki.answer`) | No published specification for a private metadata key; the full validated output is already the answer | A structured Agent emits a standard `application/json` `DataPart`; the user-facing text is the JSON field selected by its `textPointer`. No private metadata on the wire. |
| Local RFC 6901 escape parser | Duplicated an existing RFC implementation already used by the runtime | Parse pointers with `github.com/qri-io/jsonpointer`. |

## Deployment model

### Phased optimization plan

| Phase | Scope | Acceptance gate |
|---|---|---|
| 1. Safe single-process mode | Explicit topology guard, official ADK session and A2A task-store injection points, readiness drain | Default behavior is unchanged; `multi` fails closed; official-interface injection is tested; no experimental cluster mode |
| 2. Shared state design | Select PostgreSQL/Spanner (or official Vertex AI) ADK sessions, database A2A tasks, and transactional audit/run ownership | Official conformance tests plus cross-replica uniqueness, cancellation, drain, and failure tests pass |
| 3. Routing design | Choose A2A distributed execution, task ownership routing, or explicit AG-UI stream affinity | `GetTask`/`CancelTask` work on any A2A replica and AG-UI stream behavior is deterministic during rollout |
| 4. Rollout authorization | Assembly image digest lock (liki-deploy), orchestration manifests, chaos tests | New/old replicas cannot execute mismatched behavior unexpectedly; kill/drain tests preserve audit invariants |

Only phase 1 is implemented. Later phases require measured capacity or availability requirements and a reviewed design.

### Current supported topology

The service is deliberately **single-process/single-replica**:

- ADK uses its in-memory session service.
- the active-run registry and run concurrency semaphore are process-local;
- A2A uses the SDK's default in-memory task store;
- audit evidence is in a local SQLite file;
- MCP toolsets and the ADK graph are process-local.

`LIKI_AGENT_RUNTIME_TOPOLOGY` makes that mode explicit. Its default and only
supported value is `single`; `multi` is recognized but fails closed so an
orchestrator cannot accidentally scale an unsafe process.

A container orchestrator may restart one replica for availability, but scaling
`replicas > 1` is not supported and must not be configured. SQLite must not be
shared by multiple nodes.

### Idempotent run boundary

The runtime keeps run semantics idempotent and stateless: a given RunID
executes at most once, the ADK session is run-scoped and deleted at terminal
execution, and crashes are never resumed — startup recovery only appends
`runtime_interrupted` terminal evidence. Multi-turn dialogue is owned by the
caller (`liki-web`), which persists history and carries it into a new run.

A2A same-TaskID pause-and-resume (`input-required` / long-running tools) is
therefore out of scope for this stateless runtime: it requires cross-run
recoverable session state, shared task/session storage, and transactional
audit/run ownership — the phase 2 shared-state prerequisites above. The runtime
does not claim to support it on the standard RPC surface today.

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

### Harmless readiness already implemented

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

### When more than one replica is justified

Multiple replicas are appropriate only after at least one of these measurable
requirements exists:

- concurrent requests exceed one process's CPU, memory, connection, or run-slot
  capacity;
- rolling or blue/green deployment requires a new instance before the old one
  drains;
- node failure must be tolerated while a replica remains available;
- agent execution must scale independently from `liki-web`, Engine, Counsel, or
  the model provider.

If traffic fits one process and clients can safely retry a failed run, multiple
replicas add shared state, routing, and recovery complexity without value.

### Required standard implementation path

Do not add ad-hoc multi-instance machinery. A future multi-replica design must
use the official extension points together:

1. Replace the in-memory ADK session service with ADK
   `session/database` backed by PostgreSQL or Spanner (or the official Vertex AI
   session service). SQLite remains suitable only for one process.
2. Provide the official A2A `taskstore.Store` implementation backed by the same
   transactional database. Use the SDK's task-store/cluster-mode APIs rather
   than a private task protocol; require a non-experimental SDK release before
   enabling cluster mode.
3. Move audit append, run-ID uniqueness, and terminal-event ownership into the
   same transactional database.
4. Lock the assembly image digest per release so replicas cannot execute
   different behavior versions.
5. Keep requests routable to the owner of a task/session, or use the official
   A2A distributed execution path.
6. If a singleton maintenance job becomes necessary, claim work with a database
   transaction or a supported orchestration lease. Kubernetes
   `client-go/leaderelection` is a standard API, but its documentation warns it
   does not guarantee strict fencing; it is not sufficient by itself for
   exactly-once audit writes.

## Change checklist

A change that touches an external boundary must answer:

- Which specification section defines this behavior?
- Which official/reference SDK type is used?
- Which standard fields are omitted or rejected, and where is that mapping
  tested?
- Is any private field visible on a standard wire? If yes, stop.
- Does deployment still assume one process? If not, where are sessions, tasks,
  audit, and routing shared?

# A2A Standard Implementation Plan

## Objective

Make the runtime a standards-correct A2A 1.0 task-capable agent runtime. The
library owns standard protocol behavior; an embedding product may choose not to
use an optional capability, but must not cause the runtime to advertise or
implement a nonstandard replacement.

The authoritative model is:

- **Context ID** groups related work and maps to the ADK session.
- **Task ID** is the protocol task aggregate and task-lifecycle owner.
- **Execution attempt** is one actual invocation and owns runtime/audit scope.
- **Push notification config** is client-owned callback state registered
  through standard A2A operations.

## Principles

1. Use official A2A Go SDK types and handlers; never create a private wire
   protocol.
2. A capability may be absent, but if advertised it must be fully usable.
3. Transport credentials map to an authenticated A2A principal.
4. Task and push-config ownership is scoped by authenticated principal.
5. Protocol state is not audit evidence and must live in a separate durable
   protocol-state store.
6. CloudEvents remain a private control-plane integration and never replace an
   A2A push payload.
7. There are no compatibility aliases for incorrect old behavior.

## Workstreams

### 1. Protocol identity and ownership

**Goal:** every A2A request reaches the SDK with an authenticated principal.

Requirements:

- Map the transport service credential to `a2asrv.CallContext.User`.
- Preserve an upstream authenticated user if one is already present.
- Use the principal for task ownership, push-config ownership, and audit
  provenance.
- Return standard A2A unauthenticated/not-found errors rather than leaking
  existence across owners.

**Status:** implemented. Verified mTLS or bearer transport identities map to A2A, durable ownership, and audit; anonymous relay headers are not trusted.

### 2. Agent Card accuracy

**Goal:** discovery never promises more than the handler serves.

Requirements:

- JSON-RPC interface, protocol version, and security schemes must match the
  listener.
- `streaming`, `pushNotifications`, and `extendedAgentCard` are accurate.
- Public skills describe externally callable capabilities, not internal
  sub-agents or MCP tool topology.
- Partial capability dependencies fail startup.

**Status:** push capability follows configured dependencies; bearer/mTLS schemes now match composed transport capabilities, and public skills expose only the entrypoint.

### 3. Standard task lifecycle

**Goal:** runtime execution follows A2A Task semantics.

Requirements:

- `ContextID` maps to ADK session.
- `TaskID` owns protocol task state, artifacts, and history.
- One execution attempt owns runtime concurrency and audit scope.
- Same-context tasks can execute independently.
- `INPUT_REQUIRED` / `AUTH_REQUIRED` are non-terminal and can continue.
- Terminal task states close the attempt but do not delete the context session
  merely because audit ends.
- Startup marks abandoned non-terminal tasks failed instead of leaving them
  working forever.

**Status:** attempt identity, task provenance, pause/cancel evidence, session
preservation, startup recovery, and ADK InvocationID correlation implemented.
Concurrent attempts sharing one ContextID can now resolve callbacks through the
runtime invocation bridge.

### 4. Durable protocol state

**Goal:** task and callback state survive process restart.

Requirements:

- SQLite (or equivalent) implementation of official `taskstore.Store`.
- SQLite implementation of official `push.ConfigStore`.
- Task versioning uses optimistic concurrency.
- List operations support owner, tenant, context, state, timestamp, and cursor
  filters.
- Retention is explicit and independent from audit cleanup.

**Status:** reference SQLite TaskStore and PushConfigStore implemented and
wired into the runtime. Official ADK SQLite session storage is also wired with
its conformance suite.

### 5. Standard push notifications

**Goal:** support the A2A push notification capability as specified.

Requirements:

- Inject official `push.ConfigStore` and `push.Sender`.
- Advertise `capabilities.pushNotifications` only when both are configured.
- Support callback registration both inline in `SendMessage` and through the
  four standard config operations.
- Send the official `a2a.StreamResponse{Event: event}` payload.
- Do not use CloudEvents, private run events, or ADK metadata as the callback
  payload.
- Enforce HTTPS callback URLs.
- Keep the SDK SSRF guard enabled by default.
- Authenticate webhook delivery according to the registered config.
- Never log callback tokens or credentials.

**Status:** official SDK wiring and capability declaration implemented;
durable reference store and conformance suite are next.

## Execution plan: durable protocol state

This execution implements the next prerequisite for a task-capable runtime: a
separate durable protocol-state database.

### Scope

1. Add `internal/protocolstate/sqlite`.
2. Store the complete standard A2A Task aggregate in `a2a_tasks`.
3. Store standard push callback configs in `a2a_push_configs`.
4. Implement the official `taskstore.Store` and `push.ConfigStore` interfaces.
5. Scope every operation by `(tenant, principal)`.
6. Return official SDK sentinel errors:
   - `a2a.ErrTaskNotFound`
   - `taskstore.ErrTaskAlreadyExists`
   - `taskstore.ErrConcurrentModification`
   - `push.ErrPushConfigNotFound`
7. Implement standard `ListTasks` filters, history limits, artifact toggles,
   and stable cursor pagination.
8. Keep audit and protocol state in separate SQLite databases.

### Storage rules

- `a2a_tasks.task_json` is the authoritative standard Task aggregate.
- Indexed columns are query projections, never a second source of truth.
- `version` starts at 1 and increments only on successful optimistic update.
- Push config IDs are UUIDv7 when omitted by the client.
- Push config task IDs are always forced to the addressed task.
- A missing or anonymous principal is rejected before touching state.
- SQLite runs WAL with `busy_timeout`, foreign keys, and restrictive file mode.

### Wiring

- `LIKI_AGENT_RUNTIME_PROTOCOL_STATE_DB_PATH` owns protocol state.
- The runtime opens it independently from the audit database.
- The same database is the reference implementation for TaskStore and
  PushConfigStore.
- The standard HTTP push sender is created by composition, not by the protocol
  state adapter.
- The database participates in health checks.

### Acceptance

- Create/get/update/list tasks survive process close/reopen.
- Push config CRUD survives process close/reopen.
- Version conflicts return `taskstore.ErrConcurrentModification`.
- Cross-owner reads and writes are masked as not found.
- Pagination is deterministic across equal timestamps.
- `make check` and race-enabled tests pass.

### 6. Runtime operations

**Goal:** every advertised method is testable through the official client.

Requirements:

- `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`, streaming, and
  push-config CRUD have integration tests.
- Cancellation produces `TASK_STATE_CANCELED`, closes audit, and emits a push
  event when configured.
- Unsupported capabilities return standard A2A errors.
- A task cannot remain permanently working after a process restart.

### 7. Observability and security

Requirements:

- Metrics cover push config changes and delivery outcomes.
- Stable log event names do not include secrets.
- HTTPS and private-network policy are tested.
- Cross-owner task/config access is tested.
- SQLite files use restrictive permissions.

**Status:** Prometheus push config and delivery metrics implemented. The
AG-UI Go SDK has been refreshed, but its current official event model still
has no canceled `RUN_FINISHED` outcome; implementing a private outcome is
prohibited.

## Implementation sequence

1. Authenticated principal in the A2A call context.
2. Accurate push capability declaration and official SDK wiring.
3. Official-client conformance tests for push CRUD and event delivery.
4. Durable SQLite `push.ConfigStore`.
5. Durable SQLite `taskstore.Store`.
6. Attempt-based audit/runtime lifecycle.
7. Input-required/auth-required continuation.
8. Startup recovery and cancellation/subscription hardening.
9. AG-UI lifecycle correction and SDK upgrade.

## Definition of done

- Every Agent Card `true` capability passes official-client conformance tests.
- Every unsupported capability returns a standard A2A error.
- Push payloads decode as `a2a.StreamResponse`.
- Task ownership and callback ownership cannot be crossed.
- `ListTasks` works for an authenticated principal.
- Restart does not lose registered callbacks or task state.
- `make check`, race-enabled package tests, and protocol conformance suites
  pass.

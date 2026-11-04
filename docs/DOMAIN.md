# Runtime Domain Model

`agent-runtime` is a generic multi-agent runtime. Prompts, skill workflow, tool
semantics, domain schemas, and domain evaluation belong to the domain content
skill/project.

## Core runtime concepts

| Concept | Meaning |
|---|---|
| AgentDeployment | External deployment artifact containing one or more Agent definitions |
| AgentDefinition | One Agent entity within a deployment: instruction, optional output schema, server-scoped tool allowlist, and per-Agent model settings with runtime-wide fallback |
| AgentReference | A delegation edge from one AgentDefinition to another by Agent name |
| MCP server | A logical external tool dependency bound through environment references |
| RunExecution | One execution aggregate identified by `RunID`; `ThreadID` is caller-owned correlation, not execution identity. The aggregate is implemented by the runtime-internal `runExecution` type in `internal/agent`; the domain documents its invariants |
| ToolCall | One allowlisted MCP tool invocation and its lifecycle within a RunExecution |
| LLMCall | One auditable model invocation and its lifecycle within a RunExecution |
| Delegation | A parent Agent delegating a subtask to a sub-expert; a child lifecycle of RunExecution |
| Audit event | An immutable domain fact containing lifecycle evidence |
| ADK working session | AG-UI owns a run-scoped in-process session (`run:<RunID>`), deleted at terminal state; A2A ContextID owns the durable ADK session |
| Skills binding | The artifact-declared skills root plus the built-in skill tools (list/load/resource) |

## AgentDeployment invariants

1. The artifact is loaded from the filesystem and cannot be uploaded at runtime.
2. Prompt and output schema are external files, not Go defaults.
3. `output.textPointer` selects the user-facing string from validated output.
4. An empty tool allowlist means the Agent has no tool access.
5. Tool authorization is scoped to a declared logical MCP server and explicit
   tool name.
6. Instruction and schema digests identify loaded behavior; a plain-text Agent
   has an empty schema digest.
7. Model name, temperature, output-token bound, and skills binding are
   behavior-bearing settings and enter semantic identity.
8. Deployment, instruction, and schema digests enter audit evidence.

## Aggregates and domain facts

### AgentDeployment aggregate

`AgentDeployment` is the aggregate root for the external deployment artifact.
`metadata.name` and `metadata.version` identify the artifact within the
deployment language. `Digest` is its canonical semantic identity;
`ManifestDigest` separately identifies exact source bytes. Semantic identity is
stable across JSON formatting but changes whenever behavior-bearing declarations
or loaded instruction/schema content change.

`AgentDefinition` is an entity inside this aggregate. Its `Name` is unique only
within one `AgentDeployment`; its behavior identity is the Agent definition
digest. `AgentReference` is a value object representing a delegation edge.

| Invariant | Single implementation |
|---|---|
| Agent names are unique | `deployment_validate.go` |
| There is exactly one entrypoint and every Agent is reachable | `deployment_validate.go` |
| The delegation graph is acyclic | `deployment_validate.go` |
| Tool allowlists refer only to declared MCP servers | `deployment_validate.go` |
| Tool access is explicit and deny-by-default | `ToolAllowlist` and ADK graph filtering |
| Protocol discovery uses a read-only, copy-out view without loaded instructions or schemas | `DeploymentView` (`view.go`) |

### RunExecution aggregate

`RunExecution` is the aggregate root for one execution. `RunID` is its
aggregate identity. `ThreadID` and `UserID` are caller-owned correlation data,
not execution identity. LLM calls, tool calls, and delegations are child
lifecycles of this aggregate.

| Invariant | Single implementation |
|---|---|
| A production run's identity is durably reserved before its in-memory lifecycle begins | `ensureDurableRunID` and the `RunReservationStore` port |
| An active `RunID` is idempotent and exclusive | `runExecutionManager` (`run_lifecycle.go`) |
| Terminal evidence is appended exactly once and in order | `closeRunEvidence` (`run_evidence.go`) |
| Closing order is close in-flight → compensate → terminal event → release state | `closeRunEvidence` (`run_evidence.go`) |
| One close claim is exclusive; failed closure rolls back to active before retry | `runExecutionManager` (`run_lifecycle.go`) |
| A failed terminal append is retried without reusing the RunID or releasing evidence bookkeeping | Run-owned terminal reconciliation |

### Audit facts

An `AuditEvent` is an immutable domain fact, not a mutable aggregate. Its
`EventID` provides append idempotency. The audit adapter and startup recovery
own the append-only persistence invariants.

| Invariant | Single implementation |
|---|---|
| Audit events are append-only | `internal/audit/sqlite` |
| Lifecycle events close with stable IDs | audit event constructors |
| Startup interruption recovery appends, never edits | `RecoverInterrupted` |

## Execution invariants

1. Only allowlisted MCP tools can be exposed or invoked.
2. Tool input/output are audited as canonical digests and sizes, never raw payload.
3. Run, thread, and user identifiers are required; an active RunID is exclusive.
4. AG-UI working sessions are run-scoped, held in-process, and removed only
   after durable terminal evidence. A2A attempt audit does not delete its
   ContextID-owned ADK session.
5. Model-text capture and protocol projection use one per-author visibility
   policy: plain Agents may expose model text; structured Agents never expose
   raw model JSON.
6. Structured output is optional and validated against the Agent's external
   JSON Schema.
7. `RunResult.Output` is generic validated JSON for structured Agents.
8. `RunResult.Text` is final model text for plain Agents or the configured
   JSON Pointer value for structured Agents.
9. Failed runs mark pending executions and delegations as interrupted so every
   lifecycle has terminal audit evidence.

## Audit invariants

1. Audit events are append-only at the database boundary.
2. Every run has started and terminal lifecycle evidence.
3. Every model call has started and terminal lifecycle evidence.
4. Every tool call has started and terminal lifecycle evidence, including
   interrupted calls.
5. Every delegation has started and terminal lifecycle evidence, including
   interrupted delegations.
6. A hard interruption is represented at startup by an append-only synthetic
   `runtime_interrupted` terminal failure, never by editing an old event.
7. Audit records protocol, trace correlation, version, and digest provenance,
   but not secrets, prompts, raw model output, or tool payloads.
8. Protocol task correlation is recorded as `TaskID`. A delegation is a
   lifecycle within its RunExecution, so it carries a stable invocation-scoped
   `delegation_id` rather than a synthetic parent run.
9. Tool and delegation lifecycle terminal transitions are pure functions in
   `internal/evidence`; auditors own only callback correlation and durable
   append ordering.
10. Audit write and recovery failures fail closed; cancellation does not prevent
   terminal audit events.
11. A reconciled terminal event (interrupted run, model call, tool, or
   delegation) carries the same trace and definition provenance as its started
   fact.

## Ownership boundary

Callers own users, products, entitlements, quota, and durable conversations.
The domain content repository owns domain behavior and MCP tool semantics.
`agent-runtime` owns generic execution, protocol adaptation, evidence, and
operations.

## Protocol-owned A2A task invariants

An A2A task is a protocol-owned aggregate. Its identity, owner, and ContextID
are immutable once created; lifecycle state, history, artifacts, and status may
evolve under optimistic concurrency. Push configurations are child state and can
exist only for an existing task owned by the same principal.
Cross-owner duplicate task creation reports not-found rather than confirming
another principal's aggregate.

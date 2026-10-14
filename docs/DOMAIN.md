# Runtime Domain Model

`liki-agent-runtime` is a generic multi-agent runtime. Prompts, skill workflow, tool
semantics, domain schemas, and domain evaluation belong to the Liki
skill/project.

## Core runtime concepts

| Concept | Meaning |
|---|---|
| AgentDeployment | External deployment artifact containing one or more Agent definitions |
| AgentDefinition | One Agent's instruction, optional output schema, server-scoped tool allowlist, and per-Agent model settings with runtime-wide fallback |
| MCP server | A logical external tool dependency bound through environment references |
| Run | One stateless execution identified by `RunID` and `ThreadID` |
| Tool execution | One allowlisted MCP tool invocation |
| LLM call | One auditable model invocation inside a run |
| Audit event | Immutable evidence of a lifecycle fact |

## AgentDeployment invariants

1. The artifact is loaded from the filesystem and cannot be uploaded at runtime.
2. Prompt and output schema are external files, not Go defaults.
3. `output.textPointer` selects the user-facing string from validated output.
4. An empty tool allowlist means the Agent has no tool access.
5. Tool authorization is scoped to a declared logical MCP server and explicit
   tool name.
6. Instruction and schema digests identify exact behavior; a plain-text Agent
   has an empty schema digest.
7. Deployment, instruction, and schema digests enter audit evidence.

## Execution invariants

1. Only allowlisted MCP tools can be exposed or invoked.
2. Tool input/output are audited as canonical digests and sizes, never raw payload.
3. Run, thread, and user identifiers are required; an active RunID is exclusive.
4. ADK working sessions are run-scoped and removed at terminal execution.
5. Plain text is the default output mode.
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
8. Audit write and recovery failures fail closed; cancellation does not prevent
   terminal audit events.
9. A reconciled terminal event (interrupted run, model call, tool, or
   delegation) carries the same trace and definition provenance as its started
   fact.

## Ownership boundary

`liki-web` owns users, products, entitlements, quota, and durable conversations.
The Liki skill/project owns domain behavior and MCP tool semantics.
`liki-agent-runtime` owns generic execution, protocol adaptation, evidence, and
operations.

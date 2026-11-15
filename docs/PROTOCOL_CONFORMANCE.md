# Protocol conformance

This matrix separates mandatory protocol behavior, advertised capability, and
fail-closed behavior. It is tested in this repository; protocol smoke is never a
image-liveness check.

## A2A

| Contract | Status | Verification |
|---|---|---|
| Agent Card at official well-known path | implemented | transport test |
| JSON-RPC transport | implemented | A2A server tests |
| `SendMessage` | implemented | A2A server tests |
| `SendStreamingMessage` | implemented | official-client tests |
| `CancelTask` | implemented | active-cancel, duplicate-cancel, terminal-cancel tests |
| `SubscribeToTask` | implemented | active-subscription and terminal-op tests |
| Cross-replica cancellation | unsupported / fail-closed | single-process deployment contract |

## AG-UI

| Contract | Status | Verification |
|---|---|---|
| Official `RunAgentInput` over SSE | implemented | adapter tests |
| Mandatory `RUN_STARTED` / `RUN_FINISHED` / `RUN_ERROR` lifecycle | implemented | adapter tests |
| Text message lifecycle | implemented | adapter tests |
| Tool call lifecycle | implemented | runtime and adapter tests |
| Sub-agent attribution | implemented | subagent stream tests |
| Human-in-the-loop interrupts | implemented | runtime pause and adapter interrupt tests |
| `RUN_FINISHED.outcome.type=interrupt` | implemented | adapter interrupt test |
| `MESSAGES_SNAPSHOT` at interrupt boundary | implemented | adapter interrupt test |
| `RunAgentInput.resume` | implemented | adapter resume projection test |
| Original `toolCallId` result on resume | implemented | adapter resume test |
| Frontend tools via success `pendingToolCallIds` | unsupported / fail-closed | client tools pause as approval interrupts |
| General shared-state `RunAgentInput.state` | unsupported / fail-closed | empty state accepted; non-empty rejected |
| Parent runs | unsupported / fail-closed | profile validation |
| Multimodal messages | unsupported / fail-closed | profile validation |

AG-UI client tools declared by the trusted gateway are await-result tools. The
runtime emits `TOOL_CALL_START`, `TOOL_CALL_ARGS`, and `TOOL_CALL_END`, then
pauses on the ADK long-running tool. It never invents a client result. A resumed
run emits `TOOL_CALL_RESULT` only for an authoritative client-supplied result.

Interrupt correlation follows the official interrupt contract: same `threadId`,
every open interrupt addressed, expiry judged and enforced by the trusted
protocol client, and a resumed approved tool emits against the original
`toolCallId`. An expired interrupt cannot be resolved but can still be
abandoned.

The runtime is a stateless platform component: it does not derive a business
`responseSchema`, does not emit `STATE_SNAPSHOT` for product state, and does not
carry UI copy on interrupts. The business payload shape and the decision to
accept or cancel belong to the trusted protocol client, which owns the resume
payload it sends back with `RunAgentInput.resume`.

## MCP

| Contract | Status |
|---|---|
| Official Streamable HTTP client transport | implemented |
| Tool discovery and allowlist enforcement | implemented |
| Required-tool readiness and timeout | implemented |
| Catalog cache | implemented |
| Product tool semantics | owned by deployed MCP service, not runtime |

## Operational boundary

`/livez` proves process liveness only. `/readyz` checks dependencies and drain
state. Neither protocol endpoint is used as image startup health.

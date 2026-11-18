# Examples

These examples are agentgo consumers. They are not part of the runtime and do
not add product capabilities to it.

Each example is intentionally self-contained so that its deployment artifact,
UI, configuration, and instructions can be understood without reading hidden
shared code.

## Shared domain vocabulary

| Term | Meaning |
|---|---|
| `RuntimeCorrelation` | Ephemeral `threadId` and `userId` used to correlate one browser demo's AG-UI stream. Reload intentionally creates new values. |
| `ConversationMessage` | One user or assistant turn. `content` is the visible text and the AG-UI message content. |
| `AgentRun` | One AG-UI request/response lifecycle. A run is active until it finishes, fails, or is stopped. |
| `AgentRunPhase` | `idle`, `connecting`, `running`, `streaming`, `complete`, `stopped`, or `error`; set from AG-UI event callbacks or the local abort path. |
| `SelectedPageElement` | The visible page content selected for explanation, limited to 600 characters. |
| `ResponseLanguage` | Explicit `navigator.language`, shown as **Browser language** and included in the selection prompt. |

Runnable examples own their local Makefile. The repository root Makefile does
not register individual examples.

Use the local Makefile from inside an example directory. Keep example lifecycle
targets consistent across examples: `setup`, `run`, `start`, `logs`, `stop`,
and `clean`.

| Example | Purpose |
|---|---|
| [`simple-chat`](./simple-chat) | Minimal in-memory browser chat showing AG-UI streaming and task state |
| [`agentgo-assistant`](./agentgo-assistant) | Minimal click-to-explain assistant showing small selected-element context |

## simple-chat

**What it is:** the shortest browser conversation path.
**What to look at:** streaming text and the status pill moving through
connecting, running, streaming, complete, stopped, or failed states.
**Port:** `8080`.

```bash
cd examples/simple-chat
make setup
# Edit .env: set AGENTGO_LLM_API_KEY.
make run
```

![agentgo simple chat animated demo](./simple-chat/assets/simple-chat-demo.gif)

## agentgo-assistant

**What it is:** an embedded page-aware assistant demo.
**What to look at:** selecting visible page content, a small
selected page content, explicit browser response language, and streamed
explanations. Page links and buttons are not activated by an explain click.
**Port:** `8081`.

```bash
cd examples/agentgo-assistant
make setup
# Edit .env: set AGENTGO_LLM_API_KEY.
make run
```

![agentgo assistant animated demo](./agentgo-assistant/assets/assistant-demo.gif)

## Common rules

Both examples are self-contained agentgo consumers. They use the same local
lifecycle targets and send public protocol requests to agentgo. They do not add
product capabilities, user management, or session storage to the runtime.

# Examples

These examples are agentgo consumers. They are not part of the runtime and do
not add product capabilities to it.

Each example is intentionally self-contained so that its deployment artifact,
UI, configuration, and instructions can be understood without reading hidden
shared code.

Runnable examples own their local Makefile. The repository root Makefile does
not register individual examples.

Use the local Makefile from inside an example directory. Keep example lifecycle
targets consistent across examples: `setup`, `run`, `start`, `logs`, `stop`,
and `clean`.

| Example | Purpose |
|---|---|
| [`simple-chat`](./simple-chat) | Run a minimal browser chat against one agent |

Start the current example with:

```bash
cd examples/simple-chat
make setup
# Edit .env, then:
make run
```

Example UIs use public runtime protocols such as AG-UI. agentgo itself does not
serve a production chat UI; production callers own their user experience.

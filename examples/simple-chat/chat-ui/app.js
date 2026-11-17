    import { HttpAgent } from "./vendor/ag-ui-client.js";

    const conversation = document.querySelector("#conversation");
    const prompt = document.querySelector("#prompt");
    const send = document.querySelector("#send");
    const stop = document.querySelector("#stop");
    const status = document.querySelector("#status");
    const storageKey = "agentgo-simple-chat";
    let currentAgent;
    let history = [];

    function loadState() {
      try {
        const state = JSON.parse(localStorage.getItem(storageKey) ?? "null");
        if (
          state &&
          typeof state.threadId === "string" &&
          typeof state.userId === "string" &&
          Array.isArray(state.messages)
        ) {
          return state;
        }
      } catch {
        // A malformed local snapshot is treated like a new conversation.
      }
      return {
        threadId: crypto.randomUUID(),
        userId: crypto.randomUUID(),
        messages: [],
      };
    }

    const state = loadState();
    history = state.messages;

    function persistState() {
      localStorage.setItem(storageKey, JSON.stringify(state));
    }

    function setStatus(message, isError = false) {
      status.textContent = message;
      status.classList.toggle("error", isError);
    }

    function appendMessage(role, content, id = crypto.randomUUID()) {
      const message = { id, role, content };
      history.push(message);
      state.messages = history;
      const element = document.createElement("article");
      element.className = `message ${role}`;
      element.dataset.id = id;
      element.textContent = content;
      conversation.append(element);
      element.scrollIntoView({ block: "end" });
      return message;
    }

    function renderMessage(message) {
      const element = conversation.querySelector(
        `[data-id="${CSS.escape(message.id)}"]`,
      );
      if (element) element.textContent = message.content;
    }

    function setRunning(value) {
      send.disabled = value;
      stop.disabled = !value;
    }

    function restoreMessages() {
      for (const message of history) {
        const element = document.createElement("article");
        element.className = `message ${message.role}`;
        element.dataset.id = message.id;
        element.textContent = message.content;
        conversation.append(element);
      }
    }

    async function sendMessage() {
      const content = prompt.value.trim();
      if (!content || currentAgent) return;

      prompt.value = "";
      appendMessage("user", content);
      persistState();
      setRunning(true);
      setStatus("connecting");

      let assistant;
      const agent = new HttpAgent({
        headers: { "X-Agent-User-ID": state.userId },
        initialMessages: structuredClone(history),
        threadId: state.threadId,
        url: "/ag-ui",
      });
      currentAgent = agent;

      try {
        const result = await agent.runAgent({ runId: crypto.randomUUID() }, {
          onRunStartedEvent() { setStatus("running"); },
          onTextMessageStartEvent({ event }) {
            assistant = appendMessage("assistant", "", event.messageId);
          },
          onTextMessageContentEvent({ event }) {
            assistant ??= appendMessage("assistant", "", event.messageId);
            assistant.content += event.delta;
            renderMessage(assistant);
            persistState();
          },
          onToolCallStartEvent({ event }) {
            setStatus(`tool: ${event.toolCallName}`);
          },
          onRunFinishedEvent() { setStatus("complete"); },
          onRunErrorEvent({ event }) {
            setStatus(event.message || "agent run failed", true);
          },
          onRunFailed({ error }) { setStatus(error.message, true); },
        });

        if (!assistant) {
          const answer = result.newMessages
            .filter((message) => message.role === "assistant")
            .at(-1)?.content;
          if (typeof answer === "string" && answer.length > 0) {
            appendMessage("assistant", answer);
          } else {
            setStatus("agent returned no text", true);
          }
        }
        persistState();
      } catch (error) {
        if (error instanceof DOMException && error.name === "AbortError") {
          setStatus("stopped");
        } else {
          setStatus(error instanceof Error ? error.message : String(error), true);
        }
      } finally {
        currentAgent = undefined;
        setRunning(false);
      }
    }

    document.querySelector("form").addEventListener("submit", (event) => {
      event.preventDefault();
      void sendMessage();
    });
    stop.addEventListener("click", () => currentAgent?.abortRun());

    restoreMessages();

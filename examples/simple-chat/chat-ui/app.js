import { HttpAgent } from "./vendor/ag-ui-client.js";

const conversationElement = document.querySelector("#conversation");
const emptyNotice = document.querySelector("#conversation-empty");
const promptElement = document.querySelector("#conversation-input");
const sendButton = document.querySelector("#conversation-send");
const stopButton = document.querySelector("#conversation-stop");
const runPhaseElement = document.querySelector("#agent-run-phase");
const runPhaseLabel = document.querySelector("#agent-run-phase-label");
const runDetailElement = document.querySelector("#agent-run-detail");

// AG-UI requires IDs to correlate one conversation stream. The demo owns no
// durable session; reloading intentionally starts an empty conversation.
const runtimeCorrelation = {
  threadId: crypto.randomUUID(),
  userId: crypto.randomUUID(),
};

const RUN_PHASE_LABELS = {
  idle: "Ready",
  connecting: "Connecting",
  running: "Agent running",
  streaming: "Streaming",
  complete: "Complete",
  stopped: "Stopped",
  error: "Failed",
};

let activeAgentRun;
let conversationMessages = [];

function setAgentRunPhase(phase, detail = "") {
  runPhaseElement.dataset.phase = phase;
  conversationElement.dataset.phase = phase;
  runPhaseLabel.textContent = RUN_PHASE_LABELS[phase];
  runDetailElement.textContent = detail;
}

function scrollConversation() {
  conversationElement.scrollTop = conversationElement.scrollHeight;
}

function appendConversationMessage({ role, content, id = crypto.randomUUID() }) {
  const message = { id, role, content };
  conversationMessages.push(message);
  emptyNotice.textContent = "";

  const element = document.createElement("article");
  element.className = `conversation-message ${role}`;
  element.dataset.id = id;
  element.textContent = content;
  conversationElement.append(element);
  scrollConversation();
  return message;
}

function renderConversationMessage(message) {
  const element = conversationElement.querySelector(`[data-id="${CSS.escape(message.id)}"]`);
  if (element) {
    element.textContent = message.content;
    scrollConversation();
  }
}

function setRunControlsEnabled(enabled) {
  sendButton.disabled = !enabled;
  stopButton.disabled = enabled;
}

function toProtocolMessages() {
  return conversationMessages.map(({ id, role, content }) => ({ id, role, content }));
}

async function sendConversationMessage() {
  const content = promptElement.value.trim();
  if (!content || activeAgentRun) return;

  promptElement.value = "";
  appendConversationMessage({ role: "user", content });
  setRunControlsEnabled(false);
  setAgentRunPhase("connecting");

  let assistantReply;
  const agentRun = new HttpAgent({
    headers: { "X-Agent-User-ID": runtimeCorrelation.userId },
    initialMessages: toProtocolMessages(),
    threadId: runtimeCorrelation.threadId,
    url: "/ag-ui",
  });
  activeAgentRun = agentRun;

  try {
    const result = await agentRun.runAgent({ runId: crypto.randomUUID() }, {
      onRunStartedEvent() { setAgentRunPhase("running", "AG-UI run started"); },
      onTextMessageStartEvent() { setAgentRunPhase("streaming", "assistant reply started"); },
      onTextMessageContentEvent({ event }) {
        assistantReply ??= appendConversationMessage({ role: "assistant", content: "", id: event.messageId });
        assistantReply.content += event.delta;
        renderConversationMessage(assistantReply);
      },
      onRunFinishedEvent() { setAgentRunPhase("complete", "AG-UI run finished"); },
      onRunErrorEvent({ event }) {
        setAgentRunPhase("error", event.message || "AG-UI run error");
      },
      onRunFailed({ error }) { setAgentRunPhase("error", error.message); },
    });

    if (!assistantReply) {
      const reply = result.newMessages
        .filter((message) => message.role === "assistant")
        .at(-1)?.content;
      if (typeof reply === "string" && reply.length > 0) {
        appendConversationMessage({ role: "assistant", content: reply });
      } else {
        setAgentRunPhase("error", "agent returned no reply");
      }
    }
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      setAgentRunPhase("stopped", "run aborted");
    } else {
      setAgentRunPhase("error", error instanceof Error ? error.message : String(error));
    }
  } finally {
    activeAgentRun = undefined;
    setRunControlsEnabled(true);
  }
}

document.querySelector("form").addEventListener("submit", (event) => {
  event.preventDefault();
  void sendConversationMessage();
});

promptElement.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  event.preventDefault();
  void sendConversationMessage();
});

stopButton.addEventListener("click", () => activeAgentRun?.abortRun());

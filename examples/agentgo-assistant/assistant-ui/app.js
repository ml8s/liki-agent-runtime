import { HttpAgent } from "./vendor/ag-ui-client.js";

const assistantLauncher = document.querySelector("#assistant-launcher");
const assistantDialog = document.querySelector("#assistant-dialog");
const conversationElement = document.querySelector("#conversation");
const conversationInput = document.querySelector("#conversation-input");
const sendButton = document.querySelector("#conversation-send");
const stopButton = document.querySelector("#conversation-stop");
const pageSelectionToggle = document.querySelector("#page-selection-mode");
const runDetailElement = document.querySelector("#agent-run-detail");
const responseLanguageElement = document.querySelector("#response-language");
const agentRunPhaseElement = document.querySelector("#agent-run-phase");
const agentRunPhaseLabel = document.querySelector("#agent-run-phase-label");

const PAGE_SELECTION_SELECTOR = "main h1, main h2, main h3, main p, main li, main a, main button, main code, main pre";
const MAX_SELECTED_PAGE_ELEMENT_LENGTH = 600;

const RUN_PHASE_LABELS = {
  idle: "Ready",
  connecting: "Connecting",
  running: "Agent running",
  streaming: "Streaming",
  complete: "Complete",
  stopped: "Stopped",
  error: "Failed",
};

const runtimeCorrelation = {
  threadId: crypto.randomUUID(),
  userId: crypto.randomUUID(),
};

let activeAgentRun;
let conversationMessages = [];

function setRunDetail(message, isError = false) {
  runDetailElement.textContent = message;
  runDetailElement.classList.toggle("error", isError);
}

function setAgentRunPhase(phase, detail = "") {
  agentRunPhaseElement.dataset.phase = phase;
  agentRunPhaseLabel.textContent = RUN_PHASE_LABELS[phase];
  if (phase === "streaming") conversationElement.dataset.phase = phase;
  else delete conversationElement.dataset.phase;
  if (detail) setRunDetail(detail, phase === "error");
}

function scrollConversation() {
  conversationElement.scrollTop = conversationElement.scrollHeight;
}

function appendConversationMessage({ role, content, id = crypto.randomUUID() }) {
  const message = { id, role, content };
  conversationMessages.push(message);

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

function openAssistantDialog() {
  assistantDialog.hidden = false;
  assistantLauncher.setAttribute("aria-expanded", "true");
  scrollConversation();
  conversationInput.focus();
}

function closeAssistantDialog() {
  assistantDialog.hidden = true;
  assistantLauncher.setAttribute("aria-expanded", "false");
}

function getSelectedPageElementText(target) {
  return target.textContent.replace(/\s+/g, " ").trim().slice(0, MAX_SELECTED_PAGE_ELEMENT_LENGTH);
}

function getResponseLanguage() {
  return navigator.language || "en";
}

function showResponseLanguage() {
  responseLanguageElement.textContent = `Browser language: ${getResponseLanguage()}`;
}

function toProtocolMessages() {
  return conversationMessages.map(({ id, role, content }) => ({ id, role, content }));
}

function highlightSelectedPageElement(target) {
  target.classList.add("selected-page-element");
  window.setTimeout(() => target.classList.remove("selected-page-element"), 1200);
}

function buildPageSelectionPrompt(target) {
  const content = getSelectedPageElementText(target);
  const language = getResponseLanguage();
  return `Explain this selected page content for a web user.\n\n${content}\n\nResponse language: ${language}`;
}

function setRunControlsActive() {
  setRunControlsEnabled(false);
  setAgentRunPhase("connecting");
}

function finishAgentRun() {
  activeAgentRun = undefined;
  setRunControlsEnabled(true);
}

async function sendConversationMessage(content) {
  if (activeAgentRun) {
    setRunDetail("assistant is busy");
    return;
  }

  appendConversationMessage({ role: "user", content });
  setRunControlsActive();

  let assistantReply;
  const agentRun = new HttpAgent({
    headers: { "X-Agent-User-ID": runtimeCorrelation.userId },
    initialMessages: toProtocolMessages(),
    threadId: runtimeCorrelation.threadId,
    url: "/ag-ui",
  });
  activeAgentRun = agentRun;

  try {
    await agentRun.runAgent({ runId: crypto.randomUUID() }, {
      onRunStartedEvent() { setAgentRunPhase("running", "AG-UI run started"); },
      onTextMessageStartEvent({ event }) {
        setAgentRunPhase("streaming", "assistant reply started");
        assistantReply = appendConversationMessage({ role: "assistant", content: "", id: event.messageId });
      },
      onTextMessageContentEvent({ event }) {
        assistantReply ??= appendConversationMessage({ role: "assistant", content: "", id: event.messageId });
        assistantReply.content += event.delta;
        renderConversationMessage(assistantReply);
      },
      onRunFinishedEvent() { setAgentRunPhase("complete", "AG-UI run finished"); },
      onRunErrorEvent({ event }) { setAgentRunPhase("error", event.message || "AG-UI run error"); },
      onRunFailed({ error }) { setRunDetail(error.message, true); },
    });

    ensureRunProducedReply(assistantReply);
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      setAgentRunPhase("stopped", "run aborted");
    } else {
      setAgentRunPhase("error", error instanceof Error ? error.message : String(error));
    }
  } finally {
    finishAgentRun();
  }
}

function ensureRunProducedReply(assistantReply) {
  if (assistantReply?.content) return;
  const reply = conversationMessages.findLast((message) => message.role === "assistant");
  if (!reply?.content) setAgentRunPhase("error", "agent returned no reply");
}

function explainSelectedPageElement(target) {
  highlightSelectedPageElement(target);
  openAssistantDialog();
  void sendConversationMessage(buildPageSelectionPrompt(target));
}

function submitConversationInput() {
  const content = conversationInput.value.trim();
  if (!content || activeAgentRun) return;
  conversationInput.value = "";
  void sendConversationMessage(content);
}

function wireEvents() {
  assistantLauncher.addEventListener("click", () => {
    if (assistantDialog.hidden) openAssistantDialog();
    else closeAssistantDialog();
  });

  document.addEventListener("click", (event) => {
    if (!pageSelectionToggle.checked) return;
    if (!(event.target instanceof Element)) return;
    if (assistantDialog.contains(event.target) || assistantLauncher.contains(event.target)) return;

    const target = event.target.closest(PAGE_SELECTION_SELECTOR);
    if (!target) return;

    event.preventDefault();
    event.stopPropagation();
    explainSelectedPageElement(target);
  }, true);

  document.querySelector("#assistant-dialog form").addEventListener("submit", (event) => {
    event.preventDefault();
    submitConversationInput();
  });

  conversationInput.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    submitConversationInput();
  });

  stopButton.addEventListener("click", () => activeAgentRun?.abortRun());
}

showResponseLanguage();
wireEvents();

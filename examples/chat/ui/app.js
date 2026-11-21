import { HttpAgent } from "./vendor/ag-ui-client.js";
import {
  applyRecognitionResults,
  beginHold,
  classifyMicrophoneError,
  createVoiceInput,
  finishRelease,
  releaseHold,
  requestMicrophoneAccess,
  stopMicrophoneStream,
  VoiceInputState,
} from "./src/voice-input.js";

const els = {
  conversation: document.querySelector("#conversation"),
  empty: document.querySelector("#conversation-empty"),
  phase: document.querySelector("#agent-run-phase"),
  phaseLabel: document.querySelector("#agent-run-phase-label"),
  phaseDetail: document.querySelector("#agent-run-detail"),
  composer: document.querySelector("#composer"),
  inputShell: document.querySelector("#input-shell"),
  draft: document.querySelector("#draft-input"),
  cancelZone: document.querySelector("#cancel-zone"),
  cancelHelp: document.querySelector("#cancel-help"),
  send: document.querySelector("#send-button"),
  stop: document.querySelector("#stop-button"),
  speakReplies: document.querySelector("#speak-replies"),
};

// IDs are ephemeral. Reloading intentionally starts a new browser-local demo.
const runtimeCorrelation = {
  threadId: crypto.randomUUID(),
  userId: crypto.randomUUID(),
};

const runPhaseLabels = {
  idle: "Ready",
  connecting: "Connecting",
  running: "Agent running",
  streaming: "Streaming",
  complete: "Complete",
  stopped: "Stopped",
  error: "Failed",
};

const HOLD_DELAY_MS = 280;

let activeAgentRun;
let conversationMessages = [];
let voiceInput = createVoiceInput();
let recognition;
let microphoneStream;
let holdPointerId;
let holdTimer;
let holdActive;
let holdCancelled;
let preVoiceDraft;
let holdStart;
let captureRequestId = 0;

function setRunPhase(phase, detail = "") {
  els.phase.dataset.phase = phase;
  els.phaseLabel.textContent = runPhaseLabels[phase] || phase;
  els.phaseDetail.textContent = detail;
}

function setControls(enabled) {
  els.send.disabled = !enabled || Boolean(activeAgentRun);
  els.stop.hidden = !activeAgentRun;
  els.draft.disabled = !enabled;
  els.composer.classList.toggle("busy", Boolean(activeAgentRun));
}

function setSendVisibility() {
  els.send.classList.toggle("visible", els.draft.value.trim().length > 0);
}

function autoResizeDraft() {
  els.draft.style.height = "auto";
  els.draft.style.height = `${Math.min(els.draft.scrollHeight, 120)}px`;
}

function submittedText(input) {
  return [
    input.finalTranscript,
    input.interimTranscript,
  ].filter(Boolean).join(" ").trim();
}

function recognitionErrorMessageText(code) {
  const messages = {
    "not-allowed": "Microphone permission is blocked. Allow microphone for this site, then retry.",
    "service-not-allowed": "Speech recognition service is unavailable. Type instead.",
    "no-speech": "No speech was detected.",
    "audio-capture": "No microphone was exposed to the browser.",
    network: "Speech recognition network failed.",
  };
  return messages[code] || `Voice error: ${code}`;
}

function scrollConversation() {
  els.conversation.scrollTop = els.conversation.scrollHeight;
}

function appendMessage({ role, content, id = crypto.randomUUID() }) {
  const message = { id, role, content };
  conversationMessages.push(message);
  els.empty.textContent = "";

  const element = document.createElement("article");
  element.className = `conversation-message ${role === "assistant" ? "agent" : role}`;
  element.dataset.id = id;
  element.textContent = content;
  els.conversation.append(element);
  scrollConversation();
  return message;
}

function renderMessage(message) {
  const element = els.conversation.querySelector(
    `[data-id="${CSS.escape(message.id)}"]`,
  );
  if (element) {
    element.textContent = message.content;
    scrollConversation();
  }
}

function toProtocolMessages() {
  return conversationMessages.map(({ id, role, content }) => ({
    id,
    role,
    content,
  }));
}

function setVoiceHelp(text) {
  els.cancelHelp.textContent = text;
}

function setRecordingUi(recording, armed = false) {
  els.inputShell.classList.toggle("recording", recording);
  els.draft.classList.toggle("recording", recording);
  els.cancelZone.classList.toggle("visible", recording);
  els.cancelZone.classList.toggle("armed", armed);
  els.cancelZone.setAttribute("aria-hidden", String(!recording));
  els.composer.classList.toggle("hold-active", recording);
}

function setRecordingLabel(text) {
  els.draft.placeholder = text;
}

function clearHoldTimer() {
  clearTimeout(holdTimer);
  holdTimer = undefined;
}

function startHoldFromInput(event) {
  if (activeAgentRun || voiceInput.state !== VoiceInputState.idle) return;

  const pointerId = event.pointerId;
  const startX = event.clientX;
  const startY = event.clientY;
  holdPointerId = pointerId;
  holdCancelled = false;
  preVoiceDraft = els.draft.value;

  holdTimer = setTimeout(() => {
    void beginHoldCapture(pointerId, startX, startY);
  }, HOLD_DELAY_MS);
}

async function beginHoldFromPointer(pointerId, startX, startY) {
  const requestId = ++captureRequestId;
  if (!window.SpeechRecognition && !window.webkitSpeechRecognition) {
    setVoiceHelp("Speech recognition is unavailable in this browser. Type instead.");
    holdPointerId = undefined;
    return;
  }

  holdActive = true;
  voiceInput = {
    ...createVoiceInput(els.draft.value),
    state: VoiceInputState.requesting,
  };
  els.draft.blur();
  setRecordingUi(true);
  setRecordingLabel("Requesting microphone…");
  setVoiceHelp("");

  try {
    microphoneStream = await requestMicrophoneAccess(window);
  } catch (error) {
    restoreDraftAfterVoice();
    setRecordingUi(false);
    setRecordingLabel("Type or press & hold to talk");
    setVoiceHelp(classifyMicrophoneError(error));
    holdActive = false;
    return;
  }

  if (pointerId !== holdPointerId) {
    restoreDraftAfterVoice();
    stopMicrophoneStream(microphoneStream);
    microphoneStream = undefined;
    holdActive = false;
    return;
  }

  recognition = makeRecognition();
  voiceInput = beginHold(voiceInput, "");
  setRecordingLabel("Release to send · slide up to cancel");
  setRecordingUi(true);

  try {
    recognition.start();
  } catch (error) {
    restoreDraftAfterVoice();
    finishHold(false, "Speech recognition could not start.");
    return;
  }

  // A drag that began before getUserMedia resolved can still cancel.
  if (holdCancelled) {
    voiceInput = releaseHold(voiceInput, { submit: false });
    recognition.stop();
  }
}

function makeRecognition() {
  const SpeechRecognitionClass =
    window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SpeechRecognitionClass) return null;

  const currentRecognition = new SpeechRecognitionClass();
  currentRecognition.lang = navigator.language || "en-US";
  currentRecognition.continuous = true;
  currentRecognition.interimResults = true;

  currentRecognition.onresult = (event) => {
    voiceInput = applyRecognitionResults(voiceInput, event);
    const transcript = [
      voiceInput.finalTranscript,
      voiceInput.interimTranscript,
    ].filter(Boolean).join(" ");
    els.draft.value = transcript;
  };

  currentRecognition.onerror = (event) => {
    voiceInput = {
      ...voiceInput,
      state: VoiceInputState.error,
      error: recognitionErrorMessageText(event.error),
    };
  };

  currentRecognition.onend = () => finishHold(true, voiceInput.error || "No speech was detected.");

  return currentRecognition;
}

function releaseCaptureResources() {
  recognition = undefined;
  stopMicrophoneStream(microphoneStream);
  microphoneStream = undefined;
  holdActive = false;
  holdPointerId = undefined;
  clearHoldTimer();
}

function stopHold({ submit = false } = {}) {
  if (voiceInput.state === VoiceInputState.listening) {
    voiceInput = releaseHold(voiceInput, { submit });
    els.draft.value = [
      voiceInput.finalTranscript,
      voiceInput.interimTranscript,
    ].filter(Boolean).join(" ");
  }
  recognition?.stop();
}

function finishHold(submit, fallbackMessage = "") {
  if (voiceInput.state !== VoiceInputState.stopping) return;

  voiceInput = finishRelease(voiceInput);
  releaseCaptureResources();
  setRecordingUi(false);
  setRecordingLabel("Type or press & hold to talk");

  const transcript = voiceInput.releasedTranscript || "";
  if (submit && transcript) {
    els.draft.value = "";
    setSendVisibility();
    submitTranscript(transcript);
    return;
  }

  els.draft.value = preVoiceDraft || fallbackMessage || "";
  setSendVisibility();
  if (fallbackMessage) setVoiceHelp(fallbackMessage);
}

function restoreDraftAfterVoice() {
  els.draft.value = preVoiceDraft || "";
  setSendVisibility();
}

function submitTranscript(content) {
  if (!content || activeAgentRun) return;
  sendConversationMessage(content);
}

function speakReply(text) {
  if (!els.speakReplies.checked || !text || !window.speechSynthesis) return;
  window.speechSynthesis.cancel();
  const utterance = new SpeechSynthesisUtterance(text);
  utterance.lang = navigator.language || "en-US";
  window.speechSynthesis.speak(utterance);
}

function beginHoldCapture(pointerId, startX, startY) {
  holdStart = { x: startX, y: startY };
  setRecordingUi(true);
  return beginHoldFromPointer(pointerId, startX, startY);
}

function stopSpeaking() {
  window.speechSynthesis?.cancel();
}

function submitDraft() {
  const content = els.draft.value.trim();
  if (!content || activeAgentRun) return;
  els.draft.value = "";
  setSendVisibility();
  sendConversationMessage(content);
}

async function sendConversationMessage(content) {
  appendMessage({ role: "user", content });
  setControls(false);
  setRunPhase("connecting");

  let assistantReply;
  const agentRun = new HttpAgent({
    headers: { "X-Agent-User-ID": runtimeCorrelation.userId },
    initialMessages: toProtocolMessages(),
    threadId: runtimeCorrelation.threadId,
    url: "/ag-ui",
  });
  activeAgentRun = agentRun;
  setControls(false);

  try {
    await agentRun.runAgent({ runId: crypto.randomUUID() }, {
      onRunStartedEvent() {
        setRunPhase("running", "AG-UI run started");
      },
      onTextMessageStartEvent({ event }) {
        setRunPhase("streaming", "assistant reply started");
        assistantReply = appendMessage({
          role: "assistant",
          content: "",
          id: event.messageId,
        });
      },
      onTextMessageContentEvent({ event }) {
        assistantReply ??= appendMessage({ role: "assistant", content: "" });
        assistantReply.content += event.delta;
        renderMessage(assistantReply);
      },
      onRunFinishedEvent() {
        setRunPhase("complete", "AG-UI run finished");
        speakReply(assistantReply?.content);
      },
      onRunErrorEvent({ event }) {
        setRunPhase("error", event.message || "AG-UI run error");
      },
      onRunFailed({ error }) {
        setRunPhase("error", error.message);
      },
    });

    if (!assistantReply?.content) {
      setRunPhase("error", "agent returned no reply");
    }
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      setRunPhase("stopped", "run aborted");
    } else {
      setRunPhase("error", error instanceof Error ? error.message : String(error));
    }
  } finally {
    if (activeAgentRun === agentRun) {
      activeAgentRun = undefined;
    }
    setControls(true);
  }
}

els.draft.addEventListener("input", () => {
  setSendVisibility();
  autoResizeDraft();
});

els.draft.addEventListener("pointerdown", (event) => {
  if (event.button !== 0 || activeAgentRun) return;
  holdPointerId = event.pointerId;
  holdCancelled = false;
  preVoiceDraft = els.draft.value;
  holdTimer = setTimeout(() => {
    holdCancelled = false;
    void beginHoldCapture(event.pointerId, event.clientX, event.clientY);
  }, HOLD_DELAY_MS);
});

els.draft.addEventListener("pointermove", (event) => {
  if (!holdActive || event.pointerId !== holdPointerId) return;

  const distance = Math.hypot(event.clientX - holdStart.x, event.clientY - holdStart.y);
  const armed = distance > 72;
  if (armed && !holdCancelled) {
    holdCancelled = true;
    voiceInput = releaseHold(voiceInput, { submit: false });
    els.draft.value = preVoiceDraft;
    setSendVisibility();
  }
  setRecordingUi(true, armed);
  positionCancelZone(event.clientX, event.clientY);
});

function positionCancelZone(pointerX, pointerY) {
  const above = pointerY - 96;
  els.cancelZone.style.top = `${Math.max(72, above)}px`;
}

els.draft.addEventListener("pointerup", (event) => {
  if (event.pointerId !== holdPointerId) return;
  clearHoldTimer();

  if (holdActive) {
    if (!holdCancelled) {
      voiceInput = releaseHold(voiceInput, { submit: true });
      els.draft.value = submittedText(voiceInput);
    }
    recognition?.stop();
    return;
  }

  holdPointerId = undefined;
  els.draft.focus();
});

els.draft.addEventListener("pointercancel", () => {
  clearHoldTimer();
  if (holdActive) {
    holdCancelled = true;
    voiceInput = releaseHold(voiceInput, { submit: false });
    recognition?.stop();
    restoreDraftAfterVoice();
    setRecordingUi(false);
    setRecordingLabel("Type or press & hold to talk");
  }
  holdPointerId = undefined;
});

els.draft.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  event.preventDefault();
  submitDraft();
});

els.composer.addEventListener("submit", (event) => {
  event.preventDefault();
  submitDraft();
});

els.stop.addEventListener("click", () => {
  stopSpeaking();
  activeAgentRun?.abortRun();
});

window.addEventListener("beforeunload", () => {
  captureRequestId += 1;
  recognition?.abort();
  releaseCaptureResources();
  stopSpeaking();
});

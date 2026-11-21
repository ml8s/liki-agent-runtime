export const VoiceInputState = Object.freeze({
  idle: "idle",
  requesting: "requesting",
  listening: "listening",
  stopping: "stopping",
  error: "error",
});

export function createVoiceInput(initialDraft = "") {
  return {
    state: VoiceInputState.idle,
    finalTranscript: "",
    interimTranscript: "",
    cancelRequested: false,
    stopReason: null,
    error: null,
    initialDraft: String(initialDraft || "").trim(),
  };
}

export function beginHold(input, existingDraft = "") {
  return {
    state: VoiceInputState.listening,
    finalTranscript: "",
    interimTranscript: "",
    cancelRequested: false,
    stopReason: null,
    error: null,
    initialDraft: String(existingDraft || "").trim(),
  };
}

export function beginRequest(input) {
  return {
    ...input,
    state: VoiceInputState.requesting,
    error: null,
  };
}

export function applyRecognitionResults(input, event) {
  if (!event || !Number.isInteger(event.resultIndex)) return input;
  if (input.state !== VoiceInputState.listening && input.state !== VoiceInputState.stopping) {
    return input;
  }
  if (input.state === VoiceInputState.stopping && input.cancelRequested) return input;

  let final = "";
  let interim = "";
  for (let index = event.resultIndex; index < event.results.length; index += 1) {
    const result = event.results[index];
    if (result.isFinal) final += result[0].transcript;
    else if (input.state === VoiceInputState.listening) interim += result[0].transcript;
  }

  const finalTranscript = `${input.finalTranscript}${final}`.trim();
  const interimTranscript = input.state === VoiceInputState.listening ? interim.trim() : "";
  return {
    ...input,
    finalTranscript,
    interimTranscript,
  };
}

export function releaseHold(input, { submit = true } = {}) {
  if (input.state !== VoiceInputState.listening) return input;
  return {
    ...input,
    state: VoiceInputState.stopping,
    cancelRequested: !submit,
    stopReason: submit ? "send" : "cancel",
  };
}

export function finishRelease(input) {
  if (input.state !== VoiceInputState.stopping) return input;

  const transcript = input.cancelRequested ? "" : input.finalTranscript;
  return {
    state: VoiceInputState.idle,
    finalTranscript: "",
    interimTranscript: "",
    cancelRequested: false,
    stopReason: null,
    error: null,
    initialDraft: input.initialDraft,
    releasedTranscript: transcript,
  };
}

export function failHold(input, code) {
  if (input.state === VoiceInputState.idle) return input;
  return {
    ...input,
    state: VoiceInputState.error,
    finalTranscript: input.finalTranscript,
    interimTranscript: "",
    error: recognitionErrorMessage(code),
  };
}

export function recognitionErrorMessage(code) {
  const messages = {
    "not-allowed": "Microphone permission is blocked. Allow microphone for this site, then retry.",
    "service-not-allowed": "Speech recognition service is unavailable. Type instead.",
    "no-speech": "No speech was detected.",
    "audio-capture": "No microphone was exposed to the browser.",
    network: "Speech recognition network failed.",
    aborted: "Speech capture stopped.",
  };
  return messages[code] || `Voice error: ${code}`;
}

export function draftValue(input) {
  const parts = [input.finalTranscript, input.interimTranscript].filter(Boolean);
  return parts.join(" ");
}

export function submittedTranscript(input) {
  return `${input.finalTranscript}${input.interimTranscript}`.trim();
}

export function classifyMicrophoneError(error) {
  const name = error?.name || "UnknownError";
  const messages = {
    NotAllowedError: "Microphone permission is blocked. Allow microphone for this site, then retry.",
    PermissionDeniedError: "Microphone permission is blocked. Allow microphone for this site, then retry.",
    NotFoundError: "No microphone is exposed to this browser.",
    DevicesNotFoundError: "No microphone is exposed to this browser.",
    NotReadableError: "Another application is using the microphone.",
    TrackStartError: "Another application is using the microphone.",
    InvalidStateError: "Microphone access requires HTTPS or localhost.",
    UnsupportedBrowserError: "This browser does not expose getUserMedia.",
  };
  return messages[name] || `Microphone access failed: ${name}`;
}

export async function requestMicrophoneAccess(environment = globalThis) {
  if (!environment.isSecureContext) {
    const error = new Error("Microphone access requires HTTPS or localhost.");
    error.name = "InvalidStateError";
    throw error;
  }
  if (!environment.navigator?.mediaDevices?.getUserMedia) {
    const error = new Error("This browser does not expose getUserMedia.");
    error.name = "UnsupportedBrowserError";
    throw error;
  }
  return environment.navigator.mediaDevices.getUserMedia({
    audio: {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  });
}

export function stopMicrophoneStream(stream) {
  for (const track of stream?.getAudioTracks?.() || []) track.stop();
}

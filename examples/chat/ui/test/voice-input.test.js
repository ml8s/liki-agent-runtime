import assert from "node:assert/strict";
import test from "node:test";
import {
  applyRecognitionResults,
  beginHold,
  classifyMicrophoneError,
  createVoiceInput,
  draftValue,
  finishRelease,
  releaseHold,
  requestMicrophoneAccess,
  submittedTranscript,
} from "../src/voice-input.js";

function results(eventIndex, alternatives) {
  return {
    resultIndex: eventIndex,
    results: alternatives.map((alternative) => ({
      isFinal: alternative.final,
      0: { transcript: alternative.text },
    })),
  };
}

test("hold accumulates final and interim transcript", () => {
  let input = beginHold(createVoiceInput(), "");
  input = applyRecognitionResults(input, results(0, [{ final: false, text: "hello" }]));
  assert.equal(draftValue(input), "hello");

  input = applyRecognitionResults(input, results(0, [
    { final: true, text: "hello world" },
    { final: false, text: "today" },
  ]));
  assert.equal(input.finalTranscript, "hello world");
  assert.equal(input.interimTranscript, "today");
  assert.equal(draftValue(input), "hello world today");
});

test("release for send accepts late final result and finishes with transcript", () => {
  let input = beginHold(createVoiceInput(), "");
  input = applyRecognitionResults(input, results(0, [{ final: false, text: "hello" }]));
  input = releaseHold(input, { submit: true });
  input = applyRecognitionResults(input, results(0, [{ final: true, text: " hello world" }]));
  input = finishRelease(input);

  assert.equal(input.state, "idle");
  assert.equal(input.releasedTranscript, "hello world");
});

test("cancel release ignores late recognition result", () => {
  let input = beginHold(createVoiceInput(), "");
  input = releaseHold(input, { submit: false });
  input = applyRecognitionResults(input, results(0, [{ final: true, text: "should disappear" }]));
  input = finishRelease(input);

  assert.equal(input.releasedTranscript, "");
});

test("submittedTranscript still works for immediate browser finalization", () => {
  let input = beginHold(createVoiceInput(), "");
  input = applyRecognitionResults(input, results(0, [{ final: true, text: "ready" }]));
  assert.equal(submittedTranscript(input), "ready");
});

test("maps permission and microphone errors", () => {
  assert.match(classifyMicrophoneError({ name: "NotAllowedError" }), /blocked/i);
  assert.match(classifyMicrophoneError({ name: "NotFoundError" }), /No microphone/i);
});

test("requests audio processing constraints only", async () => {
  let actual;
  const environment = {
    isSecureContext: true,
    navigator: {
      mediaDevices: {
        getUserMedia: async (constraint) => {
          actual = constraint;
          return { getAudioTracks: () => [] };
        },
      },
    },
  };
  await requestMicrophoneAccess(environment);
  assert.deepEqual(actual.audio, {
    echoCancellation: true,
    noiseSuppression: true,
    autoGainControl: true,
  });
});

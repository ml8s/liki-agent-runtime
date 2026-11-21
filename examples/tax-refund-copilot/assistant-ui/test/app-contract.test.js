import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const app = await readFile(new URL("../app.js", import.meta.url), "utf8");

test("initial and resume clients use the local AG-UI endpoint", () => {
  const matches = [...app.matchAll(/new HttpAgent\(\{[\s\S]*?url:\s*"([^"]+)"/g)].map((match) => match[1]);
  assert.equal(matches.length, 2);
  assert.deepEqual(matches, ["/ag-ui", "/ag-ui"]);
});

test("voice capture is no longer coupled to the tax reducer example", () => {
  assert.doesNotMatch(app, /SpeechRecognition|voice-button|voice-apply|voice-status/);
});

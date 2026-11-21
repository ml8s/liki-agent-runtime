import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const app = await readFile(new URL("../app.js", import.meta.url), "utf8");

test("message box is the single text and hold-to-talk surface", () => {
  for (const id of [
    "conversation",
    "conversation-empty",
    "composer",
    "input-shell",
    "draft-input",
    "send-button",
    "input-help",
    "cancel-help",
    "cancel-zone",
    "agent-run-phase",
    "agent-run-phase-label",
    "agent-run-detail",
  ]) {
    assert.match(html, new RegExp(`id="${id}"`), `missing #${id}`);
    if (!["input-help"].includes(id)) {
      assert.match(app, new RegExp(`#${id}`), `app.js does not use #${id}`);
    }
  }

  // The mic/keyboard mode toggle is intentionally absent: the same message box
  // is quick-tapped for text and long-pressed for speech.
  assert.doesNotMatch(html, /id="mode-button"/);
  assert.doesNotMatch(html, /id="hold-button"/);
  assert.doesNotMatch(app, /#mode-button|#hold-button/);
});

test("useful run and playback controls remain available", () => {
  for (const id of ["stop-button", "speak-replies"]) {
    assert.match(html, new RegExp(`id="${id}"`), `missing #${id}`);
    if (!["input-help"].includes(id)) {
      assert.match(app, new RegExp(`#${id}`), `app.js does not use #${id}`);
    }
  }
});

test("chat app posts through local AG-UI endpoint", () => {
  assert.match(app, /url:\s*"\/ag-ui"/);
  assert.doesNotMatch(app, /localhost:\d+\/ag-ui/);
});

test("message role CSS does not collide with page shell styles", () => {
  assert.match(html, /\.conversation-message\.agent\s*\{/);
  assert.doesNotMatch(html, /\.conversation-message\.assistant\s*\{/);
});

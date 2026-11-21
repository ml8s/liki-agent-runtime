import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const app = await readFile(new URL("../app.js", import.meta.url), "utf8");

test("assistant UI exposes the contract expected by app.js", () => {
  for (const id of [
    "assistant-launcher",
    "assistant-dialog",
    "conversation",
    "conversation-input",
    "conversation-send",
    "conversation-stop",
    "response-language",
    "agent-run-phase",
    "agent-run-phase-label",
    "agent-run-detail",
  ]) {
    assert.match(html, new RegExp(`id="${id}"`), `missing #${id}`);
    assert.match(app, new RegExp(`#${id}`), `app.js does not use #${id}`);
  }
});

test("assistant app posts through the local AG-UI endpoint", () => {
  assert.match(app, /url:\s*"\/ag-ui"/);
  assert.doesNotMatch(app, /localhost:\d+\/ag-ui/);
});

test("assistant sends an explicit response language field", () => {
  assert.match(app, /responseLanguage/);
});

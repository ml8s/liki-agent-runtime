import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { SCALAR_FIELDS, TAX_REFUND_PATCH_TOOL } from "../src/contract.js";

test("HTML scalar inputs exactly match the registered scalar contract", async () => {
  const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
  const htmlPaths = [...html.matchAll(/data-path="([^"]+)"/g)]
    .map((match) => match[1])
    .filter((path) => !path.startsWith("/items/"))
    .sort();
  const contractPaths = SCALAR_FIELDS.map((field) => field.path).sort();
  assert.deepEqual(htmlPaths, contractPaths);
});

test("tool schema does not expose user-only declaration paths", () => {
  const paths = TAX_REFUND_PATCH_TOOL.parameters.properties.patch.items.properties.path.enum;
  assert.equal(paths.includes("/declarations/certified_accurate"), false);
  assert.equal(paths.includes("/declarations/consent_to_processing"), false);
});

import assert from "node:assert/strict";
import test from "node:test";
import { CONTRACT, createTaxRefundState, taxRefundPatchTool } from "../src/contract.js";
import { applyTaxRefundPatch, markItemEdited, markScalarEdited } from "../src/reducer.js";

function patch(operations) {
  return { contractVersion: CONTRACT.contractVersion, target: CONTRACT.target, patch: operations };
}

test("tool contract has a stable snake_case client tool name", () => {
  const tool = taxRefundPatchTool();
  assert.equal(tool.name, "tax_refund_form_patch");
  assert.match(tool.name, /^[a-z][a-z0-9_]*$/);
});

test("rejects contract target and version mismatches without mutating draft", () => {
  const state = createTaxRefundState();
  for (const key of ["contractVersion", "target"]) {
    const input = patch([{ op: "set", path: "/claim/claimant_name", value: "Dana" }]);
    input[key] = "wrong";
    const applied = applyTaxRefundPatch(state, input);
    assert.equal(applied.result.status, "rejected");
    assert.equal(applied.state.draft.claim.claimant_name, "");
  }
});

test("rejects malformed arguments instead of treating them as an empty success", () => {
  const state = createTaxRefundState();
  const applied = applyTaxRefundPatch(state, { ...patch([]), patch: "not-an-array" });
  assert.equal(applied.result.status, "rejected");
  assert.deepEqual(applied.result.rejectedPatches, [{ path: "/patch", reason: "invalid_patch" }]);
});

test("applies scalar and item patches with normalized line ids", () => {
  let state = createTaxRefundState();
  const applied = applyTaxRefundPatch(state, patch([
    { op: "set", path: "/claim/claimant_name", value: "Dana Ellison" },
    { op: "set", path: "/claim/requested_amount", value: 1315 },
    { op: "upsert_items", path: "/items", key: "description", items: [{
      description: "Overpayment", category: "overpayment", period: "2025",
      original_amount: 780, refund_amount: 780, evidence_reference: "",
    }] },
  ]));
  state = applied.state;

  assert.equal(applied.result.status, "applied");
  assert.equal(state.draft.claim.claimant_name, "Dana Ellison");
  assert.equal(state.draft.claim.requested_amount, 1315);
  assert.equal(state.draft.items.length, 1);
  assert.match(state.draft.items[0].id, /^[0-9a-f-]{36}$/);
});

test("rejects enum, money, operation/path, and unknown field mistakes", () => {
  const state = createTaxRefundState();
  const applied = applyTaxRefundPatch(state, patch([
    { op: "set", path: "/claim/currency", value: "ZZZ" },
    { op: "set", path: "/claim/requested_amount", value: "1,315" },
    { op: "set", path: "/items", value: "oops" },
    { op: "upsert_items", path: "/items", items: [{ description: "x", unknown: true }] },
  ]));

  assert.equal(applied.result.status, "rejected");
  const reasons = applied.result.rejectedPatches.map((error) => error.reason);
  assert.deepEqual(reasons, ["invalid_enum_value", "invalid_money_value", "operation_path_mismatch", "unknown_item_field"]);
});

test("manual scalar edits win over later assistant patches", () => {
  const edited = markScalarEdited(createTaxRefundState(), "/claim/claimant_name", "Dana Manual");
  const applied = applyTaxRefundPatch(edited, patch([
    { op: "set", path: "/claim/claimant_name", value: "Assistant Value" },
    { op: "set", path: "/contact/email", value: "dana@example.com" },
  ]));

  assert.equal(applied.result.status, "partial");
  assert.equal(applied.state.draft.claim.claimant_name, "Dana Manual");
  assert.equal(applied.state.draft.contact.email, "dana@example.com");
});

test("assistant cannot set user-only declarations", () => {
  const state = createTaxRefundState();
  const applied = applyTaxRefundPatch(state, patch([
    { op: "set", path: "/declarations/certified_accurate", value: true },
  ]));

  assert.equal(applied.result.status, "rejected");
  assert.equal(applied.state.draft.declarations.certified_accurate, false);
});

test("manual item edits are protected across upsert", () => {
  const initial = applyTaxRefundPatch(createTaxRefundState(), patch([
    { op: "upsert_items", path: "/items", items: [{
      description: "Overpayment", category: "overpayment", period: "2025",
      original_amount: 780, refund_amount: 780, evidence_reference: "A-001",
    }] },
  ]));
  const itemId = initial.state.draft.items[0].id;
  const edited = markItemEdited(initial.state, itemId, "evidence_reference", "USER-001");
  const applied = applyTaxRefundPatch(edited, patch([
    { op: "upsert_items", items: [{
      description: "Overpayment", category: "overpayment", period: "2025",
      original_amount: 900, refund_amount: 900, evidence_reference: "MODEL-001",
    }] },
  ]));

  assert.equal(applied.result.status, "rejected");
  assert.equal(applied.state.draft.items[0].id, itemId);
  assert.equal(applied.state.draft.items[0].evidence_reference, "USER-001");
  assert.equal(applied.state.draft.items[0].original_amount, 780);
});

test("partial item update preserves unspecified fields", () => {
  const initial = applyTaxRefundPatch(createTaxRefundState(), patch([
    { op: "add_item", path: "/items", value: {
      description: "Overpayment", category: "overpayment", period: "2025",
      original_amount: 780, refund_amount: 780, evidence_reference: "A-001",
    } },
  ]));
  const itemId = initial.state.draft.items[0].id;
  const applied = applyTaxRefundPatch(initial.state, patch([
    { op: "update_item", path: "/items", id: itemId, value: { refund_amount: 800 } },
  ]));

  assert.equal(applied.result.status, "applied");
  assert.equal(applied.state.draft.items[0].description, "Overpayment");
  assert.equal(applied.state.draft.items[0].original_amount, 780);
  assert.equal(applied.state.draft.items[0].refund_amount, 800);
});

test("patch application never mutates the caller-owned state", () => {
  const before = createTaxRefundState();
  const snapshot = structuredClone(before.draft);
  const applied = applyTaxRefundPatch(before, patch([
    { op: "set", path: "/claim/claimant_name", value: "Changed" },
    { op: "upsert_items", path: "/items", key: "description", items: [{
      description: "Line", category: "other", period: "2025",
      original_amount: 1, refund_amount: 1, evidence_reference: "E-1",
    }] },
  ]));

  assert.notEqual(applied.state, before);
  assert.deepEqual(before.draft, snapshot);
  assert.notEqual(applied.state.draft, before.draft);
  assert.equal(applied.state.draft.claim.claimant_name, "Changed");
});

test("clear and indexed item operations behave predictably", () => {
  const initial = applyTaxRefundPatch(createTaxRefundState(), patch([
    { op: "set", path: "/contact/phone", value: "+44 20 7000 0000" },
    { op: "upsert_items", path: "/items", key: "description", items: [{
      description: "Line one", category: "other", period: "2025",
      original_amount: 10, refund_amount: 10, evidence_reference: "E-1",
    }] },
  ]));
  const itemId = initial.state.draft.items[0].id;
  const applied = applyTaxRefundPatch(initial.state, patch([
    { op: "clear", path: "/contact/phone" },
    { op: "update_item", path: "/items", index: 0, value: { refund_amount: 20 } },
    { op: "remove_item", path: "/items", id: itemId },
  ]));

  assert.equal(applied.result.status, "applied");
  assert.equal(applied.state.draft.contact.phone, "");
  assert.equal(applied.state.draft.items.length, 0);
});

test("invalid confidence or review metadata rejects the operation", () => {
  const state = createTaxRefundState();
  const applied = applyTaxRefundPatch(state, patch([
    { op: "set", path: "/claim/claimant_name", value: "Dana", confidence: 2 },
    { op: "set", path: "/contact/email", value: "dana@example.com", requiresReview: "yes" },
  ]));

  assert.equal(applied.result.status, "rejected");
  assert.equal(applied.state.draft.claim.claimant_name, "");
  assert.equal(applied.state.draft.contact.email, "");
  assert.deepEqual(applied.result.rejectedPatches.map((error) => error.reason), [
    "invalid_confidence",
    "invalid_requires_review",
  ]);
});

test("invalid manual item edits do not acquire protection", () => {
  const initial = applyTaxRefundPatch(createTaxRefundState(), patch([
    { op: "upsert_items", path: "/items", key: "description", items: [{
      description: "Line", category: "other", period: "2025",
      original_amount: 10, refund_amount: 10, evidence_reference: "E-1",
    }] },
  ]));
  const itemId = initial.state.draft.items[0].id;
  const edited = markItemEdited(initial.state, itemId, "refund_amount", "not-a-number");
  const applied = applyTaxRefundPatch(edited, patch([
    { op: "update_item", path: "/items", id: itemId, value: { refund_amount: 20 } },
  ]));

  assert.equal(edited, initial.state);
  assert.equal(applied.result.status, "applied");
  assert.equal(applied.state.draft.items[0].refund_amount, 20);
});

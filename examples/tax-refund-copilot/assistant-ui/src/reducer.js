import {
  ARRAY_PATHS,
  CONTRACT,
  ITEM_FIELDS,
  ITEM_KEYS,
  SCALAR_FIELDS,
  UPSERT_KEYS,
  scalarField,
} from "./contract.js";

const OPERATIONS = new Set(["set", "clear", "add_item", "update_item", "remove_item", "upsert_items"]);

export function applyTaxRefundPatch(state, args) {
  const envelopeError = validateEnvelope(args);
  if (envelopeError) {
    return {
      state,
      result: {
        status: "rejected",
        appliedPaths: [],
        rejectedPatches: [envelopeError],
      },
    };
  }

  if (!Array.isArray(args.patch)) {
    return {
      state,
      result: {
        status: "rejected",
        appliedPaths: [],
        rejectedPatches: [{ path: "/patch", reason: "invalid_patch" }],
      },
    };
  }

  const nextState = {
    draft: structuredClone(state.draft),
    metadata: new Map(state.metadata),
    lockedScalars: new Set(state.lockedScalars),
    lockedItemFields: new Set(state.lockedItemFields),
  };
  const result = { status: "applied", appliedPaths: [], rejectedPatches: [] };

  for (const operation of args.patch) {
    const validation = validateOperation(operation, nextState.draft, locksFor(state));
    if (validation.errors.length) {
      result.status = result.appliedPaths.length ? "partial" : "rejected";
      result.rejectedPatches.push(...validation.errors);
      continue;
    }

    applyOperation(nextState, operation, validation);
    result.appliedPaths.push(validation.appliedPath);
    applyOperationMetadata(operation, operation.path, nextState.metadata);
  }

  result.status = result.rejectedPatches.length
    ? (result.appliedPaths.length ? "partial" : "rejected")
    : "applied";

  return { state: nextState, result };
}

export function markScalarEdited(state, path, value) {
  const field = scalarField(path);
  if (!field) return state;

  const validated = validateValue(field, value);
  if (!validated.ok) return state;

  const nextState = cloneState(state);
  setScalar(nextState.draft, path, validated.value);
  nextState.lockedScalars.add(path);
  nextState.metadata.set(path, {
    source: "user",
    confidence: 1,
    status: "user_edited",
  });
  return nextState;
}

export function markItemEdited(state, itemId, key, value) {
  const field = ITEM_FIELDS.find((candidate) => candidate.key === key);
  const index = state.draft.items.findIndex((item) => item.id === itemId);
  if (!field || index < 0) return state;

  const validated = validateValue(field, value);
  if (!validated.ok) return state;

  const nextState = cloneState(state);
  nextState.draft.items[index][key] = validated.value;
  nextState.lockedItemFields.add(`${itemId}:${key}`);
  nextState.metadata.set(`/items/${itemId}/${key}`, {
    source: "user",
    confidence: 1,
    status: "user_edited",
  });
  return nextState;
}

function validateEnvelope(args) {
  if (!args || typeof args !== "object" || Array.isArray(args)) {
    return { path: "/", reason: "invalid_arguments" };
  }
  if (args.contractVersion !== CONTRACT.contractVersion) {
    return { path: "/contractVersion", reason: "invalid_contract_version", value: args.contractVersion };
  }
  if (args.target !== CONTRACT.target) {
    return { path: "/target", reason: "invalid_target", value: args.target };
  }
  return null;
}

function locksFor(state) {
  return {
    scalars: state.lockedScalars,
    itemFields: state.lockedItemFields,
  };
}

function validateOperation(operation, draft, locks) {
  const invalid = {
    errors: [{ path: "/patch", reason: "invalid_operation" }],
    appliedPath: "/patch",
  };
  if (!operation || typeof operation !== "object" || Array.isArray(operation)) return invalid;

  const path = typeof operation.path === "string" ? operation.path : "";
  const validation = { errors: [], appliedPath: path };
  if (!OPERATIONS.has(operation.op)) {
    validation.errors.push({ path, reason: "unsupported_operation", operation: operation.op });
  }
  if (!isValidPath(path)) {
    validation.errors.push({ path, reason: "unknown_path", operation: operation.op });
    return validation;
  }

  validatePatchMetadata(operation, validation.errors);

  if (operation.op === "set" || operation.op === "clear") {
    if (ARRAY_PATHS.includes(path)) {
      validation.errors.push({ path, reason: "operation_path_mismatch", operation: operation.op });
    } else {
      validateScalar(operation, locks, validation.errors);
    }
    return validation;
  }

  if (path !== "/items") {
    validation.errors.push({ path, reason: "operation_path_mismatch", operation: operation.op });
    return validation;
  }

  validateItemOperation(operation, draft, locks, validation.errors);
  return validation;
}

function isValidPath(path) {
  return SCALAR_FIELDS.some((field) => field.path === path) || ARRAY_PATHS.includes(path);
}

function validateScalar(operation, locks, errors) {
  const field = scalarField(operation.path);
  if (field.userOnly) {
    errors.push({
      path: field.path,
      reason: "user_only_field",
      message: "Declarations must be entered by the user.",
    });
    return;
  }
  if (locks.scalars.has(field.path)) {
    errors.push({
      path: field.path,
      reason: "user_edited",
      message: "A manual user edit owns this field.",
    });
    return;
  }
  if (operation.op === "clear") return;

  const validated = validateValue(field, operation.value);
  if (!validated.ok) {
    errors.push({
      path: field.path,
      reason: validated.reason,
      value: operation.value,
      ...(validated.message ? { message: validated.message } : {}),
      ...(validated.allowedValues ? { allowedValues: validated.allowedValues } : {}),
    });
    return;
  }
  if (field.required && (validated.value === "" || validated.value == null)) {
    errors.push({ path: field.path, reason: "required_value_missing" });
  }
}

function validateItemOperation(operation, draft, locks, errors) {
  if (operation.op === "upsert_items") {
    validateUpsert(operation, draft, locks, errors);
    return;
  }

  if (operation.op === "add_item") {
    validateItemRow(operation.value, "/items", errors);
    return;
  }

  const target = locateItem(draft.items, operation);
  if (!target) {
    errors.push({
      path: "/items",
      reason: "item_not_found",
      id: operation.id,
      index: operation.index,
    });
    return;
  }

  if (operation.op === "remove_item") {
    if (ITEM_KEYS.some((key) => locks.itemFields.has(`${target.item.id}:${key}`))) {
      errors.push({
        path: `/items/${target.item.id}`,
        reason: "user_edited",
        message: "A manual user edit owns this line.",
      });
    }
    return;
  }

  validateItemRow(operation.value, `/items/${target.item.id}`, errors);
  if (ITEM_KEYS.some((key) => key in (operation.value || {}) && locks.itemFields.has(`${target.item.id}:${key}`))) {
    errors.push({
      path: `/items/${target.item.id}`,
      reason: "user_edited",
      message: "A manual user edit owns one of the target line fields.",
    });
  }
}

function validateUpsert(operation, draft, locks, errors) {
  const key = operation.key || "description";
  if (!UPSERT_KEYS.includes(key)) {
    errors.push({
      path: "/items",
      reason: "invalid_upsert_key",
      value: operation.key,
      allowedValues: UPSERT_KEYS,
    });
    return;
  }

  const rows = Array.isArray(operation.items) ? operation.items : Array.isArray(operation.value) ? operation.value : null;
  if (!rows) {
    errors.push({ path: "/items", reason: "missing_item_rows" });
    return;
  }

  rows.forEach((row, rowIndex) => {
    const rowPath = `/items/${rowIndex}`;
    validateItemRow(row, rowPath, errors);
    if (!row || typeof row !== "object" || row[key] == null || row[key] === "") {
      errors.push({ path: `${rowPath}/${key}`, reason: "missing_upsert_key" });
      return;
    }
    const match = draft.items.find((item) => item[key] === row[key]);
    if (match && ITEM_KEYS.some((itemKey) => itemKey in row && locks.itemFields.has(`${match.id}:${itemKey}`))) {
      errors.push({
        path: `/items/${match.id}`,
        reason: "user_edited",
        message: "A manual user edit owns one of the target line fields.",
      });
    }
  });
}

function locateItem(items, operation) {
  const index = Number(operation.index);
  if (Number.isInteger(index) && index >= 0 && index < items.length) {
    return { index, item: items[index] };
  }
  if (typeof operation.id === "string" && operation.id) {
    const foundIndex = items.findIndex((item) => item.id === operation.id);
    if (foundIndex >= 0) return { index: foundIndex, item: items[foundIndex] };
  }
  return null;
}

function validateItemRow(row, path, errors) {
  if (!row || typeof row !== "object" || Array.isArray(row)) {
    errors.push({ path, reason: "invalid_item_row" });
    return;
  }

  const unknownKeys = Object.keys(row).filter((key) => key !== "id" && !ITEM_KEYS.includes(key));
  if (unknownKeys.length) {
    errors.push({ path, reason: "unknown_item_field", fields: unknownKeys });
  }

  for (const field of ITEM_FIELDS) {
    if (!(field.key in row) || row[field.key] == null) continue;
    const validated = validateValue(field, row[field.key]);
    if (!validated.ok) {
      errors.push({
        path: `${path}/${field.key}`,
        reason: validated.reason,
        value: row[field.key],
        ...(validated.message ? { message: validated.message } : {}),
        ...(validated.allowedValues ? { allowedValues: validated.allowedValues } : {}),
      });
    }
  }
}

function validateValue(field, value) {
  if (value === undefined || value === null || value === "") {
    return {
      ok: true,
      value: field.type === "boolean" ? false : field.type === "money" ? null : "",
    };
  }
  if (field.type === "money") {
    if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
      return { ok: false, reason: "invalid_money_value" };
    }
    return { ok: true, value };
  }
  if (field.type === "boolean") {
    return typeof value === "boolean"
      ? { ok: true, value }
      : { ok: false, reason: "invalid_boolean_value" };
  }
  if (typeof value !== "string") return { ok: false, reason: "invalid_string_value" };
  if (field.type === "enum" && !field.enum.includes(value)) {
    return { ok: false, reason: "invalid_enum_value", allowedValues: field.enum };
  }
  if (field.pattern && !field.pattern.test(value)) {
    return { ok: false, reason: "invalid_format", message: `Expected format ${field.pattern}` };
  }
  return { ok: true, value };
}

function validatePatchMetadata(operation, errors) {
  if (operation.confidence != null) {
    if (typeof operation.confidence !== "number" || !Number.isFinite(operation.confidence) || operation.confidence < 0 || operation.confidence > 1) {
      errors.push({ path: operation.path || "/", reason: "invalid_confidence" });
    }
  }
  if (operation.requiresReview != null && typeof operation.requiresReview !== "boolean") {
    errors.push({ path: operation.path || "/", reason: "invalid_requires_review" });
  }
  for (const key of ["source", "evidence", "warning"]) {
    if (operation[key] != null && typeof operation[key] !== "string") {
      errors.push({ path: operation.path || "/", reason: `invalid_${key}` });
    }
  }
}

function applyOperation(nextState, operation, validation) {
  if (operation.op === "set") {
    const field = scalarField(operation.path);
    setScalar(nextState.draft, operation.path, validateValue(field, operation.value).value);
    return;
  }

  if (operation.op === "clear") {
    const field = scalarField(operation.path);
    setScalar(nextState.draft, operation.path, field.type === "boolean" ? false : field.type === "money" ? null : "");
    return;
  }

  const items = nextState.draft.items;
  if (operation.op === "add_item") {
    const row = normalizeItem(operation.value);
    items.push(row);
    validation.appliedPath = `/items/${row.id}`;
    return;
  }

  const target = locateItem(items, operation);
  if (operation.op === "remove_item") {
    items.splice(target.index, 1);
    validation.appliedPath = `/items/${target.item.id}`;
    return;
  }

  if (operation.op === "update_item") {
    items[target.index] = normalizeItem(operation.value, target.item);
    validation.appliedPath = `/items/${target.item.id}`;
    return;
  }

  const key = operation.key || "description";
  const rows = Array.isArray(operation.items) ? operation.items : operation.value;
  const appliedIds = [];
  for (const inputRow of rows) {
    const matchIndex = items.findIndex((item) => item[key] === inputRow[key]);
    if (matchIndex >= 0) {
      items[matchIndex] = normalizeItem(inputRow, items[matchIndex]);
      appliedIds.push(items[matchIndex].id);
    } else {
      const row = normalizeItem(inputRow);
      items.push(row);
      appliedIds.push(row.id);
    }
  }
  validation.appliedPath = `/items/${appliedIds.join(",")}`;
}

function normalizeItem(row, existingItem) {
  const normalized = {
    id: existingItem?.id || row?.id || crypto.randomUUID(),
  };
  for (const field of ITEM_FIELDS) {
    const value = row?.[field.key];
    normalized[field.key] = value === undefined
      ? (existingItem?.[field.key] ?? null)
      : validateValue(field, value).value;
  }
  return normalized;
}

function applyOperationMetadata(operation, path, metadata) {
  const next = { ...(metadata.get(path) || {}) };
  if (operation.confidence != null) next.confidence = operation.confidence;
  if (operation.source) next.source = operation.source;
  if (operation.evidence) next.evidence = operation.evidence;
  if (operation.warning) next.warning = operation.warning;
  next.requiresReview = Boolean(operation.requiresReview);
  metadata.set(path, next);
}

function setScalar(draft, path, value) {
  const parts = path.split("/").filter(Boolean);
  let cursor = draft;
  while (parts.length > 1) cursor = cursor[parts.shift()];
  cursor[parts[0]] = value;
}

function cloneState(state) {
  return {
    draft: structuredClone(state.draft),
    metadata: new Map(state.metadata),
    lockedScalars: new Set(state.lockedScalars),
    lockedItemFields: new Set(state.lockedItemFields),
  };
}

export function addEmptyItem(state) {
  const nextState = cloneState(state);
  nextState.draft.items.push({
    id: crypto.randomUUID(),
    description: "",
    category: "",
    period: "",
    original_amount: null,
    tax_withheld: null,
    refund_amount: null,
    evidence_reference: "",
  });
  return nextState;
}

export function removeItem(state, itemId) {
  const nextState = cloneState(state);
  nextState.draft.items = nextState.draft.items.filter((item) => item.id !== itemId);
  return nextState;
}

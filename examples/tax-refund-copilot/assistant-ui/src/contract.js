export const CONTRACT = Object.freeze({
  contractVersion: "1.0",
  target: "tax_refund_claim_v1",
});

export const AG_UI_EVENT = Object.freeze({
  toolCallStart: "TOOL_CALL_START",
  toolCallArgs: "TOOL_CALL_ARGS",
  toolCallEnd: "TOOL_CALL_END",
});

export const SCALAR_FIELDS = Object.freeze([
  Object.freeze({ path: "/claim/claimant_name", type: "string", required: true }),
  Object.freeze({ path: "/claim/taxpayer_reference_last4", type: "string", required: true, pattern: /^[0-9]{4}$/ }),
  Object.freeze({ path: "/claim/tax_period", type: "string", required: true, pattern: /^[0-9]{4}(?:-H[12])?$/ }),
  Object.freeze({ path: "/claim/refund_reason", type: "enum", required: true, enum: Object.freeze(["overpayment", "withholding_correction", "duplicate_payment", "amended_return", "eligible_relief", "other"]) }),
  Object.freeze({ path: "/claim/currency", type: "enum", required: true, enum: Object.freeze(["EUR", "GBP", "JPY", "USD", "CAD", "AUD", "CHF", "other"]) }),
  Object.freeze({ path: "/claim/requested_amount", type: "money", required: true }),
  Object.freeze({ path: "/claim/explanation", type: "string" }),
  Object.freeze({ path: "/contact/email", type: "string" }),
  Object.freeze({ path: "/contact/phone", type: "string" }),
  Object.freeze({ path: "/contact/mailing_address", type: "string" }),
  Object.freeze({ path: "/payment/method", type: "enum", required: true, enum: Object.freeze(["bank_transfer", "original_payment_method", "cheque", "account_credit"]) }),
  Object.freeze({ path: "/payment/bank_name", type: "string" }),
  Object.freeze({ path: "/payment/account_holder", type: "string" }),
  Object.freeze({ path: "/payment/account_number_last4", type: "string", pattern: /^[0-9]{4}$/ }),
  Object.freeze({ path: "/payment/bank_reference", type: "string" }),
  Object.freeze({ path: "/declarations/certified_accurate", type: "boolean", required: true, userOnly: true }),
  Object.freeze({ path: "/declarations/consent_to_processing", type: "boolean", required: true, userOnly: true }),
  Object.freeze({ path: "/review/notes", type: "string" }),
]);

export const SCALAR_PATHS = Object.freeze(SCALAR_FIELDS.map((field) => field.path));
export const ARRAY_PATHS = Object.freeze(["/items"]);
export const MODEL_SCALAR_PATHS = Object.freeze(SCALAR_FIELDS.filter((field) => !field.userOnly).map((field) => field.path));
export const USER_ONLY_PATHS = Object.freeze(SCALAR_FIELDS.filter((field) => field.userOnly).map((field) => field.path));
export const REQUIRED_CORE_PATHS = Object.freeze([
  "/claim/claimant_name",
  "/claim/taxpayer_reference_last4",
  "/claim/tax_period",
  "/claim/refund_reason",
  "/claim/currency",
  "/claim/requested_amount",
  "/payment/method",
]);

export const ITEM_FIELDS = Object.freeze([
  Object.freeze({ key: "description", type: "string", required: true }),
  Object.freeze({ key: "category", type: "enum", required: true, enum: Object.freeze(["overpayment", "withholding_correction", "duplicate_payment", "amended_return_adjustment", "eligible_relief", "penalty_reversal", "other"]) }),
  Object.freeze({ key: "period", type: "string", required: true }),
  Object.freeze({ key: "original_amount", type: "money", required: true }),
  Object.freeze({ key: "tax_withheld", type: "money" }),
  Object.freeze({ key: "refund_amount", type: "money", required: true }),
  Object.freeze({ key: "evidence_reference", type: "string", required: true }),
]);

export const ITEM_KEYS = Object.freeze(ITEM_FIELDS.map((field) => field.key));
export const REQUIRED_ITEM_KEYS = Object.freeze(ITEM_FIELDS.filter((field) => field.required).map((field) => field.key));
export const UPSERT_KEYS = Object.freeze(["description", "id"]);
export const FIELD_ENUMS = Object.freeze(Object.fromEntries([
  ...SCALAR_FIELDS.filter((field) => field.type === "enum").map((field) => [field.path, field.enum]),
  ["/items/category", ITEM_FIELDS.find((field) => field.key === "category").enum],
]));

export const TOOL_NAME = "tax_refund_form_patch";

export const TAX_REFUND_PATCH_TOOL = Object.freeze({
  name: TOOL_NAME,
  description: "Apply an incremental patch to the draft tax refund claim form. Use this whenever the user provides, confirms, or corrects refund facts. Do not set declaration checkboxes.",
  parameters: Object.freeze({
    type: "object",
    additionalProperties: false,
    required: ["contractVersion", "target", "patch"],
    properties: Object.freeze({
      contractVersion: { type: "string", const: CONTRACT.contractVersion },
      target: { type: "string", const: CONTRACT.target },
      summary: { type: "string" },
      patch: {
        type: "array",
        items: {
          type: "object",
          additionalProperties: false,
          required: ["op", "path"],
          properties: {
            op: { type: "string", enum: ["set", "clear", "add_item", "update_item", "remove_item", "upsert_items"] },
            path: { type: "string", enum: [...MODEL_SCALAR_PATHS, ...ARRAY_PATHS], description: "Registered claim path. set/clear target scalar fields; item operations target /items." },
            value: { description: "Scalar value for set; row object for add_item/update_item; row array for upsert_items when items is unused." },
            id: { type: "string", description: "Stable line id for update_item/remove_item." },
            index: { type: "integer", minimum: 0, description: "Zero-based line index for update_item/remove_item." },
            key: { type: "string", enum: [...UPSERT_KEYS], description: "Line field used to match rows in upsert_items." },
            items: {
              type: "array",
              description: "Rows for upsert_items.",
              items: {
                type: "object",
                additionalProperties: false,
                properties: {
                  id: { type: "string" },
                  ...Object.fromEntries(ITEM_FIELDS.map((field) => [field.key, valueSchema(field)])),
                },
              },
            },
            confidence: { type: "number", minimum: 0, maximum: 1 },
            source: { type: "string" },
            evidence: { type: "string" },
            requiresReview: { type: "boolean" },
            warning: { type: "string" },
          },
        },
      },
      missingFields: { type: "array", items: { type: "string" } },
      warnings: {
        type: "array",
        items: {
          type: "object",
          additionalProperties: false,
          required: ["path", "message"],
          properties: { path: { type: "string" }, message: { type: "string" } },
        },
      },
    }),
  }),
});

function valueSchema(field) {
  if (field.type === "money") return { type: "number", minimum: 0 };
  if (field.type === "boolean") return { type: "boolean" };
  if (field.type === "enum") return { type: "string", enum: [...field.enum] };
  return { type: "string" };
}

export function taxRefundPatchTool() {
  return structuredClone(TAX_REFUND_PATCH_TOOL);
}

export function scalarField(path) {
  return SCALAR_FIELDS.find((field) => field.path === path);
}

export function scalarValue(draft, path) {
  if (!path.startsWith("/")) return undefined;
  return path.split("/").filter(Boolean).reduce(
    (cursor, key) => (cursor == null ? undefined : cursor[key]),
    draft,
  );
}

export function createTaxRefundState() {
  return {
    draft: {
      claim: {
        claimant_name: "",
        taxpayer_reference_last4: "",
        tax_period: "",
        refund_reason: "",
        currency: "",
        requested_amount: null,
        explanation: "",
      },
      contact: {
        email: "",
        phone: "",
        mailing_address: "",
      },
      payment: {
        method: "",
        bank_name: "",
        account_holder: "",
        account_number_last4: "",
        bank_reference: "",
      },
      items: [],
      declarations: {
        certified_accurate: false,
        consent_to_processing: false,
      },
      review: {
        notes: "",
      },
    },
    metadata: new Map(),
    lockedScalars: new Set(),
    lockedItemFields: new Set(),
  };
}

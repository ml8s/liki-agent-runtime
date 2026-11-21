import { HttpAgent } from "./vendor/ag-ui-client.js";
import {
  AG_UI_EVENT,
  ARRAY_PATHS,
  CONTRACT,
  FIELD_ENUMS,
  ITEM_FIELDS,
  REQUIRED_CORE_PATHS,
  REQUIRED_ITEM_KEYS,
  SCALAR_FIELDS,
  SCALAR_PATHS,
  TOOL_NAME,
  createTaxRefundState,
  scalarField,
  scalarValue,
  taxRefundPatchTool,
} from "./src/contract.js";
import {
  addEmptyItem,
  applyTaxRefundPatch,
  markItemEdited,
  markScalarEdited,
  removeItem,
} from "./src/reducer.js";

const els = {
  conversation: document.querySelector("#conversation"),
  input: document.querySelector("#conversation-input"),
  send: document.querySelector("#conversation-send"),
  stop: document.querySelector("#conversation-stop"),
  phase: document.querySelector("#agent-run-phase"),
  detail: document.querySelector("#agent-run-detail"),
  sourceNotes: document.querySelector("#source-notes"),
  assistantForm: document.querySelector("#assistant-form"),
  completeness: document.querySelector("#completeness"),
  completenessBar: document.querySelector("#completeness-bar"),
  fieldCount: document.querySelector("#field-count"),
  validation: document.querySelector("#validation"),
  reviewWarnings: document.querySelector("#review-warnings"),
  totalsWarning: document.querySelector("#totals-warning"),
  itemsTotal: document.querySelector("#derived-items-total"),
  requested: document.querySelector("#derived-requested"),
  exportButton: document.querySelector("#export-json"),
  addItem: document.querySelector("#add-item"),
};

const SAMPLE_SOURCE_NOTES = `TAX ACCOUNT SUMMARY
Claimant: Dana Ellison
Reference ending: 4821
Period: 2025

Assessed overpayment: 780.00
Withholding correction: 415.00
Duplicate instalment: 120.00

Bank transfer preferred
Bank: Northbank
Account holder: Dana Ellison
Account number ending: 8890
Bank reference: NB-99231

Email: dana.ellison@example.com
Phone: +44 20 7946 0812
Mailing address: 18 Harbourview Road, Northport`;

let taxState = createTaxRefundState();
let conversationMessages = [];
let protocolMessages = [];
let activeAgentRun;
let currentTaxPatch;
let resumeDepth = 0;

const runtimeCorrelation = {
  threadId: crypto.randomUUID(),
  userId: `browser-${crypto.randomUUID()}`,
};

const runPhaseLabels = {
  idle: "Ready",
  connecting: "Connecting",
  running: "Agent running",
  streaming: "Streaming",
  complete: "Complete",
  resuming: "Applying claim patch",
  stopped: "Stopped",
  error: "Failed",
};

async function agentFetch(url, init) {
  const method = init?.method || "GET";
  const path = new URL(url, window.location.href).pathname;
  const response = await fetch(url, init);
  if (!response.ok) {
    let detail = "";
    try {
      detail = (await response.text()).slice(0, 500);
    } catch {
      detail = "";
    }
    throw new Error(`${method} ${path} failed: HTTP ${response.status} ${detail}`);
  }
  return response;
}

function setRunPhase(phase, label) {
  els.phase.dataset.phase = phase;
  els.phase.querySelector("#agent-run-phase-label").textContent =
    label || runPhaseLabels[phase] || phase;
}

function setRunDetail(message, isError = false) {
  els.detail.textContent = message || "";
  els.detail.classList.toggle("error", isError);
}

function setBusy(busy) {
  els.send.disabled = busy;
  els.stop.disabled = !busy;
}

function uniqueId(prefix) {
  return `${prefix}-${crypto.randomUUID()}`;
}

function appendChat(role, content) {
  const element = document.createElement("div");
  element.className = `message ${role}`;
  element.textContent = content || "";
  els.conversation.appendChild(element);
  els.conversation.scrollTop = els.conversation.scrollHeight;
  return element;
}

function appendSystem(content) {
  appendChat("assistant", content).classList.add("system");
}

function toVisibleMessages() {
  return conversationMessages.map(({ id, role, content }) => ({ id, role, content }));
}

function buildForwardedProps() {
  return {
    formTarget: CONTRACT.target,
    contractVersion: CONTRACT.contractVersion,
    allowedPaths: {
      scalars: SCALAR_PATHS,
      arrays: ARRAY_PATHS,
      enums: FIELD_ENUMS,
      userOnly: SCALAR_FIELDS.filter((field) => field.userOnly).map((field) => field.path),
    },
    currentClaim: taxState.draft,
    fieldProvenance: Object.fromEntries(taxState.metadata),
    sourceNotes: els.sourceNotes.value.trim(),
  };
}

function normalizeNumber(value) {
  if (value === "" || value == null) return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

function isNonEmpty(value) {
  return value !== undefined && value !== null && value !== "";
}

function requiredMissing() {
  const missing = [];
  for (const path of REQUIRED_CORE_PATHS) {
    if (!isNonEmpty(scalarValue(taxState.draft, path))) missing.push(path);
  }

  if (scalarValue(taxState.draft, "/payment/method") === "bank_transfer") {
    for (const path of [
      "/payment/bank_name",
      "/payment/account_holder",
      "/payment/account_number_last4",
    ]) {
      if (!isNonEmpty(scalarValue(taxState.draft, path))) missing.push(path);
    }
  }

  taxState.draft.items.forEach((item, index) => {
    for (const key of REQUIRED_ITEM_KEYS) {
      const value = item[key];
      if (value === undefined || value === null || value === "") {
        missing.push(`/items/${index}/${key}`);
      }
    }
  });

  for (const path of [
    "/declarations/certified_accurate",
    "/declarations/consent_to_processing",
  ]) {
    if (scalarValue(taxState.draft, path) !== true) missing.push(path);
  }

  return missing;
}

function itemWarnings() {
  const warnings = [];
  taxState.draft.items.forEach((item, index) => {
    const original = normalizeNumber(item.original_amount);
    const withheld = normalizeNumber(item.tax_withheld) || 0;
    const refund = normalizeNumber(item.refund_amount);
    if (original != null && refund != null && refund > original + withheld) {
      warnings.push(`/items/${index}/refund_amount — refund exceeds original amount plus withholding`);
    }
  });
  return warnings;
}

function metadataWarnings() {
  const warnings = [];
  for (const [path, metadata] of taxState.metadata.entries()) {
    if (metadata.requiresReview) warnings.push(`${path} — requires review`);
    if (metadata.warning) warnings.push(`${path} — ${metadata.warning}`);
    if (metadata.confidence != null && metadata.confidence < 0.8) {
      warnings.push(`${path} — confidence ${Math.round(metadata.confidence * 100)}%`);
    }
  }
  return [...new Set(warnings)];
}

function renderValidation() {
  const missing = requiredMissing();
  const warnings = [...new Set([...metadataWarnings(), ...itemWarnings()])];
  const missingText = missing.length
    ? `Required draft checks still open:\n${missing.map((field) => `- ${field}`).join("\n")}`
    : "All required draft checks are complete.";
  const warningText = warnings.length
    ? `\nReview:\n${warnings.map((warning) => `- ${warning}`).join("\n")}`
    : "";

  els.validation.classList.remove("hidden");
  els.validation.textContent = `${missingText}${warningText}`;

  const completion = completionSummary();
  els.completeness.textContent = `${completion.percent}%`;
  els.completenessBar.style.width = `${completion.percent}%`;
  els.fieldCount.textContent = completion.label;
  renderMetadata();
}

function completionSummary() {
  const core = REQUIRED_CORE_PATHS.filter(
    (path) => isNonEmpty(scalarValue(taxState.draft, path)),
  ).length;
  const bankRequired = scalarValue(taxState.draft, "/payment/method") === "bank_transfer";
  const bank = bankRequired
    ? ["/payment/bank_name", "/payment/account_holder", "/payment/account_number_last4"]
      .filter((path) => isNonEmpty(scalarValue(taxState.draft, path)))
      .length
    : 0;
  const itemsComplete = taxState.draft.items.filter(
    (item) => REQUIRED_ITEM_KEYS.every((key) => isNonEmpty(item[key])),
  ).length;
  const declarations = ["/declarations/certified_accurate", "/declarations/consent_to_processing"]
    .filter((path) => scalarValue(taxState.draft, path) === true).length;

  const completed = core + bank + itemsComplete + declarations;
  const total = REQUIRED_CORE_PATHS.length + (bankRequired ? 3 : 0) +
    taxState.draft.items.length + 2;
  const percent = total === 0 ? 100 : Math.min(100, Math.round((completed / total) * 100));

  return {
    percent,
    label: `${completed}/${total} completion points · ${taxState.draft.items.length} line(s)`,
  };
}

function metadataForPath(path) {
  return taxState.metadata.get(path) || {};
}

function renderMetadata() {
  for (const element of document.querySelectorAll("[data-meta]")) {
    const metadata = metadataForPath(element.dataset.meta);
    const parts = [];
    if (metadata.source) parts.push(`Source: ${metadata.source}`);
    if (metadata.confidence != null) parts.push(`Confidence: ${Math.round(metadata.confidence * 100)}%`);
    if (metadata.status === "user_edited") parts.push("Entered manually");
    if (metadata.requiresReview) parts.push("Review");
    if (metadata.evidence) parts.push(`Evidence: ${metadata.evidence}`);

    element.textContent = parts.join(" · ");
    element.classList.toggle(
      "review",
      Boolean(metadata.requiresReview || metadata.confidence < 0.8),
    );
  }

  for (const wrapper of document.querySelectorAll(".field-wrapper")) {
    const input = wrapper.querySelector("[data-path]");
    if (!input) continue;
    const metadata = metadataForPath(input.dataset.path);
    const locked = taxState.lockedScalars.has(input.dataset.path);
    wrapper.classList.toggle("locked", locked);
    const uncertain = metadata.requiresReview ||
      (metadata.confidence != null && metadata.confidence < 0.8);
    input.classList.toggle("needs-review-input", Boolean(uncertain));
  }
}

function createItemInput(item, field, index) {
  const wrapper = document.createElement("div");
  wrapper.className = "field-wrapper";
  const fieldId = `refund-item-${index}-${field.key}`;
  const label = document.createElement("label");
  label.className = "sr-only";
  label.htmlFor = fieldId;
  label.textContent = `${field.key.replace(/_/g, " ")} ${index + 1}`;

  let input;
  if (field.type === "enum") {
    input = document.createElement("select");
    const blank = document.createElement("option");
    blank.value = "";
    blank.textContent = "Select";
    input.appendChild(blank);
    for (const option of field.enum) {
      const optionElement = document.createElement("option");
      optionElement.value = option;
      optionElement.textContent = option.replaceAll("_", " ");
      input.appendChild(optionElement);
    }
  } else {
    input = document.createElement("input");
    if (field.type === "money") {
      input.type = "number";
      input.step = "0.01";
      input.min = "0";
    }
  }

  input.id = fieldId;
  input.name = fieldId;
  input.value = item[field.key] ?? "";
  input.dataset.itemId = item.id;
  input.dataset.itemField = field.key;
  input.addEventListener("change", () => {
    const value = field.type === "money"
      ? normalizeNumber(input.value)
      : input.value;
    taxState = markItemEdited(taxState, item.id, field.key, value);
    renderForm();
  });

  wrapper.appendChild(input);
  const cell = document.createElement("td");
  cell.appendChild(label);
  cell.appendChild(wrapper);
  return cell;
}

function renderItems() {
  const container = document.querySelector("#items-container");
  const items = taxState.draft.items;
  container.replaceChildren();

  if (!items.length) {
    const empty = document.createElement("div");
    empty.className = "empty-row";
    empty.textContent = "No refund lines yet. Add a line or ask the assistant.";
    container.appendChild(empty);
    return;
  }

  const table = document.createElement("table");
  const header = document.createElement("tr");
  for (const field of ITEM_FIELDS) {
    const cell = document.createElement("th");
    cell.textContent = field.key.replace(/_/g, " ");
    header.appendChild(cell);
  }
  const actionCell = document.createElement("th");
  actionCell.textContent = "";
  header.appendChild(actionCell);
  table.appendChild(header);

  items.forEach((item, index) => {
    const row = document.createElement("tr");
    for (const field of ITEM_FIELDS) {
      row.appendChild(createItemInput(item, field, index));
    }

    const actionCell = document.createElement("td");
    const removeButton = document.createElement("button");
    removeButton.type = "button";
    removeButton.textContent = "Remove";
    removeButton.addEventListener("click", () => {
      taxState = removeItem(taxState, item.id);
      renderForm();
    });
    actionCell.appendChild(removeButton);
    row.appendChild(actionCell);
    table.appendChild(row);
  });

  container.appendChild(table);
}

function syncInputs() {
  for (const input of document.querySelectorAll("[data-path]")) {
    const path = input.dataset.path;
    const field = scalarField(path);
    const value = scalarValue(taxState.draft, path);
    if (field.type === "checkbox") input.checked = value === true;
    else if (value !== undefined) input.value = value ?? "";
  }
}

function renderTotals() {
  const itemsTotal = taxState.draft.items.reduce(
    (total, item) => total + (normalizeNumber(item.refund_amount) || 0),
    0,
  );
  const requested = normalizeNumber(scalarValue(taxState.draft, "/claim/requested_amount"));
  els.itemsTotal.value = formatMoney(itemsTotal);
  els.requested.value = formatMoney(requested);

  if (requested != null && Math.abs(itemsTotal - requested) > 0.009) {
    els.totalsWarning.classList.remove("hidden");
    els.totalsWarning.textContent =
      `Line total ${formatMoney(itemsTotal)} does not match entered claim amount ${formatMoney(requested)}.`;
  } else {
    els.totalsWarning.classList.add("hidden");
    els.totalsWarning.textContent = "";
  }
  els.reviewWarnings.classList.toggle("hidden", !itemWarnings().length && !metadataWarnings().length);
  els.reviewWarnings.textContent = [...new Set([...metadataWarnings(), ...itemWarnings()])]
    .map((warning) => `- ${warning}`).join("\n");
}

function formatMoney(value) {
  if (!Number.isFinite(value)) return "";
  return value.toLocaleString("en-GB", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
}

function renderForm() {
  renderItems();
  syncInputs();
  renderTotals();
  renderValidation();
}

function wireStaticInputs() {
  for (const input of document.querySelectorAll("[data-path]")) {
    input.addEventListener("change", () => {
      const path = input.dataset.path;
      const field = scalarField(path);
      const value = field.type === "boolean"
        ? input.checked
        : field.type === "money"
          ? normalizeNumber(input.value)
          : input.value;
      taxState = markScalarEdited(taxState, path, value);
      renderForm();
    });
  }

  els.addItem.addEventListener("click", () => {
    taxState = addEmptyItem(taxState);
    renderForm();
  });

  els.exportButton.addEventListener("click", exportDraft);
}

function exportDraft() {
  const blob = new Blob(
    [JSON.stringify({
      contract: CONTRACT,
      draft: taxState.draft,
      totals: { items_total: normalizeNumber(els.itemsTotal.value.replaceAll(",", "")) || 0 },
    }, null, 2)],
    { type: "application/json" },
  );
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = "tax-refund-claim-draft.json";
  link.click();
  URL.revokeObjectURL(link.href);
}

function safeJsonParse(text) {
  if (!text) return { error: new Error("empty tool arguments") };
  try {
    return { value: JSON.parse(text) };
  } catch (error) {
    return { error };
  }
}

function appendProtocolMessages(messages) {
  if (Array.isArray(messages)) protocolMessages = messages;
}

function makeCallbacks() {
  let assistantReply;
  let pendingPatchArguments = "";
  let pendingPatchToolCallId = "";
  let pendingPatchToolName = "";

  return {
    onEvent({ event }) {
      if (event.type === AG_UI_EVENT.toolCallStart) {
        pendingPatchArguments = "";
        pendingPatchToolCallId = event.toolCallId;
        pendingPatchToolName = event.toolCallName;
        if (event.toolCallName === TOOL_NAME) {
          setRunPhase("streaming", `Calling ${event.toolCallName}`);
        }
        return;
      }

      if (event.type === AG_UI_EVENT.toolCallArgs && event.toolCallId === pendingPatchToolCallId) {
        pendingPatchArguments += event.delta || "";
        const partial = safeJsonParse(pendingPatchArguments);
        if (pendingPatchToolName === TOOL_NAME && !partial.error && Array.isArray(partial.value.patch)) {
          setRunDetail(`Receiving ${partial.value.patch.length} patch operation(s)`);
        }
        return;
      }

      if (event.type !== AG_UI_EVENT.toolCallEnd || event.toolCallId !== pendingPatchToolCallId) return;

      const parsed = safeJsonParse(pendingPatchArguments);
      currentTaxPatch = {
        toolCallId: event.toolCallId,
        toolName: pendingPatchToolName,
      };

      if (pendingPatchToolName !== TOOL_NAME) return;
      if (parsed.error) {
        currentTaxPatch.result = {
          status: "rejected",
          appliedPaths: [],
          rejectedPatches: [{ path: "/patch", reason: "invalid_json", message: parsed.error.message }],
        };
        setRunDetail("Claim update rejected: invalid tool arguments", true);
        appendSystem("Claim update rejected: the tool arguments were not valid JSON.");
        return;
      }

      const applied = applyTaxRefundPatch(taxState, parsed.value);
      taxState = applied.state;
      currentTaxPatch.result = applied.result;
      renderForm();

      if (applied.result.status === "rejected") {
        setRunDetail("Claim update rejected", true);
        appendSystem(`Rejected ${applied.result.rejectedPatches.length} claim update(s). The model received the reasons.`);
        return;
      }

      appendSystem(
        `${applied.result.status === "applied" ? "Applied" : "Partially applied"} ` +
        `${applied.result.appliedPaths.length} claim update(s). ` +
        `Rejected ${applied.result.rejectedPatches.length}.`,
      );
    },
    onRunStartedEvent() {
      setRunPhase("running", "AG-UI run started");
    },
    onTextMessageStartEvent() {
      setRunPhase("streaming", "Assistant reply started");
      assistantReply = appendChat("assistant", "");
    },
    onTextMessageContentEvent({ event }) {
      assistantReply ??= appendChat("assistant", "");
      assistantReply.textContent += event.delta;
      els.conversation.scrollTop = els.conversation.scrollHeight;
    },
    onRunFinishedEvent({ event, messages }) {
      appendProtocolMessages(messages);
      const outcome = event.outcome;
      if (outcome?.type === "interrupt" && currentTaxPatch?.toolCallId) {
        setRunPhase("resuming", "Claim patch handled; continuing assistant");
        void resumeAfterTaxPatch(outcome.interrupts, messages);
        return;
      }
      setRunPhase("complete", "AG-UI run finished");
      setRunDetail("Claim draft synchronized");
    },
    onRunErrorEvent({ event }) {
      setRunPhase("error", event.message || "AG-UI run error");
    },
    onRunFailed({ error }) {
      setRunDetail(error.message, true);
      setRunPhase("error", "Failed");
      appendSystem(`Run failed: ${error.message}`);
    },
  };
}

async function resumeAfterTaxPatch(interrupts, messages) {
  if (resumeDepth >= 4) {
    setRunDetail("Too many consecutive client-tool resumes; stopping for safety.", true);
    return;
  }

  const interrupt = interrupts?.find((entry) => entry.toolCallId === currentTaxPatch?.toolCallId);
  if (!interrupt || currentTaxPatch?.resumed || !currentTaxPatch?.result) return;

  const resultMessage = {
    id: uniqueId("tool-result"),
    role: "tool",
    toolCallId: currentTaxPatch.toolCallId,
    content: JSON.stringify({
      status: currentTaxPatch.result.status,
      formTarget: CONTRACT.target,
      appliedBy: "browser_form_reducer",
      appliedPaths: currentTaxPatch.result.appliedPaths,
      rejectedPatches: currentTaxPatch.result.rejectedPatches,
      requiredMissing: requiredMissing(),
    }),
  };

  appendProtocolMessages(messages);
  protocolMessages.push(resultMessage);
  currentTaxPatch.resumed = true;

  const resumeAgent = new HttpAgent({
    url: "/ag-ui",
    headers: { "X-Agent-User-ID": runtimeCorrelation.userId },
    threadId: runtimeCorrelation.threadId,
    initialMessages: protocolMessages,
    fetch: agentFetch,
  });

  activeAgentRun = resumeAgent;
  resumeDepth += 1;
  setBusy(true);
  setRunPhase("resuming", "Resuming refund copilot");

  try {
    await resumeAgent.runAgent({
      runId: uniqueId("run-resume"),
      tools: [taxRefundPatchTool()],
      forwardedProps: buildForwardedProps(),
      resume: [{
        interruptId: interrupt.id,
        status: "resolved",
        metadata: { tool_call_id: currentTaxPatch.toolCallId },
      }],
    }, makeCallbacks());
  } catch (error) {
    setRunDetail(error.message, true);
    setRunPhase("error", "Resume failed");
    appendSystem(`Resume failed: ${error.message}`);
  } finally {
    if (activeAgentRun === resumeAgent) {
      activeAgentRun = undefined;
      setBusy(false);
    }
  }
}

async function sendConversationMessage(content) {
  if (activeAgentRun) {
    setRunDetail("Assistant is busy");
    return;
  }

  const userMessage = { id: uniqueId("user"), role: "user", content };
  conversationMessages.push(userMessage);
  appendChat("user", content);
  setBusy(true);
  setRunPhase("connecting");
  protocolMessages = toVisibleMessages();
  resumeDepth = 0;

  const agent = new HttpAgent({
    url: "/ag-ui",
    headers: { "X-Agent-User-ID": runtimeCorrelation.userId },
    threadId: runtimeCorrelation.threadId,
    initialMessages: protocolMessages,
    fetch: agentFetch,
  });
  activeAgentRun = agent;

  try {
    await agent.runAgent({
      runId: uniqueId("run"),
      tools: [taxRefundPatchTool()],
      forwardedProps: buildForwardedProps(),
    }, makeCallbacks());
    setRunPhase("complete", "AG-UI run finished");
    setRunDetail("Claim draft synchronized");
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      setRunPhase("stopped", "Run aborted");
    } else {
      setRunPhase("error", error instanceof Error ? error.message : String(error));
    }
  } finally {
    if (activeAgentRun === agent) {
      activeAgentRun = undefined;
      setBusy(false);
    }
  }
}

function submitInput() {
  const content = els.input.value.trim();
  if (!content || activeAgentRun) return;
  els.input.value = "";
  void sendConversationMessage(content);
}

function selectFormTab(tab) {
  const selectedTab = tab.dataset.formTab;
  for (const button of document.querySelectorAll("[data-form-tab]")) {
    button.setAttribute("aria-selected", String(button.dataset.formTab === selectedTab));
  }
  for (const panel of document.querySelectorAll("[data-form-panel]")) {
    panel.hidden = panel.dataset.formPanel !== selectedTab;
  }
}

function wireEvents() {
  document.addEventListener("click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;

    if (target.closest("#conversation-send")) {
      event.preventDefault();
      submitInput();
      return;
    }
    const tab = target.closest("[data-form-tab]");
    if (tab) {
      event.preventDefault();
      selectFormTab(tab);
    }
  });

  els.assistantForm.addEventListener("submit", (event) => {
    event.preventDefault();
    submitInput();
  });

  els.input.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    submitInput();
  });
}

els.sourceNotes.value = SAMPLE_SOURCE_NOTES;
renderForm();
wireStaticInputs();
wireEvents();
setRunPhase("idle");
setRunDetail("Claim contract ready. This demo stores draft state only in the browser.");

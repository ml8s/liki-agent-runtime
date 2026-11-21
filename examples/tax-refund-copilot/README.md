# Tax Refund Claim Copilot

This example embeds an AgentGo assistant beside a draft tax refund claim form.
The user can paste, type, or speak tax facts. The agent maps those facts to a
versioned `tax_refund_form.patch` contract. The browser reducer validates the
patch and fills the claim form incrementally.

This is **not** a complete tax product, a tax calculator, tax advice, a tax
authority integration, or a filing system. There is no tax backend. The claim
exists in browser state and can be exported as JSON.

---

## Product shape

```text
+----------------------+ +----------------------+
| Assistant panel      | | Refund claim form    |
| - paste tax facts    | | - claimant details   |
| - typed facts        | | - refund reason      |
| - follow-up answers  | | - refund breakdown   |
| - patch summaries    | | - payment details    |
+----------------------+ | - review warnings    |
                         +----------------------+
```

The Assistant and form are separate UI surfaces in the same browser app. They
share claim state in the browser. The AgentGo runtime does not add a separate
tax backend.

---

## Core flow

```text
User types or pastes tax facts into the assistant
        ↓
Assistant sends an AG-UI run to AgentGo
        ↓
AgentGo exposes `tax_refund_form.patch` to the model as an AG-UI client tool
        ↓
Model emits incremental claim patch arguments
        ↓
Browser validates allowed paths and applies the contract
        ↓
Assistant summarizes the fields and asks follow-up questions
        ↓
User exports the draft JSON after review
```

---

## Claim scope

The example supports a focused generic refund claim:

### Claim profile

- Claimant name;
- taxpayer reference last four;
- tax period;
- refund reason;
- currency;
- requested refund amount;
- explanation.

### Contact

- email;
- phone;
- mailing address.

### Payment

- refund method;
- bank name;
- account holder;
- account number last four;
- bank reference.

### Refund breakdown

- description;
- category;
- period;
- original amount;
- tax withheld;
- refund amount;
- evidence reference.

---

## UI contract

The frontend declares the `tax_refund_form_patch` AG-UI client tool. Every call
must carry `contractVersion: "1.0"`, `target: "tax_refund_claim_v1"`, and a
`patch` array.

Operations are intentionally narrow:

- `set` / `clear` target registered scalar paths;
- `add_item`, `update_item`, `remove_item`, and `upsert_items` target `/items`;
- item rows may contain only the registered item fields;
- declaration checkboxes are user-only and are not exposed to the assistant.

The reducer returns a machine-readable tool result with:

- `status`: `applied`, `partial`, or `rejected`;
- `appliedPaths`;
- `rejectedPatches` with stable reasons such as `invalid_enum_value`,
  `user_edited`, `user_only_field`, and `invalid_contract_version`;
- `requiredMissing`.

The browser reducer is authoritative. It rejects an invalid contract version or
target, malformed arguments, unknown paths, enum/type mismatches, invalid money
values, unknown item fields, operation/path mismatches, and paths or line fields
that the user has already edited manually.

---

## Example interaction

Paste this into the Assistant:

```text
Claimant Dana Ellison, reference ending 4821, period 2025. There was an
assessed overpayment of 780.00, a withholding correction of 415.00, and a
duplicate instalment of 120.00. Currency EUR. Contact
dana.ellison@example.com. Prepare the refund claim draft.
```

The Assistant should incrementally patch:

```text
/claim/claimant_name                         Dana Ellison
/claim/taxpayer_reference_last4              4821
/claim/tax_period                            2025
/claim/currency                              EUR
/contact/email                               dana.ellison@example.com
/items                                       three refund lines
```

It should also ask about missing facts, such as phone, mailing address, and
whether the claimant certifies the statement.

---

## Build and run

From the repository root:

```bash
make run
```

Or from this example:

```bash
cd examples/tax-refund-copilot
make setup
# Edit .env and set AGENTGO_LLM_API_KEY.
make test
make run
```

Open:

```text
http://localhost:8082
```

The model must support tool calling. A tool-capable OpenAI-compatible provider
is recommended for this example.

---

## Deterministic checks in the UI

The reducer and UI calculate only simple derived facts:

- total entered claim amount;
- total refund-line amount;
- missing required claim fields;
- payment-method completeness.

They do not calculate final tax owed, eligibility, interest, penalties, or
filing approval.

---

## Safety boundaries

The Assistant must not:

- provide tax advice;
- invent tax-law conclusions;
- claim refunds or credits;
- calculate final tax owed;
- invent references, account numbers, names, or document numbers;
- submit a claim;
- fill fields outside the registered contract.

The UI marks uncertain values as `requiresReview`. Manual user edits are
protected from later assistant patches.

---

## Local data boundary

The claim is frontend state. There is no tax database, tax authority API,
filing adapter, PDF parser, or persistent backend. Export JSON is the only way
to preserve the draft outside the current browser session.

---

## Cleanup

```bash
make stop
# or, including the local AgentGo session volume:
make clean
```

---

## Non-goals

This example intentionally omits:

- a specific country's tax forms;
- state, provincial, or national filing rules;
- e-file;
- audit support;
- tax-law reasoning;
- PDF parsing;
- persistence of the authoritative draft beyond the browser/runtime demo state;
- authentication and multi-user access;
- real accounting integration.

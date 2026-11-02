# Guardrails: actions, detectors and where they apply

A guardrail rule has a **kind** (what it looks for), a **stage** (where it applies), an **action** (what it does when it finds something)
and a **config**. Design record: [ADR-0032](../ADR/0032-guardrail-actions-and-hops.md). A broader redesign proposal is in
[guardrails-redesign-proposal.md](guardrails-redesign-proposal.md).

## Actions

| Action | What happens |
|---|---|
| `block` (default) | The text is refused. A rule with no `action` blocks, as every rule did before actions existed. |
| `redact` | Each match is replaced by a typed placeholder (`[IBAN]`, `[EMAIL]`, `[AWS_ACCESS_KEY]`); the rest of the text continues. The same text always gives the same result. |
| `mask` | Each match is replaced by asterisks keeping its last four characters (`***************1111`). Secrets are always replaced whole. |
| `warn` | The text passes. The rule is recorded in the audit event (`guardrail.<stage>_warned`) and in the trace's `guardrails.warned`. Use it to try a rule before it blocks. |
| `require_approval` | On a **tool call**, the run pauses for a person (the same pause as `approval_tools`), with the redacted arguments in the approval and the rule that asked. Anywhere else it behaves as `block`. |

`redact` and `mask` need a rule that finds spans: `pii_detection`, `content_filter`, `regex_filter` (with `block_on_match` true) or `custom`.

Rules that judge (block, warn, require_approval) see the original text. Rules that redact run afterwards, in order, each on the previous
one's output, so a redacting rule cannot hide a match from a blocking rule. Custom endpoints run last, on the cleaned text.

## Stages

`input` (the user's message, and tool results, which are input to the model), `output` (the reply, and tool-call arguments), `both`
(input and output), `retrieval` (each retrieved chunk before it enters the prompt) and `all` (input, output and retrieval). A rule with no
stage applies to input and output. **Retrieval is opt-in**: a rule written before it was guarded never applies to chunks. A chunk that a
rule blocks is dropped; one that it redacts goes in redacted.

## PII and secret detectors (`pii_detection`)

`config.patterns` lists detector names or groups. With no list, the original four are checked: `email`, `phone`, `ssn`, `credit_card`.

| Detector | Validation |
|---|---|
| `credit_card` | Luhn, issuer prefix and length |
| `iban` | country length, ISO 7064 mod 97 |
| `ssn` | SSA rules (area not 000, 666, 9xx; group not 00; serial not 0000) |
| `aadhaar` | Verhoeff, first digit 2 to 9 |
| `pan_in` | holder-type letter |
| `uk_nino`, `nhs_number` | invalid prefixes; mod 11 |
| `eu_vat` | DE, FR, IT, BE, PL check digits |
| `es_dni` | letter by mod 23 (DNI and NIE); `nl_bsn`: 11-proof |
| `phone_intl`, `email`, `phone` | 8 to 15 digits; shape only |
| `ip_address`, `ipv6_address` | parsed |
| `aws_access_key`, `aws_secret_key`, `github_token`, `slack_token`, `jwt`, `private_key`, `api_key` | shapes; a JWT header must be JSON with `alg` |

Groups: `default`, `pii`, `secrets`, `national_id`, `all`. `allowlist` (exact values) and `allow_domains` (email domains) leave listed values alone.

```json
{"kind": "pii_detection", "stage": "all", "action": "redact",
 "config": {"patterns": ["pii", "secrets"], "allowlist": ["support@example.com"], "allow_domains": ["example.com"]}}
```

## Regex rules

`regex_filter`: `pattern` (Go RE2 syntax) and `label` (the placeholder name when redacting, default `REDACTED`). RE2 matches in time linear in the
text, so a pattern cannot cause catastrophic backtracking. Bounds: patterns and word lists up to 4 KiB, lists up to 1000 entries, text up to 1 MiB for a
rule that finds spans (a longer text is refused), and at most 128 enabled rules per evaluation.

## Try a rule

`POST /api/v1/guardrails/test` with `{"text": "...", "stage": "output", "rules": [...]}` returns `outcome` (`pass`, `redacted`, `warned`,
`approval_required`, `blocked`), the `text` that would continue, and each rule's findings (type and count). Nothing is stored. The CLI:
`agentoven guardrails test`.

## What is never stored

Redacted input is what a trace and a stored session keep; redacted output is what the trace, the run's last turn and the session keep. Audit
events and results carry rule ids, detector types and counts, never the matched text. Traces show `metadata.guardrails.redacted` and `warned`.

## Surfaces that cannot be rewritten

A held-back SSE stream, a streamed A2A reply, a bot action, spoken input and a captured replay request cannot be changed in place, so a rule that
would redact there blocks instead of passing the sensitive text on.

## Custom rules

`kind: custom` calls an evaluator the edition installs. The community edition has none, so a custom rule passes. Pro posts the text to the kitchen's
HTTPS endpoint (signed, SSRF-protected, fail-closed unless the rule says otherwise); see the Pro documentation.

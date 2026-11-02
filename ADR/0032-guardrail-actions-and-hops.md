# ADR-0032: Guardrail actions, validated detectors and more places that are guarded (OSS)

**Status:** Accepted (built 2026-11-03; see "Not done")
**Date:** 2026-11-03
**Authors:** AgentOven Engineering
**Scope:** OSS (the engine, the contracts and the seams). The signed webhook behind `custom`, the workspace rule store, the test route with a kitchen's rules and the dashboard page are Pro: `agentoven-pro` ADR-0049.

---

## Context

A guardrail returned pass or fail. PII had four United States shaped patterns with no validation (any sixteen digits was a card), `custom`
was a no-op in both editions, and the only things checked were the user's message, the final answer and tool arguments and results.
Retrieved chunks went into the prompt unchecked. A rule could only refuse; a team that wanted `IBAN` out of a reply, but not the reply
refused, had no way to say so.

Already true before this ADR (checked 2026-11-03, the gap analysis of 2026-11-02 is stale on these two): tool-call arguments and tool results
were evaluated in `executor/tool_dispatch.go`, and a delegation was held to the target's guardrails (`executor.DelegationBoundary`, A2A
`proxyA2AGuarded`). What was missing there was what to do beyond refusing.

## Decision

1. **An action per rule** (`models.Guardrail.Action`, empty means `block`, so every existing rule behaves as it did):
   - `block`: refuse (default).
   - `redact`: replace each matched span with a typed placeholder, `[IBAN]`, and continue with the rest of the text. Deterministic.
   - `mask`: replace with asterisks keeping the last four characters; a secret is never partly kept.
   - `warn`: pass, and say so in the audit event, the trace counts and the evaluation.
   - `require_approval`: on a tool call, pause the run through the existing approval pause with the (redacted) arguments on the screen.
     On any other surface nothing can wait for a person, so it behaves as `block`.
2. **The evaluation says what continues.** `GuardrailEvaluation.Text` (never serialised) is the text with all redactions applied;
   `Apply(original)` returns it. `Passed` is false for a block and for `require_approval`, so a caller that only reads `Passed` stays safe.
   `ApprovalRequired` is true only when nothing blocked outright. Results carry rule id and name, the action, `Redacted` and `Findings`
   (type and count), never the matched text, so serialising a result into an audit event or response cannot leak the original.
3. **Order.** Rules that judge (block, warn, require_approval) all see the original text. Rules that rewrite (redact, mask) then run in order,
   each on the previous one's output, and custom endpoints last on the cleaned text. A redacting rule therefore cannot hide a match from a
   blocking rule.
4. **Detectors with validation** (`internal/guardrails/detectors.go`, `checksum.go`): email, phone (the old loose one) and `phone_intl`, `ssn` (SSA
   rules), `credit_card` (Luhn plus issuer prefix and length), `iban` (country length and mod 97), `aadhaar` (Verhoeff), `pan_in`, `uk_nino`,
   `nhs_number` (mod 11), `eu_vat` (DE, FR, IT, BE, PL check digits), `es_dni`, `nl_bsn`, `ip_address`, `ipv6_address`, and secrets: `aws_access_key`,
   `aws_secret_key`, `github_token`, `slack_token`, `jwt` (header must be JSON with `alg`), `private_key` (a cut-off key is redacted to the end
   of the text), `api_key`. Groups: `default` (the four original), `pii`, `secrets`, `national_id`, `all`. A rule with no `patterns` still checks
   the original four. `allowlist` (exact values) and `allow_domains` (email) leave listed values alone. Overlapping findings: the longer wins,
   then the checksum-validated one.
5. **Regex rules.** `regex_filter` gains `label` (the placeholder) and a bounded pattern. Go's `regexp` is RE2: matching is linear in the text, so
   catastrophic backtracking (ReDoS) cannot happen. The bounds that remain are on size: a pattern or word list over 4 KiB, a list over 1000
   entries, a text over 1 MiB for a span-finding rule (refused, fail closed) and more than 128 enabled rules for one evaluation (refused).
6. **Custom rules.** `contracts.CustomGuardrailEvaluator` is the seam: Pro installs one (`Handlers.SetCustomGuardrails`). The endpoint answers
   `{action, text?, reason?}` with `allow`, `warn`, `redact`, `require_approval` or `block`; the rule's own action caps it (a rule set to `warn` turns
   a `block` into a warning: shadow mode). An unreachable or malformed endpoint follows the rule's `fail_open` (closed by default). With no
   evaluator installed a custom rule passes, as it always has in the community edition.
7. **More hops.**
   - Retrieved chunks: stage `retrieval` (and `all`) apply to each chunk before it enters the prompt (`executor.guardChunks`). A blocked chunk is
     dropped, a redacted one goes in redacted, and nothing enters if the rules cannot be established. Rules written before this ADR (`input`,
     `output`, `both`, no stage) never apply to chunks, so a topic list does not start dropping them.
   - Tool-call arguments are redacted before they are journaled, shown to an approver or run; tool results are redacted before the model sees them.
   - Delegation: the target's input rules can now redact the message, and its output rules the answer.
   - A2A proxy: the JSON-RPC body is rewritten (text parts) in both directions.
8. **Where a surface cannot rewrite, it refuses.** A held-back SSE stream, a streamed A2A reply, a bot action, spoken input and a captured replay
   request cannot be changed in place; a redaction there blocks instead of releasing the sensitive text.
9. **The original is not stored by the guardrail layer.** Redacted input is what the trace and the session store, redacted output is what the
   trace, the executor's last turn and the stored session reply hold, and audit events carry rule ids, types and counts only. Traces show
   `metadata.guardrails.redacted` / `warned`, and the executor's trace carries `guardrails`.
10. **Routes.** `POST /api/v1/guardrails/test` evaluates rules sent in the body against a text and reports the outcome, the redacted text and each rule's
    findings. `GET /api/v1/guardrails/kinds` lists kinds, stages, actions and detectors. `contracts.StageGuardrailService` is the optional interface
    (`Evaluate`, `Scrub`, `ValidateGuardrail`); services that only implement `GuardrailService` keep working, without the new hops.

`guardrails.Effective` stamps the kitchen on every rule it returns (`Guardrail.KitchenID`, overwriting what a client sent), so an evaluator that calls out
knows whose rule it is.

## Consequences

- Existing rules, and rules saved without an action, block exactly as before. The detector set behind `pii_detection` with no list is unchanged
  in kind, but `ssn` and `credit_card` now validate: a sixteen-digit number that fails Luhn, or an SSN with area 000, 666 or 9xx, is no longer
  flagged, and a 15-digit American Express number now is.
- A span-finding rule (PII, regex) no longer scans a text over 1 MiB: it refuses it (fail closed). Before, such a text was scanned however large; the cost of a scan (about a quarter of a second per MiB for every detector at once) is why the bound exists.
- A redacting rule on a held-back stream refuses the reply (403) instead of redacting it. Rewriting frames of an SSE stream is a separate piece of work.
- Evaluating custom rules, and a rule set with webhooks, adds a network call to the path; Pro bounds it (ADR-0049).

## Not done

- Redacting inside a held-back SSE stream or a streamed A2A reply (they refuse instead).
- The model's raw reply remains in the run journal (needed to resume) and in the router's call-capture hook; only what leaves the control
  plane, the trace and its spans, the stored session reply and audit events are redacted. A hosted framework's own turns (`execTraceFromProxy`) are not scrubbed.
- Token-level task events are checked per event, so a value split across two token events is not seen until the final event.
- Per-turn redaction of intermediate assistant text of an in-process run.
- Additional national identifier checksums (only those listed); an LLM-judge custom evaluator.
- Delegation to an external A2A agent is still refused (unchanged).

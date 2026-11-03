# ADR-0030: Background tasks take files by blob reference

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Authors:** AgentOven Engineering
**Scope:** OSS (`POST /api/v1/tasks`, one request field and one status-code fix). The feature that needs it, inbound triggers, is Pro ([agentoven-pro ADR-0047](https://github.com/agentoven/agentoven-pro)).

---

## Context

Pro's inbound triggers start an agent from an e-mail or a file that arrived. The message text is easy; the attachments (a scanned claim form, a photo of damage, a PDF invoice) are what the agent has to read. The routes that already take media (`/agents/{name}/invoke`, session messages, A2A `message/send`) answer the caller and wait for the model. A trigger cannot hold an HTTP request open for a model call, so it starts the agent as a background task (`POST /api/v1/tasks`, the durable, budgeted, resumable run of ADR long-running-agents), and that route took a text `message` only.

Found on the way, in `BakeRecipe`: the check that turns an executor's `permission_denied: <why>` into a 403 compared the first 16 characters of the message with the 17-character word, so it matched only the bare word. Every real refusal (an environment that needs approval, a role that may not run in staging) was answered `500`. A caller that retries on 5xx, as a webhook sender does, would have retried a refusal for ever.

## Decision

1. **`content_parts` on task creation.** `POST /api/v1/tasks` accepts `content_parts`: media parts (`image_url`, `audio`, `video`, `file`), each with `media.blob_ref` (from `POST /api/v1/blobs`) and `media.mime_type`. At most 16. They become `ChatMessage.ContentParts` of the task's first message, the same field the other routes fill.
2. **References only.** Inline `data` and `url` are refused (400). A task is queued and journaled, so it carries a reference and not the bytes, and the control plane does not fetch a URL on the caller's say-so (no SSRF surface on a route a webhook can reach). A blob the kitchen does not own is 404 before any task exists, the same answer as for an unknown one.
3. **The 403 fix.** `BakeRecipe` uses `strings.HasPrefix(err, "permission_denied")`, as `RerunRecipeRun` already did. Test: `TestAnExecutorsPermissionRefusalIsA403NotA500`.

Text guardrails still judge `message` only; media is not inspected, as on invoke (ADR guardrails: a modality the engine cannot read is named, not silently allowed).

## Consequences

- Any caller of the tasks API can attach files to a background task. Whether the model can read them is decided by the router's media policy as for any other call (a model that cannot read a PDF refuses it when the task runs).
- Blobs expire (`AGENTOVEN_BLOB_TTL_DAYS`, 7 by default, counted from the last read or write); a task started long after its blob was stored can find it gone.
- A refused approval is a 403 for every caller of `bake`, which changes what an SDK sees for that case from `AgentOvenAPIError(500)` to `AgentOvenAPIError(403)`.

## Not done

- Media on `POST /api/v1/recipes/{name}/bake`: a recipe's input is JSON and the engine does not turn blob references in it into content parts for its agent steps. A trigger that targets a recipe hands the attachments over as `attachments: [{blob_ref, mime_type, name, size}]` in the input; whether a step uses them is the recipe's business.
- Text guardrails on file content.

## Verification

`TestATaskTakesFilesByBlobReferenceOnly` (text part, inline data, URL, no mime type, no media, seventeen parts: 400; unknown blob: 404; no task created). Pro e2e `scripts/e2e/e2e_triggers.py` (an e-mail with an image attachment read from a real IMAP server; the model's request carries the image).

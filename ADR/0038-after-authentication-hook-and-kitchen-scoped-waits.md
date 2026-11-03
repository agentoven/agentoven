# ADR-0038: After-authentication hook, kitchen-scoped wait release, decision routes in `pkg/contracts`

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** OSS control plane: `pkg/middleware` (the hook), `internal/api/middleware/auth.go`, `pkg/contracts` (`DecisionRoutes`, `DigestOf`, gate names), `internal/api/handlers` (wait release, recipe-gate scope, signed run decisions), `internal/workflow` (`ReleaseWait`), `internal/executor` (`RunState`).
**Pro side:** [Pro ADR-0031, 0041 and 0043](../../agentoven-pro/ADR/) amendments of 2026-11-03.

## Context

An adversarial review of the Pro audit chain and e-signatures found problems that live in the community code:
the audit middleware wraps the router and cannot see who was authenticated or which kitchen was settled, so
the intent record of a GxP decision was written before authentication from headers; a wait could be released
from another kitchen (the engine's release map had no kitchen, and the describer read nothing); a run whose
state could not be read was decided unsigned; `DigestOf` collapsed large integers and digested the error text of
an unencodable value; Pro's list of fail-closed decision routes was hand-kept and lagged the routes here.

## Decisions

1. **`pkgmw.WithAfterAuth` / `RunAfterAuth`.** A function added to the request context runs once per
   request after authentication and tenant resolution, before the route is served, with the verified identity
   (nil when the installation does not require authentication). It may return a changed request or an error that
   refuses the request (an `*contracts.AuthError` names its status). `AuthMiddleware.Handler` runs the hooks; a
   request that fails authentication, and a public path, never reaches them. Pro uses it to canonicalise the
   kitchen (name or id to the one name) and to write a decision's intent record with the caller known.
2. **Waits are kitchen-scoped.** `Engine.ReleaseWait(kitchen, runID, step)` releases only a wait of that
   kitchen. `Handlers.waitingStep` checks the run (kitchen, status, the step waiting) in the describer and in the
   handler, signed or not; the digest covers the run, recipe, step and wake time. A recipe-gate record that
   names no kitchen belongs to its run's kitchen (an empty owner is the default kitchen, never a wildcard,
   `ownedBy`).
3. **A signed run decision does not proceed blind.** With a signer installed, a run whose state cannot be read
   is 404 (`executor.ErrNoSuchRun`, wrapped by `RunState`) or 503, never "nothing to sign". `RunState` tells a
   missing run from an unreadable journal (`FileJournal` now wraps `ErrRunNotFound`).
4. **`contracts.DecisionRoutes`** (moved from `internal/api/handlers`, which keeps an alias) is the list an
   edition builds its fail-closed routes from; the route-table test still fails for a decision route not in it.
5. **`DigestOf`** keeps numbers as written (`UseNumber`) so integers above 2^53 stay distinct, and returns `""`
   for a value it cannot encode; Pro's signing service refuses an empty digest.
6. **New gate names:** `audit_erasure`, `gxp_switch`, `approver_policy` (Pro decides them; the names live here
   with the others).

## Consequences

- Embedders that call `Engine.ReleaseWait` pass the kitchen.
- `DigestOf` of an integer above 2^53 changes; nothing recomputes stored digests.
- A deployment with `AGENTOVEN_REQUIRE_AUTH` unset still runs the hooks for anonymous requests that the router accepts.

## Not done

- A second hook point for requests that are refused before authentication.
- The unsigned community resume path still reads the journal twice; it is kitchen-scoped but not transactional.

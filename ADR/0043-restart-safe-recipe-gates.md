# ADR-0043: Restart-safe recipe gates and waits (community seam)

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** `control-plane/internal/workflow`, `internal/store`, `pkg/models`, `pkg/server`. The Postgres implementation and the audit event are in Pro ADR-0059. Owner decision P1 ("restart-safe recipe gates"); closes gap G7 of the gap analysis for recipe runs.

## Problem

A recipe run lived in the memory of the process that started it. A human gate (an `approval_records` row, a goroutine and an in-process timer) and a wait step (a goroutine and a timer) were lost with the process: the run stayed `running` or `paused` for ever, a gate that was approved while the server was down was never noticed, and a wait never ended. Steps that had finished were also lost, because the stored run was only written at a gate, at a wait and at the end.

## Decision

The engine makes runs restart-safe when its store implements two small optional interfaces (`internal/store/resume.go`). A store that does not keeps the old behaviour and the engine says so in its log. `AGENTOVEN_RUN_RESUME=false` turns it off.

1. **Checkpoint.** Every step is recorded on the stored run when it starts (`running`) and when it ends, and a gate or wait shows as `waiting` (`workflow/resume.go`: `begin`, `annotate`, `end`, one `runState` per run). A run with a gate waiting is `paused`.
2. **Lease.** `RunResumeStore.ClaimRun` leases a run to the process that starts it. One heartbeat per engine renews all its leases every `ttl/4` (`AGENTOVEN_RUN_LEASE_SECONDS`, default 20). A run whose lease ran out, or that never had one and is older than a minute, is taken over by `ClaimResumableRuns` (a pass at start, once the server answers `/health`, and every `ttl/2`). A process whose lease was taken over (`RenewRunLeases` no longer lists the run) stops executing it and records nothing: the run is not over. A graceful shutdown (`Server.Shutdown`, `Engine.ReleaseRuns`) gives the leases up so the next process takes the runs at once.
3. **Take-over.** `runRecipe` rebuilds the scheduler's tables from the stored step results, so a step that ended is not run again. The recipe's steps are hashed when the run starts; a run whose recipe changed since is failed with a message instead of resumed on a different graph. A sub-recipe's run is executed by its parent's process and re-attached through the `sub_run_id` recorded on the parent step.
4. **What happens to a step that was running** (`resumePolicy`): a human gate or wait is re-entered (the gate reuses its approval record and sends nothing again; the wait finds its timer); a sub-recipe step carries on in the run it started; `condition`, `router`, `fan_out`, `fan_in` and `rag` only read or compute and are run again; any other step (agent, evaluator, map, notify, a sub-recipe with no recorded run) may already have acted in another system, so it fails with "was running when the control plane restarted and may already have acted, so it was not run again" unless the recipe says `config.on_restart: rerun`.
5. **Timers.** The end of a gate's `max_wait_minutes` and of a wait is a row the store keeps on its own clock (`ArmRunTimer`: created once, returns what is left). A restart finds what is left of the original timer; a wait that ran out during downtime ends at once, a gate whose SLA ran out times out at once. The memory store keeps its timers in the snapshot.
6. **Decide once.** `ApprovalResolver.ResolveApproval` writes a decision only if the record is still pending. `ApproveGate`, `ApproveGateAs` and `ExpireGate` use it, so two answers, or an answer and an expiry, on one replica or two, cannot both win; the loser is told the gate is not pending. A gate decided while nothing was running is taken as it stands by the process that resumes the run, once, because the step has no result until then.

`StepResult.Resumed` marks a gate, wait or sub-recipe step that was taken up again, or a step that was not run again. `Engine.SetResumeHook` lets an edition record the take-over (Pro writes `recipe.run.resumed` and `recipe.run.failed`).

## Not done

- Per-write fencing: a process that stalls for longer than the lease and then writes (a hung call that returns) can still write one checkpoint before its next renewal notices; the new owner's next checkpoint overwrites it. A fencing token on `UpdateRecipeRun` would close it.
- A step that was running and is not marked `on_restart: rerun` fails; there is no idempotency-key protocol to re-run agent steps safely.
- Map steps and a gate inside a map are not resumable (the map is a running step and fails by policy).
- The trace spans of an agent step that was running when the process died are not closed.
- The in-memory fast path for a decision (`gates` channel) is per process; another replica's decision is seen on the next 5 second poll.
- Cancelling a run and releasing a wait act on the process that executes the run: with several replicas, the request must reach that one (before this change a run was always on the process that started it, so this is not new, but a resumed run can now be on another replica than the one that started it).
- The community edition's file-backed memory store is a single process; resume there means "restart this process", not high availability.

## Tests

`internal/workflow/resume_test.go` (two engines over one store: restart in the middle of a gate, decided during downtime with two processes racing, double resolve and expiry, wait keeping its deadline, wait and SLA expired during downtime, lease not stolen then taken over, lost lease stops quietly, changed recipe, step that may have acted, sub-recipe gate), `internal/store/resume_test.go`. Pro: ADR-0059.

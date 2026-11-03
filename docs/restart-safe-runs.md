# Runs that survive a restart

A recipe run that waits for a person (a `human_gate`) or for time (a `wait` step) is not lost when the control plane restarts, is deployed again or crashes.

## What you see

- While a run executes, its steps appear on the stored run as they start and end: `running`, `waiting` (a wait, with `wake_at`), a gate with `gate_status: waiting`. A run with a gate waiting is `paused`.
- After a restart the control plane takes the run over and carries on from there. The step shows `"resumed": true`. The approval for a gate that was waiting is the same record, in the same queue; nobody is asked again.
- A wait ends at the time it had, not a full wait after the restart. A wait that ran out while the server was down ends as soon as the run is taken over.
- A gate decided while the server was down (a reply through another path, another replica) is honoured when the run is taken over, once. A gate whose `max_wait_minutes` ran out while the server was down times out and takes its `on_timeout` path or fails the run.
- A decision on a gate that was already decided or expired is refused (the gate is not pending).

## What does not carry on

A step that was running when the process died (an agent, a map, a notification) may already have done something in another system, so it is not run again: it fails with "was running when the control plane restarted and may already have acted, so it was not run again", and the run fails like any run with a failed step. A step that is safe to repeat can say so:

```json
{"name": "lookup", "kind": "agent", "agent": "researcher", "config": {"on_restart": "rerun"}}
```

Steps that only read or compute (`condition`, `router`, `fan_out`, `fan_in`, `rag`) are always run again. A run whose recipe was edited after the run started is failed with a message instead of being resumed on a different recipe.

## Running more than one control plane

Each run is executed by one process, which holds a lease on it (`AGENTOVEN_RUN_LEASE_SECONDS`, default 20). If that process dies, another one (or the same one, restarted) takes the run over when the lease runs out; a graceful shutdown gives the leases up so a restarted process takes them at once. Switch the feature off with `AGENTOVEN_RUN_RESUME=false`. It needs a store that can lease runs: the PostgreSQL store of the enterprise edition and the in-memory store (whose snapshot file survives a restart of the single process) can.

Design: [ADR-0043](../ADR/0043-restart-safe-recipe-gates.md).

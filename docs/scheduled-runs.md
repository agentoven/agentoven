# Scheduled runs (Pro)

A schedule starts a recipe on a cron expression, as a **service account that belongs to a person**. The run passes everything a call from that account passes: the account's role, its owner's membership in the kitchen, the environment's approval rule (and the GxP rule), guardrails, budgets and the audit trail. A recipe that reaches a human gate waits there for a person; a schedule cannot take a decision.

Create one in the dashboard (Recipes, Schedules) or with `POST /api/v1/schedules`:

```json
{"recipe_name": "nightly-report", "cron_expr": "0 2 * * *", "service_account_id": "<id>",
 "environment": "staging", "input": {"batch": 7}}
```

- Needs `kitchen:admin` (Head Chef). You must hold at least the role of the account you name; the account must belong to a person and be this kitchen's.
- Times are the server's clock, five fields, minute resolution. A schedule that was due while the server was down runs once, not once per missed slot. With several API replicas exactly one starts each slot.
- Every schedule shows its **last attempt**: `started` with the run id, or `failed` with the control plane's own reason (`permission_denied: environment "prod" requires approval`, `owner_unbound`, a recipe that does not exist) and how many attempts in a row failed. A failed attempt is not retried until the next cron time. A schedule made before accounts existed has none, starts nothing and says so until you give it one.
- `POST /api/v1/schedules/{id}/trigger` ("Run now") runs it as the account and answers with the reason when nothing started.
- Audit: `schedule.run_started` / `schedule.run_failed` on the kitchen's chain, with the run id; the run's own start is recorded as the service account.
- Airflow: `GET /api/v1/schedules/dags/{id}/export/dag` exports a DAG that calls `POST /api/v1/recipes/{r}/bake`; give the Airflow connection `agentoven_api` the key of a service account that belongs to a person. The standalone `cmd/scheduler` dispatches to Airflow or an Azure Function only.

`AGENTOVEN_SCHEDULER_TICK` (default `1m`) sets how often the server looks for due schedules; `DISABLE_EMBEDDED_SCHEDULER=true` turns the server's scheduler off.

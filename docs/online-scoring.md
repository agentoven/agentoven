# Online scoring: judges on live traces, experiments, alert rules (Pro)

Three things that tell you whether an agent is getting better or worse, and stop a bad release from going further. They are in AgentOven Pro; the community edition has the trace scores they write (`POST /api/v1/traces/{id}/scores`) and nothing that scores by itself.

## Online judges

A judge is a model that grades a sample of your live traces against a rubric you write, and puts its score on the trace (source `eval`, shown in the trace view as a Judge score). Define one under **Observe, Quality, Online judges**, or `POST /api/v1/online-judges`:

| Field | |
|---|---|
| `rubric`, `prompt` | the criterion, and extra guidance for the grader |
| `scale` | `binary` (0 or 1) or `graded` (0 to 1) |
| `provider`, `model` | **one of this kitchen's own providers** and a model it lists. A judge has no endpoint or key of its own: a body that names one is refused |
| `artifact` | `output` (the reply) or `input_output` (the prompt and the reply) |
| `sample_rate` | the share of matching traces to judge (0 to 1) |
| `filter` | agent, environment, tags |
| `max_cost_usd_per_day`, `max_judgments_per_hour` | both required. At a cap the judge stops sampling and the skipped traces are counted |

What to know before you switch one on:

- **Switching a judge on is a signed decision.** A Chef defines it; a person signs to switch it on, shown the rubric, what it reads, which provider and model get the text, the rate and the limits. It also needs the right to read trace content, because the judge reads what the traces hold. An API key cannot switch one on. Switching off needs no signature. **Changing a judge that is on switches it off** until it is signed again.
- It runs **after** the trace is written, off the request path: a slow or failed provider never slows or fails your users. Its calls go through the same router as everything else, so your guardrails, rate limits and the kitchen's budget apply, and they are counted against that budget.
- **Every read of a trace's content is an audit event** with the trace id, the judge, the provider and the signature that switched it on.
- The score is on the trace for everyone who can see the trace. The judge's **rationale** is not: it is shown only to roles that can read trace content (`trace:read:content`), the same as the prompt and the reply.
- The judge never grades the platform's own traces (judge calls, experiments, replays), and a trace of another kitchen is never read.

The agent's **Scores** tab shows each score by day and the lowest-scored traces.

## Experiments

An experiment runs two baked versions of agents on the same cases and says how sure it is. Cases come from a **published dataset version** or from **sampled recent traces** of an agent. Both versions answer each case with their tools blocked; a judge (one that is switched on) or cost or latency is compared per case.

The result is an interval over cases and a verdict in words: `improved`, `regressed`, `equivalent`, `non_inferior`, or `inconclusive`. Fewer than 20 paired cases (you may lower it to 5) is inconclusive, and so is a comparison where one version failed far more often. An experiment needs a cost cap and stops when it reaches it; one that is stopped keeps what it has and says it is partial.

**A Head Chef accepts or rejects the result, by signature.** Only a result that supports the candidate can be accepted; a regression, an inconclusive or a partial result can only be rejected.

## Alert rules and promotion holds

A rule watches one number over a window (a score's mean, error rate, cost, p95 latency) and fires when it crosses a threshold with at least a minimum number of traces. It tells the notification channels you name (it cannot name an address; the channels' own network allowlist applies) with numbers and no content, and it resolves when the number is back a tenth past the threshold.

A rule can also **hold the promotion** of an agent. While a hold stands, promoting that agent (or a dry run of it) is refused with `blocked_hold`; rolling back is not. The alert resolving does not lift the hold. **A Head Chef lifts it by a signed decision**, with a reason.

## Who may do what

| | Roles |
|---|---|
| See judges, scores, coverage and cost | Chef, Baker, Taster, Auditor, Finance, Head Chef |
| Define, change, switch off a judge; edit rules | Chef, Head Chef |
| Run an experiment | Chef, Baker, Head Chef |
| Accept an experiment; release a hold | Head Chef |
| See rules, alerts and holds | everyone but Legal |

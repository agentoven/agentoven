# Budgets, rate limits and the response cache (Pro)

Three controls decide how much, how fast and how often a kitchen's agents call models. They sit on the one
path every model call takes (the model router), so they apply to agent runs, background tasks, summaries and
the OpenAI-compatible endpoint alike. All three are **Pro**; the community edition has the per-run budget
described in [Long-running agents](long-running-agents.md) and nothing shared beyond a run.

| Control | Question it answers | Over the line |
|---|---|---|
| Period budget | How much may this kitchen, agent, provider or API key spend this day, week or month? | 402 `budget_exceeded` |
| Rate limit | How many requests, tokens or invocations per minute? | 429 `rate_limited` with `Retry-After` |
| Response cache | Has this exact request been answered already? | (a hit costs nothing and uses neither of the above) |

## Period budgets

A budget caps what is spent in a period, in **dollars** (`usd`) or **tokens** (`tokens`), and counts one of:

| Scope | Counts |
|---|---|
| kitchen | every model call in the kitchen |
| `provider` | calls to one provider |
| `agent` | calls made for one agent |
| `api_key` | calls made with one credential (its subject, for example `scopedkey:<id>`) |

A budget is one scope in one unit, so an agent can have a dollar budget and a token budget at once; a call
must fit in every budget that applies to it. Periods are `daily`, `weekly` (from Monday), `monthly` and
`lifetime`, in UTC; the first call after a boundary starts the new period.

```bash
agentoven budgets set 500 --period monthly                                  # the kitchen, $500 a month
agentoven budgets set 2000000 --unit tokens --agent triage --period daily   # one agent, tokens a day
agentoven budgets set 50 --provider openai
agentoven budgets set 20 --key scopedkey:7f3a --period weekly              # one API key
agentoven budgets list
```

```python
client.set_budget(500, period="monthly")
client.set_budget(2_000_000, unit="tokens", agent="triage", period="daily")
client.list_budgets()      # scope, unit, amount, consumed, reserved, remaining, used_pct, exhausted, reset_at
```

`GET|PUT|DELETE /api/v1/spend/budgets`. The same names in the SDKs: `list_budgets`, `set_budget`,
`remove_budget`, `list_budget_ledger`, `list_budget_alerts`, `budget_usage`.

### Thresholds and the hard stop

A budget has thresholds, each a percentage and an action. The default warns at **50%** and **80%** and
stops at **100%**; set your own with `--alert 70 --block 95` (the lowest `block` is where calls stop; a
budget with only alerts never stops anything).

The stop is exact, not eventual: a call is **admitted only if its worst-case cost fits** in what is left
(its prompt plus the output it may produce), decided in one atomic update, so many parallel callers and many
replicas cannot spend a budget twice. A call whose worst case does not fit is refused even if its real cost
would have; give calls a `max_tokens` to size them tightly.

A call that is refused was never sent. Through the OpenAI-compatible endpoint (`/v1/chat/completions`) it
is a **402** in OpenAI's error shape, with the budget named:

```json
{ "error": { "message": "the monthly budget for agent triage is used up; it resets 2026-12-01T00:00:00Z",
             "type": "budget_exceeded", "code": "budget_exceeded",
             "scope": "agent", "unit": "tokens", "period": "monthly", "resets_at": "2026-12-01T00:00:00Z" } }
```

402 is a final error to OpenAI clients (they do not retry it, unlike 429). The SDKs raise it as
`BudgetExceededError` (`scope`, `unit`, `period`, `resets_at`).

### Runs and tasks pause; a person decides

An agent run or background task that meets a budget shared beyond itself (the kitchen's, an agent's, a
provider's) **pauses instead of failing**, like a run at its own cap: it checkpoints, a `task.paused` event
and webhook go out, and `invoke` answers 402 `run_budget_exhausted` with the `scope` of the budget that ran
out. A person then does one of two things:

1. **Raises the budget**, which is a signed decision (below), or waits for the period to reset.
2. **Resumes the run** (`POST /api/v1/agents/{agent}/runs/{id}/resume` or `agentoven task resume`, also
   signed in Pro). Resuming a run that paused at a shared budget needs no extension; it simply tries the call
   again, and pauses again if the budget is still used up.

### Raising a budget is a signed decision

Creating a budget, or lowering one, only protects and needs no signature. **Raising** one (a higher amount, a
different period, which starts the count again, or a stop that moves up or goes away) and **removing** one
are signed decisions at the `budget_raise` gate (electronic signatures, ADR-0041): record id
`<scope>:<name>:<unit>` such as `agent:triage:tokens`, `kitchen::usd`. The person signs again with their own
password; an API key or service account cannot sign and is refused with `signature_required`.

```bash
agentoven budgets set 800 --period monthly --sign       # a raise
agentoven budgets rm --agent triage --unit tokens --sign
```

```python
client.set_budget(800, period="monthly", password=pw)   # or signing_token=...
```

Whether a change is a raise is decided in the same statement that writes it, so two requests racing cannot
turn a tightening into a raise.

### Alerts

When a budget crosses a threshold the event is recorded once per budget per period (a unique key, so many
callers and replicas cannot fire it twice) and sent to:

- the kitchen's **notification channels** (Slack, Teams, Discord, e-mail, webhook, Zapier): a channel
  receives budget events when its `events` list is empty (it hears everything) or includes
  `budget.threshold_crossed`, `budget.exhausted` or `budget.call_refused`;
- the signed global webhook, when one is configured.

`budget.call_refused` repeats for as long as calls are refused, so it is reported at most once a minute per
budget, with the number of refusals it stands for. Notifications are **throttled per kitchen** (a burst of
ten, then one every thirty seconds), so a kitchen with many budgets that cross together does not bury the
people it is telling. A throttled alert is still audited and listed: `agentoven budgets alerts` shows
`notified: no` for it.

### The Spend page

The dashboard's **Spend** page shows each budget's usage against its amount, the thresholds crossed, and the
top agents, models and providers over the last 30 days with the response cache's hit rate and what it saved.
`GET /api/v1/spend/usage?group=agent|model|provider|day` is the same data; it counts every model call,
whether or not a budget covers it, and is written in batches every few seconds.

## Rate limits

A limit counts, per minute:

- `requests_per_minute`: model requests (calls to a provider);
- `tokens_per_minute`: the tokens they use;
- `invocations_per_minute`: agent invocations at the API's door (`invoke`, `invoke/stream`, session messages
  and starting a task), before any work starts. Not available for a provider.

Each limit has a scope (`kitchen`, `agent`, `api_key` or `provider`) and a `ref` naming it; the ref `*` gives
every agent, key or provider **its own bucket** with that rate. A rate of 0 does not limit that dimension.

```bash
agentoven limits set kitchen --tpm 400000
agentoven limits set agent triage --rpm 60 --ipm 20
agentoven limits set provider openai --rpm 500
agentoven limits set api_key '*' --rpm 30           # every key gets 30 a minute
agentoven limits list                                # with how many requests each has refused
```

A limit is a **token bucket** with a capacity of one minute of its rate: it admits a burst up to the limit,
then the rate refills it continuously. A call takes from every limit that applies to it, or from none. A
token limit holds the call's expected tokens (its prompt plus its output cap) up front and gives back what the
call did not use when it finishes. A call larger than a whole minute's tokens is admitted when the bucket is
full, or it could never run.

Over a limit the answer is **429** with `Retry-After` (whole seconds, at least one):

```json
{ "error": "rate_limited", "scope": "agent", "limit": "requests_per_minute", "retry_after": 2,
  "message": "agent triage is over its requests-per-minute limit" }
```

On the OpenAI-compatible endpoint it is OpenAI's shape (`type` `rate_limit_error`, `code`
`rate_limit_exceeded`), which clients retry after waiting. The SDKs raise `RateLimitedError`
(`retry_after`, `scope`, `limit`); they do not retry for you. A budgeted run or task that meets a rate limit
inside the model loop waits at least `Retry-After` (at most a minute) and tries again; an interactive call
returns the 429.

A rate limit says nothing about the provider's health: the router does not count it as a provider failure.
A limit on one provider makes the router fall back to the next; a limit on the kitchen, an agent or a key
stops the call, because every other provider would be refused for the same reason.

### Where the buckets live

| Backend | Setting | Exact across replicas | Cost |
|---|---|---|---|
| memory (default) | `AGENTOVEN_RATELIMIT_BACKEND=memory` | no: **each replica counts alone**, so N replicas admit up to N times the rate | none |
| postgres | `AGENTOVEN_RATELIMIT_BACKEND=postgres` | yes: one count for all replicas | one write per request and limit to a hot row |

The postgres backend takes from all the buckets a call needs in one transaction and times refills with the
database's clock. If the database cannot be reached it **counts in memory instead of refusing traffic**: a
rate limit protects a provider from a runaway caller, and refusing everything because the counter is down
would be the larger harm. (A budget, which is money, fails the other way: if the ledger is unreachable, the
call does not go out.) `AGENTOVEN_RATELIMIT=off` disables rate limits.

## The response cache

An agent that is asked the same thing again need not pay for the same answer. The cache is **exact-match**:
a request is answered from it only if its kitchen, agent, provider, model, sampling parameters, tools and every
message are byte-for-byte what was cached, with each image or file by the hash of its bytes. It is **off**
until a kitchen turns it on.

```bash
agentoven cache set deterministic                   # the kitchen: requests that ask for temperature 0
agentoven cache set always --agent faq-bot --ttl 900   # one agent; any eligible request, kept 15 minutes
agentoven cache show                                # policies, entries, hits, misses, why requests were not cached
agentoven cache purge --agent faq-bot              # forget it, on every replica
```

| Mode | Caches |
|---|---|
| `off` | nothing (the default) |
| `deterministic` | requests that state `temperature: 0`. Agent runs send no temperature, so for an agent use `always` |
| `always` | any eligible request; whoever sets it accepts that a sampled answer is repeated |

An agent's policy replaces the kitchen's for that agent. `ttl_seconds` defaults to an hour and is at most a
week.

**Never cached**, whatever the mode:

- a request or answer containing something that looks like a credential (API keys, tokens, private keys,
  bearer or JWT tokens) — a pattern check, so it finds the common formats and not a secret with no shape;
- an answer that carries **tool calls** (replaying it would make the agent run the tool, and its side effect,
  again), that was cut off (`finish_reason` other than `stop`), or that is empty;
- a request with an image or file behind a web address, which can change under the same address (inline data
  and stored blobs are keyed by their bytes and are cached);
- a streamed call, and an answer over 256 KiB.

**Whose answer it is.** The cache key includes who is asking. What one person's request produced is never
handed to another: the entry is kept for the caller whose credential made the call, and a different caller
misses. `share_across_principals` lets callers share entries; turn it on only for content that is the same for
everyone. Because the key covers every message in full, retrieved passages included, a request whose retrieved
content differs per reader differs in key too.

A **hit** is marked in the trace (span metadata `cache_hit`, trace metadata `cache_hit`) and costs nothing: its
usage is zero, so it is charged to no budget, takes nothing from a rate limit, and adds nothing to the cost
summary. The gateway answers with the header `X-AgentOven-Cache: hit`. `agentoven budgets usage` shows the
hit rate and the dollars saved.

Entries live in the memory of the server process, bounded per kitchen (a quarter of the cache) and in total
(64 MiB, `AGENTOVEN_RESPONSE_CACHE_MAX_MB`), least recently used first. Each replica has its own entries, so
each warms its own; a **purge** or a policy change is recorded with the database's time, and every replica
drops what was stored before it within seconds. Replicas' clocks are assumed to agree to within a few seconds.
`AGENTOVEN_RESPONSE_CACHE=off` disables it.

### Why not a semantic cache

There is no "similar request" cache, on purpose. A near match returns the answer to a different question, and
the platform cannot tell when that is wrong. It cannot be made safe under per-caller access control (a
neighbour found by similarity may have been produced for someone who could see more), the signed and audited
decisions an answer may sit under cannot be replayed from a different request, and a hit that is nearly right
is worse than a miss that is exactly right. An exact match is the strongest guarantee that the answer is the
one the provider gave to this request.

## Permissions and audit

| Permission | Allows | Default roles (a Restaurateur holds every permission) |
|---|---|---|
| `cost:read` | read budgets, the ledger, alerts, usage, limits, the cache's policy and numbers | head chef, chef, auditor, finance |
| `budget:extend` | set, raise and remove budgets (raising and removing are signed) | head chef, chef, finance |
| `limit:manage` | set and remove rate limits | head chef, chef |
| `cache:manage` | set and remove cache policies, purge | head chef, chef |

Every route is scoped to the request's kitchen; a body cannot name another. Changes are audit events:
`budget.updated` (with the previous amount and the signature id of a raise), `budget.deleted`,
`budget.threshold_crossed`, `budget.exhausted`, `budget.call_refused`, `limit.updated`, `limit.deleted`,
`cache.policy_updated`, `cache.policy_deleted` and `cache.purged`. A request refused by a rate limit is not
audited one by one (a caller over its limit repeats the request); the limit's `rejected` count is the record.

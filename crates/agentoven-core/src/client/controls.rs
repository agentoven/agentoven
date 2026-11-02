//! Budgets, rate limits and the response cache (Pro, ADR-0051).
//!
//! A budget caps what a kitchen, or one provider, agent or API key in it, may spend in a period, in
//! dollars or tokens. A rate limit caps requests, tokens or invocations per minute. The response cache
//! answers a repeated request without calling the provider. Raising a budget, or removing one, is a
//! signed decision on the `budget_raise` gate (ADR-0041): give a [`Signature`]. A refusal is an
//! [`ApiError`](super::pro::ApiError); a rate limit carries `retry_after` and a budget refusal says whose
//! budget it was. Nothing here retries for the caller.

use reqwest::Method;
use serde_json::{json, Map, Value};

use super::pro::{with_query, Signature};
use super::AgentOvenClient;

/// What a budget is measured in.
pub const BUDGET_UNITS: [&str; 2] = ["usd", "tokens"];
/// The periods a budget resets on.
pub const BUDGET_PERIODS: [&str; 4] = ["daily", "weekly", "monthly", "lifetime"];
/// What a rate limit can count.
pub const LIMIT_SCOPES: [&str; 4] = ["kitchen", "agent", "api_key", "provider"];
/// The modes of a response-cache policy.
pub const CACHE_MODES: [&str; 3] = ["off", "deterministic", "always"];
/// How the usage report can be grouped.
pub const USAGE_GROUPS: [&str; 4] = ["agent", "model", "provider", "day"];

/// Which budget: the kitchen's (none of the three), or one provider's, agent's or credential's, in
/// dollars (`unit` `None` or `usd`) or tokens.
#[derive(Debug, Clone, Copy, Default)]
pub struct BudgetScope<'a> {
    pub provider: Option<&'a str>,
    pub agent: Option<&'a str>,
    /// The credential's subject, for example `scopedkey:<id>`.
    pub api_key: Option<&'a str>,
    pub unit: Option<&'a str>,
}

impl BudgetScope<'_> {
    fn unit(&self) -> &str {
        self.unit.unwrap_or("usd")
    }

    /// The fields that say which budget, checked: at most one of provider, agent and api_key, and a unit
    /// the control plane has.
    fn fields(&self) -> anyhow::Result<Map<String, Value>> {
        let named: Vec<(&str, &str)> = [
            ("provider", self.provider),
            ("agent", self.agent),
            ("api_key", self.api_key),
        ]
        .into_iter()
        .filter_map(|(k, v)| v.filter(|v| !v.is_empty()).map(|v| (k, v)))
        .collect();
        if named.len() > 1 {
            anyhow::bail!(
                "a budget counts the kitchen or one provider, one agent or one API key, not a mix"
            );
        }
        if !BUDGET_UNITS.contains(&self.unit()) {
            anyhow::bail!(
                "unit is one of {}, not '{}'",
                BUDGET_UNITS.join(", "),
                self.unit()
            );
        }
        let mut m = Map::new();
        for (k, v) in named {
            m.insert(k.into(), json!(v));
        }
        m.insert("unit".into(), json!(self.unit()));
        Ok(m)
    }
}

/// The record id a signature on a budget change covers: `<scope>:<name>:<unit>`, for example
/// `provider:openai:usd`, `agent:triage:tokens` or `kitchen::usd` (the name is empty for the kitchen).
pub fn budget_record_id(scope: &BudgetScope<'_>) -> anyhow::Result<String> {
    let f = scope.fields()?;
    let unit = scope.unit();
    for kind in ["provider", "agent", "api_key"] {
        if let Some(name) = f.get(kind).and_then(Value::as_str) {
            return Ok(format!("{kind}:{name}:{unit}"));
        }
    }
    Ok(format!("kitchen::{unit}"))
}

impl AgentOvenClient {
    // ── Budgets ──────────────────────────────────────────────

    /// The kitchen's period budgets with what is left (`scope`, `unit`, `amount`, `period`, `consumed`,
    /// `reserved`, `remaining`, `used_pct`, `exhausted`, `reset_at`, `thresholds`). Needs `cost:read`.
    pub async fn list_budgets(&self) -> anyhow::Result<Vec<Value>> {
        self.list_of("/api/v1/spend/budgets", "budgets").await
    }

    /// Create or change a period budget (`budget:extend`). `period` is one of [`BUDGET_PERIODS`] (monthly
    /// when `None`); `thresholds` are `(pct, action)` pairs with the action `alert` or `block` (the default
    /// warns at 50 and 80 percent and stops at 100).
    ///
    /// Creating a budget or lowering one is not a decision. Raising one (a higher amount, a different
    /// period, a stop that moves up or goes away) is a signed decision on the `budget_raise` gate, record
    /// id [`budget_record_id`], payload this request's body: give a [`Signature`].
    pub async fn set_budget(
        &self,
        scope: &BudgetScope<'_>,
        amount: f64,
        period: Option<&str>,
        thresholds: Option<&[(u32, &str)]>,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let mut body = scope.fields()?;
        body.insert("amount".into(), json!(amount));
        if let Some(p) = period {
            if !BUDGET_PERIODS.contains(&p) {
                anyhow::bail!("period is one of {}, not '{p}'", BUDGET_PERIODS.join(", "));
            }
            body.insert("period".into(), json!(p));
        }
        if let Some(ts) = thresholds {
            let mut list = Vec::new();
            for (pct, action) in ts {
                if *action != "alert" && *action != "block" {
                    anyhow::bail!("a threshold's action is 'alert' or 'block', not '{action}'");
                }
                list.push(json!({"pct": pct, "action": action}));
            }
            body.insert("thresholds".into(), Value::Array(list));
        }
        let body = Value::Object(body);
        let token = self
            .signing_token_for(sig, "budget_raise", &budget_record_id(scope)?, Some(&body))
            .await?;
        self.call_json(
            Method::PUT,
            "/api/v1/spend/budgets",
            Some(&body),
            token.as_deref(),
        )
        .await
    }

    /// Remove a budget (`budget:extend`). Removing lifts its limit, so it is signed like a raise: the
    /// `budget_raise` gate, payload `{"delete": true}`. Its ledger entries stay.
    pub async fn remove_budget(
        &self,
        scope: &BudgetScope<'_>,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let f = scope.fields()?;
        let q = |k: &str| f.get(k).and_then(Value::as_str).map(str::to_string);
        let path = with_query(
            "/api/v1/spend/budgets",
            &[
                ("provider", q("provider")),
                ("agent", q("agent")),
                ("api_key", q("api_key")),
                ("unit", q("unit")),
            ],
        );
        let token = self
            .signing_token_for(
                sig,
                "budget_raise",
                &budget_record_id(scope)?,
                Some(&json!({"delete": true})),
            )
            .await?;
        self.call_json(Method::DELETE, &path, None, token.as_deref())
            .await
    }

    /// The ledger, newest first: one entry per model call per budget it was charged to. `since` is an
    /// RFC 3339 time, `before` an entry id to page from.
    pub async fn list_budget_ledger(
        &self,
        budget_id: Option<&str>,
        since: Option<&str>,
        before: Option<u64>,
        limit: Option<u32>,
    ) -> anyhow::Result<Vec<Value>> {
        let path = with_query(
            "/api/v1/spend/ledger",
            &[
                ("budget_id", budget_id.map(Into::into)),
                ("since", since.map(Into::into)),
                ("before", before.map(|b| b.to_string())),
                ("limit", limit.map(|l| l.to_string())),
            ],
        );
        self.list_of(&path, "entries").await
    }

    /// The thresholds the kitchen's budgets crossed, newest first. `notified` is false for an alert the
    /// notification channels were not told about (the kitchen was throttled).
    pub async fn list_budget_alerts(&self, limit: Option<u32>) -> anyhow::Result<Vec<Value>> {
        let path = with_query(
            "/api/v1/spend/alerts",
            &[("limit", limit.map(|l| l.to_string()))],
        );
        self.list_of(&path, "alerts").await
    }

    /// What the kitchen's calls amounted to over the last `days` (30 when `None`), grouped by one of
    /// [`USAGE_GROUPS`], biggest spender first; includes the response cache's hits, misses and savings.
    pub async fn budget_usage(
        &self,
        group: &str,
        days: Option<u32>,
        limit: Option<u32>,
    ) -> anyhow::Result<Vec<Value>> {
        if !USAGE_GROUPS.contains(&group) {
            anyhow::bail!("group is one of {}, not '{group}'", USAGE_GROUPS.join(", "));
        }
        let path = with_query(
            "/api/v1/spend/usage",
            &[
                ("group", Some(group.into())),
                ("days", days.map(|d| d.to_string())),
                ("limit", limit.map(|l| l.to_string())),
            ],
        );
        self.list_of(&path, "usage").await
    }

    // ── Rate limits ──────────────────────────────────────────

    /// The kitchen's rate limits with how many requests each has refused since the server started.
    pub async fn list_limits(&self) -> anyhow::Result<Vec<Value>> {
        self.list_of("/api/v1/limits", "limits").await
    }

    /// Create or replace a rate limit (`limit:manage`). `scope` is one of [`LIMIT_SCOPES`]; `reference`
    /// names the agent, credential subject or provider (`*` gives each its own bucket) and is empty for
    /// the kitchen. A rate of 0 does not limit that dimension.
    pub async fn set_limit(
        &self,
        scope: &str,
        reference: &str,
        requests_per_minute: u64,
        tokens_per_minute: u64,
        invocations_per_minute: u64,
    ) -> anyhow::Result<Value> {
        if !LIMIT_SCOPES.contains(&scope) {
            anyhow::bail!("scope is one of {}, not '{scope}'", LIMIT_SCOPES.join(", "));
        }
        if requests_per_minute == 0 && tokens_per_minute == 0 && invocations_per_minute == 0 {
            anyhow::bail!("a limit sets at least one of requests, tokens and invocations per minute (remove the limit to lift it)");
        }
        let body = json!({
            "scope": scope, "ref": reference, "requests_per_minute": requests_per_minute,
            "tokens_per_minute": tokens_per_minute, "invocations_per_minute": invocations_per_minute,
        });
        self.call_json(Method::PUT, "/api/v1/limits", Some(&body), None)
            .await
    }

    /// Remove a rate limit (`limit:manage`).
    pub async fn remove_limit(&self, scope: &str, reference: &str) -> anyhow::Result<Value> {
        let path = with_query(
            "/api/v1/limits",
            &[
                ("scope", Some(scope.into())),
                ("ref", Some(reference.into())),
            ],
        );
        self.call_json(Method::DELETE, &path, None, None).await
    }

    // ── The response cache ───────────────────────────────────

    /// The kitchen's cache policies and what the server process holds (`{"policies", "stats"}`).
    pub async fn get_cache(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/cache", None, None)
            .await
    }

    /// Set the cache policy of the kitchen (`agent` empty) or one agent (`cache:manage`). `mode` is one
    /// of [`CACHE_MODES`]. Entries are kept per caller unless `share_across_principals`. Setting a policy
    /// forgets what was stored under the old one.
    pub async fn set_cache_policy(
        &self,
        mode: &str,
        agent: &str,
        ttl_seconds: Option<u64>,
        share_across_principals: bool,
    ) -> anyhow::Result<Value> {
        if !CACHE_MODES.contains(&mode) {
            anyhow::bail!("mode is one of {}, not '{mode}'", CACHE_MODES.join(", "));
        }
        let mut body = json!({"agent": agent, "mode": mode, "share_across_principals": share_across_principals});
        if let Some(t) = ttl_seconds {
            body["ttl_seconds"] = json!(t);
        }
        self.call_json(Method::PUT, "/api/v1/cache/policy", Some(&body), None)
            .await
    }

    /// Remove a cache policy (`cache:manage`).
    pub async fn remove_cache_policy(&self, agent: &str) -> anyhow::Result<Value> {
        let path = with_query("/api/v1/cache/policy", &[("agent", Some(agent.into()))]);
        self.call_json(Method::DELETE, &path, None, None).await
    }

    /// Forget everything stored for the kitchen, or for one agent, on every replica (`cache:manage`).
    pub async fn purge_cache(&self, agent: &str) -> anyhow::Result<Value> {
        let body = if agent.is_empty() {
            json!({})
        } else {
            json!({"agent": agent})
        };
        self.call_json(Method::POST, "/api/v1/cache/purge", Some(&body), None)
            .await
    }

    /// GET `path` and return the array under `key` of the reply.
    async fn list_of(&self, path: &str, key: &str) -> anyhow::Result<Vec<Value>> {
        let v = self.call_json(Method::GET, path, None, None).await?;
        Ok(v[key].as_array().cloned().unwrap_or_default())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::client::pro::ApiError;
    use crate::testing::{fake_control_plane, json as reply, Reply, Routes};

    const DIGEST: &str = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd";

    fn routes(extra: &[(&str, Reply)]) -> Routes {
        let mut r = Routes::from([
            (
                "POST /api/v1/signatures/challenge".to_string(),
                reply(
                    200,
                    json!({"subject": {"digest": DIGEST}, "reauth": "password"}),
                ),
            ),
            (
                "POST /api/v1/signatures/token".to_string(),
                reply(200, json!({"signing_token": "tok.sig"})),
            ),
        ]);
        for (k, v) in extra {
            r.insert(k.to_string(), v.clone());
        }
        r
    }

    async fn client(extra: &[(&str, Reply)]) -> (AgentOvenClient, crate::testing::Log) {
        let (base, log) = fake_control_plane(routes(extra)).await;
        (
            AgentOvenClient::new(&base)
                .unwrap()
                .with_api_key("ao_key")
                .with_kitchen("acme"),
            log,
        )
    }

    #[test]
    fn record_ids_name_the_scope_who_and_the_unit() {
        let id = |s: BudgetScope| budget_record_id(&s).unwrap();
        assert_eq!(id(BudgetScope::default()), "kitchen::usd");
        assert_eq!(
            id(BudgetScope {
                unit: Some("tokens"),
                ..Default::default()
            }),
            "kitchen::tokens"
        );
        assert_eq!(
            id(BudgetScope {
                provider: Some("openai"),
                ..Default::default()
            }),
            "provider:openai:usd"
        );
        assert_eq!(
            id(BudgetScope {
                agent: Some("triage"),
                unit: Some("tokens"),
                ..Default::default()
            }),
            "agent:triage:tokens"
        );
        assert_eq!(
            id(BudgetScope {
                api_key: Some("scopedkey:abc"),
                ..Default::default()
            }),
            "api_key:scopedkey:abc:usd"
        );
        assert!(budget_record_id(&BudgetScope {
            agent: Some("a"),
            provider: Some("p"),
            ..Default::default()
        })
        .is_err());
        assert!(budget_record_id(&BudgetScope {
            unit: Some("euros"),
            ..Default::default()
        })
        .is_err());
        assert!(crate::client::pro::SIGNING_GATES.contains(&"budget_raise"));
    }

    #[tokio::test]
    async fn list_budgets_unwraps_the_list() {
        let (c, _) = client(&[(
            "GET /api/v1/spend/budgets",
            reply(
                200,
                json!({"budgets": [{"scope": "agent", "unit": "tokens"}], "count": 1}),
            ),
        )])
        .await;
        assert_eq!(c.list_budgets().await.unwrap()[0]["unit"], "tokens");
    }

    #[tokio::test]
    async fn set_budget_sends_scope_unit_amount_period_and_thresholds_unsigned() {
        let (c, log) = client(&[("PUT /api/v1/spend/budgets", reply(200, json!({})))]).await;
        let scope = BudgetScope {
            agent: Some("triage"),
            unit: Some("tokens"),
            ..Default::default()
        };
        c.set_budget(
            &scope,
            5000.0,
            Some("daily"),
            Some(&[(50, "alert"), (100, "block")]),
            Signature::None,
        )
        .await
        .unwrap();
        let seen = log.last();
        assert_eq!(
            seen.body,
            json!({"agent": "triage", "unit": "tokens", "amount": 5000.0, "period": "daily",
                   "thresholds": [{"pct": 50, "action": "alert"}, {"pct": 100, "action": "block"}]})
        );
        assert!(!seen.headers.contains_key("x-signing-token"));
        assert_eq!(
            log.len(),
            1,
            "no signing for a change that is not a decision"
        );
    }

    #[tokio::test]
    async fn a_raise_is_signed_for_exactly_this_change() {
        let (c, log) = client(&[("PUT /api/v1/spend/budgets", reply(200, json!({})))]).await;
        let scope = BudgetScope {
            provider: Some("openai"),
            ..Default::default()
        };
        c.set_budget(&scope, 200.0, None, None, Signature::Password("pw"))
            .await
            .unwrap();
        let all = log.all();
        assert_eq!(all[0].body["gate"], "budget_raise");
        assert_eq!(all[0].body["record_id"], "provider:openai:usd");
        assert_eq!(
            all[0].body["payload"],
            json!({"provider": "openai", "unit": "usd", "amount": 200.0})
        );
        assert_eq!(all[2].headers["x-signing-token"], "tok.sig");
        assert!(all[2].body.get("password").is_none());
        // a token you hold is sent as it is
        c.set_budget(&scope, 300.0, None, None, Signature::Token("held.tok"))
            .await
            .unwrap();
        assert_eq!(log.last().headers["x-signing-token"], "held.tok");
    }

    #[tokio::test]
    async fn set_budget_checks_what_it_can_before_sending() {
        let (c, log) = client(&[]).await;
        let none = BudgetScope::default();
        assert!(c
            .set_budget(&none, 1.0, Some("yearly"), None, Signature::None)
            .await
            .is_err());
        assert!(c
            .set_budget(&none, 1.0, None, Some(&[(50, "explode")]), Signature::None)
            .await
            .is_err());
        let mix = BudgetScope {
            agent: Some("a"),
            provider: Some("p"),
            ..Default::default()
        };
        assert!(c
            .set_budget(&mix, 1.0, None, None, Signature::None)
            .await
            .is_err());
        assert!(log.is_empty());
    }

    #[tokio::test]
    async fn remove_budget_is_signed_with_the_delete_payload() {
        let (c, log) = client(&[(
            "DELETE /api/v1/spend/budgets?agent=triage&unit=tokens",
            reply(200, json!({"deleted": true})),
        )])
        .await;
        let scope = BudgetScope {
            agent: Some("triage"),
            unit: Some("tokens"),
            ..Default::default()
        };
        c.remove_budget(&scope, Signature::Password("pw"))
            .await
            .unwrap();
        let all = log.all();
        assert_eq!(all[0].body["record_id"], "agent:triage:tokens");
        assert_eq!(all[0].body["payload"], json!({"delete": true}));
        assert_eq!(all[2].headers["x-signing-token"], "tok.sig");
    }

    #[tokio::test]
    async fn ledger_alerts_and_usage() {
        let (c, _) = client(&[
            (
                "GET /api/v1/spend/ledger?budget_id=b1&since=2026-11-01T00%3A00%3A00Z&limit=5",
                reply(200, json!({"entries": [{"id": 3}]})),
            ),
            (
                "GET /api/v1/spend/alerts?limit=10",
                reply(200, json!({"alerts": [{"notified": false}]})),
            ),
            (
                "GET /api/v1/spend/usage?group=model&days=7&limit=3",
                reply(200, json!({"usage": [{"group": "gpt", "cache_hits": 2}]})),
            ),
        ])
        .await;
        assert_eq!(
            c.list_budget_ledger(Some("b1"), Some("2026-11-01T00:00:00Z"), None, Some(5))
                .await
                .unwrap()[0]["id"],
            3
        );
        assert_eq!(
            c.list_budget_alerts(Some(10)).await.unwrap()[0]["notified"],
            false
        );
        assert_eq!(
            c.budget_usage("model", Some(7), Some(3)).await.unwrap()[0]["cache_hits"],
            2
        );
        assert!(c.budget_usage("password", None, None).await.is_err());
    }

    #[tokio::test]
    async fn limits_round_trip() {
        let (c, log) = client(&[
            (
                "GET /api/v1/limits",
                reply(200, json!({"limits": [{"scope": "agent", "rejected": 4}]})),
            ),
            ("PUT /api/v1/limits", reply(200, json!({}))),
            (
                "DELETE /api/v1/limits?scope=agent&ref=triage",
                reply(200, json!({"deleted": true})),
            ),
            (
                "DELETE /api/v1/limits?scope=kitchen&ref=",
                reply(200, json!({"deleted": true})),
            ),
        ])
        .await;
        assert_eq!(c.list_limits().await.unwrap()[0]["rejected"], 4);
        c.set_limit("agent", "triage", 30, 20000, 0).await.unwrap();
        assert_eq!(
            log.last().body,
            json!({"scope": "agent", "ref": "triage", "requests_per_minute": 30, "tokens_per_minute": 20000, "invocations_per_minute": 0})
        );
        c.remove_limit("agent", "triage").await.unwrap();
        assert!(c.set_limit("planet", "x", 1, 0, 0).await.is_err());
        assert!(c.set_limit("agent", "x", 0, 0, 0).await.is_err());
    }

    #[tokio::test]
    async fn cache_calls() {
        let (c, log) = client(&[
            (
                "GET /api/v1/cache",
                reply(200, json!({"policies": [], "stats": {"hits": 0}})),
            ),
            ("PUT /api/v1/cache/policy", reply(200, json!({}))),
            (
                "DELETE /api/v1/cache/policy?agent=bot",
                reply(200, json!({"deleted": true})),
            ),
            (
                "POST /api/v1/cache/purge",
                reply(200, json!({"purged_at": "2026-11-03T00:00:00Z"})),
            ),
        ])
        .await;
        assert_eq!(c.get_cache().await.unwrap()["stats"]["hits"], 0);
        c.set_cache_policy("always", "bot", Some(120), true)
            .await
            .unwrap();
        assert_eq!(
            log.last().body,
            json!({"agent": "bot", "mode": "always", "share_across_principals": true, "ttl_seconds": 120})
        );
        c.remove_cache_policy("bot").await.unwrap();
        assert!(c.purge_cache("").await.unwrap()["purged_at"].is_string());
        assert_eq!(log.last().body, json!({}));
        c.purge_cache("bot").await.unwrap();
        assert_eq!(log.last().body, json!({"agent": "bot"}));
        assert!(c
            .set_cache_policy("sometimes", "", None, false)
            .await
            .is_err());
    }

    #[tokio::test]
    async fn a_rate_limit_says_when_to_come_back_and_is_not_retried() {
        let (c, log) = client(&[(
            "GET /api/v1/limits",
            Reply::Raw(
                429,
                json!({"error": "rate_limited", "message": "agent triage is over its limit", "scope": "agent", "limit": "requests_per_minute", "retry_after": 2}).to_string(),
                vec![("Retry-After".into(), "7".into())],
            ),
        )])
        .await;
        let err = c.list_limits().await.unwrap_err();
        let e = err.downcast_ref::<ApiError>().expect("an ApiError");
        assert!(e.is_rate_limited());
        assert_eq!(
            (e.retry_after, e.scope()),
            (Some(7), Some("agent")),
            "the header wins"
        );
        assert_eq!(log.len(), 1);
    }

    #[tokio::test]
    async fn a_budget_refusal_says_whose_budget_and_in_what_unit() {
        let (c, _) = client(&[(
            "GET /api/v1/spend/budgets",
            reply(402, json!({"error": "run_budget_exhausted", "message": "m", "scope": "kitchen", "dimension": "tokens"})),
        )])
        .await;
        let err = c.list_budgets().await.unwrap_err();
        let e = err.downcast_ref::<ApiError>().unwrap();
        assert!(e.is_budget_exceeded() && !e.is_rate_limited());
        assert_eq!(
            (e.scope(), e.unit(), e.retry_after),
            (Some("kitchen"), Some("tokens"), None)
        );
    }
}

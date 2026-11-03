//! Check a configuration against the kitchen before it is sent, and ask the control plane to check a stored one.
//!
//! The control plane refuses an agent or a recipe that names things the kitchen does not have (HTTP 422, a
//! [`ConfigurationError`] here) and judges every write itself: it is the authority. Everything on the client
//! side is a convenience that is off unless asked for, and must stay skippable:
//!
//! * [`AgentOvenClient::agent_validate`] / [`AgentOvenClient::recipe_validate`]: the server's own verdict on a
//!   stored agent or recipe (`GET .../validate`), read only.
//! * [`AgentOvenClient::validate_agent`] / [`AgentOvenClient::validate_recipe`]: the same checks, run here
//!   against a [`Catalog`] read through this client, for a configuration that is not saved yet.
//! * [`AgentOvenClient::with_validation`]: make `register`, `update_agent`, `bake`, `recook_agent`,
//!   `create_recipe`, `create_recipe_steps`, `bake_recipe` and `run_recipe` run that check first and return a
//!   [`ConfigurationError`] without sending when it finds a problem. Off by default; a clone with the flag off
//!   (`client.clone().with_validation(false)`) always skips it.

use serde_json::{json, Value};

use super::pro::seg;
use super::AgentOvenClient;
use crate::refcheck::{check_agent, check_recipe, Catalog, Options, Validation};

/// The edit fields whose value is a string that, when empty, means "unchanged" (the control plane's
/// `AgentEdit`).
const EDIT_TEXT: [&str; 11] = [
    "description",
    "framework",
    "model_provider",
    "model_name",
    "backup_provider",
    "backup_model",
    "mode",
    "execution_mode",
    "behavior",
    "runtime",
    "reasoning_strategy",
];
/// Numbers that mean "unchanged" at zero.
const EDIT_POSITIVE: [&str; 2] = ["max_turns", "context_budget"];
/// Lists that mean "unchanged" when empty.
const EDIT_NONEMPTY_LISTS: [&str; 2] = ["ingredients", "skills"];
/// Lists where only null means "unchanged": an empty list clears.
const EDIT_CLEARABLE_LISTS: [&str; 3] = ["guardrails", "approval_tools", "subagents"];
/// Strings where "" clears: the summary model is "not part of the edit" only when it is absent.
const EDIT_CLEARABLE_TEXT: [&str; 2] = ["summary_provider", "summary_model"];

/// Lay an edit over an agent the way the control plane does (`AgentEdit.Apply`): what the edit leaves out, or
/// leaves empty where empty means "unchanged", stays as it was.
fn apply_edit(agent: &mut Value, edit: &Value) {
    let (Some(a), Some(e)) = (agent.as_object_mut(), edit.as_object()) else {
        return;
    };
    for (k, v) in e {
        let k = k.as_str();
        let takes = if EDIT_TEXT.contains(&k) {
            v.as_str().is_some_and(|s| !s.is_empty())
        } else if EDIT_POSITIVE.contains(&k) {
            v.as_f64().is_some_and(|n| n > 0.0)
        } else if EDIT_NONEMPTY_LISTS.contains(&k) {
            v.as_array().is_some_and(|l| !l.is_empty())
        } else if EDIT_CLEARABLE_LISTS.contains(&k) {
            v.is_array()
        } else if EDIT_CLEARABLE_TEXT.contains(&k) {
            v.is_string()
        } else {
            k == "bot" && v.is_object()
        };
        if takes {
            a.insert(k.to_string(), v.clone());
        }
    }
}

impl AgentOvenClient {
    /// Run the client-side check before `register`, `update_agent`, `bake`, `recook_agent`, `create_recipe*`,
    /// `bake_recipe` and `run_recipe` send anything, and refuse with a [`crate::ConfigurationError`] on a
    /// problem. Off by default: the control plane judges every request anyway.
    pub fn with_validation(mut self, on: bool) -> Self {
        self.validate = on;
        self
    }

    /// Whether this client checks configurations before sending them.
    pub fn validates(&self) -> bool {
        self.validate
    }

    /// The control plane's verdict on a stored agent, as the next bake would judge it (its draft applied):
    /// `GET /api/v1/agents/{name}/validate`. Read only; an agent that is invalid keeps running.
    pub async fn agent_validate(&self, name: &str) -> anyhow::Result<Validation> {
        let reply = self
            .read_json(
                self.authed_request(
                    self.http
                        .get(self.url(&format!("/api/v1/agents/{}/validate", seg(name)))),
                ),
            )
            .await?;
        Validation::from_reply(&reply)
    }

    /// The control plane's verdict on a stored recipe: `GET /api/v1/recipes/{name}/validate`.
    pub async fn recipe_validate(&self, name: &str) -> anyhow::Result<Validation> {
        let reply = self
            .read_json(
                self.authed_request(
                    self.http
                        .get(self.url(&format!("/api/v1/recipes/{}/validate", seg(name)))),
                ),
            )
            .await?;
        Validation::from_reply(&reply)
    }

    /// Check an agent's configuration (the JSON the API takes) against what the kitchen has, here, before
    /// anything is sent. [`Options::bake`] judges it as something about to run; [`Options::write`] as a draft.
    pub async fn validate_agent(
        &self,
        agent: &Value,
        options: &Options,
    ) -> anyhow::Result<Validation> {
        let catalog = Catalog::load(self).await?;
        let name = agent["name"].as_str().unwrap_or_default();
        Ok(Validation::from_problems(
            name,
            check_agent(agent, &catalog, options),
        ))
    }

    /// [`Self::validate_agent`] for a recipe. `options.environment` is the environment a run asks for.
    pub async fn validate_recipe(
        &self,
        recipe: &Value,
        options: &Options,
    ) -> anyhow::Result<Validation> {
        let catalog = Catalog::load(self).await?;
        let name = recipe["name"].as_str().unwrap_or_default();
        Ok(Validation::from_problems(
            name,
            check_recipe(recipe, &catalog, options),
        ))
    }

    /// A stored agent as the next bake would run it: read, its draft applied, then `edits` (what a re-cook
    /// sends) laid over it, checked here with the bake-strict rules.
    pub async fn validate_stored_agent(
        &self,
        name: &str,
        edits: Option<&Value>,
    ) -> anyhow::Result<Validation> {
        let mut agent = self
            .read_json(
                self.authed_request(
                    self.http
                        .get(self.url(&format!("/api/v1/agents/{}", seg(name)))),
                ),
            )
            .await?;
        if let Some(draft) = agent.as_object_mut().and_then(|a| a.remove("draft")) {
            apply_edit(&mut agent, &draft);
        }
        if let Some(edits) = edits {
            apply_edit(&mut agent, edits);
        }
        self.validate_agent(&agent, &Options::bake()).await
    }

    /// A stored recipe as a run would judge it, on `environment` (empty: the recipe's own default).
    pub async fn validate_stored_recipe(
        &self,
        name: &str,
        environment: &str,
    ) -> anyhow::Result<Validation> {
        let recipe = self.get_recipe(name).await?;
        let options = Options {
            bake: true,
            environment: environment.to_string(),
        };
        self.validate_recipe(&recipe, &options).await
    }

    // ── The pre-checks the calls above run when `with_validation(true)` ──

    /// What a client-side refusal returns, and the warnings logged otherwise.
    fn refuse_or_warn(v: Validation) -> anyhow::Result<()> {
        for w in v.into_result()? {
            tracing::warn!(path = %w.path, "{}", w.message);
        }
        Ok(())
    }

    /// An agent about to be created or updated (a write: gaps that only matter at run time are warnings).
    pub(crate) async fn precheck_agent_write(&self, body: &Value) -> anyhow::Result<()> {
        if !self.validate {
            return Ok(());
        }
        Self::refuse_or_warn(self.validate_agent(body, &Options::write()).await?)
    }

    /// A stored agent about to be baked or re-cooked with `edits`.
    pub(crate) async fn precheck_agent_bake(
        &self,
        name: &str,
        edits: Option<&Value>,
    ) -> anyhow::Result<()> {
        if !self.validate {
            return Ok(());
        }
        Self::refuse_or_warn(self.validate_stored_agent(name, edits).await?)
    }

    /// A recipe about to be created.
    pub(crate) async fn precheck_recipe_write(&self, body: &Value) -> anyhow::Result<()> {
        if !self.validate {
            return Ok(());
        }
        Self::refuse_or_warn(self.validate_recipe(body, &Options::write()).await?)
    }

    /// A stored recipe about to run.
    pub(crate) async fn precheck_recipe_run(
        &self,
        name: &str,
        environment: Option<&str>,
    ) -> anyhow::Result<()> {
        if !self.validate {
            return Ok(());
        }
        Self::refuse_or_warn(
            self.validate_stored_recipe(name, environment.unwrap_or_default())
                .await?,
        )
    }

    /// Send a request and return its JSON reply; a refusal is an [`super::pro::ApiError`], or a
    /// [`crate::ConfigurationError`] for a 422 that lists problems.
    async fn read_json(&self, req: reqwest::RequestBuilder) -> anyhow::Result<Value> {
        let resp = req.send().await?;
        let status = resp.status();
        let headers = resp.headers().clone();
        let text = resp.text().await.unwrap_or_default();
        if !status.is_success() {
            return Err(super::pro::failure(status.as_u16(), &text, &headers));
        }
        Ok(serde_json::from_str(&text).unwrap_or_else(|_| json!({})))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::refcheck::ConfigurationError;
    use crate::testing::{fake_control_plane, json as reply, Log, Reply, Routes};
    use crate::ApiError;

    /// A kitchen with two providers, a search tool, an accepted skill and two agents.
    fn kitchen() -> Vec<(&'static str, Reply)> {
        vec![
            (
                "GET /api/v1/models/providers",
                reply(
                    200,
                    json!([
                        {"name": "anthropic", "kind": "anthropic", "models": ["claude-haiku-4-5", "claude-sonnet-4-5"], "last_test_healthy": true},
                        {"name": "openai", "kind": "openai", "models": ["gpt-4o"]},
                    ]),
                ),
            ),
            (
                "GET /api/v1/prompts",
                reply(200, json!([{"name": "support", "version": 2}])),
            ),
            (
                "GET /api/v1/tools",
                reply(
                    200,
                    json!([{"name": "search", "enabled": true, "capabilities": ["tool"]}]),
                ),
            ),
            (
                "GET /api/v1/skills",
                reply(200, json!([{"name": "pdf-reader", "status": "accepted"}])),
            ),
            (
                "GET /api/v1/agents",
                reply(
                    200,
                    json!([{"name": "writer", "status": "ready"}, {"name": "old", "status": "retired"}]),
                ),
            ),
            (
                "GET /api/v1/recipes",
                reply(
                    200,
                    json!([{"name": "nightly"}, {"name": "gone", "archived_at": "2026-01-01T00:00:00Z"}]),
                ),
            ),
            ("GET /api/v1/channels", reply(200, json!([{"name": "ops"}]))),
            (
                "GET /api/v1/environments",
                reply(200, json!([{"slug": "prod"}])),
            ),
            (
                "GET /api/v1/credentials",
                reply(200, json!([{"name": "partner"}])),
            ),
            (
                "GET /api/v1/scenarios",
                reply(404, json!({"error": "not found"})),
            ),
        ]
    }

    async fn serve(extra: Vec<(&str, Reply)>, without: &[&str]) -> (AgentOvenClient, Log) {
        let mut routes = Routes::new();
        for (k, v) in kitchen().into_iter().chain(extra) {
            if !without.contains(&k) {
                routes.insert(k.to_string(), v);
            }
        }
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    fn refusal(problems: Value) -> Reply {
        reply(
            422,
            json!({"error": "model_name: \"claude-hiku-4-5\" is not a model of provider \"anthropic\"", "code": "invalid_configuration",
            "problems": problems, "warnings": []}),
        )
    }

    fn typo() -> Value {
        json!({"name": "bot", "model_provider": "anthropic", "model_name": "claude-hiku-4-5"})
    }

    fn posts(log: &Log) -> Vec<String> {
        log.calls()
            .into_iter()
            .filter(|c| !c.starts_with("GET "))
            .collect()
    }

    #[tokio::test]
    async fn the_catalog_is_read_from_the_lists_and_an_absent_scenario_route_is_unknown() {
        let (client, _) = serve(vec![], &[]).await;
        let c = Catalog::load(&client).await.unwrap();
        assert_eq!(c.providers.len(), 2);
        assert_eq!(c.providers[0].healthy, Some(true));
        assert_eq!(c.prompts["support"], 2);
        assert_eq!(c.tools[0].capabilities, vec!["tool"]);
        assert_eq!(c.agents[1].status, "retired");
        assert_eq!(c.recipes, vec!["nightly"], "an archived recipe cannot run");
        assert_eq!(c.environments, vec!["prod"]);
        assert!(!c.scenarios.known, "a 404 is not an empty list");
        assert!(!c.roles.known);
        assert!(
            c.unknown.is_empty(),
            "scenarios and roles carry their own flag: {:?}",
            c.unknown
        );
    }

    #[tokio::test]
    async fn a_list_the_caller_may_not_read_is_unknown_never_empty() {
        let (client, _) = serve(
            vec![(
                "GET /api/v1/tools",
                reply(403, json!({"error": "forbidden"})),
            )],
            &[],
        )
        .await;
        let c = Catalog::load(&client).await.unwrap();
        assert!(c.unknown.contains("tool") && c.tools.is_empty());
        // A reference to a tool is therefore not refused; a provider typo still is.
        let agent = json!({"name": "a", "model_provider": "antropic", "model_name": "x", "approval_tools": ["whatever"]});
        let v = client
            .validate_agent(&agent, &Options::write())
            .await
            .unwrap();
        let paths: Vec<&str> = v.problems.iter().map(|p| p.path.as_str()).collect();
        assert_eq!(paths, vec!["model_provider"], "{v:?}");
        assert_eq!(v.problems[0].suggestions, vec!["anthropic"]);
    }

    #[tokio::test]
    async fn a_list_that_cannot_be_read_stops_the_check_instead_of_emptying_the_kitchen() {
        let (client, _) = serve(
            vec![(
                "GET /api/v1/models/providers",
                reply(500, json!({"error": "db down"})),
            )],
            &[],
        )
        .await;
        let err = client
            .validate_agent(&typo(), &Options::write())
            .await
            .unwrap_err();
        assert!(
            format!("{err:#}").contains("/api/v1/models/providers"),
            "{err:#}"
        );
    }

    #[tokio::test]
    async fn catalog_load_calls_only_routes_the_server_has() {
        let (client, log) = serve(vec![], &[]).await;
        Catalog::load(&client).await.unwrap();
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../sdk/fixtures/server-routes.json");
        let routes: Value = serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
        let have: Vec<String> = routes["routes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|r| {
                format!(
                    "{} {}",
                    r["method"].as_str().unwrap(),
                    r["path"].as_str().unwrap()
                )
            })
            .collect();
        let calls = log.calls();
        assert_eq!(calls.len(), 10, "{calls:?}");
        for c in calls {
            assert!(have.contains(&c), "the server has no route {c}");
        }
    }

    #[tokio::test]
    async fn the_server_verdict_is_read_for_an_agent_and_a_recipe() {
        let problem = json!({"path": "summary_model", "code": "not_found", "severity": "error", "kind": "model", "value": "x", "within": "openai",
            "message": "summary_model: \"x\" is not a model of provider \"openai\"", "suggestions": ["gpt-4o"]});
        let (client, log) = serve(
            vec![
                ("GET /api/v1/agents/bot/validate", reply(200, json!({"agent": "bot", "valid": false, "problems": [problem], "warnings": []}))),
                ("GET /api/v1/recipes/nightly/validate", reply(200, json!({"recipe": "nightly", "valid": true, "problems": [], "warnings": []}))),
            ],
            &[],
        )
        .await;
        let v = client.agent_validate("bot").await.unwrap();
        assert!(!v.valid);
        assert_eq!(v.subject, "bot");
        assert_eq!(v.problems[0].suggestions, vec!["gpt-4o"]);
        let err = v.into_result().unwrap_err();
        assert_eq!(err.status, 0, "found here, not by the server");
        assert!(err.to_string().contains("summary_model"), "{err}");
        let r = client.recipe_validate("nightly").await.unwrap();
        assert!(r.valid && r.problems.is_empty() && r.subject == "nightly");
        assert_eq!(
            log.calls(),
            vec![
                "GET /api/v1/agents/bot/validate",
                "GET /api/v1/recipes/nightly/validate"
            ]
        );
        // A missing agent is the server's 404, not a verdict.
        let missing = client.agent_validate("nobody").await.unwrap_err();
        assert_eq!(missing.downcast_ref::<ApiError>().unwrap().status, 404);
    }

    #[tokio::test]
    async fn a_422_with_problems_is_a_configuration_error_for_every_call_that_writes() {
        let problems = json!([{"path": "model_name", "code": "not_found", "severity": "error", "kind": "model", "value": "claude-hiku-4-5",
            "within": "anthropic", "message": "model_name: \"claude-hiku-4-5\" is not a model of provider \"anthropic\"", "suggestions": ["claude-haiku-4-5"]}]);
        let routes: Vec<(&str, Reply)> = vec![
            ("GET /api/v1/agents/bot", reply(404, json!({"error": "no"}))),
            ("POST /api/v1/agents", refusal(problems.clone())),
            ("PUT /api/v1/agents/bot", refusal(problems.clone())),
            ("POST /api/v1/agents/bot/bake", refusal(problems.clone())),
            ("POST /api/v1/agents/bot/recook", refusal(problems.clone())),
            ("POST /api/v1/recipes", refusal(problems.clone())),
            ("POST /api/v1/recipes/r/bake", refusal(problems.clone())),
        ];
        let (client, _) = serve(routes, &[]).await;
        let agent = crate::Agent::builder("bot")
            .model_provider("anthropic")
            .model_name("claude-hiku-4-5")
            .build();
        let errs = vec![
            client.register(&agent).await.unwrap_err(),
            client
                .update_agent("bot", json!({"model_name": "claude-hiku-4-5"}))
                .await
                .unwrap_err(),
            client.bake(&agent, "prod").await.unwrap_err(),
            client.recook_agent("bot", json!({})).await.unwrap_err(),
            client
                .create_recipe_steps("r", "", vec![])
                .await
                .unwrap_err(),
            client.bake_recipe("r", json!({})).await.unwrap_err(),
            client
                .run_recipe("r", &json!({}), None, crate::Signature::None)
                .await
                .unwrap_err(),
        ];
        assert_eq!(errs.len(), 7);
        for e in errs {
            let c = e
                .downcast_ref::<ConfigurationError>()
                .unwrap_or_else(|| panic!("not a ConfigurationError: {e:#}"));
            assert_eq!(c.status, 422);
            assert_eq!(c.problems[0].path, "model_name");
            assert_eq!(c.problems[0].suggestions, vec!["claude-haiku-4-5"]);
            assert!(e.to_string().contains("claude-hiku-4-5"), "{e}");
        }
    }

    #[tokio::test]
    async fn a_422_without_the_configuration_code_stays_an_api_error() {
        let (client, _) = serve(
            vec![(
                "PUT /api/v1/agents/bot",
                reply(422, json!({"error": "unprocessable", "message": "no"})),
            )],
            &[],
        )
        .await;
        let e = client
            .update_agent("bot", json!({"description": "x"}))
            .await
            .unwrap_err();
        assert!(e.downcast_ref::<ConfigurationError>().is_none());
        assert_eq!(e.downcast_ref::<ApiError>().unwrap().status, 422);
    }

    #[tokio::test]
    async fn validation_is_off_by_default_and_the_server_stays_the_authority() {
        let (client, log) = serve(
            vec![(
                "PUT /api/v1/agents/bot",
                reply(
                    200,
                    serde_json::to_value(crate::Agent::builder("bot").build()).unwrap(),
                ),
            )],
            &[],
        )
        .await;
        client.update_agent("bot", typo()).await.unwrap();
        assert_eq!(
            log.calls(),
            vec!["PUT /api/v1/agents/bot"],
            "no list was read, nothing was checked"
        );
    }

    #[tokio::test]
    async fn with_validation_a_bad_name_is_refused_before_anything_is_sent() {
        let stored = serde_json::to_value(crate::Agent::builder("bot").build()).unwrap();
        let (client, log) = serve(vec![("PUT /api/v1/agents/bot", reply(200, stored))], &[]).await;
        let client = client.with_validation(true);
        let e = client.update_agent("bot", typo()).await.unwrap_err();
        let c = e
            .downcast_ref::<ConfigurationError>()
            .expect("a ConfigurationError");
        assert_eq!(c.status, 0);
        assert_eq!(c.problems[0].path, "model_name");
        assert_eq!(c.problems[0].suggestions, vec!["claude-haiku-4-5"]);
        assert!(
            posts(&log).is_empty(),
            "nothing was sent: {:?}",
            log.calls()
        );
        // validate=false always skips, even on a clone of a client that checks.
        client
            .clone()
            .with_validation(false)
            .update_agent("bot", typo())
            .await
            .unwrap();
        assert_eq!(posts(&log), vec!["PUT /api/v1/agents/bot"]);
    }

    #[tokio::test]
    async fn with_validation_a_good_configuration_is_sent_and_a_warning_does_not_stop_it() {
        let stored = serde_json::to_value(crate::Agent::builder("bot").build()).unwrap();
        let (client, log) = serve(vec![("PUT /api/v1/agents/bot", reply(200, stored))], &[]).await;
        let client = client.with_validation(true);
        // A model id that is not in a closed provider's list is an error; a provider without a model is a warning on a write.
        client
            .update_agent("bot", json!({"model_provider": "openai"}))
            .await
            .unwrap();
        assert_eq!(posts(&log), vec!["PUT /api/v1/agents/bot"]);
    }

    #[tokio::test]
    async fn register_with_validation_checks_the_summary_model_and_sends_it() {
        let stored = serde_json::to_value(crate::Agent::builder("bot").build()).unwrap();
        let (client, log) = serve(
            vec![
                ("GET /api/v1/agents/bot", reply(404, json!({}))),
                ("POST /api/v1/agents", reply(201, stored)),
            ],
            &[],
        )
        .await;
        let client = client.with_validation(true);
        let bad = crate::Agent::builder("bot")
            .summary_model("anthropic", "claude-hiku-4-5")
            .build();
        let e = client.register(&bad).await.unwrap_err();
        let c = e.downcast_ref::<ConfigurationError>().unwrap();
        assert_eq!(c.problems[0].path, "summary_model");
        assert!(posts(&log).is_empty());
        let good = crate::Agent::builder("bot")
            .summary_model("anthropic", "claude-haiku-4-5")
            .build();
        client.register(&good).await.unwrap();
        let sent = log.all().into_iter().find(|s| s.method == "POST").unwrap();
        assert_eq!(sent.body["summary_provider"], "anthropic");
        assert_eq!(sent.body["summary_model"], "claude-haiku-4-5");
    }

    #[tokio::test]
    async fn a_bake_with_validation_judges_the_stored_agent_with_its_draft_applied() {
        // Stored fine; the draft (an edit kept for the next bake) holds the typo.
        let stored = json!({"name": "bot", "mode": "managed", "model_provider": "openai", "model_name": "gpt-4o",
            "draft": {"summary_provider": "openai", "summary_model": "gpt-4oo"}});
        let (client, log) = serve(
            vec![
                ("GET /api/v1/agents/bot", reply(200, stored)),
                ("POST /api/v1/agents/bot/bake", reply(200, json!({}))),
            ],
            &[],
        )
        .await;
        let client = client.with_validation(true);
        let agent = crate::Agent::builder("bot").build();
        let e = client.bake(&agent, "prod").await.unwrap_err();
        let c = e.downcast_ref::<ConfigurationError>().unwrap();
        assert_eq!(c.problems[0].path, "summary_model");
        assert_eq!(c.problems[0].within, "openai");
        assert!(posts(&log).is_empty(), "{:?}", log.calls());
    }

    #[tokio::test]
    async fn a_recook_with_validation_applies_the_edits_to_the_stored_agent() {
        let stored = json!({"name": "bot", "mode": "managed", "model_provider": "openai", "model_name": "gpt-4o"});
        let (client, log) = serve(
            vec![
                ("GET /api/v1/agents/bot", reply(200, stored)),
                (
                    "POST /api/v1/agents/bot/recook",
                    reply(200, json!({"status": "ready"})),
                ),
            ],
            &[],
        )
        .await;
        let client = client.with_validation(true);
        let e = client
            .recook_agent("bot", json!({"model_name": "gpt-4"}))
            .await
            .unwrap_err();
        assert_eq!(
            e.downcast_ref::<ConfigurationError>().unwrap().problems[0].path,
            "model_name"
        );
        assert!(posts(&log).is_empty());
        // The same edit that is fine goes through.
        client
            .recook_agent("bot", json!({"model_name": "gpt-4o"}))
            .await
            .unwrap();
        assert_eq!(posts(&log), vec!["POST /api/v1/agents/bot/recook"]);
    }

    #[tokio::test]
    async fn a_recipe_run_with_validation_checks_the_environment_and_the_agents() {
        let stored =
            json!({"name": "r", "steps": [{"name": "a", "kind": "agent", "agent_ref": "writr"}]});
        let (client, log) = serve(
            vec![
                ("GET /api/v1/recipes/r", reply(200, stored)),
                ("POST /api/v1/recipes/r/bake", reply(200, json!({}))),
            ],
            &[],
        )
        .await;
        let client = client.with_validation(true);
        let e = client
            .run_recipe("r", &json!({}), Some("prd"), crate::Signature::None)
            .await
            .unwrap_err();
        let c = e.downcast_ref::<ConfigurationError>().unwrap();
        let mut paths: Vec<&str> = c.problems.iter().map(|p| p.path.as_str()).collect();
        paths.sort();
        assert_eq!(paths, vec!["environment", "steps[0].agent_ref"]);
        assert_eq!(
            c.problems
                .iter()
                .find(|p| p.path == "environment")
                .unwrap()
                .suggestions,
            vec!["prod"]
        );
        assert!(posts(&log).is_empty());
    }

    #[tokio::test]
    async fn a_new_recipe_with_validation_checks_its_steps_before_sending() {
        let (client, log) = serve(
            vec![("POST /api/v1/recipes", reply(201, json!({"id": "1"})))],
            &[],
        )
        .await;
        let client = client.with_validation(true);
        let bad = vec![json!({"name": "a", "kind": "agent", "agent_ref": "writr"})];
        let e = client.create_recipe_steps("r", "", bad).await.unwrap_err();
        assert_eq!(
            e.downcast_ref::<ConfigurationError>().unwrap().problems[0].suggestions,
            vec!["writer"]
        );
        assert!(posts(&log).is_empty());
        client
            .create_recipe_steps(
                "r",
                "",
                vec![json!({"name": "a", "kind": "agent", "agent_ref": "writer"})],
            )
            .await
            .unwrap();
        assert_eq!(posts(&log), vec!["POST /api/v1/recipes"]);
    }
}

//! Build a [`Catalog`] from the control plane's own list calls.

use reqwest::Method;
use serde_json::Value;

use super::{Catalog, Kind, Names};
use crate::client::pro::ApiError;
use crate::client::AgentOvenClient;

use super::catalog::{AgentRef, Provider, Skill, Tool};

/// One list call. `Ok(None)` when the caller may not read it or the edition does not have the route (HTTP 403
/// or 404): that part of the catalog is then unknown, never empty. Any other failure (the server is down, the
/// key is wrong, a 5xx) is an error, because "could not read" must not be mistaken for "nothing exists".
async fn list(client: &AgentOvenClient, path: &str) -> anyhow::Result<Option<Vec<Value>>> {
    match client.call_json(Method::GET, path, None, None).await {
        Ok(Value::Array(items)) => Ok(Some(items)),
        Ok(other) => anyhow::bail!(
            "{path} did not answer with a list (got {})",
            match other {
                Value::Object(_) => "an object",
                _ => "something else",
            }
        ),
        Err(e) => match e.downcast_ref::<ApiError>() {
            Some(api) if api.status == 403 || api.status == 404 => Ok(None),
            _ => Err(e.context(format!("could not read {path} to check the configuration"))),
        },
    }
}

fn text(v: &Value, key: &str) -> String {
    v[key].as_str().unwrap_or_default().to_string()
}

fn texts(v: &Value, key: &str) -> Vec<String> {
    v[key]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(|x| x.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default()
}

impl Catalog {
    /// Read what the kitchen has through the client (the lists the dashboard's dropdowns use): providers with
    /// their models, prompts, tools, skills, agents, recipes, notification channels, environments, kitchen
    /// credentials and, where the edition has them, scenarios.
    ///
    /// A list the caller may not read (HTTP 403, or 404 for a route the edition does not have) leaves that part
    /// unknown (`Catalog::unknown`): references of that kind are not checked. Roles have no list route, so they
    /// are always unknown here and approver roles are left to the server. Any other failure is returned.
    pub async fn load(client: &AgentOvenClient) -> anyhow::Result<Catalog> {
        let (
            providers,
            prompts,
            tools,
            skills,
            agents,
            recipes,
            channels,
            environments,
            credentials,
            scenarios,
        ) = tokio::join!(
            list(client, "/api/v1/models/providers"),
            list(client, "/api/v1/prompts"),
            list(client, "/api/v1/tools"),
            list(client, "/api/v1/skills"),
            list(client, "/api/v1/agents"),
            list(client, "/api/v1/recipes"),
            list(client, "/api/v1/channels"),
            list(client, "/api/v1/environments"),
            list(client, "/api/v1/credentials"),
            list(client, "/api/v1/scenarios"),
        );
        let mut c = Catalog::default();
        let mut skip = |kind: Kind, items: Option<Vec<Value>>| -> Vec<Value> {
            items.unwrap_or_else(|| {
                c.unknown.insert(kind.as_str().to_string());
                Vec::new()
            })
        };
        let providers = skip(Kind::Provider, providers?);
        let prompts = skip(Kind::Prompt, prompts?);
        let tools = skip(Kind::Tool, tools?);
        let skills = skip(Kind::Skill, skills?);
        let agents = skip(Kind::Agent, agents?);
        let recipes = skip(Kind::Recipe, recipes?);
        let channels = skip(Kind::Channel, channels?);
        let environments = skip(Kind::Environment, environments?);
        let credentials = skip(Kind::Credential, credentials?);

        c.providers = providers
            .iter()
            .map(|p| Provider {
                name: text(p, "name"),
                kind: text(p, "kind"),
                models: texts(p, "models"),
                healthy: p["last_test_healthy"].as_bool(),
            })
            .collect();
        for p in prompts.iter().filter(|p| !text(p, "name").is_empty()) {
            // The latest version of a name, whether the list sends one row per name or one per version.
            let latest = c.prompts.entry(text(p, "name")).or_insert(1);
            *latest = (*latest).max(p["version"].as_i64().unwrap_or(1));
        }
        c.tools = tools
            .iter()
            .map(|t| Tool {
                name: text(t, "name"),
                enabled: t["enabled"].as_bool().unwrap_or(false),
                capabilities: texts(t, "capabilities"),
            })
            .collect();
        c.skills = skills
            .iter()
            .map(|s| Skill {
                name: text(s, "name"),
                status: text(s, "status"),
            })
            .collect();
        c.agents = agents
            .iter()
            .map(|a| AgentRef {
                name: text(a, "name"),
                status: text(a, "status"),
            })
            .collect();
        // An archived recipe cannot run, so it is not something to name.
        c.recipes = recipes
            .iter()
            .filter(|r| r["archived_at"].is_null())
            .map(|r| text(r, "name"))
            .collect();
        c.channels = channels.iter().map(|x| text(x, "name")).collect();
        c.environments = environments.iter().map(|x| text(x, "slug")).collect();
        c.credentials = credentials.iter().map(|x| text(x, "name")).collect();
        if let Some(items) = scenarios? {
            let ids = items
                .iter()
                .map(|s| {
                    if s["id"].is_string() {
                        text(s, "id")
                    } else {
                        text(s, "name")
                    }
                })
                .collect();
            c.scenarios = Names {
                known: true,
                names: ids,
            };
        }
        Ok(c)
    }
}

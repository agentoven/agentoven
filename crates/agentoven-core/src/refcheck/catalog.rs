//! What a kitchen has: the names a configuration may refer to. The JSON shape is the `catalogs.*` of
//! `sdk/fixtures/refcheck-cases.json`.

use std::collections::{BTreeMap, BTreeSet};

use serde::{Deserialize, Serialize};

use super::Kind;

/// Provider kinds that serve models their list does not name: OpenRouter and LiteLLM route to whatever the
/// account can reach, Ollama serves whatever was pulled. A model id outside such a provider's list is accepted
/// with a warning. For every other kind the list is the whole truth, unless it is empty (a provider registered
/// without one accepts any id, as bakes always have).
pub const OPEN_MODEL_KINDS: [&str; 3] = ["openrouter", "litellm", "ollama"];

/// A model provider as the checks see it.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Provider {
    pub name: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub models: Vec<String>,
    /// The result of the provider's last connectivity test; `None` when it was never tested.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub healthy: Option<bool>,
}

impl Provider {
    /// Whether the provider may serve a model id that is not in its list.
    pub fn is_open(&self) -> bool {
        OPEN_MODEL_KINDS.contains(&self.kind.as_str()) || self.models.is_empty()
    }

    /// Whether the model is in the provider's list.
    pub fn has(&self, model: &str) -> bool {
        self.models.iter().any(|m| m == model)
    }
}

/// A registered tool.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Tool {
    pub name: String,
    #[serde(default)]
    pub enabled: bool,
    #[serde(default)]
    pub capabilities: Vec<String>,
}

/// A registered skill. `status` is `accepted` for the only state that may run.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Skill {
    pub name: String,
    #[serde(default)]
    pub status: String,
}

/// An agent as a reference sees it.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct AgentRef {
    pub name: String,
    #[serde(default)]
    pub status: String,
}

/// A set of names an edition may or may not be able to list. The community edition has no scenarios and no
/// roles, so `known` is false there and a reference to one is not checked rather than refused.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Names {
    #[serde(default)]
    pub known: bool,
    #[serde(default)]
    pub names: Vec<String>,
}

/// A snapshot of what one kitchen has.
///
/// `unknown` names the lists the reader could not read (an HTTP 403 or 404 on the list call): a reference of
/// that kind is not checked, because a list that could not be read is not an empty one. It is never part of
/// the shared fixture, where every list is known.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Catalog {
    #[serde(default)]
    pub providers: Vec<Provider>,
    /// A prompt's name and its latest version.
    #[serde(default)]
    pub prompts: BTreeMap<String, i64>,
    #[serde(default)]
    pub tools: Vec<Tool>,
    #[serde(default)]
    pub skills: Vec<Skill>,
    #[serde(default)]
    pub agents: Vec<AgentRef>,
    #[serde(default)]
    pub recipes: Vec<String>,
    #[serde(default)]
    pub channels: Vec<String>,
    #[serde(default)]
    pub environments: Vec<String>,
    #[serde(default)]
    pub credentials: Vec<String>,
    #[serde(default)]
    pub scenarios: Names,
    #[serde(default)]
    pub roles: Names,
    #[serde(default, skip_serializing_if = "BTreeSet::is_empty")]
    pub unknown: BTreeSet<String>,
}

impl Catalog {
    /// Whether the list behind this kind of reference was read. Scenarios and roles carry their own flag.
    pub(crate) fn knows(&self, kind: Kind) -> bool {
        !self.unknown.contains(kind.as_str())
    }

    pub(crate) fn provider(&self, name: &str) -> Option<&Provider> {
        self.providers.iter().find(|p| p.name == name)
    }

    pub(crate) fn provider_names(&self) -> Vec<String> {
        sorted(self.providers.iter().map(|p| p.name.clone()).collect())
    }

    /// Every model of every provider (sorted, once each), for a reference that names a model without saying
    /// whose.
    pub(crate) fn all_models(&self) -> Vec<String> {
        let set: BTreeSet<&String> = self
            .providers
            .iter()
            .flat_map(|p| p.models.iter())
            .collect();
        set.into_iter().cloned().collect()
    }

    pub(crate) fn tool(&self, name: &str) -> Option<&Tool> {
        self.tools.iter().find(|t| t.name == name)
    }

    pub(crate) fn tool_names(&self, capability: &str) -> Vec<String> {
        sorted(
            self.tools
                .iter()
                .filter(|t| capability.is_empty() || t.capabilities.iter().any(|c| c == capability))
                .map(|t| t.name.clone())
                .collect(),
        )
    }

    pub(crate) fn skill(&self, name: &str) -> Option<&Skill> {
        self.skills.iter().find(|s| s.name == name)
    }

    pub(crate) fn skill_names(&self) -> Vec<String> {
        sorted(self.skills.iter().map(|s| s.name.clone()).collect())
    }

    pub(crate) fn agent(&self, name: &str) -> Option<&AgentRef> {
        self.agents.iter().find(|a| a.name == name)
    }

    /// The agents worth offering: a retired one is not something to pick.
    pub(crate) fn agent_names(&self) -> Vec<String> {
        sorted(
            self.agents
                .iter()
                .filter(|a| a.status != "retired")
                .map(|a| a.name.clone())
                .collect(),
        )
    }

    pub(crate) fn prompt_names(&self) -> Vec<String> {
        self.prompts.keys().cloned().collect()
    }
}

pub(crate) fn sorted(mut v: Vec<String>) -> Vec<String> {
    v.sort();
    v
}

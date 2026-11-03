//! Check that what an agent or a recipe names exists in the kitchen that holds it.
//!
//! An agent's configuration is full of names: the provider and model it runs on, the model that summarizes its
//! history, the tools, prompts and skills it uses, the agents it may delegate to. A recipe names agents, steps,
//! channels, credentials, approver roles. A typo in any of them surfaces late (at bake, or on the first run) as
//! a string the operator has to decode. This module reads such a configuration against a [`Catalog`] (a
//! snapshot of what the kitchen has) and returns every problem at once, each with the field it is in, the value
//! that is wrong and the nearest values that are right.
//!
//! It is the Rust twin of the control plane's `pkg/refcheck` and follows it rule for rule; the shared cases in
//! `sdk/fixtures/refcheck-cases.json` (run by `tests/refcheck_cases.rs`) pin the two together. It is pure: no
//! network, no clock. [`Catalog::load`] builds the catalog from the API; the client can run the check before
//! it sends (`AgentOvenClient::with_validation`, `agentoven ... --validate`). The control plane stays the
//! authority: it repeats every check and refuses a write with HTTP 422 ([`ConfigurationError`]); a client-side
//! check is a convenience that is off unless asked for.
//!
//! The check reads the JSON the API takes (`serde_json::Value`), not the typed [`crate::Agent`], which does
//! not carry every setting the control plane reads. An absent field is the field's zero value, as it is on the
//! server.

mod agent;
mod catalog;
mod load;
mod nearest;
mod recipe;

use serde::{Deserialize, Serialize};
use serde_json::Value;

pub use agent::check_agent;
pub use catalog::{AgentRef, Catalog, Names, Provider, Skill, Tool, OPEN_MODEL_KINDS};
pub use nearest::{nearest, MAX_SUGGESTIONS};
pub use recipe::check_recipe;

use catalog::sorted;

/// Severity of a problem. Only errors refuse a write or a bake; a warning says something a person should see (a
/// disabled optional tool, a provider whose last test failed, a model id the provider does not list but whose
/// kind accepts any).
pub const SEVERITY_ERROR: &str = "error";
pub const SEVERITY_WARNING: &str = "warning";

/// Codes say what is wrong. They are stable: clients switch on them.
pub mod code {
    /// The thing named does not exist in this kitchen (another kitchen's counts as absent).
    pub const NOT_FOUND: &str = "not_found";
    /// It exists but cannot be used as it is (a disabled tool, a skill nobody approved, a retired agent).
    pub const NOT_USABLE: &str = "not_usable";
    /// A model id the provider's list does not hold, accepted because the provider's kind serves models outside
    /// its list. Always a warning.
    pub const UNLISTED: &str = "unlisted";
    /// A field the API has no such name for (found by the server when it reads the body).
    pub const UNKNOWN_FIELD: &str = "unknown_field";
    /// A value of the wrong JSON type.
    pub const WRONG_TYPE: &str = "wrong_type";
    /// A number outside what the setting allows.
    pub const OUT_OF_RANGE: &str = "out_of_range";
    /// The same thing twice where it can be given once.
    pub const DUPLICATE: &str = "duplicate";
    /// A value the setting needs and does not have.
    pub const REQUIRED: &str = "required";
    /// A value outside the fixed set the setting accepts.
    pub const INVALID_VALUE: &str = "invalid_value";
}

/// What a reference should have pointed at. A problem carries it as text (`Problem::kind`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    Provider,
    Model,
    Prompt,
    Tool,
    Skill,
    Agent,
    Recipe,
    Scenario,
    Channel,
    Environment,
    Credential,
    Role,
    Step,
    IngredientKind,
    StepKind,
    Embedding,
    VectorStore,
    Backend,
}

impl Kind {
    pub fn as_str(self) -> &'static str {
        match self {
            Kind::Provider => "provider",
            Kind::Model => "model",
            Kind::Prompt => "prompt",
            Kind::Tool => "tool",
            Kind::Skill => "skill",
            Kind::Agent => "agent",
            Kind::Recipe => "recipe",
            Kind::Scenario => "scenario",
            Kind::Channel => "channel",
            Kind::Environment => "environment",
            Kind::Credential => "credential",
            Kind::Role => "role",
            Kind::Step => "step",
            Kind::IngredientKind => "ingredient_kind",
            Kind::StepKind => "step_kind",
            Kind::Embedding => "embedding",
            Kind::VectorStore => "vectorstore",
            Kind::Backend => "vectorstore_backend",
        }
    }
}

/// One thing wrong with a configuration. The JSON is the server's own (`problems` and `warnings` of a 422 or of
/// `GET /agents/{name}/validate`).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Problem {
    /// The field, in the JSON the API takes: `summary_model`, `ingredients[2].config.provider`, `steps[1].agent_ref`.
    pub path: String,
    pub code: String,
    #[serde(default)]
    pub severity: String,
    /// What the value should have named (empty for problems that are not references).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub kind: String,
    /// What was given, as text.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub value: String,
    /// What the value should have been found in: the provider whose model list was searched.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub within: String,
    /// The sentence for a person.
    #[serde(default)]
    pub message: String,
    /// The nearest valid values, nearest first (at most three).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub suggestions: Vec<String>,
}

impl Problem {
    /// Whether the problem refuses a write.
    pub fn is_blocking(&self) -> bool {
        self.severity != SEVERITY_WARNING
    }
}

/// Separate the problems that refuse a write from the warnings.
pub fn split(problems: Vec<Problem>) -> (Vec<Problem>, Vec<Problem>) {
    problems.into_iter().partition(Problem::is_blocking)
}

/// What the configuration is being checked for.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// Set when the configuration is about to run (a bake, a re-cook, a run of a recipe). A draft may be
    /// incomplete, so on a write the gaps that only matter at run time (a model provider with no model, a
    /// managed agent with no model) are warnings; at a bake they are errors.
    pub bake: bool,
    /// The environment a run was asked for, checked against the kitchen's.
    pub environment: String,
}

impl Options {
    /// Options for a write (create or update): gaps are warnings.
    pub fn write() -> Self {
        Self::default()
    }

    /// Options for something about to run.
    pub fn bake() -> Self {
        Self {
            bake: true,
            environment: String::new(),
        }
    }
}

/// The outcome of checking one agent or recipe: the problems that refuse it and the warnings.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Validation {
    /// The agent's or recipe's name.
    #[serde(default, skip_serializing)]
    pub subject: String,
    pub valid: bool,
    #[serde(default)]
    pub problems: Vec<Problem>,
    #[serde(default)]
    pub warnings: Vec<Problem>,
}

impl Validation {
    /// Build the outcome from a raw list of problems, as [`check_agent`] returns them.
    pub fn from_problems(subject: &str, problems: Vec<Problem>) -> Self {
        let (problems, warnings) = split(problems);
        Self {
            subject: subject.to_string(),
            valid: problems.is_empty(),
            problems,
            warnings,
        }
    }

    /// Read the reply of `GET /agents/{name}/validate` or `GET /recipes/{name}/validate`.
    pub(crate) fn from_reply(reply: &Value) -> anyhow::Result<Self> {
        let mut v: Validation = serde_json::from_value(reply.clone()).map_err(|e| {
            anyhow::anyhow!(
                "the control plane sent a validation report this client cannot read: {e}"
            )
        })?;
        v.subject = reply["agent"]
            .as_str()
            .or_else(|| reply["recipe"].as_str())
            .unwrap_or_default()
            .to_string();
        Ok(v)
    }

    /// `Ok` with the warnings when the configuration is valid, the [`ConfigurationError`] when it is not.
    pub fn into_result(self) -> Result<Vec<Problem>, ConfigurationError> {
        if self.valid {
            Ok(self.warnings)
        } else {
            Err(ConfigurationError::found_here(
                &self.subject,
                self.problems,
                self.warnings,
            ))
        }
    }
}

/// The control plane (or a client-side check) refused a configuration that names things the kitchen does not
/// have: HTTP 422 with `code: "invalid_configuration"`. It carries every problem at once.
///
/// It is returned inside an `anyhow::Error`: `err.downcast_ref::<ConfigurationError>()`. `Display` is the
/// server's message followed by one line per problem (each already names its field).
#[derive(Debug, Clone)]
pub struct ConfigurationError {
    /// 422 when the control plane refused, 0 when the client's own check did before anything was sent.
    pub status: u16,
    /// The server's one-line message (`error` of the reply).
    pub message: String,
    pub problems: Vec<Problem>,
    pub warnings: Vec<Problem>,
}

/// The `code` of a reply that is a configuration refusal.
pub const INVALID_CONFIGURATION: &str = "invalid_configuration";

impl ConfigurationError {
    /// Read a 422 body. `None` unless it is a configuration refusal (`code == "invalid_configuration"`): any
    /// other 422 stays an [`crate::ApiError`].
    pub(crate) fn from_reply(status: u16, body: &Value) -> Option<Self> {
        if status != 422 || body["code"].as_str() != Some(INVALID_CONFIGURATION) {
            return None;
        }
        let list = |key: &str| -> Vec<Problem> {
            body[key]
                .as_array()
                .map(|a| {
                    a.iter()
                        .filter_map(|p| serde_json::from_value(p.clone()).ok())
                        .collect()
                })
                .unwrap_or_default()
        };
        let problems = list("problems");
        let message = body["error"]
            .as_str()
            .or_else(|| body["message"].as_str())
            .unwrap_or("the configuration is not valid")
            .to_string();
        Some(Self {
            status,
            message,
            problems,
            warnings: list("warnings"),
        })
    }

    /// The error for problems the client found before sending anything.
    pub fn found_here(subject: &str, problems: Vec<Problem>, warnings: Vec<Problem>) -> Self {
        let message = match problems.as_slice() {
            [] => "the configuration is not valid".to_string(),
            [only] => only.message.clone(),
            [first, rest @ ..] => format!("{} (and {} more)", first.message, rest.len()),
        };
        let message = if subject.is_empty() {
            message
        } else {
            format!("{subject}: {message}")
        };
        Self {
            status: 0,
            message,
            problems,
            warnings,
        }
    }
}

impl std::fmt::Display for ConfigurationError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.message)?;
        for p in &self.problems {
            if p.message != self.message {
                write!(f, "\n  {}", p.message)?;
            }
        }
        Ok(())
    }
}

impl std::error::Error for ConfigurationError {}

// ── The checker both kinds of configuration share ─────────────────────────────────────────────

pub(crate) type Map = serde_json::Map<String, Value>;

pub(crate) struct Checker<'a> {
    pub c: &'a Catalog,
    pub o: &'a Options,
    pub out: Vec<Problem>,
}

pub(crate) fn quote(s: &str) -> String {
    format!("{s:?}")
}

fn join_quoted(ss: &[String]) -> String {
    ss.iter().map(|s| quote(s)).collect::<Vec<_>>().join(", ")
}

fn join_or(ss: &[String]) -> String {
    let q: Vec<String> = ss.iter().map(|s| quote(s)).collect();
    match q.as_slice() {
        [one] => one.clone(),
        [head @ .., last] => format!("{} or {}", head.join(", "), last),
        [] => String::new(),
    }
}

pub(crate) fn trim_float(f: f64) -> String {
    if f.is_finite() && f == (f as i64) as f64 {
        format!("{}", f as i64)
    } else {
        format!("{f}")
    }
}

/// A value as text for `Problem::value`: a string as it is, anything else as compact JSON (clipped).
fn describe(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Number(n) => trim_float(n.as_f64().unwrap_or(0.0)),
        other => {
            let text = other.to_string();
            if text.len() > 60 {
                let mut end = 57;
                while !text.is_char_boundary(end) {
                    end -= 1;
                }
                format!("{}...", &text[..end])
            } else {
                text
            }
        }
    }
}

fn json_type_name(v: &Value) -> &'static str {
    match v {
        Value::Null => "null",
        Value::String(_) => "a string",
        Value::Bool(_) => "a boolean",
        Value::Object(_) => "an object",
        Value::Array(_) => "a list",
        Value::Number(_) => "a number",
    }
}

/// A string field, empty when absent or not a string.
pub(crate) fn str_of<'v>(m: &'v Map, key: &str) -> &'v str {
    m.get(key).and_then(Value::as_str).unwrap_or("")
}

/// The strings of a list field (anything else in it is skipped).
pub(crate) fn strings_of(m: &Map, key: &str) -> Vec<String> {
    m.get(key)
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(|v| v.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default()
}

/// A whole-number field of the agent or step itself, zero when absent.
pub(crate) fn int_of(m: &Map, key: &str) -> i64 {
    m.get(key)
        .map(|v| {
            v.as_i64()
                .unwrap_or_else(|| v.as_f64().unwrap_or(0.0) as i64)
        })
        .unwrap_or(0)
}

pub(crate) fn float_of(m: &Map, key: &str) -> f64 {
    m.get(key).and_then(Value::as_f64).unwrap_or(0.0)
}

pub(crate) fn present<'v>(m: &'v Map, key: &str) -> Option<&'v Value> {
    m.get(key).filter(|v| !v.is_null())
}

pub(crate) fn contains(list: &[String], s: &str) -> bool {
    list.iter().any(|x| x == s)
}

pub(crate) fn contains_str(list: &[&str], s: &str) -> bool {
    list.contains(&s)
}

impl<'a> Checker<'a> {
    pub fn new(c: &'a Catalog, o: &'a Options) -> Self {
        Self {
            c,
            o,
            out: Vec::new(),
        }
    }

    pub fn add(&mut self, mut p: Problem) {
        if p.severity.is_empty() {
            p.severity = SEVERITY_ERROR.to_string();
        }
        self.out.push(p);
    }

    /// The severity of a gap that only matters when the configuration runs.
    pub fn gap_severity(&self) -> &'static str {
        if self.o.bake {
            SEVERITY_ERROR
        } else {
            SEVERITY_WARNING
        }
    }

    pub fn required(&mut self, path: &str, what: &str) {
        self.add(Problem {
            path: path.into(),
            code: code::REQUIRED.into(),
            message: format!("{path}: {what}"),
            ..Default::default()
        });
    }

    pub fn invalid(&mut self, path: &str, value: &str, message: &str, suggestions: Vec<String>) {
        self.add(Problem {
            path: path.into(),
            code: code::INVALID_VALUE.into(),
            value: value.into(),
            message: format!("{path}: {message}"),
            suggestions,
            ..Default::default()
        });
    }

    pub fn wrong_type(&mut self, path: &str, want: &str, got: &Value) {
        self.add(Problem {
            path: path.into(),
            code: code::WRONG_TYPE.into(),
            value: describe(got),
            message: format!("{path}: must be {want}, not {}", json_type_name(got)),
            ..Default::default()
        });
    }

    pub fn out_of_range(&mut self, path: &str, got: f64, lo: f64, hi: f64, whole: bool) {
        let what = if whole { "a whole number" } else { "a number" };
        self.add(Problem {
            path: path.into(),
            code: code::OUT_OF_RANGE.into(),
            value: trim_float(got),
            message: format!(
                "{path}: {} must be {what} from {} to {}",
                trim_float(got),
                trim_float(lo),
                trim_float(hi)
            ),
            ..Default::default()
        });
    }

    /// A value that is not among the candidates, with the nearest ones.
    pub fn not_found(
        &mut self,
        path: &str,
        kind: Kind,
        value: &str,
        within: &str,
        candidates: &[String],
    ) {
        let sugg = nearest(value, candidates, MAX_SUGGESTIONS);
        let what = match kind {
            Kind::Model if !within.is_empty() => format!("a model of provider {}", quote(within)),
            Kind::Model => "a model of any provider in this kitchen".to_string(),
            Kind::Agent => "an agent in this kitchen".to_string(),
            Kind::Step => "a step of this recipe".to_string(),
            Kind::IngredientKind => "an ingredient kind".to_string(),
            Kind::Backend => "a vector store backend".to_string(),
            Kind::StepKind => "a step kind".to_string(),
            Kind::Embedding | Kind::VectorStore => {
                format!("one of this agent's {} ingredients", kind.as_str())
            }
            Kind::Environment => "an environment of this kitchen".to_string(),
            other => format!("a {} in this kitchen", other.as_str()),
        };
        let mut msg = format!("{path}: {} is not {what}", quote(value));
        if !sugg.is_empty() {
            msg += &format!(" (did you mean {}?)", join_or(&sugg));
        } else if candidates.is_empty() {
            msg += " (there are none)";
        } else if candidates.len() <= 8 {
            msg += &format!(" (available: {})", join_quoted(candidates));
        } else {
            msg += &format!(" ({} available)", candidates.len());
        }
        self.add(Problem {
            path: path.into(),
            code: code::NOT_FOUND.into(),
            kind: kind.as_str().into(),
            value: value.into(),
            within: within.into(),
            message: msg,
            suggestions: sugg,
            ..Default::default()
        });
    }

    pub fn not_usable(&mut self, path: &str, kind: Kind, value: &str, why: &str, severity: &str) {
        self.add(Problem {
            path: path.into(),
            code: code::NOT_USABLE.into(),
            severity: severity.into(),
            kind: kind.as_str().into(),
            value: value.into(),
            message: format!("{path}: {} {} {why}", kind.as_str(), quote(value)),
            ..Default::default()
        });
    }

    /// A provider name; the provider when it exists. When the kitchen's providers could not be listed, a
    /// provider with no list stands in (it accepts any model id), so nothing is refused that cannot be shown
    /// absent but the checks that need no catalog still run.
    pub fn provider(&mut self, path: &str, name: &str) -> Option<Provider> {
        if !self.c.knows(Kind::Provider) {
            return Some(Provider {
                name: name.into(),
                ..Default::default()
            });
        }
        let Some(p) = self.c.provider(name).cloned() else {
            let names = self.c.provider_names();
            self.not_found(path, Kind::Provider, name, "", &names);
            return None;
        };
        if p.healthy == Some(false) {
            self.not_usable(
                path,
                Kind::Provider,
                name,
                "failed its last connectivity test",
                SEVERITY_WARNING,
            );
        }
        Some(p)
    }

    /// A model id against a provider that exists.
    pub fn model(&mut self, path: &str, p: &Provider, model: &str) {
        if p.has(model) {
            return;
        }
        if !p.is_open() {
            self.not_found(path, Kind::Model, model, &p.name, &sorted(p.models.clone()));
        } else if !p.models.is_empty() {
            self.add(Problem {
                path: path.into(),
                code: code::UNLISTED.into(),
                severity: SEVERITY_WARNING.into(),
                kind: Kind::Model.as_str().into(),
                value: model.into(),
                within: p.name.clone(),
                message: format!(
                    "{path}: {} is not in provider {}'s list; accepted because a {} provider serves models its list does not name",
                    quote(model),
                    quote(&p.name),
                    p.kind
                ),
                ..Default::default()
            });
        }
    }

    /// A provider/model pair. `provider_required` says whether a model without a provider is a note (the
    /// primary and backup model) or is looked up among every provider's models (the summarizer, whose provider
    /// is optional). `model_required` is whether a provider without a model is a gap.
    pub fn model_pair(
        &mut self,
        provider_path: &str,
        model_path: &str,
        provider: &str,
        model: &str,
        provider_required: bool,
        model_required: bool,
    ) {
        if provider.is_empty() && model.is_empty() {
            return;
        }
        if provider.is_empty() {
            if provider_required {
                // Nothing reads a model name without its provider (an external agent may carry one), so this
                // is a note, not a refusal; a managed agent with no model at all is refused at its bake.
                self.add(Problem {
                    path: provider_path.into(),
                    code: code::REQUIRED.into(),
                    severity: SEVERITY_WARNING.into(),
                    message: format!("{provider_path}: {model_path} has no effect without the provider that serves it"),
                    ..Default::default()
                });
                return;
            }
            if !self.c.knows(Kind::Provider)
                || self.c.providers.iter().any(|p| p.has(model) || p.is_open())
            {
                return; // some provider serves it, or any id; the model cannot be shown absent
            }
            let all = self.c.all_models();
            self.not_found(model_path, Kind::Model, model, "", &all);
            return;
        }
        let Some(p) = self.provider(provider_path, provider) else {
            return;
        };
        if model.is_empty() {
            if model_required {
                let sev = self.gap_severity();
                self.add(Problem {
                    path: model_path.into(),
                    code: code::REQUIRED.into(),
                    severity: sev.into(),
                    within: provider.into(),
                    message: format!(
                        "{model_path}: provider {} is set and needs a model",
                        quote(provider)
                    ),
                    ..Default::default()
                });
            }
            return;
        }
        self.model(model_path, &p, model);
    }

    /// A tool name. `needs` is the capability the use requires (`tool` to call it, `notify` to send to it);
    /// `required` says whether a tool that exists but cannot be used is an error or a warning.
    pub fn tool(&mut self, path: &str, name: &str, needs: &str, required: bool) {
        if !self.c.knows(Kind::Tool) {
            return;
        }
        let Some(t) = self.c.tool(name).cloned() else {
            let names = self.c.tool_names(needs);
            self.not_found(path, Kind::Tool, name, "", &names);
            return;
        };
        let sev = if required {
            SEVERITY_ERROR
        } else {
            SEVERITY_WARNING
        };
        if !t.enabled {
            self.not_usable(path, Kind::Tool, name, "is disabled", sev);
        } else if !needs.is_empty() && !t.capabilities.iter().any(|c| c == needs) {
            self.not_usable(
                path,
                Kind::Tool,
                name,
                &format!("does not have the {} capability", quote(needs)),
                sev,
            );
        }
    }

    pub fn skill(&mut self, path: &str, name: &str, required: bool) {
        if !self.c.knows(Kind::Skill) {
            return;
        }
        let Some(s) = self.c.skill(name).cloned() else {
            let names = self.c.skill_names();
            self.not_found(path, Kind::Skill, name, "", &names);
            return;
        };
        if s.status != "accepted" {
            let sev = if required {
                SEVERITY_ERROR
            } else {
                SEVERITY_WARNING
            };
            self.not_usable(
                path,
                Kind::Skill,
                name,
                &format!("is not accepted (status: {})", s.status),
                sev,
            );
        }
    }

    /// An agent a subagent list or a recipe step names.
    pub fn agent_ref(&mut self, path: &str, name: &str) {
        if !self.c.knows(Kind::Agent) {
            return;
        }
        let Some(a) = self.c.agent(name).cloned() else {
            let names = self.c.agent_names();
            self.not_found(path, Kind::Agent, name, "", &names);
            return;
        };
        match a.status.as_str() {
            "retired" => self.not_usable(
                path,
                Kind::Agent,
                name,
                "is retired: register it again to bring it back",
                SEVERITY_ERROR,
            ),
            "burnt" | "cooled" => self.not_usable(
                path,
                Kind::Agent,
                name,
                &format!("is not running (status: {})", a.status),
                SEVERITY_WARNING,
            ),
            _ => {}
        }
    }

    /// A name that must be one of `set` (when the kitchen's list of that kind was read).
    pub fn from_set(&mut self, path: &str, kind: Kind, name: &str, set: &[String]) {
        if self.c.knows(kind) && !contains(set, name) {
            self.not_found(path, kind, name, "", &sorted(set.to_vec()));
        }
    }

    /// The second and later occurrence of each value of a list.
    pub fn dupes(&mut self, path: &str, values: &[String]) {
        let mut seen: Vec<&String> = Vec::new();
        for (i, v) in values.iter().enumerate() {
            if seen.contains(&v) {
                self.add(Problem {
                    path: format!("{path}[{i}]"),
                    code: code::DUPLICATE.into(),
                    value: v.clone(),
                    message: format!("{path}[{i}]: {} is listed more than once", quote(v)),
                    ..Default::default()
                });
            }
            seen.push(v);
        }
    }

    /// A value against a fixed set. An empty value is the default and passes.
    pub fn enum_value(&mut self, path: &str, value: &str, allowed: &[&str]) {
        if value.is_empty() || contains_str(allowed, value) {
            return;
        }
        let allowed_owned: Vec<String> = allowed.iter().map(|s| s.to_string()).collect();
        let sugg = nearest(value, &allowed_owned, MAX_SUGGESTIONS);
        let msg = if sugg.is_empty() {
            format!(
                "{} is not valid: must be one of {}",
                quote(value),
                join_quoted(&allowed_owned)
            )
        } else {
            format!(
                "{} is not valid (did you mean {}?)",
                quote(value),
                join_or(&sugg)
            )
        };
        self.invalid(path, value, &msg, sugg);
    }

    pub fn range_int(&mut self, path: &str, v: i64, lo: i64, hi: i64) {
        if v < lo || v > hi {
            self.out_of_range(path, v as f64, lo as f64, hi as f64, true);
        }
    }

    // ── values read from a config map ──

    /// A string setting, reporting a value of another type. `None` when absent, null or wrong.
    pub fn cfg_string(&mut self, path: &str, m: &Map, key: &str) -> Option<String> {
        match present(m, key)? {
            Value::String(s) => Some(s.clone()),
            other => {
                let other = other.clone();
                self.wrong_type(&format!("{path}.{key}"), "a string", &other);
                None
            }
        }
    }

    /// A whole-number setting within `[lo, hi]`. `None` when absent or wrong.
    pub fn cfg_int(&mut self, path: &str, m: &Map, key: &str, lo: i64, hi: i64) -> Option<i64> {
        let v = present(m, key)?.clone();
        let full = format!("{path}.{key}");
        let Some(f) = v.as_f64() else {
            self.wrong_type(&full, "a whole number", &v);
            return None;
        };
        if !f.is_finite() || f.fract() != 0.0 {
            self.wrong_type(&full, "a whole number", &v);
            return None;
        }
        if (f as i64) < lo || (f as i64) > hi {
            self.out_of_range(&full, f, lo as f64, hi as f64, true);
            return None;
        }
        Some(f as i64)
    }

    pub fn cfg_float(&mut self, path: &str, m: &Map, key: &str, lo: f64, hi: f64) -> Option<f64> {
        let v = present(m, key)?.clone();
        let full = format!("{path}.{key}");
        let Some(f) = v.as_f64() else {
            self.wrong_type(&full, "a number", &v);
            return None;
        };
        if f < lo || f > hi {
            self.out_of_range(&full, f, lo, hi, false);
            return None;
        }
        Some(f)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn problem(path: &str, message: &str) -> Value {
        json!({"path": path, "code": "not_found", "severity": "error", "kind": "model", "value": "x", "message": message, "suggestions": ["y"]})
    }

    #[test]
    fn only_a_422_with_the_configuration_code_is_a_configuration_error() {
        let body = json!({"error": "first (and 1 more)", "code": "invalid_configuration",
            "problems": [problem("a", "a: bad"), problem("b", "b: bad")], "warnings": [problem("c", "c: meh")]});
        let e = ConfigurationError::from_reply(422, &body).unwrap();
        assert_eq!((e.problems.len(), e.warnings.len(), e.status), (2, 1, 422));
        assert_eq!(e.problems[0].suggestions, vec!["y"]);
        // Display: the server's message, then each problem (which names its field).
        assert_eq!(e.to_string(), "first (and 1 more)\n  a: bad\n  b: bad");
        assert!(
            ConfigurationError::from_reply(400, &body).is_none(),
            "another status"
        );
        assert!(
            ConfigurationError::from_reply(422, &json!({"error": "x", "code": "other"})).is_none(),
            "another code"
        );
        assert!(
            ConfigurationError::from_reply(422, &json!({"error": "x"})).is_none(),
            "no code"
        );
    }

    #[test]
    fn a_problem_that_cannot_be_read_does_not_hide_the_others() {
        let body = json!({"error": "bad", "code": "invalid_configuration", "problems": [{"nonsense": 1}, problem("a", "a: bad")]});
        let e = ConfigurationError::from_reply(422, &body).unwrap();
        assert_eq!(e.problems.len(), 1);
    }

    #[test]
    fn the_error_found_here_says_how_many_problems_there_are() {
        let p = |m: &str| Problem {
            path: "a".into(),
            code: "x".into(),
            severity: "error".into(),
            message: m.into(),
            ..Default::default()
        };
        let e = ConfigurationError::found_here("bot", vec![p("one"), p("two"), p("three")], vec![]);
        assert_eq!(e.message, "bot: one (and 2 more)");
        assert_eq!(e.status, 0);
        assert_eq!(
            ConfigurationError::found_here("", vec![p("only")], vec![]).message,
            "only"
        );
    }

    #[test]
    fn a_warning_does_not_make_a_configuration_invalid() {
        let warn = Problem {
            path: "a".into(),
            code: "unlisted".into(),
            severity: SEVERITY_WARNING.into(),
            ..Default::default()
        };
        let v = Validation::from_problems("bot", vec![warn.clone()]);
        assert!(v.valid && v.problems.is_empty());
        assert_eq!(v.into_result().unwrap(), vec![warn]);
    }

    #[test]
    fn a_problem_round_trips_as_the_servers_json() {
        let wire = problem("ingredients[2].config.provider", "bad");
        let p: Problem = serde_json::from_value(wire.clone()).unwrap();
        assert_eq!(
            serde_json::to_value(&p).unwrap()["suggestions"],
            wire["suggestions"]
        );
        // Empty optional fields are left out, as the server leaves them out.
        let bare = serde_json::to_value(Problem {
            path: "a".into(),
            code: "required".into(),
            severity: "error".into(),
            message: "m".into(),
            ..Default::default()
        })
        .unwrap();
        assert!(
            bare.get("kind").is_none()
                && bare.get("suggestions").is_none()
                && bare.get("within").is_none()
        );
    }
}

//! The checks of an agent's configuration (the Rust twin of `pkg/refcheck/agent.go`).

use serde_json::Value;

use super::{
    code, contains, contains_str, float_of, int_of, present, quote, str_of, strings_of, Catalog,
    Checker, Kind, Map, Options, Problem,
};

// Limits of the numeric settings. They are generous: they exist to refuse a value that cannot be meant (a
// negative number, a million turns), not to ration anything.
const MAX_TURNS_LIMIT: i64 = 1000;
const MIN_CONTEXT_BUDGET: i64 = 256;
const MAX_CONTEXT_BUDGET: i64 = 10_000_000;
const MAX_NAME_LENGTH: usize = 128;
const MAX_TOP_K: i64 = 1000;
const MAX_EMBEDDING_DIMS: i64 = 65_536;
const MAX_EMBEDDING_BATCH: i64 = 100_000;
const MAX_SCENARIO_ROLLOUT: i64 = 1000;

const AGENT_MODES: [&str; 2] = ["managed", "external"];
const AGENT_RUNTIMES: [&str; 7] = [
    "agentoven",
    "langchain",
    "langgraph",
    "deepagents",
    "crewai",
    "custom",
    "agenticbot",
];
const EXEC_MODES: [&str; 3] = ["local", "docker", "k8s"];
const BEHAVIORS: [&str; 2] = ["reactive", "agentic"];
const STRATEGIES: [&str; 3] = ["react", "plan-and-execute", "reflexion"];
const INGREDIENT_KINDS: [&str; 10] = [
    "model",
    "tool",
    "prompt",
    "data",
    "observability",
    "embedding",
    "vectorstore",
    "retriever",
    "scenario",
    "skill",
];
const VECTOR_BACKENDS: [&str; 8] = [
    "embedded",
    "pgvector",
    "pinecone",
    "qdrant",
    "cosmosdb",
    "chroma",
    "snowflake",
    "databricks",
];
const GUARDRAIL_KINDS: [&str; 8] = [
    "content_filter",
    "pii_detection",
    "topic_restriction",
    "max_length",
    "regex_filter",
    "prompt_injection",
    "llamaguard",
    "custom",
];
const GUARDRAIL_STAGES: [&str; 5] = ["input", "output", "both", "retrieval", "all"];
const GUARDRAIL_ACTIONS: [&str; 5] = ["block", "redact", "mask", "warn", "require_approval"];

/// Check an agent's configuration, as the JSON the API takes, against what the kitchen has. The agent is
/// judged as it is given: apply its draft first to judge what the next bake would run.
pub fn check_agent(agent: &Value, catalog: &Catalog, options: &Options) -> Vec<Problem> {
    let empty = Map::new();
    let a = agent.as_object().unwrap_or(&empty);
    let mut ck = Checker::new(catalog, options);
    check(&mut ck, a);
    ck.out
}

fn has_control(s: &str) -> bool {
    s.chars().any(|c| (c as u32) < 0x20 || c as u32 == 0x7f)
}

fn check(ck: &mut Checker, a: &Map) {
    let name = str_of(a, "name");
    if !name.is_empty() {
        if name.len() > MAX_NAME_LENGTH {
            ck.invalid(
                "name",
                name,
                &format!("is longer than {MAX_NAME_LENGTH} characters"),
                vec![],
            );
        } else if name.contains(['/', '\\', '?', '#', '%']) || has_control(name) {
            ck.invalid("name", name, r##"must not contain "/", "\", "?", "#", "%" or control characters: it is part of the agent's URL"##, vec![]);
        }
    }
    ck.enum_value("mode", str_of(a, "mode"), &AGENT_MODES);
    ck.enum_value("runtime", str_of(a, "runtime"), &AGENT_RUNTIMES);
    ck.enum_value("execution_mode", str_of(a, "execution_mode"), &EXEC_MODES);
    ck.enum_value("behavior", str_of(a, "behavior"), &BEHAVIORS);
    ck.enum_value(
        "reasoning_strategy",
        str_of(a, "reasoning_strategy"),
        &STRATEGIES,
    );
    ck.range_int("max_turns", int_of(a, "max_turns"), 0, MAX_TURNS_LIMIT);
    if int_of(a, "context_budget") != 0 {
        ck.range_int(
            "context_budget",
            int_of(a, "context_budget"),
            MIN_CONTEXT_BUDGET,
            MAX_CONTEXT_BUDGET,
        );
    }

    // The model the agent runs on, the model it falls back to, the model that summarizes its history.
    ck.model_pair(
        "model_provider",
        "model_name",
        str_of(a, "model_provider"),
        str_of(a, "model_name"),
        true,
        true,
    );
    ck.model_pair(
        "backup_provider",
        "backup_model",
        str_of(a, "backup_provider"),
        str_of(a, "backup_model"),
        true,
        true,
    );
    ck.model_pair(
        "summary_provider",
        "summary_model",
        str_of(a, "summary_provider"),
        str_of(a, "summary_model"),
        false,
        true,
    );

    let skills = strings_of(a, "skills");
    for (i, n) in skills.iter().enumerate() {
        ck.skill(&format!("skills[{i}]"), n, false);
    }
    ck.dupes("skills", &skills);
    let approval = strings_of(a, "approval_tools");
    for (i, n) in approval.iter().enumerate() {
        ck.tool(&format!("approval_tools[{i}]"), n, "tool", false);
    }
    ck.dupes("approval_tools", &approval);
    let subagents = strings_of(a, "subagents");
    for (i, n) in subagents.iter().enumerate() {
        let path = format!("subagents[{i}]");
        if n == name && !name.is_empty() {
            ck.invalid(&path, n, "an agent cannot delegate to itself", vec![]);
            continue;
        }
        ck.agent_ref(&path, n);
    }
    ck.dupes("subagents", &subagents);

    if let Some(b) = present(a, "budget").and_then(Value::as_object) {
        if float_of(b, "max_cost_usd") < 0.0 {
            ck.out_of_range(
                "budget.max_cost_usd",
                float_of(b, "max_cost_usd"),
                0.0,
                1e9,
                false,
            );
        }
        for f in ["max_tokens", "max_active_seconds", "max_turns"] {
            if int_of(b, f) < 0 {
                ck.out_of_range(&format!("budget.{f}"), int_of(b, f) as f64, 0.0, 1e12, true);
            }
        }
        let share = float_of(b, "child_share");
        if !(0.0..=1.0).contains(&share) {
            ck.out_of_range("budget.child_share", share, 0.0, 1.0, false);
        }
    }
    if let Some(bot) = present(a, "bot").and_then(Value::as_object) {
        let tools = strings_of(bot, "tools");
        for (i, n) in tools.iter().enumerate() {
            ck.tool(&format!("bot.tools[{i}]"), n, "tool", false);
        }
        ck.dupes("bot.tools", &tools);
    }

    if let Some(rules) = a.get("guardrails").and_then(Value::as_array) {
        for (i, g) in rules.iter().filter_map(Value::as_object).enumerate() {
            if g.get("enabled").and_then(Value::as_bool) != Some(true) {
                continue; // a rule that is switched off is never evaluated, whatever it says
            }
            let path = format!("guardrails[{i}]");
            ck.enum_value(&format!("{path}.kind"), str_of(g, "kind"), &GUARDRAIL_KINDS);
            ck.enum_value(
                &format!("{path}.stage"),
                str_of(g, "stage"),
                &GUARDRAIL_STAGES,
            );
            ck.enum_value(
                &format!("{path}.action"),
                str_of(g, "action"),
                &GUARDRAIL_ACTIONS,
            );
        }
    }

    ingredients(ck, a);

    // A managed agent that is about to run needs a model somewhere.
    let mode = str_of(a, "mode");
    if ck.o.bake && (mode == "managed" || mode.is_empty()) && !has_model(a) {
        ck.add(Problem {
            path: "model_provider".into(),
            code: code::REQUIRED.into(),
            kind: Kind::Model.as_str().into(),
            message: "model_provider: a managed agent needs a model: set model_provider and model_name, or add a model ingredient".into(),
            ..Default::default()
        });
    }
}

fn has_model(a: &Map) -> bool {
    !str_of(a, "model_provider").is_empty()
        || a.get("ingredients")
            .and_then(Value::as_array)
            .is_some_and(|l| {
                l.iter()
                    .any(|i| i.get("kind").and_then(Value::as_str) == Some("model"))
            })
}

struct EmbeddingFacts {
    provider_name: String,
    kind: String,
    model: String,
}

struct VectorFacts {
    backend: String,
    index: String,
}

/// The ingredients and how they fit together.
fn ingredients(ck: &mut Checker, a: &Map) {
    let list: Vec<&Value> = a
        .get("ingredients")
        .and_then(Value::as_array)
        .map(|l| l.iter().collect())
        .unwrap_or_default();
    let mut seen: Vec<(String, String)> = Vec::new();
    let mut kind_count: std::collections::HashMap<String, usize> = Default::default();
    let mut embeddings: Vec<EmbeddingFacts> = Vec::new();
    let mut vectorstores: Vec<VectorFacts> = Vec::new();
    let mut retrievers: Vec<(String, Map)> = Vec::new();
    let empty = Map::new();

    for (i, raw) in list.iter().enumerate() {
        let ing = raw.as_object().unwrap_or(&empty);
        let path = format!("ingredients[{i}]");
        let kind = str_of(ing, "kind");
        if !contains_str(&INGREDIENT_KINDS, kind) {
            if kind.is_empty() {
                ck.required(&format!("{path}.kind"), "an ingredient needs a kind");
            } else {
                let kinds: Vec<String> = INGREDIENT_KINDS.iter().map(|s| s.to_string()).collect();
                ck.not_found(
                    &format!("{path}.kind"),
                    Kind::IngredientKind,
                    kind,
                    "",
                    &kinds,
                );
            }
            continue;
        }
        let count = kind_count.entry(kind.to_string()).or_insert(0);
        *count += 1;
        let count = *count;
        let ing_name = str_of(ing, "name");
        if !ing_name.is_empty() {
            let key = (kind.to_string(), ing_name.to_string());
            if seen.contains(&key) {
                ck.add(Problem {
                    path: format!("{path}.name"),
                    code: code::DUPLICATE.into(),
                    value: ing_name.into(),
                    message: format!(
                        "{path}.name: there is already a {kind} ingredient named {}",
                        quote(ing_name)
                    ),
                    ..Default::default()
                });
            }
            seen.push(key);
        }
        if (kind == "model" || kind == "prompt") && count == 2 {
            ck.add(Problem {
                path: format!("{path}.kind"),
                code: code::DUPLICATE.into(),
                value: kind.into(),
                message: format!(
                    "{path}.kind: an agent has one {kind} ingredient; this is the second"
                ),
                ..Default::default()
            });
        }
        let cfg: Map = ing
            .get("config")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        let required = ing
            .get("required")
            .and_then(Value::as_bool)
            .unwrap_or(false);
        match kind {
            "model" => model_ingredient(ck, &path, &cfg),
            "tool" => {
                let mut name = ing_name.to_string();
                if let Some(n) = ck.cfg_string(&format!("{path}.config"), &cfg, "tool_name") {
                    if !n.is_empty() {
                        name = n;
                    }
                }
                if name.is_empty() {
                    ck.required(
                        &format!("{path}.name"),
                        "a tool ingredient needs the name of a tool",
                    );
                    continue;
                }
                ck.tool(&tool_path(&path, &cfg), &name, "tool", required);
            }
            "observability" => {
                let mut name = ing_name.to_string();
                if let Some(n) = ck.cfg_string(&format!("{path}.config"), &cfg, "tool_name") {
                    if !n.is_empty() {
                        name = n;
                    }
                }
                if !name.is_empty() {
                    ck.tool(&tool_path(&path, &cfg), &name, "tool", false);
                }
            }
            "prompt" => prompt_ingredient(ck, &path, ing_name, &cfg),
            "data" => {
                let uri = ck.cfg_string(&format!("{path}.config"), &cfg, "uri");
                let wrong_type = present(&cfg, "uri").is_some() && uri.is_none();
                if !wrong_type && uri.unwrap_or_default().trim().is_empty() {
                    ck.required(
                        &format!("{path}.config.uri"),
                        "a data ingredient needs a uri",
                    );
                }
            }
            "embedding" => embeddings.push(embedding_ingredient(ck, &path, &cfg)),
            "vectorstore" => vectorstores.push(vector_store_ingredient(ck, &path, &cfg)),
            "retriever" => {
                retriever_ingredient(ck, &path, &cfg);
                retrievers.push((path.clone(), cfg.clone()));
            }
            "scenario" => scenario_ingredient(ck, &path, ing_name, &cfg),
            "skill" => {
                if ing_name.is_empty() {
                    ck.required(
                        &format!("{path}.name"),
                        "a skill ingredient needs the name of a skill",
                    );
                    continue;
                }
                ck.skill(&format!("{path}.name"), ing_name, required);
            }
            _ => {}
        }
    }

    // A built-in retriever names an embedding and a vector store among the agent's own ingredients.
    for (path, cfg) in &retrievers {
        let provider = str_of(cfg, "provider");
        if !provider.is_empty() && provider != "built-in" {
            continue; // an external RAG provider keeps its own
        }
        let emb_ref = str_of(cfg, "embedding_ref");
        if !emb_ref.is_empty() {
            let names: Vec<String> = embeddings.iter().map(|e| e.provider_name.clone()).collect();
            let found = embeddings.iter().any(|e| {
                e.provider_name == emb_ref
                    || format!("{}/{}", e.kind, e.model) == emb_ref
                    || e.kind == emb_ref
            });
            if !found {
                ck.not_found(
                    &format!("{path}.config.embedding_ref"),
                    Kind::Embedding,
                    emb_ref,
                    "",
                    &super::sorted(names),
                );
            }
        }
        let vs_ref = str_of(cfg, "vectorstore_ref");
        if !vs_ref.is_empty() {
            let found = vectorstores
                .iter()
                .any(|v| v.backend == vs_ref || v.index == vs_ref);
            if !found {
                let names: Vec<String> = vectorstores
                    .iter()
                    .flat_map(|v| [v.backend.clone(), v.index.clone()])
                    .collect();
                ck.not_found(
                    &format!("{path}.config.vectorstore_ref"),
                    Kind::VectorStore,
                    vs_ref,
                    "",
                    &super::sorted(uniq(names)),
                );
            }
        }
    }
}

fn tool_path(path: &str, cfg: &Map) -> String {
    if cfg
        .get("tool_name")
        .and_then(Value::as_str)
        .is_some_and(|n| !n.is_empty())
    {
        format!("{path}.config.tool_name")
    } else {
        format!("{path}.name")
    }
}

fn uniq(ss: Vec<String>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for s in ss {
        if !s.is_empty() && !out.contains(&s) {
            out.push(s);
        }
    }
    out
}

fn model_ingredient(ck: &mut Checker, path: &str, cfg: &Map) {
    let cpath = format!("{path}.config");
    let provider = ck.cfg_string(&cpath, cfg, "provider").unwrap_or_default();
    let model = ck.cfg_string(&cpath, cfg, "model").unwrap_or_default();
    if provider.is_empty() {
        let sev = ck.gap_severity();
        ck.add(Problem {
            path: format!("{cpath}.provider"),
            code: code::REQUIRED.into(),
            severity: sev.into(),
            message: format!(
                "{cpath}.provider: a model ingredient needs the provider that serves it"
            ),
            ..Default::default()
        });
        return;
    }
    let Some(p) = ck.provider(&format!("{cpath}.provider"), &provider) else {
        return;
    };
    if model.is_empty() {
        let sev = ck.gap_severity();
        ck.add(Problem {
            path: format!("{cpath}.model"),
            code: code::REQUIRED.into(),
            severity: sev.into(),
            within: provider.clone(),
            message: format!(
                "{cpath}.model: provider {} is set and needs a model",
                quote(&provider)
            ),
            ..Default::default()
        });
        return;
    }
    ck.model(&format!("{cpath}.model"), &p, &model);
    ck.cfg_float(&cpath, cfg, "temperature", 0.0, 2.0);
    ck.cfg_int(&cpath, cfg, "max_tokens", 1, 10_000_000);
}

fn prompt_ingredient(ck: &mut Checker, path: &str, ing_name: &str, cfg: &Map) {
    let text = ck
        .cfg_string(&format!("{path}.config"), cfg, "text")
        .unwrap_or_default();
    let tmpl = ck
        .cfg_string(&format!("{path}.config"), cfg, "template")
        .unwrap_or_default();
    if !text.is_empty() || !tmpl.is_empty() {
        return; // the prompt is written here, there is nothing to look up
    }
    let mut name = ing_name.to_string();
    if let Some(n) = ck.cfg_string(&format!("{path}.config"), cfg, "prompt_name") {
        if !n.is_empty() {
            name = n;
        }
    }
    let field = if cfg.contains_key("prompt_name") {
        format!("{path}.config.prompt_name")
    } else {
        format!("{path}.name")
    };
    if name.is_empty() {
        ck.required(
            &format!("{path}.name"),
            "a prompt ingredient needs the prompt's name, or the prompt written in config.text",
        );
        return;
    }
    if !ck.c.knows(Kind::Prompt) {
        return;
    }
    let Some(&latest) = ck.c.prompts.get(&name) else {
        let names = ck.c.prompt_names();
        ck.not_found(&field, Kind::Prompt, &name, "", &names);
        return;
    };
    if let Some(v) = present(cfg, "version") {
        let v = v.clone();
        match v.as_f64() {
            Some(f) if f.fract() == 0.0 => {
                if (f as i64) < 1 || (f as i64) > latest {
                    ck.add(Problem {
                        path: format!("{path}.config.version"),
                        code: code::NOT_FOUND.into(),
                        kind: Kind::Prompt.as_str().into(),
                        value: super::trim_float(f),
                        within: name.clone(),
                        message: format!(
                            "{path}.config.version: prompt {} has versions 1 to {latest}, not {}",
                            quote(&name),
                            super::trim_float(f)
                        ),
                        ..Default::default()
                    });
                }
            }
            _ => ck.wrong_type(&format!("{path}.config.version"), "a whole number", &v),
        }
    }
}

fn embedding_ingredient(ck: &mut Checker, path: &str, cfg: &Map) -> EmbeddingFacts {
    let cpath = format!("{path}.config");
    let provider = ck.cfg_string(&cpath, cfg, "provider").unwrap_or_default();
    let model = ck.cfg_string(&cpath, cfg, "model").unwrap_or_default();
    let mut facts = EmbeddingFacts {
        provider_name: provider.clone(),
        kind: String::new(),
        model,
    };
    ck.cfg_int(&cpath, cfg, "dimensions", 1, MAX_EMBEDDING_DIMS);
    ck.cfg_int(&cpath, cfg, "batch_size", 1, MAX_EMBEDDING_BATCH);
    if provider.is_empty() {
        ck.add(Problem {
            path: format!("{cpath}.provider"),
            code: code::REQUIRED.into(),
            message: format!(
                "{cpath}.provider: an embedding ingredient needs the provider that serves it"
            ),
            ..Default::default()
        });
        return facts;
    }
    if let Some(p) = ck.provider(&format!("{cpath}.provider"), &provider) {
        facts.kind = p.kind;
    }
    facts
}

fn vector_store_ingredient(ck: &mut Checker, path: &str, cfg: &Map) -> VectorFacts {
    let cpath = format!("{path}.config");
    let mut backend = ck.cfg_string(&cpath, cfg, "backend").unwrap_or_default();
    let mut index = ck.cfg_string(&cpath, cfg, "index").unwrap_or_default();
    ck.cfg_int(&cpath, cfg, "dimensions", 1, MAX_EMBEDDING_DIMS);
    if backend.is_empty() {
        backend = "embedded".into();
    }
    if !contains_str(&VECTOR_BACKENDS, &backend) {
        let kinds: Vec<String> = VECTOR_BACKENDS.iter().map(|s| s.to_string()).collect();
        ck.not_found(
            &format!("{cpath}.backend"),
            Kind::Backend,
            &backend,
            "",
            &kinds,
        );
    }
    if index.is_empty() && backend != "embedded" {
        ck.required(
            &format!("{cpath}.index"),
            "a vector store other than the embedded one needs an index",
        );
    }
    if index.is_empty() {
        index = "default".into();
    }
    VectorFacts { backend, index }
}

fn retriever_ingredient(ck: &mut Checker, path: &str, cfg: &Map) {
    let cpath = format!("{path}.config");
    ck.cfg_int(&cpath, cfg, "top_k", 1, MAX_TOP_K);
    ck.cfg_float(&cpath, cfg, "score_threshold", 0.0, 1.0);
    if let Some(v) = present(cfg, "hybrid_search") {
        if !v.is_boolean() {
            let v = v.clone();
            ck.wrong_type(&format!("{cpath}.hybrid_search"), "true or false", &v);
        }
    }
}

fn scenario_ingredient(ck: &mut Checker, path: &str, ing_name: &str, cfg: &Map) {
    let cpath = format!("{path}.config");
    let mut scenario = ing_name.to_string();
    if let Some(s) = ck.cfg_string(&cpath, cfg, "scenario") {
        if !s.is_empty() {
            scenario = s;
        }
    }
    ck.cfg_int(&cpath, cfg, "rollouts", 1, MAX_SCENARIO_ROLLOUT);
    ck.cfg_float(&cpath, cfg, "min_pass_rate", 0.0, 1.0);
    if scenario.is_empty() {
        ck.required(
            &format!("{cpath}.scenario"),
            "a scenario ingredient needs the id of a scenario",
        );
        return;
    }
    if ck.c.scenarios.known && !contains(&ck.c.scenarios.names, &scenario) {
        let field = if cfg.contains_key("scenario") {
            format!("{cpath}.scenario")
        } else {
            format!("{path}.name")
        };
        let names = super::sorted(ck.c.scenarios.names.clone());
        ck.not_found(&field, Kind::Scenario, &scenario, "", &names);
    }
}

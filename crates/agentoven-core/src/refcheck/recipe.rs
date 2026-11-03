//! The checks of a recipe (the Rust twin of `pkg/refcheck/recipe.go`): the agents its steps run, the steps they
//! wait for, the recipes it nests, the channels it notifies, the credentials it signs in with, the roles that may
//! approve its gates, the environment it runs in.

use serde_json::Value;

use super::{
    code, contains, contains_str, int_of, present, quote, sorted, str_of, strings_of, Catalog,
    Checker, Kind, Map, Options, Problem,
};

const MAX_STEP_RETRIES: i64 = 20;
const MAX_STEP_TIMEOUT: i64 = 7 * 24 * 3600;
const MAX_STEP_ITERATIONS: i64 = 10_000;
const MAX_STEP_CONCURRENT: i64 = 1000;

const STEP_KINDS: [&str; 12] = [
    "agent",
    "human_gate",
    "evaluator",
    "condition",
    "fan_out",
    "fan_in",
    "rag",
    "router",
    "map",
    "sub_recipe",
    "wait",
    "notify",
];

/// Check a recipe, as the JSON the API takes, against what the kitchen has.
pub fn check_recipe(recipe: &Value, catalog: &Catalog, options: &Options) -> Vec<Problem> {
    let empty = Map::new();
    let r = recipe.as_object().unwrap_or(&empty);
    let mut ck = Checker::new(catalog, options);
    check(&mut ck, r);
    ck.out
}

fn check(ck: &mut Checker, r: &Map) {
    let (c, o) = (ck.c, ck.o);
    let default_env = str_of(r, "default_environment");
    if !default_env.is_empty() {
        ck.from_set(
            "default_environment",
            Kind::Environment,
            default_env,
            &c.environments,
        );
    }
    if !o.environment.is_empty() {
        ck.from_set(
            "environment",
            Kind::Environment,
            &o.environment,
            &c.environments,
        );
    }

    let empty = Map::new();
    let steps: Vec<&Map> = r
        .get("steps")
        .and_then(Value::as_array)
        .map(|l| l.iter().map(|s| s.as_object().unwrap_or(&empty)).collect())
        .unwrap_or_default();
    let mut names: Vec<String> = Vec::new();
    for (i, s) in steps.iter().enumerate() {
        let path = format!("steps[{i}]");
        let name = str_of(s, "name");
        if name.is_empty() {
            ck.required(&format!("{path}.name"), "a step needs a name");
        } else if names.iter().any(|n| n == name) {
            ck.add(Problem {
                path: format!("{path}.name"),
                code: code::DUPLICATE.into(),
                value: name.into(),
                message: format!("{path}.name: there is already a step named {}", quote(name)),
                ..Default::default()
            });
        } else {
            names.push(name.to_string());
        }
    }
    let sorted_names = sorted(names.clone());

    let recipe_name = str_of(r, "name");
    for (i, s) in steps.iter().enumerate() {
        step(ck, &format!("steps[{i}]"), s, recipe_name, &sorted_names);
    }
    cycles(ck, &steps);
}

fn step(ck: &mut Checker, path: &str, s: &Map, recipe_name: &str, step_names: &[String]) {
    let kind = str_of(s, "kind");
    if kind.is_empty() {
        ck.required(&format!("{path}.kind"), "a step needs a kind");
    } else if !contains_str(&STEP_KINDS, kind) {
        let kinds: Vec<String> = STEP_KINDS.iter().map(|k| k.to_string()).collect();
        ck.not_found(&format!("{path}.kind"), Kind::StepKind, kind, "", &kinds);
    }

    // The agent a step runs. "agent" is the client-facing alias of "agent_ref": when both are given they must
    // agree, or one of them is the typo.
    let agent_ref = str_of(s, "agent_ref");
    let agent = str_of(s, "agent");
    if !agent.is_empty() && !agent_ref.is_empty() && agent != agent_ref {
        ck.invalid(
            &format!("{path}.agent"),
            agent,
            &format!("disagrees with agent_ref {}", quote(agent_ref)),
            vec![],
        );
    }
    let target = if agent_ref.is_empty() {
        agent
    } else {
        agent_ref
    };
    let ref_path = if agent_ref.is_empty() {
        format!("{path}.agent")
    } else {
        format!("{path}.agent_ref")
    };
    if !target.is_empty() {
        ck.agent_ref(&ref_path, target);
    } else if kind == "agent" || kind == "map" {
        ck.required(
            &format!("{path}.agent_ref"),
            &format!("a {kind} step needs the agent to run"),
        );
    }

    if kind == "sub_recipe" {
        let recipe_ref = str_of(s, "recipe_ref");
        if recipe_ref.is_empty() {
            ck.required(
                &format!("{path}.recipe_ref"),
                "a sub_recipe step needs the recipe to run",
            );
        } else if recipe_ref == recipe_name && !recipe_name.is_empty() {
            // A recipe nesting itself exists once it is saved, and the server refuses the loop with the path it
            // makes; it is not "missing" here.
        } else {
            ck.from_set(
                &format!("{path}.recipe_ref"),
                Kind::Recipe,
                recipe_ref,
                &ck.c.recipes,
            );
        }
    }
    if kind == "map" && str_of(s, "source_path").is_empty() {
        ck.required(
            &format!("{path}.source_path"),
            "a map step needs the path of the list it runs over",
        );
    }

    let name = str_of(s, "name");
    let depends = strings_of(s, "depends_on");
    for (j, dep) in depends.iter().enumerate() {
        let p = format!("{path}.depends_on[{j}]");
        if dep == name && !name.is_empty() {
            ck.invalid(&p, dep, "a step cannot wait for itself", vec![]);
        } else if !contains(step_names, dep) {
            ck.not_found(&p, Kind::Step, dep, "", step_names);
        }
    }
    ck.dupes(&format!("{path}.depends_on"), &depends);
    if let Some(branches) = s.get("branches").and_then(Value::as_array) {
        for (j, b) in branches.iter().filter_map(Value::as_object).enumerate() {
            let next = str_of(b, "next_step");
            if !next.is_empty() && !contains(step_names, next) {
                ck.not_found(
                    &format!("{path}.branches[{j}].next_step"),
                    Kind::Step,
                    next,
                    "",
                    step_names,
                );
            }
        }
    }
    let default_next = str_of(s, "default_next");
    if !default_next.is_empty() && !contains(step_names, default_next) {
        ck.not_found(
            &format!("{path}.default_next"),
            Kind::Step,
            default_next,
            "",
            step_names,
        );
    }
    let config: Map = s
        .get("config")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    let question_from = str_of(&config, "question_from");
    if !question_from.is_empty() && !contains(step_names, question_from) {
        ck.not_found(
            &format!("{path}.config.question_from"),
            Kind::Step,
            question_from,
            "",
            step_names,
        );
    }

    ck.range_int(
        &format!("{path}.max_retries"),
        int_of(s, "max_retries"),
        0,
        MAX_STEP_RETRIES,
    );
    ck.range_int(
        &format!("{path}.timeout_secs"),
        int_of(s, "timeout_secs"),
        0,
        MAX_STEP_TIMEOUT,
    );
    ck.range_int(
        &format!("{path}.max_iterations"),
        int_of(s, "max_iterations"),
        0,
        MAX_STEP_ITERATIONS,
    );
    ck.range_int(
        &format!("{path}.max_concurrency"),
        int_of(s, "max_concurrency"),
        0,
        MAX_STEP_CONCURRENT,
    );
    if !str_of(s, "loop_condition").is_empty() && int_of(s, "max_iterations") <= 0 {
        ck.required(
            &format!("{path}.max_iterations"),
            "a loop_condition needs max_iterations, the hard cap on the loop",
        );
    }

    // Who is told, and who may decide.
    let notify = strings_of(s, "notify_tools");
    for (j, t) in notify.iter().enumerate() {
        ck.tool(&format!("{path}.notify_tools[{j}]"), t, "notify", true);
    }
    ck.dupes(&format!("{path}.notify_tools"), &notify);
    if kind == "notify" {
        channels(ck, path, &config);
    }
    let auth_key_ref = str_of(s, "auth_key_ref");
    if !auth_key_ref.is_empty() {
        ck.from_set(
            &format!("{path}.auth_key_ref"),
            Kind::Credential,
            auth_key_ref,
            &ck.c.credentials,
        );
    }
    let approver_roles = strings_of(s, "approver_roles");
    for (j, role) in approver_roles.iter().enumerate() {
        let known = ck.c.roles.known;
        let listed =
            ck.c.roles
                .names
                .iter()
                .any(|n| n.trim().to_lowercase() == role.trim().to_lowercase());
        if known && !listed && role.to_lowercase() != "admin" {
            let names = sorted(ck.c.roles.names.clone());
            ck.not_found(
                &format!("{path}.approver_roles[{j}]"),
                Kind::Role,
                role,
                "",
                &names,
            );
        }
    }
    // A role is the same role in any case.
    let lowered: Vec<String> = approver_roles
        .iter()
        .map(|r| r.trim().to_lowercase())
        .collect();
    ck.dupes(&format!("{path}.approver_roles"), &lowered);
    let emails = strings_of(s, "approver_emails");
    for (j, e) in emails.iter().enumerate() {
        let bad = match e.find('@') {
            None | Some(0) => true,
            Some(at) => at == e.len() - 1 || e.contains([' ', ',', ';']),
        };
        if bad {
            ck.invalid(
                &format!("{path}.approver_emails[{j}]"),
                e,
                "is not an e-mail address",
                vec![],
            );
        }
    }
    ck.dupes(&format!("{path}.approver_emails"), &emails);
    let domain = str_of(s, "approver_domain");
    if domain.contains('@') {
        ck.invalid(
            &format!("{path}.approver_domain"),
            domain,
            r#"is a domain such as "acme.com", without "@""#,
            vec![],
        );
    }
}

/// The channels a notify step sends to (`config.channels`).
fn channels(ck: &mut Checker, path: &str, config: &Map) {
    let Some(raw) = present(config, "channels") else {
        return;
    };
    let Some(items) = raw.as_array() else {
        let raw = raw.clone();
        ck.wrong_type(
            &format!("{path}.config.channels"),
            "a list of channel names",
            &raw,
        );
        return;
    };
    let mut list: Vec<String> = Vec::new();
    for (j, c) in items.iter().enumerate() {
        match c.as_str() {
            Some(name) => list.push(name.to_string()),
            None => ck.wrong_type(&format!("{path}.config.channels[{j}]"), "a channel name", c),
        }
    }
    for (j, name) in list.iter().enumerate() {
        if name.trim().is_empty() {
            continue;
        }
        ck.from_set(
            &format!("{path}.config.channels[{j}]"),
            Kind::Channel,
            name.trim(),
            &ck.c.channels,
        );
    }
    ck.dupes(&format!("{path}.config.channels"), &list);
}

/// A loop in the steps' dependencies, reported once, at the step where the loop is found.
fn cycles(ck: &mut Checker, steps: &[&Map]) {
    #[derive(Clone, Copy, PartialEq)]
    enum State {
        Unseen,
        Walking,
        Done,
    }
    // The first step of each name, as the server's index has it.
    let index = |name: &str| {
        if name.is_empty() {
            None
        } else {
            steps.iter().position(|s| str_of(s, "name") == name)
        }
    };

    fn visit(
        i: usize,
        steps: &[&Map],
        state: &mut Vec<State>,
        stack: &mut Vec<String>,
        reported: &mut bool,
        ck: &mut Checker,
        index: &dyn Fn(&str) -> Option<usize>,
    ) {
        if *reported {
            return;
        }
        state[i] = State::Walking;
        stack.push(str_of(steps[i], "name").to_string());
        for dep in strings_of(steps[i], "depends_on") {
            let Some(j) = index(&dep) else { continue };
            if dep == str_of(steps[i], "name") {
                continue;
            }
            if state[j] == State::Walking {
                let mut lp: Vec<String> =
                    stack.iter().skip_while(|n| **n != dep).cloned().collect();
                lp.push(dep.clone());
                ck.add(Problem {
                    path: format!("steps[{i}].depends_on"),
                    code: code::INVALID_VALUE.into(),
                    value: dep.clone(),
                    message: format!(
                        "steps[{i}].depends_on: the steps wait for each other in a circle: {}",
                        lp.join(" -> ")
                    ),
                    ..Default::default()
                });
                *reported = true;
                return;
            }
            if state[j] == State::Unseen {
                visit(j, steps, state, stack, reported, ck, index);
            }
        }
        stack.pop();
        state[i] = State::Done;
    }

    let mut state = vec![State::Unseen; steps.len()];
    let mut stack = Vec::new();
    let mut reported = false;
    for i in 0..steps.len() {
        if state[i] == State::Unseen && !str_of(steps[i], "name").is_empty() {
            visit(i, steps, &mut state, &mut stack, &mut reported, ck, &index);
        }
    }
}

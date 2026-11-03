//! The Rust checks give the answers of the control plane's `pkg/refcheck`.
//!
//! `sdk/fixtures/refcheck-cases.json` holds catalogs, agent and recipe inputs and the problems each must
//! produce; the Go test, the Python and TypeScript SDKs and this one all run the same file, so the four
//! implementations cannot drift apart. A problem is identified by (path, code, severity, kind, value, within)
//! and the two sides are compared as sorted multisets; `suggestions` are compared where the case lists them.

use std::collections::BTreeMap;
use std::fs;
use std::path::Path;

use agentoven_core::refcheck::{check_agent, check_recipe, nearest, Catalog, Options, Problem};
use serde_json::Value;

fn fixture() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../sdk/fixtures/refcheck-cases.json");
    serde_json::from_str(&fs::read_to_string(&path).expect("sdk/fixtures/refcheck-cases.json"))
        .expect("the fixture is JSON")
}

fn key(p: &Problem) -> String {
    format!(
        "{}|{}|{}|{}|{}|{}",
        p.path, p.code, p.severity, p.kind, p.value, p.within
    )
}

fn keys(ps: &[Problem]) -> Vec<String> {
    let mut k: Vec<String> = ps.iter().map(key).collect();
    k.sort();
    k
}

#[test]
fn every_case_of_the_shared_fixture_gives_the_same_answer() {
    let f = fixture();
    let catalogs: BTreeMap<String, Catalog> =
        serde_json::from_value(f["catalogs"].clone()).expect("the catalogs");
    let cases = f["cases"].as_array().expect("cases");
    assert!(
        cases.len() >= 71,
        "the fixture shrank to {} cases",
        cases.len()
    );
    let (mut nearest_cases, mut agent_cases, mut recipe_cases) = (0, 0, 0);
    for case in cases {
        let name = case["name"].as_str().unwrap();
        let kind = case["kind"].as_str().unwrap();
        if kind == "nearest" {
            nearest_cases += 1;
            let candidates: Vec<String> =
                serde_json::from_value(case["candidates"].clone()).unwrap();
            let want: Vec<String> = serde_json::from_value(case["expect"].clone()).unwrap();
            assert_eq!(
                nearest(case["value"].as_str().unwrap(), &candidates, 3),
                want,
                "{name}"
            );
            continue;
        }
        let want_raw = case["expect"]
            .as_array()
            .unwrap_or_else(|| panic!("{name}: expect is not a list"));
        let want: Vec<Problem> = serde_json::from_value(case["expect"].clone())
            .unwrap_or_else(|e| panic!("{name}: {e}"));
        let catalog = &catalogs[case["catalog"].as_str().unwrap()];
        let opts = Options {
            bake: case["options"]["bake"].as_bool().unwrap_or(false),
            environment: case["options"]["environment"]
                .as_str()
                .unwrap_or_default()
                .to_string(),
        };
        let got = match kind {
            "agent" => {
                agent_cases += 1;
                check_agent(&case["input"], catalog, &opts)
            }
            "recipe" => {
                recipe_cases += 1;
                check_recipe(&case["input"], catalog, &opts)
            }
            other => panic!("{name}: unknown case kind {other}"),
        };
        let messages: Vec<&str> = got.iter().map(|p| p.message.as_str()).collect();
        assert_eq!(keys(&got), keys(&want), "{name}\n  messages: {messages:#?}");
        // Suggestions are compared where the case lists them.
        let by_key: BTreeMap<String, &Problem> = got.iter().map(|p| (key(p), p)).collect();
        for (i, w) in want.iter().enumerate() {
            if want_raw[i].get("suggestions").is_some() {
                assert_eq!(
                    by_key[&key(w)].suggestions,
                    w.suggestions,
                    "{name}: suggestions for {}",
                    w.path
                );
            }
        }
        for p in &got {
            assert!(!p.message.is_empty(), "{name}: {} has no message", p.path);
        }
    }
    // A runner that skipped a kind of case would pass for the wrong reason.
    assert!(
        nearest_cases >= 5 && agent_cases >= 40 && recipe_cases >= 15,
        "ran {nearest_cases} nearest, {agent_cases} agent, {recipe_cases} recipe cases"
    );
}

//! How the CLI shows what the control plane (or `--validate`) found wrong with a configuration: every problem
//! with its field, the value that is wrong and the nearest values that are right, instead of a JSON blob.

use agentoven_core::refcheck::{ConfigurationError, Problem, Validation};
use colored::Colorize;

/// One problem as lines: the field and the bad value, the sentence, and what was probably meant.
fn problem_lines(p: &Problem, mark: &str) -> String {
    let mut head = format!("  {mark} {}", p.path.bold());
    if !p.value.is_empty() {
        head += &format!(" = {}", format!("{:?}", p.value).cyan());
    }
    if !p.kind.is_empty() {
        head += &format!(" {}", format!("[{}]", p.kind).dimmed());
    }
    // The message starts with the path ("model_name: ..."): the head already says it.
    let said = p
        .message
        .strip_prefix(&format!("{}: ", p.path))
        .unwrap_or(&p.message);
    let mut out = format!("{head}\n      {said}\n");
    if !p.suggestions.is_empty() && !said.contains("did you mean") {
        out += &format!("      did you mean: {}\n", p.suggestions.join(", ").green());
    }
    out
}

/// The problems that refuse the configuration, then the warnings, as text.
pub fn render_problems(problems: &[Problem], warnings: &[Problem]) -> String {
    let mut out = String::new();
    for p in problems {
        out += &problem_lines(p, &"✗".red().bold().to_string());
    }
    for w in warnings {
        out += &problem_lines(w, &"⚠".yellow().bold().to_string());
    }
    out
}

/// The report of `agent validate` / `recipe validate` and the exit code (1 when anything refuses).
pub fn render_validation(what: &str, v: &Validation) -> (String, i32) {
    let name = v.subject.bold();
    let mut out = if v.valid {
        let note = if v.warnings.is_empty() {
            String::new()
        } else {
            format!(" ({} warning(s))", v.warnings.len())
        };
        format!(
            "\n  {} {what} {name} is valid{note}\n\n",
            "✓".green().bold()
        )
    } else {
        format!(
            "\n  {} {what} {name} has {} problem(s) that would refuse it:\n\n",
            "✗".red().bold(),
            v.problems.len()
        )
    };
    out += &render_problems(&v.problems, &v.warnings);
    (out, i32::from(!v.valid))
}

/// What `--validate` does with the outcome of a check made before sending: print the warnings and go on, or
/// print the problems and end the command with a failure (nothing is sent).
pub fn gate(v: Validation) -> anyhow::Result<()> {
    if v.valid {
        if !v.warnings.is_empty() {
            print!("{}", render_problems(&[], &v.warnings));
        }
        return Ok(());
    }
    let err: anyhow::Error = v
        .into_result()
        .expect_err("an invalid verdict is an error")
        .into();
    Err(refused(&err).expect("a configuration error is a refusal"))
}

/// When `err` is the control plane's (or the client's own) refusal of a configuration, print every problem and
/// return the one-line error that ends the command with a failure status; `None` for any other error.
pub fn refused(err: &anyhow::Error) -> Option<anyhow::Error> {
    let c = err.downcast_ref::<ConfigurationError>()?;
    let who = if c.status == 0 {
        "the check before sending found"
    } else {
        "the control plane refused"
    };
    println!("\n  {} {} the configuration:\n", "✗".red().bold(), who);
    print!("{}", render_problems(&c.problems, &c.warnings));
    println!();
    Some(anyhow::anyhow!(
        "the configuration names things this kitchen does not have ({} problem(s))",
        c.problems.len()
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn problem() -> Problem {
        Problem {
            path: "summary_model".into(),
            code: "not_found".into(),
            severity: "error".into(),
            kind: "model".into(),
            value: "claude-hiku".into(),
            message: "summary_model: \"claude-hiku\" is not a model of provider \"anthropic\""
                .into(),
            suggestions: vec!["claude-haiku-4-5".into()],
            ..Default::default()
        }
    }

    #[test]
    fn a_problem_shows_its_field_the_bad_value_and_what_was_meant() {
        colored::control::set_override(false);
        let text = render_problems(&[problem()], &[]);
        assert!(
            text.contains("✗ summary_model = \"claude-hiku\" [model]"),
            "{text}"
        );
        assert!(text.contains("is not a model of provider"), "{text}");
        assert!(text.contains("did you mean: claude-haiku-4-5"), "{text}");
        // The path is said once, in the head.
        assert!(!text.contains("summary_model: "), "{text}");
    }

    #[test]
    fn a_message_that_already_says_did_you_mean_is_not_repeated() {
        colored::control::set_override(false);
        let mut p = problem();
        p.message =
            "summary_model: \"claude-hiku\" is not a model (did you mean \"claude-haiku-4-5\"?)"
                .into();
        let text = render_problems(&[p], &[]);
        assert_eq!(text.matches("did you mean").count(), 1, "{text}");
    }

    #[test]
    fn a_valid_report_exits_zero_and_an_invalid_one_exits_one() {
        colored::control::set_override(false);
        let ok = Validation {
            subject: "bot".into(),
            valid: true,
            ..Default::default()
        };
        assert_eq!(render_validation("agent", &ok).1, 0);
        let mut warn = problem();
        warn.severity = "warning".into();
        let ok = Validation {
            subject: "bot".into(),
            valid: true,
            warnings: vec![warn],
            ..Default::default()
        };
        let (text, code) = render_validation("agent", &ok);
        assert_eq!(code, 0);
        assert!(
            text.contains("is valid (1 warning(s))") && text.contains("⚠ summary_model"),
            "{text}"
        );
        let bad = Validation {
            subject: "bot".into(),
            valid: false,
            problems: vec![problem()],
            ..Default::default()
        };
        let (text, code) = render_validation("agent", &bad);
        assert_eq!(code, 1);
        assert!(text.contains("has 1 problem(s)"), "{text}");
    }

    #[test]
    fn the_gate_lets_a_valid_configuration_through_and_stops_an_invalid_one() {
        colored::control::set_override(false);
        let mut warn = problem();
        warn.severity = "warning".into();
        assert!(gate(Validation {
            valid: true,
            warnings: vec![warn],
            ..Default::default()
        })
        .is_ok());
        let err = gate(Validation {
            subject: "bot".into(),
            valid: false,
            problems: vec![problem()],
            ..Default::default()
        })
        .unwrap_err();
        assert!(err.to_string().contains("1 problem"), "{err}");
    }

    #[test]
    fn only_a_configuration_error_is_a_refusal() {
        colored::control::set_override(false);
        assert!(refused(&anyhow::anyhow!("connection refused")).is_none());
        let e: anyhow::Error = ConfigurationError {
            status: 422,
            message: "m".into(),
            problems: vec![problem()],
            warnings: vec![],
        }
        .into();
        let end = refused(&e).expect("a refusal");
        assert!(end.to_string().contains("1 problem"), "{end}");
    }
}

//! The "did you mean" rule: the same one the control plane's `pkg/refcheck/nearest.go` applies, pinned by the
//! `nearest` cases of `sdk/fixtures/refcheck-cases.json`.

/// How many nearest values a problem carries.
pub const MAX_SUGGESTIONS: usize = 3;

/// The candidates closest to `value`, nearest first, at most `limit` of them. A candidate is close when the
/// two differ by a few characters (a typo), when they are the same once case and punctuation are ignored
/// (`gpt4o` and `gpt-4o`), or when one holds the other (`haiku` in `claude-haiku-4-5`).
///
/// The distance is optimal string alignment (an adjacent swap is one edit), taken on the lowercased text and
/// on the text with everything but letters and digits removed, whichever is smaller. A candidate is close when
/// the distance is at most `clamp(len(squashed value) / 4, 1, 3)`; one that merely contains the other (both at
/// least three characters squashed) counts as exactly that limit, so it ranks after the nearer typos. Ties
/// sort by name.
pub fn nearest(value: &str, candidates: &[String], limit: usize) -> Vec<String> {
    let v = value.to_lowercase();
    let vn = squash(&v);
    if vn.is_empty() {
        return Vec::new();
    }
    let vn_len = vn.chars().count();
    let allowed = (vn_len / 4).clamp(1, 3);
    let mut seen: Vec<&str> = Vec::new();
    let mut close: Vec<(usize, &str)> = Vec::new();
    for c in candidates {
        if seen.contains(&c.as_str()) {
            continue;
        }
        seen.push(c);
        let cl = c.to_lowercase();
        let cn = squash(&cl);
        if cn.is_empty() {
            continue;
        }
        let mut d = distance(&v, &cl).min(distance(&vn, &cn));
        if d > allowed
            && vn_len >= 3
            && cn.chars().count() >= 3
            && (cn.contains(&vn) || vn.contains(&cn))
        {
            d = allowed; // contained: close, ranked after the typos that are nearer
        }
        if d <= allowed {
            close.push((d, c));
        }
    }
    close.sort_by(|a, b| a.0.cmp(&b.0).then_with(|| a.1.cmp(b.1)));
    let take = if limit == 0 {
        close.len()
    } else {
        limit.min(close.len())
    };
    close
        .into_iter()
        .take(take)
        .map(|(_, n)| n.to_string())
        .collect()
}

/// Letters and digits only.
fn squash(s: &str) -> String {
    s.chars()
        .filter(|c| c.is_alphabetic() || c.is_numeric())
        .collect()
}

/// Edit distance with adjacent swaps counted as one edit (optimal string alignment): the commonest typo,
/// "dokcer", is one slip.
fn distance(a: &str, b: &str) -> usize {
    let (a, b): (Vec<char>, Vec<char>) = (a.chars().collect(), b.chars().collect());
    let mut d = vec![vec![0usize; b.len() + 1]; a.len() + 1];
    for (i, row) in d.iter_mut().enumerate() {
        row[0] = i;
    }
    for j in 0..=b.len() {
        d[0][j] = j;
    }
    for i in 1..=a.len() {
        for j in 1..=b.len() {
            let cost = usize::from(a[i - 1] != b[j - 1]);
            d[i][j] = (d[i][j - 1] + 1)
                .min(d[i - 1][j] + 1)
                .min(d[i - 1][j - 1] + cost);
            if i > 1 && j > 1 && a[i - 1] == b[j - 2] && a[i - 2] == b[j - 1] {
                d[i][j] = d[i][j].min(d[i - 2][j - 2] + 1);
            }
        }
    }
    d[a.len()][b.len()]
}

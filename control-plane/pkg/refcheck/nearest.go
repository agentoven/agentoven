package refcheck

import (
	"sort"
	"strings"
	"unicode"
)

// maxSuggestions is how many nearest values a problem carries.
const maxSuggestions = 3

// Nearest returns the candidates closest to value, nearest first, at most limit of them. A candidate is
// close when the two differ by a few characters (a typo), when they are the same once case and
// punctuation are ignored ("gpt4o" and "gpt-4o"), or when one holds the other ("haiku" in
// "claude-haiku-4-5"). It is the rule behind every "did you mean": the SDKs implement the same one, and
// the fixture pins them together.
func Nearest(value string, candidates []string, limit int) []string {
	v := strings.ToLower(value)
	vn := squash(v)
	if vn == "" {
		return nil
	}
	type scored struct {
		name string
		dist int
	}
	var close []scored
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		cl := strings.ToLower(c)
		cn := squash(cl)
		if cn == "" {
			continue
		}
		d := minInt(levenshtein(v, cl), levenshtein(vn, cn))
		allowed := len([]rune(vn)) / 4
		if allowed < 1 {
			allowed = 1
		}
		if allowed > 3 {
			allowed = 3
		}
		if d > allowed && len([]rune(vn)) >= 3 && len([]rune(cn)) >= 3 && (strings.Contains(cn, vn) || strings.Contains(vn, cn)) {
			d = allowed // contained: close, ranked after the typos that are nearer
		}
		if d <= allowed {
			close = append(close, scored{c, d})
		}
	}
	sort.SliceStable(close, func(i, j int) bool {
		if close[i].dist != close[j].dist {
			return close[i].dist < close[j].dist
		}
		return close[i].name < close[j].name
	})
	if limit <= 0 || limit > len(close) {
		limit = len(close)
	}
	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = close[i].name
	}
	return out
}

// squash keeps letters and digits only.
func squash(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// levenshtein is the edit distance with adjacent swaps counted as one edit (optimal string alignment): the
// commonest typo, "dokcer", is one slip.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := 0; j <= len(rb); j++ {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = minInt(minInt(d[i][j-1]+1, d[i-1][j]+1), d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = minInt(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

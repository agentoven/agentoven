package documents

import "strings"

// maxEditTokens bounds the word-level edit distance, which costs the product of the two lengths.
const maxEditTokens = 4000

// Agreement is how alike two transcriptions are, from 0 to 1, comparing words (case, punctuation
// spacing and line breaks are ignored). Two pages agree when one can be turned into the other by few
// word edits: 1 - edits / words in the longer one. Past maxEditTokens words it falls back to the
// overlap of the two bags of words, which does not see reordering.
func Agreement(a, b string) float64 {
	x, y := words(a), words(b)
	if len(x) == 0 && len(y) == 0 {
		return 1
	}
	longer := max(len(x), len(y))
	if len(x) > maxEditTokens || len(y) > maxEditTokens {
		return bagOverlap(x, y)
	}
	return 1 - float64(editDistance(x, y))/float64(longer)
}

func words(s string) []string { return strings.Fields(strings.ToLower(s)) }

func editDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// bagOverlap is the Dice coefficient of the two multisets of words.
func bagOverlap(a, b []string) float64 {
	counts := map[string]int{}
	for _, w := range a {
		counts[w]++
	}
	shared := 0
	for _, w := range b {
		if counts[w] > 0 {
			counts[w]--
			shared++
		}
	}
	return 2 * float64(shared) / float64(len(a)+len(b))
}

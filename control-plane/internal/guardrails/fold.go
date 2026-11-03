package guardrails

// Text folding for the span detectors (ADR-0049 "Not done": the detectors matched raw text).
//
// A pattern for a card number, an IBAN or a key matches ASCII. Text a model or a caller controls
// does not have to be ASCII: a zero-width space inside the number, full-width digits, a no-break
// space between the groups or a combining mark on a digit all read as the same value to a person
// or a model and as something else to a regexp. The detectors therefore run on a folded copy of
// the text, and a finding is mapped back to the byte range of the original, which is what gets
// redacted: the caller still sees (and the stores still hold) the original wording around it.

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// folded is text prepared for matching. When the text is ASCII, fold returns it unchanged and the
// offset tables are nil (a byte of the folded text is the same byte of the original).
type folded struct {
	text string
	// ostart and oend say, for every byte of text, the byte range of the original that produced it
	// (a cluster of one base character and its combining marks: normalisation is not per rune).
	ostart, oend []int32
}

// isASCII is whether s needs no folding.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// foldRune maps what NFKC leaves different from what a reader sees: dashes become a hyphen (a
// number written with U+2011 or U+2212 is the same number) and every space or line separator a plain
// space. ok is false for characters that carry no reading at all (zero-width, bidi controls, the
// soft hyphen, the byte-order mark: category Cf), which are dropped.
func foldRune(r rune) (out rune, ok bool) {
	switch {
	case r >= 0x2010 && r <= 0x2015, r == 0x2212:
		return '-', true
	case unicode.Is(unicode.Cf, r):
		return 0, false
	case unicode.Is(unicode.Z, r):
		return ' ', true
	}
	return r, true
}

// fold prepares text for the detectors.
func fold(s string) *folded {
	if isASCII(s) {
		return &folded{text: s}
	}
	var sb strings.Builder
	sb.Grow(len(s))
	ostart := make([]int32, 0, len(s))
	oend := make([]int32, 0, len(s))
	for i := 0; i < len(s); {
		// A cluster is a character with the combining marks that follow it.
		j := i
		for first := true; j < len(s); first = false {
			r, size := utf8.DecodeRuneInString(s[j:])
			if !first && !unicode.Is(unicode.M, r) {
				break
			}
			j += size
		}
		cluster := s[i:j]
		if !isASCII(cluster) {
			cluster = norm.NFKC.String(cluster)
		}
		base := true // the first rune of the cluster; a mark after an ASCII letter or digit is dropped
		dropMarks := false
		for _, r := range cluster {
			if unicode.Is(unicode.M, r) && dropMarks {
				continue
			}
			out, ok := foldRune(r)
			if !ok {
				continue
			}
			if base {
				dropMarks = out < utf8.RuneSelf && (unicode.IsLetter(out) || unicode.IsDigit(out))
				base = false
			}
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], out)
			sb.Write(buf[:n])
			for k := 0; k < n; k++ {
				ostart = append(ostart, int32(i))
				oend = append(oend, int32(j))
			}
		}
		i = j
	}
	return &folded{text: sb.String(), ostart: ostart, oend: oend}
}

// original maps a byte range of the folded text to the range of the original that produced it.
func (f *folded) original(start, end int) (int, int) {
	if f.ostart == nil {
		return start, end
	}
	return int(f.ostart[start]), int(f.oend[end-1])
}

// jsonNumbers lists the byte ranges of the number tokens of a text that is a JSON document (it
// is one: valid JSON starting with { or [): the values that are numbers, never the digits inside a string. A tool call's
// arguments are such a document, and replacing digits inside a number with a label makes the
// document invalid, which is how a timestamp used to become "[PHONE]" and refuse the call. A text
// that is not a document has none, so a number in a sentence is judged as it always was.
func jsonNumbers(s string) [][2]int {
	if t := strings.TrimLeft(s, " \t\r\n"); t == "" || (t[0] != '{' && t[0] != '[') || !json.Valid([]byte(s)) {
		return nil // only a text that really is a document: "[note] call 5551234567" is a sentence
	}
	var out [][2]int
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && strings.IndexByte("0123456789.eE+-", s[j]) >= 0 {
				j++
			}
			out = append(out, [2]int{i, j})
			i = j - 1
		}
	}
	return out
}

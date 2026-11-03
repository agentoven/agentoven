package guardrails

// The detectors a pii_detection rule can name. Every pattern is a Go regexp, which is RE2:
// matching takes time linear in the text, so no pattern a user or this file writes can be made
// to backtrack catastrophically (the ReDoS class does not exist here). What can still be
// abused is the size of the text and the number of rules, so scan.go bounds both.
//
// A pattern finds candidates and, where the value has a public check digit or issuing rule,
// a validator in checksum.go confirms it. That is what keeps a redacting rule from mangling
// an order number that happens to have sixteen digits.

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// boundary says what may not stand next to a finding. Go's \b is ASCII-only and counts "_" and a
// letter as a neighbour that forbids a match, which is how "token_sk-..." and "1sk-..." walked past
// the key pattern. A detector names the neighbours that really would make the match part of a
// longer value, and the engine checks them (a regexp has no lookaround).
type boundary uint8

const (
	boundNone   boundary = iota // anything may stand next to it
	boundDigit                  // not a digit: the match would be part of a longer number
	boundAlnum                  // not an ASCII letter or digit: part of a longer code
	boundLetter                 // not a letter of any script: "task-" must not read as "sk-"
)

// class is the regexp class of the characters boundary forbids.
func (b boundary) class() string {
	switch b {
	case boundDigit:
		return `[^0-9]`
	case boundAlnum:
		return `[^0-9A-Za-z]`
	case boundLetter:
		return `[^\p{L}]`
	}
	return ""
}

// allows reports whether the rune next to a match (utf8.RuneError at the edge of the text counts as
// nothing there) is acceptable.
func (b boundary) allows(r rune, edge bool) bool {
	if edge {
		return true
	}
	switch b {
	case boundDigit:
		return r < '0' || r > '9'
	case boundAlnum:
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	case boundLetter:
		return !unicode.IsLetter(r)
	}
	return true
}

// detector finds one kind of sensitive value.
type detector struct {
	name  string
	label string // the placeholder text: [label]
	re    *regexp.Regexp
	// group is the submatch to replace; 0 is the whole match. A pattern that needs context on
	// either side (to stand in for a lookbehind RE2 does not have) captures the value in 1.
	group int
	// valid, when set, confirms a candidate. A candidate it rejects is not a finding.
	valid func(match string) bool
	// secret detectors are always replaced whole, whatever the action: keeping the last four
	// characters of a key would hand over part of it.
	secret bool
	// anyOf, when set, lists literals of which at least one must occur in the text for the pattern
	// to be worth running: a regexp pass over megabytes is the cost of a detector, and a text with no
	// "@" has no email in it. The literals are matched as written, so a case-insensitive detector
	// leaves it empty.
	anyOf []string
	// rank breaks a tie between overlapping findings of equal length: a checksum-validated
	// finding outranks a bare pattern.
	rank int
	// lead and trail are the neighbours that disqualify a match (see boundary).
	lead, trail boundary
	// core is the pattern of a detector that takes its neighbours from lead and trail (see compile).
	core string
	// jsonNumber says what to do with a finding that sits inside a number of a JSON document: skip it
	// (a loose pattern that matches timestamps and ids) or, the default, replace the whole number
	// with a string, which keeps the document valid and still removes the value.
	skipJSONNumber bool
}

var detectors = buildDetectors()

// sep is the separator an issuer's grouping may use: a space, hyphen, dot, slash or line break
// (no-break and exotic spaces are folded to a space before matching, see fold.go).
const sep = `[ \-./\n]`

func buildDetectors() map[string]*detector {
	list := []*detector{
		// The four names that existed before actions, with the patterns they always had.
		{name: "email", label: "EMAIL", anyOf: []string{"@"}, rank: 1,
			re: regexp.MustCompile(`[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}.\-]+\.(?:xn--[a-z0-9\-]{2,}|\p{L}{2,})`)},
		{name: "phone", label: "PHONE", rank: 0, skipJSONNumber: true,
			re: regexp.MustCompile(`(\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`)},
		// Nine digits with no separator are accepted: a number written without its dashes is the same
		// number. They are a loose match (an order number is nine digits too), so a JSON number is skipped.
		{name: "ssn", label: "SSN", valid: ssnValid, rank: 2, lead: boundDigit, trail: boundDigit, skipJSONNumber: true,
			core: `\d{3}-\d{2}-\d{4}|\d{3}\.\d{2}\.\d{4}|\d{3}/\d{2}/\d{4}|\d{9}`},
		// A card is 13 to 19 digits in the groupings issuers use, or unbroken. The alternatives
		// stop at the group boundary so a number followed by more digits is not swallowed whole.
		{name: "credit_card", label: "CARD_NUMBER", valid: cardValid, rank: 2, lead: boundDigit, trail: boundDigit,
			core: `\d{4}` + sep + `?\d{4}` + sep + `?\d{4}` + sep + `?\d{4}|\d{4}` + sep + `?\d{6}` + sep + `?\d{5}|\d{4}` + sep + `?\d{6}` + sep + `?\d{4}|\d{13,19}`},

		{name: "phone_intl", label: "PHONE", anyOf: []string{"+"}, re: regexp.MustCompile(`\+[1-9][\d\s().\-]{6,18}\d`), valid: phoneIntlValid, rank: 1},
		{name: "iban", label: "IBAN", valid: ibanValid, rank: 2, lead: boundAlnum, trail: boundAlnum,
			core: `(?i:[A-Z]{2}\d{2}(?:` + sep + `?[A-Z0-9]{4}){2,7}(?:` + sep + `?[A-Z0-9]{1,3})?)`},
		{name: "aadhaar", label: "AADHAAR", valid: aadhaarValid, rank: 2, lead: boundDigit, trail: boundDigit,
			core: `[2-9]\d{3}` + sep + `?\d{4}` + sep + `?\d{4}`},
		{name: "pan_in", label: "PAN", valid: panValid, rank: 1, lead: boundAlnum, trail: boundAlnum,
			core: `(?i:[A-Z]{5}\d{4}[A-Z])`},
		{name: "uk_nino", label: "NINO", valid: ninoValid, rank: 1, lead: boundAlnum, trail: boundAlnum,
			core: `(?i:[A-CEGHJ-PR-TW-Z][A-CEGHJ-NPR-TW-Z]` + sep + `?\d{2}` + sep + `?\d{2}` + sep + `?\d{2}` + sep + `?[A-D])`},
		{name: "nhs_number", label: "NHS_NUMBER", valid: nhsValid, rank: 2, lead: boundDigit, trail: boundDigit, skipJSONNumber: true,
			core: `\d{3}` + sep + `?\d{3}` + sep + `?\d{4}`},
		{name: "eu_vat", label: "VAT_ID", valid: vatValid, rank: 2, lead: boundAlnum, trail: boundAlnum,
			core: `(?i:DE\d{9}|FR\d{11}|IT\d{11}|BE[01]\d{9}|PL\d{10})`},
		{name: "es_dni", label: "NATIONAL_ID", valid: dniValid, rank: 2, lead: boundAlnum, trail: boundAlnum,
			core: `(?:\d{8}|[XYZxyz]\d{7})[A-Za-z]`},
		{name: "nl_bsn", label: "NATIONAL_ID", valid: bsnValid, rank: 1, lead: boundDigit, trail: boundDigit, skipJSONNumber: true,
			core: `\d{9}`},
		{name: "ip_address", label: "IP_ADDRESS", anyOf: []string{"."}, rank: 1, valid: ipValid, lead: boundDigit, trail: boundDigit,
			core: `(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)`},
		// IPv6 has no word boundary to lean on, so the neighbours are matched and left alone.
		{name: "ipv6_address", label: "IP_ADDRESS", anyOf: []string{":"}, rank: 1, group: 1, valid: ipValid,
			re: regexp.MustCompile(`(?:^|[^0-9A-Za-z:.])([0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7})(?:$|[^0-9A-Za-z:.])`)},

		// Secrets. These are replaced whole under every action. Their prefixes are distinctive, so
		// most take no neighbour at all ("_ghp_..." and "1sk-..." are keys); "sk-" and "sk_" are also
		// the end of ordinary words ("task-management"), so a letter before them is a word.
		{name: "aws_access_key", label: "AWS_ACCESS_KEY", anyOf: []string{"AKIA", "ASIA", "ABIA", "ACCA", "AGPA", "AIDA", "AIPA", "ANPA", "ANVA", "AROA"}, secret: true, rank: 2, trail: boundAlnum,
			core: `(?:AKIA|ASIA|ABIA|ACCA|AGPA|AIDA|AIPA|ANPA|ANVA|AROA)[A-Z2-7]{16}`},
		{name: "aws_secret_key", label: "AWS_SECRET_KEY", secret: true, rank: 2, group: 1,
			re: regexp.MustCompile(`(?i)aws[_\-\s]?(?:secret)?[_\-\s]?(?:access)?[_\-\s]?key["'\s:=]{1,6}([A-Za-z0-9/+=]{40})\b`)},
		{name: "github_token", label: "GITHUB_TOKEN", anyOf: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}, secret: true, rank: 2,
			core: `gh[pousr]_[A-Za-z0-9]{36,251}|github_pat_[A-Za-z0-9_]{50,255}`},
		{name: "slack_token", label: "SLACK_TOKEN", anyOf: []string{"xox", "hooks.slack.com"}, secret: true, rank: 2,
			core: `xox[abposr]-[A-Za-z0-9-]{10,72}|https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+`},
		{name: "jwt", label: "JWT", anyOf: []string{"eyJ"}, secret: true, rank: 2, valid: jwtValid,
			core: `eyJ[A-Za-z0-9_\-]{5,}\.eyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`},
		// A private key that was cut off (no END line) is redacted to the end of the text: what
		// follows a BEGIN line is key material.
		{name: "private_key", label: "PRIVATE_KEY", anyOf: []string{"PRIVATE KEY"}, secret: true, rank: 2,
			core: `-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(?:-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----|\z)`},
		{name: "api_key", label: "API_KEY", anyOf: []string{"sk-", "AIza", "sk_", "rk_"}, secret: true, rank: 1, lead: boundLetter,
			core: `sk-(?:ant-|proj-|live-)?[A-Za-z0-9_\-]{20,}|AIza[0-9A-Za-z_\-]{35}|[sr]k_(?:live|test)_[0-9a-zA-Z]{16,}`},
	}
	m := make(map[string]*detector, len(list))
	for _, d := range list {
		if d.re == nil {
			d.compile()
		}
		m[d.name] = d
	}
	return m
}

// compile builds the pattern of a detector written as a core: the core with the lead neighbour in
// front of it (the neighbour is matched and left alone, the value is the group). The trail
// neighbour is checked by the engine after the match, so two findings with one character between
// them are both found.
func (d *detector) compile() {
	expr := d.core
	if class := d.lead.class(); class != "" {
		expr = `(?:^|` + class + `)(` + expr + `)`
		d.group = 1
	}
	d.re = regexp.MustCompile(expr)
}

// legacyPII is what a pii_detection rule with no pattern list has always checked, and still does.
var legacyPII = []string{"email", "phone", "ssn", "credit_card"}

// detectorGroups are names that stand for several detectors.
var detectorGroups = map[string][]string{
	"default": legacyPII,
	"pii": {"email", "phone_intl", "ssn", "credit_card", "iban", "aadhaar", "pan_in", "uk_nino", "nhs_number",
		"eu_vat", "es_dni", "ip_address", "ipv6_address"},
	"secrets":     {"aws_access_key", "aws_secret_key", "github_token", "slack_token", "jwt", "private_key", "api_key"},
	"national_id": {"aadhaar", "pan_in", "uk_nino", "nhs_number", "ssn", "es_dni", "eu_vat"},
}

func init() {
	detectorGroups["all"] = append(append([]string{}, detectorGroups["pii"]...), detectorGroups["secrets"]...)
}

// DetectorNames lists every name a pii_detection rule accepts: the detectors and the groups.
func DetectorNames() []string {
	out := make([]string, 0, len(detectors)+len(detectorGroups))
	for n := range detectors {
		out = append(out, n)
	}
	for n := range detectorGroups {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// resolveDetectors turns the names in a rule's patterns list into detectors, each once. A name
// that is neither a detector nor a group is an error: a typo must not leave a rule checking
// nothing.
func resolveDetectors(names []string) ([]*detector, error) {
	if len(names) == 0 {
		names = legacyPII
	}
	seen := map[string]bool{}
	var out []*detector
	var add func(name string) error
	add = func(name string) error {
		if group, ok := detectorGroups[name]; ok {
			for _, n := range group {
				if err := add(n); err != nil {
					return err
				}
			}
			return nil
		}
		d, ok := detectors[name]
		if !ok {
			return &unknownDetectorError{name}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, d)
		}
		return nil
	}
	for _, n := range names {
		if err := add(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type unknownDetectorError struct{ name string }

func (e *unknownDetectorError) Error() string { return "unknown PII pattern " + e.name }

// span is one finding: a byte range of the scanned text and what it is.
type span struct {
	start, end int
	det        *detector
	// val is the value as the detector read it (folded), which is what a mask keeps the end of.
	val string
	// quote: the finding is a number of a JSON document and is replaced by a string, so the document
	// stays valid. start and end then cover the whole number.
	quote bool
}

// errScanBudget is returned when a scan would take more work than one evaluation may spend.
var errScanBudget = errors.New("the text needs too much work to check with the rules that apply to it")

// findSpans runs the detectors over text and returns the findings in text order, without
// overlaps. Where two findings overlap the longer wins, and for equal lengths the one with the
// higher rank (checksum-validated before bare pattern) — so a card number is not also reported
// as the ten digits of a loose phone pattern inside it; a finding that overlaps only in part is
// merged into the winner, so the tail of a value is not left behind. allow decides which matched
// values are left alone.
//
// Detection runs on a folded copy of the text (fold.go) and the spans are mapped back to byte
// ranges of text. sc bounds the work: every pattern pass is charged, and a scan that would exceed
// the budget returns errScanBudget instead of finishing.
func findSpans(dets []*detector, text string, allow *allowlist, sc *scan) ([]span, error) {
	f := sc.fold(text)
	if f == nil {
		return nil, errScanBudget
	}
	var numbers [][2]int
	numbersDone := false
	var all []span
	for _, d := range dets {
		if !mentions(f.text, d.anyOf) {
			continue
		}
		if !sc.charge(len(f.text)) {
			return nil, errScanBudget
		}
		for _, loc := range d.re.FindAllStringSubmatchIndex(f.text, -1) {
			s, e := loc[0], loc[1]
			if d.group > 0 {
				if len(loc) <= 2*d.group+1 || loc[2*d.group] < 0 {
					continue
				}
				s, e = loc[2*d.group], loc[2*d.group+1]
			}
			if !d.trail.allows(runeAt(f.text, e)) {
				continue
			}
			value := f.text[s:e]
			if d.valid != nil && !d.valid(value) {
				continue
			}
			if allow.permits(d, value) {
				continue
			}
			sp := span{start: s, end: e, det: d, val: value}
			if !numbersDone {
				numbers, numbersDone = jsonNumbers(f.text), true
			}
			if n, inside := numberAround(numbers, s, e); inside {
				if d.skipJSONNumber {
					continue
				}
				sp.start, sp.end, sp.val, sp.quote = n[0], n[1], f.text[n[0]:n[1]], true
			}
			all = append(all, sp)
		}
	}
	// Overlaps are settled within clusters of findings that touch one another, which are small, so a
	// text with a hundred thousand findings does not cost a comparison of every pair.
	sort.Slice(all, func(i, j int) bool {
		if all[i].start != all[j].start {
			return all[i].start < all[j].start
		}
		return all[i].end > all[j].end
	})
	var kept []span
	for i := 0; i < len(all); {
		j, reach := i+1, all[i].end
		for j < len(all) && all[j].start < reach {
			if all[j].end > reach {
				reach = all[j].end
			}
			j++
		}
		kept = append(kept, settle(all[i:j])...)
		i = j
	}
	for i := range kept {
		kept[i].val = f.text[kept[i].start:kept[i].end]
	}
	return f.mapBack(kept), nil
}

// runeAt is the rune starting at byte i of s, and whether i is past the end of s.
func runeAt(s string, i int) (rune, bool) {
	if i >= len(s) {
		return 0, true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r, false
}

// numberAround returns the JSON number that contains [s,e), if there is one. numbers is in text order.
func numberAround(numbers [][2]int, s, e int) ([2]int, bool) {
	i := sort.Search(len(numbers), func(i int) bool { return numbers[i][1] > s })
	if i < len(numbers) && numbers[i][0] <= s && e <= numbers[i][1] {
		return numbers[i], true
	}
	return [2]int{}, false
}

// mapBack turns spans of the folded text into spans of the original. Two findings that sit in one
// character of the original (a ligature) are joined, so the result still has no overlap.
func (f *folded) mapBack(spans []span) []span {
	if f.ostart == nil {
		return spans
	}
	for i := range spans {
		spans[i].start, spans[i].end = f.original(spans[i].start, spans[i].end)
		if i > 0 && spans[i].start < spans[i-1].end {
			spans[i].start = spans[i-1].end
			if spans[i].end < spans[i].start {
				spans[i].end = spans[i].start
			}
		}
	}
	return spans
}

// mentions is whether text contains any of the literals; no literals means "always run".
func mentions(text string, anyOf []string) bool {
	if len(anyOf) == 0 {
		return true
	}
	for _, lit := range anyOf {
		if strings.Contains(text, lit) {
			return true
		}
	}
	return false
}

// settle picks, from findings that overlap in a chain, the ones to keep: the longer wins, then the
// checksum-validated one, then the earlier. A finding that is not kept but reaches beyond a kept one
// is joined to it (the winner keeps its label), so no part of either value is left in the text. The
// result is in text order.
func settle(cluster []span) []span {
	if len(cluster) == 1 {
		return cluster
	}
	byPriority := append([]span(nil), cluster...)
	sort.SliceStable(byPriority, func(i, j int) bool {
		li, lj := byPriority[i].end-byPriority[i].start, byPriority[j].end-byPriority[j].start
		if li != lj {
			return li > lj
		}
		if byPriority[i].det.rank != byPriority[j].det.rank {
			return byPriority[i].det.rank > byPriority[j].det.rank
		}
		return byPriority[i].start < byPriority[j].start
	})
	var kept, dropped []span
	for _, sp := range byPriority {
		overlaps := false
		for _, k := range kept {
			if sp.start < k.end && k.start < sp.end {
				overlaps = true
				break
			}
		}
		if overlaps {
			dropped = append(dropped, sp)
		} else {
			kept = append(kept, sp)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].start < kept[j].start })
	for _, sp := range dropped {
		for i := range kept {
			k := &kept[i]
			if sp.start < k.end && k.start < sp.end {
				k.start, k.end = min(k.start, sp.start), max(k.end, sp.end)
				k.quote = k.quote || sp.quote
			}
		}
	}
	// Growing a winner can make it touch its neighbour; two that touch are one finding.
	merged := kept[:1]
	for _, k := range kept[1:] {
		last := &merged[len(merged)-1]
		if k.start < last.end {
			last.end = max(last.end, k.end)
			continue
		}
		merged = append(merged, k)
	}
	return merged
}

// allowlist is the values a rule is told to leave alone: exact values (compared without case
// and, for numbers, without spaces and dashes) and, for email addresses, whole domains.
type allowlist struct {
	values  map[string]bool
	domains []string
}

func newAllowlist(values, domains []string) *allowlist {
	if len(values) == 0 && len(domains) == 0 {
		return nil
	}
	a := &allowlist{values: map[string]bool{}}
	for _, v := range values {
		a.values[normalizeAllowed(v)] = true
	}
	for _, d := range domains {
		a.domains = append(a.domains, strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@")))
	}
	return a
}

func normalizeAllowed(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(s)))
}

func (a *allowlist) permits(d *detector, value string) bool {
	if a == nil {
		return false
	}
	if a.values[normalizeAllowed(value)] {
		return true
	}
	if d.name == "email" {
		if at := strings.LastIndexByte(value, '@'); at >= 0 {
			host := strings.ToLower(value[at+1:])
			for _, dom := range a.domains {
				if host == dom || strings.HasSuffix(host, "."+dom) {
					return true
				}
			}
		}
	}
	return false
}

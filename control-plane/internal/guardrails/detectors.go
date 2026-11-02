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
	"regexp"
	"sort"
	"strings"
)

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
	// "@" has no email in it.
	anyOf []string
	// rank breaks a tie between overlapping findings of equal length: a checksum-validated
	// finding outranks a bare pattern.
	rank int
}

var detectors = buildDetectors()

func buildDetectors() map[string]*detector {
	list := []*detector{
		// The four names that existed before actions, with the patterns they always had.
		{name: "email", label: "EMAIL", anyOf: []string{"@"}, re: regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`), rank: 1},
		{name: "phone", label: "PHONE", re: regexp.MustCompile(`(\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`), rank: 0},
		{name: "ssn", label: "SSN", re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), valid: ssnValid, rank: 2},
		// A card is 13 to 19 digits in the groupings issuers use, or unbroken. The alternatives
		// stop at the group boundary so a number followed by more digits is not swallowed whole.
		{name: "credit_card", label: "CARD_NUMBER", re: regexp.MustCompile(
			`\b(?:\d{4}[ -]?\d{4}[ -]?\d{4}[ -]?\d{4}|\d{4}[ -]?\d{6}[ -]?\d{5}|\d{4}[ -]?\d{6}[ -]?\d{4}|\d{13,19})\b`),
			valid: cardValid, rank: 2},

		{name: "phone_intl", label: "PHONE", anyOf: []string{"+"}, re: regexp.MustCompile(`\+[1-9][\d\s().\-]{6,18}\d`), valid: phoneIntlValid, rank: 1},
		{name: "iban", label: "IBAN", re: regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,3})?\b`), valid: ibanValid, rank: 2},
		{name: "aadhaar", label: "AADHAAR", re: regexp.MustCompile(`\b[2-9]\d{3}[ -]?\d{4}[ -]?\d{4}\b`), valid: aadhaarValid, rank: 2},
		{name: "pan_in", label: "PAN", re: regexp.MustCompile(`\b[A-Z]{5}\d{4}[A-Z]\b`), valid: panValid, rank: 1},
		{name: "uk_nino", label: "NINO", re: regexp.MustCompile(`\b[A-CEGHJ-PR-TW-Z][A-CEGHJ-NPR-TW-Z] ?\d{2} ?\d{2} ?\d{2} ?[A-D]\b`), valid: ninoValid, rank: 1},
		{name: "nhs_number", label: "NHS_NUMBER", re: regexp.MustCompile(`\b\d{3}[ -]?\d{3}[ -]?\d{4}\b`), valid: nhsValid, rank: 2},
		{name: "eu_vat", label: "VAT_ID", anyOf: []string{"DE", "FR", "IT", "BE", "PL"}, re: regexp.MustCompile(`\b(?:DE\d{9}|FR\d{11}|IT\d{11}|BE[01]\d{9}|PL\d{10})\b`), valid: vatValid, rank: 2},
		{name: "es_dni", label: "NATIONAL_ID", re: regexp.MustCompile(`\b(?:\d{8}|[XYZxyz]\d{7})[A-Za-z]\b`), valid: dniValid, rank: 2},
		{name: "nl_bsn", label: "NATIONAL_ID", re: regexp.MustCompile(`\b\d{9}\b`), valid: bsnValid, rank: 1},
		{name: "ip_address", label: "IP_ADDRESS", anyOf: []string{"."}, rank: 1,
			re: regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`), valid: ipValid},
		// IPv6 has no word boundary to lean on, so the neighbours are matched and left alone.
		{name: "ipv6_address", label: "IP_ADDRESS", anyOf: []string{":"}, rank: 1, group: 1, valid: ipValid,
			re: regexp.MustCompile(`(?:^|[^0-9A-Za-z:.])([0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7})(?:$|[^0-9A-Za-z:.])`)},

		// Secrets. These are replaced whole under every action.
		{name: "aws_access_key", label: "AWS_ACCESS_KEY", anyOf: []string{"AKIA", "ASIA", "ABIA", "ACCA", "AGPA", "AIDA", "AIPA", "ANPA", "ANVA", "AROA"}, secret: true, rank: 2,
			re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA|AGPA|AIDA|AIPA|ANPA|ANVA|AROA)[A-Z2-7]{16}\b`)},
		{name: "aws_secret_key", label: "AWS_SECRET_KEY", secret: true, rank: 2, group: 1,
			re: regexp.MustCompile(`(?i)\baws[_\-\s]?(?:secret)?[_\-\s]?(?:access)?[_\-\s]?key["'\s:=]{1,6}([A-Za-z0-9/+=]{40})\b`)},
		{name: "github_token", label: "GITHUB_TOKEN", anyOf: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}, secret: true, rank: 2,
			re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,251}|github_pat_[A-Za-z0-9_]{50,255})\b`)},
		{name: "slack_token", label: "SLACK_TOKEN", anyOf: []string{"xox", "hooks.slack.com"}, secret: true, rank: 2,
			re: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,72}\b|https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+`)},
		{name: "jwt", label: "JWT", anyOf: []string{"eyJ"}, secret: true, rank: 2, valid: jwtValid,
			re: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.eyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`)},
		// A private key that was cut off (no END line) is redacted to the end of the text: what
		// follows a BEGIN line is key material.
		{name: "private_key", label: "PRIVATE_KEY", anyOf: []string{"PRIVATE KEY"}, secret: true, rank: 2,
			re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(?:-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----|\z)`)},
		{name: "api_key", label: "API_KEY", anyOf: []string{"sk-", "AIza", "sk_", "rk_"}, secret: true, rank: 1,
			re: regexp.MustCompile(`\bsk-(?:ant-|proj-|live-)?[A-Za-z0-9_\-]{20,}\b|\bAIza[0-9A-Za-z_\-]{35}\b|\b[sr]k_(?:live|test)_[0-9a-zA-Z]{16,}\b`)},
	}
	m := make(map[string]*detector, len(list))
	for _, d := range list {
		m[d.name] = d
	}
	return m
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
}

// findSpans runs the detectors over text and returns the findings in text order, without
// overlaps. Where two findings overlap the longer wins, and for equal lengths the one with the
// higher rank (checksum-validated before bare pattern) — so a card number is not also reported
// as the ten digits of a loose phone pattern inside it. allow decides which matched values are
// left alone.
func findSpans(dets []*detector, text string, allow *allowlist) []span {
	var all []span
	for _, d := range dets {
		if !mentions(text, d.anyOf) {
			continue
		}
		for _, loc := range d.re.FindAllStringSubmatchIndex(text, -1) {
			s, e := loc[0], loc[1]
			if d.group > 0 {
				if len(loc) <= 2*d.group+1 || loc[2*d.group] < 0 {
					continue
				}
				s, e = loc[2*d.group], loc[2*d.group+1]
			}
			value := text[s:e]
			if d.valid != nil && !d.valid(value) {
				continue
			}
			if allow.permits(d, value) {
				continue
			}
			all = append(all, span{s, e, d})
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
	return kept
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
// checksum-validated one, then the earlier. The result is in text order.
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
	var kept []span
	for _, sp := range byPriority {
		overlaps := false
		for _, k := range kept {
			if sp.start < k.end && k.start < sp.end {
				overlaps = true
				break
			}
		}
		if !overlaps {
			kept = append(kept, sp)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].start < kept[j].start })
	return kept
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

package guardrails

// MaxScanWork is the regexp work, in bytes of text passed through a pattern, that one evaluation may
// spend over all the rules that apply to a text. A pattern pass runs at roughly a hundred megabytes a
// second, and a pii rule naming "all" makes about fourteen passes, so twenty such rules over a
// 1 MiB text cost five seconds of CPU for one request (review 5, finding 15). MaxScanBytes bounds one
// text and MaxRules the rules; this bounds their product. A request over it is refused with a message
// that says so, never half-scanned.
const MaxScanWork = 48 << 20

// growthFactor is how much longer than MaxScanBytes a text may become when a rule redacts. A text
// that fitted when it arrived must not be refused by the next rule because a label is longer than
// the value it replaced ("a@b.co" is six bytes, "[EMAIL]" seven); the work budget bounds the cost.
const growthFactor = 8

// scan is the state of one evaluation: the work left and the size limit that applies to the text it
// carries. It belongs to one evaluation, which runs its rules one after another. A nil scan has no budget and the plain size limit, which is what direct callers of the
// detectors (tests, the allowlist check) get.
type scan struct {
	work  int64
	limit int

	// The folded copy of the text last asked for, so the rules of one evaluation fold it once.
	foldedFor string
	folded    *folded
}

// newScan opens the budget for a text of origLen bytes. A text over MaxScanBytes gets no growth
// allowance: every span-finding rule refuses it.
func newScan(origLen int) *scan {
	limit := MaxScanBytes
	if origLen <= MaxScanBytes {
		limit = MaxScanBytes * growthFactor
	}
	return &scan{work: MaxScanWork, limit: limit}
}

// tooBig reports whether a text of n bytes may not be scanned.
func (s *scan) tooBig(n int) bool {
	if s == nil {
		return n > MaxScanBytes
	}
	return n > s.limit
}

// charge spends n bytes of pattern work; false means the budget is gone and the work must not be done.
func (s *scan) charge(n int) bool {
	if s == nil {
		return true
	}
	if int64(n) > s.work {
		s.work = 0
		return false
	}
	s.work -= int64(n)
	return true
}

// fold is the folded copy of text, or nil when folding it would exceed the budget. Folding costs one
// pass; the copy is kept for the next rule, which in a redacting chain sees different text and folds
// again.
func (s *scan) fold(text string) *folded {
	if s == nil {
		return fold(text)
	}
	if s.folded != nil && len(s.foldedFor) == len(text) && s.foldedFor == text {
		return s.folded
	}
	if !isASCII(text) && !s.charge(len(text)) {
		return nil
	}
	f := fold(text)
	s.foldedFor, s.folded = text, f
	return f
}

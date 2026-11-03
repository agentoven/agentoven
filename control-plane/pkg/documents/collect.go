package documents

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// collector gathers blocks until the text budget is spent. Every reader writes through it, so the
// bound on what a document contributes is in one place and a reader cannot forget it.
type collector struct {
	res    *Result
	limit  int
	budget int
	full   bool
}

func newCollector(res *Result, lim Limits) *collector {
	return &collector{res: res, limit: lim.MaxTextBytes, budget: lim.MaxTextBytes}
}

// done reports whether the budget is spent; readers stop reading when it is.
func (c *collector) done() bool { return c.full }

func (c *collector) stop() {
	if !c.full {
		c.full = true
		c.res.Truncated = true
		c.res.warn("the document is longer than the %d-byte text limit; the rest was left out", c.limit)
	}
}

// text adds a heading, paragraph or list item. Empty text is dropped.
func (c *collector) text(b Block) {
	if c.full {
		return
	}
	b.Text = strings.TrimSpace(b.Text)
	if b.Text == "" {
		return
	}
	if len(b.Text) > c.budget {
		b.Text = cutRunes(b.Text, c.budget)
		c.res.Blocks = append(c.res.Blocks, b)
		c.budget = 0
		c.stop()
		return
	}
	c.budget -= len(b.Text) + 2
	c.res.Blocks = append(c.res.Blocks, b)
	if c.budget <= 0 {
		c.stop()
	}
}

// table adds a table, dropping the rows that do not fit. A table with no non-empty cell is dropped.
func (c *collector) table(b Block) {
	if c.full || b.Table == nil {
		return
	}
	t := b.Table
	kept := 0
	for i, row := range t.Rows {
		cost := 4
		for _, cell := range row {
			cost += len(cell) + 3
		}
		if len(t.RowNumbers) > 0 {
			cost += 6
		}
		if cost > c.budget {
			t.Truncated = true
			break
		}
		c.budget -= cost
		kept = i + 1
	}
	if kept < len(t.Rows) {
		t.Rows = t.Rows[:kept]
		if len(t.RowNumbers) > kept {
			t.RowNumbers = t.RowNumbers[:kept]
		}
		t.Truncated = true
	}
	if len(t.Rows) == 0 {
		c.stop()
		return
	}
	c.res.Blocks = append(c.res.Blocks, b)
	if t.Truncated || c.budget <= 0 {
		c.stop()
	}
}

func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// sanitize makes text safe to hand to a model and to a terminal: invalid UTF-8 and control
// characters go (a tab and a newline stay), and so do the invisible characters used to hide text
// from a reader or to reorder what it sees (zero-width characters, bidirectional overrides, the
// "tag" block that can smuggle a whole sentence past a human). Joiners stay: some scripts need them.
func sanitize(s string) string {
	s = strings.ToValidUTF8(s, "")
	if !needsSanitizing(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if dropRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func needsSanitizing(s string) bool {
	for _, r := range s {
		if dropRune(r) {
			return true
		}
	}
	return false
}

func dropRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r == '\r':
		return true
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r < 0xa0:
		return true
	case r == 0x200b, r == 0x2060, r == 0xfeff, r == 0x00ad:
		return true
	case r >= 0x200e && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0xe0000 && r <= 0xe007f:
		return true
	}
	return !unicode.IsPrint(r) && !unicode.IsSpace(r) && r != 0x200c && r != 0x200d
}

// collapse squeezes runs of whitespace (newlines included) into single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

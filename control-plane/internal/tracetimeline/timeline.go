// Package tracetimeline computes what a person reads off a trace's spans: the tree, each span's
// place on the clock, the critical path, retries and fallbacks, and the totals. It is computed here,
// once, so the dashboard, the SDKs, the CLI and a customer's own tooling get the same answer
// (ADR-0039 section 1). Clients render it; they do not recompute it.
//
// The rules are written down so two implementations cannot disagree:
//
//   - Clock. Offsets are relative to the root span's start. A span that starts before its parent or
//     ends after it (a hosted agent on a skewed clock) is clamped into the parent and marked skew.
//     Overshoot of a millisecond or less is rounding, and is clamped without the mark. A span is
//     never dropped.
//   - Root. The root is the earliest span with no parent. Any other span with no parent, a parent
//     that is not in the trace, or a parent chain that loops, is attached to the root and marked
//     orphan.
//   - Critical path. Within a span, the children that decided when it ended: the child whose clamped
//     end is last (ties go to the longer span, then the lower ID), then the child that ended last at or
//     before that one began, and so on back. A parallel branch that finished sooner is not on it.
//     The rule is applied again inside each of those children. CriticalPathMs is the time on the path
//     no child of the same span covers: the sum, over its spans, of the span's duration less the union
//     of its children.
//   - Retries. Among the children of one span, an LLM span that follows a failed LLM span of the same
//     name (or, under a router.route span, any failed LLM span: a fallback), and a tool span that
//     follows a failed span of the same tool, is the next attempt. attempt counts from 1 and retry_of
//     names the span it retries.
//   - Size. At most Limit spans are returned, in tree order, with Truncated and a Cursor for the
//     next page. Every total is computed over all the spans.
package tracetimeline

import (
	"sort"
	"strconv"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// SchemaVersion is the shape of Timeline. A change that removes or renames a field is a new version,
// and the previous one is served for one minor release.
const SchemaVersion = 1

const (
	// DefaultLimit and MaxLimit bound a page of spans.
	DefaultLimit = 1000
	MaxLimit     = 5000
	// skewToleranceMs is overshoot that is rounding, not a skewed clock.
	skewToleranceMs = 1
)

// Timeline is the computed view of one trace.
type Timeline struct {
	SchemaVersion  int       `json:"schema_version"`
	TraceID        string    `json:"trace_id"`
	Environment    string    `json:"environment,omitempty"`
	AgentVersion   string    `json:"agent_version,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	TotalMs        int64     `json:"total_ms"`
	CriticalPathMs int64     `json:"critical_path_ms"`
	SpanCount      int       `json:"span_count"`
	Truncated      bool      `json:"truncated"`
	Cursor         string    `json:"cursor,omitempty"`

	Spans  []Span            `json:"spans"`
	Models []ModelRollup     `json:"models"`
	Lanes  []Lane            `json:"lanes"`
	Totals models.TokenUsage `json:"totals"`
}

// Span is one span placed on the timeline.
type Span struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	Depth      int    `json:"depth"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	OffsetMs   int64  `json:"offset_ms"`
	DurationMs int64  `json:"duration_ms"`
	Critical   bool   `json:"critical,omitempty"`
	Skew       bool   `json:"skew,omitempty"`
	Orphan     bool   `json:"orphan,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	RetryOf    string `json:"retry_of,omitempty"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Error      string `json:"error,omitempty"`

	Usage       *models.TokenUsage `json:"usage,omitempty"`
	CostUSD     float64            `json:"cost_usd,omitempty"`
	PriceSource string             `json:"price_source,omitempty"`
}

// ModelRollup is what one model used across the trace's LLM spans.
type ModelRollup struct {
	Model        string  `json:"model"`
	Provider     string  `json:"provider,omitempty"`
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Lane is a row of events beside the spans. The governance lane holds the audit events of the trace.
type Lane struct {
	Name   string      `json:"name"`
	Events []LaneEvent `json:"events"`
}

// LaneEvent is one event on a lane, placed on the same clock as the spans.
type LaneEvent struct {
	At        time.Time `json:"at"`
	OffsetMs  int64     `json:"offset_ms"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Status    int       `json:"status,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	EntryHash string    `json:"entry_hash,omitempty"`
}

// Options select a page of the result.
type Options struct {
	Limit  int
	Cursor string // the previous page's Cursor
}

type node struct {
	span     models.Span
	start    time.Time // as clamped
	end      time.Time
	depth    int
	skew     bool
	orphan   bool
	children []*node
	critical bool
	attempt  int
	retryOf  string
}

// Build computes the timeline of a trace from its spans.
func Build(trace *models.Trace, spans []models.Span, lanes []Lane, opt Options) *Timeline {
	tl := &Timeline{SchemaVersion: SchemaVersion, Spans: []Span{}, Models: []ModelRollup{}, Lanes: lanes}
	if tl.Lanes == nil {
		tl.Lanes = []Lane{}
	}
	if trace != nil {
		tl.TraceID, tl.Environment, tl.AgentVersion = trace.ID, trace.Environment, trace.AgentVersion
		tl.StartedAt, tl.TotalMs = trace.CreatedAt, trace.DurationMs
	}
	if len(spans) == 0 {
		return tl
	}

	// One node per distinct span, in a fixed order so the result never depends on storage order.
	nodes := make(map[string]*node, len(spans))
	order := make([]*node, 0, len(spans))
	for _, sp := range spans {
		if _, dup := nodes[sp.ID]; dup {
			continue
		}
		n := &node{span: sp}
		n.start, n.end = bounds(sp)
		nodes[sp.ID] = n
		order = append(order, n)
	}
	sortNodes(order, func(n *node) (time.Time, string) { return n.start, n.span.ID })

	// The root: the earliest span with no parent, else the earliest span.
	var root *node
	for _, n := range order {
		if n.span.ParentSpanID == "" {
			root = n
			break
		}
	}
	if root == nil {
		root = order[0]
	}

	attached := map[string]bool{root.span.ID: true}
	var attach func(n *node)
	attach = func(n *node) {
		for _, c := range order {
			if attached[c.span.ID] || c.span.ParentSpanID != n.span.ID {
				continue
			}
			attached[c.span.ID] = true
			n.children = append(n.children, c)
			attach(c)
		}
	}
	attach(root)
	// What is left has no way to the root: another root, a missing parent, a loop. It hangs off the root.
	for _, n := range order {
		if attached[n.span.ID] {
			continue
		}
		attached[n.span.ID] = true
		n.orphan = true
		root.children = append(root.children, n)
		attach(n)
	}

	rootStart := root.start
	var place func(n *node, parent *node, depth int)
	place = func(n *node, parent *node, depth int) {
		n.depth = depth
		if parent != nil {
			if n.start.Before(parent.start.Add(-skewToleranceMs*time.Millisecond)) || n.end.After(parent.end.Add(skewToleranceMs*time.Millisecond)) {
				n.skew = true
			}
			if n.start.Before(parent.start) {
				n.start = parent.start
			}
			if n.start.After(parent.end) {
				n.start = parent.end
			}
			if n.end.After(parent.end) {
				n.end = parent.end
			}
			if n.end.Before(n.start) {
				n.end = n.start
			}
		}
		sortNodes(n.children, func(c *node) (time.Time, string) { return c.start, c.span.ID })
		for _, c := range n.children {
			place(c, n, depth+1)
		}
		// Clamping can reorder siblings, so settle the order after it.
		sortNodes(n.children, func(c *node) (time.Time, string) { return c.start, c.span.ID })
	}
	place(root, nil, 0)

	markAttempts(root)
	tl.CriticalPathMs = markCritical(root)

	tl.StartedAt = rootStart
	tl.TotalMs = ms(root.end.Sub(root.start))

	var flat []Span
	rollups := map[string]*ModelRollup{}
	var walk func(n *node)
	walk = func(n *node) {
		sp := Span{
			ID: n.span.ID, ParentID: n.span.ParentSpanID, Depth: n.depth, Kind: string(n.span.Kind), Name: n.span.Name,
			Status: n.span.Status, OffsetMs: ms(n.start.Sub(rootStart)), DurationMs: ms(n.end.Sub(n.start)),
			Critical: n.critical, Skew: n.skew, Orphan: n.orphan, Attempt: n.attempt, RetryOf: n.retryOf,
			Model: n.span.Model, Provider: n.span.Provider, Error: n.span.Error, Usage: n.span.Usage,
		}
		if n.orphan {
			sp.ParentID = ""
		}
		if u := n.span.Usage; u != nil {
			sp.CostUSD, sp.PriceSource = u.EstimatedCost, u.PriceSource
		}
		flat = append(flat, sp)
		if n.span.Kind == models.SpanKindLLM && n.span.Usage != nil && n.span.Model != "" {
			key := n.span.Provider + "\x00" + n.span.Model
			r := rollups[key]
			if r == nil {
				r = &ModelRollup{Model: n.span.Model, Provider: n.span.Provider}
				rollups[key] = r
			}
			r.Calls++
			r.InputTokens += n.span.Usage.InputTokens
			r.OutputTokens += n.span.Usage.OutputTokens
			r.TotalTokens += n.span.Usage.TotalTokens
			r.CostUSD += n.span.Usage.EstimatedCost
			tl.Totals.InputTokens += n.span.Usage.InputTokens
			tl.Totals.OutputTokens += n.span.Usage.OutputTokens
			tl.Totals.TotalTokens += n.span.Usage.TotalTokens
			tl.Totals.ThinkingTokens += n.span.Usage.ThinkingTokens
			tl.Totals.CachedTokens += n.span.Usage.CachedTokens
			tl.Totals.EstimatedCost += n.span.Usage.EstimatedCost
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
	tl.SpanCount = len(flat)

	keys := make([]string, 0, len(rollups))
	for k := range rollups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tl.Models = append(tl.Models, *rollups[k])
	}

	// A page of the tree order.
	limit := opt.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	from := 0
	if n, err := strconv.Atoi(opt.Cursor); err == nil && n > 0 {
		from = n
	}
	if from > len(flat) {
		from = len(flat)
	}
	to := from + limit
	if to < len(flat) {
		tl.Truncated, tl.Cursor = true, strconv.Itoa(to)
	} else {
		to = len(flat)
	}
	tl.Spans = flat[from:to]

	// Lane events sit on the same clock as the spans.
	for i := range tl.Lanes {
		for j := range tl.Lanes[i].Events {
			tl.Lanes[i].Events[j].OffsetMs = ms(tl.Lanes[i].Events[j].At.Sub(rootStart))
		}
	}
	return tl
}

// bounds is a span's start and end as recorded; a span still running, or one with no end, ends where
// its recorded duration says.
func bounds(sp models.Span) (time.Time, time.Time) {
	start, end := sp.StartTime, sp.EndTime
	if end.IsZero() || end.Before(start) {
		end = start.Add(time.Duration(sp.DurationMs) * time.Millisecond)
	}
	return start, end
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func sortNodes(ns []*node, key func(*node) (time.Time, string)) {
	sort.SliceStable(ns, func(i, j int) bool {
		ti, ii := key(ns[i])
		tj, ij := key(ns[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return ii < ij
	})
}

// markCritical flags the critical path under n and returns its length (see the package comment).
func markCritical(n *node) int64 {
	n.critical = true
	total := ms(n.end.Sub(n.start)) - covered(n)
	for _, c := range criticalChain(n.children) {
		total += markCritical(c)
	}
	return total
}

// criticalChain is the children of one span that decided when it ended: the child that ended last,
// then the child that ended last before it began, and so on back. Children that ran beside the chain
// (parallel branches that finished sooner) are not in it.
func criticalChain(children []*node) []*node {
	later := func(c, than *node) bool { // does c rank above than, ending later, then longer, then by ID
		switch {
		case than == nil, c.end.After(than.end):
			return true
		case c.end.Equal(than.end):
			cd, td := c.end.Sub(c.start), than.end.Sub(than.start)
			return cd > td || (cd == td && c.span.ID < than.span.ID)
		}
		return false
	}
	var head *node
	for _, c := range children {
		if later(c, head) {
			head = c
		}
	}
	if head == nil {
		return nil
	}
	chain := []*node{head}
	in := map[*node]bool{head: true}
	for cutoff := head.start; ; {
		var prev *node
		for _, c := range children {
			if !in[c] && !c.end.After(cutoff.Add(skewToleranceMs*time.Millisecond)) && later(c, prev) {
				prev = c
			}
		}
		if prev == nil {
			return chain
		}
		in[prev] = true
		chain = append(chain, prev)
		cutoff = prev.start
	}
}

// covered is how long a span's children occupy it, overlaps counted once.
func covered(n *node) int64 {
	if len(n.children) == 0 {
		return 0
	}
	type iv struct{ s, e time.Time }
	ivs := make([]iv, 0, len(n.children))
	for _, c := range n.children {
		ivs = append(ivs, iv{c.start, c.end})
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].s.Before(ivs[j].s) })
	var total time.Duration
	cur := ivs[0]
	for _, v := range ivs[1:] {
		if v.s.After(cur.e) {
			total += cur.e.Sub(cur.s)
			cur = v
			continue
		}
		if v.e.After(cur.e) {
			cur.e = v.e
		}
	}
	total += cur.e.Sub(cur.s)
	return ms(total)
}

// markAttempts numbers retries and fallbacks among each span's children.
func markAttempts(n *node) {
	last := map[string]*node{} // group key -> the previous child of that group
	for _, c := range n.children {
		key := ""
		switch c.span.Kind {
		case models.SpanKindLLM:
			key = "llm"
			if n.span.Name != "router.route" {
				key += "\x00" + c.span.Name
			}
		case models.SpanKindTool:
			key = "tool\x00" + c.span.Name
		}
		if key != "" {
			if prev := last[key]; prev != nil && prev.span.Status == "failed" {
				if prev.attempt == 0 {
					prev.attempt = 1
				}
				c.attempt, c.retryOf = prev.attempt+1, prev.span.ID
			}
			last[key] = c
		}
		markAttempts(c)
	}
}

package executor

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestTruncateMiddleKeepsTheEndsAndSaysWhatItDropped(t *testing.T) {
	long := "START-" + strings.Repeat("x", 100000) + "-END"
	got := truncateMiddle(long, 2000)
	if len(got) > 2000 {
		t.Fatalf("result is %d bytes, over the 2000 limit", len(got))
	}
	if !strings.HasPrefix(got, "START-") || !strings.HasSuffix(got, "-END") {
		t.Fatalf("the start and the end must survive: %q … %q", got[:12], got[len(got)-12:])
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Fatal("it should say that something was left out")
	}
	if short := "small"; truncateMiddle(short, 2000) != short {
		t.Fatal("a result under the limit is untouched")
	}
	if truncateMiddle(long, 0) != long {
		t.Fatal("a limit of 0 means no limit")
	}
}

func TestTruncateMiddleNeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("日本語のテキスト🙂", 5000)
	for _, limit := range []int{200, 333, 1000, 4097} {
		if got := truncateMiddle(s, limit); !utf8.ValidString(got) {
			t.Fatalf("limit %d produced invalid UTF-8", limit)
		}
	}
}

// A conversation of random shape: tool calls come with their results, in order.
func randomConversation(r *rand.Rand, turns int) []models.ChatMessage {
	msgs := []models.ChatMessage{
		{Role: "system", Content: "you are an agent"},
		{Role: "user", Content: "do the long task"},
	}
	for i := 0; i < turns; i++ {
		n := r.Intn(3) // 0 means a plain assistant message
		var calls []models.ToolCallResult
		for k := 0; k < n; k++ {
			id := fmt.Sprintf("c%d_%d", i, k)
			calls = append(calls, models.ToolCallResult{ID: id, Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "t", Arguments: `{"a":1}`}})
		}
		msgs = append(msgs, models.ChatMessage{Role: "assistant", Content: strings.Repeat("a", r.Intn(800)), ToolCalls: calls})
		for _, c := range calls {
			msgs = append(msgs, models.ChatMessage{Role: "tool", ToolCallID: c.ID, Name: "t", Content: strings.Repeat("r", r.Intn(1500))})
		}
		if r.Intn(8) == 0 {
			msgs = append(msgs, models.ChatMessage{Role: "user", Content: "also, " + strings.Repeat("u", r.Intn(300))})
		}
	}
	return msgs
}

// wellFormed: every tool result follows the assistant message that asked for it.
func wellFormed(msgs []models.ChatMessage) error {
	asked := map[string]bool{}
	for i, m := range msgs {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				asked[tc.ID] = true
			}
		case "tool":
			if !asked[m.ToolCallID] {
				return fmt.Errorf("message %d is a result for %q with no request before it", i, m.ToolCallID)
			}
		}
	}
	return nil
}

func TestCompactionNeverSeparatesAToolCallFromItsResults(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for trial := 0; trial < 400; trial++ {
		msgs := randomConversation(r, 5+r.Intn(60))
		budget := 300 + r.Intn(6000)
		base, first, _, middle, tail, ok := compactionSplit(msgs, budget)
		if !ok {
			continue
		}
		// Nothing is lost or reordered: what is kept plus what is summarized is the conversation.
		var rebuilt []models.ChatMessage
		rebuilt = append(rebuilt, base...)
		if first != nil {
			rebuilt = append(rebuilt, *first)
		}
		rebuilt = append(rebuilt, middle...)
		rebuilt = append(rebuilt, tail...)
		if len(rebuilt) != len(msgs) {
			t.Fatalf("trial %d: %d messages in, %d accounted for", trial, len(msgs), len(rebuilt))
		}
		// What goes back to the model must be a valid conversation.
		kept := append(append(append([]models.ChatMessage{}, base...), models.ChatMessage{Role: "system", Content: SummaryPrefix + "s"}), func() []models.ChatMessage {
			if first != nil {
				return append([]models.ChatMessage{*first}, tail...)
			}
			return tail
		}()...)
		if err := wellFormed(kept); err != nil {
			t.Fatalf("trial %d (budget %d, %d msgs, tail %d): %v", trial, budget, len(msgs), len(tail), err)
		}
		if len(tail) == 0 {
			t.Fatalf("trial %d: the most recent turns must be kept", trial)
		}
		if tail[0].Role == "tool" {
			t.Fatalf("trial %d: the tail starts on a tool result", trial)
		}
	}
}

func TestACompactionReplacesThePreviousSummaryInsteadOfStackingThem(t *testing.T) {
	msgs := []models.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "system", Content: SummaryPrefix + "OLD SUMMARY"},
		{Role: "user", Content: "task"},
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, models.ChatMessage{Role: "assistant", Content: strings.Repeat("w", 400)})
	}
	base, _, old, middle, _, ok := compactionSplit(msgs, 1000)
	if !ok || old != "OLD SUMMARY" {
		t.Fatalf("ok=%v old=%q", ok, old)
	}
	if len(base) != 1 || base[0].Content != "prompt" {
		t.Fatalf("only the real system prompt is kept as is: %+v", base)
	}
	for _, m := range middle {
		if isSummary(m) {
			t.Fatal("the old summary is passed separately, not as part of the middle")
		}
	}
}

func TestStallTrackerCountsRecentIdenticalCalls(t *testing.T) {
	s := &stallTracker{limit: 5}
	a := callSignature(ToolCall{Name: "search", Arguments: map[string]interface{}{"q": "x", "n": 1.0}})
	same := callSignature(ToolCall{Name: "search", Arguments: map[string]interface{}{"n": 1.0, "q": "x"}})
	other := callSignature(ToolCall{Name: "search", Arguments: map[string]interface{}{"q": "y"}})
	if a != same {
		t.Fatal("argument order must not matter")
	}
	if s.note(a) != 1 || s.note(other) != 1 || s.note(same) != 2 {
		t.Fatal("identical calls are counted together, different ones apart")
	}
	for i := 0; i < stallWindow; i++ {
		s.note(other)
	}
	if n := s.note(a); n != 1 {
		t.Fatalf("calls older than the window stop counting, got %d", n)
	}
	if s.warnAt() != 3 {
		t.Fatalf("warning at half the limit, got %d", s.warnAt())
	}
}

func TestNotesToolSetsAppendsReadsAndEnforcesItsSize(t *testing.T) {
	e := &Executor{}
	n := newNotes(nil)
	ctx := withNotes(context.Background(), n)
	call := func(args map[string]interface{}) ToolResult {
		return e.executeNotes(ctx, ToolCall{ID: "1", Name: notesToolName, Arguments: args})
	}

	if r := call(map[string]interface{}{"action": "set", "section": "plan", "text": "1. look\n2. fix"}); r.IsError {
		t.Fatal(r.Content)
	}
	call(map[string]interface{}{"action": "append", "section": "plan", "text": "3. verify"})
	if r := call(map[string]interface{}{"action": "read", "section": "plan"}); r.Content != "1. look\n2. fix\n3. verify" {
		t.Fatalf("got %q", r.Content)
	}
	if r := call(map[string]interface{}{"action": "set", "section": "nonsense", "text": "x"}); !r.IsError {
		t.Fatal("an unknown section is an error")
	}
	if r := call(map[string]interface{}{"action": "set", "section": "progress", "text": strings.Repeat("z", maxNotesBytes+1)}); !r.IsError {
		t.Fatal("notes beyond the size limit are refused")
	}
	if r := call(map[string]interface{}{"action": "read", "section": "progress"}); r.Content != "" {
		t.Fatal("a refused write must change nothing")
	}
	if r := (&Executor{}).executeNotes(context.Background(), ToolCall{ID: "1", Name: notesToolName, Arguments: map[string]interface{}{"action": "read"}}); !r.IsError {
		t.Fatal("without a notes store the tool says so")
	}
}

func TestPinnedNotesRemindAnAgentWithoutAPlan(t *testing.T) {
	n := newNotes(nil)
	if !strings.Contains(n.render(1, true), "write a plan") {
		t.Fatal("a plan-and-execute agent is told to plan first")
	}
	if strings.Contains(n.render(1, false), "no plan in your notes") {
		t.Fatal("no reminder before turn 3 for other strategies")
	}
	if !strings.Contains(n.render(3, false), "no plan in your notes") {
		t.Fatal("from turn 3 an agent without a plan is reminded")
	}
	n.sections["plan"] = "do things"
	if strings.Contains(n.render(5, false), "no plan in your notes") || !strings.Contains(n.render(5, false), "do things") {
		t.Fatal("with a plan there is no reminder, and the plan is shown")
	}
}

func TestWhichModelErrorsAreWorthRetrying(t *testing.T) {
	for msg, want := range map[string]bool{
		"429 too many requests":                  true,
		"503 service unavailable":                true,
		"read tcp: connection reset by peer":     true,
		"401 unauthorized":                       false,
		"invalid api key provided":               false,
		"400 bad request: invalid_request_error": false,
		"maximum context length exceeded":        false,
	} {
		if got := retriable(fmt.Errorf("%s", msg)); got != want {
			t.Errorf("%q: retriable = %v, want %v", msg, got, want)
		}
	}
	if retriable(context.Canceled) || retriable(nil) {
		t.Fatal("a cancelled call is not retried")
	}
}

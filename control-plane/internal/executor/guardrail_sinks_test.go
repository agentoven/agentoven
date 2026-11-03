package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A secret planted in what the model says must not be found in anything a run keeps once a rule has dealt with
// it: the capture of the call, the journal, the trace the caller gets, the session that is replayed to the model
// next turn (review 5, findings 4 and 5). sinkRig runs one agent and reads every sink back as text.
const plantedKey = "sk-abcdefghijklmnopqrstuvwxyz0123"

type sinkRig struct {
	e       *executor.Executor
	d       *scriptedDriver
	rec     *callRecorder
	dir     string
	session contracts.SessionStore
	ran     *int // how often the email tool actually ran
}

func newSinkRig(t *testing.T, d *scriptedDriver) *sinkRig {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d)
	_ = s.CreateProvider(context.Background(), &models.ModelProvider{Name: "p", Kitchen: "default", Kind: "scripted", Models: []string{"test-model"}, IsDefault: true})
	rec := &callRecorder{}
	mr.SetCallCapturer(rec)
	gw := newFakeGateway()
	ran := new(int)
	gw.register("send_email", func(map[string]interface{}) (string, bool) { *ran++; return "ok", false })
	sess := sessions.NewMemorySessionStore()
	e := executor.NewExecutor(s, mr, gw, sess)
	e.SetGuardrails(&guardrails.CommunityGuardrailService{})
	dir := t.TempDir()
	e.SetJournal(mustJournal(t, dir))
	return &sinkRig{e: e, d: d, rec: rec, dir: dir, session: sess, ran: ran}
}

// sinks is everything the run kept, as one string.
func (r *sinkRig) sinks(t *testing.T, trace *executor.ExecutionTrace) string {
	t.Helper()
	var all strings.Builder
	b, _ := json.Marshal(trace)
	all.Write(b)
	for _, c := range r.rec.calls {
		b, _ := json.Marshal(c)
		all.Write(b)
	}
	_ = filepath.Walk(r.dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	if trace != nil && trace.SessionID != "" {
		if s, err := r.session.GetSession(context.Background(), trace.SessionID); err == nil {
			b, _ := json.Marshal(s)
			all.Write(b)
		}
	}
	return all.String()
}

func secretRule(action models.GuardrailAction) models.Guardrail {
	return piiRule("keys", models.GuardrailStageAll, action, "secrets", "iban")
}

func TestARedactedReplyIsKeptNowhereInItsOriginalForm(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{{content: "your key is " + plantedKey + " and iban " + testIBAN}}}
	r := newSinkRig(t, d)
	agent := testAgent()
	agent.Guardrails = []models.Guardrail{secretRule(models.GuardrailActionRedact)}
	reply, trace, err := r.e.Execute(context.Background(), agent, "hi", resolvedWithTools(), nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, plantedKey) || strings.Contains(reply, testIBAN) {
		t.Fatalf("the caller got the original: %s", reply)
	}
	if all := r.sinks(t, trace); strings.Contains(all, plantedKey) || strings.Contains(all, testIBAN) {
		t.Fatalf("a sink kept the original: %s", all)
	}
	if !trace.OutputJudged {
		t.Fatal("the run judged its replies, so a handler need not ask the rules again")
	}
}

func TestTextBesideAToolCallIsJudgedAndABlockedOneEndsTheRunWithNothingKept(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{
		{content: "Sure. key " + plantedKey, toolCalls: []models.ToolCallResult{toolCall("1", "send_email", map[string]interface{}{"to": "x"})}},
		{content: "done"},
	}}
	r := newSinkRig(t, d)
	agent := testAgent()
	agent.Guardrails = []models.Guardrail{secretRule(models.GuardrailActionBlock)}
	reply, trace, err := r.e.Execute(context.Background(), agent, "hi", resolvedWithTools(emailTool()), nil, false, "")
	var blocked *models.GuardrailBlockedError
	if !errors.As(err, &blocked) || reply != "" {
		t.Fatalf("a blocked turn ends the run with a refusal and no text: %q %v", reply, err)
	}
	if d.n != 1 {
		t.Fatalf("the model is not asked again after a refusal (%d calls)", d.n)
	}
	if all := r.sinks(t, trace); strings.Contains(all, plantedKey) {
		t.Fatalf("a sink kept the blocked text: %s", all)
	}
}

func TestARedactedTurnBesideToolCallsNeverReachesTheNextModelCall(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{
		{content: "key " + plantedKey, toolCalls: []models.ToolCallResult{toolCall("1", "send_email", map[string]interface{}{"to": "x"})}},
		{content: "done"},
	}}
	r := newSinkRig(t, d)
	agent := testAgent()
	agent.Guardrails = []models.Guardrail{secretRule(models.GuardrailActionRedact)}
	_, trace, err := r.e.Execute(context.Background(), agent, "hi", resolvedWithTools(emailTool()), nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range d.requests {
		if b, _ := json.Marshal(req.Messages); strings.Contains(string(b), plantedKey) {
			t.Fatalf("the model was sent the original text back: %s", b)
		}
	}
	if all := r.sinks(t, trace); strings.Contains(all, plantedKey) {
		t.Fatalf("a sink kept the original: %s", all)
	}
}

// A tool call a blocking rule refuses was left whole in every sink (and, with a redact rule beside it, was the
// only thing the redaction did not reach). Its arguments are a placeholder everywhere, and the call is refused
// again even if the run paused and was resumed with the placeholder in the journal.
func TestTheArgumentsOfABlockedToolCallAreKeptNowhere(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("1", "send_email", map[string]interface{}{"body": "pay " + testIBAN})}},
		{content: "I could not send it"},
	}}
	r := newSinkRig(t, d)
	agent := testAgent()
	agent.Guardrails = []models.Guardrail{piiRule("block", models.GuardrailStageAll, models.GuardrailActionBlock, "iban")}
	_, trace, err := r.e.Execute(context.Background(), agent, "hi", resolvedWithTools(emailTool()), nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if *r.ran != 0 {
		t.Fatal("a blocked call must not run")
	}
	if all := r.sinks(t, trace); strings.Contains(all, testIBAN) {
		t.Fatalf("a sink kept the arguments of a blocked call: %s", all)
	}
	for _, req := range d.requests {
		if b, _ := json.Marshal(req.Messages); strings.Contains(string(b), testIBAN) {
			t.Fatalf("the model was sent the blocked arguments back: %s", b)
		}
	}
	if !trace.Turns[0].ToolCalls[0].Refused || !trace.Turns[0].ToolResults[0].IsError {
		t.Fatalf("the call is marked refused and its result is an error: %+v", trace.Turns[0])
	}
}

func TestAReplyBlockedAtTheFinalTurnIsNotInTheSession(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{{content: "ok"}, {content: "key " + plantedKey}, {content: "fine"}}}
	r := newSinkRig(t, d)
	agent := testAgent()
	agent.Guardrails = []models.Guardrail{secretRule(models.GuardrailActionBlock)}
	_, t1, err := r.e.Execute(context.Background(), agent, "first", resolvedWithTools(), nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.e.Execute(context.Background(), agent, "second", resolvedWithTools(), nil, false, t1.SessionID); !errors.Is(err, models.ErrGuardrailBlocked) {
		t.Fatalf("the second turn is refused: %v", err)
	}
	if _, _, err := r.e.Execute(context.Background(), agent, "third", resolvedWithTools(), nil, false, t1.SessionID); err != nil {
		t.Fatal(err)
	}
	for _, req := range d.requests {
		if b, _ := json.Marshal(req.Messages); strings.Contains(string(b), plantedKey) {
			t.Fatalf("a refused reply was replayed to the model: %s", b)
		}
	}
	s, _ := r.session.GetSession(context.Background(), t1.SessionID)
	if b, _ := json.Marshal(s); strings.Contains(string(b), plantedKey) {
		t.Fatalf("the session kept the refused reply: %s", b)
	}
}

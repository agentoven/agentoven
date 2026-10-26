package router_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

// The skill verifier calls a model on the platform's own behalf. Its trace must be findable (it is the
// skill verifier's, not an anonymous "router" call) and have spans, whether or not the caller pinned
// a provider.
func TestSkillVerificationLeavesANamedTraceWithSpans(t *testing.T) {
	srv := openAIServer(t, `{"choices":[{"message":{"content":"{\"verdict\":\"accept\",\"reasoning\":\"does what it says\"}"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":300,"completion_tokens":20,"total_tokens":320}}`, nil)
	mr, st := routerWith(t, nil,
		models.ModelProvider{Name: "judge", Kind: "openai", Endpoint: srv.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}})
	manifest := &models.SkillManifest{Name: "refund-helper", Description: "helps with refunds", Instructions: "Be polite."}
	bundle := skills.Bundle{skills.ManifestFilename: []byte("---\nname: refund-helper\n---\nBe polite.")}

	for name, run := range map[string]func() (skills.Verdict, error){
		"unpinned": func() (skills.Verdict, error) {
			v, _, _, err := skills.VerifyIntent(context.Background(), mr, "default", manifest, bundle)
			return v, err
		},
		"pinned": func() (skills.Verdict, error) {
			v, _, _, err := skills.VerifyIntentWithProvider(context.Background(), mr, "default", "judge", manifest, bundle)
			return v, err
		},
	} {
		before, _ := st.ListTraces(context.Background(), "default", 50)
		v, err := run()
		if err != nil || v != skills.VerdictAccept {
			t.Fatalf("%s: %v %v", name, v, err)
		}
		after, _ := st.ListTraces(context.Background(), "default", 50)
		if len(after) != len(before)+1 {
			t.Fatalf("%s: traces %d -> %d", name, len(before), len(after))
		}
		var tr *models.Trace
		for i := range after {
			seen := false
			for _, b := range before {
				if b.ID == after[i].ID {
					seen = true
				}
			}
			if !seen {
				tr, _ = st.GetTraceWithSpans(context.Background(), after[i].ID)
			}
		}
		if tr.AgentName != "skill-verifier" || tr.Metadata["skill"] != "refund-helper" || tr.Metadata["purpose"] != "skill_verification" {
			t.Errorf("%s: trace = %+v", name, tr)
		}
		if len(tr.Spans) < 2 || tr.Spans[0].Metadata["skill"] != "refund-helper" {
			t.Errorf("%s: spans = %+v", name, tr.Spans)
		}
		var llm *models.Span
		for i := range tr.Spans {
			if tr.Spans[i].Kind == models.SpanKindLLM {
				llm = &tr.Spans[i]
			}
		}
		if llm == nil || llm.Provider != "judge" || llm.Model != "gpt-4o" || !strings.Contains(string(llm.Input), "security reviewer") || !strings.Contains(string(llm.Output), "accept") {
			t.Errorf("%s: llm span = %+v", name, llm)
		}
	}
}

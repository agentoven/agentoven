package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Fields the full type has and the public one does not, with why. A field added to Trace, Span,
// TraceScore or Session must be put here or carried by the public type: the test fails until someone
// has decided, because a field that is carried by accident is a leak (ADR-0043).
// (spans, scores and events are carried, as PublicSpan, PublicScore and PublicSpanEvent.)
var withheldFields = map[string]map[string]string{
	"Trace": {
		"input_text": "what the user typed", "output_text": "what the model answered", "thinking_blocks": "the model's reasoning",
	},
	"Span": {
		"input": "the request payload", "output": "the response payload",
	},
	"TraceScore": {"comment": "what a reviewer wrote about the content"},
	"Session":    {"messages": "the conversation", "metadata": "free-form, written by the caller"},
}

func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		if n := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]; n != "" && n != "-" {
			out[n] = true
		}
	}
	return out
}

func TestEveryFieldOfAContentTypeIsEitherCarriedOrWithheld(t *testing.T) {
	for _, c := range []struct {
		name         string
		full, public reflect.Type
	}{
		{"Trace", reflect.TypeOf(Trace{}), reflect.TypeOf(PublicTrace{})},
		{"Span", reflect.TypeOf(Span{}), reflect.TypeOf(PublicSpan{})},
		{"TraceScore", reflect.TypeOf(TraceScore{}), reflect.TypeOf(PublicScore{})},
		{"Session", reflect.TypeOf(Session{}), reflect.TypeOf(PublicSession{})},
	} {
		pub := jsonFields(c.public)
		for name := range jsonFields(c.full) {
			_, withheld := withheldFields[c.name][name]
			if pub[name] == withheld {
				t.Errorf("%s field %q must be either carried by %s or listed in withheldFields with a reason", c.name, name, c.public.Name())
			}
		}
		for name := range pub {
			if name == "content_withheld" {
				continue
			}
			if !jsonFields(c.full)[name] {
				t.Errorf("%s has %q, which %s does not: a public type carries only what the full one has", c.public.Name(), name, c.full.Name())
			}
		}
	}
}

func TestPublicMetadataKeepsLabelsAndDropsText(t *testing.T) {
	now := time.Now()
	tr := Trace{ID: "t", Metadata: map[string]interface{}{
		"model": "gpt-x", "turns": 3, "cache_hit": true, "thinking_tokens": float64(12),
		"error": "provider said: SECRET", "input": "SECRET", "output": "SECRET", "thinking_blocks": []string{"SECRET"},
		"unknown_future_key": "SECRET", "model_long": strings.Repeat("x", 500),
		"source":     strings.Repeat("S", maxPublicMetadataString+1), // allowed key, but a value this long is text
		"provider":   map[string]interface{}{"nested": "SECRET"},     // allowed key, not a scalar
		"guardrails": map[string]interface{}{"redacted": 2, "warned": float64(1), "rule": "SECRET-RULE-TEXT"},
	}, RuntimeMetadata: map[string]interface{}{"input": "SECRET", "runtime": "agentoven"}, CreatedAt: now}
	raw, _ := json.Marshal(tr.Public())
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "xxxx") {
		t.Fatalf("public metadata leaks: %s", raw)
	}
	for _, want := range []string{`"model":"gpt-x"`, `"turns":3`, `"cache_hit":true`, `"redacted":2`, `"warned":1`, `"runtime":"agentoven"`, `"content_withheld":true`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("public trace lost %s: %s", want, raw)
		}
	}
}

func TestPublicSpanKeepsTheShapeAndDropsTheContent(t *testing.T) {
	now := time.Now()
	sp := Span{ID: "s", TraceID: "t", Name: "llm", Status: "failed", Input: json.RawMessage(`"SECRET"`), Output: json.RawMessage(`"SECRET"`),
		Error: "SECRET provider echo", Events: []SpanEvent{{Name: "first token", Timestamp: now, Attributes: map[string]interface{}{"x": "SECRET"}}},
		Metadata: map[string]interface{}{"tool_call_id": "c1", "error": "SECRET"}}
	raw, _ := json.Marshal(sp.Public())
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("%s", raw)
	}
	if !strings.Contains(string(raw), WithheldError) || !strings.Contains(string(raw), `"first token"`) || !strings.Contains(string(raw), `"tool_call_id":"c1"`) {
		t.Fatalf("a failed span still says that it failed, and keeps its event names: %s", raw)
	}
	if ok := (Span{ID: "ok"}).Public(); ok.Error != "" {
		t.Fatal("a span that did not fail has no error to show")
	}
}

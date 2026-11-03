package guardrails

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

func lgRule(endpoint string, extra map[string]interface{}) models.Guardrail {
	cfg := map[string]interface{}{"endpoint": endpoint, "timeout_secs": float64(2)}
	for k, v := range extra {
		cfg[k] = v
	}
	return models.Guardrail{ID: "lg", Kind: models.GuardrailLlamaGuard, Stage: models.GuardrailStageAll, Enabled: true, Config: cfg, KitchenID: "k1"}
}

func lgVerdict(reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":` + quote(reply) + `}}]}`))
	}))
}

func quote(s string) string { return `"` + strings.NewReplacer("\n", `\n`, `"`, `\"`).Replace(s) + `"` }

// Review 5, finding 6: the control plane POSTed the conversation, with the rule's key, to any
// address a baker wrote in a rule, and the answer came back in the refusal.
func TestLlamaGuardDoesNotReachPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"content":"unsafe\ns1-leaked-from-internal"}}]}`))
	}))
	defer srv.Close()
	res := evalLlamaGuard(lgRule(srv.URL, map[string]interface{}{"api_key": "K-123"}), "secret text", "input")
	if res.Passed || hits.Load() != 0 {
		t.Fatalf("a loopback endpoint must be refused before any request is made (hits %d): %+v", hits.Load(), res)
	}
	if strings.Contains(res.Message, "leaked") || strings.Contains(res.Message, "127.0.0.1") {
		t.Fatalf("the refusal must not carry anything of the endpoint: %q", res.Message)
	}
}

func TestLlamaGuardErrorsAreUniform(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	closed := func() string { // a port nothing listens on
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := l.Addr().String()
		l.Close()
		return "http://" + addr
	}()
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) }))
	defer hang.Close()
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>secret internal page</html>")) }))
	defer junk.Close()
	errs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"db password is hunter2"}}`))
	}))
	defer errs.Close()
	var msgs []string
	for _, ep := range []string{closed, hang.URL, junk.URL, errs.URL, "http://169.254.169.254", "http://10.255.255.1:81"} {
		res := evalLlamaGuard(lgRule(ep, nil), "x", "input")
		if res.Passed {
			t.Fatalf("%s: fail closed by default", ep)
		}
		msgs = append(msgs, res.Message)
	}
	for _, m := range msgs[1:] {
		if m != msgs[0] {
			t.Fatalf("every failure must read the same, got %q and %q", msgs[0], m)
		}
	}
	if strings.ContainsAny(msgs[0], "0123456789") || strings.Contains(msgs[0], "hunter2") {
		t.Fatalf("no address, port or body in the message: %q", msgs[0])
	}
}

func TestLlamaGuardVerdictIsParsedStrictly(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	for _, c := range []struct {
		reply  string
		passed bool
		want   string
	}{
		{"safe", true, ""},
		{"unsafe\nS1,S10", false, "(categories: s1,s10)"},
		{"unsafe\ns1-leaked-from-internal", false, ""}, // free text from the endpoint is never repeated
		{"I am not sure", false, ""},                   // neither word: no verdict
	} {
		srv := lgVerdict(c.reply)
		res := evalLlamaGuard(lgRule(srv.URL, nil), "x", "input")
		srv.Close()
		if res.Passed != c.passed || (c.want != "" && !strings.Contains(res.Message, c.want)) || strings.Contains(res.Message, "leaked") || strings.Contains(res.Message, "not sure") {
			t.Errorf("%q: %+v", c.reply, res)
		}
	}
}

func TestLlamaGuardCapsTheResponseAndDoesNotFollowRedirects(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"content":"safe"}}]}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if res := evalLlamaGuard(lgRule(redirect.URL, nil), "x", "input"); res.Passed || followed.Load() != 0 {
		t.Fatalf("a redirect is not followed (the key would be replayed elsewhere): %+v followed=%d", res, followed.Load())
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"safe` + strings.Repeat(" ", 1<<20) + `"}}]}`))
	}))
	defer big.Close()
	if res := evalLlamaGuard(lgRule(big.URL, nil), "x", "input"); res.Passed {
		t.Fatalf("a response over the cap is no verdict: %+v", res)
	}
}

func TestLlamaGuardFailOpenStaysOptIn(t *testing.T) {
	res := evalLlamaGuard(lgRule("http://127.0.0.1:1", map[string]interface{}{"fail_open": true}), "x", "input")
	if !res.Passed {
		t.Fatalf("fail_open passes on a refused endpoint: %+v", res)
	}
}

func TestValidateRefusesRulesThatCannotWork(t *testing.T) {
	long := make([]interface{}, MaxListEntries+1)
	for i := range long {
		long[i] = "w"
	}
	for name, g := range map[string]models.Guardrail{
		"typo in kind":         {Kind: "llama_guard", Config: map[string]interface{}{"endpoint": "http://x"}},
		"bad regex":            {Kind: models.GuardrailRegexFilter, Config: map[string]interface{}{"pattern": "(unclosed"}},
		"too many words":       {Kind: models.GuardrailContentFilter, Config: map[string]interface{}{"blocked_words": long}},
		"llamaguard file url":  lgRule("file:///etc/passwd", nil),
		"llamaguard userinfo":  lgRule("http://u:p@example.com", nil),
		"llamaguard no host":   lgRule("http://", nil),
		"llamaguard timeout":   lgRule("http://example.com", map[string]interface{}{"timeout_secs": float64(9999)}),
		"llamaguard fail_open": lgRule("http://example.com", map[string]interface{}{"fail_open": "yes"}),
	} {
		if err := Validate(g); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if err := Validate(lgRule("https://guard.example.com", nil)); err != nil {
		t.Errorf("a plain endpoint is fine: %v", err)
	}
}

func TestEvaluateOneFailsClosedOnAnUnknownKind(t *testing.T) {
	res := evaluateOne(models.Guardrail{Kind: "llama_guard"}, "x", "input", nil)
	if res.Passed {
		t.Fatal("an unknown kind must not pass")
	}
	re := evaluateOne(models.Guardrail{Kind: models.GuardrailRegexFilter, Config: map[string]interface{}{"pattern": "(unclosed"}}, "x", "input", nil)
	if re.Passed {
		t.Fatal("a pattern that does not compile must not pass")
	}
}

func TestEffectiveRefusesAnAgentRuleThatCannotWork(t *testing.T) {
	bad := models.Guardrail{ID: "typo", Kind: "llama_guard", Enabled: true}
	if _, err := Effective(nil, nil, "k", "a", []models.Guardrail{bad}); err == nil {
		t.Fatal("an agent's own rule is validated like a workspace rule")
	}
	bad.Enabled = false
	if _, err := Effective(nil, nil, "k", "a", []models.Guardrail{bad}); err != nil {
		t.Fatalf("a disabled rule is never evaluated: %v", err)
	}
}

func TestSavingARuleRefusesAnAddressTheGuardWouldRefuseButDoesNotResolveNames(t *testing.T) {
	s := &CommunityGuardrailService{}
	for _, ep := range []string{"http://169.254.169.254", "http://10.0.0.5:8000", "http://127.0.0.1:18493", "http://[::1]:80", "http://2130706433", "http://0x7f.1"} {
		if err := s.ValidateGuardrail(lgRule(ep, nil)); err == nil {
			t.Errorf("%s must be refused when the rule is saved", ep)
		}
	}
	// A name is judged when it is dialled, so saving does not tell the caller which internal names exist.
	for _, ep := range []string{"https://guard.example.com", "http://ollama:11434", "http://metadata.google.internal", "http://cafe.de", "http://bad.fed:8080"} {
		if err := s.ValidateGuardrail(lgRule(ep, nil)); err != nil {
			t.Errorf("%s: %v", ep, err)
		}
	}
	t.Setenv(netguard.EnvAllow, "10.0.0.5")
	if err := s.ValidateGuardrail(lgRule("http://10.0.0.5:8000", nil)); err != nil {
		t.Errorf("an allowlisted on-premises address is accepted: %v", err)
	}
}

package toolbroker

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newBroker(secret string) (*Broker, *clock) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	return New([]byte(secret), c.now), c
}

func TestATokenNamesItsKitchenAgentAndRunAndNothingElse(t *testing.T) {
	b, _ := newBroker("s")
	tok, done := b.Open("k1", "triager", "run-9")
	defer done()
	g, err := b.Admit(tok)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Done(0)
	if g.Claims.Kitchen != "k1" || g.Claims.Agent != "triager" || g.Claims.Run != "run-9" {
		t.Fatalf("claims: %+v", g.Claims)
	}
}

func TestForgedTamperedAndForeignTokensAreRefused(t *testing.T) {
	b, _ := newBroker("s")
	other, _ := newBroker("another secret")
	tok, done := b.Open("k1", "a", "r")
	defer done()
	foreign, doneF := other.Open("k1", "a", "r")
	defer doneF()
	payload := strings.SplitN(strings.TrimPrefix(tok, TokenPrefix), ".", 2)
	// The payload of one token with the signature of another: a different kitchen, same agent.
	tokB, doneB := b.Open("k2", "a", "r")
	defer doneB()
	sigB := strings.SplitN(strings.TrimPrefix(tokB, TokenPrefix), ".", 2)[1]
	for name, bad := range map[string]string{
		"empty":           "",
		"no prefix":       strings.TrimPrefix(tok, TokenPrefix),
		"other prefix":    "aogw_" + strings.TrimPrefix(tok, TokenPrefix),
		"foreign key":     foreign,
		"spliced":         TokenPrefix + payload[0] + "." + sigB,
		"truncated":       tok[:len(tok)-4],
		"no signature":    TokenPrefix + payload[0],
		"huge":            TokenPrefix + strings.Repeat("a", 4000),
		"not base64":      TokenPrefix + "!!!.???",
		"signature of {}": TokenPrefix + "e30." + sigB,
	} {
		if _, err := b.Admit(bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestATokenExpiresAndANewOneIsNeeded(t *testing.T) {
	b, c := newBroker("s")
	tok, done := b.Open("k", "a", "r")
	defer done()
	c.add(DefaultTTL + time.Second)
	if _, err := b.Admit(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("an expired token: %v", err)
	}
}

func TestClosingTheScopeEndsTheTokenAtOnce(t *testing.T) {
	b, _ := newBroker("s")
	tok, done := b.Open("k", "a", "r")
	g, err := b.Admit(tok)
	if err != nil {
		t.Fatal(err)
	}
	g.Done(0)
	done()
	if _, err := b.Admit(tok); !errors.Is(err, ErrScopeClosed) {
		t.Fatalf("a replay after the call ended must be refused: %v", err)
	}
	if g.Open() {
		t.Fatal("a call that waited must see its scope closed")
	}
}

func TestAScopeHasACallCapAndAConcurrencyCapAndAByteCap(t *testing.T) {
	t.Setenv("AGENTOVEN_TOOL_SCOPE_MAX_CALLS", "3")
	b, _ := newBroker("s")
	tok, done := b.Open("k", "a", "r")
	defer done()
	for i := 0; i < 3; i++ {
		g, err := b.Admit(tok)
		if err != nil {
			t.Fatal(err)
		}
		g.Done(0)
	}
	if _, err := b.Admit(tok); !errors.Is(err, ErrBudget) {
		t.Fatalf("the fourth call: %v", err)
	}

	t.Setenv("AGENTOVEN_TOOL_SCOPE_MAX_CALLS", "")
	b2, _ := newBroker("s")
	tok2, done2 := b2.Open("k", "a", "r")
	defer done2()
	var held []*Grant
	for i := 0; i < MaxConcurrent; i++ {
		g, err := b2.Admit(tok2)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, g)
	}
	if _, err := b2.Admit(tok2); !errors.Is(err, ErrBusy) {
		t.Fatalf("too many in flight: %v", err)
	}
	held[0].Done(MaxScopeBytes) // one result as large as the whole allowance
	if _, err := b2.Admit(tok2); !errors.Is(err, ErrBudget) {
		t.Fatalf("a scope that took out its bytes: %v", err)
	}
}

func TestNoSecretMeansNoTokensAndNoAcceptance(t *testing.T) {
	b := New(nil, nil)
	tok, done := b.Open("k", "a", "r")
	done()
	if tok != "" || b.Enabled() {
		t.Fatal("without a key nothing is minted")
	}
	if _, err := b.Admit(TokenPrefix + "x.y"); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	var nilBroker *Broker
	if nilBroker.Enabled() {
		t.Fatal("a nil broker is disabled")
	}
}

func TestATokenSeenByAnotherReplicaIsJudgedBySignatureAndExpiryAndCountedFromThere(t *testing.T) {
	t.Setenv("AGENTOVEN_TOOL_SCOPE_MAX_CALLS", "2")
	a, clk := newBroker("shared")
	b := New([]byte("shared"), clk.now) // another replica: same secret, no memory of the scope
	tok, done := a.Open("k", "a", "r")
	defer done()
	for i := 0; i < 2; i++ {
		g, err := b.Admit(tok)
		if err != nil {
			t.Fatal(err)
		}
		g.Done(0)
	}
	if _, err := b.Admit(tok); !errors.Is(err, ErrBudget) {
		t.Fatalf("the replica that adopted the scope enforces its cap: %v", err)
	}
	clk.add(DefaultTTL + time.Second)
	if _, err := b.Admit(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("and the expiry: %v", err)
	}
}

func TestTheRegistryDoesNotGrowWithoutLimit(t *testing.T) {
	b, c := newBroker("s")
	for i := 0; i < 50; i++ {
		_, done := b.Open("k", "a", "r")
		done()
	}
	c.add(DefaultTTL + 2*time.Minute)
	b.Open("k", "a", "r")
	b.mu.Lock()
	n := len(b.scopes)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("expired scopes are swept: %d left", n)
	}
}

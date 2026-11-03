// Package toolbroker issues and checks the credential a hosted agent process uses to ask the control plane to
// run a tool call for it (ADR-0044).
//
// A process the platform launched (the embedded agent_runner.py) used to call every tool's endpoint itself, which
// bypassed the approval policy, the guardrails, the egress guard and the audit, and could not reach a native
// ("pack://") tool at all. It now posts the call to the control plane, presenting a token the control plane gave it
// for the one call it is handling. The token is not a user identity and opens nothing but that route.
package toolbroker

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Header carries the token on the control plane's relayed call to a process.
	Header = "X-AgentOven-Tool-Token"
	// TokenPrefix marks the credential. It is not shared with any other credential of the platform, so none of them
	// can be presented as this one.
	TokenPrefix = "aotc_"

	keyLabel   = "agentoven-tool-broker-key-v1"
	tokenLabel = "agentoven-tool-broker-token-v1|"

	// DefaultTTL bounds how long a token is good for, whatever the scope's state. It is longer than most relayed calls
	// and shorter than a working day: the scope closing is the normal end, this is the backstop.
	DefaultTTL = 15 * time.Minute
	// MaxTTL is the most AGENTOVEN_TOOL_TOKEN_TTL may ask for.
	MaxTTL = time.Hour
	// DefaultMaxCalls is how many tool calls one scope (one relayed call to a process) may make.
	DefaultMaxCalls = 200
	// MaxConcurrent is how many calls of one scope may be in flight at once.
	MaxConcurrent = 8
	// MaxScopeBytes bounds the result bytes one scope may take out of the control plane.
	MaxScopeBytes = 32 << 20
	// maxScopes bounds the registry: a flood of relayed calls cannot grow it without limit.
	maxScopes = 20000
)

// Refusals. Each maps to a typed error kind in the route's answer.
var (
	ErrMalformed   = errors.New("malformed credential")
	ErrSignature   = errors.New("credential signature does not match")
	ErrExpired     = errors.New("credential has expired")
	ErrScopeClosed = errors.New("the call this credential was issued for has ended")
	ErrBudget      = errors.New("this call has used up the tool calls it may make")
	ErrBusy        = errors.New("too many tool calls of this call are running at once")
	ErrDisabled    = errors.New("tool calls for hosted processes are not enabled on this server")
)

// Claims are what a token says. The route takes who is calling from here and from nowhere else.
type Claims struct {
	V       int    `json:"v"`
	Kitchen string `json:"k"`
	Agent   string `json:"a"`
	Run     string `json:"r"`
	ID      string `json:"j"` // unique per mint
	Expires int64  `json:"e"` // unix seconds
}

type scope struct {
	claims   Claims
	closed   bool
	calls    int
	inflight int
	bytes    int64
}

// Broker mints tokens and tracks the scopes they were minted for. The scopes are in this process's memory: a token
// whose scope this replica never opened (another replica relayed the call, or this one restarted) is judged by its
// signature and its expiry alone, and gets a scope here from the moment it is first seen.
type Broker struct {
	key      []byte
	now      func() time.Time
	ttl      time.Duration
	maxCalls int

	mu     sync.Mutex
	scopes map[string]*scope
}

// New creates a broker whose key is derived from secret (the server's A2A secret). With no secret nothing is minted and
// every token is refused. AGENTOVEN_TOOL_TOKEN_TTL and AGENTOVEN_TOOL_SCOPE_MAX_CALLS tune the limits.
func New(secret []byte, now func() time.Time) *Broker {
	b := &Broker{now: now, ttl: DefaultTTL, maxCalls: DefaultMaxCalls, scopes: map[string]*scope{}}
	if b.now == nil {
		b.now = time.Now
	}
	if len(secret) > 0 {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(keyLabel))
		b.key = mac.Sum(nil)
	}
	if d, err := time.ParseDuration(os.Getenv("AGENTOVEN_TOOL_TOKEN_TTL")); err == nil && d > 0 {
		b.ttl = d
		if b.ttl > MaxTTL {
			b.ttl = MaxTTL
		}
	}
	if n, err := strconv.Atoi(os.Getenv("AGENTOVEN_TOOL_SCOPE_MAX_CALLS")); err == nil && n > 0 {
		b.maxCalls = n
	}
	return b
}

// Enabled reports whether tokens can be minted and checked.
func (b *Broker) Enabled() bool { return b != nil && len(b.key) > 0 }

func (b *Broker) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte(tokenLabel))
	mac.Write(payload)
	return mac.Sum(nil)
}

// Open mints a token for one relayed call of agent in kitchen, under run, and registers its scope. The returned func
// closes the scope; call it when the relayed call returns. With the broker disabled it returns an empty token and a
// no-op, and the process then has no way to run a tool (it fails closed).
func (b *Broker) Open(kitchen, agent, run string) (string, func()) {
	if !b.Enabled() {
		return "", func() {}
	}
	var jti [12]byte
	if _, err := rand.Read(jti[:]); err != nil {
		return "", func() {}
	}
	c := Claims{V: 1, Kitchen: kitchen, Agent: agent, Run: run, ID: hex.EncodeToString(jti[:]), Expires: b.now().Add(b.ttl).Unix()}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", func() {}
	}
	tok := TokenPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(b.sign(payload))
	b.mu.Lock()
	b.sweepLocked()
	if len(b.scopes) < maxScopes {
		b.scopes[c.ID] = &scope{claims: c}
	}
	b.mu.Unlock()
	return tok, func() {
		b.mu.Lock()
		if s := b.scopes[c.ID]; s != nil {
			s.closed = true
		}
		b.mu.Unlock()
	}
}

// Verify checks signature and expiry only (constant-time comparison).
func (b *Broker) Verify(tok string) (*Claims, error) {
	if !b.Enabled() {
		return nil, ErrDisabled
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) > 2048 {
		return nil, ErrMalformed
	}
	parts := strings.SplitN(strings.TrimPrefix(tok, TokenPrefix), ".", 2)
	if len(parts) != 2 {
		return nil, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	if !hmac.Equal(sig, b.sign(payload)) {
		return nil, ErrSignature
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil || c.V != 1 || c.Kitchen == "" || c.Agent == "" || c.ID == "" {
		return nil, ErrMalformed
	}
	if b.now().Unix() >= c.Expires {
		return nil, ErrExpired
	}
	return &c, nil
}

// Grant is one admitted call. Done must be called when the call ends.
type Grant struct {
	Claims Claims
	b      *Broker
	id     string
	once   sync.Once
}

// Admit verifies tok and charges one call to its scope. A scope that is closed, out of calls or too busy refuses.
func (b *Broker) Admit(tok string) (*Grant, error) {
	c, err := b.Verify(tok)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.scopes[c.ID]
	if s == nil {
		// Not opened here: judged by signature and expiry, and counted from now on.
		b.sweepLocked()
		if len(b.scopes) >= maxScopes {
			return nil, ErrBusy
		}
		s = &scope{claims: *c}
		b.scopes[c.ID] = s
	}
	switch {
	case s.closed:
		return nil, ErrScopeClosed
	case s.calls >= b.maxCalls:
		return nil, ErrBudget
	case s.inflight >= MaxConcurrent:
		return nil, ErrBusy
	case s.bytes >= MaxScopeBytes:
		return nil, ErrBudget
	}
	s.calls++
	s.inflight++
	return &Grant{Claims: *c, b: b, id: c.ID}, nil
}

// Done ends the call and charges its result size.
func (g *Grant) Done(resultBytes int) {
	g.once.Do(func() {
		g.b.mu.Lock()
		if s := g.b.scopes[g.id]; s != nil {
			s.inflight--
			s.bytes += int64(resultBytes)
		}
		g.b.mu.Unlock()
	})
}

// Open reports whether the scope of the grant is still open: a call that waited (for an approval) checks it before it acts.
func (g *Grant) Open() bool {
	g.b.mu.Lock()
	defer g.b.mu.Unlock()
	s := g.b.scopes[g.id]
	return s != nil && !s.closed && g.b.now().Unix() < g.Claims.Expires
}

// sweepLocked drops scopes whose token has expired: nothing can use them any more.
func (b *Broker) sweepLocked() {
	now := b.now().Unix()
	for id, s := range b.scopes {
		if now >= s.claims.Expires+60 {
			delete(b.scopes, id)
		}
	}
}

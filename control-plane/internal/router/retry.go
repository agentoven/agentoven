package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// RetryableError marks an error as safe to retry against the same provider:
// a rate limit, a transient server error, or a network failure that said
// nothing about the request itself being bad. Wrapping an error in it is how
// the three provider drivers (OpenAI, Anthropic, Ollama) tell Route what a
// human already knows from the HTTP status: a 429 or a 503 is the provider
// asking to be tried again, a 400 or a 401 is the request itself being wrong,
// and retrying that only wastes the budget on an error that will not change.
type RetryableError struct {
	StatusCode int
	Err        error
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Retryable wraps err as a RetryableError carrying status, for a driver to
// return from a failed call.
func Retryable(status int, err error) error {
	return &RetryableError{StatusCode: status, Err: err}
}

// retryableStatus reports whether an HTTP status is worth retrying against the
// same provider: 429 (rate limited) and the 5xx family (provider-side fault).
// Everything else — 400, 401, 403, 404 — is the request itself being wrong,
// and the same request will fail again every time.
func retryableStatus(status int) bool {
	return status == 429 || (status >= 500 && status < 600)
}

// isRetryable reports whether err is worth another attempt against the same
// provider: an explicit RetryableError, or a network-level failure (timeout,
// connection refused, DNS) that never reached the provider's own error
// handling at all.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var re *RetryableError
	if errors.As(err, &re) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// A url.Error wrapping context.Canceled means the caller gave up, not
		// the provider — that is never worth retrying.
		return !errors.Is(urlErr.Err, context.Canceled)
	}
	return false
}

// circuitState is one provider's circuit-breaker state: closed (normal),
// open (skip this provider until openUntil), or effectively half-open (the
// first call after openUntil passes is allowed through as a probe).
type circuitState struct {
	consecutiveFailures int
	openUntil           time.Time
}

const (
	// circuitFailureThreshold is how many consecutive failures (across retries
	// already exhausted, i.e. whole Route() attempts) open the circuit for a
	// provider. A provider that has failed this many times in a row is
	// probably down, not unlucky — stop sending it traffic for a while instead
	// of making every subsequent request pay for one more doomed attempt.
	circuitFailureThreshold = 5
	// circuitCooldown is how long an open circuit stays open before the next
	// call is let through as a probe.
	circuitCooldown = 30 * time.Second
)

// circuitBreaker tracks one circuit per provider name. Providers are few and
// long-lived (configured, not created per request), so a plain map with a
// mutex is simpler than anything fancier and never grows unbounded.
type circuitBreaker struct {
	mu    sync.Mutex
	state map[string]*circuitState
}

func newCircuitBreaker() *circuitBreaker {
	return &circuitBreaker{state: make(map[string]*circuitState)}
}

// allow reports whether provider may be called right now. An open circuit
// still lets exactly one call through once openUntil has passed, so the
// breaker can discover recovery without waiting for someone to reset it.
func (cb *circuitBreaker) allow(provider string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s, ok := cb.state[provider]
	if !ok {
		return true
	}
	return time.Now().After(s.openUntil)
}

func (cb *circuitBreaker) recordSuccess(provider string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	delete(cb.state, provider)
}

func (cb *circuitBreaker) recordFailure(provider string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s, ok := cb.state[provider]
	if !ok {
		s = &circuitState{}
		cb.state[provider] = s
	}
	s.consecutiveFailures++
	if s.consecutiveFailures >= circuitFailureThreshold {
		s.openUntil = time.Now().Add(circuitCooldown)
		log.Warn().
			Str("provider", provider).
			Int("consecutive_failures", s.consecutiveFailures).
			Dur("cooldown", circuitCooldown).
			Msg("Circuit breaker opened for provider")
	}
}

// callWithRetry calls provider through callProvider, retrying a retryable
// failure against the same provider with exponential backoff before Route's
// caller moves on to the next configured provider. The circuit breaker sits
// in front of the whole attempt: a provider whose circuit is open is skipped
// without spending any of the retry budget on it.
func (mr *ModelRouter) callWithRetry(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	if !mr.circuit.allow(provider.Name) {
		return nil, fmt.Errorf("provider %q: circuit open after repeated failures, skipping", provider.Name)
	}

	attempts := retryAttemptsFor(req.ThinkingEnabled)
	var resp *models.RouteResponse
	var lastErr error
	tries := 0

	operation := func() error {
		tries++
		var err error
		resp, err = mr.callProvider(ctx, provider, req)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return backoff.Permanent(err)
		}
		if tries >= attempts {
			return backoff.Permanent(err)
		}
		log.Warn().
			Str("provider", provider.Name).
			Int("attempt", tries).
			Int("max_attempts", attempts).
			Int("status", unwrapStatus(err)).
			Err(err).
			Msg("Retryable provider error, backing off")
		return err
	}

	err := backoff.Retry(operation, backoff.WithMaxRetries(backoffPolicy(ctx), uint64(attempts-1)))
	if err != nil {
		mr.circuit.recordFailure(provider.Name)
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, err
	}
	mr.circuit.recordSuccess(provider.Name)
	return resp, nil
}

// backoffPolicy builds the exponential backoff used between retry attempts,
// capped so a slow provider cannot stall Route() for minutes: ~200ms, ~400ms,
// ~800ms, ... up to maxRetryElapsed total, whichever the request's own
// context deadline doesn't cut short first.
func backoffPolicy(ctx context.Context) backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 200 * time.Millisecond
	b.Multiplier = 2
	b.MaxInterval = 3 * time.Second
	b.MaxElapsedTime = 10 * time.Second
	return backoff.WithContext(b, ctx)
}

// retryAttemptsFor returns the retry budget for a provider call. Thinking
// calls already run long and the provider has usually done real work before
// failing (partial generation, mid-stream drop) — retrying those doubles the
// latency and the bill for a case that is often a genuine timeout rather than
// a transient fault, so they get a single try. Everything else gets real
// retry budget, since a 429 or a 503 on an ordinary call is cheap to retry
// and usually succeeds on the second attempt.
func retryAttemptsFor(thinkingEnabled bool) int {
	if thinkingEnabled {
		return 1
	}
	return 3
}

func unwrapStatus(err error) int {
	var re *RetryableError
	if errors.As(err, &re) {
		return re.StatusCode
	}
	return 0
}

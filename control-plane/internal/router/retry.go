package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
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

// RequestError marks a failure the request or the caller's own account caused: a malformed prompt
// (400), a model name that does not exist (404), a body that is too large (413), an unprocessable one
// (422), a provider that says the account has no credit left (402). It says nothing about the
// provider's health, so it never counts toward opening a circuit: one tenant sending five malformed
// prompts must not take a healthy provider away from everyone else.
type RequestError struct {
	StatusCode int
	Err        error
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// RequestRejected wraps err as a RequestError carrying status, for a driver to return.
func RequestRejected(status int, err error) error {
	return &RequestError{StatusCode: status, Err: err}
}

// requestCaused reports whether an HTTP status means the request (or the account behind it) is at
// fault. A 401 or 403 is not in the list: a bad or forbidden key is the provider's configuration
// failing, and the circuit, which is per kitchen, should stop sending traffic into it.
func requestCaused(status int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case 401, 403, 407, 408, 429:
		return false
	}
	return true
}

// classifyStatus is what a driver returns for a failed HTTP call: Retryable for a rate limit or a
// provider fault, RequestRejected for a request-caused 4xx, the error as it is otherwise.
func classifyStatus(status int, err error) error {
	switch {
	case retryableStatus(status):
		return Retryable(status, err)
	case requestCaused(status):
		return RequestRejected(status, err)
	}
	return err
}

// providerFault reports whether a call that failed after its retries says the provider is unhealthy.
// A refusal by a gate, a request-caused error and a caller that gave up are not the provider's doing.
func providerFault(err error) bool {
	if err == nil || isGateRefusal(err) || errors.Is(err, context.Canceled) {
		return false
	}
	var re *RequestError
	return !errors.As(err, &re)
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
	lastFailure         time.Time
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
	// circuitForget is how long a circuit with no new failure is remembered.
	circuitForget = 10 * time.Minute
)

// circuitKey names one circuit: a provider of one kitchen, at one endpoint. Two kitchens that both
// call their provider "openai" have separate circuits, so one tenant's failures (a revoked key, a
// budget, a flood of bad requests) cannot take the provider away from another; moving a provider to
// another endpoint starts it with a closed circuit.
func circuitKey(provider *models.ModelProvider, req *models.RouteRequest) string {
	kitchen := provider.Kitchen
	if kitchen == "" && req != nil {
		kitchen = req.Kitchen // a platform-wide provider is still judged per calling kitchen
	}
	return kitchen + "\x00" + provider.Name + "\x00" + provider.Endpoint
}

// circuitBreaker tracks one circuit per circuitKey. Entries are removed on the first success, and
// ones that have been quiet for circuitForget are dropped when another failure is recorded, so a
// dead kitchen or endpoint does not leave a streak behind forever.
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
	s.lastFailure = time.Now()
	for k, o := range cb.state {
		if k != provider && time.Since(o.lastFailure) > circuitForget {
			delete(cb.state, k)
		}
	}
	if s.consecutiveFailures >= circuitFailureThreshold {
		s.openUntil = time.Now().Add(circuitCooldown)
		log.Warn().
			Str("circuit", strings.ReplaceAll(provider, "\x00", "/")).
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
	circuit := circuitKey(provider, req)
	if !mr.circuit.allow(circuit) {
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
		if providerFault(lastErr) {
			mr.circuit.recordFailure(circuit)
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, err
	}
	mr.circuit.recordSuccess(circuit)
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

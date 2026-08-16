package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// siteverifyTimeout limits one siteverify call. Verification sits on the
// critical path of Login/SignUp, but the breaker below — not this
// timeout — is what stops a provider brownout from piling up blocked
// requests, so the per-call limit stays generous for slow-but-working
// provider responses; the caller's context (the request deadline) is the
// other bound.
const siteverifyTimeout = 10 * time.Second

// Breaker policy: once siteverifyBreakerThreshold consecutive calls fail
// at the transport level (unreachable endpoint, non-200, undecodable
// body, OR a deadline expiry — the request deadline fires before this
// client's own timeout, so a provider that accepts connections and never
// answers surfaces as a deadline and must still count as a fault), the
// circuit opens for siteverifyBreakerCooldown and every verification
// fails closed without dialing — a provider outage must not hold one
// goroutine per in-flight login for the full timeout. After the cooldown
// the circuit half-opens for exactly one probe: the first caller dials
// alone while every other caller still fails fast, a success closes it,
// and a fault re-opens it without another threshold run — an unbounded
// probe burst on a recovering-but-shaky provider would re-trip it and
// stretch the outage. Decoded replies (even denials) count as successes —
// the provider is healthy when it answers. A call that ends because the
// CALLER cancelled it (client disconnect: context.Canceled) counts as
// neither: the provider was never given a chance to fail, and aborted
// requests must not open the circuit against a healthy provider.
const (
	siteverifyBreakerThreshold = 5
	siteverifyBreakerCooldown  = 30 * time.Second
)

// errBreakerOpen fails a verification fast while the circuit is open.
var errBreakerOpen = errors.New("captcha siteverify breaker open")

// errCodeTimeoutOrDuplicate is reCAPTCHA's and Turnstile's shared error
// code for a token that is expired or was already verified once — the
// external-providers' counterpart of a replayed ALTCHA salt.
const errCodeTimeoutOrDuplicate = "timeout-or-duplicate"

// siteverifyResponse is the reply shape shared by Google's and
// Cloudflare's siteverify endpoints (identical field names; Score is
// only ever populated by reCAPTCHA v3).
type siteverifyResponse struct {
	Success    bool     `json:"success"`
	Score      float64  `json:"score"`
	Action     string   `json:"action"`
	Hostname   string   `json:"hostname"`
	ErrorCodes []string `json:"error-codes"`
}

// siteverifyClient posts token verification to one provider's siteverify
// endpoint. reCAPTCHA and Turnstile use the same request encoding
// (form-urlencoded secret + response) and reply shape, so one client
// serves both; only the endpoint and the policy checks differ.
//
// remoteip is deliberately NOT forwarded: hubs commonly sit behind a
// reverse proxy, and the proxy's address would reach the provider as the
// client IP, turning a correct token into a spurious denial. The token's
// own validity, its action, and (for reCAPTCHA) its score are the
// security decisions; the IP is not load-bearing without trusted-proxy
// configuration this project does not require.
type siteverifyClient struct {
	provider Provider
	endpoint string
	http     *http.Client

	// mu guards the breaker state below.
	mu        sync.Mutex
	faults    int       // consecutive transport-level faults
	trippedAt time.Time // zero while the circuit is closed
	// halfOpen is true while the single post-cooldown probe is in flight.
	halfOpen bool
	// cooldown overrides siteverifyBreakerCooldown; tests shrink it.
	cooldown time.Duration
}

func newSiteverifyClient(provider Provider, endpoint string) *siteverifyClient {
	return &siteverifyClient{
		provider: provider,
		endpoint: endpoint,
		http:     &http.Client{Timeout: siteverifyTimeout},
		cooldown: siteverifyBreakerCooldown,
	}
}

// verify submits secret + token and decodes the reply, shedding load
// through the breaker while the circuit is open. A returned error is a
// transport-level fault (unreachable endpoint, non-200, undecodable
// body, deadline expiry) or errBreakerOpen; policy decisions on a
// decoded reply are the caller's.
func (c *siteverifyClient) verify(ctx context.Context, secret, token string) (siteverifyResponse, error) {
	probe, err := c.enterCall()
	if err != nil {
		return siteverifyResponse{}, err
	}
	resp, err := c.call(ctx, secret, token)
	switch {
	case err == nil:
		c.recordSuccess()
	case errors.Is(err, context.Canceled):
		// The caller cancelled mid-flight (client disconnect): the
		// provider was never given a chance to answer, so this counts as
		// neither a fault nor a success. Only this call's own probe slot,
		// if it held one, returns to open for the next caller to probe.
		// A deadline expiry is NOT this arm: the provider had its chance
		// and did not answer, so it stays a fault above.
		c.abandon(probe)
	default:
		c.recordFault()
	}
	return resp, err
}

// enterCall is the single admission gate. It returns errBreakerOpen while
// the circuit is open or another caller holds the half-open probe. When
// the cooldown has elapsed it admits exactly one caller as the probe;
// the probe identity travels back to verify so only its holder can
// abandon the slot.
func (c *siteverifyClient) enterCall() (probe bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.halfOpen {
		return false, errBreakerOpen
	}
	if !c.trippedAt.IsZero() {
		if time.Since(c.trippedAt) < c.cooldown {
			return false, errBreakerOpen
		}
		c.halfOpen = true
		return true, nil
	}
	return false, nil
}

// breakerTripped reports whether the circuit currently denies calls
// (open, or half-open with the one probe in flight). It records no
// state; enterCall owns the transitions.
func (c *siteverifyClient) breakerTripped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.halfOpen {
		return true
	}
	return !c.trippedAt.IsZero() && time.Since(c.trippedAt) < c.cooldown
}

// abandon returns the probe slot for a call that ended because the
// CALLER cancelled it. Only the probe holder may abandon; the circuit
// re-arms with a fresh cooldown — it stays open, because the provider
// was never re-tested.
func (c *siteverifyClient) abandon(probe bool) {
	if !probe {
		return
	}
	c.mu.Lock()
	c.halfOpen = false
	c.trippedAt = time.Now()
	c.mu.Unlock()
}

func (c *siteverifyClient) recordFault() {
	c.mu.Lock()
	wasProbe := c.halfOpen
	c.faults++
	c.halfOpen = false
	var tripped bool
	switch {
	case wasProbe:
		// The probe faulted: re-open with a fresh cooldown, without
		// another threshold run.
		c.trippedAt = time.Now()
		tripped = true
	case c.faults >= siteverifyBreakerThreshold && c.trippedAt.IsZero():
		c.trippedAt = time.Now()
		tripped = true
	}
	c.mu.Unlock()
	if tripped {
		slog.Warn("captcha siteverify breaker open: failing closed without provider calls",
			"provider", ProviderAlias(c.provider), "cooldown", c.cooldown.String())
	}
}

func (c *siteverifyClient) recordSuccess() {
	c.mu.Lock()
	c.faults = 0
	c.trippedAt = time.Time{}
	c.halfOpen = false
	c.mu.Unlock()
}

// call submits the form post and decodes the reply.
func (c *siteverifyClient) call(ctx context.Context, secret, token string) (siteverifyResponse, error) {
	var zero siteverifyResponse
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return zero, fmt.Errorf("build %s siteverify request: %w", c.provider, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("call %s siteverify: %w", c.provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Drain a limited amount so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return zero, fmt.Errorf("%s siteverify returned %s", c.provider, resp.Status)
	}

	var out siteverifyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return zero, fmt.Errorf("decode %s siteverify response: %w", c.provider, err)
	}
	return out, nil
}

// verifyWithClient is the policy half shared by both external providers:
// fail closed on transport faults, map the provider's duplicate-token
// error to the replayed result for the metrics label, and leave the
// success-path policy (action/score) to accept.
func verifyWithClient(ctx context.Context, client *siteverifyClient, provider Provider, secret, token string, accept func(siteverifyResponse) bool) (Result, error) {
	resp, err := client.verify(ctx, secret, token)
	if err != nil {
		if errors.Is(err, errBreakerOpen) {
			// Fail closed without dialing; the breaker's trip already
			// warned once, so an open circuit logs nothing per request.
			return ResultFailed, ErrVerificationFailed
		}
		// Fail closed, like every other captcha fault: an unreachable
		// provider must not become an open door. The warn is the
		// operator's only signal, since clients see the uniform denial.
		logSiteverifyFault(ctx, provider, err)
		return ResultFailed, ErrVerificationFailed
	}
	if !resp.Success {
		for _, code := range resp.ErrorCodes {
			if code == errCodeTimeoutOrDuplicate {
				return ResultReplayed, errReplayed
			}
		}
		return ResultFailed, ErrVerificationFailed
	}
	if !accept(resp) {
		return ResultFailed, ErrVerificationFailed
	}
	return ResultPassed, nil
}

// logSiteverifyFault records a transport-level verification fault. It is
// the operator's only window into provider outages: clients receive the
// uniform denial either way.
func logSiteverifyFault(ctx context.Context, provider Provider, err error) {
	slog.WarnContext(ctx, "captcha siteverify request failed", "provider", ProviderAlias(provider), "error", err)
}

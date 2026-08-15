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
// body), the circuit opens for siteverifyBreakerCooldown and every
// verification fails closed without dialing — a provider outage must not
// hold one goroutine per in-flight login for the full timeout. After the
// cooldown the circuit half-opens: the next call probes the provider, a
// success closes it, and a fault re-opens it without another threshold
// run. Decoded replies (even denials) count as successes — the provider
// is healthy when it answers.
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
// body) or errBreakerOpen; policy decisions on a decoded reply are the
// caller's.
func (c *siteverifyClient) verify(ctx context.Context, secret, token string) (siteverifyResponse, error) {
	if c.breakerTripped() {
		return siteverifyResponse{}, errBreakerOpen
	}
	resp, err := c.call(ctx, secret, token)
	if err != nil {
		c.recordFault()
	} else {
		c.recordSuccess()
	}
	return resp, err
}

// breakerTripped reports whether the circuit is open. Once the cooldown
// elapses it half-opens — returning false so the next call probes the
// provider — and a later fault re-trips without another threshold run.
func (c *siteverifyClient) breakerTripped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.trippedAt.IsZero() {
		return false
	}
	if time.Since(c.trippedAt) < c.cooldown {
		return true
	}
	c.trippedAt = time.Time{}
	return false
}

func (c *siteverifyClient) recordFault() {
	c.mu.Lock()
	c.faults++
	var tripped bool
	if c.faults >= siteverifyBreakerThreshold && c.trippedAt.IsZero() {
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

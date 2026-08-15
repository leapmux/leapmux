package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// siteverifyTimeout bounds one siteverify call. Verification sits on the
// critical path of Login/SignUp, so a slow provider must not hold the
// request open indefinitely; the caller's context (the request deadline)
// is the other bound.
const siteverifyTimeout = 10 * time.Second

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
}

func newSiteverifyClient(provider Provider, endpoint string) *siteverifyClient {
	return &siteverifyClient{
		provider: provider,
		endpoint: endpoint,
		http:     &http.Client{Timeout: siteverifyTimeout},
	}
}

// verify submits secret + token and decodes the reply. A returned error
// is a transport-level fault (unreachable endpoint, non-200, undecodable
// body); policy decisions on a decoded reply are the caller's.
func (c *siteverifyClient) verify(ctx context.Context, secret, token string) (siteverifyResponse, error) {
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
		// Drain a bounded amount so the connection can be reused.
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

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/locallisten"
)

// The CLI's access token lives for an hour and its refresh token for months,
// so without a refresh the credential a login mints is usable for exactly one
// hour and then demands a browser again. This file is that refresh.
//
// Two triggers, and both are needed:
//
//   - Proactive, before a call, once the stored expiry is within
//     refreshSkew. This is the ordinary path and it costs one extra request
//     per hour of use.
//   - Reactive, on a single 401 from a unary call. The proactive check reads
//     a stored expiry, and a stored expiry can be wrong -- a clock that
//     moved, a token revoked by hand, a file written by an older build.
//
// Exactly ONE retry, and none on a stream. A retried unary call is
// idempotent from the CLI's point of view (the hub did not act on a
// request it refused); a stream already begins delivering, and replaying
// it would duplicate what the caller consumed.

const (
	// refreshSkew is how long before the recorded expiry the CLI renews.
	//
	// It must exceed the worst plausible round trip plus any clock skew
	// between this machine and the hub, or the "still valid" check passes
	// and the request arrives after the token died. A minute is far more
	// than a LAN round trip and small against the hour-long access token.
	refreshSkew = 60 * time.Second
	// refreshTimeout caps one refresh leg. It is separate from the caller's
	// own deadline because a refresh that hangs must not consume the whole
	// budget of the call that triggered it.
	refreshTimeout = 30 * time.Second
)

// ErrCredentialRejected reports a credential the hub refused permanently:
// revoked, reused, or past its absolute lifetime. doRefresh deletes the
// stored file before this reaches a caller, so the next command reports
// ErrNotLoggedIn, whose message already specifies the remedy.
var ErrCredentialRejected = errors.New("this hub credential is no longer valid")

// refreshFlight collapses concurrent refreshes of the SAME credential file.
//
// A refresh rotates the token single-use: two concurrent refreshes with the
// same refresh secret make the second look like a reuse, which the hub
// treats as compromise and revokes the row. The key is the credential path,
// so two processes are not covered -- only the hub's own reuse-grace window
// protects that case, which is exactly what it is for.
var refreshFlight singleflight.Group

// credsMu serializes the read-modify-write of one process's credential file
// around a refresh, so a second goroutine cannot read the pre-rotation token
// after the rotation is written.
var credsMu sync.Mutex

// EnsureFreshBearer renews the client's credential when the stored access
// token is at or past refreshSkew of its expiry, and updates the client in
// place. A client with no refresh token (an anonymous or worker-IPC client)
// is left alone.
//
// A failure is fatal ONLY when continuing cannot work:
//
//   - A permanent rejection (ErrCredentialRejected) is fatal. The stored
//     file is already deleted and the stale access token specifies the same
//     revoked row, so the call that follows can only answer Unauthenticated
//     -- and the message that specifies `leapmux control auth login` would be
//     replaced by the hub's bare refusal.
//   - A transient failure with a token still INSIDE its lifetime is
//     swallowed. That token may well work, and turning a network blip
//     during a renewal into a failed command is worse than the renewal not
//     happening.
//   - A transient failure with a token already PAST its expiry is fatal.
//     Continuing can only produce a 401 with nothing in it that identifies the
//     cause; the refresh error does.
func (c *Client) EnsureFreshBearer(ctx context.Context) error {
	// From memory first. This runs before EVERY unary call and every
	// stream open, and the file read below is an os.ReadFile plus a JSON
	// decode under a process-wide mutex -- so a command that makes N calls
	// paid N of them to re-learn an expiry that cannot have moved. The
	// cached value moves only when this process rotates the token, and an
	// expiry another process wrote is picked up by the reactive retry.
	if !c.bearerNeedsRenewal() {
		return nil
	}
	creds, ok := c.refreshableCredentials()
	if !ok {
		return nil
	}
	if time.Until(creds.ExpiresAt) > refreshSkew {
		// The file is ahead of this client: adopt its token and its expiry
		// rather than presenting the stale one until a 401.
		c.setBearer(creds.AccessToken, creds.ExpiresAt)
		return nil
	}
	err := c.refreshBearer(ctx, creds)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCredentialRejected):
		return err
	case time.Now().Before(creds.ExpiresAt):
		return nil
	default:
		return err
	}
}

// freshBearer renews the credential and returns the token to present.
//
// The two paths that hand a bearer to a transport of their own -- the
// user-event subscription and the E2EE channel open -- go through this one
// call rather than remembering to renew and then read. ApplyAuth stays a
// pure header stamp, because WrapUnary calls it twice (before the call and
// before the retry) and the second must not re-check freshness.
func (c *Client) freshBearer(ctx context.Context) (string, error) {
	if err := c.EnsureFreshBearer(ctx); err != nil {
		return "", err
	}
	return c.currentBearer(), nil
}

// refreshableCredentials loads the stored credential when this client is one
// that can refresh: hub-bound, carrying a bearer, with a refresh token on
// disk for the same hub.
func (c *Client) refreshableCredentials() (*CredentialFile, bool) {
	if c.IsWorkerIPC() || c.currentBearer() == "" {
		return nil, false
	}
	credsMu.Lock()
	defer credsMu.Unlock()
	creds, err := LoadCredentials(c.HubURL)
	if err != nil || creds.RefreshToken == "" {
		return nil, false
	}
	return creds, true
}

// refreshBearer performs one refresh and adopts the result. presented is the
// credential the caller ALREADY read, so a concurrent refresh that rotated
// in the meantime is detected as "somebody else moved it" and simply
// adopted -- and this path does not read the file again to learn what it
// was just handed.
func (c *Client) refreshBearer(ctx context.Context, presented *CredentialFile) error {
	path, err := CredentialsPath(c.HubURL)
	if err != nil {
		return err
	}
	v, err, _ := refreshFlight.Do(path, func() (any, error) {
		return c.doRefresh(ctx, presented)
	})
	if err != nil {
		return err
	}
	creds := v.(*CredentialFile)
	c.setBearer(creds.AccessToken, creds.ExpiresAt)
	return nil
}

// doRefresh runs the rotation and rewrites the credential file.
//
// It re-reads the file even though the caller passed one, and that read is
// the point rather than a duplicate: the flight may have queued this call
// behind another goroutine's rotation, so the value on disk NOW is what
// decides whether the caller's token is still the current one.
func (c *Client) doRefresh(ctx context.Context, presented *CredentialFile) (*CredentialFile, error) {
	credsMu.Lock()
	current, err := LoadCredentials(c.HubURL)
	credsMu.Unlock()
	if err != nil {
		return nil, err
	}
	// Another goroutine refreshed while this one waited for the flight.
	// Adopt what it wrote rather than presenting a rotated-out secret, which
	// the hub would read as reuse and answer by revoking the row.
	if current.RefreshToken != presented.RefreshToken {
		return current, nil
	}

	// DETACHED from the caller's context, and this is the same rule the
	// hub's own handleRefresh states for the same exchange. A refresh
	// rotates the token single-use: once the request reaches the hub, the
	// old secret is dead whatever happens next, so a cancellation between
	// the hub's commit and SaveCredentials below leaves the file holding a
	// secret the hub already rotated away. Presenting it later reads as a
	// reuse, which the hub answers by REVOKING the row -- a Ctrl-C would
	// end a credential that nothing was wrong with.
	//
	// It matters twice here, because singleflight collapses concurrent
	// callers onto one leg: the leader's cancellation would otherwise abort
	// the rotation for every follower as well, including one whose own
	// deadline had ample time left.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	body, err := postRefresh(refreshCtx, c.HubURL, current.RefreshToken)
	if err != nil {
		// A permanent rejection is what deletes the stored file, and it is
		// deleted HERE rather than in the transport leg: this function owns
		// the read-modify-write of that file and holds credsMu around it,
		// so the one place that writes it is the one place that removes it.
		if errors.Is(err, ErrCredentialRejected) {
			credsMu.Lock()
			_ = DeleteCredentials(c.HubURL)
			credsMu.Unlock()
		}
		return nil, err
	}

	credsMu.Lock()
	defer credsMu.Unlock()
	// ONE instant for both deadlines. The hub derived expires_in and
	// refresh_expires_in from a single now, so reading the clock twice here
	// splits that instant back apart and anchors the stored pair to two.
	now := time.Now()
	next := *current
	next.AccessToken = body.AccessToken
	next.RefreshToken = body.RefreshToken
	next.ExpiresAt = now.Add(time.Duration(body.ExpiresIn) * time.Second)
	if body.RefreshExpiresIn > 0 {
		next.RefreshExpiresAt = now.Add(time.Duration(body.RefreshExpiresIn) * time.Second)
	}
	if body.TokenID != "" {
		next.TokenID = body.TokenID
	}
	if err := SaveCredentials(c.HubURL, next); err != nil {
		return nil, fmt.Errorf("save refreshed credentials: %w", err)
	}
	return &next, nil
}

// refreshBody is the /auth/cli/refresh success payload.
type refreshBody struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenID          string `json:"token_id"`
}

// postRefresh performs the token rotation request.
//
// invalid_grant is PERMANENT: the row was revoked, the refresh was reused,
// or the credential reached its absolute lifetime. It reports
// ErrCredentialRejected and touches NO local state; doRefresh deletes the
// stored file, under the same lock that owns every other write to it, so the
// next command answers ErrNotLoggedIn -- whose message specifies
// `leapmux control auth login` -- instead of retrying a credential that can
// never work again.
func postRefresh(ctx context.Context, hubURL, refreshToken string) (refreshBody, error) {
	// locallisten.RESTClient, which the login-flow REST calls in the cmd
	// package also use: the two packages cannot share a helper between
	// themselves, because cmd imports this one, so they both reach for the
	// leaf instead.
	hc, baseURL := locallisten.RESTClient(hubURL, refreshTimeout)
	form := url.Values{"refresh_token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(baseURL, "/auth/cli/refresh"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return refreshBody{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return refreshBody{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		var body refreshBody
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return refreshBody{}, fmt.Errorf("decode refresh response: %w", err)
		}
		if body.AccessToken == "" || body.RefreshToken == "" {
			return refreshBody{}, errors.New("refresh response carried no token pair")
		}
		return body, nil
	}

	var oerr struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&oerr)
	if oerr.Error == "invalid_grant" {
		if oerr.ErrorDescription != "" {
			return refreshBody{}, fmt.Errorf("%w: %s", ErrCredentialRejected, oerr.ErrorDescription)
		}
		return refreshBody{}, ErrCredentialRejected
	}
	return refreshBody{}, fmt.Errorf("refresh failed: %s", resp.Status)
}

// retryAfterUnauthenticated refreshes once after a rejected unary call and
// reports whether the caller should retry.
//
// It refuses to retry when the credential was permanently rejected (the file
// is gone by then) or when nothing was refreshable, so a genuinely
// unauthenticated caller sees the hub's own error rather than a doubled one.
//
// The loaded credential is PASSED DOWN so doRefresh can tell "my token is
// still the current one" from "somebody else already rotated it"; doRefresh
// re-reads the file for that comparison, which is the point rather than a
// duplicate. See its own comment.
func (c *Client) retryAfterUnauthenticated(ctx context.Context) bool {
	creds, ok := c.refreshableCredentials()
	if !ok {
		return false
	}
	// The presented token is whatever this path read from disk: the 401 may
	// come from a rotation another process already performed, in which case
	// doRefresh adopts it without a network call.
	return c.refreshBearer(ctx, creds) == nil
}

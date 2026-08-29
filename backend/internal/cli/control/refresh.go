package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"connectrpc.com/connect"
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
//   - Reactive, on a 401 from a unary call. The proactive check reads
//     a stored expiry, and a stored expiry can be wrong -- a clock that
//     moved, a token revoked by hand, a file written by an older build.
//
// The reactive repair runs at most ONCE for one call, and never on a
// stream. A replayed unary call is idempotent from the CLI's point of view
// (the hub did not act on a request it refused); a stream already begins
// delivering, and replaying it would duplicate what the caller consumed.
// WrapUnary owns that rule -- see its own comment for the loop that holds
// it.

const (
	// refreshSkew is how long before the recorded expiry the CLI renews.
	//
	// It must exceed the worst plausible round trip plus any clock skew
	// between this machine and the hub, or the "still valid" check passes
	// and the request arrives after the token died. A minute is far more
	// than a LAN round trip and small against the hour-long access token.
	refreshSkew = 60 * time.Second
	// refreshTimeout caps one refresh stage. It is separate from the caller's
	// own deadline because a refresh that hangs must not consume the whole
	// budget of the call that triggered it.
	refreshTimeout = 30 * time.Second
)

// ErrCredentialRejected reports a credential the hub refused permanently:
// revoked, reused, or past its absolute lifetime. doRefresh deletes the
// stored file before this reaches a caller, so the next command reports
// ErrNotLoggedIn, whose message already specifies the remedy.
var ErrCredentialRejected = errors.New("this hub credential is no longer valid")

// ErrCredentialsNotSaved reports a rotation that the hub COMMITTED and this
// process could not write to disk.
//
// The pair in memory is live and the process keeps working with it. The FILE
// is what is broken: it still holds the refresh secret the hub rotated away,
// and past the hub's reuse grace a command that presents it reads as a reuse
// -- which the hub answers by revoking the row. So the CLI reports this, and
// never swallows it. One command fails with the reason, and the operator can repair
// the disk before the next login is lost.
var ErrCredentialsNotSaved = errors.New("the hub rotated this credential and it could not be written to disk; the stored file is stale and a later command may revoke the credential")

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
//   - EnsureFreshBearer swallows a transient failure with a token still
//     INSIDE its lifetime. That token may well work, and turning a brief
//     network failure during a renewal into a failed command is worse than
//     the renewal not happening.
//   - A transient failure with a token already PAST its expiry is fatal.
//     Continuing can only produce a 401 with nothing in it that identifies the
//     cause; the refresh error does.
//   - A rotation that could not be SAVED (ErrCredentialsNotSaved) is fatal,
//     although the rotated pair is already adopted and the call that follows
//     would succeed. The file holds a secret the hub rotated away, and the
//     next process to present it loses the credential; the swallow above
//     would hide that, because a proactive renewal fires refreshSkew before
//     the expiry and so is always inside the token's lifetime.
func (c *Client) EnsureFreshBearer(ctx context.Context) error {
	// From memory first. This runs before EVERY unary call and every
	// stream open, and the file read below is an os.ReadFile plus a JSON
	// decode under a process-wide mutex -- so a command that makes N calls
	// paid N of them to re-learn an expiry that cannot move between calls.
	// The cached value moves only when this process rotates the token, and
	// the reactive retry picks up an expiry another process wrote.
	if !c.bearerNeedsRenewal() {
		return nil
	}
	creds, ok := c.refreshableCredentials()
	if !ok {
		return nil
	}
	// The file is ahead of this client: adopt its token and its expiry
	// rather than presenting the stale one until a 401.
	if c.adoptStoredBearer(creds) {
		return nil
	}
	err := c.refreshBearer(ctx, creds)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCredentialRejected), errors.Is(err, ErrCredentialsNotSaved):
		return err
	case time.Now().Before(creds.ExpiresAt):
		return nil
	default:
		return err
	}
}

// adoptStoredBearer adopts the stored access token and its expiry when that
// expiry is far enough away to use, and reports whether it did.
//
// The bar is refreshSkew, the same one that decides to renew: a token this
// client would renew at once is not one worth adopting.
func (c *Client) adoptStoredBearer(creds *CredentialFile) bool {
	if time.Until(creds.ExpiresAt) <= refreshSkew {
		return false
	}
	c.setBearer(creds.AccessToken, creds.ExpiresAt)
	return true
}

// bearerErrorCode maps a pre-call credential failure onto the code the
// caller reports.
//
// Almost every one means "this call cannot authenticate". A rotation that
// the hub committed and this process could not SAVE is the exception: the
// token in memory is live and the fault is a local disk, so Unauthenticated
// would send the operator to the login rather than to the file.
func bearerErrorCode(err error) connect.Code {
	if errors.Is(err, ErrCredentialsNotSaved) {
		return connect.CodeInternal
	}
	return connect.CodeUnauthenticated
}

// freshBearer renews the credential and returns the token to present.
//
// The two paths that hand a bearer to a transport of their own -- the
// user-event subscription and the E2EE channel open -- go through this one
// call rather than remembering to renew and then read. ApplyAuth stays a
// pure header stamp, because WrapUnary stamps the header once per attempt
// and runs the freshness check itself, in one place, before each of them.
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
// in the meantime reads as "somebody else moved it" and this path simply
// adopts it -- and it does not read the file again to learn what it
// was just handed.
func (c *Client) refreshBearer(ctx context.Context, presented *CredentialFile) error {
	path, err := CredentialsPath(c.HubURL)
	if err != nil {
		return err
	}
	v, err, _ := refreshFlight.Do(path, func() (any, error) {
		return c.doRefresh(ctx, presented)
	})
	// refreshBearer adopts the pair whenever there IS one, error or not. doRefresh
	// returns a pair with an error exactly once -- the hub rotated and the
	// file could not be written -- and the pair it hands back is then the
	// only live one in this process. Discarding it because the call also
	// failed threw away a credential the hub already committed to.
	if creds, ok := v.(*CredentialFile); ok && creds != nil {
		c.setBearer(creds.AccessToken, creds.ExpiresAt)
	}
	return err
}

// doRefresh runs the rotation and rewrites the credential file.
//
// It re-reads the file even though the caller passed one, and that read is
// the point rather than a duplicate: the flight can queue this call
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
	// old secret is unusable whatever happens next, so a cancellation between
	// the hub's commit and SaveCredentials below leaves the file holding a
	// secret the hub already rotated away. Presenting it later reads as a
	// reuse, which the hub answers by REVOKING the row -- a credential that
	// nothing was wrong with would end.
	//
	// Three cancellations reach here, and the commonest is not the one that
	// reads as obvious. Any verb that resolves an entity fans its lookups
	// out under errgroup.WithContext, so the FIRST sibling call to fail
	// cancels the context of every other call in flight, one of which may be
	// mid-rotation. Next, singleflight collapses concurrent callers onto one
	// flight, so the leader's cancellation would abort the rotation for every
	// follower as well, including one whose own deadline had ample time
	// left. Last, `control events` installs signal.NotifyContext, so a
	// Ctrl-C there cancels the stream's context and the refresh under it.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	body, err := c.postRefresh(refreshCtx, current.RefreshToken, current.ClientIDOrBuiltIn())
	if err != nil {
		// A permanent rejection is what deletes the stored file, and this
		// function deletes it HERE rather than the transport stage: it owns
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
	// The hub states the reachable grant on every rotation; an empty value is
	// a hub that did not answer the field, and the stored one stays rather
	// than being wiped by silence.
	if body.Scope != "" {
		next.Scope = body.Scope
	}
	if err := SaveCredentials(c.HubURL, next); err != nil {
		// doRefresh returns the pair WITH the error, and the caller adopts
		// it. The hub rotated already, so the old access token is unusable
		// and the pair held here is the only live one; returning nil
		// discarded it and left the process presenting a token the hub
		// retired.
		return &next, fmt.Errorf("%w: %w", ErrCredentialsNotSaved, err)
	}
	return &next, nil
}

// refreshBody is the /oauth/token refresh-grant success payload.
//
// scope is what the credential REACHES after this rotation, not what it asked
// for: the hub reports the stored grant narrowed to the app registration's
// ceiling, so an owner removing a permission from the registration reaches
// this file on the next rotation. Leaving it out would keep `auth status`
// printing a grant the hub stopped honoring.
type refreshBody struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenID          string `json:"token_id"`
	Scope            string `json:"scope"`
}

// TokenResponseBody is refreshBody, exported for the login's decode: the
// authorization-code exchange and the rotation read the SAME endpoint's
// success payload, and the login embeds this shape plus its two one-time
// fields rather than restating six fields whose drift already happened once
// (scope reached the rotation's copy one commit before the login's).
type TokenResponseBody = refreshBody

// postRefresh performs the token rotation request.
//
// invalid_grant is PERMANENT: the row was revoked, the refresh was reused,
// or the credential reached its absolute lifetime. It reports
// ErrCredentialRejected and touches NO local state; doRefresh deletes the
// stored file, under the same lock that owns every other write to it, so the
// next command answers ErrNotLoggedIn -- whose message specifies
// `leapmux control auth login` -- instead of retrying a credential that can
// never work again.
//
// It reuses the client's OWN transport rather than building one. For a
// `unix:`/`npipe:` hub each build allocates an http.Transport whose
// IdleConnTimeout is zero and that nothing closes, so a long-running
// `control events` leaked one idle connection and its read goroutine per
// hourly rotation. The 30-second budget of a rotation is enforced by the
// request context's deadline, which refreshTimeout already sets.
// clientID is the app the credential was issued to: the hub binds the refresh
// stage to that app, so a credential minted to another registration must present
// its own id and not the CLI's built-in one.
func (c *Client) postRefresh(ctx context.Context, refreshToken, clientID string) (refreshBody, error) {
	// One token endpoint, and grant_type is REQUIRED: the hub no longer infers
	// which grant a request means from which field happens to be present.
	form := url.Values{
		"grant_type":    {GrantTypeRefreshToken},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}
	resp, err := PostForm(ctx, c.HTTPClient,
		locallisten.JoinPath(c.connectURL, "/oauth/token"), form)
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

	oerr := ReadOAuthError(resp)
	if oerr.Error == "invalid_grant" {
		if oerr.ErrorDescription != "" {
			return refreshBody{}, fmt.Errorf("%w: %s", ErrCredentialRejected, oerr.ErrorDescription)
		}
		return refreshBody{}, ErrCredentialRejected
	}
	return refreshBody{}, fmt.Errorf("refresh failed: %s", resp.Status)
}

// errNothingToRefresh reports a client with no credential to renew: an
// anonymous client, a worker-IPC client, or a credential file with no
// refresh token. The refusal is what keeps a genuinely unauthenticated
// caller reading the hub's own error rather than a doubled one.
var errNothingToRefresh = errors.New("this client holds no credential that can be renewed")

// repairAfterUnauthenticated renews the credential after a rejected unary
// call, so the replay can present a live token.
//
// It ADOPTS before it rotates. A 401 on a token that differs from the stored
// one means another process rotated the credential and this client presented
// the token it replaced, so the stored token is the repair and a rotation is
// pure loss: two long-lived processes on one credential file would rotate on
// nearly every call, each one retiring the token the other just adopted.
//
// A rotation, when it runs, PASSES DOWN the credential this path read, so
// doRefresh can tell "my token is still the current one" from "somebody else
// already rotated it"; doRefresh re-reads the file for that comparison,
// which is the point rather than a duplicate. See its own comment.
func (c *Client) repairAfterUnauthenticated(ctx context.Context) error {
	creds, ok := c.refreshableCredentials()
	if !ok {
		return errNothingToRefresh
	}
	if creds.AccessToken != c.currentBearer() && c.adoptStoredBearer(creds) {
		return nil
	}
	return c.refreshBearer(ctx, creds)
}

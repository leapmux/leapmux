package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"connectrpc.com/connect"
	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/locallisten"
)

// Step-up ("sudo mode") for this CLI's own credential.
//
// The hub refuses a sensitive action from a credential that proved no factor
// recently, and marks that refusal with ElevationRequiredHeader. The marker
// means "prove a factor and retry", and this file is what does it.
//
// The factor is proven IN A BROWSER, and deliberately somewhere this process
// cannot reach: a CLI that could elevate itself from the terminal would hand a
// stolen credential file everything the window exists to withhold. So the
// ceremony is the device-code ceremony -- ask, show a URL and a code, wait for
// a human -- and it works from an SSH session or a container, where the
// browser is on another machine entirely.
//
// What it grants is a WINDOW on the credential this CLI already holds. It
// mints nothing: the file on disk is unchanged, and every restricted command
// inside the window lands without another prompt.

// ElevationRequiredHeader marks a hub refusal whose remedy is "prove a
// factor and retry". It mirrors the constant the hub sets; the value is
// always "1" and only its presence is meaningful.
//
// The name is owned by contracts/headers.json and generated for the hub, this
// CLI, and the browser alike -- which keeps the ORIGINAL intent of the old
// hand copy: the CLI still depends on no hub-internal package (generated/
// is not one), and the two programs can still be upgraded separately while
// the wire spelling can no longer drift between them.
const ElevationRequiredHeader = contracts.ElevationRequiredHeader

// ErrElevationUnsupported reports a credential the hub will not elevate: a
// worker-minted delegation token, or an IPC client with no bearer at all.
// Neither has a browser ceremony to run, so the caller reports the hub's
// original refusal rather than a failed step-up.
var ErrElevationUnsupported = errors.New("this credential cannot be verified in a browser")

// ErrElevationNeedsAPerson reports a step-up that cannot run here, because
// nothing drives this process from a terminal or the caller refused the
// prompt with LEAPMUX_CONTROL_NO_PROMPT.
//
// The ceremony ends only when a person opens a browser, so a process with
// nobody at a keyboard would print a URL that nobody reads and then block
// for the full life of the grant -- ten minutes of a CI job or a cron run,
// ending in "the verification request expired". Refusing at once lets the
// caller report the hub's own refusal, which states what the command needs.
var ErrElevationNeedsAPerson = errors.New("this command needs a person to verify their identity in a browser, and nothing here can show the prompt")

// NeedsElevation reports whether a hub error is the marked refusal.
//
// The MARKER, never the message. The wording is user-facing prose that somebody
// will reword, and matching it would break on the first edit; the code alone is
// too broad, because a FailedPrecondition also means "this account has no
// password" and half a dozen other things a step-up cannot fix.
func NeedsElevation(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return false
	}
	return connectErr.Meta().Get(ElevationRequiredHeader) != ""
}

// elevateFlight collapses concurrent step-ups of the SAME credential.
//
// One ceremony needs one person, and a CLI command fans its calls out on one
// client -- the entity resolver runs its lookups in an errgroup, and the
// workspace cleanup fans out too. Without this, the first restricted verb on such
// a path posts one device-authorization row per call and prints that many
// interleaved URL and code triples, which a person cannot answer.
//
// The followers take the LEADER's answer, including its context. That costs
// nothing to repair -- a step-up mints no credential, so a cancelled stage
// leaves no rotated-away secret behind and the next command asks again --
// which is why this needs no detached context of its own, and the refresh
// flight does.
var elevateFlight singleflight.Group

// Elevate runs one browser step-up for this client's credential and returns
// when the hub reports it granted.
//
// It BLOCKS on a human, for up to the grant's own lifetime, and prints the
// address and the code to this package's Err. A process with nobody at a
// keyboard gets ErrElevationNeedsAPerson at once instead, so a caller may
// call this without testing first.
//
// Err, never Out: a restricted verb prints its JSON envelope to Out in the same
// invocation, and four lines of prose in that stream stop `... | jq` from
// parsing on the first run after the window lapses.
func (c *Client) Elevate(ctx context.Context) error {
	key, keyErr := CredentialsPath(c.HubURL)
	if keyErr != nil {
		// A hub address with no derivable credential path collapses on the
		// address itself. The key only has to be equal for two callers that
		// share one credential.
		key = c.HubURL
	}
	_, err, _ := elevateFlight.Do(key, func() (any, error) {
		return nil, c.elevateOnce(ctx)
	})
	return err
}

// elevateOnce runs the ceremony itself. Elevate wraps it in the flight.
func (c *Client) elevateOnce(ctx context.Context) error {
	if !c.promptAllowed {
		return ErrElevationNeedsAPerson
	}
	grant, err := c.startElevation(ctx)
	if err != nil {
		return err
	}
	// Resolved ONCE, before the poll loop: the client id the hub bound the
	// grant to cannot change during the ceremony -- the hub refuses a poll
	// whose presented id differs from the grant row's -- and re-reading and
	// re-parsing the credential file on every 5 s poll (~120 reads across a
	// full ceremony) bought nothing. A missing or unreadable credential falls
	// back to the built-in id, exactly as the per-poll read did.
	pollClientID := ControlCLIClientID
	if creds, err := LoadCredentials(c.HubURL); err == nil {
		pollClientID = creds.ClientIDOrBuiltIn()
	}
	_, _ = fmt.Fprintln(Err, "This command needs you to verify your identity.")
	_, _ = fmt.Fprintln(Err, "  1. Visit", grant.VerificationURI)
	_, _ = fmt.Fprintln(Err, "  2. Enter the code:", grant.UserCode)
	if grant.VerificationURIComplete != "" {
		_, _ = fmt.Fprintln(Err, "Or open:", grant.VerificationURIComplete)
	}

	if err := grant.Poll(ctx, func(ctx context.Context) error {
		return c.pollElevation(ctx, grant.DeviceCode, pollClientID)
	}); err != nil {
		if errors.Is(err, ErrDeviceGrantExpired) {
			return errors.New("the verification request expired before it was approved")
		}
		return err
	}
	return nil
}

func (c *Client) startElevation(ctx context.Context) (*DeviceGrant, error) {
	form := url.Values{"installation_name": {DefaultDeviceName()}}
	// The credential itself is the right to ASK. What it cannot do is
	// approve, which needs a browser session that proves a factor.
	resp, err := PostForm(ctx, c.HTTPClient,
		locallisten.JoinPath(c.connectURL, "/oauth/step-up"),
		form, c.ApplyAuth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	// 403 is the app's own registration refusing the stage: its owner has not
	// allowed it to verify a factor. 400 and 401 are the credential kinds that
	// carry no window at all. All three are permanent for this credential, so
	// the caller reports the remedy instead of retrying.
	//
	// 403 decodes the BODY, because the hub states the one-step remedy there
	// (allow elevation under Preferences › Apps › App registrations). The
	// sentinel's own two causes are both wrong for this refusal, and an
	// operator who reads them retries a browser ceremony the hub keeps
	// refusing.
	if resp.StatusCode == http.StatusForbidden {
		var body struct {
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.ErrorDescription != "" {
			return nil, errors.New(body.ErrorDescription)
		}
		return nil, ErrElevationUnsupported
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrElevationUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the hub refused the verification request: %s", resp.Status)
	}
	grant := &DeviceGrant{}
	if err := json.NewDecoder(resp.Body).Decode(grant); err != nil {
		return nil, err
	}
	if grant.DeviceCode == "" {
		return nil, errors.New("the hub returned no verification code")
	}
	return grant, nil
}

// pollElevation performs one /oauth/token poll for a step-up grant. It is
// the login flow's poll without the credential handling: a step-up mints
// nothing, so there is nothing to persist. The hub answers `{"elevated":true}`
// once the window is stamped.
func (c *Client) pollElevation(ctx context.Context, deviceCode, clientID string) error {
	form := url.Values{
		"grant_type":  {GrantTypeDeviceCode},
		"client_id":   {clientID},
		"device_code": {deviceCode},
	}
	resp, err := PostForm(ctx, c.HTTPClient,
		locallisten.JoinPath(c.connectURL, "/oauth/token"), form)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return DeviceFlowError(resp)
}

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"connectrpc.com/connect/v2"
	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/loopbackauth"
)

// Step-up ("sudo mode") for this CLI's own credential.
//
// The hub refuses a sensitive action from a credential that proved no factor
// recently, and marks that refusal with ElevationRequiredHeader. The marker
// means "prove a factor and retry", and this file is what does it.
//
// The account holder proves a factor in a browser. A credential issued
// through the authorization-code grant uses a loopback ceremony and needs
// a browser on the CLI's machine. A credential issued through the device-code
// grant or by an administrator uses a device-code ceremony, which can be
// approved in a browser on another machine.
//
// Step-up opens a window on the CLI's existing credential. The CLI may
// refresh the bearer and save the rotated tokens before redeeming a loopback
// code. While the window is current, commands that require it can proceed
// without another step-up prompt.

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

// NeedsElevation reports whether a hub refusal is the marked one.
//
// The MARKER, never the message. The wording is user-facing prose that somebody
// will reword, and matching it would break on the first edit; the code alone is
// too broad, because a FailedPrecondition also means "this account has no
// password" and half a dozen other things a step-up cannot fix.
//
// The marker rides the RESPONSE header and not the error, so the response
// header is a parameter: connect v2 carries no metadata on an error.
func NeedsElevation(err error, responseHeader *connect.Header) bool {
	if err == nil {
		return false
	}
	return responseHeader.Get(ElevationRequiredHeader) != ""
}

// elevateFlight collapses concurrent step-ups of the SAME credential.
//
// One ceremony needs one person, and a CLI command fans its calls out on one
// client -- the entity resolver runs its lookups in an errgroup, and the
// workspace cleanup fans out too. Without this, each refused call could
// start a separate ceremony and ask the person to verify again.
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
// Elevate waits for approval until the ceremony expires. It prints the
// browser address to Err and includes a code for a device-code ceremony.
// A process with nobody at a keyboard gets ErrElevationNeedsAPerson at once.
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

// errBrowserStepUpRequired identifies an authorization-code credential that
// needs a loopback step-up. The browser must run on the same machine as the
// CLI. The hub refuses the device ceremony with
// oauthapp.BrowserStepUpRequiredCode and creates no device row.
var errBrowserStepUpRequired = errors.New("this credential must be verified in the browser that issued it")

// errStepUpNeedsSignIn reports a loopback step-up with nothing to verify: the
// credential file holds no token id, so the ceremony has no step_up target
// and the hub could not bind a code to this credential.
var errStepUpNeedsSignIn = errors.New("no token id is recorded for this credential; sign in again with `leapmux control auth login`")

// elevateOnce runs the ceremony itself. Elevate wraps it in the flight.
//
// The CLI asks the Hub to start a device-code ceremony. If the Hub replies
// that the credential requires browser step-up, the CLI starts the loopback
// ceremony on this machine.
func (c *Client) elevateOnce(ctx context.Context) error {
	if !c.promptAllowed {
		return ErrElevationNeedsAPerson
	}
	grant, err := c.startElevation(ctx)
	if errors.Is(err, errBrowserStepUpRequired) {
		return c.elevateViaLoopback(ctx, loopbackauth.Start)
	}
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
	resp, err := loopbackauth.PostForm(ctx, c.HTTPClient,
		locallisten.JoinPath(c.connectURL, "/oauth/step-up"),
		form, c.ApplyAuth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	// The Hub checks the app's elevation permission before checking the
	// credential's issuance. Its 403 response can explain how to allow
	// elevation in Preferences › Apps › App registrations.
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
	// 400 is the credential kind the device ceremony cannot carry: a
	// browser-issued credential gets the refusal that identifies the browser leg,
	// and every other 400 is a credential kind that carries no window at
	// all. Both are permanent for the device ceremony -- but the first one
	// has a working alternative, so it is a sentinel rather than a failure.
	if resp.StatusCode == http.StatusBadRequest {
		body := loopbackauth.ReadOAuthError(resp)
		if body.Error == oauthapp.BrowserStepUpRequiredCode {
			return nil, errBrowserStepUpRequired
		}
		return nil, ErrElevationUnsupported
	}
	if resp.StatusCode == http.StatusUnauthorized {
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
	resp, err := loopbackauth.PostForm(ctx, c.HTTPClient,
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

// elevateViaLoopback runs the browser step-up of a credential the hub
// refuses to verify through the device ceremony. The person at the browser
// is on THIS machine, so the redirect comes back to a loopback listener here.
func (c *Client) elevateViaLoopback(
	ctx context.Context, start func(loopbackauth.Request) (*loopbackauth.Flow, error),
) error {
	creds, err := LoadCredentials(c.HubURL)
	if err != nil {
		// Only a missing credential means "sign in again". A file that
		// will not parse is a different problem with a different remedy,
		// and the parse error must reach the user instead of the
		// sign-in message.
		if errors.Is(err, ErrNotLoggedIn) {
			return errStepUpNeedsSignIn
		}
		return err
	}
	if creds.TokenID == "" {
		return errStepUpNeedsSignIn
	}
	flow, err := start(loopbackauth.Request{
		HubBaseURL:       c.HubURL,
		App:              CLIApp(creds.ClientIDOrBuiltIn()),
		InstallationName: DefaultDeviceName(),
		StepUp:           loopbackauth.StepUpForToken(creds.TokenID),
	})
	if err != nil {
		return err
	}
	defer flow.Close()

	authorizeURL := flow.AuthorizeURL()
	_, _ = fmt.Fprintln(Err, "This command needs you to verify your identity.")
	_, _ = fmt.Fprintln(Err, "Open this URL in your browser:")
	_, _ = fmt.Fprintln(Err, " ", authorizeURL)
	_ = OpenBrowser(authorizeURL)

	code, err := flow.Wait(ctx)
	if err != nil {
		return err
	}
	// The access token can expire or rotate while the person is at the
	// browser; the redemption must present the credential the code identifies.
	if err := c.EnsureFreshBearer(ctx); err != nil {
		return err
	}
	return c.redeemStepUpCode(ctx, flow, code)
}

// redeemStepUpCode exchanges the loopback step-up's code for the window.
// A step-up mints nothing, so the answer is the confirmation alone.
func (c *Client) redeemStepUpCode(ctx context.Context, flow *loopbackauth.Flow, code string) error {
	resp, err := c.postStepUpRedemption(ctx, flow, code)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		// The hub keeps the code valid after a 401, so a repaired bearer is
		// worth one more post rather than a lost ceremony.
		if err := c.repairAfterUnauthenticated(ctx); err != nil {
			return err
		}
		resp, err = c.postStepUpRedemption(ctx, flow, code)
		if err != nil {
			return err
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body := loopbackauth.ReadOAuthError(resp)
		if body.Error != "" {
			return errors.New(body.Message())
		}
		return fmt.Errorf("the hub refused the verification request: %s", resp.Status)
	}
	var out struct {
		Elevated bool `json:"elevated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.Elevated {
		return errors.New("the hub did not confirm the verification")
	}
	return nil
}

// postStepUpRedemption performs one redemption attempt, presenting the
// bearer this client holds.
func (c *Client) postStepUpRedemption(ctx context.Context, flow *loopbackauth.Flow, code string) (*http.Response, error) {
	return loopbackauth.PostForm(ctx, c.HTTPClient,
		locallisten.JoinPath(c.connectURL, "/oauth/token"),
		flow.TokenForm(code), c.ApplyAuth)
}

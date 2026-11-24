package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/loopbackauth"
)

// --- auth login -------------------------------------------------------

// runAuthLogin implements `leapmux auth login`. Tries the
// local-redirect (PKCE) flow by default; falls back to (or honors
// --device-code) when the local listener can't be reached from a
// browser.
//
// A solo hub needs no login over local IPC. Its TCP addresses use ordinary
// sessions after first-password setup. Solo still authorizes apps: the solo
// rung yields to a presented lmx_ bearer, so a credential minted here binds
// its scope there.
func runAuthLogin(inv *cli.Invocation) (any, error) {
	hub, err := hubURLFor(inv.Command)
	if noHub := (*control.NoHubError)(nil); errors.As(err, &noHub) {
		// The no-hub hint suggests a login, which is this command.
		return nil, cli.Usagef("no hub selected").WithHint("pass --hub <url> to choose the hub to sign in to")
	}
	if err != nil {
		return nil, err
	}
	deviceName := strings.TrimSpace(stringFlag(inv, "device-name"))
	if deviceName == "" {
		deviceName = control.DefaultDeviceName()
	}
	deviceCode := boolFlag(inv, "device-code")
	scopeFlag := stringFlag(inv, "scope")
	ctx := inv.Context

	// Every input is validated BEFORE the destructive cleanup below: a
	// refused flag must not cost the machine its working credential.
	scope, scopeErr := requestedScope(scopeFlag)
	if scopeErr != nil {
		return nil, scopeErr
	}
	if !deviceCode && locallisten.IsLocal(hub) {
		// The local-redirect flow needs a browser to reach BOTH the hub (to
		// load the consent page) and this CLI (the loopback callback). A
		// socket hub URL gives the browser no hub origin to visit — solo
		// deployments need no login at all, and a socket-reached multi-user
		// hub has an http(s) origin derived from its settings. State the
		// working alternative instead of failing mysteriously.
		return nil, cli.Usagef("a unix: or npipe: hub URL gives the browser no hub origin to visit").
			WithHint("pass --device-code to sign in through the hub's public origin")
	}

	// Read any existing credential BEFORE starting the new flow, but do NOT
	// revoke or delete it until the new login succeeds and is atomically saved.
	// That way a canceled browser prompt, Ctrl-C, or hub outage never leaves the
	// user logged out or breaks running scripts.
	var prevCreds *control.CredentialFile
	prevCorrupt := false
	var prevCorruptErr error
	switch creds, loadErr := control.LoadCredentials(hub); {
	case loadErr == nil:
		prevCreds = creds
	case errors.Is(loadErr, control.ErrNotLoggedIn):
		// No existing credential.
	default:
		prevCorrupt = true
		prevCorruptErr = loadErr
	}

	var result *loginResult
	var loginErr error
	if deviceCode {
		result, loginErr = runDeviceCodeLogin(ctx, inv.Printer, hub, deviceName, scope)
	} else {
		result, loginErr = runLocalRedirectLogin(ctx, inv.Printer, hub, deviceName, scope)
	}
	if loginErr != nil {
		return nil, loginErr
	}

	// Best-effort cleanup of the previous grant AFTER the new credential is
	// safely persisted on disk.
	switch {
	case prevCreds != nil:
		if revokeErr := revokeBearer(hub, prevCreds.AccessToken, prevCreds.ClientIDOrBuiltIn()); revokeErr != nil {
			msg := fmt.Sprintf("could not revoke previous login (%s on %s): %v; %s", prevCreds.Username, prevCreds.HubURL, revokeErr, disconnectInPreferences)
			inv.Printer.Notice("warning: %s", msg)
			result.Warning = joinWarnings(result.Warning, msg)
		}
	case prevCorrupt:
		msg := fmt.Sprintf("the previous credential file did not parse (%v); replaced it, and cannot revoke its old grant so %s if the hub still lists it", prevCorruptErr, disconnectInPreferences)
		inv.Printer.Notice("warning: %s", msg)
		result.Warning = joinWarnings(result.Warning, msg)
	}
	return result, nil
}

func joinWarnings(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

// The login prompts are prose for a person, so they go to stderr. stdout
// holds only the result, and `auth login --json | jq` parses it.

func runLocalRedirectLogin(ctx context.Context, p *cli.Printer, hubURL, deviceName, scope string) (*loginResult, error) {
	// The socket-hub refusal this flow needs already ran in runAuthLogin,
	// before the destructive cleanup: a refused flag must not cost the
	// machine its working credential.
	flow, err := loopbackauth.Start(loopbackauth.Request{
		HubBaseURL:       hubURL,
		App:              control.CLIApp(control.ControlCLIClientID),
		Scope:            scope,
		InstallationName: deviceName,
	})
	if err != nil {
		return nil, err
	}
	defer flow.Close()

	startURL := flow.AuthorizeURL()
	p.Notice("Open this URL in your browser to authorize the CLI:")
	p.Notice("  %s", startURL)
	_ = control.OpenBrowser(startURL)

	spin := cli.StartSpinner(p, "waiting for browser authorization...")
	code, err := flow.Wait(ctx)
	spin.Stop()
	switch {
	case err == nil:
	case errors.Is(err, loopbackauth.ErrTimeout):
		return nil, cli.Newf(cli.CodeTimeout, "the browser did not authorize the CLI within 10 minutes")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	default:
		return nil, cli.Wrap(cli.CodeInternal, err, "the browser authorization callback failed")
	}
	return exchangeAuthorizationCode(ctx, hubURL, flow.TokenForm(code), scope)
}

func runDeviceCodeLogin(ctx context.Context, p *cli.Printer, hubURL, deviceName, scope string) (*loginResult, error) {
	hc, baseURL, err := cliHTTPClient(hubURL)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	// Only an ASK. The activation page decides, and the token response below
	// reports what was actually granted -- which is not always what was asked
	// for, because the person at the browser may hold an account that cannot
	// grant part of it.
	form := url.Values{
		"client_id":         {control.ControlCLIClientID},
		"installation_name": {deviceName},
		"scope":             {scope},
	}
	resp, err := loopbackauth.PostForm(ctx, hc,
		locallisten.JoinPath(baseURL, "/oauth/device-authorization"), form)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, cli.Newf(cli.CodeInternal, "the hub refused the device authorization: %s", resp.Status)
	}
	var auth control.DeviceGrant
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return nil, err
	}
	p.Notice("To authorize this CLI, on any device with a browser:")
	p.Notice("  1. Visit %s", auth.VerificationURI)
	p.Notice("  2. Enter the code: %s", auth.UserCode)
	if auth.VerificationURIComplete != "" {
		p.Notice("Or open: %s", auth.VerificationURIComplete)
	}
	spin := cli.StartSpinner(p, "waiting for device authorization...")
	var result *loginResult
	err = auth.Poll(ctx, func(ctx context.Context) error {
		var err error
		result, err = tryExchangeDeviceCode(ctx, hc, baseURL, hubURL, auth.DeviceCode, scope)
		return err
	})
	spin.Stop()
	switch {
	case err == nil:
		return result, nil
	case errors.Is(err, control.ErrDeviceGrantExpired):
		return nil, cli.Newf(cli.CodeTimeout, "the device code expired before anybody entered it")
	default:
		return nil, err
	}
}

// tryExchangeDeviceCode performs one /oauth/token poll. nil on
// success (creds saved); control.ErrAuthorizationPending /
// control.ErrSlowDown when the user did not complete the flow yet.
//
// The caller SUPPLIES the client and its base URL, and hubURL stays a
// separate parameter because the saved credential is keyed by the
// user-visible address, not by the placeholder a socket transport dials.
// Building the client here instead allocated a fresh http.Transport on
// every poll for a `unix:`/`npipe:` hub: nothing closes it and its
// IdleConnTimeout is zero, so each poll left one idle socket connection
// and its read goroutine alive for the life of the process.
func tryExchangeDeviceCode(ctx context.Context, hc *http.Client, baseURL, hubURL, deviceCode, requestedScope string) (*loginResult, error) {
	form := url.Values{
		"grant_type":  {control.GrantTypeDeviceCode},
		"client_id":   {control.ControlCLIClientID},
		"device_code": {deviceCode},
	}
	resp, err := loopbackauth.PostForm(ctx, hc, locallisten.JoinPath(baseURL, "/oauth/token"), form)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return persistTokenResponse(hubURL, resp.Body, control.ControlCLIClientID, requestedScope)
	}
	return nil, control.DeviceFlowError(resp)
}

// exchangeAuthorizationCode redeems the local-redirect login's code. The form
// is flow.TokenForm: the verifier and the redirect URI are the ceremony's own
// (RFC 6749 section 4.1.3 makes redirect_uri REQUIRED and identical to the one
// the authorization used), so the hub can refuse a code intercepted at one
// registered address and redeemed as though it came from another.
func exchangeAuthorizationCode(ctx context.Context, hubURL string, form url.Values, requestedScope string) (*loginResult, error) {
	hc, baseURL, err := cliHTTPClient(hubURL)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	resp, err := loopbackauth.PostForm(ctx, hc, locallisten.JoinPath(baseURL, "/oauth/token"), form)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, cli.Newf(cli.CodeInternal, "the hub refused the authorization code: %s", resp.Status)
	}
	return persistTokenResponse(hubURL, resp.Body, form.Get("client_id"), requestedScope)
}

// loginResult is the result of `auth login`.
type loginResult struct {
	HubURL   string `json:"hub_url"`
	Username string `json:"username"`
	UserID   string `json:"user_id"`
	Scope    string `json:"scope"`
	// Warning lists the permissions that the login asked for and the hub
	// did not grant.
	Warning string `json:"warning,omitempty"`
}

// persistTokenResponse writes the freshly minted credential and returns the
// result of the login. Existing credentials are retired up front in
// runAuthLogin after the replacement token is persisted.
func persistTokenResponse(hubURL string, body io.Reader, clientID, requestedScope string) (*loginResult, error) {
	var out struct {
		control.TokenResponseBody
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, err
	}

	now := time.Now()
	creds := control.CredentialFile{
		HubURL:       hubURL,
		ClientID:     clientID,
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(out.ExpiresIn) * time.Second),
		UserID:       out.UserID,
		Username:     out.Username,
		TokenID:      out.TokenID,
		Scope:        out.Scope,
	}
	if out.RefreshExpiresIn > 0 {
		creds.RefreshExpiresAt = now.Add(time.Duration(out.RefreshExpiresIn) * time.Second)
	}
	if err := control.SaveCredentials(hubURL, creds); err != nil {
		return nil, err
	}

	result := &loginResult{HubURL: hubURL, Username: out.Username, UserID: out.UserID, Scope: out.Scope}
	if missing := control.MissingScopes(requestedScope, out.Scope); len(missing) > 0 {
		// Say so HERE rather than letting the first call that needs one fail
		// with a permission error that specifies nothing the user did.
		result.Warning = "these permissions were requested but not granted: " + strings.Join(missing, ", ")
	}
	return result, nil
}

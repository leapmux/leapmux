// Package cmd implements the leaf commands of `leapmux control ...`.
// Each entry is a func compatible with the dispatcher's signature (the
// cmdCtx shape) so these entries reuse the same flag-parsing scaffolding
// that the command trees use.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/oauth2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/locallisten"
)

// Ctx is the dispatcher-supplied context. The cmd package keeps its
// own minimal shape so it doesn't pull in cmd/leapmux's cmdCtx
// (avoids an import cycle).
type Ctx interface {
	Path() string
	Description() string
}

// adminCmdLike adapts cmdCtx (any struct with Path / Description
// fields).
type adminCmdLike struct {
	PathStr        string
	DescriptionStr string
}

func (c adminCmdLike) Path() string        { return c.PathStr }
func (c adminCmdLike) Description() string { return c.DescriptionStr }

// asCtx accepts the dispatcher's cmdCtx (declared in main package)
// via reflection-free duck typing: callers convert with adminCmdLike.
func asCtx(v any) Ctx {
	if c, ok := v.(Ctx); ok {
		return c
	}
	return adminCmdLike{PathStr: "control", DescriptionStr: ""}
}

// flagSet returns a flag.FlagSet pre-configured with --hub.
func flagSet(cmd Ctx, hubPtr *string) *flag.FlagSet {
	fs := flag.NewFlagSet("leapmux "+cmd.Path(), flag.ContinueOnError)
	fs.StringVar(hubPtr, "hub", os.Getenv("LEAPMUX_HUB"), "hub URL (or LEAPMUX_HUB env var)")
	return fs
}

// parseFlags consolidates the boilerplate around ConfigureAndParse.
func parseFlags(fs *flag.FlagSet, args []string, description string) error {
	return internalconfig.ConfigureAndParse(fs, args, description, nil, nil)
}

// parseFlagsWithPositionals is parseFlags for leaves that take positional
// arguments after the flags (settings get KEY, settings set KEY VALUE):
// ConfigureAndParse's RejectPositionalArgs would refuse them.
//
// usage states the positional form, and --help prints it. PrintFlagUsage
// knows only the flags, so it writes `Usage: <cmd> [flags]`; without this
// line the help of a leaf that REQUIRES two positionals never states them,
// and only a wrong count answered with the form.
func parseFlagsWithPositionals(fs *flag.FlagSet, args []string, description, usage string) error {
	if internalconfig.HasHelpArg(args) {
		fs.SetOutput(os.Stdout)
	}
	fs.Usage = func() {
		internalconfig.PrintFlagUsage(fs, description, nil, nil)
		if usage != "" {
			_, _ = fmt.Fprintf(fs.Output(), "\n%s\n", usage)
		}
	}
	return fs.Parse(args)
}

// pathCmdFlags carries the standard flags every worker-bound path
// command takes: --hub, the entity-resolver flag set (--workspace-id /
// --tab-id / --tile-id / --worker-id), and --path. Returned unparsed
// so callers can bind command-specific flags before calling parseFlags.
type pathCmdFlags struct {
	Hub  string
	Path string
	In   resolve.Inputs
	FS   *flag.FlagSet
}

// bindPathCmd binds the common --hub + entity + --path flags onto a
// fresh FlagSet. When defaultFromEnv is true the --path default is
// workingDirEnv(); otherwise the flag has no default and the caller
// must enforce non-empty after parseFlags. Callers add per-command
// extra flags on the returned FlagSet, then call parseFlags themselves.
func bindPathCmd(cmd Ctx, defaultFromEnv bool, usage string) *pathCmdFlags {
	out := &pathCmdFlags{}
	out.FS = flagSet(cmd, &out.Hub)
	resolve.BindEntityFlags(out.FS, &out.In, resolve.FlagOptions{})
	def := ""
	if defaultFromEnv {
		def = workingDirEnv()
	}
	out.FS.StringVar(&out.Path, "path", def, usage)
	return out
}

// Require returns an invalid_request envelope when Path is empty.
// hint is appended after the canonical "--path is required" so commands
// can document path semantics ("must be a file path; …").
func (f *pathCmdFlags) Require(hint string) error {
	if f.Path != "" {
		return nil
	}
	msg := "--path is required"
	if hint != "" {
		msg = msg + " " + hint
	}
	return control.EmitError("invalid_request", msg)
}

// requireClient builds a control.Client from --hub or the LEAPMUX_CONTROL_*
// env vars. Returns a clear error envelope on failure.
func requireClient(hubFlag string) (*control.Client, error) {
	c, err := control.NewClientFromEnv(hubFlag)
	if err != nil {
		return nil, control.EmitErrorWith("not_logged_in", err)
	}
	return c, nil
}

// --- auth login -------------------------------------------------------

// RunAuthLogin implements `leapmux control auth login`. Tries the
// local-redirect (PKCE) flow by default; falls back to (or honors
// --device-code) when the local listener can't be reached from a
// browser.
//
// A solo hub needs no login to USE, because it authenticates every request as
// its one account. It still authorizes apps: the solo rung yields to a
// presented lmx_ bearer, so a credential minted here binds its scope there --
// which is how an agent on a solo machine holds file:read and nothing more.
// Both flows complete, because the consent stages accept the solo account.
func RunAuthLogin(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, deviceName, scopeFlag string
	var deviceCode bool
	fs := flagSet(cmd, &hub)
	// The label is recorded on the grant and surfaces in the account's own
	// records (Connected apps names each credential by it), but the consent
	// pages deliberately show only the app's verified identity: the label is
	// requester-chosen, so rendering it on a decision screen would let a
	// phisher manufacture "your own laptop" reassurance out of nothing.
	fs.StringVar(&deviceName, "device-name", control.DefaultDeviceName(), "label recorded on the grant; the account's Connected-apps list names the credential by it")
	fs.BoolVar(&deviceCode, "device-code", false, "force RFC 8628 device-code flow (headless / SSH / container)")
	// The grant. EMPTY asks for everything except the admin scopes,
	// which is what an ordinary login has always meant -- so the credential
	// file this leaves on disk for months can do everything its owner can do
	// EXCEPT administer the hub, and an administering credential exists only
	// when somebody asked for one.
	fs.StringVar(&scopeFlag, "scope", "",
		"space- or comma-separated permissions, e.g. \"file:read git:read\" (empty = everything except admin:*)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if hub == "" {
		return control.EmitError("invalid_request", "--hub is required")
	}
	ctx := context.Background()

	if deviceCode {
		scope, scopeErr := requestedScope(scopeFlag)
		if scopeErr != nil {
			return scopeErr
		}
		return runDeviceCodeLogin(ctx, hub, deviceName, scope)
	}
	scope, scopeErr := requestedScope(scopeFlag)
	if scopeErr != nil {
		return scopeErr
	}
	return runLocalRedirectLogin(ctx, hub, deviceName, scope)
}

func runLocalRedirectLogin(ctx context.Context, hubURL, deviceName, scope string) error {
	// The local-redirect flow needs a browser to reach BOTH the hub (to
	// load the consent page) and this CLI (the loopback callback). A
	// socket hub URL gives the browser no hub origin to visit — solo
	// deployments need no login at all, and a socket-reached multi-user
	// hub has an http(s) origin derived from its settings. State the
	// working alternative instead of failing mysteriously.
	if locallisten.IsLocal(hubURL) {
		return control.EmitError("invalid_request",
			"--hub unix:/npipe: URLs cannot use the local-redirect flow (no browser-reachable hub origin); pass --device-code to authenticate in a browser against the hub's public origin")
	}
	verifier := oauth2.GenerateVerifier()
	challenge := pkce.S256(verifier)
	state := id.Generate()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return control.EmitErrorWith("listen_failed", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := localRedirectURI(port)

	startParams := url.Values{
		"client_id":             {control.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"installation_name":     {deviceName},
		"scope":                 {scope},
	}
	startURL := locallisten.JoinPath(hubURL, "/oauth/authorize?"+startParams.Encode())
	_, _ = fmt.Fprintln(control.Out, "Open this URL in your browser to authorize the CLI:")
	_, _ = fmt.Fprintln(control.Out, " ", startURL)
	_ = openBrowser(startURL)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: callbackHandler(state, codeCh, errCh)}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return control.EmitErrorWith("callback_error", err)
	case code = <-codeCh:
	case <-time.After(10 * time.Minute):
		return control.EmitError("timeout", "timed out waiting for browser authorization")
	}

	return exchangeAuthorizationCode(ctx, hubURL, code, verifier, redirectURI, scope)
}

func runDeviceCodeLogin(ctx context.Context, hubURL, deviceName, scope string) error {
	hc, baseURL := cliHTTPClient(hubURL)
	// Only an ASK. The activation page decides, and the token response below
	// reports what was actually granted -- which is not always what was asked
	// for, because the person at the browser may hold an account that cannot
	// grant part of it.
	form := url.Values{
		"client_id":         {control.ControlCLIClientID},
		"installation_name": {deviceName},
		"scope":             {scope},
	}
	resp, err := control.PostForm(ctx, hc,
		locallisten.JoinPath(baseURL, "/oauth/device-authorization"), form)
	if err != nil {
		return control.EmitErrorWith("device_authorization_failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return control.EmitError("device_authorization_failed", resp.Status)
	}
	var auth control.DeviceGrant
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return control.EmitErrorWith("device_authorization_failed", err)
	}
	_, _ = fmt.Fprintln(control.Out, "To authorize this CLI, on any device with a browser:")
	_, _ = fmt.Fprintln(control.Out, "  1. Visit", auth.VerificationURI)
	_, _ = fmt.Fprintln(control.Out, "  2. Enter the code:", auth.UserCode)
	if auth.VerificationURIComplete != "" {
		_, _ = fmt.Fprintln(control.Out, "Or open:", auth.VerificationURIComplete)
	}
	err = auth.Poll(ctx, func(ctx context.Context) error {
		return tryExchangeDeviceCode(ctx, hc, baseURL, hubURL, auth.DeviceCode, scope)
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, control.ErrDeviceGrantExpired):
		return control.EmitError("expired_token", "device code expired")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return control.EmitErrorWith("device_grant_failed", err)
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
func tryExchangeDeviceCode(ctx context.Context, hc *http.Client, baseURL, hubURL, deviceCode, requestedScope string) error {
	form := url.Values{
		"grant_type":  {control.GrantTypeDeviceCode},
		"client_id":   {control.ControlCLIClientID},
		"device_code": {deviceCode},
	}
	resp, err := control.PostForm(ctx, hc, locallisten.JoinPath(baseURL, "/oauth/token"), form)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return persistTokenResponse(hubURL, resp.Body, control.ControlCLIClientID, requestedScope)
	}
	return control.DeviceFlowError(resp)
}

func exchangeAuthorizationCode(ctx context.Context, hubURL, code, verifier, redirectURI, requestedScope string) error {
	form := url.Values{
		"grant_type":    {control.GrantTypeAuthorizationCode},
		"client_id":     {control.ControlCLIClientID},
		"code":          {code},
		"code_verifier": {verifier},
		// RFC 6749 section 4.1.3 makes this REQUIRED and identical to the one
		// the authorization used, so the hub can refuse a code intercepted at
		// one registered address and redeemed as though it came from another.
		"redirect_uri": {redirectURI},
	}
	hc, baseURL := cliHTTPClient(hubURL)
	resp, err := control.PostForm(ctx, hc, locallisten.JoinPath(baseURL, "/oauth/token"), form)
	if err != nil {
		return control.EmitErrorWith("token_exchange_failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return control.EmitError("token_exchange_failed", resp.Status)
	}
	return persistTokenResponse(hubURL, resp.Body, control.ControlCLIClientID, requestedScope)
}

// persistTokenResponse writes the freshly minted credential and retires the
// one it replaces.
//
// The ORDER is deliberate: save first, revoke second. A crash between the
// two leaves the user logged in with one abandoned row on the hub, which the
// device list shows and the expiry sweep eventually removes. The reverse
// order would leave them logged OUT with a credential file the hub
// already refused -- the failure the user cannot fix without a browser.
//
// The revoke is best-effort for the same reason: a hub that is briefly
// unreachable must not turn a successful login into a failed one.
func persistTokenResponse(hubURL string, body io.Reader, clientID, requestedScope string) error {
	var out struct {
		control.TokenResponseBody
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return control.EmitErrorWith("token_exchange_failed", err)
	}
	// Read the outgoing credential BEFORE the new one overwrites the file.
	previous, previousErr := control.LoadCredentials(hubURL)

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
		return control.EmitErrorWith("save_credentials_failed", err)
	}
	// Retire the credential this login replaced. Without this, each re-login
	// abandons a row nobody revoked, whose plaintext refresh secret stays
	// live for months on this machine's disk history and in the hub's table.
	retirementWarning := ""
	if previousErr == nil && previous.AccessToken != "" && previous.AccessToken != creds.AccessToken {
		if err := revokeBearer(hubURL, previous.AccessToken, previous.ClientIDOrBuiltIn()); err != nil {
			// Best-effort by design: the new credential is already on disk
			// and the login succeeded. But it must not be SILENT, or the
			// retirement reads as done on exactly the runs where it did not
			// happen -- and the old refresh secret then stays live for the
			// rest of its window.
			retirementWarning = "the previous credential could not be revoked (" + err.Error() +
				"); disconnect it under Preferences, Account, Connected apps"
		}
	}

	result := map[string]any{
		"hub_url":  hubURL,
		"username": out.Username,
		"user_id":  out.UserID,
		"scope":    out.Scope,
	}
	// EVERY warning, joined, not the first one that matches. A `switch`
	// reported one and discarded the rest, so a login that was refused part of
	// its scope AND could not retire its predecessor said nothing about the
	// credential still live on the hub -- the exact silence the retirement
	// warning exists to break.
	var warnings []string
	if missing := control.MissingScopes(requestedScope, out.Scope); len(missing) > 0 {
		// Say so HERE rather than letting the first call that needs one fail
		// with a permission error that specifies nothing the user did.
		warnings = append(warnings,
			"these permissions were requested but not granted: "+strings.Join(missing, ", "))
	}
	if retirementWarning != "" {
		warnings = append(warnings, retirementWarning)
	}
	if len(warnings) > 0 {
		result["warning"] = strings.Join(warnings, "; ")
	}
	return control.EmitData(result)
}

// --- auth logout / list / status -------------------------------------

func RunAuthLogout(rawCtx any, args []string) error {
	hub, err := parseHubOnly(rawCtx, args, nil)
	if err != nil {
		return err
	}
	// The revoke's answer is REPORTED, not discarded. revokeBearer reads the
	// status precisely so a refusal stops reading as success, and the local
	// file goes either way -- logout must stay locally idempotent -- so
	// swallowing the error here left the row live with its ninety-day
	// refresh secret, printed a clean result, and took away the bearer the
	// user needed to retry.
	warning := ""
	creds, err := control.LoadCredentials(hub)
	if err == nil {
		if revokeErr := revokeBearer(hub, creds.AccessToken, creds.ClientIDOrBuiltIn()); revokeErr != nil {
			warning = "the credential could not be revoked on the hub (" + revokeErr.Error() +
				"); it is gone from this machine, so disconnect it under Preferences, Account, Connected apps"
		}
	}
	if err := control.DeleteCredentials(hub); err != nil {
		return control.EmitErrorWith("delete_failed", err)
	}
	result := map[string]any{"hub_url": hub}
	if warning != "" {
		result["warning"] = warning
	}
	return control.EmitData(result)
}

func RunAuthList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	fs := flag.NewFlagSet("leapmux "+cmd.Path(), flag.ContinueOnError)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	files, err := control.ListCredentialFiles()
	if err != nil {
		return control.EmitErrorWith("list_failed", err)
	}
	out := make([]map[string]any, 0, len(files))
	for _, c := range files {
		out = append(out, map[string]any{
			"hub_url":  c.HubURL,
			"username": c.Username,
			"user_id":  c.UserID,
			"expires":  c.ExpiresAt,
			"scope":    c.Scope,
		})
	}
	return control.EmitData(out)
}

// RunAuthCredentials lists the ACCOUNT's command-line credentials, as the
// hub holds them -- which is a different question from `auth list`, and the
// reason both exist. `auth list` reads this machine's credential files and
// answers "which hubs am I signed in to from here". This asks the hub
// "what else can reach my account", which is the question somebody has when
// they suspect a credential is stolen, and it is the same list Preferences
// shows.
//
// It is also the only caller that can make `current` true. The field marks
// the row the request itself authenticated with, and the hub derives it from
// the caller's own credential -- so a browser session, which is what every
// other caller is, always reads false.
func RunAuthCredentials(rawCtx any, args []string) error {
	hub, err := parseHubOnly(rawCtx, args, nil)
	if err != nil {
		return err
	}
	c, err := control.NewClient(hub)
	if err != nil {
		return control.EmitErrorWith("not_logged_in", err)
	}
	// Every page, not the first: an omitted limit resolves to a default on
	// the hub, and a credential the listing never printed is one the operator
	// cannot see to revoke.
	// Keyed by id while assembling: a keyset boundary can repeat a row
	// across two pages, and a stalled cursor makes the last page arrive
	// twice. Neither should print the same credential as two live ones to
	// somebody auditing what can reach their account.
	seen := make(map[string]bool)
	out := make([]map[string]any, 0)
	cursor := ""
	for range maxCredentialPages {
		resp, err := c.UserService().ListMyAPITokens(context.Background(), connect.NewRequest(&leapmuxv1.ListMyAPITokensRequest{
			Cursor: cursor,
			Limit:  credentialPageSize,
		}))
		if err != nil {
			return control.EmitErrorWith("rpc_failed", err)
		}
		for _, tok := range resp.Msg.GetTokens() {
			if seen[tok.GetId()] {
				continue
			}
			seen[tok.GetId()] = true
			row := map[string]any{
				"id":                tok.GetId(),
				"client_id":         tok.GetClientId(),
				"client_name":       tok.GetClientName(),
				"client_verified":   tok.GetClientVerified(),
				"installation_name": tok.GetInstallationName(),
				"granted_scopes":    tok.GetGrantedScopes(),
				// True for the credential THIS command uses, so an operator
				// does not revoke the device they work from.
				"current": tok.GetCurrent(),
			}
			// putTime, like every other listing verb in this package: it
			// omits an unset instant and renders the rest in the one layout
			// the CLI's JSON uses, so `auth credentials` does not print
			// timestamps in a shape of its own.
			putTime(row, "created_at", tok.GetCreatedAt())
			putTime(row, "last_used_at", tok.GetLastUsedAt())
			putTime(row, "refresh_expires", tok.GetRefreshExpiresAt())
			// The fixed-lifetime kind carries no refresh deadline, so its
			// whole life is reported here instead. Exactly one of the two is
			// ever set, and putTime omits the absent one -- so a credential
			// with no deadline in this listing is one that never expires,
			// rather than one whose deadline the CLI cannot state.
			putTime(row, "expires", tok.GetExpiresAt())
			out = append(out, row)
		}
		next := resp.Msg.GetNextCursor()
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return control.EmitData(out)
}

const (
	// credentialPageSize is the hub's maximum page, READ from the hub's own
	// constant rather than restated. Asking for it makes the listing one
	// round trip for any real account; an omitted limit resolves to
	// service.DefaultPageLimit, so the loop below took ten times the
	// requests and covered a tenth of what its own limit claimed.
	credentialPageSize = service.MaxPageLimit
	// maxCredentialPages limits the listing loop. At credentialPageSize
	// that covers a quarter of a million credentials, so it is a guard
	// against a cursor that never advances, not a limit anybody reaches.
	maxCredentialPages = 500
)

func RunAuthStatus(rawCtx any, args []string) error {
	hub, err := parseHubOnly(rawCtx, args, nil)
	if err != nil {
		return err
	}
	creds, err := control.LoadCredentials(hub)
	if err != nil {
		return control.EmitErrorWith("not_logged_in", err)
	}
	status := map[string]any{
		"hub_url":  creds.HubURL,
		"username": creds.Username,
		"user_id":  creds.UserID,
		"expires":  creds.ExpiresAt,
		"expired":  time.Now().After(creds.ExpiresAt),
		// The access token above renews itself; this is the deadline that
		// actually sends the user back to a browser.
		"scope":    creds.Scope,
		"token_id": creds.TokenID,
	}
	// Included only when the credential carries one: the field is zero on a
	// credential written before the hub reported it, and a hand-built map has
	// no omitzero to keep "0001-01-01T00:00:00Z" -- a nonsense date on the one
	// deadline a reader acts on -- out of the JSON envelope.
	if !creds.RefreshExpiresAt.IsZero() {
		status["refresh_expires"] = creds.RefreshExpiresAt
	}
	return control.EmitData(status)
}

// --- helpers ----------------------------------------------------------

// cliRESTTimeout caps one CLI-auth REST request over EITHER transport.
const cliRESTTimeout = 60 * time.Second

// cliHTTPClient returns the HTTP client the CLI-auth REST calls should
// use for hubURL: a socket-dialer-backed client (with the placeholder
// http://localhost base) for `unix:`/`npipe:` hub URLs, and a plain client
// that carries the SAME timeout otherwise. http.DefaultClient cannot dial a
// socket URL, so without the first the device-code flow silently cannot log
// in against a hub reached over its IPC listener; and http.DefaultClient has
// no timeout at all, so a remote hub that accepts a connection and never
// answers used to hang the command for ever. locallisten.RESTClient holds
// both rules, because the login-flow calls in this package and the refresh
// stage in `control` need the same answer.
//
// A socket client that fails to build falls through to the remote client,
// which SelectClient does for every transport factory in the tree: the
// request then fails with a scheme error that gives the URL, which is
// better than a panic here.
func cliHTTPClient(hubURL string) (*http.Client, string) {
	return locallisten.RESTClient(hubURL, cliRESTTimeout)
}

// callbackHandler serves the local-redirect login's /callback. Its channel
// sends are NON-BLOCKING: the success page is a plain 200, and a user reload
// re-GETs this address with the same valid values. The channels hold one
// outcome each; a duplicate that arrives after the CLI has its answer is
// ACKNOWLEDGED here and dropped, because a blocking send parks the handler
// and the deferred srv.Shutdown waits for it forever -- a login that already
// succeeded would hang the CLI.
func callbackHandler(state string, codeCh chan<- string, errCh chan<- error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		gotState := q.Get("state")
		gotCode := q.Get("code")
		// RFC 6749 section 4.1.2.1: a redirect the hub could validate carries
		// either a code or an error, plus the echoed state. Reading the error
		// parameter matters because the hub redirects EVERY refusal it could
		// validate back here -- a Deny on the consent page, an invalid_scope,
		// a server error -- and folding those into the state-mismatch branch
		// told a user who deliberately refused that the state check had failed,
		// a CSRF suspicion they can neither check nor act on. The state check
		// stays first: a mismatched state is refused as a mismatch whatever
		// else the query carries.
		if gotState != state {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			select {
			case errCh <- errors.New("callback state mismatch"):
			default:
			}
			return
		}
		if gotErr := q.Get("error"); gotErr != "" {
			msg := gotErr
			if desc := q.Get("error_description"); desc != "" {
				msg = gotErr + ": " + desc
			}
			http.Error(w, "authorization refused", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("authorization failed: %s", msg):
			default:
			}
			return
		}
		if gotCode == "" {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			select {
			case errCh <- errors.New("callback carried neither a code nor an error"):
			default:
			}
			return
		}
		_, _ = fmt.Fprintln(w, "Authorization received. You can close this window and return to the CLI.")
		select {
		case codeCh <- gotCode:
		default:
		}
	})
}

// localRedirectURI binds an ephemeral port onto the REGISTERED loopback
// redirect rather than a local literal: the hub matches the callback against
// the registration (scheme, host and path exact; only the port free per RFC
// 8252 section 7.3), so the path here is whatever the constant says, and a
// future edit to the registration cannot silently break every CLI login.
func localRedirectURI(port int) string {
	u, err := url.Parse(control.ControlCLIRedirectURI)
	if err != nil {
		// Unreachable for the constant's shape; the fallback keeps the
		// registered host and path rather than inventing a third spelling.
		return fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	}
	u.Host = fmt.Sprintf("127.0.0.1:%d", port)
	return u.String()
}

// revokeBearer ends one credential. clientID is the app the credential was
// issued to -- the hub binds the revocation to that app (RFC 7009 section
// 2.1), so a credential minted to another registration must present its own
// id and not the CLI's.
func revokeBearer(hubURL, bearer, clientID string) error {
	if bearer == "" {
		return nil
	}
	// Resolve the transport FIRST and build the request against its base
	// URL, the way the sibling token calls do. Building against hubURL and
	// then patching req.URL discarded the parse error, so a URL that failed
	// to parse left req.URL nil and answered with a generic
	// `http: nil Request.URL` instead of stating the address.
	hc, baseURL := cliHTTPClient(hubURL)
	// The CLI identifies itself so the revocation stage can bind the credential to
	// the app it was issued to: this is a public client, and RFC 7009 section
	// 2.1 has a public client identify itself with its client_id rather than
	// a secret it does not hold.
	form := url.Values{
		"token":     {bearer},
		"client_id": {clientID},
	}
	resp, err := control.PostForm(context.Background(), hc,
		locallisten.JoinPath(baseURL, "/oauth/revoke"), form,
		func(h http.Header) { h.Set("Authorization", "Bearer "+bearer) })
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	// A row that is already gone is a revoke that already happened. The hub
	// hard-deletes an api_tokens row once both of its deadlines close, and
	// it answers a bearer whose row it cannot find with 401 -- so reporting
	// that as a failure told the user to revoke a credential under
	// Preferences that no listing there shows. 404 reads the same way,
	// whether it comes from the hub or from a proxy in front of it.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	// Every OTHER status is the answer. Without reading it this reported
	// success for a hub that refused, so `auth logout` printed a clean
	// result and the credential stayed live, and the re-login retirement
	// silently kept the very row its comment says it exists to remove.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("revoke failed: %s", resp.Status)
	}
	return nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// requestedScope normalizes a --scope value into the RFC 6749 section 3.3 wire
// form.
//
// It accepts BOTH separators, for the reason splitScopeFlag gives: the wire
// format is space-delimited, which a shell needs quoted, and a comma-separated
// list is what somebody types without thinking about quoting.
func requestedScope(raw string) (string, error) {
	tokens, err := splitScopeFlag(raw)
	if err != nil {
		return "", err
	}
	return strings.Join(tokens, " "), nil
}

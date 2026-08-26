// Package cmd implements the leaf commands of `leapmux control ...`.
// Each entry is a func compatible with the dispatcher's signature (the
// cmdCtx shape) so the same flag-parsing scaffolding the command trees
// use is reused.
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
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/locallisten"
)

// OAuth 2.0 grant-type wire identifiers (RFC 6749 §4.1.3, RFC 8628
// §3.4). Mirrored on the server side in hub/service/api_auth_token.go;
// stable per spec so drift between the two definitions cannot occur.
const (
	grantTypeAuthorizationCode = "authorization_code"
	grantTypeDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
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
// line the help of a leaf that REQUIRES two positionals never names them,
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
// Solo deployments need NO login: the hub's solo mode authenticates every
// request as the solo user, so there is no credential to obtain. (The
// device-code flow in particular cannot complete there — activation is
// cookies-only and solo has no cookie session.)
func RunAuthLogin(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, deviceName string
	var deviceCode, adminScope bool
	fs := flagSet(cmd, &hub)
	fs.StringVar(&deviceName, "device-name", defaultDeviceName(), "label recorded on the grant and shown on the consent page")
	fs.BoolVar(&deviceCode, "device-code", false, "force RFC 8628 device-code flow (headless / SSH / container)")
	fs.BoolVar(&adminScope, "admin", false, "also request hub administration for this credential (`leapmux control admin ...`)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if hub == "" {
		return control.EmitError("invalid_request", "--hub is required")
	}
	ctx := context.Background()

	if deviceCode {
		return runDeviceCodeLogin(ctx, hub, deviceName, adminScope)
	}
	return runLocalRedirectLogin(ctx, hub, deviceName, adminScope)
}

func runLocalRedirectLogin(ctx context.Context, hubURL, deviceName string, adminScope bool) error {
	// The local-redirect flow needs a browser to reach BOTH the hub (to
	// load the consent page) and this CLI (the loopback callback). A
	// socket hub URL gives the browser no hub origin to visit — solo
	// deployments need no login at all, and a socket-reached multi-user
	// hub has an http(s) origin derived from its settings. Name the
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
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	startParams := url.Values{
		"redirect_uri":   {redirectURI},
		"state":          {state},
		"code_challenge": {challenge},
		"device_name":    {deviceName},
	}
	if adminScope {
		startParams.Set("admin", "1")
	}
	startURL := locallisten.JoinPath(hubURL, "/auth/cli/start?"+startParams.Encode())
	_, _ = fmt.Fprintln(control.Out, "Open this URL in your browser to authorize the CLI:")
	_, _ = fmt.Fprintln(control.Out, " ", startURL)
	_ = openBrowser(startURL)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			gotState := r.URL.Query().Get("state")
			gotCode := r.URL.Query().Get("code")
			if gotState != state || gotCode == "" {
				http.Error(w, "invalid callback", http.StatusBadRequest)
				errCh <- errors.New("callback state mismatch")
				return
			}
			_, _ = fmt.Fprintln(w, "Authorization received. You can close this window and return to the CLI.")
			codeCh <- gotCode
		}),
	}
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

	return exchangeAuthorizationCode(ctx, hubURL, code, verifier, adminScope)
}

func runDeviceCodeLogin(ctx context.Context, hubURL, deviceName string, adminScope bool) error {
	hc, baseURL := cliHTTPClient(hubURL)
	form := url.Values{"device_name": {deviceName}}
	if adminScope {
		// Only an ASK. The activation page decides, and it says so on the
		// page: a user who types the code by hand rather than opening the
		// complete URI leaves the checkbox clear, and the response below
		// reports what was actually granted.
		form.Set("admin", "1")
	}
	resp, err := hc.PostForm(locallisten.JoinPath(baseURL, "/auth/cli/device-authorization"), form)
	if err != nil {
		return control.EmitErrorWith("device_authorization_failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return control.EmitError("device_authorization_failed", resp.Status)
	}
	var auth struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return control.EmitErrorWith("device_authorization_failed", err)
	}
	_, _ = fmt.Fprintln(control.Out, "To authorize this CLI, on any device with a browser:")
	_, _ = fmt.Fprintln(control.Out, "  1. Visit", auth.VerificationURI)
	_, _ = fmt.Fprintln(control.Out, "  2. Enter the code:", auth.UserCode)
	if auth.VerificationURIComplete != "" {
		_, _ = fmt.Fprintln(control.Out, "Or open:", auth.VerificationURIComplete)
	}
	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		err := tryExchangeDeviceCode(ctx, hc, baseURL, hubURL, auth.DeviceCode, adminScope)
		if errors.Is(err, errAuthorizationPending) {
			continue
		}
		if errors.Is(err, errSlowDown) {
			interval += 5 * time.Second
			continue
		}
		if err != nil {
			return control.EmitErrorWith("device_grant_failed", err)
		}
		return nil
	}
	return control.EmitError("expired_token", "device code expired")
}

var (
	errAuthorizationPending = errors.New("authorization_pending")
	errSlowDown             = errors.New("slow_down")
)

// tryExchangeDeviceCode performs one /auth/cli/token poll. nil on
// success (creds saved); errAuthorizationPending / errSlowDown when
// the user hasn't completed the flow yet.
//
// The caller SUPPLIES the client and its base URL, and hubURL stays a
// separate parameter because the saved credential is keyed by the
// user-visible address, not by the placeholder a socket transport dials.
// Building the client here instead allocated a fresh http.Transport on
// every poll for a `unix:`/`npipe:` hub: nothing closes it and its
// IdleConnTimeout is zero, so each poll left one idle socket connection
// and its read goroutine alive for the life of the process.
func tryExchangeDeviceCode(ctx context.Context, hc *http.Client, baseURL, hubURL, deviceCode string, adminScope bool) error {
	form := url.Values{
		"grant_type":  {grantTypeDeviceCode},
		"device_code": {deviceCode},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(baseURL, "/auth/cli/token"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return persistTokenResponse(hubURL, resp.Body, adminScope)
	}
	var oerr struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&oerr)
	switch oerr.Error {
	case "authorization_pending":
		return errAuthorizationPending
	case "slow_down":
		return errSlowDown
	default:
		return fmt.Errorf("%s: %s", oerr.Error, oerr.ErrorDescription)
	}
}

func exchangeAuthorizationCode(ctx context.Context, hubURL, code, verifier string, adminScope bool) error {
	form := url.Values{
		"grant_type":    {grantTypeAuthorizationCode},
		"code":          {code},
		"code_verifier": {verifier},
	}
	hc, baseURL := cliHTTPClient(hubURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(baseURL, "/auth/cli/token"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return control.EmitErrorWith("token_exchange_failed", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return control.EmitErrorWith("token_exchange_failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return control.EmitError("token_exchange_failed", resp.Status)
	}
	return persistTokenResponse(hubURL, resp.Body, adminScope)
}

// persistTokenResponse writes the freshly minted credential and retires the
// one it replaces.
//
// The ORDER is deliberate: save first, revoke second. A crash between the
// two leaves the user logged in with one abandoned row on the hub, which the
// device list shows and the expiry sweep eventually removes. The reverse
// order would leave them logged OUT with a credential file the hub has
// refused already -- the failure the user cannot fix without a browser.
//
// The revoke is best-effort for the same reason: a hub that is briefly
// unreachable must not turn a successful login into a failed one.
func persistTokenResponse(hubURL string, body io.Reader, requestedAdminScope bool) error {
	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		RefreshExpiresIn int    `json:"refresh_expires_in"`
		TokenID          string `json:"token_id"`
		UserID           string `json:"user_id"`
		Username         string `json:"username"`
		AdminScope       bool   `json:"admin_scope"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return control.EmitErrorWith("token_exchange_failed", err)
	}
	// Read the outgoing credential BEFORE the new one overwrites the file.
	previous, previousErr := control.LoadCredentials(hubURL)

	now := time.Now()
	creds := control.CredentialFile{
		HubURL:       hubURL,
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(out.ExpiresIn) * time.Second),
		UserID:       out.UserID,
		Username:     out.Username,
		TokenID:      out.TokenID,
		AdminScope:   out.AdminScope,
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
		if err := revokeBearer(hubURL, previous.AccessToken); err != nil {
			// Best-effort by design: the new credential is already on disk
			// and the login succeeded. But it must not be SILENT, or the
			// retirement reads as done on exactly the runs where it did not
			// happen -- and the old refresh secret then stays live for the
			// rest of its window.
			retirementWarning = "the previous credential could not be revoked (" + err.Error() +
				"); revoke it under Preferences, Account, Command-line credentials"
		}
	}

	result := map[string]any{
		"hub_url":     hubURL,
		"username":    out.Username,
		"user_id":     out.UserID,
		"admin_scope": out.AdminScope,
	}
	// EVERY warning, joined, not the first one that matches. A `switch`
	// reported one and discarded the rest, so a login that was refused the
	// admin scope AND could not retire its predecessor said nothing about
	// the credential still live on the hub -- the exact silence the
	// retirement warning exists to break.
	var warnings []string
	if requestedAdminScope && !out.AdminScope {
		// Say so HERE rather than letting the first admin verb fail with a
		// permission error that specifies nothing the user did.
		warnings = append(warnings,
			"hub administration was requested but not granted; authorize it in the browser and run `leapmux control auth login --admin` again")
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
	// user would have retried with.
	warning := ""
	creds, err := control.LoadCredentials(hub)
	if err == nil {
		if revokeErr := revokeBearer(hub, creds.AccessToken); revokeErr != nil {
			warning = "the credential could not be revoked on the hub (" + revokeErr.Error() +
				"); it is gone from this machine, so revoke it under Preferences, Account, Command-line credentials"
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
			"hub_url":     c.HubURL,
			"username":    c.Username,
			"user_id":     c.UserID,
			"expires":     c.ExpiresAt,
			"admin_scope": c.AdminScope,
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
				"id":          tok.GetId(),
				"client_type": tok.GetClientType(),
				"client_name": tok.GetClientName(),
				"admin_scope": tok.GetAdminScope(),
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
	// credentialPageSize is the hub's maximum page. Asking for it makes the
	// listing one round trip for any real account; an omitted limit
	// resolved to the hub's default of fifty, so the loop below took ten
	// times the requests and covered a tenth of what its own bound claimed.
	credentialPageSize = 500
	// maxCredentialPages limits the listing loop. At credentialPageSize
	// that covers a quarter of a million credentials, so it is a runaway
	// guard against a cursor that never advances, not a limit anybody
	// reaches.
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
	return control.EmitData(map[string]any{
		"hub_url":  creds.HubURL,
		"username": creds.Username,
		"user_id":  creds.UserID,
		"expires":  creds.ExpiresAt,
		"expired":  time.Now().After(creds.ExpiresAt),
		// The access token above renews itself; this is the deadline that
		// actually sends the user back to a browser.
		"refresh_expires": creds.RefreshExpiresAt,
		"admin_scope":     creds.AdminScope,
		"token_id":        creds.TokenID,
	})
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
// leg in `control` need the same answer.
//
// A socket client that fails to build falls through to the remote client,
// which SelectClient does for every transport factory in the tree: the
// request then fails with a scheme error that gives the URL, which is
// better than a panic here.
func cliHTTPClient(hubURL string) (*http.Client, string) {
	return locallisten.RESTClient(hubURL, cliRESTTimeout)
}

func revokeBearer(hubURL, bearer string) error {
	if bearer == "" {
		return nil
	}
	// Resolve the transport FIRST and build the request against its base
	// URL, the way the sibling token calls do. Building against hubURL and
	// then patching req.URL discarded the parse error, so a URL that failed
	// to parse left req.URL nil and answered with a generic
	// `http: nil Request.URL` instead of naming the address.
	hc, baseURL := cliHTTPClient(hubURL)
	form := url.Values{"token": {bearer}}
	req, err := http.NewRequest(http.MethodPost,
		locallisten.JoinPath(baseURL, "/auth/cli/revoke"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	// The status is the answer. Without reading it this reported success
	// for a hub that refused, so `auth logout` printed a clean result and
	// the credential stayed live, and the re-login retirement below
	// silently kept the very row its comment says it exists to remove.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("revoke failed: %s", resp.Status)
	}
	return nil
}

func defaultDeviceName() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	if user == "" {
		return host
	}
	return user + "@" + host
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

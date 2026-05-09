// Package cmd implements the leaf commands of `leapmux remote ...`.
// Each entry is a func compatible with the admin dispatcher's signature
// (adminCmdCtx-like) so the same flag-parsing scaffolding from the
// admin tree is reused.
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

	"golang.org/x/oauth2"

	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
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
// own minimal shape so it doesn't pull in cmd/leapmux's adminCmdCtx
// (avoids an import cycle).
type Ctx interface {
	Path() string
	Description() string
}

// genericCtx adapts adminCmdCtx (any struct with Path / Description
// fields).
type adminCmdLike struct {
	PathStr        string
	DescriptionStr string
}

func (c adminCmdLike) Path() string        { return c.PathStr }
func (c adminCmdLike) Description() string { return c.DescriptionStr }

// asCtx accepts the dispatcher's adminCmdCtx (declared in main package)
// via reflection-free duck typing: callers convert with adminCmdLike.
func asCtx(v any) Ctx {
	if c, ok := v.(Ctx); ok {
		return c
	}
	return adminCmdLike{PathStr: "remote", DescriptionStr: ""}
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
	resolve.BindEntityFlags(out.FS, &out.In, resolve.FlagOptions{HideOrg: true, HideUser: true})
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
	return remote.EmitError("invalid_request", msg)
}

// requireClient builds a remote.Client from --hub or the LEAPMUX_REMOTE_*
// env vars. Returns a clear error envelope on failure.
func requireClient(hubFlag string) (*remote.Client, error) {
	c, err := remote.NewClientFromEnv(hubFlag)
	if err != nil {
		return nil, remote.EmitErrorWith("not_logged_in", err)
	}
	return c, nil
}

// --- auth login -------------------------------------------------------

// RunAuthLogin implements `leapmux remote auth login`. Tries the
// local-redirect (PKCE) flow by default; falls back to (or honors
// --device-code) when the local listener can't be reached from a
// browser.
func RunAuthLogin(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, deviceName string
	var deviceCode bool
	fs := flagSet(cmd, &hub)
	fs.StringVar(&deviceName, "device-name", defaultDeviceName(), "label shown on the consent page")
	fs.BoolVar(&deviceCode, "device-code", false, "force RFC 8628 device-code flow (headless / SSH / container)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if hub == "" {
		return remote.EmitError("invalid_request", "--hub is required")
	}
	ctx := context.Background()

	if deviceCode {
		return runDeviceCodeLogin(ctx, hub, deviceName)
	}
	return runLocalRedirectLogin(ctx, hub, deviceName)
}

func runLocalRedirectLogin(ctx context.Context, hubURL, deviceName string) error {
	verifier := oauth2.GenerateVerifier()
	challenge := pkce.S256(verifier)
	state := id.Generate()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return remote.EmitErrorWith("listen_failed", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	startURL := locallisten.JoinPath(hubURL, "/auth/cli/start?"+url.Values{
		"redirect_uri":   {redirectURI},
		"state":          {state},
		"code_challenge": {challenge},
		"device_name":    {deviceName},
	}.Encode())
	_, _ = fmt.Fprintln(remote.Out, "Open this URL in your browser to authorize the CLI:")
	_, _ = fmt.Fprintln(remote.Out, " ", startURL)
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
		return remote.EmitErrorWith("callback_error", err)
	case code = <-codeCh:
	case <-time.After(10 * time.Minute):
		return remote.EmitError("timeout", "timed out waiting for browser authorization")
	}

	return exchangeAuthorizationCode(ctx, hubURL, code, verifier, deviceName)
}

func runDeviceCodeLogin(ctx context.Context, hubURL, deviceName string) error {
	form := url.Values{"device_name": {deviceName}}
	resp, err := http.PostForm(locallisten.JoinPath(hubURL, "/auth/cli/device-authorization"), form)
	if err != nil {
		return remote.EmitErrorWith("device_authorization_failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return remote.EmitError("device_authorization_failed", resp.Status)
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
		return remote.EmitErrorWith("device_authorization_failed", err)
	}
	_, _ = fmt.Fprintln(remote.Out, "To authorize this CLI, on any device with a browser:")
	_, _ = fmt.Fprintln(remote.Out, "  1. Visit", auth.VerificationURI)
	_, _ = fmt.Fprintln(remote.Out, "  2. Enter the code:", auth.UserCode)
	if auth.VerificationURIComplete != "" {
		_, _ = fmt.Fprintln(remote.Out, "Or open:", auth.VerificationURIComplete)
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
		err := tryExchangeDeviceCode(ctx, hubURL, auth.DeviceCode, deviceName)
		if errors.Is(err, errAuthorizationPending) {
			continue
		}
		if errors.Is(err, errSlowDown) {
			interval += 5 * time.Second
			continue
		}
		if err != nil {
			return remote.EmitErrorWith("device_grant_failed", err)
		}
		return nil
	}
	return remote.EmitError("expired_token", "device code expired")
}

var (
	errAuthorizationPending = errors.New("authorization_pending")
	errSlowDown             = errors.New("slow_down")
)

// tryExchangeDeviceCode performs one /auth/cli/token poll. nil on
// success (creds saved); errAuthorizationPending / errSlowDown when
// the user hasn't completed the flow yet.
func tryExchangeDeviceCode(ctx context.Context, hubURL, deviceCode, deviceName string) error {
	form := url.Values{
		"grant_type":  {grantTypeDeviceCode},
		"device_code": {deviceCode},
		"device_name": {deviceName},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(hubURL, "/auth/cli/token"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return persistTokenResponse(hubURL, resp.Body)
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

func exchangeAuthorizationCode(ctx context.Context, hubURL, code, verifier, deviceName string) error {
	form := url.Values{
		"grant_type":    {grantTypeAuthorizationCode},
		"code":          {code},
		"code_verifier": {verifier},
		"device_name":   {deviceName},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(hubURL, "/auth/cli/token"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return remote.EmitErrorWith("token_exchange_failed", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return remote.EmitErrorWith("token_exchange_failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return remote.EmitError("token_exchange_failed", resp.Status)
	}
	return persistTokenResponse(hubURL, resp.Body)
}

func persistTokenResponse(hubURL string, body io.Reader) error {
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenID      string `json:"token_id"`
		UserID       string `json:"user_id"`
		Username     string `json:"username"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return remote.EmitErrorWith("token_exchange_failed", err)
	}
	creds := remote.CredentialFile{
		HubURL:       hubURL,
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
		UserID:       out.UserID,
		Username:     out.Username,
	}
	if err := remote.SaveCredentials(hubURL, creds); err != nil {
		return remote.EmitErrorWith("save_credentials_failed", err)
	}
	return remote.EmitData(map[string]any{
		"hub_url":  hubURL,
		"username": out.Username,
		"user_id":  out.UserID,
	})
}

// --- auth logout / list / status -------------------------------------

func RunAuthLogout(rawCtx any, args []string) error {
	hub, err := parseHubOnly(rawCtx, args, nil)
	if err != nil {
		return err
	}
	creds, err := remote.LoadCredentials(hub)
	if err == nil {
		_ = revokeBearer(hub, creds.AccessToken)
	}
	if err := remote.DeleteCredentials(hub); err != nil {
		return remote.EmitErrorWith("delete_failed", err)
	}
	return remote.EmitData(map[string]string{"hub_url": hub})
}

func RunAuthList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	fs := flag.NewFlagSet("leapmux "+cmd.Path(), flag.ContinueOnError)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	files, err := remote.ListCredentialFiles()
	if err != nil {
		return remote.EmitErrorWith("list_failed", err)
	}
	out := make([]map[string]any, 0, len(files))
	for _, c := range files {
		out = append(out, map[string]any{
			"hub_url":  c.HubURL,
			"username": c.Username,
			"user_id":  c.UserID,
			"expires":  c.ExpiresAt,
		})
	}
	return remote.EmitData(out)
}

func RunAuthStatus(rawCtx any, args []string) error {
	hub, err := parseHubOnly(rawCtx, args, nil)
	if err != nil {
		return err
	}
	creds, err := remote.LoadCredentials(hub)
	if err != nil {
		return remote.EmitErrorWith("not_logged_in", err)
	}
	return remote.EmitData(map[string]any{
		"hub_url":  creds.HubURL,
		"username": creds.Username,
		"user_id":  creds.UserID,
		"expires":  creds.ExpiresAt,
		"expired":  time.Now().After(creds.ExpiresAt),
	})
}

// --- helpers ----------------------------------------------------------

func revokeBearer(hubURL, bearer string) error {
	if bearer == "" {
		return nil
	}
	form := url.Values{"token": {bearer}}
	req, err := http.NewRequest(http.MethodPost,
		locallisten.JoinPath(hubURL, "/auth/cli/revoke"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
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

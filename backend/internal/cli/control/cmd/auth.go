package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/loopbackauth"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newAuthCommand() *cobra.Command {
	return cli.Group("auth", "Sign this CLI in to a hub, and manage its credentials",
		cli.NewLeaf(cli.Leaf{
			Use:              "login",
			Mutates:          true,
			QuietUnsupported: true,
			Short:            "Sign this CLI in to a hub through a browser",
			Next: []string{
				"leapmux whoami",
				"leapmux tree",
				"leapmux doctor",
			},
			Examples: []cli.Example{
				{Command: "leapmux auth login --hub https://hub.example.com"},
				{Comment: "Sign in from a machine without a browser.", Command: "leapmux auth login --hub https://hub.example.com --device-code"},
			},
			Flags: func(fs *pflag.FlagSet) {
				// The label is recorded on the grant and surfaces in the
				// account's own records (Connected apps lists each credential
				// by it), but the consent pages deliberately show only the
				// app's verified identity: the label is requester-chosen, so
				// rendering it on a decision screen would let a phisher
				// manufacture "your own laptop" reassurance out of nothing.
				// The default is computed when the login runs, never declared:
				// a declared default prints in the help, in `leapmux commands`
				// and in the generated docs, with the name of this machine.
				fs.String("device-name", "", "a label for this credential; the account's list of connected apps shows it (default: <user>@<host> of this machine)")
				fs.Bool("device-code", false, "sign in with a code that you enter on any device with a browser (for SSH and containers)")
				// The grant. EMPTY asks for everything except the admin
				// scopes, which is what an ordinary login has always meant --
				// so the credential file this leaves on disk for months can
				// do everything its owner can do EXCEPT administer the hub,
				// and an administering credential exists only when somebody
				// asked for one.
				fs.String("scope", "", `permissions to ask for, separated by spaces or commas, such as "file:read git:read" (default: everything except admin:*)`)
			},
			Run: runAuthLogin,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "logout",
			Short:            "Revoke this CLI's credential for a hub and delete it",
			Mutates:          true,
			Destructive:      true,
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux auth logout --hub https://hub.example.com"},
			},
			Run: runAuthLogout,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List the hubs that this machine holds a credential for",
			Examples: []cli.Example{
				{Command: "leapmux auth list"},
			},
			Run: runAuthList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "use <hub>",
			Mutates:          true,
			QuietUnsupported: true,
			Short:            "Choose the hub that a command uses without --hub",
			Examples: []cli.Example{
				{Command: "leapmux auth use https://hub.example.com"},
			},
			Complete: completeStoredHubs,
			Run:      runAuthUse,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "status",
			QuietUnsupported: true,
			Short:            "Show the user, the expiry and the permissions of the hub's credential",
			Examples: []cli.Example{
				{Command: "leapmux auth status"},
			},
			Run: runAuthStatus,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "credentials",
			Short: "List every credential that can reach your account on the hub",
			Examples: []cli.Example{
				{Command: "leapmux auth credentials"},
			},
			Run: runAuthCredentials,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "change-password",
			Short:   "Change your account password on the hub (prompts securely when --password is omitted)",
			Mutates: true,
			Examples: []cli.Example{
				{Comment: "Prompt for the new password interactively.", Command: "leapmux auth change-password"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("password", "", "new password (prompted for when omitted, so it stays out of shell history)")
			},
			Run: runAuthChangePassword,
		}),
	)
}

// openDirectHubSession builds a direct Hub client and a session for commands
// that never route through a tab socket. It omits rpcDeadline because callers
// run interactive password prompts and 10-minute browser step-up flows that
// outlive the 30-second RPC deadline; each unary HTTP call still carries
// hubtransport.DefaultUnaryTimeout.
func openDirectHubSession(inv *cli.Invocation) (*session, *control.HubClient, error) {
	hub, source, err := hubURLWithSource(inv.Command)
	if err != nil {
		return nil, nil, err
	}
	c, err := control.NewClientForHub(hub)
	if err != nil {
		return nil, nil, err
	}
	c.Source = source
	configurePeer(c, inv)
	noticeDiscoveredLocalHub(inv, c)
	ctx, cancel := context.WithCancel(inv.Context)
	return &session{
		inv:     inv,
		c:       c,
		ctx:     ctx,
		cancel:  cancel,
		located: newLocatedTabCache(),
	}, c, nil
}

// requirePasswordFromInv returns the password from flagName when set, or
// prompts interactively on inv.Stdin when empty.
func requirePasswordFromInv(inv *cli.Invocation, flagName, prompt string) (string, error) {
	pw := stringFlag(inv, flagName)
	if pw != "" {
		return pw, nil
	}
	if inv.Context.Err() != nil {
		return "", context.Cause(inv.Context)
	}
	in := os.Stdin
	if inv.Stdin != nil {
		f, ok := inv.Stdin.(*os.File)
		if !ok {
			return "", cli.Usagef("--%s is required (stdin is not a terminal)", flagName)
		}
		in = f
	}
	return control.RequirePassword(inv.Context, pw, prompt, in, errOutOf(inv))
}

func runAuthChangePassword(inv *cli.Invocation) (any, error) {
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	pw, err := requirePasswordFromInv(inv, "password", "New password: ")
	if err != nil {
		return nil, err
	}
	resp, err := c.UserService().ChangePassword(s.ctx, &leapmuxv1.ChangePasswordRequest{NewPassword: pw})
	if err != nil {
		return nil, err
	}
	user := resp.GetUser()
	return struct {
		ID       string `json:"id,omitempty"`
		Username string `json:"username,omitempty"`
		Changed  bool   `json:"changed"`
	}{
		ID:       user.GetId(),
		Username: user.GetUsername(),
		Changed:  true,
	}, nil
}

// --- auth logout / list / status -------------------------------------

// authResult is the result of `auth logout` and `auth use`.
type authResult struct {
	HubURL  string `json:"hub_url"`
	Warning string `json:"warning,omitempty"`
}

func runAuthLogout(inv *cli.Invocation) (any, error) {
	hub, err := hubURLFor(inv.Command)
	if err != nil {
		return nil, err
	}
	// The revoke's answer is REPORTED, not discarded. revokeBearer reads the
	// status precisely so a refusal stops reading as success, and the local
	// file goes either way -- logout must stay locally idempotent -- so
	// swallowing the error here left the row live with its ninety-day
	// refresh secret, printed a clean result, and took away the bearer the
	// user needed to retry.
	out := authResult{HubURL: hub}
	creds, loadErr := control.LoadCredentials(hub)
	switch {
	case loadErr == nil:
		if revokeErr := revokeBearer(inv.Context, nil, hub, creds.AccessToken, creds.ClientIDOrBuiltIn()); revokeErr != nil {
			out.Warning = "this CLI could not revoke the credential on the hub (" + revokeErr.Error() +
				"); it is gone from this machine, so " + disconnectInPreferences
		}
	case errors.Is(loadErr, control.ErrNotLoggedIn):
		// Nothing to revoke on the hub.
	default:
		out.Warning = fmt.Sprintf(
			"the stored credential file could not be read (%v), so this CLI could not revoke its grant on the hub; it is gone from this machine, so %s",
			loadErr, disconnectInPreferences,
		)
	}
	if err := control.DeleteCredentials(hub); err != nil {
		return nil, err
	}
	if err := control.ClearDefaultHubIf(hub); err != nil {
		return nil, err
	}
	return out, nil
}

// credentialRow is one hub that this machine holds a credential for.
type credentialRow struct {
	HubURL   string `json:"hub_url" table:"HUB"`
	Username string `json:"username" table:"USER"`
	UserID   string `json:"user_id"`
	Scope    string `json:"scope,omitempty"`
	Expires  string `json:"expires,omitempty" table:"EXPIRES,dim"`
	// Default marks the hub that a command uses without --hub.
	Default bool `json:"default" table:"DEFAULT"`
}

type credentialList struct {
	Credentials []credentialRow `json:"credentials"`
}

func (l credentialList) QuietLines() []string {
	out := make([]string, 0, len(l.Credentials))
	for _, c := range l.Credentials {
		out = append(out, c.HubURL)
	}
	return out
}

func runAuthList(*cli.Invocation) (any, error) {
	files, err := control.ListCredentialFiles()
	if err != nil {
		return nil, err
	}
	defaultHub, _ := control.DefaultHub()
	rows := make([]credentialRow, 0, len(files))
	for _, c := range files {
		rows = append(rows, credentialRow{
			HubURL:   c.HubURL,
			Username: c.Username,
			UserID:   c.UserID,
			Scope:    c.Scope,
			Expires:  formatWallTime(c.ExpiresAt),
			Default:  control.SameHubURL(c.HubURL, defaultHub),
		})
	}
	return credentialList{Credentials: rows}, nil
}

// validateDefaultHub normalizes raw and verifies that non-local remote hubs
// have stored credentials on this machine before saving them as the default.
func validateDefaultHub(raw string) (string, error) {
	canonical := control.CanonicalHubURL(raw)
	if canonical == "" {
		return "", cli.Usagef("hub URL is empty")
	}
	if locallisten.IsLocal(canonical) {
		return canonical, nil
	}
	if _, err := control.HubHost(canonical); err != nil {
		return "", cli.Usagef("invalid hub URL %q: %v", raw, err)
	}
	creds, err := control.LoadCredentials(canonical)
	if err != nil {
		if errors.Is(err, control.ErrNotLoggedIn) {
			return "", cli.Wrap(cli.CodeNotLoggedIn, err, "").
				WithHint(`run "%s" to sign in first`, authLoginCommand(canonical))
		}
		return "", err
	}
	if creds.HubURL != "" {
		return control.CanonicalHubURL(creds.HubURL), nil
	}
	return canonical, nil
}

// runAuthUse records the hub that a command uses when nothing else selects
// one. It refuses a remote hub that this machine holds no credential for, so a
// typo cannot become the default.
func runAuthUse(inv *cli.Invocation) (any, error) {
	canonicalHub, err := validateDefaultHub(inv.Arg("hub"))
	if err != nil {
		return nil, err
	}
	if err := control.SetDefaultHub(canonicalHub); err != nil {
		return nil, err
	}
	return authResult{HubURL: canonicalHub}, nil
}

// runAuthCredentials lists the ACCOUNT's command-line credentials, as the
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
func runAuthCredentials(inv *cli.Invocation) (any, error) {
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	// Every page, not the first: an omitted limit resolves to a default on
	// the hub, and a credential the listing never printed is one the operator
	// cannot see to revoke. readAllPages keys the rows by id, so a keyset
	// boundary never prints the same credential as two live ones to somebody
	// auditing what can reach their account.
	tokens, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]*leapmuxv1.MyAPIToken, *leapmuxv1.PageResponse, error) {
		resp, err := c.UserService().ListMyAPITokens(s.ctx, &leapmuxv1.ListMyAPITokensRequest{Page: page})
		return resp.GetTokens(), resp.GetPage(), err
	}, (*leapmuxv1.MyAPIToken).GetId)
	if err != nil {
		return nil, err
	}
	rows := make([]accountCredential, 0, len(tokens))
	for _, tok := range tokens {
		scopes := tok.GetGrantedScopes()
		if scopes == nil {
			scopes = []string{}
		}
		rows = append(rows, accountCredential{
			ID:               tok.GetId(),
			ClientID:         tok.GetClientId(),
			ClientName:       tok.GetClientName(),
			ClientVerified:   tok.GetClientVerified(),
			InstallationName: tok.GetInstallationName(),
			GrantedScopes:    scopes,
			Current:          tok.GetCurrent(),
			CreatedAt:        formatTime(tok.GetCreatedAt()),
			LastUsedAt:       formatTime(tok.GetLastUsedAt()),
			RefreshExpires:   formatTime(tok.GetRefreshExpiresAt()),
			Expires:          formatTime(tok.GetExpiresAt()),
		})
	}
	return struct {
		Credentials []accountCredential `json:"credentials"`
	}{rows}, nil
}

// accountCredential is one credential that can reach the account.
type accountCredential struct {
	ID               string   `json:"id" table:"ID,short"`
	ClientID         string   `json:"client_id"`
	ClientName       string   `json:"client_name" table:"APP"`
	ClientVerified   bool     `json:"client_verified"`
	InstallationName string   `json:"installation_name" table:"DEVICE"`
	GrantedScopes    []string `json:"granted_scopes"`
	// Current is true for the credential THIS command uses, so an operator
	// does not revoke the device they work from.
	Current    bool   `json:"current" table:"CURRENT"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty" table:"LAST USED,dim"`
	// RefreshExpires and Expires: the fixed-lifetime kind carries no
	// refresh deadline, so its whole life is reported in Expires instead.
	// Exactly one of the two is ever set, so a credential with neither is
	// one that never expires.
	RefreshExpires string `json:"refresh_expires,omitempty"`
	Expires        string `json:"expires,omitempty"`
}

// authStatus is the result of `auth status`.
type authStatus struct {
	HubURL   string `json:"hub_url"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	IsAdmin  *bool  `json:"is_admin,omitempty"`
	Scope    string `json:"scope,omitempty"`
	TokenID  string `json:"token_id,omitempty"`
	// Expired reports the access token, which renews itself.
	Expired bool   `json:"expired"`
	Expires string `json:"expires,omitempty"`
	// RefreshExpires is the deadline that actually sends the user back to
	// a browser. It is empty on a credential written before the hub
	// reported it.
	RefreshExpires string `json:"refresh_expires,omitempty"`
	// HubChecked is false when the hub could not confirm the credential;
	// Warning then states what was not verified.
	HubChecked bool   `json:"hub_checked"`
	Warning    string `json:"warning,omitempty"`
}

// runAuthStatus answers "can this machine still reach my account" -- a
// question about the HUB's truth, not the credential file's. The file's own
// fields print either way, and one authenticated GetCurrentUser confirms
// them: confirmed answers carry the hub's username and admin flag, a refusal
// is a signed-out state (the rotation the interceptor attempts has already
// deleted the file), and an unreachable hub keeps the local answer useful
// with a warning that states exactly what was not verified.
func runAuthStatus(inv *cli.Invocation) (any, error) {
	hub, source, err := hubURLWithSource(inv.Command)
	if err != nil {
		return nil, err
	}
	creds, loadErr := control.LoadCredentials(hub)
	var (
		c      *control.HubClient
		status authStatus
	)
	if loadErr != nil {
		if !errors.Is(loadErr, control.ErrNotLoggedIn) || !locallisten.IsLocal(hub) {
			return nil, loadErr
		}
		// A local solo or dev Hub authenticates its local IPC socket without a
		// stored credential file, so confirm the session directly with the Hub.
		c, err = control.NewClientOrAnonymous(hub)
		if err != nil {
			return nil, err
		}
		status = authStatus{HubURL: hub}
	} else {
		c, err = control.NewClientForHub(hub)
		if err != nil {
			return nil, err
		}
		status = authStatus{
			HubURL:         creds.HubURL,
			UserID:         creds.UserID,
			Scope:          creds.Scope,
			TokenID:        creds.TokenID,
			Expired:        time.Now().After(creds.ExpiresAt),
			Expires:        formatWallTime(creds.ExpiresAt),
			RefreshExpires: formatWallTime(creds.RefreshExpiresAt),
		}
	}
	c.Source = source
	configurePeer(c, inv)
	noticeDiscoveredLocalHub(inv, c)

	// Confirm what the hub actually does with this session:
	//   - confirmed: use the hub's username and admin flag (the local file is
	//     stale if the account was renamed).
	//   - refused: return not_logged_in with the hub's reason.
	//   - fallback (unreachable or missing account:read): return the local
	//     fields with a warning stating what could not be verified.
	check, err := checkHubSession(inv.Context, c)
	if err != nil {
		return nil, err
	}
	if check.Fallback {
		if creds != nil {
			status.Username = creds.Username
			status.Warning = check.Warning
		} else if check.Unreachable {
			status.Warning = localHubUnreachableWarning
		} else {
			status.Warning = check.Warning
		}
		return status, nil
	}
	user := check.Report.GetUser()
	isAdmin := user.GetIsAdmin()
	status.HubChecked = true
	if id := user.GetId(); id != "" {
		status.UserID = id
	}
	status.Username = user.GetUsername()
	status.IsAdmin = &isAdmin
	return status, nil
}

// disconnectInPreferences identifies the one place a user revokes a credential
// by hand when this CLI could not: the account's Connected-apps list. It is a
// constant because three CLI warnings state it and a Preferences
// reorganisation must move one string, not three. The path separator is ›,
// the same character the Preferences search breadcrumb spells paths with, so
// one spelling of "drill down through these" reaches the user everywhere.
const disconnectInPreferences = "disconnect it under Preferences › Apps › Connected apps"

// authLoginCommand returns the `leapmux auth login` command line for hubURL,
// including --device-code for local IPC socket URLs that have no browser origin.
func authLoginCommand(hubURL string) string {
	if locallisten.IsLocal(hubURL) {
		return fmt.Sprintf("leapmux auth login --hub %s --device-code", hubURL)
	}
	return fmt.Sprintf("leapmux auth login --hub %s", hubURL)
}

// hubRefusedCredential reports an error that is the hub ANSWERING: this
// credential opens nothing, and no retry will change that. The sentinel
// cases are the refresh path's (the file is already deleted when they
// surface); the code case is the hub's own Unauthenticated on a bearer it
// revoked or no longer holds.
func hubRefusedCredential(err error) bool {
	return errors.Is(err, control.ErrNotLoggedIn) ||
		errors.Is(err, control.ErrCredentialRejected) ||
		connect.CodeOf(err) == connect.CodeUnauthenticated
}

// hubUnreachable reports an error that says the hub could not be ASKED --
// down, timing out, unresolvable, or answering through a broken reverse
// proxy -- which is a fact about the network rather than about the
// credential, so the caller reports the local answer with a warning instead
// of a failure.
func hubUnreachable(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded,
		// connect-go maps a proxy's 404 to Unimplemented and a 500 to
		// Unknown: a misrouted or crashed proxy is the same "could not be
		// asked" as one that is down, and the 502/503/504 family already
		// arrives as Unavailable.
		connect.CodeUnimplemented, connect.CodeUnknown:
		return true
	default:
		return false
	}
}

// hubCheckTimeout caps the whole confirmation read below, the token rotation
// the interceptor performs beneath it included (doRefresh clamps its own
// budget to this deadline). The local answer plus a warning is the designed
// fallback, so a hung hub must not hold the command for the transport's full
// 60-second default -- or the rotation's 30 -- before that fallback fires.
const hubCheckTimeout = 5 * time.Second

// hubUnconfirmedTail states, on every local-answer fallback, that the fields
// the command printed are the file's, not the hub's. Both fallback warnings
// end with it, so the two wordings cannot drift apart.
const hubUnconfirmedTail = "the fields above are this machine's credential file, not the hub's answer"

// hubUnconfirmedWarning states, on the unreachable fallback, exactly what
// was not verified. One string, because `auth status` and `whoami` answer
// the same question and must not word it differently.
const hubUnconfirmedWarning = "the hub could not be reached to confirm this; " + hubUnconfirmedTail

// localHubUnreachableWarning states why an anonymous local-IPC session check
// could not confirm the session.
const localHubUnreachableWarning = "the local hub could not be reached to confirm this session"

// hubSessionCheck holds the non-error result of checkHubSession.
type hubSessionCheck struct {
	Report      *leapmuxv1.SessionReport
	Fallback    bool
	Warning     string
	Unreachable bool
}

// checkHubSession performs the one authenticated GetCurrentUser read that
// settles whether the hub still honours this machine's credential. `auth
// status`, `whoami`, and `doctor` ask the same question through it so they
// classify refusals and fallbacks identically.
//
// A PermissionDenied answer is a local fallback, not a refusal: the credential
// is valid for its granted scopes and merely lacks account:read.
func checkHubSession(ctx context.Context, c *control.HubClient) (hubSessionCheck, error) {
	checkCtx, cancel := context.WithTimeout(ctx, hubCheckTimeout)
	defer cancel()
	me, callErr := c.AuthService().GetCurrentUser(checkCtx, &leapmuxv1.GetCurrentUserRequest{})
	switch {
	case callErr == nil:
		return hubSessionCheck{Report: me.GetSessionReport()}, nil
	case hubRefusedCredential(callErr):
		return hubSessionCheck{}, cli.Wrap(cli.CodeNotLoggedIn, stripHubHintError(callErr), "the hub refused this credential").
			WithHint(`run "%s" to sign in again`, authLoginCommand(c.HubURL))
	case connect.CodeOf(callErr) == connect.CodePermissionDenied:
		return hubSessionCheck{
			Fallback: true,
			Warning:  "this credential lacks the account:read permission, so the hub could not confirm it; " + hubUnconfirmedTail,
		}, nil
	case hubUnreachable(callErr):
		return hubSessionCheck{
			Fallback:    true,
			Unreachable: true,
			Warning:     hubUnconfirmedWarning,
		}, nil
	default:
		return hubSessionCheck{}, callErr
	}
}

// --- helpers ----------------------------------------------------------

// revokeBearer ends one credential. clientID is the app the credential was
// issued to -- the hub binds the revocation to that app (RFC 7009 section
// 2.1), so a credential minted to another registration must present its own
// id and not the CLI's. When ep is nil, revokeBearer creates and closes a
// scoped hubtransport.Endpoint for hubURL.
func revokeBearer(ctx context.Context, ep *hubtransport.Endpoint, hubURL, bearer, clientID string) error {
	if bearer == "" {
		return nil
	}
	if ep == nil {
		var err error
		ep, err = hubtransport.New(hubURL)
		if err != nil {
			return err
		}
		defer ep.CloseIdleConnections()
	}
	hc := ep.UnaryClient(hubtransport.DefaultUnaryTimeout)
	baseURL := ep.BaseURL()
	// The CLI identifies itself so the revocation stage can bind the credential to
	// the app it was issued to: this is a public client, and RFC 7009 section
	// 2.1 has a public client identify itself with its client_id rather than
	// a secret it does not hold.
	form := url.Values{
		"token":     {bearer},
		"client_id": {clientID},
	}
	resp, err := loopbackauth.PostForm(ctx, hc,
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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

func runAuthChangePassword(inv *cli.Invocation) (any, error) {
	pw, err := control.RequirePassword(inv.Context, stringFlag(inv, "password"), "New password: ")
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	hub, err := hubURLFor(inv.Command)
	if err != nil {
		return nil, err
	}
	c, err := control.NewClient(hub)
	if err != nil {
		return nil, cli.Wrap(cli.CodeNotLoggedIn, err, "")
	}
	ctx, cancel := rpcDeadline(inv.Context)
	defer cancel()
	resp, err := c.UserService().ChangePassword(ctx, &leapmuxv1.ChangePasswordRequest{NewPassword: pw})
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
	creds, err := control.LoadCredentials(hub)
	if err == nil {
		if revokeErr := revokeBearer(hub, creds.AccessToken, creds.ClientIDOrBuiltIn()); revokeErr != nil {
			out.Warning = "this CLI could not revoke the credential on the hub (" + revokeErr.Error() +
				"); it is gone from this machine, so " + disconnectInPreferences
		}
	}
	if err := control.DeleteCredentials(hub); err != nil {
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
	Expires  string `json:"expires,omitempty" table:"EXPIRES"`
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
			Default:  c.HubURL == defaultHub,
		})
	}
	return credentialList{Credentials: rows}, nil
}

// runAuthUse records the hub that a command uses when nothing else selects
// one. It refuses a hub that this machine holds no credential for, so a
// typo cannot become the default.
func runAuthUse(inv *cli.Invocation) (any, error) {
	hub := inv.Arg("hub")
	if _, err := control.LoadCredentials(hub); err != nil {
		return nil, err
	}
	if err := control.SetDefaultHub(hub); err != nil {
		return nil, err
	}
	return authResult{HubURL: hub}, nil
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
	hub, err := hubURLFor(inv.Command)
	if err != nil {
		return nil, err
	}
	c, err := control.NewClient(hub)
	if err != nil {
		return nil, err
	}
	// Every page, not the first: an omitted limit resolves to a default on
	// the hub, and a credential the listing never printed is one the operator
	// cannot see to revoke. readAllPages keys the rows by id, so a keyset
	// boundary never prints the same credential as two live ones to somebody
	// auditing what can reach their account.
	tokens, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]*leapmuxv1.MyAPIToken, *leapmuxv1.PageResponse, error) {
		resp, err := c.UserService().ListMyAPITokens(inv.Context, &leapmuxv1.ListMyAPITokensRequest{Page: page})
		return resp.GetTokens(), resp.GetPage(), err
	}, (*leapmuxv1.MyAPIToken).GetId)
	if err != nil {
		return nil, err
	}
	rows := make([]accountCredential, 0, len(tokens))
	for _, tok := range tokens {
		rows = append(rows, accountCredential{
			ID:               tok.GetId(),
			ClientID:         tok.GetClientId(),
			ClientName:       tok.GetClientName(),
			ClientVerified:   tok.GetClientVerified(),
			InstallationName: tok.GetInstallationName(),
			GrantedScopes:    tok.GetGrantedScopes(),
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
	LastUsedAt string `json:"last_used_at,omitempty" table:"LAST USED"`
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
	hub, err := hubURLFor(inv.Command)
	if err != nil {
		return nil, err
	}
	creds, err := control.LoadCredentials(hub)
	if err != nil {
		return nil, err
	}
	status := authStatus{
		HubURL:         creds.HubURL,
		UserID:         creds.UserID,
		Scope:          creds.Scope,
		TokenID:        creds.TokenID,
		Expired:        time.Now().After(creds.ExpiresAt),
		Expires:        formatWallTime(creds.ExpiresAt),
		RefreshExpires: formatWallTime(creds.RefreshExpiresAt),
	}

	/*
		THE HUB CHECK. The fields above are this machine's credential file,
		which answers what the file says -- and a revoked credential's file
		keeps saying "valid" until its stored expiry. One authenticated read
		settles what the hub would actually do with the bearer:

		  - confirmed: the hub's own username and admin flag join the answer,
		    and the hub's username WINS -- the local copy is stale the moment
		    an account is renamed;
		  - refused: the credential opens nothing. The interceptor's refresh
		    already deleted the file on an invalid_grant, so not_logged_in is
		    the honest code, with the hub's reason attached;
		  - unreachable: the hub could not be asked, so the local file is
		    stated with a warning rather than failing -- status must stay
		    useful offline, and the warning states exactly what was not
		    verified.
	*/
	c, err := control.NewClient(hub)
	if err != nil {
		return nil, err
	}
	user, warning, outcome, err := hubCheck(c)
	switch outcome {
	case hubCheckRefused, hubCheckFailed:
		return nil, err
	case hubCheckLocalFallback:
		status.Username = creds.Username
		status.Warning = warning
	default:
		isAdmin := user.GetIsAdmin()
		status.HubChecked = true
		status.Username = user.GetUsername()
		status.IsAdmin = &isAdmin
	}
	return status, nil
}

// disconnectInPreferences identifies the one place a user revokes a credential
// by hand when this CLI could not: the account's Connected-apps list. It is a
// constant because three CLI warnings state it and a Preferences
// reorganisation must move one string, not three. The path separator is ›,
// the same character the Preferences search breadcrumb spells paths with, so
// one spelling of "drill down through these" reaches the user everywhere.
const disconnectInPreferences = "disconnect it under Preferences › Apps › Connected apps"

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

// hubCheckOutcome classifies what one authenticated GetCurrentUser read said
// about this machine's credential.
type hubCheckOutcome int

const (
	// hubCheckConfirmed: the hub answered with the account. The caller uses
	// the hub's own username and admin flag.
	hubCheckConfirmed hubCheckOutcome = iota
	// hubCheckRefused: the credential opens nothing; the caller reports a
	// signed-out state.
	hubCheckRefused
	// hubCheckLocalFallback: the hub could not be asked, or the credential
	// lacks the account:read permission the read requires. The local answer
	// stands with a warning.
	hubCheckLocalFallback
	// hubCheckFailed: an error this classification does not own.
	hubCheckFailed
)

// hubCheck performs the one authenticated GetCurrentUser read that settles
// whether the hub still honours this machine's credential. `auth status` and
// `whoami` ask the same question through it, so the two commands classify a
// refusal identically and word the fallback warning identically.
//
// A PermissionDenied answer is a LOCAL FALLBACK, not a refusal: the hub
// answered, and the credential is valid for what it can do -- it merely
// lacks the account:read permission GetCurrentUser requires (a credential
// minted with `auth login --scope`). The identity fields it would confirm
// come from the file instead.
func hubCheck(c *control.Client) (user *leapmuxv1.User, warning string, outcome hubCheckOutcome, err error) {
	report, warning, outcome, err := hubCheckSession(c)
	return report.GetUser(), warning, outcome, err
}

func hubCheckSession(c *control.Client) (report *leapmuxv1.SessionReport, warning string, outcome hubCheckOutcome, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), hubCheckTimeout)
	defer cancel()
	me, callErr := c.AuthService().GetCurrentUser(ctx,
		&leapmuxv1.GetCurrentUserRequest{})
	switch {
	case callErr == nil:
		return me.GetSessionReport(), "", hubCheckConfirmed, nil
	case hubRefusedCredential(callErr):
		return nil, "", hubCheckRefused,
			cli.Wrap(cli.CodeNotLoggedIn, callErr, "the hub refused this credential").
				WithHint(`run "leapmux auth login --hub %s" to sign in again`, c.HubURL)
	case connect.CodeOf(callErr) == connect.CodePermissionDenied:
		return nil, "this credential lacks the account:read permission, so the hub could not confirm it; " +
			hubUnconfirmedTail, hubCheckLocalFallback, nil
	case hubUnreachable(callErr):
		return nil, hubUnconfirmedWarning, hubCheckLocalFallback, nil
	default:
		return nil, "", hubCheckFailed, callErr
	}
}

// --- helpers ----------------------------------------------------------

// cliHTTPClient returns the HTTP client the CLI-auth REST calls should use for
// hubURL, and the base URL to build their paths against.
//
// http.DefaultClient cannot dial a socket URL, so without this the device-code
// flow silently cannot log in against a hub reached over its IPC listener; and
// http.DefaultClient has no timeout at all, so a remote hub that accepts a
// connection and never answers used to hang the command for ever. Both rules
// live in hubtransport, which every hub client in the tree shares.
//
// A URL this cannot build a transport for is reported here rather than
// deferred: the error states the URL and the schemes that are accepted, which
// the "unsupported protocol scheme" from a later request does not.
//
// It MEMOIZES the endpoint per URL, because a command reaches it more than
// once: `auth login` against a hub the machine already holds a credential for
// revokes the old bearer and then runs the whole login, and each step asked
// separately. An endpoint owns the h2c verdict and the connection pools, so a
// second one for the same URL probes the hub again and pools separately — the
// per-poll version of the same waste is what the comment in
// tryExchangeDeviceCode already describes one level down. A CLI process holds
// one hub URL, so the map never grows past a handful of entries.
func cliHTTPClient(hubURL string) (*http.Client, string, error) {
	endpoint, err := cliEndpoint(hubURL)
	if err != nil {
		return nil, "", err
	}
	return endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout), endpoint.BaseURL(), nil
}

var (
	cliEndpointMu    sync.Mutex
	cliEndpointCache = map[string]*hubtransport.Endpoint{}
)

// cliEndpoint returns the process's endpoint for hubURL, building it once.
func cliEndpoint(hubURL string) (*hubtransport.Endpoint, error) {
	cliEndpointMu.Lock()
	defer cliEndpointMu.Unlock()
	if endpoint, ok := cliEndpointCache[hubURL]; ok {
		return endpoint, nil
	}
	endpoint, err := hubtransport.New(hubURL)
	if err != nil {
		return nil, err
	}
	cliEndpointCache[hubURL] = endpoint
	return endpoint, nil
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
	hc, baseURL, err := cliHTTPClient(hubURL)
	if err != nil {
		return err
	}
	// The CLI identifies itself so the revocation stage can bind the credential to
	// the app it was issued to: this is a public client, and RFC 7009 section
	// 2.1 has a public client identify itself with its client_id rather than
	// a secret it does not hold.
	form := url.Values{
		"token":     {bearer},
		"client_id": {clientID},
	}
	resp, err := loopbackauth.PostForm(context.Background(), hc,
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

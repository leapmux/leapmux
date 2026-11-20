package cmd

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// `leapmux control admin app ...` -- the app REGISTRATION surface.
//
// "app" is the OUTBOUND direction: a program this hub grants access to. The
// inbound direction -- an identity provider the hub signs users in WITH -- is
// `control admin idp`. The two were one word for years and the confusion was
// the reason to separate them.
//
// The verbs live under `admin` because registering an app is an operator's job
// on a shared hub, but the RPCs themselves are not admin-only: a user
// registering an app for themself uses the same ones through the preferences
// dialog. See control.Client.AppService.

// appScopeFlagUsage lists the whole grantable vocabulary in the flag's help, so
// an operator does not have to find the documentation to learn what is sayable.
var appScopeFlagUsage = "space-separated permissions this app may ask for, from: " +
	strings.Join(authscope.EveryGrantableScope().SortedTokens(), ", ")

func newAdminAppList() *cobra.Command {
	var page adminPageFlags
	var includeRevoked bool
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List app registrations",
		Examples: []cli.Example{
			{Command: "leapmux control admin app list"},
		},
		Page: &page,
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include retired apps")
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AppService().ListApps(a.Context(), &leapmuxv1.ListAppsRequest{
				Page: page.request(), IncludeRevoked: includeRevoked,
			})
			if err != nil {
				return err
			}
			rows := make([]AppRegistrationRow, 0, len(resp.GetApps()))
			for _, app := range resp.GetApps() {
				rows = append(rows, appRegistrationRowOf(app))
			}
			return a.Result(struct {
				Apps                    []AppRegistrationRow `json:"apps"`
				NextCursor              string               `json:"next_cursor"`
				OpenRegistrationEnabled bool                 `json:"open_registration_enabled"`
			}{Apps: rows, NextCursor: resp.GetPage().GetNextCursor(), OpenRegistrationEnabled: resp.GetOpenRegistrationEnabled()})
		},
	})
}

// AppRegistrationRow contains the fields an admin can read repeatedly.
// The Hub returns a client secret once at registration and stores its hash.
type AppRegistrationRow struct {
	ClientID            string   `json:"client_id"`
	ClientName          string   `json:"client_name"`
	ClientURI           string   `json:"client_uri"`
	Visibility          string   `json:"visibility"`
	ClientType          string   `json:"client_type"`
	RedirectURIs        []string `json:"redirect_uris"`
	Scopes              []string `json:"scopes"`
	GrantTypes          []string `json:"grant_types"`
	ElevationAllowed    bool     `json:"elevation_allowed"`
	RegistrationSource  string   `json:"registration_source"`
	Verified            bool     `json:"verified"`
	VerifiedBy          string   `json:"verified_by"`
	HasIcon             bool     `json:"has_icon"`
	LiveCredentialCount int64    `json:"live_credential_count"`
	CreatedAt           string   `json:"created_at,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
	VerifiedAt          string   `json:"verified_at,omitempty"`
	RevokedAt           string   `json:"revoked_at,omitempty"`
}

func appRegistrationRowOf(app *leapmuxv1.App) AppRegistrationRow {
	return AppRegistrationRow{
		ClientID:            app.GetClientId(),
		ClientName:          app.GetClientName(),
		ClientURI:           app.GetClientUri(),
		Visibility:          enumToken(app.GetVisibility()),
		ClientType:          enumToken(app.GetClientType()),
		RedirectURIs:        app.GetRedirectUris(),
		Scopes:              scopeTokensOf(app.GetScopes()),
		GrantTypes:          app.GetGrantTypes(),
		ElevationAllowed:    app.GetElevationAllowed(),
		RegistrationSource:  app.GetRegistrationSource(),
		Verified:            app.GetVerified(),
		VerifiedBy:          app.GetVerifiedByUsername(),
		HasIcon:             app.GetHasIcon(),
		LiveCredentialCount: app.GetLiveCredentialCount(),
		CreatedAt:           formatTime(app.GetCreatedAt()),
		UpdatedAt:           formatTime(app.GetUpdatedAt()),
		VerifiedAt:          formatTime(app.GetVerifiedAt()),
		RevokedAt:           formatTime(app.GetRevokedAt()),
	}
}

// scopeTokensOf renders a wire scope list as its RFC 6749 section 3.3 tokens.
//
// An unreadable value renders as NOTHING rather than as its enum name: the
// listing is what an operator reads to decide, so a set they cannot interpret
// must not appear as though it were one they can.
func scopeTokensOf(wire []leapmuxv1.Scope) []string {
	set, ok := authscope.ScopesFromWire(wire)
	if !ok {
		return []string{}
	}
	return set.SortedTokens()
}

func newAdminAppRegister() *cobra.Command {
	var name, uri, redirect, scope, grants, kind, visibility string
	var elevation bool
	return adminLeaf(adminVerbSpec{
		Use:   "register <name>",
		Short: "Register an app; the hub shows its secret once",
		Examples: []cli.Example{
			{Command: `leapmux control admin app register "Release bot" --scope "workspace:read" --redirect-uri http://127.0.0.1/callback`},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&uri, "uri", "", "the app's home page")
			fs.StringVar(&redirect, "redirect-uri", "",
				"comma-separated addresses the authorization may return to (required for the authorization_code grant)")
			fs.StringVar(&scope, "scope", "", appScopeFlagUsage+" (required)")
			fs.StringVar(&grants, "grant-type", "",
				"comma-separated OAuth grant types; empty takes the RFC 7591 default of authorization_code and refresh_token")
			fs.StringVar(&kind, "type", "public",
				"public (a binary a user holds; PKCE protects it) or confidential (a server that can keep a secret)")
			fs.StringVar(&visibility, "visibility", "hub-wide",
				"hub-wide (every account may authorize it; administrators only) or private (yours alone)")
			fs.BoolVar(&elevation, "allow-elevation", false,
				"let this app run the step-up stage, which is what a sensitive change needs")
		},
		BeforeDial: argInto(&name, "name"),
		Run: func(c *control.Client, a adminArgs) error {
			scopes, err := parseAppScopes(scope)
			if err != nil {
				return err
			}
			clientType, err := parseAppClientType(kind)
			if err != nil {
				return err
			}
			vis, err := parseAppVisibility(visibility)
			if err != nil {
				return err
			}
			resp, err := c.AppService().RegisterApp(a.Context(), &leapmuxv1.RegisterAppRequest{
				ClientName:       name,
				ClientUri:        uri,
				RedirectUris:     splitCommaList(redirect),
				Scopes:           scopes,
				GrantTypes:       splitCommaList(grants),
				Visibility:       vis,
				ClientType:       clientType,
				ElevationAllowed: elevation,
			})
			if err != nil {
				return err
			}
			out := struct {
				AppRegistrationRow
				ClientSecret     string `json:"client_secret,omitempty"`
				ClientSecretNote string `json:"client_secret_note,omitempty"`
			}{AppRegistrationRow: appRegistrationRowOf(resp.GetApp())}
			// The ONLY time the hub emits this, so the envelope carries it and
			// says so. A public client has none at all.
			if secret := resp.GetClientSecret(); secret != "" {
				out.ClientSecret = secret
				out.ClientSecretNote = "Copy this now. The Hub stores only its hash and shows the secret once."
			}
			return a.Result(out)
		},
	})
}

func newAdminAppUpdate() *cobra.Command {
	var clientID, name, uri, redirect, scope, grants string
	return adminLeaf(adminVerbSpec{
		Use:   "update <app>",
		Short: "Change the editable fields of an app",
		Examples: []cli.Example{
			{Command: `leapmux control admin app update lmx_app_x1y2 --name "Release bot 2"`},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&name, "name", "", "new app name")
			fs.StringVar(&uri, "uri", "", "new home page")
			fs.StringVar(&redirect, "redirect-uri", "", "replace the redirect list (comma-separated)")
			fs.StringVar(&scope, "scope", "", "replace the permission ceiling; "+appScopeFlagUsage)
			fs.StringVar(&grants, "grant-type", "", "replace the grant-type list (comma-separated)")
		},
		BeforeDial: argInto(&clientID, "app"),
		Run: func(c *control.Client, a adminArgs) error {
			msg := &leapmuxv1.UpdateAppRequest{ClientId: clientID}
			// An UNSET flag leaves the field alone, so a caller that changes a
			// name does not send back a redirect list it read minutes ago and
			// overwrite a concurrent edit with it. a.Passed reports what the
			// operator actually typed (the scaffolding builds it from
			// flag.Visit), which is the only way to tell an empty value from
			// an absent one -- `--uri ""` CLEARS the field instead of being
			// read as "leave it alone".
			if a.Passed("name") {
				msg.ClientName = &name
			}
			if a.Passed("uri") {
				msg.ClientUri = &uri
			}
			if a.Passed("redirect-uri") {
				msg.ReplaceRedirectUris = true
				msg.RedirectUris = splitCommaList(redirect)
			}
			if a.Passed("scope") {
				scopes, err := parseAppScopes(scope)
				if err != nil {
					return err
				}
				msg.ReplaceScopes = true
				msg.Scopes = scopes
			}
			if a.Passed("grant-type") {
				msg.ReplaceGrantTypes = true
				msg.GrantTypes = splitCommaList(grants)
			}
			resp, err := c.AppService().UpdateApp(a.Context(), msg)
			if err != nil {
				return err
			}
			return a.Result(appRegistrationRowOf(resp.GetApp()))
		},
	})
}

// newAdminAppSetElevation toggles the step-up stage.
//
// It is its own verb because it is the ONE field a built-in registration may
// still change: an operator who does not want `leapmux control admin` to
// elevate must be able to say so.
func newAdminAppSetElevation(allowed bool, use, short string, examples []cli.Example) *cobra.Command {
	var clientID string
	return adminLeaf(adminVerbSpec{
		Use:        use,
		Short:      short,
		Examples:   examples,
		Mutates:    true,
		BeforeDial: argInto(&clientID, "app"),
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AppService().SetAppElevationAllowed(a.Context(),
				&leapmuxv1.SetAppElevationAllowedRequest{ClientId: clientID, Allowed: allowed})
			if err != nil {
				return err
			}
			return a.Result(appRegistrationRowOf(resp.GetApp()))
		},
	})
}

func newAdminAppSetVerified(verified bool, use, short string, examples []cli.Example) *cobra.Command {
	var clientID string
	return adminLeaf(adminVerbSpec{
		Use:        use,
		Short:      short,
		Examples:   examples,
		Mutates:    true,
		BeforeDial: argInto(&clientID, "app"),
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AppService().VerifyApp(a.Context(),
				&leapmuxv1.VerifyAppRequest{ClientId: clientID, Verified: verified})
			if err != nil {
				return err
			}
			return a.Result(appRegistrationRowOf(resp.GetApp()))
		},
	})
}

func newAdminAppRevoke() *cobra.Command {
	var clientID string
	return adminLeaf(adminVerbSpec{
		Use:   "revoke <app>",
		Short: "Retire an app and revoke every credential it holds",
		Examples: []cli.Example{
			{Command: "leapmux control admin app revoke lmx_app_x1y2"},
		},
		Mutates:    true,
		BeforeDial: argInto(&clientID, "app"),
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AppService().RevokeApp(a.Context(),
				&leapmuxv1.RevokeAppRequest{ClientId: clientID})
			if err != nil {
				return err
			}
			return a.Result(struct {
				Revoked                string `json:"revoked"`
				RevokedCredentialCount int64  `json:"revoked_credential_count"`
			}{Revoked: clientID, RevokedCredentialCount: resp.GetRevokedCredentialCount()})
		},
	})
}

func newAdminAppDelete() *cobra.Command {
	var clientID string
	return adminLeaf(adminVerbSpec{
		Use:   "delete <app>",
		Short: "Delete an app that never held a credential",
		Examples: []cli.Example{
			{Command: "leapmux control admin app delete lmx_app_x1y2"},
		},
		Mutates:    true,
		BeforeDial: argInto(&clientID, "app"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AppService().DeleteApp(a.Context(),
				&leapmuxv1.DeleteAppRequest{ClientId: clientID}); err != nil {
				return err
			}
			return a.Result(struct {
				Deleted string `json:"deleted"`
			}{Deleted: clientID})
		},
	})
}

// --- flag parsing ----------------------------------------------------------

// parseAppScopes turns a --scope value into the wire list.
//
// It refuses the WHOLE value on an unknown token rather than dropping it, for
// the reason every scope parser here does: a registration that silently lost
// one permission would fail at the consent screen, in front of a user who
// cannot fix it.
func parseAppScopes(raw string) ([]leapmuxv1.Scope, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, cli.Usagef("--scope is required: an app must specify the permissions it may ask for")
	}
	// splitScopeFlag, the one splitter every --scope flag in this package
	// uses: `file:read,git:read` succeeds here exactly as it does one verb
	// over, rather than failing on the comma authscope.Parse alone would
	// treat as part of a token.
	tokens, err := splitScopeFlag(raw)
	if err != nil {
		return nil, err
	}
	set, err := authscope.Parse(strings.Join(tokens, " "))
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	return authscope.ScopesToWire(set), nil
}

func parseAppClientType(raw string) (leapmuxv1.AppClientType, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "public":
		return leapmuxv1.AppClientType_APP_CLIENT_TYPE_PUBLIC, nil
	case "confidential":
		return leapmuxv1.AppClientType_APP_CLIENT_TYPE_CONFIDENTIAL, nil
	}
	return 0, cli.Usagef("unknown app type %q", raw).WithHint("use public or confidential")
}

func parseAppVisibility(raw string) (leapmuxv1.AppVisibility, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "hub-wide":
		return leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE, nil
	case "private":
		return leapmuxv1.AppVisibility_APP_VISIBILITY_PRIVATE, nil
	}
	return 0, cli.Usagef("unknown visibility %q", raw).WithHint("use hub-wide or private")
}

// splitCommaList splits a comma-separated flag value, dropping empties so a
// trailing comma is not an address.
func splitCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

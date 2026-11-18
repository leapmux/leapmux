package cmd

import (
	"context"
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
			resp, err := c.AppService().ListApps(context.Background(), &leapmuxv1.ListAppsRequest{
				Page: page.request(), IncludeRevoked: includeRevoked,
			})
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(resp.GetApps()))
			for _, app := range resp.GetApps() {
				rows = append(rows, adminAppJSON(app))
			}
			return a.Result(map[string]any{
				"apps":                      rows,
				"next_cursor":               resp.GetPage().GetNextCursor(),
				"open_registration_enabled": resp.GetOpenRegistrationEnabled(),
			})
		},
	})
}

// adminAppJSON renders one registration.
//
// It carries no secret and no hash, because the App message holds neither: the
// client secret crosses once, in the registration response, and the hub keeps
// only its hash.
func adminAppJSON(app *leapmuxv1.App) map[string]any {
	row := map[string]any{
		"client_id":             app.GetClientId(),
		"client_name":           app.GetClientName(),
		"client_uri":            app.GetClientUri(),
		"visibility":            app.GetVisibility().String(),
		"client_type":           app.GetClientType().String(),
		"redirect_uris":         app.GetRedirectUris(),
		"scopes":                scopeTokensOf(app.GetScopes()),
		"grant_types":           app.GetGrantTypes(),
		"elevation_allowed":     app.GetElevationAllowed(),
		"registration_source":   app.GetRegistrationSource(),
		"verified":              app.GetVerifiedAt() != nil,
		"verified_by":           app.GetVerifiedByUsername(),
		"has_icon":              app.GetHasIcon(),
		"live_credential_count": app.GetLiveCredentialCount(),
	}
	putTime(row, "created_at", app.GetCreatedAt())
	putTime(row, "updated_at", app.GetUpdatedAt())
	putTime(row, "verified_at", app.GetVerifiedAt())
	putTime(row, "revoked_at", app.GetRevokedAt())
	return row
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
			resp, err := c.AppService().RegisterApp(context.Background(), &leapmuxv1.RegisterAppRequest{
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
			out := adminAppJSON(resp.GetApp())
			// The ONLY time the hub emits this, so the envelope carries it and
			// says so. A public client has none at all.
			if secret := resp.GetClientSecret(); secret != "" {
				out["client_secret"] = secret
				out["client_secret_note"] = "copy this now; the hub stores only its hash and cannot show it again"
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
			resp, err := c.AppService().UpdateApp(context.Background(), msg)
			if err != nil {
				return err
			}
			return a.Result(adminAppJSON(resp.GetApp()))
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
			resp, err := c.AppService().SetAppElevationAllowed(context.Background(),
				&leapmuxv1.SetAppElevationAllowedRequest{ClientId: clientID, Allowed: allowed})
			if err != nil {
				return err
			}
			return a.Result(adminAppJSON(resp.GetApp()))
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
			resp, err := c.AppService().VerifyApp(context.Background(),
				&leapmuxv1.VerifyAppRequest{ClientId: clientID, Verified: verified})
			if err != nil {
				return err
			}
			return a.Result(adminAppJSON(resp.GetApp()))
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
			resp, err := c.AppService().RevokeApp(context.Background(),
				&leapmuxv1.RevokeAppRequest{ClientId: clientID})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{
				"revoked":                  clientID,
				"revoked_credential_count": resp.GetRevokedCredentialCount(),
			})
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
			if _, err := c.AppService().DeleteApp(context.Background(),
				&leapmuxv1.DeleteAppRequest{ClientId: clientID}); err != nil {
				return err
			}
			return a.Result(map[string]any{"deleted": clientID})
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

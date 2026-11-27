package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// `leapmux admin app ...` -- the app REGISTRATION surface.
//
// "app" is the OUTBOUND direction: a program this hub grants access to. The
// inbound direction -- an identity provider the hub signs users in WITH -- is
// `admin idp`.
var appScopeFlagUsage = "space-separated permissions this app may ask for, from: " +
	strings.Join(authscope.EveryGrantableScope().SortedTokens(), ", ")

func newAdminAppList() *cobra.Command {
	var page adminPageFlags
	var includeRevoked bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List app registrations",
		Examples: []cli.Example{
			{Command: "leapmux admin app list"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include retired apps")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AppService().ListApps(inv.Context, &leapmuxv1.ListAppsRequest{
				Page: page.request(), IncludeRevoked: includeRevoked,
			})
			if err != nil {
				return nil, err
			}
			rows := make([]AppRegistrationRow, 0, len(resp.GetApps()))
			for _, app := range resp.GetApps() {
				rows = append(rows, appRegistrationRowOf(app))
			}
			return adminAppListResult{Apps: rows, NextCursor: resp.GetPage().GetNextCursor(), OpenRegistrationEnabled: resp.GetOpenRegistrationEnabled()}, nil
		},
	})
}

type adminAppListResult struct {
	Apps                    []AppRegistrationRow `json:"apps"`
	NextCursor              string               `json:"next_cursor"`
	OpenRegistrationEnabled bool                 `json:"open_registration_enabled"`
}

func (r adminAppListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Apps))
	for _, app := range r.Apps {
		out = append(out, app.ClientID)
	}
	return out
}

func resolveAdminAppID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	return resolvePagedAdminID(ctx, "app", raw, true,
		func(ctx context.Context, idPrefix string, page *leapmuxv1.PageRequest) ([]*leapmuxv1.App, *leapmuxv1.PageResponse, error) {
			resp, err := c.AppService().ListApps(ctx, &leapmuxv1.ListAppsRequest{
				IncludeRevoked: true,
				IdPrefix:       idPrefix,
				Page:           page,
			})
			return resp.GetApps(), resp.GetPage(), err
		},
		(*leapmuxv1.App).GetClientId,
		func(app *leapmuxv1.App) cli.Candidate {
			return cli.Candidate{ID: app.GetClientId(), Kind: "app", Title: app.GetClientName()}
		},
	)
}

// AppRegistrationRow contains the fields an admin can read repeatedly.
// The Hub returns a client secret once at registration and stores its hash.
type AppRegistrationRow struct {
	ClientID            string   `json:"client_id" table:"CLIENT ID"`
	ClientName          string   `json:"client_name" table:"NAME"`
	ClientURI           string   `json:"client_uri"`
	Visibility          string   `json:"visibility" table:"VISIBILITY"`
	ClientType          string   `json:"client_type" table:"TYPE"`
	RedirectURIs        []string `json:"redirect_uris"`
	Scopes              []string `json:"scopes"`
	GrantTypes          []string `json:"grant_types"`
	ElevationAllowed    bool     `json:"elevation_allowed"`
	RegistrationSource  string   `json:"registration_source"`
	Verified            bool     `json:"verified" table:"VERIFIED"`
	VerifiedBy          string   `json:"verified_by"`
	HasIcon             bool     `json:"has_icon"`
	LiveCredentialCount int64    `json:"live_credential_count" table:"CREDS"`
	CreatedAt           string   `json:"created_at,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
	VerifiedAt          string   `json:"verified_at,omitempty"`
	RevokedAt           string   `json:"revoked_at,omitempty"`
}

func (r AppRegistrationRow) QuietLines() []string { return []string{r.ClientID} }

func appRegistrationRowOf(app *leapmuxv1.App) AppRegistrationRow {
	return AppRegistrationRow{
		ClientID:            app.GetClientId(),
		ClientName:          app.GetClientName(),
		ClientURI:           app.GetClientUri(),
		Visibility:          enumToken(app.GetVisibility()),
		ClientType:          enumToken(app.GetClientType()),
		RedirectURIs:        nonNilStrings(app.GetRedirectUris()),
		Scopes:              scopeTokensOf(app.GetScopes()),
		GrantTypes:          nonNilStrings(app.GetGrantTypes()),
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

func scopeTokensOf(wire []leapmuxv1.Scope) []string {
	set, ok := authscope.ScopesFromWire(wire)
	if !ok {
		return []string{}
	}
	return nonNilStrings(set.SortedTokens())
}

type adminAppRegisterResult struct {
	AppRegistrationRow
	ClientSecret     string `json:"client_secret,omitempty"`
	ClientSecretNote string `json:"client_secret_note,omitempty"`
}

func newAdminAppRegister() *cobra.Command {
	var uri, redirect, scope, grants, kind, visibility string
	var elevation bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "register <name>",
		Short:   "Register an app; the hub shows its secret once",
		Mutates: true,
		Examples: []cli.Example{
			{Command: `leapmux admin app register "Release bot" --scope "workspace:read" --redirect-uri http://127.0.0.1/callback`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&uri, "uri", "", "the app's home page")
			fs.StringVar(&redirect, "redirect-uri", "",
				"comma-separated addresses the authorization may return to (required for the authorization_code grant)")
			fs.StringVar(&scope, "scope", "", appScopeFlagUsage)
			cli.MarkRequired(fs, "scope")
			fs.StringVar(&grants, "grant-type", "",
				"comma-separated OAuth grant types; empty takes the RFC 7591 default of authorization_code and refresh_token")
			fs.StringVar(&kind, "type", "public",
				"public (a binary a user holds; PKCE protects it) or confidential (a server that can keep a secret)")
			cli.SetEnum(fs, "type", "public", "confidential")
			fs.StringVar(&visibility, "visibility", "hub-wide",
				"hub-wide (every account may authorize it; administrators only) or private (yours alone)")
			cli.SetEnum(fs, "visibility", "hub-wide", "private")
			fs.BoolVar(&elevation, "allow-elevation", false,
				"let this app run the step-up stage, which is what a sensitive change needs")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			name, err := requireNonEmptyArg(inv, "name")
			if err != nil {
				return nil, err
			}
			scopes, err := parseAppScopes(scope)
			if err != nil {
				return nil, err
			}
			clientType, err := parseAppClientType(kind)
			if err != nil {
				return nil, err
			}
			vis, err := parseAppVisibility(visibility)
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AppService().RegisterApp(inv.Context, &leapmuxv1.RegisterAppRequest{
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
				return nil, err
			}
			out := adminAppRegisterResult{AppRegistrationRow: appRegistrationRowOf(resp.GetApp())}
			if secret := resp.GetClientSecret(); secret != "" {
				out.ClientSecret = secret
				out.ClientSecretNote = "Copy this now. The Hub stores only its hash and shows the secret once."
			}
			return out, nil
		},
	})
}

func newAdminAppUpdate() *cobra.Command {
	var name, uri, redirect, scope, grants string
	return cli.NewLeaf(cli.Leaf{
		Use:     "update <app>",
		Short:   "Change the editable fields of an app",
		Mutates: true,
		Examples: []cli.Example{
			{Command: `leapmux admin app update lmx_app_x1y2 --name "Release bot 2"`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&name, "name", "", "new app name")
			fs.StringVar(&uri, "uri", "", "new home page")
			fs.StringVar(&redirect, "redirect-uri", "", "replace the redirect list (comma-separated)")
			fs.StringVar(&scope, "scope", "", "replace the permission ceiling; "+appScopeFlagUsage)
			fs.StringVar(&grants, "grant-type", "", "replace the grant-type list (comma-separated)")
		},
		Complete: completeAdminApps,
		Run: func(inv *cli.Invocation) (any, error) {
			clientID, err := requireNonEmptyArg(inv, "app")
			if err != nil {
				return nil, err
			}
			if !anyLocalFlagChanged(inv) {
				return nil, cli.Usagef("nothing to update (pass at least one flag)")
			}
			var parsedScopes []leapmuxv1.Scope
			if inv.Changed("scope") {
				parsedScopes, err = parseAppScopes(scope)
				if err != nil {
					return nil, err
				}
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminAppID(inv.Context, c, clientID)
			if err != nil {
				return nil, err
			}
			msg := &leapmuxv1.UpdateAppRequest{ClientId: resolved}
			if inv.Changed("name") {
				msg.ClientName = &name
			}
			if inv.Changed("uri") {
				msg.ClientUri = &uri
			}
			if inv.Changed("redirect-uri") {
				msg.ReplaceRedirectUris = true
				msg.RedirectUris = splitCommaList(redirect)
			}
			if inv.Changed("scope") {
				msg.ReplaceScopes = true
				msg.Scopes = parsedScopes
			}
			if inv.Changed("grant-type") {
				msg.ReplaceGrantTypes = true
				msg.GrantTypes = splitCommaList(grants)
			}
			resp, err := c.AppService().UpdateApp(inv.Context, msg)
			if err != nil {
				return nil, err
			}
			return appRegistrationRowOf(resp.GetApp()), nil
		},
	})
}

func newAdminAppToggleLeaf(
	use, short string,
	examples []cli.Example,
	apply func(ctx context.Context, c *control.HubClient, clientID string) (*leapmuxv1.App, error),
) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:      use,
		Short:    short,
		Examples: examples,
		Mutates:  true,
		Complete: completeAdminApps,
		Run: func(inv *cli.Invocation) (any, error) {
			clientID, err := requireNonEmptyArg(inv, "app")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminAppID(inv.Context, c, clientID)
			if err != nil {
				return nil, err
			}
			app, err := apply(inv.Context, c, resolved)
			if err != nil {
				return nil, err
			}
			return appRegistrationRowOf(app), nil
		},
	})
}

func newAdminAppSetElevation(allowed bool, use, short string, examples []cli.Example) *cobra.Command {
	return newAdminAppToggleLeaf(use, short, examples, func(ctx context.Context, c *control.HubClient, clientID string) (*leapmuxv1.App, error) {
		resp, err := c.AppService().SetAppElevationAllowed(ctx,
			&leapmuxv1.SetAppElevationAllowedRequest{ClientId: clientID, Allowed: allowed})
		if err != nil {
			return nil, err
		}
		return resp.GetApp(), nil
	})
}

func newAdminAppSetVerified(verified bool, use, short string, examples []cli.Example) *cobra.Command {
	return newAdminAppToggleLeaf(use, short, examples, func(ctx context.Context, c *control.HubClient, clientID string) (*leapmuxv1.App, error) {
		resp, err := c.AppService().VerifyApp(ctx,
			&leapmuxv1.VerifyAppRequest{ClientId: clientID, Verified: verified})
		if err != nil {
			return nil, err
		}
		return resp.GetApp(), nil
	})
}

type adminAppRevokeResult struct {
	Revoked                string `json:"revoked"`
	RevokedCredentialCount int64  `json:"revoked_credential_count"`
}

func (r adminAppRevokeResult) QuietLines() []string { return []string{r.Revoked} }

func newAdminAppRevoke() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:         "revoke <app>",
		Short:       "Retire an app and revoke every credential it holds",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin app revoke lmx_app_x1y2"},
		},
		Complete: completeAdminApps,
		Run: func(inv *cli.Invocation) (any, error) {
			clientID, err := requireNonEmptyArg(inv, "app")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminAppID(inv.Context, c, clientID)
			if err != nil {
				return nil, err
			}
			resp, err := c.AppService().RevokeApp(inv.Context,
				&leapmuxv1.RevokeAppRequest{ClientId: resolved})
			if err != nil {
				return nil, err
			}
			return adminAppRevokeResult{Revoked: resolved, RevokedCredentialCount: resp.GetRevokedCredentialCount()}, nil
		},
	})
}

type adminAppDeleteResult struct {
	Deleted string `json:"deleted"`
}

func (r adminAppDeleteResult) QuietLines() []string { return []string{r.Deleted} }

func newAdminAppDelete() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:         "delete <app>",
		Aliases:     []string{"rm"},
		Short:       "Delete an app that never held a credential",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin app delete lmx_app_x1y2"},
		},
		Complete: completeAdminApps,
		Run: func(inv *cli.Invocation) (any, error) {
			clientID, err := requireNonEmptyArg(inv, "app")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminAppID(inv.Context, c, clientID)
			if err != nil {
				return nil, err
			}
			if _, err := c.AppService().DeleteApp(inv.Context,
				&leapmuxv1.DeleteAppRequest{ClientId: resolved}); err != nil {
				return nil, err
			}
			return adminAppDeleteResult{Deleted: resolved}, nil
		},
	})
}

// --- flag parsing ----------------------------------------------------------

func parseAppScopes(raw string) ([]leapmuxv1.Scope, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, cli.Usagef("--scope is required: an app must specify the permissions it may ask for")
	}
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

func splitCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

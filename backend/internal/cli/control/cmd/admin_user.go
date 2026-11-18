package cmd

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Flag names, defaults, and validation messages carry over verbatim from
// the old offline admin verbs; --data-dir/--config are gone (these are
// RPC calls now) and output uses the control JSON envelope.

func adminUserJSON(u *leapmuxv1.AdminUser) map[string]any {
	out := map[string]any{
		"id":             u.GetId(),
		"username":       u.GetUsername(),
		"display_name":   u.GetDisplayName(),
		"email":          u.GetEmail(),
		"email_verified": u.GetEmailVerified(),
		"pending_email":  u.GetPendingEmail(),
		"password_set":   u.GetPasswordSet(),
		"is_admin":       u.GetIsAdmin(),
	}
	putTime(out, "created_at", u.GetCreatedAt())
	putTime(out, "updated_at", u.GetUpdatedAt())
	return out
}

// newAdminUserList builds `control admin user list`.
func newAdminUserList() *cobra.Command {
	var page adminPageFlags
	var query string
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List users",
		Examples: []cli.Example{
			{Command: "leapmux control admin user list"},
			{Comment: "Search by name or email.", Command: "leapmux control admin user list --query alice"},
		},
		Page: &page,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&query, "query", "", "search query (empty = list all)")
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().ListUsers(a.Context(), &leapmuxv1.ListUsersRequest{
				Page: page.request(), Query: query,
			})
			if err != nil {
				return err
			}
			users := make([]map[string]any, 0, len(resp.GetUsers()))
			for _, u := range resp.GetUsers() {
				users = append(users, adminUserJSON(u))
			}
			return a.Result(map[string]any{"users": users, "next_cursor": resp.GetPage().GetNextCursor()})
		},
	})
}

// userTarget is the <user> argument of every verb that addresses ONE user,
// as control.ParseUserArgument reads it.
//
// read refuses an empty argument before the dial. The hub holds the same
// rule (service.ResolveUserSelector), but it can only answer over a
// connection that the operator did not need, which is exactly what
// adminVerbSpec.BeforeDial exists to avoid.
//
// The token LISTINGS must NOT use this type: there an absent owner means
// "every user", not a missing argument. They take userFilterFlags.
type userTarget struct {
	// Selector is the UserSelector that the hub reads. read fills it, so Run
	// reads it and an empty argument never reaches a request.
	Selector *leapmuxv1.UserSelector
}

// read reads the <user> argument. It matches adminVerbSpec.BeforeDial, so a
// verb assigns it directly.
func (u *userTarget) read(a adminArgs) (err error) {
	u.Selector, err = control.ParseUserArgument(a.Arg("user"))
	return err
}

// userFilterFlags binds the OPTIONAL (--user-id | --username) owner filter of
// a listing, where neither flag means "every user". It refuses both flags
// before the dial: the hub's selector holds one of the two, so a pair cannot
// even be sent.
type userFilterFlags struct {
	ID       string
	Username string
	// selector is the UserSelector that resolve built; see Selector.
	selector *leapmuxv1.UserSelector
	resolved bool
}

// Selector returns the optional UserSelector of the listing: nil when neither
// flag is set, which the hub reads as every user. It panics when resolve did not
// run, because that nil would widen a listing that a flag narrowed.
func (f *userFilterFlags) Selector() *leapmuxv1.UserSelector {
	if !f.resolved {
		panic("userFilterFlags.Selector called before resolve: set BeforeDial on the verb")
	}
	return f.selector
}

// bind declares the pair. Every listing that takes the filter resolves it
// through the same hub rule, so the help text is one string here.
func (f *userFilterFlags) bind(fs *pflag.FlagSet) {
	fs.StringVar(&f.ID, "user-id", "", "filter by user ID (soft-deleted users included; empty = all users)")
	fs.StringVar(&f.Username, "username", "", "filter by username")
}

// resolve refuses an ambiguous pair and builds the selector that Selector
// returns. It matches adminVerbSpec.BeforeDial, so a verb assigns it directly.
func (f *userFilterFlags) resolve(adminArgs) error {
	sel, err := service.SelectUserFromPair(f.ID, f.Username)
	if err != nil {
		return cli.Usagef("--user-id and --username are mutually exclusive")
	}
	f.selector, f.resolved = sel, true
	return nil
}

// newAdminUserGet builds `control admin user get`.
func newAdminUserGet() *cobra.Command {
	var user userTarget
	return adminLeaf(adminVerbSpec{
		Use:   "get <user>",
		Short: "Show a user",
		Examples: []cli.Example{
			{Command: "leapmux control admin user get alice"},
			{Comment: "Select a user by id.", Command: "leapmux control admin user get id:Ab3dE9"},
		},
		BeforeDial: user.read,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().GetUser(a.Context(), &leapmuxv1.GetUserRequest{
				User: user.Selector,
			})
			if err != nil {
				return err
			}
			return a.Result(adminUserJSON(resp.GetUser()))
		},
	})
}

// newAdminUserCreate builds `control admin user create`.
func newAdminUserCreate() *cobra.Command {
	var username, password, displayName, email string
	var emailVerified, admin bool
	return adminLeaf(adminVerbSpec{
		Use:   "create <username>",
		Short: "Create a user",
		Examples: []cli.Example{
			{Command: "leapmux control admin user create alice --email alice@example.com"},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&password, "password", "", "password (prompted for when omitted, so it stays out of the shell history)")
			fs.StringVar(&displayName, "display-name", "", "display name")
			fs.StringVar(&email, "email", "", "email address")
			fs.BoolVar(&emailVerified, "email-verified", false, "mark email as verified")
			fs.BoolVar(&admin, "admin", false, "grant admin privileges")
		},
		// Prompt LOCALLY before this code builds the request. The RPC cannot
		// prompt, but the CLI can: a password on the command line lands in
		// the shell history and in the process table of every user on the
		// host.
		BeforeDial: func(a adminArgs) error {
			// The username check runs BEFORE the prompt. An operator must
			// not type a secret for a request that cannot succeed, and the
			// hub answers "username is required" only after the dial.
			if err := argInto(&username, "username")(a); err != nil {
				return err
			}
			entered, err := control.RequirePassword(a.Context(), password, "Password: ")
			if err != nil {
				return cli.Wrap(cli.CodeUsage, err, "")
			}
			password = entered
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().CreateUser(a.Context(), &leapmuxv1.CreateUserRequest{
				Username: username, Password: password, DisplayName: displayName,
				Email: email, EmailVerified: emailVerified, IsAdmin: admin,
			})
			if err != nil {
				return err
			}
			return a.Result(adminUserJSON(resp.GetUser()))
		},
	})
}

// newAdminUserUpdate builds `control admin user update`.
func newAdminUserUpdate() *cobra.Command {
	var user userTarget
	var displayName, email, emailVerified string
	var clearPendingEmail bool
	var req *leapmuxv1.UpdateUserRequest
	return adminLeaf(adminVerbSpec{
		Use:   "update <user>",
		Short: "Change a user's name, email or verification",
		Examples: []cli.Example{
			{Command: `leapmux control admin user update alice --display-name "Alice Kim"`},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&displayName, "display-name", "", "new display name")
			fs.StringVar(&email, "email", "", "new email address")
			fs.StringVar(&emailVerified, "email-verified", "", "mark email as verified (true/false)")
			fs.BoolVar(&clearPendingEmail, "clear-pending-email", false, "clear any in-flight email verification (token + attempt counter)")
		},
		BeforeDial: func(a adminArgs) error {
			// Flag PRESENCE, not a non-empty value. `--email ""` clears the
			// address and `--display-name ""` resets the name to the
			// username; a `!= ""` test made both indistinguishable from an
			// absent flag, so the only way to clear an email became
			// unreachable from the CLI.
			req = &leapmuxv1.UpdateUserRequest{ClearPendingEmail: clearPendingEmail}
			if a.Passed("display-name") {
				req.DisplayName = &displayName
			}
			if a.Passed("email") {
				req.Email = &email
			}
			if a.Passed("email-verified") {
				b, err := parseBoolFlag(emailVerified, "email-verified")
				if err != nil {
					return cli.Wrap(cli.CodeUsage, err, "")
				}
				req.EmailVerified = &b
			}
			if req.DisplayName == nil && req.Email == nil && req.EmailVerified == nil && !clearPendingEmail {
				return cli.Usagef("no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)")
			}
			// The selector check runs LAST here. "Nothing to update" is the
			// more specific complaint, and an operator who typed no flags at
			// all is better served by it than by a malformed-selector notice.
			if err := user.read(a); err != nil {
				return err
			}
			req.User = user.Selector
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().UpdateUser(a.Context(), req)
			if err != nil {
				return err
			}
			return a.Result(adminUserJSON(resp.GetUser()))
		},
	})
}

// newAdminUserDelete builds `control admin user delete`.
func newAdminUserDelete() *cobra.Command {
	var user userTarget
	var force bool
	return adminLeaf(adminVerbSpec{
		Use:   "delete <user>",
		Short: "Delete a user",
		Examples: []cli.Example{
			{Command: "leapmux control admin user delete alice"},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&force, "force", false, "required to delete an admin user")
		},
		BeforeDial: user.read,
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminUserService().DeleteUser(a.Context(), &leapmuxv1.DeleteUserRequest{
				User: user.Selector, Force: force,
			}); err != nil {
				return err
			}
			return a.Result(map[string]any{"deleted": true})
		},
	})
}

// newAdminUserSetAdmin builds `control admin user grant-admin` and
// `revoke-admin`.
func newAdminUserSetAdmin(admin bool, use, short string, examples []cli.Example) *cobra.Command {
	var user userTarget
	var force bool
	return adminLeaf(adminVerbSpec{
		Use:      use,
		Short:    short,
		Examples: examples,
		Mutates:  true,
		Flags: func(fs *pflag.FlagSet) {
			if admin {
				// A grant removes no access, so the hub never asks for
				// --force on it.
				return
			}
			// The hub refuses a caller that removes its OWN administrator
			// access without this. The reduction fences the caller's tokens
			// at once and the admin gate then denies the account every
			// Admin* procedure, so recovery needs the offline verb.
			fs.BoolVar(&force, "force", false, "required to remove your own administrator access")
		},
		BeforeDial: user.read,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().SetUserAdmin(a.Context(), &leapmuxv1.SetUserAdminRequest{
				User: user.Selector, IsAdmin: admin, Force: force,
			})
			if err != nil {
				return err
			}
			return a.Result(adminUserJSON(resp.GetUser()))
		},
	})
}

// newAdminUserResetPassword builds `control admin user reset-password`.
//
// The offline break-glass twin is `leapmux recover password reset`, which
// works with the hub stopped. This one needs a running hub and an
// administrator login, and it is the one to reach for while the hub serves.
func newAdminUserResetPassword() *cobra.Command {
	var user userTarget
	var pw string
	return adminLeaf(adminVerbSpec{
		Use:   "reset-password <user>",
		Short: "Set a user's password and revoke every credential they hold",
		Examples: []cli.Example{
			{Comment: "Prompt for the new password.", Command: "leapmux control admin user reset-password alice"},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&pw, "password", "", "new password (prompted for when omitted, so it stays out of the shell history)")
		},
		// Prompt LOCALLY before this code builds the request, the way `user create`
		// does. The RPC cannot prompt, but the CLI can: a password on the
		// command line lands in the shell history and in the process table of
		// every user on the host.
		BeforeDial: func(a adminArgs) error {
			// The selector check runs BEFORE the prompt. An operator must not
			// type a secret for a request that cannot succeed.
			if err := user.read(a); err != nil {
				return err
			}
			entered, err := control.RequirePassword(a.Context(), pw, "New password: ")
			if err != nil {
				return cli.Wrap(cli.CodeUsage, err, "")
			}
			pw = entered
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().ResetPassword(a.Context(), &leapmuxv1.ResetPasswordRequest{
				User: user.Selector, Password: pw,
			})
			if err != nil {
				return err
			}
			// The subject comes back from the hub, so the envelope carries
			// both handles whichever one the operator addressed the user by.
			return a.Result(map[string]any{
				"user_id":                   resp.GetUserId(),
				"username":                  resp.GetUsername(),
				"api_tokens_revoked":        resp.GetApiTokensRevoked(),
				"delegation_tokens_revoked": resp.GetDelegationTokensRevoked(),
			})
		},
	})
}

// newAdminUserListSessions builds `control admin user list-sessions`.
func newAdminUserListSessions() *cobra.Command {
	var page adminPageFlags
	var user userTarget
	return adminLeaf(adminVerbSpec{
		Use:   "list-sessions <user>",
		Short: "List a user's active sessions",
		Examples: []cli.Example{
			{Command: "leapmux control admin user list-sessions alice"},
		},
		Page:       &page,
		BeforeDial: user.read,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().ListUserSessions(a.Context(), &leapmuxv1.ListUserSessionsRequest{
				Page: page.request(), User: user.Selector,
			})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{
				"sessions":    adminSessionsJSON(resp.GetSessions()),
				"next_cursor": resp.GetPage().GetNextCursor(),
			})
		},
	})
}

// tokenLifecycleJSON renders the fields every bearer-token row shares:
// its identity, its owner, and the three lifecycle stamps.
//
// The two token verbs differ only in the two fields their own kind adds,
// so the shared half lives here beside the other row mappers. Building it
// inline twice already cost a field: `owner_deleted` reached both
// rows on the wire and neither loop printed it, so an operator
// enumerating a soft-deleted account's outstanding tokens — which is what
// the user-id filter exists for — could not see that the owner was gone.
func tokenLifecycleJSON(
	id, userID, username string,
	ownerDeleted bool,
	createdAt, lastUsedAt, expiresAt, revokedAt *timestamppb.Timestamp,
) map[string]any {
	row := map[string]any{
		"id":            id,
		"user_id":       userID,
		"username":      username,
		"owner_deleted": ownerDeleted,
	}
	// EVERY stamp goes through putTime, which omits an absent one instead
	// of printing the epoch. A lifecycle stamp is absent until it happens;
	// a stamp the hub always sets reads no differently here, and a hub that
	// somehow omits one must not answer with a 1970 event.
	putTime(row, "created_at", createdAt)
	putTime(row, "last_used_at", lastUsedAt)
	putTime(row, "expires_at", expiresAt)
	putTime(row, "revoked_at", revokedAt)
	return row
}

// adminAPITokenJSON renders one API-token row.
func adminAPITokenJSON(t *leapmuxv1.AdminAPIToken) map[string]any {
	row := tokenLifecycleJSON(t.GetId(), t.GetUserId(), t.GetUsername(), t.GetOwnerDeleted(),
		t.GetCreatedAt(), t.GetLastUsedAt(), t.GetExpiresAt(), t.GetRevokedAt())
	row["client_id"] = t.GetClientId()
	row["client_name"] = t.GetClientName()
	row["installation_name"] = t.GetInstallationName()
	// This field always appears, including when it is empty: "what can this
	// credential do" is the question the listing exists to answer, and an
	// omitted key reads as "unknown" rather than "nothing". An audit of "which
	// credentials administer the hub" reads it for an "admin:" prefix.
	row["granted_scopes"] = t.GetGrantedScopes()
	return row
}

// adminDelegationTokenJSON renders one delegation-token row.
func adminDelegationTokenJSON(t *leapmuxv1.AdminDelegationToken) map[string]any {
	row := tokenLifecycleJSON(t.GetId(), t.GetUserId(), t.GetUsername(), t.GetOwnerDeleted(),
		t.GetCreatedAt(), t.GetLastUsedAt(), t.GetExpiresAt(), t.GetRevokedAt())
	row["worker_id"] = t.GetWorkerId()
	return row
}

// adminSessionJSON renders one session row. Its stamps go through putTime
// for the reason tokenLifecycleJSON states.
func adminSessionJSON(s *leapmuxv1.AdminSession) map[string]any {
	row := map[string]any{
		"id":           s.GetId(),
		"user_id":      s.GetUserId(),
		"username":     s.GetUsername(),
		"user_deleted": s.GetUserDeleted(),
		"ip_address":   s.GetIpAddress(),
		"user_agent":   s.GetUserAgent(),
	}
	putTime(row, "created_at", s.GetCreatedAt())
	putTime(row, "last_active_at", s.GetLastActiveAt())
	putTime(row, "expires_at", s.GetExpiresAt())
	return row
}

func adminSessionsJSON(sessions []*leapmuxv1.AdminSession) []map[string]any {
	rows := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, adminSessionJSON(s))
	}
	return rows
}

// newAdminSessionList builds `control admin session list`.
func newAdminSessionList() *cobra.Command {
	var page adminPageFlags
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List every active session",
		Examples: []cli.Example{
			{Command: "leapmux control admin session list"},
		},
		Page: &page,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().ListSessions(a.Context(), &leapmuxv1.ListSessionsRequest{
				Page: page.request(),
			})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{
				"sessions":    adminSessionsJSON(resp.GetSessions()),
				"next_cursor": resp.GetPage().GetNextCursor(),
			})
		},
	})
}

// newAdminSessionRevoke builds `control admin session revoke`.
func newAdminSessionRevoke() *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:   "revoke <session>",
		Short: "Revoke a session",
		Examples: []cli.Example{
			{Command: "leapmux control admin session revoke 4f1aC2"},
		},
		Mutates:    true,
		BeforeDial: argInto(&id, "session"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminUserService().RevokeSession(a.Context(), &leapmuxv1.RevokeSessionRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(map[string]any{"revoked": id})
		},
	})
}

// newAdminSessionRevokeUser builds `control admin session revoke-user`.
func newAdminSessionRevokeUser() *cobra.Command {
	var user userTarget
	return adminLeaf(adminVerbSpec{
		Use:   "revoke-user <user>",
		Short: "Revoke every session of a user",
		Examples: []cli.Example{
			{Command: "leapmux control admin session revoke-user alice"},
		},
		Mutates:    true,
		BeforeDial: user.read,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().RevokeUserSessions(a.Context(), &leapmuxv1.RevokeUserSessionsRequest{
				User: user.Selector,
			})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{
				"api_tokens_revoked":        resp.GetApiTokensRevoked(),
				"delegation_tokens_revoked": resp.GetDelegationTokensRevoked(),
			})
		},
	})
}

// newAdminSessionPurgeExpired builds `control admin session purge-expired`.
func newAdminSessionPurgeExpired() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "purge-expired",
		Short: "Delete every expired session",
		Examples: []cli.Example{
			{Command: "leapmux control admin session purge-expired"},
		},
		Mutates: true,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().PurgeExpiredSessions(a.Context(), &leapmuxv1.PurgeExpiredSessionsRequest{})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{"purged": resp.GetPurged()})
		},
	})
}

// newAdminAPITokenList builds `control admin api-token list`.
func newAdminAPITokenList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	var clientID string
	var includeRevoked bool
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List API tokens",
		Examples: []cli.Example{
			{Command: "leapmux control admin api-token list"},
			{Comment: "List one user's tokens.", Command: "leapmux control admin api-token list --username alice"},
		},
		Page: &page,
		Flags: func(fs *pflag.FlagSet) {
			owner.bind(fs)
			fs.StringVar(&clientID, "client-id", "", "filter by app (empty = all apps)")
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		},
		BeforeDial: owner.resolve,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().ListAPITokens(a.Context(), &leapmuxv1.ListAPITokensRequest{
				User: owner.Selector(), ClientId: clientID,
				IncludeRevoked: includeRevoked, Page: page.request(),
			})
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(resp.GetTokens()))
			for _, t := range resp.GetTokens() {
				rows = append(rows, adminAPITokenJSON(t))
			}
			return a.Result(map[string]any{"tokens": rows, "next_cursor": resp.GetPage().GetNextCursor()})
		},
	})
}

// newAdminAPITokenIssue builds `control admin api-token issue`. The
// secrets cross the envelope exactly once; they cannot be retrieved.
func newAdminAPITokenIssue() *cobra.Command {
	var user userTarget
	var clientID, installationName, scopeFlag string
	var ttlSeconds int64
	var scopeList []string
	return adminLeaf(adminVerbSpec{
		Use:   "issue <user>",
		Short: "Issue an API token for a script or a service account",
		// The owner is the same <user> argument that every other
		// user-addressing verb takes. The verb that MINTS a credential is the
		// last one on which specifying the wrong account should be easy.
		Long: "Issue an API token that <user> owns.",
		Examples: []cli.Example{
			{Command: `leapmux control admin api-token issue ci-bot --installation-name ci-runner-3 --scope "workspace:read"`},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&clientID, "client-id", "",
				"the app this credential belongs to (empty = the built-in service-account registration)")
			fs.StringVar(&installationName, "installation-name", "",
				"human-visible name for this installation, e.g. ci-runner-3")
			cli.MarkRequired(fs, "installation-name")
			// The flag picks WHICH KIND of credential, and the help says so:
			// zero mints the renewing one, and a positive value mints a
			// fixed-lifetime service credential with no refresh token. The
			// two do not combine -- a long TTL plus a refresh token loses the
			// TTL on the first rotation, because the row records an expiry
			// and never the lifetime it was minted from.
			fs.Int64Var(&ttlSeconds, "ttl", 0,
				"fixed lifetime in seconds, with no refresh token (0 = the renewing credential: 1h access + refresh)")
			// EMPTY grants everything except the admin scopes, which is
			// the same default a `leapmux control auth login` takes: a service
			// account that only needs to drive workspaces must not come out of
			// this verb able to administer the hub.
			fs.StringVar(&scopeFlag, "scope", "",
				"space- or comma-separated permissions, e.g. \"file:read git:read\" (empty = everything except admin:*)")
		},
		BeforeDial: func(a adminArgs) error {
			if err := user.read(a); err != nil {
				return err
			}
			if strings.TrimSpace(installationName) == "" {
				return cli.Usagef("--installation-name is empty")
			}
			var scopeErr error
			scopeList, scopeErr = splitScopeFlag(scopeFlag)
			if scopeErr != nil {
				return scopeErr
			}
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().IssueAPIToken(a.Context(), &leapmuxv1.IssueAPITokenRequest{
				User: user.Selector, ClientId: clientID,
				InstallationName: installationName,
				TtlSeconds:       ttlSeconds, Scopes: scopeList,
			})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{
				"token_id":       resp.GetTokenId(),
				"access_token":   resp.GetAccessToken(),
				"refresh_token":  resp.GetRefreshToken(),
				"client_id":      resp.GetClientId(),
				"granted_scopes": resp.GetGrantedScopes(),
				"note":           "capture the tokens now; they cannot be retrieved later",
			})
		},
	})
}

// newAdminAPITokenRevoke builds `control admin api-token revoke`.
func newAdminAPITokenRevoke() *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:   "revoke <token>",
		Short: "Revoke an API token",
		Examples: []cli.Example{
			{Command: "leapmux control admin api-token revoke 7Qx2Lm"},
		},
		Mutates:    true,
		BeforeDial: argInto(&id, "token"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminUserService().RevokeAPIToken(a.Context(), &leapmuxv1.RevokeAPITokenRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(map[string]any{"revoked": id})
		},
	})
}

// newAdminDelegationTokenList builds `control admin delegation-token list`.
func newAdminDelegationTokenList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	var includeRevoked bool
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List the delegation tokens that workers minted",
		Examples: []cli.Example{
			{Command: "leapmux control admin delegation-token list"},
		},
		Page: &page,
		Flags: func(fs *pflag.FlagSet) {
			owner.bind(fs)
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		},
		BeforeDial: owner.resolve,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminUserService().ListDelegationTokens(a.Context(), &leapmuxv1.ListDelegationTokensRequest{
				User: owner.Selector(), IncludeRevoked: includeRevoked, Page: page.request(),
			})
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(resp.GetTokens()))
			for _, t := range resp.GetTokens() {
				rows = append(rows, adminDelegationTokenJSON(t))
			}
			return a.Result(map[string]any{"tokens": rows, "next_cursor": resp.GetPage().GetNextCursor()})
		},
	})
}

// newAdminDelegationTokenRevoke builds `control admin delegation-token revoke`.
func newAdminDelegationTokenRevoke() *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:   "revoke <token>",
		Short: "Revoke a delegation token",
		Examples: []cli.Example{
			{Command: "leapmux control admin delegation-token revoke 7Qx2Lm"},
		},
		Mutates:    true,
		BeforeDial: argInto(&id, "token"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminUserService().RevokeDelegationToken(a.Context(), &leapmuxv1.AdminUserServiceRevokeDelegationTokenRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(map[string]any{"revoked": id})
		},
	})
}

// splitScopeFlag splits a --scope value into its tokens.
//
// It accepts BOTH separators. The wire format is space-delimited (RFC 6749
// section 3.3), which a shell needs quoted; a comma-separated list is what
// somebody types without thinking about quoting. Accepting both costs one
// FieldsFunc and removes a failure whose only symptom is a scope named
// "file:read,git:read", which the hub then refuses as unknown.
//
// An EMPTY value is the documented "take the default" and returns no
// tokens, which every caller reads as "the default grant" rather than as
// "no permissions at all". A value that is NOT empty but yields no tokens --
// a stray comma, a shell-mangled "${A},${B}" with empty variables -- is a
// REFUSAL rather than silence, because every caller's hub reads an absent
// list as "unspecified" and answers the widest default grant. Losing the
// operator's constraint that way is the one outcome this parser exists to
// prevent.
func splitScopeFlag(raw string) ([]string, error) {
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if raw != "" && len(tokens) == 0 {
		return nil, cli.Usagef("--scope carried no permission names; pass real tokens or omit the flag for the default")
	}
	return tokens, nil
}

package cmd

import (
	"context"
	"flag"
	"strings"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
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

// RunAdminUserList implements `control admin user list`.
func RunAdminUserList(rawCtx any, args []string) error {
	var page adminPageFlags
	var query string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page: &page,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&query, "query", "", "search query (empty = list all)")
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().ListUsers(context.Background(), connect.NewRequest(&leapmuxv1.ListUsersRequest{
				Query: query, Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			users := make([]map[string]any, 0, len(resp.Msg.GetUsers()))
			for _, u := range resp.Msg.GetUsers() {
				users = append(users, adminUserJSON(u))
			}
			return control.EmitData(map[string]any{"users": users, "next_cursor": resp.Msg.GetNextCursor()})
		},
	})
}

// userSelectorFlags binds the (id | username) pair that every verb
// addressing ONE user takes, and refuses an empty or ambiguous selector
// before the dial.
//
// The hub holds the same rule (service.ResolveUserSelector), but it can
// only answer over a connection the operator did not need — which is
// exactly what adminVerbSpec.BeforeDial exists to avoid. Declaring the
// pair through this type keeps the binding and its check inseparable, the
// way adminPageFlags and rateLimitTarget already do in this package.
//
// The id flag NAME is a parameter, because `session revoke-user` spells it
// `--user-id`. The token LISTINGS must NOT use this type: there an empty
// selector means "every user", not a missing argument.
type userSelectorFlags struct {
	ID       string
	Username string
	idFlag   string
}

// bind declares the pair. usage overrides the username flag's help for the
// one verb that reads it as a lookup key rather than a subject.
func (s *userSelectorFlags) bind(fs *flag.FlagSet, idFlag, usernameUsage string) {
	s.idFlag = idFlag
	fs.StringVar(&s.ID, idFlag, "", "user ID")
	fs.StringVar(&s.Username, "username", "", usernameUsage)
}

// resolve matches adminVerbSpec.BeforeDial, so a verb assigns it directly.
func (s *userSelectorFlags) resolve(adminArgs) error {
	switch {
	case s.ID == "" && s.Username == "":
		return control.EmitError("invalid_request", "--"+s.idFlag+" or --username is required")
	case s.ID != "" && s.Username != "":
		return control.EmitError("invalid_request", "--"+s.idFlag+" and --username are mutually exclusive")
	}
	return nil
}

// RunAdminUserGet implements `control admin user get`.
func RunAdminUserGet(rawCtx any, args []string) error {
	var sel userSelectorFlags
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags:      func(fs *flag.FlagSet) { sel.bind(fs, "id", "username") },
		BeforeDial: sel.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().GetUser(context.Background(), connect.NewRequest(&leapmuxv1.GetUserRequest{
				Id: sel.ID, Username: sel.Username,
			}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			return control.EmitData(adminUserJSON(resp.Msg.GetUser()))
		},
	})
}

// RunAdminUserCreate implements `control admin user create`.
func RunAdminUserCreate(rawCtx any, args []string) error {
	var username, password, displayName, email string
	var emailVerified, admin bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&username, "username", "", "username (required)")
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
			if err := requireFlag(&username, "username")(a); err != nil {
				return err
			}
			entered, err := control.RequirePassword(password, "Password: ")
			if err != nil {
				return control.EmitErrorWith("invalid_request", err)
			}
			password = entered
			return nil
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().CreateUser(context.Background(), connect.NewRequest(&leapmuxv1.CreateUserRequest{
				Username: username, Password: password, DisplayName: displayName,
				Email: email, EmailVerified: emailVerified, IsAdmin: admin,
			}))
			if err != nil {
				return control.EmitErrorWith("create_failed", err)
			}
			return control.EmitData(adminUserJSON(resp.Msg.GetUser()))
		},
	})
}

// RunAdminUserUpdate implements `control admin user update`.
func RunAdminUserUpdate(rawCtx any, args []string) error {
	var sel userSelectorFlags
	var displayName, email, emailVerified string
	var clearPendingEmail bool
	var req *leapmuxv1.UpdateUserRequest
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			sel.bind(fs, "id", "username (for lookup)")
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
			req = &leapmuxv1.UpdateUserRequest{Id: sel.ID, Username: sel.Username, ClearPendingEmail: clearPendingEmail}
			if a.Passed("display-name") {
				req.DisplayName = &displayName
			}
			if a.Passed("email") {
				req.Email = &email
			}
			if a.Passed("email-verified") {
				b, err := parseBoolFlag(emailVerified, "email-verified")
				if err != nil {
					return control.EmitErrorWith("invalid_request", err)
				}
				req.EmailVerified = &b
			}
			if req.DisplayName == nil && req.Email == nil && req.EmailVerified == nil && !clearPendingEmail {
				return control.EmitError("invalid_request", "no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)")
			}
			// The selector check runs LAST here. "Nothing to update" is the
			// more specific complaint, and an operator who typed no flags at
			// all is better served by it than by a missing-selector notice.
			return sel.resolve(a)
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().UpdateUser(context.Background(), connect.NewRequest(req))
			if err != nil {
				return control.EmitErrorWith("update_failed", err)
			}
			return control.EmitData(adminUserJSON(resp.Msg.GetUser()))
		},
	})
}

// RunAdminUserDelete implements `control admin user delete`.
func RunAdminUserDelete(rawCtx any, args []string) error {
	var sel userSelectorFlags
	var force bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			sel.bind(fs, "id", "username")
			fs.BoolVar(&force, "force", false, "required to delete an admin user")
		},
		BeforeDial: sel.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminUserService().DeleteUser(context.Background(), connect.NewRequest(&leapmuxv1.DeleteUserRequest{
				Id: sel.ID, Username: sel.Username, Force: force,
			})); err != nil {
				return control.EmitErrorWith("delete_failed", err)
			}
			return control.EmitData(map[string]any{"deleted": true})
		},
	})
}

// RunAdminUserSetAdmin implements `control admin user grant-admin` and
// `revoke-admin`.
func RunAdminUserSetAdmin(rawCtx any, args []string, admin bool) error {
	var sel userSelectorFlags
	var force bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			sel.bind(fs, "id", "username")
			// The hub refuses a caller that removes its OWN administrator
			// access without this. The reduction fences the caller's tokens
			// at once and the admin gate then denies the account every
			// Admin* procedure, so recovery needs the offline verb.
			fs.BoolVar(&force, "force", false, "required to remove your own administrator access")
		},
		BeforeDial: sel.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().SetUserAdmin(context.Background(), connect.NewRequest(&leapmuxv1.SetUserAdminRequest{
				Id: sel.ID, Username: sel.Username, IsAdmin: admin, Force: force,
			}))
			if err != nil {
				return control.EmitErrorWith("update_failed", err)
			}
			return control.EmitData(adminUserJSON(resp.Msg.GetUser()))
		},
	})
}

// RunAdminUserResetPassword implements `control admin user reset-password`.
//
// The offline break-glass twin is `leapmux recover password reset`, which
// works with the hub stopped. This one needs a running hub and an
// administrator login, and it is the one to reach for while the hub serves.
func RunAdminUserResetPassword(rawCtx any, args []string) error {
	var sel userSelectorFlags
	var pw string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			sel.bind(fs, "id", "username")
			fs.StringVar(&pw, "password", "", "new password (prompted for when omitted, so it stays out of the shell history)")
		},
		// Prompt LOCALLY before this code builds the request, the way `user create`
		// does. The RPC cannot prompt, but the CLI can: a password on the
		// command line lands in the shell history and in the process table of
		// every user on the host.
		BeforeDial: func(a adminArgs) error {
			// The selector check runs BEFORE the prompt. An operator must not
			// type a secret for a request that cannot succeed.
			if err := sel.resolve(a); err != nil {
				return err
			}
			entered, err := control.RequirePassword(pw, "New password: ")
			if err != nil {
				return control.EmitErrorWith("invalid_request", err)
			}
			pw = entered
			return nil
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().ResetPassword(context.Background(), connect.NewRequest(&leapmuxv1.ResetPasswordRequest{
				Id: sel.ID, Username: sel.Username, Password: pw,
			}))
			if err != nil {
				return control.EmitErrorWith("reset_failed", err)
			}
			// The subject comes back from the hub, so the envelope carries
			// both handles whichever one the operator addressed the user by.
			return control.EmitData(map[string]any{
				"user_id":                   resp.Msg.GetUserId(),
				"username":                  resp.Msg.GetUsername(),
				"api_tokens_revoked":        resp.Msg.GetApiTokensRevoked(),
				"delegation_tokens_revoked": resp.Msg.GetDelegationTokensRevoked(),
			})
		},
	})
}

// RunAdminUserListSessions implements `control admin user list-sessions`.
func RunAdminUserListSessions(rawCtx any, args []string) error {
	var page adminPageFlags
	var sel userSelectorFlags
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page:       &page,
		Flags:      func(fs *flag.FlagSet) { sel.bind(fs, "id", "username") },
		BeforeDial: sel.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().ListUserSessions(context.Background(), connect.NewRequest(&leapmuxv1.ListUserSessionsRequest{
				Id: sel.ID, Username: sel.Username, Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			return control.EmitData(map[string]any{
				"sessions":    adminSessionsJSON(resp.Msg.GetSessions()),
				"next_cursor": resp.Msg.GetNextCursor(),
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
	row["agent_id"] = t.GetAgentId()
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

// RunAdminSessionList implements `control admin session list`.
func RunAdminSessionList(rawCtx any, args []string) error {
	var page adminPageFlags
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page: &page,
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().ListSessions(context.Background(), connect.NewRequest(&leapmuxv1.ListSessionsRequest{
				Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			return control.EmitData(map[string]any{
				"sessions":    adminSessionsJSON(resp.Msg.GetSessions()),
				"next_cursor": resp.Msg.GetNextCursor(),
			})
		},
	})
}

// RunAdminSessionRevoke implements `control admin session revoke`.
func RunAdminSessionRevoke(rawCtx any, args []string) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "session ID (required)")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminUserService().RevokeSession(context.Background(), connect.NewRequest(&leapmuxv1.RevokeSessionRequest{Id: id})); err != nil {
				return control.EmitErrorWith("revoke_failed", err)
			}
			return control.EmitData(map[string]any{"revoked": id})
		},
	})
}

// RunAdminSessionRevokeUser implements `control admin session revoke-user`.
func RunAdminSessionRevokeUser(rawCtx any, args []string) error {
	var sel userSelectorFlags
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags:      func(fs *flag.FlagSet) { sel.bind(fs, "user-id", "username") },
		BeforeDial: sel.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().RevokeUserSessions(context.Background(), connect.NewRequest(&leapmuxv1.RevokeUserSessionsRequest{
				Id: sel.ID, Username: sel.Username,
			}))
			if err != nil {
				return control.EmitErrorWith("revoke_failed", err)
			}
			return control.EmitData(map[string]any{
				"api_tokens_revoked":        resp.Msg.GetApiTokensRevoked(),
				"delegation_tokens_revoked": resp.Msg.GetDelegationTokensRevoked(),
			})
		},
	})
}

// RunAdminSessionPurgeExpired implements `control admin session purge-expired`.
func RunAdminSessionPurgeExpired(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().PurgeExpiredSessions(context.Background(), connect.NewRequest(&leapmuxv1.PurgeExpiredSessionsRequest{}))
			if err != nil {
				return control.EmitErrorWith("purge_failed", err)
			}
			return control.EmitData(map[string]any{"purged": resp.Msg.GetPurged()})
		},
	})
}

// RunAdminAPITokenList implements `control admin api-token list`.
func RunAdminAPITokenList(rawCtx any, args []string) error {
	var page adminPageFlags
	var userID, username, clientID string
	var includeRevoked bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page: &page,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&userID, "user-id", "", "filter by user ID (soft-deleted users included; empty = all users)")
			fs.StringVar(&username, "username", "", "filter by username")
			fs.StringVar(&clientID, "client-id", "", "filter by app (empty = all apps)")
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().ListAPITokens(context.Background(), connect.NewRequest(&leapmuxv1.ListAPITokensRequest{
				UserId: userID, Username: username, ClientId: clientID,
				IncludeRevoked: includeRevoked, Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			rows := make([]map[string]any, 0, len(resp.Msg.GetTokens()))
			for _, t := range resp.Msg.GetTokens() {
				rows = append(rows, adminAPITokenJSON(t))
			}
			return control.EmitData(map[string]any{"tokens": rows, "next_cursor": resp.Msg.GetNextCursor()})
		},
	})
}

// RunAdminAPITokenIssue implements `control admin api-token issue`. The
// secrets cross the envelope exactly once; they cannot be retrieved.
func RunAdminAPITokenIssue(rawCtx any, args []string) error {
	var sel userSelectorFlags
	var clientID, installationName, scopeFlag string
	var ttlSeconds int64
	var scopeList []string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			// The SAME selector the six sibling verbs take, spelled
			// --user-id here to match the filter on `api-token list` and to
			// stay clear of --id, which addresses the TOKEN on `revoke`.
			// The verb that MINTS a credential is the last one on which
			// specifying the wrong account should be easy.
			sel.bind(fs, "user-id", "username of the token owner")
			fs.StringVar(&clientID, "client-id", "",
				"the app this credential belongs to (empty = the built-in service-account registration)")
			fs.StringVar(&installationName, "installation-name", "",
				"human-visible name for this installation, e.g. ci-runner-3 (required)")
			// The flag picks WHICH KIND of credential, and the help says so:
			// zero mints the renewing one, and a positive value mints a
			// fixed-lifetime service credential with no refresh token. The
			// two do not combine -- a long TTL plus a refresh token loses the
			// TTL on the first rotation, because the row records an expiry
			// and never the lifetime it was minted from.
			fs.Int64Var(&ttlSeconds, "ttl", 0,
				"fixed lifetime in seconds, with no refresh token (0 = the renewing credential: 1h access + refresh)")
			// EMPTY grants everything except the four admin scopes, which is
			// the same default a `leapmux control auth login` takes: a service
			// account that only needs to drive workspaces must not come out of
			// this verb able to administer the hub.
			fs.StringVar(&scopeFlag, "scope", "",
				"space- or comma-separated permissions, e.g. \"file:read git:read\" (empty = everything except admin:*)")
		},
		BeforeDial: func(a adminArgs) error {
			if err := sel.resolve(a); err != nil {
				return err
			}
			if installationName == "" {
				return control.EmitError("invalid_request", "--installation-name is required")
			}
			scopeList = splitScopeFlag(scopeFlag)
			return nil
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().IssueAPIToken(context.Background(), connect.NewRequest(&leapmuxv1.IssueAPITokenRequest{
				UserId: sel.ID, Username: sel.Username, ClientId: clientID,
				InstallationName: installationName,
				TtlSeconds:       ttlSeconds, Scopes: scopeList,
			}))
			if err != nil {
				return control.EmitErrorWith("issue_failed", err)
			}
			return control.EmitData(map[string]any{
				"token_id":      resp.Msg.GetTokenId(),
				"access_token":  resp.Msg.GetAccessToken(),
				"refresh_token": resp.Msg.GetRefreshToken(),
				"scopes":        scopeList,
				"note":          "capture the tokens now; they cannot be retrieved later",
			})
		},
	})
}

// RunAdminAPITokenRevoke implements `control admin api-token revoke`.
func RunAdminAPITokenRevoke(rawCtx any, args []string) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "token id (required)")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminUserService().RevokeAPIToken(context.Background(), connect.NewRequest(&leapmuxv1.RevokeAPITokenRequest{Id: id})); err != nil {
				return control.EmitErrorWith("revoke_failed", err)
			}
			return control.EmitData(map[string]any{"revoked": id})
		},
	})
}

// RunAdminDelegationTokenList implements `control admin delegation-token list`.
func RunAdminDelegationTokenList(rawCtx any, args []string) error {
	var page adminPageFlags
	var userID, username string
	var includeRevoked bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page: &page,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&userID, "user-id", "", "filter by user ID (soft-deleted users included; empty = all users)")
			fs.StringVar(&username, "username", "", "filter by username")
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminUserService().ListDelegationTokens(context.Background(), connect.NewRequest(&leapmuxv1.ListDelegationTokensRequest{
				UserId: userID, Username: username, IncludeRevoked: includeRevoked, Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			rows := make([]map[string]any, 0, len(resp.Msg.GetTokens()))
			for _, t := range resp.Msg.GetTokens() {
				rows = append(rows, adminDelegationTokenJSON(t))
			}
			return control.EmitData(map[string]any{"tokens": rows, "next_cursor": resp.Msg.GetNextCursor()})
		},
	})
}

// RunAdminDelegationTokenRevoke implements `control admin delegation-token revoke`.
func RunAdminDelegationTokenRevoke(rawCtx any, args []string) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "token id (required)")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminUserService().RevokeDelegationToken(context.Background(), connect.NewRequest(&leapmuxv1.AdminUserServiceRevokeDelegationTokenRequest{Id: id})); err != nil {
				return control.EmitErrorWith("revoke_failed", err)
			}
			return control.EmitData(map[string]any{"revoked": id})
		},
	})
}

// splitScopeFlag reads a --scope value.
//
// It accepts BOTH separators. The wire format is space-delimited (RFC 6749
// section 3.3), which a shell needs quoted; a comma-separated list is what
// somebody types without thinking about quoting. Accepting both costs one
// FieldsFunc and removes a failure whose only symptom is a scope named
// "file:read,git:read", which the hub then refuses as unknown.
//
// An empty value yields nil, which every caller reads as "the default grant"
// rather than as "no permissions at all".
func splitScopeFlag(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

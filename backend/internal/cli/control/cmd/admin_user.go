package cmd

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

type adminUserRow struct {
	ID            string `json:"id" table:"ID,short"`
	Username      string `json:"username" table:"USERNAME"`
	DisplayName   string `json:"display_name" table:"DISPLAY NAME"`
	Email         string `json:"email" table:"EMAIL"`
	EmailVerified bool   `json:"email_verified"`
	PendingEmail  string `json:"pending_email"`
	PasswordSet   bool   `json:"password_set"`
	IsAdmin       bool   `json:"is_admin" table:"ADMIN"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

func (r adminUserRow) QuietLines() []string { return []string{r.ID} }

func adminUserRowOf(u *leapmuxv1.AdminUser) adminUserRow {
	return adminUserRow{
		ID:            u.GetId(),
		Username:      u.GetUsername(),
		DisplayName:   u.GetDisplayName(),
		Email:         u.GetEmail(),
		EmailVerified: u.GetEmailVerified(),
		PendingEmail:  u.GetPendingEmail(),
		PasswordSet:   u.GetPasswordSet(),
		IsAdmin:       u.GetIsAdmin(),
		CreatedAt:     formatTime(u.GetCreatedAt()),
		UpdatedAt:     formatTime(u.GetUpdatedAt()),
	}
}

// newAdminUserList builds `admin user list`.
func newAdminUserList() *cobra.Command {
	var page adminPageFlags
	var query string
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List users",
		Examples: []cli.Example{
			{Command: "leapmux admin user list"},
			{Comment: "Search by name or email.", Command: "leapmux admin user list --query alice"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			fs.StringVar(&query, "query", "", "search query (empty = list all)")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminUserService().ListUsers(inv.Context, &leapmuxv1.ListUsersRequest{
				Page: page.request(), Query: query,
			})
			if err != nil {
				return nil, err
			}
			users := make([]adminUserRow, 0, len(resp.GetUsers()))
			for _, u := range resp.GetUsers() {
				users = append(users, adminUserRowOf(u))
			}
			return adminUserListResult{Users: users, NextCursor: resp.GetPage().GetNextCursor()}, nil
		},
	})
}

type adminUserListResult struct {
	Users      []adminUserRow `json:"users"`
	NextCursor string         `json:"next_cursor"`
}

func (r adminUserListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Users))
	for _, u := range r.Users {
		out = append(out, u.ID)
	}
	return out
}

// userFilterFlags binds the optional --user owner filter of an admin listing,
// where an absent flag means "every user".
type userFilterFlags struct {
	User string
}

func (f *userFilterFlags) bind(fs *pflag.FlagSet) {
	fs.StringVar(&f.User, "user", "", `filter by user: username, "id:<id>", "username:<name>", or user ID prefix (omit for all users)`)
}

func (f *userFilterFlags) parse(inv *cli.Invocation) (adminUserTarget, error) {
	if !inv.Changed("user") {
		return adminUserTarget{}, nil
	}
	return parseAdminUserTarget(f.User)
}

// newAdminUserGet builds `admin user get`.
func newAdminUserGet() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "get <user>",
		Short: "Show a user",
		Examples: []cli.Example{
			{Command: "leapmux admin user get alice"},
			{Comment: "Select a user by id.", Command: "leapmux admin user get id:Ab3dE9"},
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.GetUserResponse, error) {
				return c.AdminUserService().GetUser(inv.Context, &leapmuxv1.GetUserRequest{User: sel})
			})
			if err != nil {
				return nil, err
			}
			return adminUserRowOf(resp.GetUser()), nil
		},
	})
}

// newAdminUserCreate builds `admin user create`.
func newAdminUserCreate() *cobra.Command {
	var displayName, email string
	var emailVerified, admin bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "create <username>",
		Short:   "Create a user",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux admin user create alice --email alice@example.com"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("password", "", "password (prompted for when omitted, so it stays out of the shell history)")
			fs.StringVar(&displayName, "display-name", "", "display name")
			fs.StringVar(&email, "email", "", "email address")
			fs.BoolVar(&emailVerified, "email-verified", false, "mark email as verified")
			fs.BoolVar(&admin, "admin", false, "grant admin privileges")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			username, err := requireNonEmptyArg(inv, "username")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			entered, err := requirePasswordFromInv(inv, "password", "Password: ")
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminUserService().CreateUser(inv.Context, &leapmuxv1.CreateUserRequest{
				Username: username, Password: entered, DisplayName: displayName,
				Email: email, EmailVerified: emailVerified, IsAdmin: admin,
			})
			if err != nil {
				return nil, err
			}
			return adminUserRowOf(resp.GetUser()), nil
		},
	})
}

// newAdminUserUpdate builds `admin user update`.
func newAdminUserUpdate() *cobra.Command {
	var displayName, email string
	var clearPendingEmail bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "update <user>",
		Short:   "Change a user's name, email or verification",
		Mutates: true,
		Examples: []cli.Example{
			{Command: `leapmux admin user update alice --display-name "Alice Kim"`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&displayName, "display-name", "", "new display name")
			fs.StringVar(&email, "email", "", "new email address")
			fs.String("email-verified", "", "mark email as verified")
			cli.SetEnum(fs, "email-verified", "true", "false")
			fs.BoolVar(&clearPendingEmail, "clear-pending-email", false, "clear any in-flight email verification (token + attempt counter)")
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			req := &leapmuxv1.UpdateUserRequest{ClearPendingEmail: clearPendingEmail}
			if inv.Changed("display-name") {
				req.DisplayName = &displayName
			}
			if inv.Changed("email") {
				req.Email = &email
			}
			verified, err := parseOptionalBoolFlag(inv, "email-verified")
			if err != nil {
				return nil, err
			}
			req.EmailVerified = verified
			if req.DisplayName == nil && req.Email == nil && req.EmailVerified == nil && !clearPendingEmail {
				return nil, cli.Usagef("no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)")
			}
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.UpdateUserResponse, error) {
				req.User = sel
				return c.AdminUserService().UpdateUser(inv.Context, req)
			})
			if err != nil {
				return nil, err
			}
			return adminUserRowOf(resp.GetUser()), nil
		},
	})
}

// newAdminUserDelete builds `admin user delete`.
func newAdminUserDelete() *cobra.Command {
	var force bool
	return cli.NewLeaf(cli.Leaf{
		Use:              "delete <user>",
		Aliases:          []string{"rm"},
		Short:            "Delete a user",
		Mutates:          true,
		Destructive:      true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin user delete alice"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&force, "force", false, "required to delete an admin user")
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			if _, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.DeleteUserResponse, error) {
				return c.AdminUserService().DeleteUser(inv.Context, &leapmuxv1.DeleteUserRequest{
					User: sel, Force: force,
				})
			}); err != nil {
				return nil, err
			}
			return struct {
				Deleted bool `json:"deleted"`
			}{Deleted: true}, nil
		},
	})
}

// newAdminUserSetAdmin builds `admin user grant-admin` and `revoke-admin`.
func newAdminUserSetAdmin(admin bool, use, short string, examples []cli.Example) *cobra.Command {
	var force bool
	return cli.NewLeaf(cli.Leaf{
		Use:         use,
		Short:       short,
		Examples:    examples,
		Mutates:     true,
		Destructive: !admin,
		Flags: func(fs *pflag.FlagSet) {
			if admin {
				return
			}
			fs.BoolVar(&force, "force", false, "required to remove your own administrator access")
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.SetUserAdminResponse, error) {
				return c.AdminUserService().SetUserAdmin(inv.Context, &leapmuxv1.SetUserAdminRequest{
					User: sel, IsAdmin: admin, Force: force,
				})
			})
			if err != nil {
				return nil, err
			}
			return adminUserRowOf(resp.GetUser()), nil
		},
	})
}

type adminResetPasswordResult struct {
	UserID                  string `json:"user_id"`
	Username                string `json:"username"`
	APITokensRevoked        int64  `json:"api_tokens_revoked"`
	DelegationTokensRevoked int64  `json:"delegation_tokens_revoked"`
}

func (r adminResetPasswordResult) QuietLines() []string { return []string{r.UserID} }

// newAdminUserResetPassword builds `admin user reset-password`.
func newAdminUserResetPassword() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:         "reset-password <user>",
		Short:       "Set a user's password and revoke every credential they hold",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Comment: "Prompt for the new password.", Command: "leapmux admin user reset-password alice"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("password", "", "new password (prompted for when omitted, so it stays out of the shell history)")
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			entered, err := requirePasswordFromInv(inv, "password", "New password: ")
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.ResetPasswordResponse, error) {
				return c.AdminUserService().ResetPassword(inv.Context, &leapmuxv1.ResetPasswordRequest{
					User: sel, Password: entered,
				})
			})
			if err != nil {
				return nil, err
			}
			return adminResetPasswordResult{
				UserID: resp.GetUserId(), Username: resp.GetUsername(),
				APITokensRevoked: resp.GetApiTokensRevoked(), DelegationTokensRevoked: resp.GetDelegationTokensRevoked(),
			}, nil
		},
	})
}

type adminSessionPage struct {
	Sessions   []adminSessionRow `json:"sessions"`
	NextCursor string            `json:"next_cursor"`
}

func (p adminSessionPage) QuietLines() []string {
	out := make([]string, 0, len(p.Sessions))
	for _, s := range p.Sessions {
		out = append(out, s.ID)
	}
	return out
}

func resolveAdminSessionID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	return resolvePagedAdminID(ctx, "session", raw, false,
		func(ctx context.Context, page *leapmuxv1.PageRequest) ([]*leapmuxv1.AdminSession, *leapmuxv1.PageResponse, error) {
			resp, err := c.AdminUserService().ListSessions(ctx, &leapmuxv1.ListSessionsRequest{Page: page})
			return resp.GetSessions(), resp.GetPage(), err
		},
		(*leapmuxv1.AdminSession).GetId,
		func(s *leapmuxv1.AdminSession) cli.Candidate {
			return cli.Candidate{ID: s.GetId(), Kind: "session", Title: s.GetUsername()}
		},
	)
}

type adminSessionRow struct {
	ID           string `json:"id" table:"ID,short"`
	UserID       string `json:"user_id"`
	Username     string `json:"username" table:"USERNAME"`
	UserDeleted  bool   `json:"user_deleted"`
	IPAddress    string `json:"ip_address" table:"IP"`
	UserAgent    string `json:"user_agent"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastActiveAt string `json:"last_active_at,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty" table:"EXPIRES,dim"`
}

func adminSessionRowOf(s *leapmuxv1.AdminSession) adminSessionRow {
	return adminSessionRow{
		ID:           s.GetId(),
		UserID:       s.GetUserId(),
		Username:     s.GetUsername(),
		UserDeleted:  s.GetUserDeleted(),
		IPAddress:    s.GetIpAddress(),
		UserAgent:    s.GetUserAgent(),
		CreatedAt:    formatTime(s.GetCreatedAt()),
		LastActiveAt: formatTime(s.GetLastActiveAt()),
		ExpiresAt:    formatTime(s.GetExpiresAt()),
	}
}

func adminSessionRowsOf(sessions []*leapmuxv1.AdminSession) []adminSessionRow {
	rows := make([]adminSessionRow, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, adminSessionRowOf(s))
	}
	return rows
}

// newAdminSessionList builds `admin session list`.
func newAdminSessionList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List active sessions, optionally filtered to one user",
		Examples: []cli.Example{
			{Command: "leapmux admin session list"},
			{Comment: "List one user's active sessions.", Command: "leapmux admin session list --user alice"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			owner.bind(fs)
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			ownerTarget, err := owner.parse(inv)
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			return callWithAdminUserTarget(inv.Context, c, ownerTarget, func(sel *leapmuxv1.UserSelector) (adminSessionPage, error) {
				if sel != nil {
					resp, err := c.AdminUserService().ListUserSessions(inv.Context, &leapmuxv1.ListUserSessionsRequest{
						Page: page.request(), User: sel,
					})
					if err != nil {
						return adminSessionPage{}, err
					}
					return adminSessionPage{
						Sessions: adminSessionRowsOf(resp.GetSessions()), NextCursor: resp.GetPage().GetNextCursor(),
					}, nil
				}
				resp, err := c.AdminUserService().ListSessions(inv.Context, &leapmuxv1.ListSessionsRequest{
					Page: page.request(),
				})
				if err != nil {
					return adminSessionPage{}, err
				}
				return adminSessionPage{
					Sessions: adminSessionRowsOf(resp.GetSessions()), NextCursor: resp.GetPage().GetNextCursor(),
				}, nil
			})
		},
	})
}

// newAdminSessionRevoke builds `admin session revoke`.
func newAdminSessionRevoke() *cobra.Command {
	return newAdminRevokeLeaf(adminRevokeLeafConfig{
		argName:  "session",
		short:    "Revoke a session",
		example:  "leapmux admin session revoke 4f1aC2",
		complete: completeAdminSessions,
		resolve:  resolveAdminSessionID,
		revoke: func(ctx context.Context, c *control.HubClient, id string) error {
			_, err := c.AdminUserService().RevokeSession(ctx, &leapmuxv1.RevokeSessionRequest{Id: id})
			return err
		},
	})
}

// newAdminSessionRevokeUser builds `admin session revoke-user`.
func newAdminSessionRevokeUser() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "revoke-user <user>",
		Short:            "Revoke every session of a user",
		Mutates:          true,
		Destructive:      true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin session revoke-user alice"},
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.RevokeUserSessionsResponse, error) {
				return c.AdminUserService().RevokeUserSessions(inv.Context, &leapmuxv1.RevokeUserSessionsRequest{
					User: sel,
				})
			})
			if err != nil {
				return nil, err
			}
			return struct {
				APITokensRevoked        int64 `json:"api_tokens_revoked"`
				DelegationTokensRevoked int64 `json:"delegation_tokens_revoked"`
			}{
				APITokensRevoked: resp.GetApiTokensRevoked(), DelegationTokensRevoked: resp.GetDelegationTokensRevoked(),
			}, nil
		},
	})
}

// newAdminSessionPurgeExpired builds `admin session purge-expired`.
func newAdminSessionPurgeExpired() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "purge-expired",
		Short:            "Delete every expired session",
		Mutates:          true,
		Destructive:      true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin session purge-expired"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminUserService().PurgeExpiredSessions(inv.Context, &leapmuxv1.PurgeExpiredSessionsRequest{})
			if err != nil {
				return nil, err
			}
			return adminPurgeResult{Purged: resp.GetPurged()}, nil
		},
	})
}

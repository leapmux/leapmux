package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/validate"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin <group> <command> [flags]\n\nGroups:\n  user              Manage users\n  session           Manage sessions\n  worker            Manage workers\n  oauth-provider    Manage OAuth/OIDC providers\n  encryption-key    Manage encryption keys\n  db                Database utilities")
	}

	switch args[0] {
	case "user":
		return runAdminUser(args[1:])
	case "session":
		return runAdminSession(args[1:])
	case "worker":
		return runAdminWorker(args[1:])
	case "oauth-provider":
		return runAdminOAuthProvider(args[1:])
	case "encryption-key":
		return runAdminEncryptionKey(args[1:])
	case "db":
		return runAdminDB(args[1:])
	default:
		return fmt.Errorf("unknown admin group: %s", args[0])
	}
}

// ---- User group ----

func runAdminUser(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin user <command> [flags]\n\nCommands:\n  list              List users\n  get               Get user details\n  create            Create a new user\n  update            Update user fields\n  delete            Delete a user\n  reset-password    Reset a user's password\n  grant-admin       Grant admin privileges\n  revoke-admin      Revoke admin privileges\n  list-sessions     List a user's active sessions")
	}

	switch args[0] {
	case "list":
		return runUserList(args[1:])
	case "get":
		return runUserGet(args[1:])
	case "create":
		return runUserCreate(args[1:])
	case "update":
		return runUserUpdate(args[1:])
	case "delete":
		return runUserDelete(args[1:])
	case "reset-password":
		return runUserResetPassword(args[1:])
	case "grant-admin":
		return runUserGrantAdmin(args[1:])
	case "revoke-admin":
		return runUserRevokeAdmin(args[1:])
	case "list-sessions":
		return runUserListSessions(args[1:])
	default:
		return fmt.Errorf("unknown user command: %s", args[0])
	}
}

func runUserList(args []string) error {
	var query *string
	var limit *int64
	var offset *int64
	return withAdminDB("user list", args, func(fs *flag.FlagSet) {
		query = fs.String("query", "", "search query (matches username, display name, email)")
		limit = fs.Int64("limit", 50, "maximum number of results")
		offset = fs.Int64("offset", 0, "offset for pagination")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		var users []gendb.User
		var err error

		if *query != "" {
			users, err = q.SearchUsers(ctx, gendb.SearchUsersParams{
				Query:  sql.NullString{String: *query, Valid: true},
				Limit:  *limit,
				Offset: *offset,
			})
		} else {
			users, err = q.ListAllUsers(ctx, gendb.ListAllUsersParams{
				Limit:  *limit,
				Offset: *offset,
			})
		}
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}

		if len(users) == 0 {
			fmt.Println("No users found.")
			return nil
		}

		fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n", "ID", "USERNAME", "DISPLAY_NAME", "EMAIL", "ADMIN", "CREATED")
		for _, u := range users {
			fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n",
				u.ID, u.Username, u.DisplayName, u.Email, yesNo(u.IsAdmin), timefmt.Format(u.CreatedAt))
		}
		return nil
	})
}

func runUserGet(args []string) error {
	var userID *string
	var username *string
	return withAdminDB("user get", args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		fmt.Printf("ID:              %s\n", user.ID)
		fmt.Printf("Org ID:          %s\n", user.OrgID)
		fmt.Printf("Username:        %s\n", user.Username)
		fmt.Printf("Display name:    %s\n", user.DisplayName)
		fmt.Printf("Email:           %s\n", user.Email)
		fmt.Printf("Email verified:  %s\n", yesNo(user.EmailVerified))
		fmt.Printf("Password set:    %s\n", yesNo(user.PasswordSet))
		fmt.Printf("Admin:           %s\n", yesNo(user.IsAdmin))
		fmt.Printf("Created at:      %s\n", timefmt.Format(user.CreatedAt))
		fmt.Printf("Updated at:      %s\n", timefmt.Format(user.UpdatedAt))
		return nil
	})
}

func runUserCreate(args []string) error {
	var username *string
	var pw *string
	var displayName *string
	var email *string
	var emailVerified *bool
	var admin *bool
	return withAdminDB("user create", args, func(fs *flag.FlagSet) {
		username = fs.String("username", "", "username (required)")
		pw = fs.String("password", "", "password (prompted if omitted)")
		displayName = fs.String("display-name", "", "display name")
		email = fs.String("email", "", "email address")
		emailVerified = fs.Bool("email-verified", false, "mark email as verified")
		admin = fs.Bool("admin", false, "grant admin privileges")
	}, func(ctx context.Context, _ *config.Config, sqlDB *sql.DB, q *gendb.Queries) error {
		if *username == "" {
			return fmt.Errorf("--username is required")
		}

		pwValue, err := requirePassword(*pw, "Password: ")
		if err != nil {
			return err
		}

		slug, err := validate.SanitizeSlug("username", *username)
		if err != nil {
			return err
		}

		if err := validate.ValidatePassword(pwValue); err != nil {
			return err
		}

		if *email != "" {
			if err := validate.ValidateEmail(*email); err != nil {
				return err
			}
		}

		dispName, err := validate.SanitizeDisplayName(*displayName, slug)
		if err != nil {
			return fmt.Errorf("display name: %w", err)
		}

		hash, err := password.Hash(pwValue)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		user, err := service.CreateUserWithOrg(ctx, sqlDB, q, service.CreateUserParams{
			Username:      slug,
			PasswordHash:  hash,
			DisplayName:   dispName,
			Email:         *email,
			EmailVerified: ptrconv.BoolToInt64(*emailVerified),
			PasswordSet:   1,
			IsAdmin:       ptrconv.BoolToInt64(*admin),
		})
		if err != nil {
			return friendlyConstraintError(err, slug, *email)
		}

		fmt.Printf("Created user %q (id: %s)\n", slug, user.ID)
		return nil
	})
}

func runUserUpdate(args []string) error {
	var flagSet *flag.FlagSet
	var userID *string
	var username *string
	var displayName *string
	var email *string
	var emailVerifiedFlag *bool
	return withAdminDB("user update", args, func(fs *flag.FlagSet) {
		flagSet = fs
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username (for lookup)")
		displayName = fs.String("display-name", "", "new display name")
		email = fs.String("email", "", "new email address")
		fs.Func("email-verified", "mark email as verified (true/false)", func(s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("must be 'true' or 'false'")
			}
			emailVerifiedFlag = &b
			return nil
		})
	}, func(ctx context.Context, _ *config.Config, sqlDB *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		setFlags := map[string]bool{}
		flagSet.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

		updateDisplayName := setFlags["display-name"]
		updateEmail := setFlags["email"]
		updateEmailVerified := emailVerifiedFlag != nil

		if !updateDisplayName && !updateEmail && !updateEmailVerified {
			return fmt.Errorf("no fields to update (use --display-name, --email, or --email-verified)")
		}

		// Validate inputs before starting the transaction.
		var sanitizedDisplayName string
		if updateDisplayName {
			sanitizedDisplayName, err = validate.SanitizeDisplayName(*displayName, user.Username)
			if err != nil {
				return fmt.Errorf("display name: %w", err)
			}
		}

		if updateEmail && *email != "" {
			if err := validate.ValidateEmail(*email); err != nil {
				return err
			}
		}

		// Wrap all updates in a transaction for atomicity.
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		txq := q.WithTx(tx)

		if updateDisplayName {
			if err := txq.UpdateUserProfile(ctx, gendb.UpdateUserProfileParams{
				Username:    user.Username,
				DisplayName: sanitizedDisplayName,
				ID:          user.ID,
			}); err != nil {
				return fmt.Errorf("update display name: %w", err)
			}
		}

		if updateEmail {
			verified := user.EmailVerified
			if emailVerifiedFlag != nil {
				verified = ptrconv.BoolToInt64(*emailVerifiedFlag)
			}
			if err := service.SetEmailAndClearCompeting(ctx, txq, user.ID, *email, verified); err != nil {
				return friendlyConstraintError(err, user.Username, *email)
			}
		} else if updateEmailVerified {
			if err := txq.UpdateUserEmailVerified(ctx, gendb.UpdateUserEmailVerifiedParams{
				EmailVerified: ptrconv.BoolToInt64(*emailVerifiedFlag),
				ID:            user.ID,
			}); err != nil {
				return fmt.Errorf("update email verified: %w", err)
			}
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}

		fmt.Printf("Updated user %q (id: %s)\n", user.Username, user.ID)
		return nil
	})
}

func runUserDelete(args []string) error {
	var userID *string
	var username *string
	var force *bool
	return withAdminDB("user delete", args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
		force = fs.Bool("force", false, "required to delete an admin user")
	}, func(ctx context.Context, _ *config.Config, sqlDB *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		if user.IsAdmin == 1 && !*force {
			return fmt.Errorf("user %q is an admin; pass --force to confirm deletion", user.Username)
		}

		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		txq := q.WithTx(tx)

		if err := txq.MarkAllWorkersDeletedByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("mark workers deleted: %w", err)
		}
		if err := txq.DeleteWorkerAccessGrantsByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("delete worker access grants: %w", err)
		}
		if err := txq.SoftDeleteAllWorkspacesByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("soft-delete workspaces: %w", err)
		}
		if err := txq.DeleteUserSessionsByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		if err := txq.DeleteOrgMember(ctx, gendb.DeleteOrgMemberParams{
			OrgID:  user.OrgID,
			UserID: user.ID,
		}); err != nil {
			return fmt.Errorf("delete org member: %w", err)
		}
		if err := txq.DeleteUser(ctx, user.ID); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if err := txq.ForceDeleteOrg(ctx, user.OrgID); err != nil {
			return fmt.Errorf("delete personal org: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}

		fmt.Printf("Deleted user %q (id: %s) and personal org %s\n", user.Username, user.ID, user.OrgID)
		return nil
	})
}

func runUserResetPassword(args []string) error {
	var userID *string
	var username *string
	var pw *string
	return withAdminDB("user reset-password", args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
		pw = fs.String("password", "", "new password (prompted if omitted)")
	}, func(ctx context.Context, _ *config.Config, sqlDB *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		pwValue, err := requirePassword(*pw, "New password: ")
		if err != nil {
			return err
		}

		if err := validate.ValidatePassword(pwValue); err != nil {
			return err
		}

		hash, err := password.Hash(pwValue)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		txq := q.WithTx(tx)

		if err := txq.UpdateUserPassword(ctx, gendb.UpdateUserPasswordParams{
			PasswordHash: hash,
			ID:           user.ID,
		}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}

		if err := txq.DeleteUserSessionsByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}

		fmt.Printf("Password reset for user %q (id: %s). All sessions revoked.\n", user.Username, user.ID)
		return nil
	})
}

func runUserGrantAdmin(args []string) error {
	return runUserSetAdmin(args, true)
}

func runUserRevokeAdmin(args []string) error {
	return runUserSetAdmin(args, false)
}

func runUserSetAdmin(args []string, admin bool) error {
	verb := "grant-admin"
	if !admin {
		verb = "revoke-admin"
	}
	var userID *string
	var username *string
	return withAdminDB("user "+verb, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		if err := q.UpdateUserAdmin(ctx, gendb.UpdateUserAdminParams{
			IsAdmin: ptrconv.BoolToInt64(admin),
			ID:      user.ID,
		}); err != nil {
			return fmt.Errorf("update admin: %w", err)
		}

		action := "Granted"
		if !admin {
			action = "Revoked"
		}
		fmt.Printf("%s admin privileges for user %q (id: %s)\n", action, user.Username, user.ID)
		return nil
	})
}

func runUserListSessions(args []string) error {
	var userID *string
	var username *string
	return withAdminDB("user list-sessions", args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		sessions, err := q.ListUserSessionsByUserID(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}

		if len(sessions) == 0 {
			fmt.Printf("No active sessions for user %q.\n", user.Username)
			return nil
		}

		fmt.Printf("%-48s %-24s %-24s %-24s %-16s %s\n", "ID", "CREATED", "LAST_ACTIVE", "EXPIRES", "IP", "USER_AGENT")
		for _, s := range sessions {
			fmt.Printf("%-48s %-24s %-24s %-24s %-16s %s\n",
				s.ID, timefmt.Format(s.CreatedAt), timefmt.Format(s.LastActiveAt),
				timefmt.Format(s.ExpiresAt), s.IpAddress, truncate(s.UserAgent, 60))
		}
		return nil
	})
}

// ---- Session group ----

func runAdminSession(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin session <command> [flags]\n\nCommands:\n  list              List all active sessions\n  revoke            Revoke a session by ID\n  revoke-user       Revoke all sessions for a user\n  purge-expired     Delete all expired sessions")
	}

	switch args[0] {
	case "list":
		return runSessionList(args[1:])
	case "revoke":
		return runSessionRevoke(args[1:])
	case "revoke-user":
		return runSessionRevokeUser(args[1:])
	case "purge-expired":
		return runSessionPurgeExpired(args[1:])
	default:
		return fmt.Errorf("unknown session command: %s", args[0])
	}
}

func runSessionList(args []string) error {
	var limit *int64
	var offset *int64
	return withAdminDB("session list", args, func(fs *flag.FlagSet) {
		limit = fs.Int64("limit", 50, "maximum number of results")
		offset = fs.Int64("offset", 0, "offset for pagination")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		sessions, err := q.ListAllActiveSessions(ctx, gendb.ListAllActiveSessionsParams{
			Limit:  *limit,
			Offset: *offset,
		})
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}

		if len(sessions) == 0 {
			fmt.Println("No active sessions.")
			return nil
		}

		fmt.Printf("%-48s %-48s %-20s %-24s %-24s %-16s %s\n", "ID", "USER_ID", "USERNAME", "LAST_ACTIVE", "EXPIRES", "IP", "USER_AGENT")
		for _, s := range sessions {
			fmt.Printf("%-48s %-48s %-20s %-24s %-24s %-16s %s\n",
				s.ID, s.UserID, s.Username,
				timefmt.Format(s.LastActiveAt), timefmt.Format(s.ExpiresAt),
				s.IpAddress, truncate(s.UserAgent, 60))
		}
		return nil
	})
}

func runSessionRevoke(args []string) error {
	var sessionID *string
	return withAdminDB("session revoke", args, func(fs *flag.FlagSet) {
		sessionID = fs.String("id", "", "session ID (required)")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *sessionID == "" {
			return fmt.Errorf("--id is required")
		}

		if err := q.DeleteUserSession(ctx, *sessionID); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}

		fmt.Printf("Revoked session %s\n", *sessionID)
		return nil
	})
}

func runSessionRevokeUser(args []string) error {
	var userID *string
	var username *string
	return withAdminDB("session revoke-user", args, func(fs *flag.FlagSet) {
		userID = fs.String("user-id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		user, err := resolveUser(ctx, q, *userID, *username)
		if err != nil {
			return err
		}

		if err := q.DeleteUserSessionsByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}

		fmt.Printf("Revoked all sessions for user %q (id: %s)\n", user.Username, user.ID)
		return nil
	})
}

func runSessionPurgeExpired(args []string) error {
	return withAdminDB("session purge-expired", args, nil, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		result, err := q.DeleteExpiredUserSessions(ctx)
		if err != nil {
			return fmt.Errorf("purge expired sessions: %w", err)
		}

		n, _ := result.RowsAffected()
		fmt.Printf("Purged %d expired sessions.\n", n)
		return nil
	})
}

// ---- Worker group ----

func runAdminWorker(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin worker <command> [flags]\n\nCommands:\n  list              List workers\n  get               Get worker details\n  deregister        Deregister a worker")
	}

	switch args[0] {
	case "list":
		return runWorkerList(args[1:])
	case "get":
		return runWorkerGet(args[1:])
	case "deregister":
		return runWorkerDeregister(args[1:])
	default:
		return fmt.Errorf("unknown worker command: %s", args[0])
	}
}

func runWorkerList(args []string) error {
	var userID *string
	var username *string
	var status *string
	var limit *int64
	var offset *int64
	return withAdminDB("worker list", args, func(fs *flag.FlagSet) {
		userID = fs.String("user-id", "", "filter by user ID")
		username = fs.String("username", "", "filter by username")
		status = fs.String("status", "active", "filter by status (active, deregistering, deleted, all)")
		limit = fs.Int64("limit", 50, "maximum number of results")
		offset = fs.Int64("offset", 0, "offset for pagination")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		var resolvedUserID interface{}
		if *userID != "" {
			resolvedUserID = *userID
		} else if *username != "" {
			user, err := q.GetUserByUsername(ctx, *username)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("user not found: %s", *username)
				}
				return fmt.Errorf("get user: %w", err)
			}
			resolvedUserID = user.ID
		}

		var statusFilter interface{}
		if *status != "all" {
			statusVal, err := parseWorkerStatus(*status)
			if err != nil {
				return err
			}
			statusFilter = statusVal
		}

		list, err := q.ListAllWorkersAdmin(ctx, gendb.ListAllWorkersAdminParams{
			UserID: resolvedUserID,
			Status: statusFilter,
			Offset: *offset,
			Limit:  *limit,
		})
		if err != nil {
			return fmt.Errorf("list workers: %w", err)
		}

		if len(list) == 0 {
			fmt.Println("No workers found.")
			return nil
		}

		fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n", "ID", "OWNER", "STATUS", "CREATED", "LAST_SEEN")
		for _, w := range list {
			lastSeen := "-"
			if w.LastSeenAt.Valid {
				lastSeen = timefmt.Format(w.LastSeenAt.Time)
			}
			fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n",
				w.ID, w.OwnerUsername, workerStatusString(w.Status), timefmt.Format(w.CreatedAt), lastSeen)
		}
		return nil
	})
}

func runWorkerGet(args []string) error {
	var workerID *string
	return withAdminDB("worker get", args, func(fs *flag.FlagSet) {
		workerID = fs.String("id", "", "worker ID (required)")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *workerID == "" {
			return fmt.Errorf("--id is required")
		}

		worker, err := q.GetWorkerByID(ctx, *workerID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("worker not found: %s", *workerID)
			}
			return fmt.Errorf("get worker: %w", err)
		}

		lastSeen := "-"
		if worker.LastSeenAt.Valid {
			lastSeen = timefmt.Format(worker.LastSeenAt.Time)
		}

		fmt.Printf("ID:              %s\n", worker.ID)
		fmt.Printf("Registered by:   %s\n", worker.RegisteredBy)
		fmt.Printf("Status:          %s\n", workerStatusString(worker.Status))
		fmt.Printf("Created at:      %s\n", timefmt.Format(worker.CreatedAt))
		fmt.Printf("Last seen at:    %s\n", lastSeen)

		// Show access grants.
		grants, err := q.ListWorkerAccessGrants(ctx, *workerID)
		if err != nil {
			return fmt.Errorf("list access grants: %w", err)
		}

		if len(grants) > 0 {
			fmt.Println("\nAccess grants:")
			fmt.Printf("  %-48s %-48s %-24s\n", "USER_ID", "GRANTED_BY", "CREATED")
			for _, g := range grants {
				fmt.Printf("  %-48s %-48s %-24s\n", g.UserID, g.GrantedBy, timefmt.Format(g.CreatedAt))
			}
		} else {
			fmt.Println("\nNo access grants.")
		}

		return nil
	})
}

func runWorkerDeregister(args []string) error {
	var workerID *string
	return withAdminDB("worker deregister", args, func(fs *flag.FlagSet) {
		workerID = fs.String("id", "", "worker ID (required)")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *workerID == "" {
			return fmt.Errorf("--id is required")
		}

		result, err := q.ForceDeregisterWorker(ctx, *workerID)
		if err != nil {
			return fmt.Errorf("deregister worker: %w", err)
		}

		n, _ := result.RowsAffected()
		if n == 0 {
			return fmt.Errorf("worker %s not found or not active", *workerID)
		}

		fmt.Printf("Deregistered worker %s\n", *workerID)
		return nil
	})
}

// ---- OAuth provider group ----

func runAdminOAuthProvider(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin oauth-provider <command> [flags]\n\nCommands:\n  add               Add an OAuth/OIDC provider\n  list              List configured providers\n  remove            Remove a provider\n  enable            Enable a provider\n  disable           Disable a provider")
	}

	switch args[0] {
	case "add":
		return runAddOAuthProvider(args[1:])
	case "list":
		return runListOAuthProviders(args[1:])
	case "remove":
		return runRemoveOAuthProvider(args[1:])
	case "enable":
		return runSetOAuthProviderEnabled(args[1:], true)
	case "disable":
		return runSetOAuthProviderEnabled(args[1:], false)
	default:
		return fmt.Errorf("unknown oauth-provider command: %s", args[0])
	}
}

func runAddOAuthProvider(args []string) error {
	var providerType *string
	var name *string
	var clientID *string
	var clientSecret *string
	var issuerURL *string
	var scopes *string
	var trustEmailFlag *bool
	return withAdminDB("oauth-provider add", args, func(fs *flag.FlagSet) {
		providerType = fs.String("type", "", "provider type (github, google, apple, oidc)")
		name = fs.String("name", "", "display name")
		clientID = fs.String("client-id", "", "OAuth client ID")
		clientSecret = fs.String("client-secret", "", "OAuth client secret")
		issuerURL = fs.String("issuer-url", "", "OIDC issuer URL")
		scopes = fs.String("scopes", "", "space-separated scopes")
		fs.Func("trust-email", "trust email from this provider as verified (true/false)", func(s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("must be 'true' or 'false'")
			}
			trustEmailFlag = &b
			return nil
		})
	}, func(ctx context.Context, cfg *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *providerType == "" {
			return fmt.Errorf("--type is required (github, google, apple, oidc)")
		}
		if *clientID == "" {
			return fmt.Errorf("--client-id is required")
		}
		if *clientSecret == "" {
			return fmt.Errorf("--client-secret is required")
		}

		// Apply preset defaults.
		preset, ok := oauth.Presets[*providerType]
		if !ok {
			return fmt.Errorf("unknown provider type: %s (supported: github, google, apple, oidc)", *providerType)
		}

		displayName := *name
		if displayName == "" {
			displayName = preset.Name
		}
		if displayName == "" {
			return fmt.Errorf("--name is required for generic OIDC providers")
		}

		storedType := preset.ProviderType
		issuer := *issuerURL
		if issuer == "" {
			issuer = preset.IssuerURL
		}
		scopeStr := *scopes
		if scopeStr == "" {
			scopeStr = preset.Scopes
		}

		// Resolve trust_email: explicit flag > preset default > error.
		trustEmailVal := trustEmailFlag
		if trustEmailVal == nil {
			trustEmailVal = preset.TrustEmail
		}
		if trustEmailVal == nil {
			return fmt.Errorf("--trust-email is required for generic OIDC providers (use --trust-email=true or --trust-email=false)")
		}
		trustEmail := ptrconv.BoolToInt64(*trustEmailVal)

		// Validate issuer for OIDC-based providers.
		if storedType == oauth.ProviderTypeOIDC {
			if issuer == "" {
				return fmt.Errorf("--issuer-url is required for OIDC providers")
			}
			fmt.Printf("Validating OIDC issuer %s ...\n", issuer)
			if err := oauth.ValidateIssuer(ctx, issuer); err != nil {
				return fmt.Errorf("issuer validation failed: %w", err)
			}
		}

		ks, err := keystore.LoadFromFile(cfg.EncryptionKeyFilePath())
		if err != nil {
			return fmt.Errorf("load encryption key: %w", err)
		}

		providerID := id.Generate()
		aad := keystore.ProviderAAD(providerID)
		encryptedSecret, err := ks.Encrypt([]byte(*clientSecret), aad)
		if err != nil {
			return fmt.Errorf("encrypt client secret: %w", err)
		}

		if err := q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
			ID:           providerID,
			ProviderType: storedType,
			Name:         displayName,
			IssuerUrl:    issuer,
			ClientID:     *clientID,
			ClientSecret: encryptedSecret,
			Scopes:       scopeStr,
			TrustEmail:   trustEmail,
			Enabled:      1,
		}); err != nil {
			return fmt.Errorf("create provider: %w", err)
		}

		fmt.Printf("Created OAuth provider %q (id: %s, type: %s)\n", displayName, providerID, storedType)
		return nil
	})
}

func runListOAuthProviders(args []string) error {
	return withAdminDB("oauth-provider list", args, nil, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		providers, err := q.ListAllOAuthProviders(ctx)
		if err != nil {
			return fmt.Errorf("list providers: %w", err)
		}

		if len(providers) == 0 {
			fmt.Println("No OAuth providers configured.")
			return nil
		}

		fmt.Printf("%-48s %-8s %-20s %-14s %s\n", "ID", "TYPE", "NAME", "TRUST_EMAIL", "ENABLED")
		for _, p := range providers {
			fmt.Printf("%-48s %-8s %-20s %-14s %s\n", p.ID, p.ProviderType, p.Name, yesNo(p.TrustEmail), yesNo(p.Enabled))
		}
		return nil
	})
}

func runRemoveOAuthProvider(args []string) error {
	var providerID *string
	return withAdminDB("oauth-provider remove", args, func(fs *flag.FlagSet) {
		providerID = fs.String("id", "", "provider ID")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *providerID == "" {
			return fmt.Errorf("--id is required")
		}

		provider, err := q.GetOAuthProviderByID(ctx, *providerID)
		if err != nil {
			return fmt.Errorf("get provider %s: %w", *providerID, err)
		}

		if err := q.DeleteOAuthProvider(ctx, *providerID); err != nil {
			return fmt.Errorf("delete provider: %w", err)
		}

		fmt.Printf("Removed OAuth provider %q (id: %s)\n", provider.Name, *providerID)
		return nil
	})
}

func runSetOAuthProviderEnabled(args []string, enabled bool) error {
	var providerID *string
	return withAdminDB("oauth-provider enable/disable", args, func(fs *flag.FlagSet) {
		providerID = fs.String("id", "", "provider ID")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *providerID == "" {
			return fmt.Errorf("--id is required")
		}

		if err := q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
			Enabled: ptrconv.BoolToInt64(enabled),
			ID:      *providerID,
		}); err != nil {
			return fmt.Errorf("update provider: %w", err)
		}

		action := "Disabled"
		if enabled {
			action = "Enabled"
		}
		fmt.Printf("%s OAuth provider %s\n", action, *providerID)
		return nil
	})
}

// ---- Encryption key group ----

func runAdminEncryptionKey(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin encryption-key <command> [flags]\n\nCommands:\n  rotate            Generate and add a new encryption key version\n  remove            Remove an old encryption key version\n  reencrypt         Re-encrypt all secrets with the active key")
	}

	switch args[0] {
	case "rotate":
		return runRotateEncryptionKey(args[1:])
	case "remove":
		return runRemoveEncryptionKey(args[1:])
	case "reencrypt":
		return runReencryptSecrets(args[1:])
	default:
		return fmt.Errorf("unknown encryption-key command: %s", args[0])
	}
}

func runRotateEncryptionKey(args []string) error {
	fs := flag.NewFlagSet("encryption-key rotate", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := adminConfig(*dataDir)
	path := cfg.EncryptionKeyFilePath()

	if _, err := keystore.LoadFromFile(path); err != nil {
		return fmt.Errorf("encryption key file not found at %s\nRun the hub once to auto-generate it, or specify --data-dir", path)
	}

	newVersion, err := keystore.RotateKey(path)
	if err != nil {
		return err
	}

	fmt.Printf("Added encryption key version %d.\n", newVersion)
	fmt.Printf("Restart the hub, then run: leapmux admin encryption-key reencrypt\n")
	return nil
}

func runRemoveEncryptionKey(args []string) error {
	fs := flag.NewFlagSet("encryption-key remove", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	version := fs.Uint("version", 0, "key version to remove")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *version < 1 {
		return fmt.Errorf("--version is required (must be >= 1)")
	}

	path := adminConfig(*dataDir).EncryptionKeyFilePath()
	if err := keystore.RemoveKey(path, uint32(*version)); err != nil {
		return err
	}

	fmt.Printf("Removed encryption key version %d.\n", *version)
	fmt.Printf("Restart the hub to apply.\n")
	return nil
}

func runReencryptSecrets(args []string) error {
	return withAdminDB("encryption-key reencrypt", args, nil, func(ctx context.Context, cfg *config.Config, sqlDB *sql.DB, q *gendb.Queries) error {
		ks, err := keystore.LoadFromFile(cfg.EncryptionKeyFilePath())
		if err != nil {
			return fmt.Errorf("load encryption key: %w", err)
		}

		activeVer := ks.ActiveVersion()
		count := 0

		// Re-encrypt oauth_providers.client_secret.
		providers, err := q.ListAllOAuthProvidersWithSecrets(ctx)
		if err != nil {
			return fmt.Errorf("list providers: %w", err)
		}
		for _, p := range providers {
			if ver, err := keystore.CiphertextVersion(p.ClientSecret); err == nil && ver == activeVer {
				continue // already at active version
			}
			aad := keystore.ProviderAAD(p.ID)
			plain, decErr := ks.Decrypt(p.ClientSecret, aad)
			if decErr != nil {
				return fmt.Errorf("decrypt provider %s client_secret: %w", p.ID, decErr)
			}
			newCt, encErr := ks.Encrypt(plain, aad)
			if encErr != nil {
				return fmt.Errorf("re-encrypt provider %s: %w", p.ID, encErr)
			}
			// Update via raw SQL since sqlc doesn't have an update for client_secret.
			if _, execErr := sqlDB.ExecContext(ctx, "UPDATE oauth_providers SET client_secret = ? WHERE id = ?", newCt, p.ID); execErr != nil {
				return fmt.Errorf("update provider %s: %w", p.ID, execErr)
			}
			count++
		}

		// Re-encrypt oauth_tokens.
		for _, ver := range ks.Versions() {
			if ver == activeVer {
				continue
			}
			tokens, listErr := q.ListOAuthTokensByKeyVersion(ctx, int64(ver))
			if listErr != nil {
				return fmt.Errorf("list tokens for key version %d: %w", ver, listErr)
			}
			for _, tok := range tokens {
				accessAAD := keystore.AccessTokenAAD(tok.UserID, tok.ProviderID)
				refreshAAD := keystore.RefreshTokenAAD(tok.UserID, tok.ProviderID)

				plainAccess, err := ks.Decrypt(tok.AccessToken, accessAAD)
				if err != nil {
					return fmt.Errorf("decrypt access_token for user %s: %w", tok.UserID, err)
				}
				plainRefresh, err := ks.Decrypt(tok.RefreshToken, refreshAAD)
				if err != nil {
					return fmt.Errorf("decrypt refresh_token for user %s: %w", tok.UserID, err)
				}

				newAccess, err := ks.Encrypt(plainAccess, accessAAD)
				if err != nil {
					return fmt.Errorf("re-encrypt access_token: %w", err)
				}
				newRefresh, err := ks.Encrypt(plainRefresh, refreshAAD)
				if err != nil {
					return fmt.Errorf("re-encrypt refresh_token: %w", err)
				}

				err = q.UpsertOAuthTokens(ctx, gendb.UpsertOAuthTokensParams{
					UserID:       tok.UserID,
					ProviderID:   tok.ProviderID,
					AccessToken:  newAccess,
					RefreshToken: newRefresh,
					TokenType:    tok.TokenType,
					ExpiresAt:    tok.ExpiresAt,
					KeyVersion:   int64(activeVer),
				})
				if err != nil {
					return fmt.Errorf("update tokens for user %s: %w", tok.UserID, err)
				}
				count++
			}
		}

		fmt.Printf("Re-encrypted %d secrets to key version %d.\n", count, activeVer)
		return nil
	})
}

// ---- DB group ----

func runAdminDB(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin db <command> [flags]\n\nCommands:\n  path              Print the database path\n  backup            Create a database backup")
	}

	switch args[0] {
	case "path":
		return runDBPath(args[1:])
	case "backup":
		return runDBBackup(args[1:])
	default:
		return fmt.Errorf("unknown db command: %s", args[0])
	}
}

func runDBPath(args []string) error {
	fs := flag.NewFlagSet("db path", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Println(adminConfig(*dataDir).DBPath())
	return nil
}

func runDBBackup(args []string) error {
	var output *string
	return withAdminDB("db backup", args, func(fs *flag.FlagSet) {
		output = fs.String("output", "", "output file path (required)")
	}, func(ctx context.Context, _ *config.Config, sqlDB *sql.DB, _ *gendb.Queries) error {
		if *output == "" {
			return fmt.Errorf("--output is required")
		}

		// Check that the output path doesn't already exist.
		if _, err := os.Stat(*output); err == nil {
			return fmt.Errorf("output file already exists: %s", *output)
		}

		_, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", *output)
		if err != nil {
			return fmt.Errorf("backup database: %w", err)
		}

		fmt.Printf("Database backed up to %s\n", *output)
		return nil
	})
}

// ---- Helpers ----

// withAdminDB creates a flag set with --data-dir, parses args, opens the
// database, and calls fn. The database is closed after fn returns.
func withAdminDB(name string, args []string, setup func(fs *flag.FlagSet), fn func(ctx context.Context, cfg *config.Config, sqlDB *sql.DB, q *gendb.Queries) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if setup != nil {
		setup(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := adminConfig(*dataDir)
	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	return fn(context.Background(), cfg, sqlDB, q)
}

// openAdminDB opens the database, runs migrations, and returns the connection
// and queries handle. The caller must close the returned *sql.DB.
func openAdminDB(cfg *config.Config) (*sql.DB, *gendb.Queries, error) {
	dbPath := cfg.DBPath()
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	return sqlDB, gendb.New(sqlDB), nil
}

// adminConfig returns a minimal Config with DataDir set. When dataDir is
// empty it uses the default hub data directory.
func adminConfig(dataDir string) *config.Config {
	cfg := &config.Config{}
	if dataDir != "" {
		cfg.DataDir = dataDir
	} else {
		cfg.DataDir = config.DefaultHubDataDir()
	}
	return cfg
}

// resolveUser looks up a user by ID or username. Exactly one of userID or
// username must be non-empty.
func resolveUser(ctx context.Context, q *gendb.Queries, userID, username string) (*gendb.User, error) {
	if userID == "" && username == "" {
		return nil, fmt.Errorf("--id or --username is required")
	}

	var user gendb.User
	var err error

	if userID != "" {
		user, err = q.GetUserByID(ctx, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("user not found: %s", userID)
			}
			return nil, fmt.Errorf("get user by ID: %w", err)
		}
	} else {
		user, err = q.GetUserByUsername(ctx, username)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("user not found: %s", username)
			}
			return nil, fmt.Errorf("get user by username: %w", err)
		}
	}

	return &user, nil
}

// friendlyConstraintError translates SQLite UNIQUE constraint violations
// into user-friendly messages.
func friendlyConstraintError(err error, username, email string) error {
	msg := err.Error()
	if strings.Contains(msg, "orgs.name") || strings.Contains(msg, "users.username") {
		return fmt.Errorf("username %q is already taken", username)
	}
	if strings.Contains(msg, "users.email") {
		return fmt.Errorf("email %q is already in use", email)
	}
	return err
}

// promptPassword reads a password from the terminal without echoing.
// It emits OSC 133;P to signal password input to terminals that support
// it (e.g. Ghostty), enabling credential detection features.
func promptPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !isatty.IsTerminal(uintptr(fd)) && !isatty.IsCygwinTerminal(uintptr(fd)) {
		return "", fmt.Errorf("--password is required (stdin is not a terminal)")
	}

	// OSC 133;P signals the start of password input to supporting terminals.
	fmt.Fprint(os.Stderr, "\x1b]133;P\x07")
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

// requirePassword returns the password from the flag if set, otherwise
// prompts interactively.
func requirePassword(pw string, prompt string) (string, error) {
	if pw != "" {
		return pw, nil
	}
	return promptPassword(prompt)
}

func parseWorkerStatus(s string) (leapmuxv1.WorkerStatus, error) {
	switch strings.ToLower(s) {
	case "active":
		return leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, nil
	case "deregistering":
		return leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, nil
	case "deleted":
		return leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, nil
	default:
		return 0, fmt.Errorf("unknown worker status: %s (use: active, deregistering, deleted, all)", s)
	}
}

func workerStatusString(s leapmuxv1.WorkerStatus) string {
	switch s {
	case leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
		return "active"
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING:
		return "deregistering"
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

func yesNo(v int64) string {
	if v == 1 {
		return "yes"
	}
	return "no"
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

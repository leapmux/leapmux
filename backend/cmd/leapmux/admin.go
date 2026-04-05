package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/password"
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
	fs := flag.NewFlagSet("user list", flag.ContinueOnError)
	query := fs.String("query", "", "search query (matches username, display name, email)")
	limit := fs.Int64("limit", 50, "maximum number of results")
	offset := fs.Int64("offset", 0, "offset for pagination")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	var users []gendb.User

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
		admin := "no"
		if u.IsAdmin == 1 {
			admin = "yes"
		}
		fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n",
			u.ID, u.Username, u.DisplayName, u.Email, admin, timefmt.Format(u.CreatedAt))
	}
	return nil
}

func runUserGet(args []string) error {
	fs := flag.NewFlagSet("user get", flag.ContinueOnError)
	userID := fs.String("id", "", "user ID")
	username := fs.String("username", "", "username")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	user, err := resolveUser(context.Background(), q, *userID, *username)
	if err != nil {
		return err
	}

	emailVerified := "no"
	if user.EmailVerified == 1 {
		emailVerified = "yes"
	}
	admin := "no"
	if user.IsAdmin == 1 {
		admin = "yes"
	}
	passwordSet := "no"
	if user.PasswordSet == 1 {
		passwordSet = "yes"
	}

	fmt.Printf("ID:              %s\n", user.ID)
	fmt.Printf("Org ID:          %s\n", user.OrgID)
	fmt.Printf("Username:        %s\n", user.Username)
	fmt.Printf("Display name:    %s\n", user.DisplayName)
	fmt.Printf("Email:           %s\n", user.Email)
	fmt.Printf("Email verified:  %s\n", emailVerified)
	fmt.Printf("Password set:    %s\n", passwordSet)
	fmt.Printf("Admin:           %s\n", admin)
	fmt.Printf("Created at:      %s\n", timefmt.Format(user.CreatedAt))
	fmt.Printf("Updated at:      %s\n", timefmt.Format(user.UpdatedAt))
	return nil
}

func runUserCreate(args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	username := fs.String("username", "", "username (required)")
	pw := fs.String("password", "", "password (required)")
	displayName := fs.String("display-name", "", "display name")
	email := fs.String("email", "", "email address")
	emailVerified := fs.Bool("email-verified", false, "mark email as verified")
	admin := fs.Bool("admin", false, "grant admin privileges")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *username == "" {
		return fmt.Errorf("--username is required")
	}
	if *pw == "" {
		return fmt.Errorf("--password is required")
	}

	slug, err := validate.SanitizeSlug("username", *username)
	if err != nil {
		return err
	}

	if err := validate.ValidatePassword(*pw); err != nil {
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

	hash, err := password.Hash(*pw)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	cfg := adminConfig(*dataDir)
	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()

	if _, lookupErr := q.GetUserByUsername(ctx, slug); lookupErr == nil {
		return fmt.Errorf("username %q is already taken", slug)
	} else if lookupErr != sql.ErrNoRows {
		return fmt.Errorf("check username: %w", lookupErr)
	}

	if *email != "" {
		if _, lookupErr := q.GetUserByEmail(ctx, *email); lookupErr == nil {
			return fmt.Errorf("email %q is already in use", *email)
		} else if lookupErr != sql.ErrNoRows {
			return fmt.Errorf("check email: %w", lookupErr)
		}
	}

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txq := q.WithTx(tx)

	orgID := id.Generate()
	if err := txq.CreateOrg(ctx, gendb.CreateOrgParams{
		ID:         orgID,
		Name:       slug,
		IsPersonal: 1,
	}); err != nil {
		return fmt.Errorf("create org: %w", err)
	}

	userID := id.Generate()
	if err := txq.CreateUser(ctx, gendb.CreateUserParams{
		ID:            userID,
		OrgID:         orgID,
		Username:      slug,
		PasswordHash:  hash,
		DisplayName:   dispName,
		Email:         *email,
		EmailVerified: ptrconv.BoolToInt64(*emailVerified),
		PasswordSet:   1,
		IsAdmin:       ptrconv.BoolToInt64(*admin),
	}); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	if err := txq.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return fmt.Errorf("create org member: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	fmt.Printf("Created user %q (id: %s)\n", slug, userID)
	return nil
}

func runUserUpdate(args []string) error {
	fs := flag.NewFlagSet("user update", flag.ContinueOnError)
	userID := fs.String("id", "", "user ID")
	username := fs.String("username", "", "username (for lookup)")
	displayName := fs.String("display-name", "", "new display name")
	email := fs.String("email", "", "new email address")
	var emailVerifiedFlag *bool
	fs.Func("email-verified", "mark email as verified (true/false)", func(s string) error {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("must be 'true' or 'false'")
		}
		emailVerifiedFlag = &b
		return nil
	})
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	user, err := resolveUser(ctx, q, *userID, *username)
	if err != nil {
		return err
	}

	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	updated := false

	if setFlags["display-name"] {
		dn, err := validate.SanitizeDisplayName(*displayName, user.Username)
		if err != nil {
			return fmt.Errorf("display name: %w", err)
		}
		if err := q.UpdateUserProfile(ctx, gendb.UpdateUserProfileParams{
			Username:    user.Username,
			DisplayName: dn,
			ID:          user.ID,
		}); err != nil {
			return fmt.Errorf("update display name: %w", err)
		}
		updated = true
	}

	if setFlags["email"] {
		if *email != "" {
			if err := validate.ValidateEmail(*email); err != nil {
				return err
			}
			if *email != user.Email {
				if existing, lookupErr := q.GetUserByEmail(ctx, *email); lookupErr == nil && existing.ID != user.ID {
					return fmt.Errorf("email %q is already in use", *email)
				}
			}
		}
		verified := user.EmailVerified
		if emailVerifiedFlag != nil {
			verified = ptrconv.BoolToInt64(*emailVerifiedFlag)
		}
		if err := q.UpdateUserEmail(ctx, gendb.UpdateUserEmailParams{
			Email:         *email,
			EmailVerified: verified,
			ID:            user.ID,
		}); err != nil {
			return fmt.Errorf("update email: %w", err)
		}
		updated = true
	} else if emailVerifiedFlag != nil {
		if err := q.UpdateUserEmailVerified(ctx, gendb.UpdateUserEmailVerifiedParams{
			EmailVerified: ptrconv.BoolToInt64(*emailVerifiedFlag),
			ID:            user.ID,
		}); err != nil {
			return fmt.Errorf("update email verified: %w", err)
		}
		updated = true
	}

	if !updated {
		return fmt.Errorf("no fields to update (use --display-name, --email, or --email-verified)")
	}

	fmt.Printf("Updated user %q (id: %s)\n", user.Username, user.ID)
	return nil
}

func runUserDelete(args []string) error {
	fs := flag.NewFlagSet("user delete", flag.ContinueOnError)
	userID := fs.String("id", "", "user ID")
	username := fs.String("username", "", "username")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	user, err := resolveUser(ctx, q, *userID, *username)
	if err != nil {
		return err
	}

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txq := q.WithTx(tx)

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
}

func runUserResetPassword(args []string) error {
	fs := flag.NewFlagSet("user reset-password", flag.ContinueOnError)
	userID := fs.String("id", "", "user ID")
	username := fs.String("username", "", "username")
	pw := fs.String("password", "", "new password (required)")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *pw == "" {
		return fmt.Errorf("--password is required")
	}

	if err := validate.ValidatePassword(*pw); err != nil {
		return err
	}

	hash, err := password.Hash(*pw)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	user, err := resolveUser(ctx, q, *userID, *username)
	if err != nil {
		return err
	}

	if err := q.UpdateUserPassword(ctx, gendb.UpdateUserPasswordParams{
		PasswordHash: hash,
		ID:           user.ID,
	}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if err := q.DeleteUserSessionsByUser(ctx, user.ID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}

	fmt.Printf("Password reset for user %q (id: %s). All sessions revoked.\n", user.Username, user.ID)
	return nil
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
	fs := flag.NewFlagSet("user "+verb, flag.ContinueOnError)
	userID := fs.String("id", "", "user ID")
	username := fs.String("username", "", "username")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
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
}

func runUserListSessions(args []string) error {
	fs := flag.NewFlagSet("user list-sessions", flag.ContinueOnError)
	userID := fs.String("id", "", "user ID")
	username := fs.String("username", "", "username")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
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
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	limit := fs.Int64("limit", 50, "maximum number of results")
	offset := fs.Int64("offset", 0, "offset for pagination")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	sessions, err := q.ListAllActiveSessions(context.Background(), gendb.ListAllActiveSessionsParams{
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
}

func runSessionRevoke(args []string) error {
	fs := flag.NewFlagSet("session revoke", flag.ContinueOnError)
	sessionID := fs.String("id", "", "session ID (required)")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *sessionID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := q.DeleteUserSession(context.Background(), *sessionID); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	fmt.Printf("Revoked session %s\n", *sessionID)
	return nil
}

func runSessionRevokeUser(args []string) error {
	fs := flag.NewFlagSet("session revoke-user", flag.ContinueOnError)
	userID := fs.String("user-id", "", "user ID")
	username := fs.String("username", "", "username")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	user, err := resolveUser(ctx, q, *userID, *username)
	if err != nil {
		return err
	}

	if err := q.DeleteUserSessionsByUser(ctx, user.ID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}

	fmt.Printf("Revoked all sessions for user %q (id: %s)\n", user.Username, user.ID)
	return nil
}

func runSessionPurgeExpired(args []string) error {
	fs := flag.NewFlagSet("session purge-expired", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	result, err := q.DeleteExpiredUserSessions(context.Background())
	if err != nil {
		return fmt.Errorf("purge expired sessions: %w", err)
	}

	n, _ := result.RowsAffected()
	fmt.Printf("Purged %d expired sessions.\n", n)
	return nil
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
	fs := flag.NewFlagSet("worker list", flag.ContinueOnError)
	userID := fs.String("user-id", "", "filter by user ID")
	username := fs.String("username", "", "filter by username")
	status := fs.String("status", "active", "filter by status (active, deregistering, deleted, all)")
	limit := fs.Int64("limit", 50, "maximum number of results")
	offset := fs.Int64("offset", 0, "offset for pagination")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()

	resolvedUserID := *userID
	if *username != "" && resolvedUserID == "" {
		user, err := q.GetUserByUsername(ctx, *username)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("user not found: %s", *username)
			}
			return fmt.Errorf("get user: %w", err)
		}
		resolvedUserID = user.ID
	}

	allStatuses := *status == "all"
	var statusVal leapmuxv1.WorkerStatus
	if !allStatuses {
		statusVal, err = parseWorkerStatus(*status)
		if err != nil {
			return err
		}
	}

	type workerRow struct {
		ID            string
		OwnerUsername string
		Status        leapmuxv1.WorkerStatus
		CreatedAt     time.Time
		LastSeenAt    sql.NullTime
	}

	toRow := func(id, owner string, st leapmuxv1.WorkerStatus, created time.Time, lastSeen sql.NullTime) workerRow {
		return workerRow{id, owner, st, created, lastSeen}
	}

	var rows []workerRow

	switch {
	case resolvedUserID != "" && allStatuses:
		list, qErr := q.ListAllWorkersByUserAnyStatus(ctx, gendb.ListAllWorkersByUserAnyStatusParams{
			UserID: resolvedUserID, Offset: *offset, Limit: *limit,
		})
		if qErr != nil {
			return fmt.Errorf("list workers: %w", qErr)
		}
		for _, w := range list {
			rows = append(rows, toRow(w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt))
		}
	case resolvedUserID != "":
		list, qErr := q.ListAllWorkersByUser(ctx, gendb.ListAllWorkersByUserParams{
			UserID: resolvedUserID, Status: statusVal, Offset: *offset, Limit: *limit,
		})
		if qErr != nil {
			return fmt.Errorf("list workers: %w", qErr)
		}
		for _, w := range list {
			rows = append(rows, toRow(w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt))
		}
	case allStatuses:
		list, qErr := q.ListAllWorkersAnyStatus(ctx, gendb.ListAllWorkersAnyStatusParams{
			Offset: *offset, Limit: *limit,
		})
		if qErr != nil {
			return fmt.Errorf("list workers: %w", qErr)
		}
		for _, w := range list {
			rows = append(rows, toRow(w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt))
		}
	default:
		list, qErr := q.ListAllWorkers(ctx, gendb.ListAllWorkersParams{
			Status: statusVal, Offset: *offset, Limit: *limit,
		})
		if qErr != nil {
			return fmt.Errorf("list workers: %w", qErr)
		}
		for _, w := range list {
			rows = append(rows, toRow(w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt))
		}
	}

	if len(rows) == 0 {
		fmt.Println("No workers found.")
		return nil
	}

	fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n", "ID", "OWNER", "STATUS", "CREATED", "LAST_SEEN")
	for _, w := range rows {
		lastSeen := "-"
		if w.LastSeenAt.Valid {
			lastSeen = timefmt.Format(w.LastSeenAt.Time)
		}
		fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n",
			w.ID, w.OwnerUsername, workerStatusString(w.Status), timefmt.Format(w.CreatedAt), lastSeen)
	}
	return nil
}

func runWorkerGet(args []string) error {
	fs := flag.NewFlagSet("worker get", flag.ContinueOnError)
	workerID := fs.String("id", "", "worker ID (required)")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *workerID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	worker, err := q.GetWorkerByID(ctx, *workerID)
	if err != nil {
		if err == sql.ErrNoRows {
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
}

func runWorkerDeregister(args []string) error {
	fs := flag.NewFlagSet("worker deregister", flag.ContinueOnError)
	workerID := fs.String("id", "", "worker ID (required)")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *workerID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	result, err := q.ForceDeregisterWorker(context.Background(), *workerID)
	if err != nil {
		return fmt.Errorf("deregister worker: %w", err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker %s not found or not active", *workerID)
	}

	fmt.Printf("Deregistered worker %s\n", *workerID)
	return nil
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
	fs := flag.NewFlagSet("oauth-provider add", flag.ContinueOnError)
	providerType := fs.String("type", "", "provider type (github, google, apple, oidc)")
	name := fs.String("name", "", "display name")
	clientID := fs.String("client-id", "", "OAuth client ID")
	clientSecret := fs.String("client-secret", "", "OAuth client secret")
	issuerURL := fs.String("issuer-url", "", "OIDC issuer URL")
	scopes := fs.String("scopes", "", "space-separated scopes")
	var trustEmailFlag *bool
	fs.Func("trust-email", "trust email from this provider as verified (true/false)", func(s string) error {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("must be 'true' or 'false'")
		}
		trustEmailFlag = &b
		return nil
	})
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

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
		if err := oauth.ValidateIssuer(context.Background(), issuer); err != nil {
			return fmt.Errorf("issuer validation failed: %w", err)
		}
	}

	cfg := adminConfig(*dataDir)

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

	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
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
}

func runListOAuthProviders(args []string) error {
	fs := flag.NewFlagSet("oauth-provider list", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	providers, err := q.ListAllOAuthProviders(context.Background())
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}

	if len(providers) == 0 {
		fmt.Println("No OAuth providers configured.")
		return nil
	}

	fmt.Printf("%-48s %-8s %-20s %-14s %s\n", "ID", "TYPE", "NAME", "TRUST_EMAIL", "ENABLED")
	for _, p := range providers {
		trustEmail := "yes"
		if p.TrustEmail != 1 {
			trustEmail = "no"
		}
		enabled := "yes"
		if p.Enabled != 1 {
			enabled = "no"
		}
		fmt.Printf("%-48s %-8s %-20s %-14s %s\n", p.ID, p.ProviderType, p.Name, trustEmail, enabled)
	}
	return nil
}

func runRemoveOAuthProvider(args []string) error {
	fs := flag.NewFlagSet("oauth-provider remove", flag.ContinueOnError)
	providerID := fs.String("id", "", "provider ID")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *providerID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	provider, err := q.GetOAuthProviderByID(context.Background(), *providerID)
	if err != nil {
		return fmt.Errorf("get provider %s: %w", *providerID, err)
	}

	if err := q.DeleteOAuthProvider(context.Background(), *providerID); err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}

	fmt.Printf("Removed OAuth provider %q (id: %s)\n", provider.Name, *providerID)
	return nil
}

func runSetOAuthProviderEnabled(args []string, enabled bool) error {
	fs := flag.NewFlagSet("oauth-provider enable/disable", flag.ContinueOnError)
	providerID := fs.String("id", "", "provider ID")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *providerID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := q.UpdateOAuthProviderEnabled(context.Background(), gendb.UpdateOAuthProviderEnabledParams{
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
	fs := flag.NewFlagSet("encryption-key reencrypt", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := adminConfig(*dataDir)
	ks, err := keystore.LoadFromFile(cfg.EncryptionKeyFilePath())
	if err != nil {
		return fmt.Errorf("load encryption key: %w", err)
	}

	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
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
	fs := flag.NewFlagSet("db backup", flag.ContinueOnError)
	output := fs.String("output", "", "output file path (required)")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *output == "" {
		return fmt.Errorf("--output is required")
	}

	cfg := adminConfig(*dataDir)
	sqlDB, _, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	// Check that the output path doesn't already exist.
	if _, err := os.Stat(*output); err == nil {
		return fmt.Errorf("output file already exists: %s", *output)
	}

	_, err = sqlDB.ExecContext(context.Background(), "VACUUM INTO ?", *output)
	if err != nil {
		return fmt.Errorf("backup database: %w", err)
	}

	fmt.Printf("Database backed up to %s\n", *output)
	return nil
}

// ---- Helpers ----

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
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("user not found: %s", userID)
			}
			return nil, fmt.Errorf("get user by ID: %w", err)
		}
	} else {
		user, err = q.GetUserByUsername(ctx, username)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("user not found: %s", username)
			}
			return nil, fmt.Errorf("get user by username: %w", err)
		}
	}

	return &user, nil
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

// workerStatusString converts a worker status to a human-readable string.
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

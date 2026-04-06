package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

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

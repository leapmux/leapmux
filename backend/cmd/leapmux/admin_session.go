package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func runSessionList(cmd adminCmdCtx, args []string) error {
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		limit, cursor = addListFlags(fs)
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if err := validateListLimit(*limit); err != nil {
			return err
		}
		page, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Cursor: *cursor, Limit: *limit},
		})
		if err != nil {
			return classifyListError("list sessions", err)
		}

		if len(page.Rows) == 0 {
			fmt.Println("No active sessions.")
			return nil
		}

		fmt.Printf("%-48s %-48s %-20s %-24s %-24s %-16s %s\n", "ID", "USER_ID", "USERNAME", "LAST_ACTIVE", "EXPIRES", "IP", "USER_AGENT")
		for _, s := range page.Rows {
			fmt.Printf("%-48s %-48s %-20s %-24s %-24s %-16s %s\n",
				s.ID, s.UserID, ownerLabel(s.Username, s.UserDeleted),
				timefmt.Format(s.LastActiveAt), timefmt.Format(s.ExpiresAt),
				s.IPAddress, truncate(s.UserAgent, 60))
		}

		maybePrintNextCursor(page)
		return nil
	})
}

func runSessionRevoke(cmd adminCmdCtx, args []string) error {
	var sessionID *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		sessionID = fs.String("id", "", "session ID (required)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if *sessionID == "" {
			return fmt.Errorf("--id is required")
		}

		n, err := st.Sessions().Delete(ctx, *sessionID)
		if err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("session not found: %s", *sessionID)
		}

		fmt.Printf("Revoked session %s\n", *sessionID)
		return nil
	})
}

func runSessionRevokeUser(cmd adminCmdCtx, args []string) error {
	var userID *string
	var username *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("user-id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		// Mint once and refuse a blank id rather than letting it address every
		// blank-owner row in the bulk operations below.
		revokeUID, err := mintResolvedUserID(user)
		if err != nil {
			return err
		}

		var apiCount, delegationCount int64
		err = st.RunInUserAuthTransaction(ctx, revokeUID, func(tx store.Store) error {
			if err := tx.Sessions().DeleteByUser(ctx, revokeUID); err != nil {
				return fmt.Errorf("delete sessions: %w", err)
			}
			// "Revoke all sessions" is the canonical
			// "kill every active credential for this user" lever.
			// Spawned agents holding delegation bearers and CLI
			// instances holding api_tokens count as active
			// credentials too, so revoke both alongside the
			// session purge. The store records durable revocation
			// events in this transaction so the hub's revocation
			// watcher picks this up cross-process and fires
			// CloseChannelsByUserRevocation on its next sweep.
			var err error
			apiCount, delegationCount, err = auth.RevokeAllUserCredentials(ctx, tx, revokeUID)
			return err
		})
		if err != nil {
			return err
		}

		fmt.Printf("Revoked all sessions for user %q (id: %s); %d api token(s) and %d delegation token(s) also revoked\n", user.Username, user.ID, apiCount, delegationCount)
		return nil
	})
}

func runSessionPurgeExpired(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, _ *config.Config, st store.Store) error {
		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx)
		if err != nil {
			return fmt.Errorf("purge expired sessions: %w", err)
		}

		fmt.Printf("Purged %d expired sessions.\n", n)
		return nil
	})
}

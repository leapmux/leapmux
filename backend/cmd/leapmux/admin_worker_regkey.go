package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func runWorkerRegKeyList(cmd adminCmdCtx, args []string) error {
	var includeExpired *bool
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		includeExpired = fs.Bool("include-expired", false, "include revoked or expired keys (forensics; default shows only live keys)")
		limit, cursor = addListFlags(fs)
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if err := validateListLimit(*limit); err != nil {
			return err
		}
		page, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			PageParams:     store.PageParams{Cursor: *cursor, Limit: *limit},
			IncludeExpired: *includeExpired,
		})
		if err != nil {
			return classifyListError("list registration keys", err)
		}

		if len(page.Rows) == 0 {
			fmt.Println("No registration keys.")
			return nil
		}

		fmt.Printf("%-32s %-20s %-24s %-24s\n", "ID", "CREATED_BY", "CREATED", "EXPIRES")
		for _, r := range page.Rows {
			fmt.Printf("%-32s %-20s %-24s %-24s\n",
				r.ID, ownerLabel(r.CreatorUsername, r.CreatorDeleted), timefmt.Format(r.CreatedAt), timefmt.Format(r.ExpiresAt))
		}

		maybePrintNextCursor(page)
		return nil
	})
}

func runWorkerRegKeyRevoke(cmd adminCmdCtx, args []string) error {
	var id *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		id = fs.String("id", "", "registration key ID (required)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if *id == "" {
			return fmt.Errorf("--id is required")
		}

		n, err := st.RegistrationKeys().AdminSoftDelete(ctx, *id)
		if err != nil {
			return fmt.Errorf("revoke registration key: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("registration key not found: %s", *id)
		}

		fmt.Printf("Revoked registration key %s\n", *id)
		return nil
	})
}

func runWorkerRegKeyPurgeExpired(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, _ *config.Config, st store.Store) error {
		// Cleanup loop uses cleanupRetention (7d) before hard-deleting, but
		// admin "purge now" wants to drop everything that is no longer live —
		// pass time.Now so any row whose expires_at is in the past goes.
		//
		// The underlying query batches 1000 rows per call so the cleanup
		// goroutine doesn't hold a long write lock; loop until a batch
		// returns less than that to drain the full backlog.
		cutoff := time.Now().UTC()
		var total int64
		for {
			n, err := st.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff)
			if err != nil {
				return fmt.Errorf("purge expired registration keys: %w", err)
			}
			total += n
			if n < 1000 {
				break
			}
		}

		fmt.Printf("Purged %d expired registration keys.\n", total)
		return nil
	})
}

package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// runDelegationTokenList prints delegation tokens through the single
// keyset-paginated store-level ListAll (a JOINed query carrying each row's
// owner username). --user-id/--username narrows it to one user via the ByUser
// query twin, with identical --limit/--cursor behavior on both paths.
func runDelegationTokenList(cmd adminCmdCtx, args []string) error {
	var userID, username string
	var includeRevoked bool
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&userID, "user-id", "", "filter by user ID (soft-deleted users included; empty = all users)")
		fs.StringVar(&username, "username", "", "filter by username")
		fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		limit, cursor = addListFlags(fs)
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if err := validateListLimit(*limit); err != nil {
			return err
		}
		userFilter, err := resolveUserFilter(ctx, st, userID, username)
		if err != nil {
			return err
		}
		page, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			UserID:         userFilter,
			PageParams:     store.PageParams{Cursor: *cursor, Limit: *limit},
			IncludeRevoked: includeRevoked,
		})
		if err != nil {
			return classifyListError("list delegation tokens", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tUSER\tWORKER\tWORKSPACE\tAGENT\tTERMINAL\tCREATED\tEXPIRES\tREVOKED")
		for _, t := range page.Rows {
			writeDelegationTokenRow(w, t.DelegationToken, ownerLabel(t.OwnerUsername, t.OwnerDeleted))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		maybePrintNextCursor(page)
		return nil
	})
}

func writeDelegationTokenRow(w *tabwriter.Writer, t store.DelegationToken, userLabel string) {
	revoked := "-"
	if t.RevokedAt != nil {
		revoked = timefmt.Format(*t.RevokedAt)
	}
	_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		t.ID, userLabel, t.WorkerID, t.WorkspaceID,
		cmp.Or(t.AgentID, "-"), cmp.Or(t.TerminalID, "-"),
		timefmt.Format(t.CreatedAt), timefmt.Format(t.ExpiresAt), revoked)
}

// runDelegationTokenRevoke marks a delegation_tokens row revoked.
//
// Note: this command runs in a separate process from the live hub and
// only mutates the database row. The store records a durable revocation
// event in the same transaction; the hub's revocation watcher publishes
// and consumes that stream (default cadence: every 2s, see
// internal/hub/revocationwatcher) and drives AuthContextRegistry.EvictBearer +
// ChannelCloser.CloseChannelsByBearer for newly-revoked rows. The
// minting worker can additionally call its own
// /worker/delegation-tokens/revoke endpoint for zero-latency
// in-process eviction; both paths converge on the same close
// machinery.
func runDelegationTokenRevoke(cmd adminCmdCtx, args []string) error {
	var tokenID string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&tokenID, "id", "", "token id (required)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if tokenID == "" {
			return fmt.Errorf("--id is required")
		}
		rows, err := st.DelegationTokens().Revoke(ctx, tokenID)
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("token %s not found or already revoked", tokenID)
		}
		fmt.Printf("Revoked delegation_token %s\n", tokenID)
		fmt.Println("note: hub revocation watcher will pick this up on its next sweep (default ~2s)")
		return nil
	})
}

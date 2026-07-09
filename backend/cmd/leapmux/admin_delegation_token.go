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

func runDelegationTokenList(cmd adminCmdCtx, args []string) error {
	var userID string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&userID, "user", "", "user id (empty = all users)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		rows, err := collectAcrossUsers(ctx, st, userID, func(uid string) ([]store.DelegationToken, error) {
			return st.DelegationTokens().ListByUser(ctx, uid)
		})
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tUSER\tWORKER\tWORKSPACE\tAGENT\tTERMINAL\tCREATED\tEXPIRES")
		for _, r := range rows {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.UserID, r.WorkerID, r.WorkspaceID,
				cmp.Or(r.AgentID, "-"), cmp.Or(r.TerminalID, "-"),
				timefmt.Format(r.CreatedAt), timefmt.Format(r.ExpiresAt))
		}
		return w.Flush()
	})
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

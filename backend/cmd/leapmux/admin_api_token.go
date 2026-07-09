package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// runAPITokenList prints all api_tokens for a user (or all users when
// --user is empty).
func runAPITokenList(cmd adminCmdCtx, args []string) error {
	var userID string
	var clientType string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&userID, "user", "", "user id (empty = all users)")
		fs.StringVar(&clientType, "client-type", "", "filter by client type (empty = all)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		rows, err := collectAcrossUsers(ctx, st, userID, func(uid string) ([]store.APIToken, error) {
			return st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
				UserID:     uid,
				ClientType: clientType,
			})
		})
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tUSER\tTYPE\tNAME\tCREATED\tLAST USED\tEXPIRES")
		for _, r := range rows {
			lastUsed := "-"
			if r.LastUsedAt != nil {
				lastUsed = timefmt.Format(*r.LastUsedAt)
			}
			expires := "-"
			if r.ExpiresAt != nil {
				expires = timefmt.Format(*r.ExpiresAt)
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.UserID, r.ClientType, r.ClientName,
				timefmt.Format(r.CreatedAt), lastUsed, expires)
		}
		return w.Flush()
	})
}

// runAPITokenIssue mints a new token for the named user. This is the
// service-account / headless-host path. The mint emits the bearer to
// stdout exactly once — there's no second chance to retrieve it.
func runAPITokenIssue(cmd adminCmdCtx, args []string) error {
	var userID, clientType, clientName string
	var ttlSeconds int64
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&userID, "user", "", "user id (required)")
		fs.StringVar(&clientType, "client-type", "cli", "client type (cli|integration|...)")
		fs.StringVar(&clientName, "client-name", "", "human-visible client name (required)")
		fs.Int64Var(&ttlSeconds, "ttl", 0, "access-token TTL seconds (0 = default 1h)")
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if userID == "" || clientName == "" {
			return fmt.Errorf("--user and --client-name are required")
		}

		ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
		if err != nil {
			return fmt.Errorf("load keystore: %w", err)
		}
		pepper := ks.Pepper()
		validator, err := auth.NewTokenValidator(st, pepper[:])
		if err != nil {
			return err
		}

		tokenID := id.Generate()
		now := time.Now()
		ttl := time.Duration(ttlSeconds) * time.Second
		if ttl <= 0 {
			ttl = auth.AccessTokenTTL
		}
		pair := validator.MintBearerPair(auth.BearerKindAPI, tokenID, now, ttl, auth.RefreshTokenTTL)
		if err := st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           userID,
			ClientType:       clientType,
			ClientName:       clientName,
			SecretHash:       pair.AccessHash,
			RefreshHash:      pair.RefreshHash,
			Scope:            "remote:*",
			ExpiresAt:        &pair.AccessExpiresAt,
			RefreshExpiresAt: &pair.RefreshExpiresAt,
		}); err != nil {
			return fmt.Errorf("create token: %w", err)
		}
		fmt.Println("Token minted. Capture it now — it cannot be retrieved later:")
		fmt.Println()
		fmt.Println("  access_token  =", pair.AccessBearer)
		fmt.Println("  refresh_token =", pair.RefreshBearer)
		fmt.Println("  token_id      =", tokenID)
		return nil
	})
}

// runAPITokenRevoke marks a token row revoked.
//
// The store records a durable revocation event in the same transaction.
// A hub running anywhere — same machine as this admin command or remote —
// publishes and consumes that event within the watcher's sweep interval
// (default 2s), then fires EvictBearer + CloseChannelsByBearer without
// an IPC or `--hub <url>` round-trip.
func runAPITokenRevoke(cmd adminCmdCtx, args []string) error {
	var tokenID string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&tokenID, "id", "", "token id (required)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if tokenID == "" {
			return fmt.Errorf("--id is required")
		}
		rows, err := st.APITokens().Revoke(ctx, tokenID)
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("token %s not found or already revoked", tokenID)
		}
		fmt.Printf("Revoked api_token %s\n", tokenID)
		fmt.Println("note: a running hub will evict the bearer cache and close any open channels authenticated by this token within its revocation-watcher sweep interval (default 2s)")
		return nil
	})
}

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

// runAPITokenList prints api_tokens through the single keyset-paginated
// store-level ListAll (a JOINed query carrying each row's owner username).
// --user-id/--username narrows it to one user via the ByUser query twin, with
// identical --limit/--cursor behavior on both paths.
func runAPITokenList(cmd adminCmdCtx, args []string) error {
	var userID, username string
	var clientType string
	var includeRevoked bool
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&userID, "user-id", "", "filter by user ID (soft-deleted users included; empty = all users)")
		fs.StringVar(&username, "username", "", "filter by username")
		fs.StringVar(&clientType, "client-type", "", "filter by client type (empty = all)")
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
		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			UserID:         userFilter,
			ClientType:     clientType,
			PageParams:     store.PageParams{Cursor: *cursor, Limit: *limit},
			IncludeRevoked: includeRevoked,
		})
		if err != nil {
			return classifyListError("list api tokens", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tUSER\tTYPE\tNAME\tCREATED\tLAST USED\tEXPIRES\tREVOKED")
		for _, t := range page.Rows {
			writeAPITokenRow(w, t.APIToken, ownerLabel(t.OwnerUsername, t.OwnerDeleted))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		maybePrintNextCursor(page)
		return nil
	})
}

func writeAPITokenRow(w *tabwriter.Writer, t store.APIToken, userLabel string) {
	lastUsed := "-"
	if t.LastUsedAt != nil {
		lastUsed = timefmt.Format(*t.LastUsedAt)
	}
	expires := "-"
	if t.ExpiresAt != nil {
		expires = timefmt.Format(*t.ExpiresAt)
	}
	revoked := "-"
	if t.RevokedAt != nil {
		revoked = timefmt.Format(*t.RevokedAt)
	}
	_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		t.ID, userLabel, t.ClientType, t.ClientName,
		timefmt.Format(t.CreatedAt), lastUsed, expires, revoked)
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

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
)

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

package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func runRotateEncryptionKey(cmd cmdCtx, args []string) error {
	return withRecoverConfig(cmd, args, nil, func(cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()

		if _, err := keystore.LoadFromFile(path); err != nil {
			return fmt.Errorf("encryption key file not found at %s\nRun the hub once to auto-generate it, or specify --data-dir", path)
		}

		newVersion, err := keystore.RotateKey(path)
		if err != nil {
			return err
		}

		fmt.Printf("Added encryption key version %d.\n", newVersion)
		fmt.Printf("Restart the hub, then run: leapmux recover encryption-key reencrypt\n")
		return nil
	})
}

func runRemoveEncryptionKey(cmd cmdCtx, args []string) error {
	var version *uint
	return withRecoverStore(cmd, args, func(fs *flag.FlagSet) {
		version = fs.Uint("version", 0, "key version to remove")
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if *version < 1 {
			return fmt.Errorf("--version is required (must be >= 1)")
		}

		path := cfg.EncryptionKeyFilePath()
		ks, err := keystore.LoadFromFile(path)
		if err != nil {
			return fmt.Errorf("encryption key file not found at %s\nRun the hub once to auto-generate it, or specify --data-dir", path)
		}
		v := uint32(*version)
		if v == ks.ActiveVersion() {
			return fmt.Errorf("cannot remove active key version %d", v)
		}

		// Guard: refuse to remove a version that still encrypts data.
		// Removing it would permanently brick those secrets (decrypt fails
		// with "unknown key version"). The operator must run reencrypt first
		// to migrate them onto the active key.
		refs, err := countEncryptedRefs(ctx, st, v)
		if err != nil {
			return err
		}
		if len(refs) > 0 {
			return fmt.Errorf("encryption key version %d still encrypts %s; run 'leapmux recover encryption-key reencrypt' first (after restarting the hub on the rotated key)", v, strings.Join(refs, " and "))
		}

		if err := keystore.RemoveKey(path, v); err != nil {
			return err
		}

		fmt.Printf("Removed encryption key version %d.\n", v)
		fmt.Printf("Restart the hub to apply.\n")
		return nil
	})
}

// countEncryptedRefs returns human-readable descriptions of data still
// encrypted under the given key version. An empty result means the version is
// safe to remove. It scans the persistent encrypted secrets — OAuth provider
// client secrets, user OAuth tokens, and captcha provider secrets. Transient
// pending_oauth_signups are not scanned: they auto-expire, are never migrated
// by reencrypt, and a bricked one only fails an in-flight signup the user can
// simply retry.
func countEncryptedRefs(ctx context.Context, st store.Store, version uint32) ([]string, error) {
	var refs []string

	tokens, err := st.OAuthTokens().CountByKeyVersion(ctx, int64(version))
	if err != nil {
		return nil, fmt.Errorf("count oauth tokens for key version %d: %w", version, err)
	}
	if tokens > 0 {
		refs = append(refs, fmt.Sprintf("%d OAuth token(s)", tokens))
	}

	providers, err := st.OAuthProviders().ListAllWithSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list oauth providers: %w", err)
	}
	var providerCount int
	for _, p := range providers {
		if ver, e := keystore.CiphertextVersion(p.ClientSecret); e == nil && ver == version {
			providerCount++
		}
	}
	if providerCount > 0 {
		refs = append(refs, fmt.Sprintf("%d OAuth provider secret(s)", providerCount))
	}

	settingRows, err := st.Settings().GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	var settingsCount int
	for _, row := range settingRows {
		if len(row.Secret) == 0 {
			continue
		}
		if ver, e := keystore.CiphertextVersion(row.Secret); e == nil && ver == version {
			settingsCount++
		}
	}
	if settingsCount > 0 {
		refs = append(refs, fmt.Sprintf("%d settings secret(s)", settingsCount))
	}

	return refs, nil
}

// runRotatePepper regenerates the dedicated api_token/delegation_token pepper.
// This INVALIDATES every existing API token and delegation token (their HMAC
// hashes can no longer be reproduced), so it requires --yes. The
// encryption key ring is untouched; encryption-key rotation never affects the
// pepper, and vice versa.
func runRotatePepper(cmd cmdCtx, args []string) error {
	var yes bool
	return withRecoverConfig(cmd, args, func(fs *flag.FlagSet) {
		fs.BoolVar(&yes, "yes", false, "confirm: this invalidates ALL API and delegation tokens")
	}, func(cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()
		if _, err := keystore.LoadFromFile(path); err != nil {
			return fmt.Errorf("encryption key file not found at %s\nRun the hub once to auto-generate it, or specify --data-dir", path)
		}
		if !yes {
			return fmt.Errorf("regenerating the pepper INVALIDATES ALL existing API tokens and delegation tokens; every client must be re-issued / re-authenticate.\nRe-run with --yes to proceed")
		}
		if err := keystore.RegeneratePepper(path); err != nil {
			return err
		}
		fmt.Println("Regenerated the API-token pepper.")
		fmt.Println("All existing API tokens and delegation tokens are now invalid.")
		fmt.Println("Restart the hub to apply, then re-issue API tokens with: leapmux control admin api-token issue --hub <url>")
		return nil
	})
}

func runReencryptSecrets(cmd cmdCtx, args []string) error {
	return withRecoverStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		ks, err := keystore.LoadFromFile(cfg.EncryptionKeyFilePath())
		if err != nil {
			return fmt.Errorf("load encryption key: %w", err)
		}

		activeVer := ks.ActiveVersion()
		count := 0

		// Re-encrypt oauth_providers.client_secret.
		providers, err := st.OAuthProviders().ListAllWithSecrets(ctx)
		if err != nil {
			return fmt.Errorf("list providers: %w", err)
		}

		for _, p := range providers {
			if ver, err := keystore.CiphertextVersion(p.ClientSecret); err == nil && ver == activeVer {
				continue // already at active version
			}
			newCt, err := reencryptBlob(ks, p.ClientSecret, keystore.ProviderAAD(p.ID),
				fmt.Sprintf("provider %s client_secret", p.ID))
			if err != nil {
				return err
			}
			if err := st.OAuthProviders().UpdateClientSecret(ctx, p.ID, newCt); err != nil {
				return fmt.Errorf("update provider %s: %w", p.ID, err)
			}
			count++
		}

		// Re-encrypt oauth_tokens.
		for _, ver := range ks.Versions() {
			if ver == activeVer {
				continue
			}
			tokens, listErr := st.OAuthTokens().ListByKeyVersion(ctx, int64(ver))
			if listErr != nil {
				return fmt.Errorf("list tokens for key version %d: %w", ver, listErr)
			}
			for _, tok := range tokens {
				accessAAD := keystore.AccessTokenAAD(tok.UserID, tok.ProviderID)
				refreshAAD := keystore.RefreshTokenAAD(tok.UserID, tok.ProviderID)

				newAccess, err := reencryptBlob(ks, tok.AccessToken, accessAAD,
					fmt.Sprintf("access_token for user %s", tok.UserID))
				if err != nil {
					return err
				}
				newRefresh, err := reencryptBlob(ks, tok.RefreshToken, refreshAAD,
					fmt.Sprintf("refresh_token for user %s", tok.UserID))
				if err != nil {
					return err
				}

				// A blank owner on an oauth_tokens row is corrupt data. Report
				// it rather than panicking through MustNew: this loop commits
				// each Upsert independently, so a panic would abort a key
				// rotation with some rows already at the new version and no
				// diagnosable cause. Matches the production refresh path in
				// hub/service/oauth_refresh.go.
				tokUID, mintOK := userid.New(tok.UserID)
				if !mintOK {
					return fmt.Errorf("oauth_tokens row for provider %s has a blank user id", tok.ProviderID)
				}

				err = st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
					UserID:       tokUID,
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

		// Re-encrypt hub_settings secret halves (SMTP passwords, captcha
		// signing keys and API secrets). The AAD is key-name-scoped and MUST
		// stay byte-identical across versions, or the row becomes
		// undecryptable — the settings snapshot degrades a bad secret to its
		// default, so skipping this leg would silently disable email or
		// captcha after a key removal.
		settingRows, err := st.Settings().GetAll(ctx)
		if err != nil {
			return fmt.Errorf("list settings: %w", err)
		}
		for _, row := range settingRows {
			if len(row.Secret) == 0 {
				continue
			}
			if ver, e := keystore.CiphertextVersion(row.Secret); e == nil && ver == activeVer {
				continue // already at active version
			}
			aad := keystore.SettingsSecretAAD(row.Key)
			newCt, err := reencryptBlob(ks, row.Secret, aad,
				fmt.Sprintf("settings secret for %s", row.Key))
			if err != nil {
				return err
			}
			if err := st.Settings().Upsert(ctx, store.UpsertSettingParams{
				Key:    row.Key,
				Value:  row.Value,
				Secret: newCt,
			}); err != nil {
				return fmt.Errorf("update settings secret for %s: %w", row.Key, err)
			}
			count++
		}

		fmt.Printf("Re-encrypted %d secrets to key version %d.\n", count, activeVer)
		return nil
	})
}

// reencryptBlob decrypts one ciphertext under its AAD and re-encrypts the
// plaintext under the active key with the SAME AAD: the AAD must stay
// byte-identical across versions, or the row becomes undecryptable. The
// what string identifies the row in error messages.
func reencryptBlob(ks *keystore.Keystore, ciphertext, aad []byte, what string) ([]byte, error) {
	plain, err := ks.Decrypt(ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", what, err)
	}
	reencrypted, err := ks.Encrypt(plain, aad)
	if err != nil {
		return nil, fmt.Errorf("re-encrypt %s: %w", what, err)
	}
	return reencrypted, nil
}

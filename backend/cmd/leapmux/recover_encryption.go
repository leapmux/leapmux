package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// missingKeyFileError states that the encryption key file is absent at path.
// The Hub writes the file at its first start.
func missingKeyFileError(path string, err error) error {
	return cli.Wrap(cli.CodeNotFound, err, fmt.Sprintf("no encryption key file at %s", path)).
		WithHint("start the Hub once to generate it, or select the Hub with --data-dir or --config")
}

func newEncryptionKeyRotateCommand() *cobra.Command {
	return recoverConfigLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:     "rotate",
		Short:   "Generate and add a new encryption key version",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux recover encryption-key rotate"},
		},
	}}, func(inv *cli.Invocation, cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()

		if _, err := keystore.LoadFromFile(path); err != nil {
			return missingKeyFileError(path, err)
		}

		newVersion, err := keystore.RotateKey(path)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(inv.Printer.Out, "Added encryption key version %d.\nRestart the hub, then run: leapmux recover encryption-key reencrypt\n", newVersion)
		return err
	})
}

// keyVersionHelp describes the <version> argument of `encryption-key remove`.
var keyVersionHelp = fmt.Sprintf("The key version to remove, from 1 to %d. The active version cannot be removed.", uint64(math.MaxUint32))

func newEncryptionKeyRemoveCommand() *cobra.Command {
	var v uint32
	return recoverStoreLeaf(recoverSpec{
		Leaf: cli.Leaf{
			Use:     "remove <version>",
			Short:   "Remove an old encryption key version",
			Mutates: true,
			Args:    map[string]string{"version": keyVersionHelp},
			Examples: []cli.Example{
				{Comment: "Remove version 1 after reencrypt moved every secret to a later version.", Command: "leapmux recover encryption-key remove 1"},
			},
		},
		BeforeOpen: func(inv *cli.Invocation) (err error) {
			v, err = parseKeyVersion(inv.Arg("version"))
			return err
		},
	}, func(inv *cli.Invocation, cfg *config.Config, st store.Store) error {
		ctx := inv.Context
		path := cfg.EncryptionKeyFilePath()
		ks, err := keystore.LoadFromFile(path)
		if err != nil {
			return missingKeyFileError(path, err)
		}
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

		_, err = fmt.Fprintf(inv.Printer.Out, "Removed encryption key version %d.\nRestart the hub to apply.\n", v)
		return err
	})
}

// parseKeyVersion reads the <version> argument of `encryption-key remove`: a
// key version from 1 to the largest uint32.
func parseKeyVersion(raw string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || v == 0 {
		return 0, cli.Usagef("<version> %q is not a key version", raw).
			WithHint("give a whole number from 1 to %d", uint64(math.MaxUint32))
	}
	return uint32(v), nil
}

// countEncryptedRefs returns human-readable descriptions of data still
// encrypted under the given key version. An empty result means the version is
// safe to remove. It scans the persistent encrypted secrets — OAuth provider
// client secrets, user OAuth tokens, captcha provider secrets, and passkey
// public keys. Transient pending_oauth_signups and webauthn_sessions are not
// scanned: they auto-expire, are never migrated by reencrypt, and a bricked
// row only fails an in-flight signup or ceremony the user can simply retry.
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

	passkeys, err := st.PasskeyCredentials().CountByKeyVersion(ctx, int64(version))
	if err != nil {
		return nil, fmt.Errorf("count passkey credentials for key version %d: %w", version, err)
	}
	if passkeys > 0 {
		refs = append(refs, fmt.Sprintf("%d passkey credential(s)", passkeys))
	}

	return refs, nil
}

// pepperRotationEffects is shared by confirmation and completion output.
const pepperRotationEffects = "- Signs out every session.\n" +
	"- Invalidates every Worker auth token.\n" +
	"- Invalidates API and delegation tokens and confidential app secrets.\n" +
	"- Invalidates live registration keys and pending device codes.\n"

// newEncryptionKeyRotatePepperCommand builds `leapmux recover encryption-key
// rotate-pepper`. It replaces the HMAC pepper for the credential classes
// listed in pepperRotationEffects. The resulting invalidations require --yes.
// The encryption key ring stays intact.
func newEncryptionKeyRotatePepperCommand() *cobra.Command {
	var yes bool
	return recoverConfigLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:     "rotate-pepper",
		Short:   "Rotate the pepper; sign out sessions and invalidate Worker/API/delegation tokens, app secrets, registration keys and device codes",
		Long:    "Rotate the token pepper. After the Hub restarts, the rotation has these effects:\n\n" + pepperRotationEffects + "\nThe encryption key ring stays unchanged.",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux recover encryption-key rotate-pepper --yes"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&yes, "yes", false, "confirm the effects of the rotation, which the command help lists")
		},
	}}, func(inv *cli.Invocation, cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()
		if _, err := keystore.LoadFromFile(path); err != nil {
			return missingKeyFileError(path, err)
		}
		if !yes {
			return cli.Usagef("rotating the token pepper requires --yes, because it has these effects after the Hub restarts:\n%s", strings.TrimSuffix(pepperRotationEffects, "\n")).
				WithHint("run the command again with --yes to proceed")
		}
		if err := keystore.RegeneratePepper(path); err != nil {
			return err
		}
		_, err := fmt.Fprint(inv.Printer.Out, "Regenerated the token pepper.\n"+
			pepperRotationEffects+
			"Restart the Hub to apply the new pepper.\n"+
			"Solo mode and the desktop app reissue their own Worker's token.\n"+
			"For each standalone Worker:\n"+
			"1. Remove the Worker in the Hub's Worker list.\n"+
			"2. Stop the Worker.\n"+
			"3. Delete its state.json.\n"+
			"4. Register it with a new registration key. It receives a new Worker ID.\n"+
			"Re-issue API tokens with: leapmux control admin api-token issue <user> --installation-name <name> --hub <url>\n")
		return err
	})
}

func newEncryptionKeyReencryptCommand() *cobra.Command {
	return recoverStoreLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:     "reencrypt",
		Short:   "Re-encrypt all secrets with the active key",
		Mutates: true,
		Examples: []cli.Example{
			{Comment: "Run it after rotate and a restart of the Hub.", Command: "leapmux recover encryption-key reencrypt"},
		},
	}}, func(inv *cli.Invocation, cfg *config.Config, st store.Store) error {
		ctx := inv.Context
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

		// Re-encrypt passkey_credentials.public_key rows.
		for _, ver := range ks.Versions() {
			if ver == activeVer {
				continue
			}
			passkeys, listErr := st.PasskeyCredentials().ListByKeyVersion(ctx, int64(ver))
			if listErr != nil {
				return fmt.Errorf("list passkey credentials for key version %d: %w", ver, listErr)
			}
			for _, pk := range passkeys {
				aad := keystore.PasskeyPublicKeyAAD(pk.ID)
				newCt, err := reencryptBlob(ks, pk.PublicKey, aad,
					fmt.Sprintf("passkey public key %s", pk.ID))
				if err != nil {
					return err
				}
				if err := st.PasskeyCredentials().UpdatePublicKey(ctx, store.UpdatePasskeyPublicKeyParams{
					ID:         pk.ID,
					UserID:     pk.UserID,
					PublicKey:  newCt,
					KeyVersion: int64(activeVer),
				}); err != nil {
					return fmt.Errorf("update passkey credential %s: %w", pk.ID, err)
				}
				count++
			}
		}

		_, err = fmt.Fprintf(inv.Printer.Out, "Re-encrypted %d secrets to key version %d.\n", count, activeVer)
		return err
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

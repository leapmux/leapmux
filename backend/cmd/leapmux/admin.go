package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/util/id"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin <command> [flags]\n\nCommands:\n  rotate-encryption-key        Generate and add a new encryption key version\n  remove-encryption-key        Remove an old encryption key version\n  reencrypt-secrets            Re-encrypt all secrets with the active key\n  add-oauth-provider           Add an OAuth/OIDC identity provider\n  list-oauth-providers         List configured OAuth providers\n  remove-oauth-provider        Remove an OAuth provider\n  enable-oauth-provider        Enable an OAuth provider\n  disable-oauth-provider       Disable an OAuth provider")
	}

	switch args[0] {
	case "rotate-encryption-key":
		return runRotateEncryptionKey(args[1:])
	case "remove-encryption-key":
		return runRemoveEncryptionKey(args[1:])
	case "reencrypt-secrets":
		return runReencryptSecrets(args[1:])
	case "add-oauth-provider":
		return runAddOAuthProvider(args[1:])
	case "list-oauth-providers":
		return runListOAuthProviders(args[1:])
	case "remove-oauth-provider":
		return runRemoveOAuthProvider(args[1:])
	case "enable-oauth-provider":
		return runSetOAuthProviderEnabled(args[1:], true)
	case "disable-oauth-provider":
		return runSetOAuthProviderEnabled(args[1:], false)
	default:
		return fmt.Errorf("unknown admin command: %s", args[0])
	}
}

// ---- Encryption key management ----

func runRotateEncryptionKey(args []string) error {
	fs := flag.NewFlagSet("rotate-encryption-key", flag.ContinueOnError)
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
	fmt.Printf("Restart the hub, then run: leapmux admin reencrypt-secrets\n")
	return nil
}

func runRemoveEncryptionKey(args []string) error {
	fs := flag.NewFlagSet("remove-encryption-key", flag.ContinueOnError)
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
	fs := flag.NewFlagSet("reencrypt-secrets", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := adminConfig(*dataDir)
	ks, err := keystore.LoadFromFile(cfg.EncryptionKeyFilePath())
	if err != nil {
		return fmt.Errorf("load encryption key: %w", err)
	}

	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
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
}

// ---- OAuth provider management ----

func runAddOAuthProvider(args []string) error {
	fs := flag.NewFlagSet("add-oauth-provider", flag.ContinueOnError)
	providerType := fs.String("type", "", "provider type (github, google, apple, oidc)")
	name := fs.String("name", "", "display name")
	clientID := fs.String("client-id", "", "OAuth client ID")
	clientSecret := fs.String("client-secret", "", "OAuth client secret")
	issuerURL := fs.String("issuer-url", "", "OIDC issuer URL")
	scopes := fs.String("scopes", "", "space-separated scopes")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *providerType == "" {
		return fmt.Errorf("--type is required (github, google, apple, oidc)")
	}
	if *clientID == "" {
		return fmt.Errorf("--client-id is required")
	}
	if *clientSecret == "" {
		return fmt.Errorf("--client-secret is required")
	}

	// Apply preset defaults.
	preset, ok := oauth.Presets[*providerType]
	if !ok {
		return fmt.Errorf("unknown provider type: %s (supported: github, google, apple, oidc)", *providerType)
	}

	displayName := *name
	if displayName == "" {
		displayName = preset.Name
	}
	if displayName == "" {
		return fmt.Errorf("--name is required for generic OIDC providers")
	}

	storedType := preset.ProviderType
	issuer := *issuerURL
	if issuer == "" {
		issuer = preset.IssuerURL
	}
	scopeStr := *scopes
	if scopeStr == "" {
		scopeStr = preset.Scopes
	}

	// Validate issuer for OIDC-based providers.
	if storedType == oauth.ProviderTypeOIDC {
		if issuer == "" {
			return fmt.Errorf("--issuer-url is required for OIDC providers")
		}
		fmt.Printf("Validating OIDC issuer %s ...\n", issuer)
		if err := oauth.ValidateIssuer(context.Background(), issuer); err != nil {
			return fmt.Errorf("issuer validation failed: %w", err)
		}
	}

	cfg := adminConfig(*dataDir)

	ks, err := keystore.LoadFromFile(cfg.EncryptionKeyFilePath())
	if err != nil {
		return fmt.Errorf("load encryption key: %w", err)
	}

	providerID := id.Generate()
	aad := keystore.ProviderAAD(providerID)
	encryptedSecret, err := ks.Encrypt([]byte(*clientSecret), aad)
	if err != nil {
		return fmt.Errorf("encrypt client secret: %w", err)
	}

	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: storedType,
		Name:         displayName,
		IssuerUrl:    issuer,
		ClientID:     *clientID,
		ClientSecret: encryptedSecret,
		Scopes:       scopeStr,
		Enabled:      1,
	}); err != nil {
		return fmt.Errorf("create provider: %w", err)
	}

	fmt.Printf("Created OAuth provider %q (id: %s, type: %s)\n", displayName, providerID, storedType)
	return nil
}

func runListOAuthProviders(args []string) error {
	fs := flag.NewFlagSet("list-oauth-providers", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	providers, err := q.ListAllOAuthProviders(context.Background())
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}

	if len(providers) == 0 {
		fmt.Println("No OAuth providers configured.")
		return nil
	}

	fmt.Printf("%-48s %-8s %-20s %s\n", "ID", "TYPE", "NAME", "ENABLED")
	for _, p := range providers {
		enabled := "yes"
		if p.Enabled != 1 {
			enabled = "no"
		}
		fmt.Printf("%-48s %-8s %-20s %s\n", p.ID, p.ProviderType, p.Name, enabled)
	}
	return nil
}

func runRemoveOAuthProvider(args []string) error {
	fs := flag.NewFlagSet("remove-oauth-provider", flag.ContinueOnError)
	providerID := fs.String("id", "", "provider ID")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *providerID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	provider, err := q.GetOAuthProviderByID(context.Background(), *providerID)
	if err != nil {
		return fmt.Errorf("get provider %s: %w", *providerID, err)
	}

	if err := q.DeleteOAuthProvider(context.Background(), *providerID); err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}

	fmt.Printf("Removed OAuth provider %q (id: %s)\n", provider.Name, *providerID)
	return nil
}

func runSetOAuthProviderEnabled(args []string, enabled bool) error {
	fs := flag.NewFlagSet("set-oauth-provider-enabled", flag.ContinueOnError)
	providerID := fs.String("id", "", "provider ID")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *providerID == "" {
		return fmt.Errorf("--id is required")
	}

	sqlDB, q, err := openAdminDB(adminConfig(*dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	var enabledInt int64
	if enabled {
		enabledInt = 1
	}
	if err := q.UpdateOAuthProviderEnabled(context.Background(), gendb.UpdateOAuthProviderEnabledParams{
		Enabled: enabledInt,
		ID:      *providerID,
	}); err != nil {
		return fmt.Errorf("update provider: %w", err)
	}

	action := "Disabled"
	if enabled {
		action = "Enabled"
	}
	fmt.Printf("%s OAuth provider %s\n", action, *providerID)
	return nil
}

// ---- Helpers ----

// openAdminDB opens the database, runs migrations, and returns the connection
// and queries handle. The caller must close the returned *sql.DB.
func openAdminDB(cfg *config.Config) (*sql.DB, *gendb.Queries, error) {
	dbPath := cfg.DBPath()
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	return sqlDB, gendb.New(sqlDB), nil
}

// adminConfig returns a minimal Config with DataDir set. When dataDir is
// empty it uses the default hub data directory.
func adminConfig(dataDir string) *config.Config {
	cfg := &config.Config{}
	if dataDir != "" {
		cfg.DataDir = dataDir
	} else {
		cfg.DataDir = config.DefaultHubDataDir()
	}
	return cfg
}

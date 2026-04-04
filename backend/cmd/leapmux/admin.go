package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	path := encryptionKeyPath(args)

	if _, err := os.Stat(path); os.IsNotExist(err) {
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
	var version int
	path := defaultEncryptionKeyPath()

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version":
			if i+1 >= len(args) {
				return fmt.Errorf("--version requires a value")
			}
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil || v < 1 || v > 255 {
				return fmt.Errorf("invalid version: %s (must be 1-255)", args[i])
			}
			version = v
		case "--data-dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--data-dir requires a value")
			}
			i++
			path = args[i] + "/encryption.key"
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	if version == 0 {
		return fmt.Errorf("--version is required")
	}

	// TODO: Check DB for rows referencing this version before removing.
	// This will be implemented when reencrypt-secrets is fully functional.

	if err := keystore.RemoveKey(path, byte(version)); err != nil {
		return err
	}

	fmt.Printf("Removed encryption key version %d.\n", version)
	fmt.Printf("Restart the hub to apply.\n")
	return nil
}

func runReencryptSecrets(args []string) error {
	dataDir := extractDataDir(args)

	ksPath := resolveEncryptionKeyPath(dataDir)
	ks, err := keystore.LoadFromFile(ksPath)
	if err != nil {
		return fmt.Errorf("load encryption key: %w", err)
	}

	dbPath := resolveDBPath(dataDir)
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	q := gendb.New(sqlDB)
	ctx := context.Background()
	activeVer := ks.ActiveVersion()
	count := 0

	// Re-encrypt oauth_providers.client_secret.
	providers, err := q.ListAllOAuthProviders(ctx)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	for _, p := range providers {
		full, getErr := q.GetOAuthProviderByID(ctx, p.ID)
		if getErr != nil {
			return fmt.Errorf("get provider %s: %w", p.ID, getErr)
		}
		if len(full.ClientSecret) > 0 && full.ClientSecret[0] == activeVer {
			continue // already at active version
		}
		aad := []byte("oauth_provider:" + p.ID)
		plain, decErr := ks.Decrypt(full.ClientSecret, aad)
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
	for ver := byte(1); ver < activeVer; ver++ {
		tokens, listErr := q.ListOAuthTokensByKeyVersion(ctx, int64(ver))
		if listErr != nil {
			continue
		}
		for _, tok := range tokens {
			accessAAD := []byte("access_token:" + tok.UserID + ":" + tok.ProviderID)
			refreshAAD := []byte("refresh_token:" + tok.UserID + ":" + tok.ProviderID)

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
	var providerType, name, clientID, clientSecret, issuerURL, scopes, dataDir string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--type":
			i++
			providerType = args[i]
		case "--name":
			i++
			name = args[i]
		case "--client-id":
			i++
			clientID = args[i]
		case "--client-secret":
			i++
			clientSecret = args[i]
		case "--issuer-url":
			i++
			issuerURL = args[i]
		case "--scopes":
			i++
			scopes = args[i]
		case "--data-dir":
			i++
			dataDir = args[i]
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	if providerType == "" {
		return fmt.Errorf("--type is required (github, google, apple, oidc)")
	}
	if clientID == "" {
		return fmt.Errorf("--client-id is required")
	}
	if clientSecret == "" {
		return fmt.Errorf("--client-secret is required")
	}

	// Apply preset defaults.
	preset, ok := oauth.Presets[providerType]
	if !ok {
		return fmt.Errorf("unknown provider type: %s (supported: github, google, apple, oidc)", providerType)
	}

	if name == "" {
		name = preset.Name
	}
	if name == "" {
		return fmt.Errorf("--name is required for generic OIDC providers")
	}

	storedType := preset.ProviderType
	if issuerURL == "" {
		issuerURL = preset.IssuerURL
	}
	if scopes == "" {
		scopes = preset.Scopes
	}

	// Validate issuer for OIDC-based providers.
	if storedType == "oidc" {
		if issuerURL == "" {
			return fmt.Errorf("--issuer-url is required for OIDC providers")
		}
		fmt.Printf("Validating OIDC issuer %s ...\n", issuerURL)
		if err := oauth.ValidateIssuer(context.Background(), issuerURL); err != nil {
			return fmt.Errorf("issuer validation failed: %w", err)
		}
	}

	// Load keystore to encrypt client secret.
	ksPath := resolveEncryptionKeyPath(dataDir)
	ks, err := keystore.LoadFromFile(ksPath)
	if err != nil {
		return fmt.Errorf("load encryption key: %w", err)
	}

	providerID := id.Generate()
	aad := []byte("oauth_provider:" + providerID)
	encryptedSecret, err := ks.Encrypt([]byte(clientSecret), aad)
	if err != nil {
		return fmt.Errorf("encrypt client secret: %w", err)
	}

	// Open database and insert.
	dbPath := resolveDBPath(dataDir)
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	q := gendb.New(sqlDB)
	if err := q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: storedType,
		Name:         name,
		IssuerUrl:    issuerURL,
		ClientID:     clientID,
		ClientSecret: encryptedSecret,
		Scopes:       scopes,
		Enabled:      1,
	}); err != nil {
		return fmt.Errorf("create provider: %w", err)
	}

	fmt.Printf("Created OAuth provider %q (id: %s, type: %s)\n", name, providerID, storedType)
	return nil
}

func runListOAuthProviders(args []string) error {
	dataDir := extractDataDir(args)

	dbPath := resolveDBPath(dataDir)
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	q := gendb.New(sqlDB)
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
	var providerID, dataDir string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			i++
			providerID = args[i]
		case "--data-dir":
			i++
			dataDir = args[i]
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	if providerID == "" {
		return fmt.Errorf("--id is required")
	}

	dbPath := resolveDBPath(dataDir)
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	q := gendb.New(sqlDB)

	// Verify provider exists.
	provider, err := q.GetOAuthProviderByID(context.Background(), providerID)
	if err != nil {
		return fmt.Errorf("provider %s not found", providerID)
	}

	// Cascade delete: tokens and user links are CASCADE'd in the schema.
	if err := q.DeleteOAuthProvider(context.Background(), providerID); err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}

	fmt.Printf("Removed OAuth provider %q (id: %s)\n", provider.Name, providerID)
	return nil
}

func runSetOAuthProviderEnabled(args []string, enabled bool) error {
	var providerID, dataDir string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			i++
			providerID = args[i]
		case "--data-dir":
			i++
			dataDir = args[i]
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	if providerID == "" {
		return fmt.Errorf("--id is required")
	}

	dbPath := resolveDBPath(dataDir)
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	q := gendb.New(sqlDB)

	var enabledInt int64
	if enabled {
		enabledInt = 1
	}
	if err := q.UpdateOAuthProviderEnabled(context.Background(), gendb.UpdateOAuthProviderEnabledParams{
		Enabled: enabledInt,
		ID:      providerID,
	}); err != nil {
		return fmt.Errorf("update provider: %w", err)
	}

	action := "Disabled"
	if enabled {
		action = "Enabled"
	}
	fmt.Printf("%s OAuth provider %s\n", action, providerID)
	return nil
}

// ---- Path helpers ----

func encryptionKeyPath(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--data-dir" && i+1 < len(args) {
			return args[i+1] + "/encryption.key"
		}
	}
	return defaultEncryptionKeyPath()
}

func defaultEncryptionKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "encryption.key"
	}
	return home + "/.config/leapmux/hub/encryption.key"
}

func extractDataDir(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--data-dir" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func resolveEncryptionKeyPath(dataDir string) string {
	if dataDir != "" {
		return dataDir + "/encryption.key"
	}
	return defaultEncryptionKeyPath()
}

func resolveDBPath(dataDir string) string {
	if dataDir != "" {
		return dataDir + "/hub.db"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hub.db"
	}
	return home + "/.config/leapmux/hub/hub.db"
}

// Ensure unused imports don't cause errors.
var _ = strings.Split

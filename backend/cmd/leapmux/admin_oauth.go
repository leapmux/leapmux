package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"strconv"

	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func runAdminOAuthProvider(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin oauth-provider <command> [flags]\n\nCommands:\n  add               Add an OAuth/OIDC provider\n  list              List configured providers\n  remove            Remove a provider\n  enable            Enable a provider\n  disable           Disable a provider")
	}

	switch args[0] {
	case "add":
		return runAddOAuthProvider(args[1:])
	case "list":
		return runListOAuthProviders(args[1:])
	case "remove":
		return runRemoveOAuthProvider(args[1:])
	case "enable":
		return runSetOAuthProviderEnabled(args[1:], true)
	case "disable":
		return runSetOAuthProviderEnabled(args[1:], false)
	default:
		return fmt.Errorf("unknown oauth-provider command: %s", args[0])
	}
}

func runAddOAuthProvider(args []string) error {
	var providerType *string
	var name *string
	var clientID *string
	var clientSecret *string
	var issuerURL *string
	var scopes *string
	var trustEmailFlag *bool
	return withAdminDB("oauth-provider add", args, func(fs *flag.FlagSet) {
		providerType = fs.String("type", "", "provider type (github, google, apple, oidc)")
		name = fs.String("name", "", "display name")
		clientID = fs.String("client-id", "", "OAuth client ID")
		clientSecret = fs.String("client-secret", "", "OAuth client secret")
		issuerURL = fs.String("issuer-url", "", "OIDC issuer URL")
		scopes = fs.String("scopes", "", "space-separated scopes")
		fs.Func("trust-email", "trust email from this provider as verified (true/false)", func(s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("must be 'true' or 'false'")
			}
			trustEmailFlag = &b
			return nil
		})
	}, func(ctx context.Context, cfg *config.Config, _ *sql.DB, q *gendb.Queries) error {
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

		// Resolve trust_email: explicit flag > preset default > error.
		trustEmailVal := trustEmailFlag
		if trustEmailVal == nil {
			trustEmailVal = preset.TrustEmail
		}
		if trustEmailVal == nil {
			return fmt.Errorf("--trust-email is required for generic OIDC providers (use --trust-email=true or --trust-email=false)")
		}
		trustEmail := ptrconv.BoolToInt64(*trustEmailVal)

		// Validate issuer for OIDC-based providers.
		if storedType == oauth.ProviderTypeOIDC {
			if issuer == "" {
				return fmt.Errorf("--issuer-url is required for OIDC providers")
			}
			fmt.Printf("Validating OIDC issuer %s ...\n", issuer)
			if err := oauth.ValidateIssuer(ctx, issuer); err != nil {
				return fmt.Errorf("issuer validation failed: %w", err)
			}
		}

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

		if err := q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
			ID:           providerID,
			ProviderType: storedType,
			Name:         displayName,
			IssuerUrl:    issuer,
			ClientID:     *clientID,
			ClientSecret: encryptedSecret,
			Scopes:       scopeStr,
			TrustEmail:   trustEmail,
			Enabled:      1,
		}); err != nil {
			return fmt.Errorf("create provider: %w", err)
		}

		fmt.Printf("Created OAuth provider %q (id: %s, type: %s)\n", displayName, providerID, storedType)
		return nil
	})
}

func runListOAuthProviders(args []string) error {
	return withAdminDB("oauth-provider list", args, nil, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		providers, err := q.ListAllOAuthProviders(ctx)
		if err != nil {
			return fmt.Errorf("list providers: %w", err)
		}

		if len(providers) == 0 {
			fmt.Println("No OAuth providers configured.")
			return nil
		}

		fmt.Printf("%-48s %-8s %-20s %-14s %s\n", "ID", "TYPE", "NAME", "TRUST_EMAIL", "ENABLED")
		for _, p := range providers {
			fmt.Printf("%-48s %-8s %-20s %-14s %s\n", p.ID, p.ProviderType, p.Name, yesNo(p.TrustEmail), yesNo(p.Enabled))
		}
		return nil
	})
}

func runRemoveOAuthProvider(args []string) error {
	var providerID *string
	return withAdminDB("oauth-provider remove", args, func(fs *flag.FlagSet) {
		providerID = fs.String("id", "", "provider ID")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *providerID == "" {
			return fmt.Errorf("--id is required")
		}

		provider, err := q.GetOAuthProviderByID(ctx, *providerID)
		if err != nil {
			return fmt.Errorf("get provider %s: %w", *providerID, err)
		}

		if err := q.DeleteOAuthProvider(ctx, *providerID); err != nil {
			return fmt.Errorf("delete provider: %w", err)
		}

		fmt.Printf("Removed OAuth provider %q (id: %s)\n", provider.Name, *providerID)
		return nil
	})
}

func runSetOAuthProviderEnabled(args []string, enabled bool) error {
	var providerID *string
	return withAdminDB("oauth-provider enable/disable", args, func(fs *flag.FlagSet) {
		providerID = fs.String("id", "", "provider ID")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *providerID == "" {
			return fmt.Errorf("--id is required")
		}

		if err := q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
			Enabled: ptrconv.BoolToInt64(enabled),
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
	})
}

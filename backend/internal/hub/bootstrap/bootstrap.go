package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

const (
	defaultPassword = "admin123"
)

// Username returns the default username for solo mode.
func Username(soloMode bool) string {
	if soloMode {
		return "solo"
	}
	return "admin"
}

// Run creates the personal org and default admin user when the database
// is empty. In hub mode (soloMode=false, devMode=false) this is a no-op
// because the first admin user is created interactively via the /setup page.
// In solo or dev mode, it bootstraps a default user for convenience.
func Run(ctx context.Context, st store.Store, soloMode, devMode bool) error {
	if !soloMode && !devMode {
		slog.Info("bootstrap: skipped (hub mode uses interactive setup)")
		return nil
	}

	hasUsers, err := st.Users().HasAny(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if hasUsers {
		slog.Info("bootstrap: skipped (already initialized)")
		return nil
	}

	username := Username(soloMode)

	var passwordHash string
	displayName := "Admin"
	if soloMode {
		displayName = "Solo"
	} else {
		hash, err := password.Hash(defaultPassword)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		passwordHash = hash
	}

	orgID := id.Generate()
	userID := id.Generate()

	if err := st.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       username,
			IsPersonal: true,
		}); err != nil {
			return fmt.Errorf("create personal org: %w", store.NewConflictError(err, store.ConflictEntityOrg))
		}

		if err := tx.Users().Create(ctx, store.CreateUserParams{
			ID:           userID,
			OrgID:        orgID,
			Username:     username,
			PasswordHash: passwordHash,
			DisplayName:  displayName,
			Email:        "",
			PasswordSet:  true,
			IsAdmin:      true,
		}); err != nil {
			return fmt.Errorf("create user: %w", store.NewConflictError(err, store.ConflictEntityUser))
		}

		if err := tx.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
			OrgID:  orgID,
			UserID: userID,
			Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
		}); err != nil {
			return fmt.Errorf("create org member: %w", err)
		}

		return nil
	}); err != nil {
		return err
	}

	slog.Info("bootstrap: created personal org and user",
		"org_id", orgID,
		"user_id", userID,
		"username", username,
	)

	return nil
}

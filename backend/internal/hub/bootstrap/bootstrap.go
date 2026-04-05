package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
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
func Run(ctx context.Context, sqlDB *sql.DB, q *db.Queries, soloMode, devMode bool) error {
	if !soloMode && !devMode {
		slog.Info("bootstrap: skipped (hub mode uses interactive setup)")
		return nil
	}

	count, err := q.CountOrgs(ctx)
	if err != nil {
		return fmt.Errorf("count orgs: %w", err)
	}
	if count > 0 {
		slog.Info("bootstrap: skipped (organizations already exist)")
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

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txq := q.WithTx(tx)

	orgID := id.Generate()
	if err := txq.CreateOrg(ctx, db.CreateOrgParams{
		ID:         orgID,
		Name:       username,
		IsPersonal: 1,
	}); err != nil {
		return fmt.Errorf("create personal org: %w", err)
	}

	userID := id.Generate()
	if err := txq.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     username,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		Email:        "",
		PasswordSet:  1,
		IsAdmin:      1,
	}); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	if err := txq.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return fmt.Errorf("create org member: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	slog.Info("bootstrap: created personal org and user",
		"org_id", orgID,
		"user_id", userID,
		"username", username,
	)

	return nil
}

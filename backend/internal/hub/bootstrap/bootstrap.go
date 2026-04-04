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
	defaultPassword = "admin"
)

// Username returns the default admin username for the given mode.
func Username(soloMode bool) string {
	if soloMode {
		return "solo"
	}
	return "admin"
}

// Run creates the personal org and admin user if no organizations
// exist yet. This is a no-op if the database already has data.
func Run(ctx context.Context, sqlDB *sql.DB, q *db.Queries, soloMode bool) error {
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
	if !soloMode {
		hash, err := password.Hash(defaultPassword)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		passwordHash = hash
	}

	displayName := "Admin"
	if soloMode {
		displayName = "Solo"
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
		return fmt.Errorf("create admin user: %w", err)
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

	slog.Info("bootstrap: created personal org and admin user",
		"org_id", orgID,
		"user_id", userID,
		"username", username,
	)

	return nil
}

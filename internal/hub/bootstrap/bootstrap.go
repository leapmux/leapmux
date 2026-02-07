package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
)

const (
	defaultUsername = "admin"
	defaultPassword = "admin"
)

// Run creates the personal org and admin user if no organizations
// exist yet. This is a no-op if the database already has data.
func Run(ctx context.Context, q *db.Queries) error {
	count, err := q.CountOrgs(ctx)
	if err != nil {
		return fmt.Errorf("count orgs: %w", err)
	}
	if count > 0 {
		slog.Info("bootstrap: skipped (organizations already exist)")
		return nil
	}

	orgID := id.Generate()
	if err := q.CreateOrg(ctx, db.CreateOrgParams{
		ID:         orgID,
		Name:       defaultUsername,
		IsPersonal: 1,
	}); err != nil {
		return fmt.Errorf("create personal org: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(defaultPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	userID := id.Generate()
	if err := q.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     defaultUsername,
		PasswordHash: string(hash),
		DisplayName:  "Admin",
		Email:        "",
		IsAdmin:      1,
	}); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	if err := q.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return fmt.Errorf("create org member: %w", err)
	}

	if err := q.UpsertUserPreferences(ctx, db.UpsertUserPreferencesParams{
		UserID: userID,
	}); err != nil {
		return fmt.Errorf("create user preferences: %w", err)
	}

	slog.Info("bootstrap: created personal org and admin user",
		"org_id", orgID,
		"user_id", userID,
		"username", defaultUsername,
	)

	return nil
}

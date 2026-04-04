package service

import (
	"context"
	"database/sql"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/util/id"
)

// CreateUserParams holds the parameters for creating a new user with a
// personal org and org membership.
type CreateUserParams struct {
	Username     string
	PasswordHash string
	DisplayName  string
	Email        string
	IsAdmin      int64
}

// createUserWithOrg creates a personal org, a user, and an org membership
// atomically within a transaction. It returns the created user row.
func createUserWithOrg(ctx context.Context, sqlDB *sql.DB, q *db.Queries, p CreateUserParams) (*db.User, error) {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txq := q.WithTx(tx)

	orgID := id.Generate()
	if err := txq.CreateOrg(ctx, db.CreateOrgParams{
		ID:         orgID,
		Name:       p.Username,
		IsPersonal: 1,
	}); err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}

	userID := id.Generate()
	if err := txq.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     p.Username,
		PasswordHash: p.PasswordHash,
		DisplayName:  p.DisplayName,
		Email:        p.Email,
		IsAdmin:      p.IsAdmin,
	}); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	if err := txq.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return nil, fmt.Errorf("create org member: %w", err)
	}

	user, err := txq.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get created user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &user, nil
}

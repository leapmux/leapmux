package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/util/id"
)

const pendingEmailExpiry = 24 * time.Hour

// CreateUserParams holds the parameters for creating a new user with a
// personal org and org membership.
type CreateUserParams struct {
	Username      string
	PasswordHash  string
	DisplayName   string
	Email         string
	EmailVerified int64
	PasswordSet   int64
	IsAdmin       int64
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
		ID:            userID,
		OrgID:         orgID,
		Username:      p.Username,
		PasswordHash:  p.PasswordHash,
		DisplayName:   p.DisplayName,
		Email:         p.Email,
		EmailVerified: p.EmailVerified,
		PasswordSet:   p.PasswordSet,
		IsAdmin:       p.IsAdmin,
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

// checkEmailAvailable checks that no other user has the given email in their
// email column. Empty emails are always allowed. Use excludeUserID to skip the
// current user (for email changes).
func checkEmailAvailable(ctx context.Context, q *db.Queries, email, excludeUserID string) error {
	if email == "" {
		return nil
	}
	existing, err := q.GetUserByEmail(ctx, email)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check email: %w", err)
	}
	if excludeUserID != "" && existing.ID == excludeUserID {
		return nil
	}
	return fmt.Errorf("email address is already in use")
}

// checkUsernameAvailable checks that no other user has the given username.
func checkUsernameAvailable(ctx context.Context, q *db.Queries, username string) error {
	_, err := q.GetUserByUsername(ctx, username)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username already taken"))
}

// verifyPendingEmailToken validates a pending-email verification token,
// checks expiry, and promotes the pending email. Returns the updated user.
func verifyPendingEmailToken(ctx context.Context, q *db.Queries, token string) (*db.User, error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("verification_token is required"))
	}

	user, err := q.GetUserByPendingEmailToken(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid verification token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if user.PendingEmailExpiresAt.Valid && time.Now().UTC().After(user.PendingEmailExpiresAt.Time) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("verification token expired"))
	}

	if user.PendingEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	if err := promotePendingEmail(ctx, q, user.ID, user.PendingEmail); err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}

	updatedUser, err := q.GetUserByID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &updatedUser, nil
}

// promotePendingEmail moves pending_email to email with email_verified=1.
// It checks that no other user has claimed the email since the pending was set.
func promotePendingEmail(ctx context.Context, q *db.Queries, userID, email string) error {
	if err := checkEmailAvailable(ctx, q, email, userID); err != nil {
		return fmt.Errorf("email was claimed by another user: %w", err)
	}
	if err := q.PromotePendingEmail(ctx, userID); err != nil {
		return fmt.Errorf("promote pending email: %w", err)
	}
	return nil
}

// setPendingEmailWithToken sets pending_email with a verification token.
// Stub: auto-verifies immediately (real email sending TBD).
func setPendingEmailWithToken(ctx context.Context, q *db.Queries, userID, email string) error {
	token := id.Generate()
	if err := q.SetPendingEmail(ctx, db.SetPendingEmailParams{
		PendingEmail:          email,
		PendingEmailToken:     token,
		PendingEmailExpiresAt: sql.NullTime{Time: time.Now().Add(pendingEmailExpiry).UTC(), Valid: true},
		ID:                    userID,
	}); err != nil {
		return fmt.Errorf("set pending email: %w", err)
	}

	// Stub: auto-verify immediately.
	if err := promotePendingEmail(ctx, q, userID, email); err != nil {
		slog.Warn("stub auto-verify failed", "user_id", userID, "error", err)
	}
	return nil
}

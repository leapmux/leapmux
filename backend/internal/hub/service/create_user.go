package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
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
	EmailVerified bool
	PasswordSet   bool
	IsAdmin       bool
}

// CreateUserWithOrg creates a personal org, a user, and an org membership
// atomically within a transaction. It returns the created user row.
func CreateUserWithOrg(ctx context.Context, st store.Store, p CreateUserParams) (*store.User, error) {
	orgID := id.Generate()
	userID := id.Generate()

	err := st.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       p.Username,
			IsPersonal: true,
		}); err != nil {
			return fmt.Errorf("create org: %w", store.NewConflictError(err, store.ConflictEntityOrg))
		}

		if err := tx.Users().Create(ctx, store.CreateUserParams{
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
	})
	if err != nil {
		return nil, err
	}

	createdUser := &store.User{
		ID:            userID,
		OrgID:         orgID,
		Username:      p.Username,
		DisplayName:   p.DisplayName,
		Email:         p.Email,
		EmailVerified: p.EmailVerified,
		PasswordSet:   p.PasswordSet,
		IsAdmin:       p.IsAdmin,
	}

	// If this user claimed a verified email, clear competing pending_email entries.
	ClearCompetingPendingEmails(ctx, st, p.Email, createdUser.ID)

	return createdUser, nil
}

// SetEmailAndClearCompeting updates a user's email and clears competing
// pending_email entries from other users. Use this instead of calling
// UpdateUserEmail + ClearCompetingPendingEmails separately.
func SetEmailAndClearCompeting(ctx context.Context, st store.Store, userID, email string, verified bool) error {
	if err := st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
		ID:            userID,
		Email:         email,
		EmailVerified: verified,
	}); err != nil {
		return err
	}
	ClearCompetingPendingEmails(ctx, st, email, userID)
	return nil
}

// CheckEmailAvailable checks that no other user has the given email in their
// verified email column. Empty emails are always allowed. Use excludeUserID
// to skip the current user (for email changes).
//
// Multiple users may have the same pending_email concurrently — only the
// verified email column is checked here. When a user promotes their
// pending_email, promotePendingEmail clears competing pending_email entries.
func CheckEmailAvailable(ctx context.Context, st store.Store, email, excludeUserID string) error {
	if email == "" {
		return nil
	}
	existing, err := st.Users().GetByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
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
func checkUsernameAvailable(ctx context.Context, st store.Store, username string) error {
	_, err := st.Users().GetByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username already taken"))
}

// verifyPendingEmailToken validates a pending-email verification token,
// checks expiry, and promotes the pending email. Returns the updated user.
func verifyPendingEmailToken(ctx context.Context, st store.Store, token string) (*store.User, error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("verification_token is required"))
	}

	user, err := st.Users().GetByPendingEmailToken(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid verification token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if user.PendingEmailExpiresAt != nil && time.Now().UTC().After(*user.PendingEmailExpiresAt) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("verification token expired"))
	}

	if user.PendingEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	if err := promotePendingEmail(ctx, st, user.ID, user.PendingEmail); err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}

	updatedUser, err := st.Users().GetByID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return updatedUser, nil
}

// promotePendingEmail moves pending_email to email with email_verified=true.
// It checks that no other user has claimed the verified email, then clears
// any other users' pending_email with the same value so they don't attempt
// to verify a now-taken address.
func promotePendingEmail(ctx context.Context, st store.Store, userID, email string) error {
	if err := CheckEmailAvailable(ctx, st, email, userID); err != nil {
		return fmt.Errorf("email was claimed by another user: %w", err)
	}
	if err := st.Users().PromotePendingEmail(ctx, userID); err != nil {
		return fmt.Errorf("promote pending email: %w", err)
	}
	ClearCompetingPendingEmails(ctx, st, email, userID)
	return nil
}

// ClearCompetingPendingEmails clears pending_email from all other users who
// have the same value. Call this whenever an email address is claimed — either
// by promotion or by direct assignment to the email column.
func ClearCompetingPendingEmails(ctx context.Context, st store.Store, email, ownerUserID string) {
	if email == "" {
		return
	}
	_ = st.Users().ClearCompetingPendingEmails(ctx, store.ClearCompetingPendingEmailsParams{
		PendingEmail: email,
		ExcludeID:    ownerUserID,
	})
}

// setPendingEmailWithToken sets pending_email with a verification token.
// It rejects immediately if the email is already verified by another user.
// Stub: auto-verifies immediately (real email sending TBD).
func setPendingEmailWithToken(ctx context.Context, st store.Store, userID, email string) error {
	if err := CheckEmailAvailable(ctx, st, email, userID); err != nil {
		return err
	}
	token := id.Generate()
	expiresAt := time.Now().Add(pendingEmailExpiry).UTC()
	if err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userID,
		PendingEmail:          email,
		PendingEmailToken:     token,
		PendingEmailExpiresAt: &expiresAt,
	}); err != nil {
		return fmt.Errorf("set pending email: %w", err)
	}

	// Stub: auto-verify immediately.
	if err := promotePendingEmail(ctx, st, userID, email); err != nil {
		slog.Warn("stub auto-verify failed", "user_id", userID, "error", err)
	}
	return nil
}

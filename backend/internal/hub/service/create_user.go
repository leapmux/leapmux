package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// pendingEmailExpiry is the lifetime of a freshly issued verification
// code. 30 minutes is long enough for the recipient to switch to their
// inbox and back, but short enough that a leaked code becomes useless
// well before someone could try to brute-force it.
const pendingEmailExpiry = 30 * time.Minute

// maxVerificationAttempts is the per-code attempt budget. The 6th wrong
// guess force-expires the code. With a 31-character alphabet this caps
// the success probability of a remote brute-force at 5/31^6 ≈ 5e-9.
const maxVerificationAttempts = 5

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
	taken, err := st.Users().ExistsByEmail(ctx, email, excludeUserID)
	if err != nil {
		return fmt.Errorf("check email: %w", err)
	}
	if taken {
		return fmt.Errorf("email address is already in use")
	}
	return nil
}

// checkUsernameAvailable checks that no other user has the given username.
func checkUsernameAvailable(ctx context.Context, st store.Store, username string) error {
	taken, err := st.Users().ExistsByUsername(ctx, username)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if taken {
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username already taken"))
	}
	return nil
}

// verifyPendingEmailToken validates the verification code submitted by
// the *session user* against their own pending row. Because the lookup
// is keyed by the session, no two users can ever collide on the short
// 6-character code — they each have at most one pending row.
//
// Logic:
//  1. Normalize the input (uppercase, strip hyphens/whitespace). A
//     malformed shape that can't be charset-checked is rejected as
//     InvalidArgument; the backend's own charset enforcement is the
//     source of truth.
//  2. Atomically charge one attempt and read back the post-update row.
//     The store helper bumps the counter, force-expires the row when
//     attempts > maxVerificationAttempts, and returns ErrNotFound when
//     there's no pending verification at all.
//  3. Expiry and mismatch collapse into a single NotFound with the
//     same message — the caller cannot tell which condition failed, so
//     we don't leak a timing/oracle signal.
//  4. On match, promote the pending email and return the fresh row.
func verifyPendingEmailToken(ctx context.Context, st store.Store, userID, submitted string) (*store.User, error) {
	normalized := verifycode.Normalize(submitted)
	if normalized == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid verification code"))
	}

	user, err := st.Users().ConsumeVerificationAttempt(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// The store's WHERE filter only checks pending_email_token; an
	// empty pending_email with a non-empty token shouldn't happen via
	// the normal SetPendingEmail path but defending here makes the
	// promotion below safe.
	if user.PendingEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	if int(user.PendingEmailAttempts) > maxVerificationAttempts {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many attempts; request a new code"))
	}

	expired := user.PendingEmailExpiresAt == nil || !time.Now().UTC().Before(*user.PendingEmailExpiresAt)
	mismatch := subtle.ConstantTimeCompare([]byte(user.PendingEmailToken), []byte(normalized)) != 1
	if expired || mismatch {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired verification code"))
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

// issuePendingEmailVerification stores a fresh pending_email row and
// dispatches the verification mail. The token is a 6-character
// verifycode (stored raw, displayed hyphenated); the mail body carries
// both the code and a click-through link to /verify-email, which is
// itself gated by login, so a leaked link alone cannot complete
// verification.
//
// Returns (sent, err): err signals a check or storage failure; sent=false
// means the row was written but the mail send failed. The row stays in
// place so signup / OAuth-signup / Resend callers can let the user try
// again via Resend. Email-change callers should use
// issuePendingEmailVerificationOrRollback instead.
func issuePendingEmailVerification(ctx context.Context, st store.Store, sender mail.Sender, userID, email string) (bool, error) {
	if err := CheckEmailAvailable(ctx, st, email, userID); err != nil {
		return false, err
	}
	storedCode := verifycode.Generate()
	expiresAt := time.Now().Add(pendingEmailExpiry).UTC()
	if err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userID,
		PendingEmail:          email,
		PendingEmailToken:     storedCode,
		PendingEmailExpiresAt: &expiresAt,
	}); err != nil {
		return false, fmt.Errorf("set pending email: %w", err)
	}

	link := "/verify-email?code=" + verifycode.Format(storedCode)
	if err := sender.Send(ctx, mail.RenderVerificationEmail(email, storedCode, link)); err != nil {
		return false, nil
	}
	return true, nil
}

// issuePendingEmailVerificationOrRollback is like
// issuePendingEmailVerification but, on send failure, clears the
// pending_email row before returning the error so the user can retry
// from a clean slate. Used by the email-change flow where the failure
// is surfaced to the user inline.
func issuePendingEmailVerificationOrRollback(ctx context.Context, st store.Store, sender mail.Sender, userID, email string) error {
	sent, err := issuePendingEmailVerification(ctx, st, sender, userID, email)
	if err != nil {
		return err
	}
	if !sent {
		_ = st.Users().ClearPendingEmail(ctx, userID)
		return fmt.Errorf("send verification email failed")
	}
	return nil
}

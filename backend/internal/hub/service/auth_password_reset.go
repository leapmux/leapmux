package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/validate"
)

// The forgot-password flow: mint a single-use link, then spend it.
//
// It sat in auth_passkey.go under a name that promised passkeys, so somebody
// tracing a reset link read past six hundred lines of passkey ceremony to
// reach it. The token is browser-bound the same way the OAuth nonce is (see
// browser_secret.go), and its resend cooldown is the shared one (see
// resend_cooldown.go).

// passwordResetExpiry is the lifetime of a reset link. It is longer than a
// verification code's because it arrives by mail and a user clicks it later,
// rather than reads it off a screen and types it. The cooldown that limits
// how often one may be minted is the shared one -- see resend_cooldown.go.
const passwordResetExpiry = time.Hour

// generatePasswordResetToken mints the emailed reset secret from the
// shared id mint (crypto-rand backed, 48 chars over a 62-alphabet, ~285
// bits), mirroring auth.MintAccessSecret and the session ids.
func generatePasswordResetToken() string {
	return id.Generate()
}

func (s *AuthService) RequestPasswordReset(ctx context.Context, req *connect.Request[leapmuxv1.RequestPasswordResetRequest]) (*connect.Response[leapmuxv1.RequestPasswordResetResponse], error) {
	if s.cfg.SoloMode {
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	identifier := req.Msg.GetIdentifier()
	var user *store.User
	var err error
	if strings.Contains(identifier, "@") {
		user, err = s.store.Users().GetByEmail(ctx, strings.TrimSpace(identifier))
	} else {
		username, slugErr := validate.SanitizeSlug("username", identifier)
		if slugErr != nil {
			return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
		}
		user, err = s.store.Users().GetByUsername(ctx, username)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Every miss path returns the same empty body as a hit. This code does
	// NOT equalize the timing: the hit path sends SMTP mail synchronously,
	// and its dial dominates any padding, so padding the miss path would look
	// like a control and protect nothing. The real anti-enumeration controls
	// are the captcha on this procedure, the uniform response body, and the
	// per-account resend cooldown below.
	targetEmail := user.Email
	if targetEmail == "" {
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	// No IsAdmin exemption here, unlike the auth interceptor's verification
	// control: this predicate asks whether the hub trusts THIS address with a
	// credential-bearing reset link, and the hub never verified it.
	//
	// An unverified administrator self-recovers with ResendVerificationEmail
	// plus VerifyEmail, which Preferences, Account offers beside the
	// "(unverified)" badge. Resend is the leg that seeds a pending row from
	// the stored address; RequestEmailChange does NOT, because its
	// administrator branch writes the new address straight to the email
	// column and clears any pending row.
	if !user.EmailVerified && s.emailVerificationRequired(ctx) {
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	// Cooldown: a recent request keeps its link. Minting a fresh token on
	// every request would flood the inbox and invalidate the link the
	// previous email still carries. The mint below is conditional -- the
	// write lands only when no previous token exists or the previous one
	// was issued at least the cooldown ago -- so a double-submit race
	// cannot mint and send twice and the first email's link stays valid.
	// mintCutoff owns the derivation; see resend_cooldown.go.
	// ONE instant for both, the way completeOAuthReauth states the rule for
	// its own leg: the cutoff and the expiry answer the same "now", so two
	// reads could disagree inside one request.
	now := s.now().UTC()
	cooldownCutoff := mintCutoff(now, passwordResetExpiry)
	rawToken := generatePasswordResetToken()
	expiresAt := now.Add(passwordResetExpiry)
	minted, err := s.store.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
		ID:                            user.ID,
		PendingPasswordResetToken:     hashBrowserSecret(rawToken),
		PendingPasswordResetExpiresAt: expiresAt,
		CooldownCutoff:                cooldownCutoff,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !minted {
		// A concurrent or recent request holds a live token: keep it (its
		// email is on the way or already delivered) and answer the same
		// empty body as every other path.
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	if err := s.mail.Send(ctx, s.renderer.PasswordResetEmail(targetEmail, rawToken, passwordResetExpiry)); err != nil {
		// Report the clear separately from the send. A log line that claims
		// "cleared pending token" while the clear itself failed makes the
		// operator look for the wrong cause of a token that is still live
		// for the full reset TTL.
		if clearErr := s.store.Users().ClearPendingPasswordReset(ctx, user.ID); clearErr != nil {
			slog.WarnContext(ctx, "clear pending password reset after failed send",
				"user_id", user.ID, "err", clearErr)
		}
		slog.WarnContext(ctx, "password reset email send failed; cleared pending token",
			"user_id", user.ID, "err", err)
		// Still return empty success so the response cannot enumerate accounts,
		// but do not leave a live token after a failed send.
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
}

func (s *AuthService) CompletePasswordReset(ctx context.Context, req *connect.Request[leapmuxv1.CompletePasswordResetRequest]) (*connect.Response[leapmuxv1.CompletePasswordResetResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "password reset"); err != nil {
		return nil, err
	}
	token := req.Msg.GetToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token is required"))
	}
	if err := validate.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hashedToken := hashBrowserSecret(token)

	// Charge one attempt against the row that holds this exact token. The
	// find, the charge, and the token re-check are one statement, so a token
	// cleared by a concurrent reset cannot surface as a 500.
	now := s.now().UTC()
	charged, err := s.store.Users().ConsumePasswordResetAttemptByToken(ctx, hashedToken, now, maxPasswordResetAttempts)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired reset token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Attempt 6+ expires the token in SQL (sets expires_at = now). Refuse
	// before Argon2 so the attempt budget is a hard cap, not soft.
	if charged.PendingPasswordResetAttempts > maxPasswordResetAttempts {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired reset token"))
	}
	if charged.PendingPasswordResetExpiresAt != nil && now.After(*charged.PendingPasswordResetExpiresAt) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reset token expired"))
	}

	hashed, err := pwdhash.Hash(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	user := charged
	uid, mintErr := mintRowUserID(user.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	var revoked *store.PasswordResetRevocation
	if err := s.store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
		var completeErr error
		revoked, completeErr = tx.Users().CompletePasswordReset(ctx, store.CompletePasswordResetParams{
			ID:                        user.ID,
			PasswordHash:              hashed,
			PendingPasswordResetToken: hashedToken,
		})
		if completeErr != nil {
			if errors.Is(completeErr, store.ErrNotFound) {
				return connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired reset token"))
			}
			return completeErr
		}
		if err := RevokePasskeyAuthState(ctx, tx, user.ID); err != nil {
			return err
		}
		if err := tx.Sessions().DeleteByUser(ctx, uid); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		if _, err := tx.APITokens().RevokeByUser(ctx, uid); err != nil {
			return fmt.Errorf("revoke api tokens: %w", err)
		}
		if _, err := tx.DelegationTokens().RevokeByUser(ctx, uid); err != nil {
			return fmt.Errorf("revoke delegation tokens: %w", err)
		}
		return nil
	}); err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if revoked != nil {
		s.lifecycle.UserRevoked(user.ID, revoked.AuthGeneration)
	}
	return connect.NewResponse(&leapmuxv1.CompletePasswordResetResponse{}), nil
}

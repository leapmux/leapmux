package service

import (
	"context"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// What AuthService reports and seeds about a pending email verification.
//
// It sat in auth_passkey.go, which named none of it: a reader looking for why
// a login answers "verify your email" had to find it inside a file about
// passkeys. The resend cooldown these read is shared with password reset --
// see resend_cooldown.go.

// verificationOutcome carries the verification flags every login and
// sign-up response reports. Not login-only: the four sign-up flavors and
// the GetCurrentUser bootstrap build one too, and each used to hand-write
// the proto literal instead.
type verificationOutcome struct {
	Required              bool
	EmailSent             bool
	NextResendAvailableAt *time.Time
}

// emailVerificationToProto maps a verification outcome onto the shared
// response message. Every response that carries the status builds it here.
func emailVerificationToProto(out verificationOutcome) *leapmuxv1.EmailVerificationStatus {
	return &leapmuxv1.EmailVerificationStatus{
		VerificationRequired:  out.Required,
		VerificationEmailSent: out.EmailSent,
		NextResendAvailableAt: optTimestamp(out.NextResendAvailableAt),
	}
}

// verificationStatusFor REPORTS the account's verification state without
// sending anything. loginVerificationOutcome is the sending twin, and the
// difference matters: this one serves GetCurrentUser, which every page load
// calls, so issuing mail from it would send a message per reload.
//
// The cooldown it reports comes from the live pending row, so a hard reload
// of /verify-email resumes the same countdown a login handed out instead of
// starting at zero and letting the button hammer a refusal.
func (s *AuthService) verificationStatusFor(ctx context.Context, user *store.User) verificationOutcome {
	if auth.EmailVerificationSatisfied(s.emailVerificationRequired(ctx), user.IsAdmin, user.EmailVerified) {
		return verificationOutcome{}
	}
	out := verificationOutcome{Required: true}
	if user.PendingEmail != "" && user.PendingEmailExpiresAt != nil {
		next := nextResendAt(issuedAtFromExpiry(*user.PendingEmailExpiresAt, pendingEmailExpiry))
		if s.now().UTC().Before(next) {
			out.NextResendAvailableAt = &next
		}
	}
	return out
}

func (s *AuthService) loginVerificationOutcome(ctx context.Context, user *store.User) verificationOutcome {
	if auth.EmailVerificationSatisfied(s.emailVerificationRequired(ctx), user.IsAdmin, user.EmailVerified) {
		return verificationOutcome{}
	}
	out := verificationOutcome{Required: true}
	targetEmail := user.PendingEmail
	if targetEmail == "" {
		targetEmail = user.Email
	}
	if targetEmail == "" {
		return out
	}
	if user.PendingEmail == "" {
		sent, next, err := s.ensurePendingVerification(ctx, user.ID, targetEmail)
		if err != nil {
			slogWarnVerification(ctx, "ensure pending verification on login", user.ID, err)
			return out
		}
		out.EmailSent = sent
		out.NextResendAvailableAt = next
		return out
	}
	if user.PendingEmailExpiresAt != nil {
		next := nextResendAt(issuedAtFromExpiry(*user.PendingEmailExpiresAt, pendingEmailExpiry))
		if s.now().UTC().Before(next) {
			out.NextResendAvailableAt = &next
			return out
		}
	}
	sent, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, user.ID, user.PendingEmail, s.now())
	if err != nil {
		slogWarnVerification(ctx, "resend verification on login", user.ID, err)
		return out
	}
	out.EmailSent = sent
	if sent {
		next := nextResendAt(s.now().UTC())
		out.NextResendAvailableAt = &next
	}
	return out
}

func (s *AuthService) ensurePendingVerification(ctx context.Context, userID, email string) (sent bool, nextResend *time.Time, err error) {
	if err := CheckEmailAvailable(ctx, s.store, email, userID); err != nil {
		return false, nil, err
	}
	sent, err = issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, userID, email, s.now())
	if err != nil {
		return false, nil, err
	}
	if sent {
		next := nextResendAt(s.now().UTC())
		nextResend = &next
	}
	return sent, nextResend, nil
}

func slogWarnVerification(ctx context.Context, msg, userID string, err error) {
	slog.WarnContext(ctx, msg, "user_id", userID, "err", err)
}

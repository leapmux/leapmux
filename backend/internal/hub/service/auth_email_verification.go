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
// It sat in auth_passkey.go, whose name covers none of it: a reader looking
// for why a login answers "verify your email" had to find it inside a file
// about passkeys. These functions and password reset share one resend
// cooldown -- see resend_cooldown.go.

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
// The two share both of their common halves -- the exemption question and the
// cooldown -- through the helpers below, so the report and the send cannot
// answer one screen two ways.
func (s *AuthService) verificationStatusFor(ctx context.Context, user *store.User) verificationOutcome {
	if s.emailVerificationSatisfied(ctx, user) {
		return verificationOutcome{}
	}
	return verificationOutcome{
		Required:              true,
		NextResendAvailableAt: s.pendingResendCooldown(user),
	}
}

// emailVerificationSatisfied answers the opening question BOTH outcomes ask:
// may this account use the hub although the hub requires a verified address.
// One helper, because the two functions below must never disagree about who
// is exempt -- the report and the send are two halves of one screen.
func (s *AuthService) emailVerificationSatisfied(ctx context.Context, user *store.User) bool {
	return auth.EmailVerificationFactsFromUser(user).Satisfied(s.emailVerificationRequired(ctx))
}

// pendingResendCooldown reports when the account may ask for another
// verification message, or nil when it may ask now.
//
// It reads the issued-at column the mint wrote -- not a value derived from
// the expiry, which the attempt consumer force-moves on a burned code. So
// a hard reload of /verify-email resumes the same countdown a login handed
// out instead of starting at zero and offering a button that the hub
// refuses.
//
// One helper, because the two callers read the SAME countdown for two
// purposes: verificationStatusFor reports it, and loginVerificationOutcome
// tests it to decide whether to send. A second copy of the rule would let
// the reported deadline and the enforced one drift apart, and the user
// would watch a timer reach zero on a button that still refuses.
func (s *AuthService) pendingResendCooldown(user *store.User) *time.Time {
	if user.PendingEmail == "" || user.PendingEmailIssuedAt == nil {
		return nil
	}
	next := nextResendAt(*user.PendingEmailIssuedAt)
	if !s.now().UTC().Before(next) {
		return nil
	}
	return &next
}

func (s *AuthService) loginVerificationOutcome(ctx context.Context, user *store.User) verificationOutcome {
	if s.emailVerificationSatisfied(ctx, user) {
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
	if next := s.pendingResendCooldown(user); next != nil {
		out.NextResendAvailableAt = next
		return out
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

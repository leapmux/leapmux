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
// about passkeys. These functions and account recovery share one resend
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
// The two read both of their common halves from one rule each -- the
// exemption question from auth.EmailVerificationFacts.Satisfied and the
// cooldown from resendAvailableAt -- so the report and the send cannot answer
// one screen two ways.
func (s *AuthService) verificationStatusFor(ctx context.Context, user *store.User) verificationOutcome {
	return reportedVerificationStatus(user, s.emailVerificationRequired(ctx), s.now())
}

// reportedVerificationStatus is the report verificationStatusFor gives,
// for a caller that holds the requirement and the clock itself:
// UserService.VerifyEmail reports the state its write produced through it,
// so the two surfaces cannot report one account two ways.
func reportedVerificationStatus(user *store.User, required bool, now time.Time) verificationOutcome {
	if auth.EmailVerificationFactsFromUser(user).Satisfied(required) {
		return verificationOutcome{}
	}
	return verificationOutcome{
		Required:              true,
		NextResendAvailableAt: resendAvailableAt(user, now),
	}
}

// resendAvailableAt reports when the account may ask for another
// verification message at now, or nil when it may ask now.
//
// It reads the issued-at column the mint wrote -- not a value derived from
// the expiry, which the attempt consumer force-moves on a burned code. So
// a hard reload of /verify-email resumes the same countdown a login handed
// out instead of starting at zero and offering a button that the hub
// refuses.
//
// One rule, because two callers read the SAME countdown for two purposes:
// reportedVerificationStatus reports it, and loginVerificationOutcome tests
// it to decide whether to send. A second copy of the rule would let the
// reported deadline and the enforced one drift apart, and the user would
// watch a timer reach zero on a button that still refuses.
func resendAvailableAt(user *store.User, now time.Time) *time.Time {
	if user.PendingEmail == "" || user.PendingEmailUnblockedAt == nil {
		return nil
	}
	next := *user.PendingEmailUnblockedAt
	if !now.UTC().Before(next) {
		return nil
	}
	return &next
}

// loginVerificationOutcome reports the verification state of an account that
// just signed in, and SENDS the code when the hub requires a verified address
// and no cooldown holds the send back. An account with no pending row gets
// one, minted for its primary address.
//
// It also reports whether it wrote the account row. A sign-in projects the
// account AFTER this call, from the row it left: a minted pending row changes
// the pending address that the projection reports.
func (s *AuthService) loginVerificationOutcome(ctx context.Context, user *store.User) (out verificationOutcome, wrote bool) {
	if auth.EmailVerificationFactsFromUser(user).Satisfied(s.emailVerificationRequired(ctx)) {
		return verificationOutcome{}, false
	}
	out = verificationOutcome{Required: true}
	targetEmail := user.PendingEmail
	if targetEmail == "" {
		targetEmail = user.Email
	}
	if targetEmail == "" {
		return out, false
	}
	if user.PendingEmail == "" {
		sent, next, err := s.ensurePendingVerification(ctx, user.ID, targetEmail)
		if err != nil {
			slogWarnVerification(ctx, "ensure pending verification on login", user.ID, err)
			return out, false
		}
		out.EmailSent = sent
		out.NextResendAvailableAt = next
		return out, true
	}
	if next := resendAvailableAt(user, s.now()); next != nil {
		out.NextResendAvailableAt = next
		return out, false
	}
	sent, next, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, user.ID, user.PendingEmail, s.now,
		mailFailureCooldown(ctx, s.set))
	if err != nil {
		slogWarnVerification(ctx, "resend verification on login", user.ID, err)
		return out, false
	}
	out.EmailSent = sent
	out.NextResendAvailableAt = next
	return out, true
}

// loginSessionReport builds the SessionReport of a password or passkey
// sign-in. It evaluates the verification outcome BEFORE it projects the
// account, because the outcome can write the account row, and the projection
// must report the row that the outcome left, as GetCurrentUser does.
func (s *AuthService) loginSessionReport(ctx context.Context, user *store.User) *leapmuxv1.SessionReport {
	outcome, wrote := s.loginVerificationOutcome(ctx, user)
	report := &leapmuxv1.SessionReport{EmailVerification: emailVerificationToProto(outcome)}
	if wrote {
		report.User = storedUserProto(ctx, s.store, user.ID, "sign-in")
	} else {
		report.User = committedUserProto(ctx, s.store, user, "sign-in")
	}
	return report
}

func (s *AuthService) ensurePendingVerification(ctx context.Context, userID, email string) (sent bool, nextResend *time.Time, err error) {
	if err := CheckEmailAvailable(ctx, s.store, email, userID); err != nil {
		return false, nil, err
	}
	sent, nextResend, err = issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, userID, email, s.now,
		mailFailureCooldown(ctx, s.set))
	if err != nil {
		return false, nil, err
	}
	return sent, nextResend, nil
}

func slogWarnVerification(ctx context.Context, msg, userID string, err error) {
	slog.WarnContext(ctx, msg, "user_id", userID, "err", err)
}

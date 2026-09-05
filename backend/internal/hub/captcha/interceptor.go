package captcha

import (
	"context"

	"connectrpc.com/connect"

	contracts "github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// captchaRequest is the field surface every protected request type must
// carry. The generated getters plus the compile-time guards below keep
// the contract checked at build time: renaming or dropping either proto
// field fails the build here instead of silently reading as "" at
// runtime, which would deny every login on the missing payload.
type captchaRequest interface {
	GetHoneypot() string
	GetCaptchaPayload() string
}

var (
	_ captchaRequest = (*leapmuxv1.LoginRequest)(nil)
	_ captchaRequest = (*leapmuxv1.SignUpRequest)(nil)
	_ captchaRequest = (*leapmuxv1.CompleteOAuthSignupRequest)(nil)
	_ captchaRequest = (*leapmuxv1.BeginPasskeyLoginRequest)(nil)
	_ captchaRequest = (*leapmuxv1.BeginPasskeySignUpRequest)(nil)
	_ captchaRequest = (*leapmuxv1.RequestAccountRecoveryRequest)(nil)
	_ captchaRequest = (*leapmuxv1.CompleteAccountRecoveryPasswordRequest)(nil)
	_ captchaRequest = (*leapmuxv1.BeginAccountRecoveryPasskeyRequest)(nil)
	_ captchaRequest = (*leapmuxv1.VerifyEmailRequest)(nil)
	_ captchaRequest = (*leapmuxv1.ResendVerificationEmailRequest)(nil)
)

// protectedProcedure is one protected procedure's captcha contract.
type protectedProcedure struct {
	// action is the name its clients mint the captcha token under
	// (reCAPTCHA's grecaptcha.execute({action}) and the Turnstile
	// widget's action parameter). reCAPTCHA requires verifying the
	// action server-side; Turnstile echoes it back; ALTCHA ignores it.
	// The tokens come from contracts/captcha.json -- the browser's
	// CaptchaField action union is generated from the same file, so a
	// rename cannot touch one side only -- and the generator enforces
	// the providers' shared constraints: alphanumerics and underscores
	// only, and Turnstile's 32-character action cap.
	action string
}

// protectedProcedures lists the procedures whose handlers are expensive
// enough (Argon2 verification, user creation, SMTP) that automation against
// them must pre-pay a captcha token. Most are unauthenticated; the two
// verification RPCs are not, and they are here for the same reason the
// anonymous ones are: their attempt budgets and cooldowns are cheap to
// charge, and the resend path drives an SMTP send, so a scripted session
// must pay the same toll a scripted login does. Carrying the action in the
// same entry makes a protected procedure without an action structurally
// impossible.
var protectedProcedures = map[string]protectedProcedure{
	leapmuxv1connect.AuthServiceLoginProcedure:                  {action: contracts.CaptchaActionLogin},
	leapmuxv1connect.AuthServiceSignUpProcedure:                 {action: contracts.CaptchaActionSignup},
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:    {action: contracts.CaptchaActionCompleteSignup},
	leapmuxv1connect.AuthServiceBeginPasskeyLoginProcedure:      {action: contracts.CaptchaActionPasskeyLogin},
	leapmuxv1connect.AuthServiceBeginPasskeySignUpProcedure:     {action: contracts.CaptchaActionPasskeySignUp},
	leapmuxv1connect.AuthServiceRequestAccountRecoveryProcedure: {action: contracts.CaptchaActionAccountRecovery},
	// account_recovery_password matches account_recovery_passkey. The longer
	// complete_account_recovery_password exceeds Turnstile's 32-character
	// action cap.
	leapmuxv1connect.AuthServiceCompleteAccountRecoveryPasswordProcedure: {action: contracts.CaptchaActionAccountRecoveryPassword},
	// The recovery passkey Begin charges an attempt against the recovery
	// token and resolves the account, so it pays the same toll the password
	// completion does. Finish is captcha-free like every ceremony Finish:
	// it can only spend a session the captcha'd Begin minted.
	leapmuxv1connect.AuthServiceBeginAccountRecoveryPasskeyProcedure: {action: contracts.CaptchaActionAccountRecoveryPasskey},
	leapmuxv1connect.UserServiceVerifyEmailProcedure:                 {action: contracts.CaptchaActionVerifyEmail},
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure:     {action: contracts.CaptchaActionResendVerification},
}

// IsProtected reports whether a captcha guards this procedure.
//
// It exists for the rate-limit package's coverage tripwire, which walks the
// proto descriptors for every procedure that carries a secret and demands that
// SOMETHING limit each one. A captcha and a budget are the two answers, so the
// walk must be able to ask this question. Restating the table there would give
// the tripwire a second copy to drift from, which is the one thing a tripwire
// must not have.
//
// The table is not exported itself, because a caller that ranged over it could
// act on the action tokens, and those belong to the interceptor.
func IsProtected(procedure string) bool {
	_, ok := protectedProcedures[procedure]
	return ok
}

// NewInterceptor returns a unary interceptor enforcing captcha + honeypot
// verification on the protected procedures. It must run BEFORE the auth
// interceptor's handler pass-through reaches the expensive handler logic.
// Its order relative to the auth interceptor still does not change WHICH
// requests are denied -- but it is no longer free either: the two
// verification procedures are authenticated, so with the auth interceptor
// first (backend/hub/server.go's chain order) an unauthenticated caller
// costs a session-gate lookup before the captcha denial instead of paying
// the captcha charge up front.
func NewInterceptor(m *Manager) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			proc, ok := protectedProcedures[req.Spec().Procedure]
			if !ok {
				return next(ctx, req)
			}

			msg, ok := req.Any().(captchaRequest)
			if !ok {
				// Every protected request type implements captchaRequest
				// (the compile-time guards above); this branch is unreachable
				// but must not panic if a future procedure puts a foreign
				// request type into the map.
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			// The honeypot check runs regardless of captcha enablement: it
			// costs the server one string comparison, catches naive bots
			// even on hubs with captcha disabled, and must not disappear
			// the moment an admin runs `captcha disable`. The frontend
			// renders the honeypot input independently of the captcha
			// widget for the same reason.
			if msg.GetHoneypot() != "" {
				m.NoteHoneypotDenial(ctx)
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			// Verify alone decides: it no-ops when verification is
			// disabled and denies closed on a resolve failure, exactly as
			// an Enabled pre-check would — without resolving the config a
			// second time per protected request.
			if err := m.Verify(ctx, proc.action, msg.GetCaptchaPayload()); err != nil {
				// Uniform denial: the manager already recorded the
				// outcome (passed/failed/replayed) under the selected
				// provider's metric label; clients see only this error.
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			return next(ctx, req)
		}
	}
}

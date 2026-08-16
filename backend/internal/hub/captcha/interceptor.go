package captcha

import (
	"context"

	"connectrpc.com/connect"

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
)

// protectedProcedure is one protected procedure's captcha contract.
type protectedProcedure struct {
	// action is the name its clients mint the captcha token under
	// (reCAPTCHA's grecaptcha.execute({action}) and the Turnstile
	// widget's action parameter). reCAPTCHA requires verifying the
	// action server-side; Turnstile echoes it back; ALTCHA ignores it.
	// The names use only alphanumerics and underscores — valid for both
	// providers — and stay under Turnstile's 32-character action cap.
	action string
}

// protectedProcedures lists the unauthenticated procedures whose handlers
// are expensive enough (Argon2 verification, user creation, SMTP) that
// automation against them must pre-pay a captcha token. Carrying the
// action in the same entry makes a protected procedure without an action
// structurally impossible.
var protectedProcedures = map[string]protectedProcedure{
	leapmuxv1connect.AuthServiceLoginProcedure:               {action: "login"},
	leapmuxv1connect.AuthServiceSignUpProcedure:              {action: "signup"},
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure: {action: "complete_signup"},
}

// NewInterceptor returns a unary interceptor enforcing captcha + honeypot
// verification on the protected procedures. It must run BEFORE the auth
// interceptor's handler pass-through reaches the expensive handler logic
// but has no ordering requirement with it: these procedures are public.
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
				// (the compile-time guards above); this arm is unreachable
				// but must not panic if a future procedure slips a foreign
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
				// Uniform denial: the manager has already recorded the
				// outcome (passed/failed/replayed) under the selected
				// provider's metric label; clients see only this error.
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			return next(ctx, req)
		}
	}
}

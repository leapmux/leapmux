package captcha

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// protectedProcedures lists the unauthenticated procedures whose handlers
// are expensive enough (Argon2 verification, user creation, SMTP) that
// automation against them must pre-pay a captcha token.
var protectedProcedures = map[string]struct{}{
	leapmuxv1connect.AuthServiceLoginProcedure:               {},
	leapmuxv1connect.AuthServiceSignUpProcedure:              {},
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure: {},
}

// procedureActions maps each protected procedure to the action name its
// clients mint the captcha token under (reCAPTCHA's
// grecaptcha.execute({action}) and the Turnstile widget's action
// parameter). reCAPTCHA requires verifying the action server-side;
// Turnstile echoes it back; ALTCHA ignores it. The names use only
// alphanumerics and underscores — valid for both providers — and stay
// under Turnstile's 32-character action cap. The classification test
// keeps this map in lockstep with protectedProcedures.
var procedureActions = map[string]string{
	leapmuxv1connect.AuthServiceLoginProcedure:               "login",
	leapmuxv1connect.AuthServiceSignUpProcedure:              "signup",
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure: "complete_signup",
}

// NewInterceptor returns a unary interceptor enforcing captcha + honeypot
// verification on the protected procedures. It must run BEFORE the auth
// interceptor's handler pass-through reaches the expensive handler logic
// but has no ordering requirement with it: these procedures are public.
func NewInterceptor(m *Manager) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, ok := protectedProcedures[req.Spec().Procedure]; !ok {
				return next(ctx, req)
			}

			msg, ok := req.Any().(protoreflect.ProtoMessage)
			if !ok {
				// Every ConnectRPC request is a proto message; this arm is
				// unreachable but must not panic if that ever changes.
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			protoMsg := msg.ProtoReflect()
			// The honeypot and payload fields exist on every protected
			// request type; a missing field reads as empty, which denies
			// on the missing payload and passes the empty honeypot.
			//
			// The honeypot check runs regardless of captcha enablement: it
			// costs the server one string comparison, catches naive bots
			// even on hubs with captcha disabled, and must not disappear
			// the moment an admin runs `captcha disable`. The frontend
			// renders the honeypot input independently of the captcha
			// widget for the same reason.
			if stringField(protoMsg, "honeypot") != "" {
				m.NoteHoneypotDenial(ctx)
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			if !m.Enabled(ctx) {
				return next(ctx, req)
			}
			if err := m.Verify(ctx, procedureActions[req.Spec().Procedure], stringField(protoMsg, "captcha_payload")); err != nil {
				// Uniform denial: the manager has already recorded the
				// outcome (passed/failed/replayed) under the selected
				// provider's metric label; clients see only this error.
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			return next(ctx, req)
		}
	}
}

// stringField reads a top-level string field via protoreflect so the
// interceptor needs no per-procedure type switches.
func stringField(msg protoreflect.Message, name string) string {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || fd.Kind() != protoreflect.StringKind || !msg.Has(fd) {
		return ""
	}
	return msg.Get(fd).String()
}

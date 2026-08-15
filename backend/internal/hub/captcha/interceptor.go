package captcha

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// protectedProcedures lists the unauthenticated procedures whose handlers
// are expensive enough (Argon2 verification, user creation, SMTP) that
// automation against them must pre-pay proof-of-work.
var protectedProcedures = map[string]struct{}{
	leapmuxv1connect.AuthServiceLoginProcedure:               {},
	leapmuxv1connect.AuthServiceSignUpProcedure:              {},
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure: {},
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
				CountResult(ResultFailed)
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			protoMsg := msg.ProtoReflect()
			// The honeypot and payload fields exist on every protected
			// request type; a missing field reads as empty, which denies
			// on the missing payload and passes the empty honeypot.
			//
			// The honeypot check runs regardless of captcha enablement: it
			// costs the server one string comparison, catches naive bots
			// even on hubs with proof-of-work disabled, and must not
			// disappear the moment an admin runs `captcha disable`. The
			// frontend renders the honeypot input independently of the
			// captcha widget for the same reason.
			if stringField(protoMsg, "honeypot") != "" {
				CountResult(ResultFailed)
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			if !m.Enabled(ctx) {
				return next(ctx, req)
			}
			if err := m.Verify(ctx, stringField(protoMsg, "captcha_payload")); err != nil {
				// The uniform denial is unchanged; only the metrics label
				// splits replay out, so operators can see payload reuse
				// separately from unsolved challenges.
				result := ResultFailed
				if errors.Is(err, errReplayed) {
					result = ResultReplayed
				}
				CountResult(result)
				return nil, connect.NewError(connect.CodePermissionDenied, ErrVerificationFailed)
			}
			CountResult(ResultPassed)
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

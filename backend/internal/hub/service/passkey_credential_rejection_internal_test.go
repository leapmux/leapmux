package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
)

// probeStream is the unused ServerStream of a probe call: the handler under
// test reads and writes no messages.
type probeStream struct{}

func (probeStream) Receive(any) error  { return io.EOF }
func (probeStream) SendHeaders() error { return nil }
func (probeStream) Send(any) error     { return nil }

// callUnderServerInfo runs fn the way a transport would: through
// connect.Server.Call, which attaches the server CallInfo that response
// headers ride. It returns that CallInfo so a test can read the headers fn
// set. A bare fn call carries no CallInfo, so no marker would be set at all.
func callUnderServerInfo(fn func(ctx context.Context) error) (*connect.CallInfo, error) {
	const procedure = "/leapmux.v1.test/Probe"
	srv := connect.NewServer()
	srv.Register(connect.Method{
		Spec:    connect.Spec{Procedure: procedure, StreamType: connect.StreamTypeUnary},
		Handler: func(ctx context.Context, _ connect.Spec, _ connect.ServerStream) error { return fn(ctx) },
	})
	info := &connect.CallInfo{}
	err := srv.Call(context.Background(), procedure, info, probeStream{})
	return info, err
}

// A rejected CREDENTIAL must never read as a dead SESSION.
//
// The client signs the user out on any Unauthenticated that carries no
// marker, so a ceremony the hub refused -- a mismatched RP ID after
// public_url changed, an expired or replayed ceremony session, a clone
// warning -- sent the user back to /login mid-dialog instead of showing
// "Failed to add passkey". The session is never what fails on this surface:
// auth.MustGetUser and the interceptor answer a dead session first, and every
// concurrency refusal here is FailedPrecondition.
//
// The marker rides the response header (connect v2 carries no metadata on an
// error), which is why each case runs under callUnderServerInfo.
func TestMapPasskeyConnectError_MarksARejectedCredential(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"a refused assertion": hubwebauthn.ErrAssertionRejected,
		"a spent ceremony":    hubwebauthn.ErrCeremonyInvalid,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			info, callErr := callUnderServerInfo(func(ctx context.Context) error {
				return mapPasskeyConnectError(ctx, err)
			})
			var connectErr *connect.Error
			require.ErrorAs(t, callErr, &connectErr)
			assert.Equal(t, connect.CodeUnauthenticated, connectErr.Code(),
				"a rejected credential is still Unauthenticated; the marker says WHICH credential")
			assert.Equal(t, "1", info.ResponseHeader().Get(CredentialRejectedHeader),
				"without the marker the client ends the session the dialog protected")
		})
	}

	// A refusal the handler already built passes through untouched, with its
	// marker -- and a FailedPrecondition must not acquire one, because it
	// was never about a credential.
	t.Run("a handler's own refusal passes through", func(t *testing.T) {
		t.Parallel()
		info, callErr := callUnderServerInfo(func(ctx context.Context) error {
			return mapPasskeyConnectError(ctx, stepUpStateMovedError())
		})
		var connectErr *connect.Error
		require.ErrorAs(t, callErr, &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
		assert.Empty(t, info.ResponseHeader().Get(CredentialRejectedHeader))
	})

	// An infrastructure failure is not the user's attempt at all, so it keeps
	// its own code and carries no marker.
	t.Run("an unrelated failure is Internal and unmarked", func(t *testing.T) {
		t.Parallel()
		info, callErr := callUnderServerInfo(func(ctx context.Context) error {
			return mapPasskeyConnectError(ctx, errors.New("the keystore is unreachable"))
		})
		var connectErr *connect.Error
		require.ErrorAs(t, callErr, &connectErr)
		assert.Equal(t, connect.CodeInternal, connectErr.Code())
		assert.Empty(t, info.ResponseHeader().Get(CredentialRejectedHeader))
	})
}

package service

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
)

// A rejected CREDENTIAL must never read as a dead SESSION.
//
// The client signs the user out on any Unauthenticated that carries no
// marker, so a ceremony the hub refused -- a mismatched RP ID after
// public_url changed, an expired or replayed ceremony session, a clone
// warning -- threw the user back to /login mid-dialog instead of showing
// "Failed to add passkey". The session is never what fails on this surface:
// auth.MustGetUser and the interceptor answer a dead session first, and every
// concurrency refusal here is FailedPrecondition.
func TestMapPasskeyConnectError_MarksARejectedCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for name, err := range map[string]error{
		"a refused assertion": hubwebauthn.ErrAssertionRejected,
		"a spent ceremony":    hubwebauthn.ErrCeremonyInvalid,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var connectErr *connect.Error
			require.ErrorAs(t, mapPasskeyConnectError(ctx, err), &connectErr)
			assert.Equal(t, connect.CodeUnauthenticated, connectErr.Code(),
				"a rejected credential is still Unauthenticated; the marker says WHICH credential")
			assert.Equal(t, "1", connectErr.Meta().Get(CredentialRejectedHeader),
				"without the marker the client ends the session the dialog was protecting")
		})
	}

	// A refusal the handler already built passes through untouched, marker
	// and all -- and a FailedPrecondition must not acquire one, because it
	// was never about a credential.
	t.Run("a handler's own refusal passes through", func(t *testing.T) {
		t.Parallel()
		var connectErr *connect.Error
		require.ErrorAs(t, mapPasskeyConnectError(ctx, stepUpStateMovedError()), &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
		assert.Empty(t, connectErr.Meta().Get(CredentialRejectedHeader))
	})

	// An infrastructure failure is not the user's attempt at all, so it keeps
	// its own code and carries no marker.
	t.Run("an unrelated failure is Internal and unmarked", func(t *testing.T) {
		t.Parallel()
		var connectErr *connect.Error
		require.ErrorAs(t, mapPasskeyConnectError(ctx, errors.New("the keystore is unreachable")), &connectErr)
		assert.Equal(t, connect.CodeInternal, connectErr.Code())
		assert.Empty(t, connectErr.Meta().Get(CredentialRejectedHeader))
	})
}

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestSessionCacheRequiresTheSecretOnEveryRequest(t *testing.T) {
	calls := 0
	revoked := false
	sessions := sessionValidationStore{validate: func(context.Context, string) (*store.SessionWithUser, error) {
		calls++
		if revoked {
			return nil, store.ErrNotFound
		}
		return validSessionRow("user", "user"), nil
	}}
	a := &authInterceptor{
		store:         sessionValidationOverrideStore{sessions: sessions},
		sessionHasher: testSecretHasher(), state: &authState{},
	}
	id := testSessionID("cache")
	wrong := FormatSessionToken(id, strings.Repeat("w", 48))
	for _, token := range []string{id, wrong} {
		_, err := a.validateTokenCached(t.Context(), token)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	}
	assert.Equal(t, 1, calls, "the bare identity must be refused before a store read")
	user, err := a.validateTokenCached(t.Context(), testSessionToken("cache"))
	require.NoError(t, err)
	assert.Equal(t, id, user.Credential.SessionID())
	assert.Equal(t, 2, calls)
	_, err = a.validateTokenCached(t.Context(), wrong)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, err = a.validateTokenCached(t.Context(), testSessionToken("cache"))
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "a wrong secret must neither authenticate nor remove the valid cache entry")
	revoked = true
	registry := &AuthContextRegistry{state: a.state}
	registry.Evict(id)
	_, err = a.validateTokenCached(t.Context(), testSessionToken("cache"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestValidateSessionTokenPreservesAStoreFailure(t *testing.T) {
	failure := errors.New("session storage unavailable")
	_, err := ValidateToken(t.Context(), validationErrorStore{err: failure}, testSecretHasher(), testSessionToken("failure"))
	require.ErrorIs(t, err, failure)
	assert.NotEqual(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestNewInterceptorRequiresTheSessionHasher(t *testing.T) {
	assert.PanicsWithValue(t, "auth interceptor requires SessionHasher when Store is set", func() {
		NewInterceptor(InterceptorOptions{Store: validationErrorStore{}, ServingCtx: t.Context()})
	})
}

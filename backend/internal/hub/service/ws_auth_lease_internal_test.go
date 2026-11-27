package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestNewWSAuthenticator_PanicsOnZeroOrInvalidScopes(t *testing.T) {
	t.Parallel()

	t.Run("zero scopes", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			newWSAuthenticator(nil, hubtestutil.TestSecretHasher(), newTestAuthContexts(t), nil, insecureCookies)
		})
	})

	for _, bad := range []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_UNSPECIFIED,
		leapmuxv1.Scope_SCOPE_NEVER,
		leapmuxv1.Scope_SCOPE_ALL,
	} {
		t.Run("multi-scope with "+bad.String(), func(t *testing.T) {
			t.Parallel()
			assert.Panics(t, func() {
				newWSAuthenticator(
					nil,
					hubtestutil.TestSecretHasher(),
					newTestAuthContexts(t),
					nil,
					insecureCookies,
					leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
					bad,
				)
			})
		})
	}
}

func TestWSAuthenticator_MultiScopeAuthenticate(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	scopes := []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		leapmuxv1.Scope_SCOPE_WORKER_READ,
	}
	authenticator := newWSAuthenticator(
		st,
		hubtestutil.TestSecretHasher(),
		newTestAuthContexts(t),
		nil,
		insecureCookies,
		scopes...,
	)
	authenticator.tokenValidator = tv
	// Mutating the caller's slice after construction must not alter door scopes.
	scopes[0] = leapmuxv1.Scope_SCOPE_UNSPECIFIED
	scopes[1] = leapmuxv1.Scope_SCOPE_NEVER

	makeReq := func(bearer string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/ws/userevents", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		return req
	}

	t.Run("admits credential holding first scope", func(t *testing.T) {
		bearer := mintScopedAPIToken(t, st, tv, "workspace:read")
		user, err := authenticator.authenticate(makeReq(bearer))
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.True(t, user.Scopes.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_READ))
	})

	t.Run("admits credential holding second scope", func(t *testing.T) {
		bearer := mintScopedAPIToken(t, st, tv, "worker:read")
		user, err := authenticator.authenticate(makeReq(bearer))
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.True(t, user.Scopes.Allows(leapmuxv1.Scope_SCOPE_WORKER_READ))
	})

	t.Run("rejects credential holding neither scope", func(t *testing.T) {
		bearer := mintScopedAPIToken(t, st, tv, "workspace:write")
		user, err := authenticator.authenticate(makeReq(bearer))
		assert.Nil(t, user)
		require.ErrorIs(t, err, auth.ErrHTTPForbidden)
		assert.ErrorContains(t, err, "this credential lacks required scopes")
	})

	t.Run("single-scope rejection preserves exact missing scope text", func(t *testing.T) {
		single := newWSAuthenticator(
			st,
			hubtestutil.TestSecretHasher(),
			newTestAuthContexts(t),
			nil,
			insecureCookies,
			leapmuxv1.Scope_SCOPE_WORKER_READ,
		)
		single.tokenValidator = tv

		bearer := mintScopedAPIToken(t, st, tv, "workspace:read")
		user, err := single.authenticate(makeReq(bearer))
		assert.Nil(t, user)
		require.ErrorIs(t, err, auth.ErrHTTPForbidden)
		assert.ErrorContains(t, err, "this credential was not granted worker:read")
	})
}

func TestRequireAnyScope_FailsClosedOnEmptyScopes(t *testing.T) {
	t.Parallel()

	user := &auth.UserInfo{
		ID:     userid.MustNew("u-unscoped"),
		Scopes: authscope.UnscopedGrant(),
	}
	err := requireAnyScope(user, nil)
	require.ErrorIs(t, err, auth.ErrHTTPForbidden)
	assert.ErrorContains(t, err, "no required scopes declared")
}

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type revokeLookupFault struct {
	store.DelegationTokenStore
	lookupErr error
	revokes   atomic.Int32
}

func (s *revokeLookupFault) GetByID(context.Context, string) (*store.DelegationToken, error) {
	return nil, s.lookupErr
}
func (s *revokeLookupFault) Revoke(ctx context.Context, id string) (int64, error) {
	s.revokes.Add(1)
	return s.DelegationTokenStore.Revoke(ctx, id)
}

type revokeLookupFaultStore struct {
	store.Store
	tokens *revokeLookupFault
}

func (s revokeLookupFaultStore) DelegationTokens() store.DelegationTokenStore { return s.tokens }

func TestWorkerDelegation_Revoke_ClassifiesLookupFailuresWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		fault  error
		status int
		body   string
	}{
		{"store fault", errors.New("database read unavailable"), http.StatusInternalServerError, "internal error\n"},
		{"wrapped absence", fmt.Errorf("lookup: %w", store.ErrNotFound), http.StatusNotFound, "token not found\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupDelegation(t)
			mint := mintRequest(t, env, env.workerAuthToken, map[string]any{"user_id": env.userID, "issued_for_tab_id": env.tabID})
			defer func() { _ = mint.Body.Close() }()
			require.Equal(t, http.StatusOK, mint.StatusCode)
			var body struct {
				TokenID string `json:"token_id"`
			}
			require.NoError(t, json.NewDecoder(mint.Body).Decode(&body))
			require.NotEmpty(t, body.TokenID)
			require.NoError(t, mint.Body.Close())
			tokens := &revokeLookupFault{DelegationTokenStore: env.store.DelegationTokens(), lookupErr: tc.fault}
			handler := service.NewWorkerDelegationHandler(revokeLookupFaultStore{Store: env.store, tokens: tokens}, env.validator, auth.NewCredentialLifecycleEffects(env.cache, nil, nil))
			mux := http.NewServeMux()
			handler.RegisterRoutes(mux)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			env.server = server
			response := revokeRequest(t, env, env.workerAuthToken, map[string]any{"token_id": body.TokenID})
			defer func() { _ = response.Body.Close() }()
			assert.Equal(t, tc.status, response.StatusCode)
			payload, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.body, string(payload))
			assert.Zero(t, tokens.revokes.Load())
			token, err := env.store.DelegationTokens().GetByID(t.Context(), body.TokenID)
			require.NoError(t, err)
			assert.Nil(t, token.RevokedAt)
		})
	}
}

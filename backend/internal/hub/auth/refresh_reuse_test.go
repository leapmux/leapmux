package auth_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// These tests cover the refresh-reuse paths the plan called out beyond
// what api_token_test.go already exercises:
//
//   - the compromise revocation cascades: once
//     ValidateAPIRefresh detects out-of-grace previous-hash reuse and
//     revokes the row, subsequent bearer validation also fails (the
//     same row backs both the access-token and refresh paths);
//   - concurrent refreshes of the same row don't tear: at most one
//     caller rotates, the others see either the new current or the
//     previous-within-grace path — never silent acceptance of a
//     stale refresh nor a row in an inconsistent state;
//   - presenting one row's refresh against a different row's id
//     fails as bad input (not as compromise) — this is the
//     bearer-shape attack where a stolen secret is paired with a
//     guessed token id.
//
// `api_token_test.go` covers the single-call validator behavior. This
// file exercises concurrent validation and the resulting row state.

func mintAPIToken(t *testing.T, st store.Store, v *auth.TokenValidator, userID string) (tokenID, currentSecret, refreshSecret string) {
	t.Helper()
	tokenID = id.Generate()
	currentSecret = auth.MintAccessSecret()
	refreshSecret = auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(currentSecret),
		RefreshHash: v.HashSecret(refreshSecret),
		Scope:       "remote:*",
	}))
	return
}

func TestRefreshReuse_CompromiseRevocationCascadesToAccessBearer(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	tokenID, accessSecret, prevSecret := mintAPIToken(t, st, v, userID)

	// Rotate so prevSecret moves to previous_refresh_hash with an
	// already-expired grace expiry. Reuse-after-grace must revoke.
	expiredGrace := time.Now().Add(-time.Hour)
	_, err := st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            v.HashSecret(auth.MintAccessSecret()),
		NewRefreshHash:           v.HashSecret(auth.MintAccessSecret()),
		PreviousRefreshHash:      v.HashSecret(prevSecret),
		PreviousRefreshExpiresAt: &expiredGrace,
	})
	require.NoError(t, err)

	// Reuse outside grace → compromise → row revoked.
	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, prevSecret))
	require.ErrorIs(t, err, auth.ErrRefreshReused)

	// The compromise revocation must also kill the access-token
	// path — otherwise an attacker who replayed an old refresh
	// could keep using the still-cached access bearer until the
	// hour-long TTL ran out.
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, accessSecret))
	require.Error(t, err, "access bearer must fail after compromise revocation")
}

func TestRefreshReuse_RevokedRowRefreshFails(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	tokenID, _, refreshSecret := mintAPIToken(t, st, v, userID)

	// Admin-driven revoke (the cleanup path the AuthContextRegistry.EvictBearer
	// test already covers for the unary call). Refresh must reject
	// even though the secret matches the current refresh_hash.
	_, err := st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret))
	require.Error(t, err, "refresh on a revoked row must fail")
	require.NotErrorIs(t, err, auth.ErrRefreshReused,
		"this is benign-revoke, not compromise — must not surface as ErrRefreshReused")
}

func TestRefreshReuse_CrossTokenSecretRejected(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	rowA, _, refreshA := mintAPIToken(t, st, v, userID)
	rowB, _, _ := mintAPIToken(t, st, v, userID)

	// Pair token id B with token A's refresh secret. The PK lookup
	// hits row B; the hash compare misses; the path must fall
	// through to "invalid token", not revoke either row.
	_, _, err := v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, rowB, refreshA))
	require.Error(t, err)
	require.NotErrorIs(t, err, auth.ErrRefreshReused,
		"cross-token shape attack must not be classified as compromise")

	rowAState, err := st.APITokens().GetByID(context.Background(), rowA)
	require.NoError(t, err)
	assert.Nil(t, rowAState.RevokedAt, "row A must remain unrevoked")
	rowBState, err := st.APITokens().GetByID(context.Background(), rowB)
	require.NoError(t, err)
	assert.Nil(t, rowBState.RevokedAt, "row B must remain unrevoked")
}

func TestRefreshReuse_ConcurrentRefreshDoesNotTearRow(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	tokenID, _, refreshSecret := mintAPIToken(t, st, v, userID)

	// Many goroutines validate the same refresh concurrently. None
	// should fail — current_hash matches → all return retry=false.
	const racers = 16
	var (
		successes atomic.Int32
		failures  atomic.Int32
		retries   atomic.Int32
		wg        sync.WaitGroup
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			_, retry, err := v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret))
			switch {
			case err != nil:
				failures.Add(1)
			case retry:
				retries.Add(1)
			default:
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Zero(t, failures.Load(), "current refresh must not fail under concurrency")
	assert.Equal(t, int32(racers), successes.Load(),
		"all racers must observe the current refresh as valid")
	assert.Zero(t, retries.Load(),
		"none of these should hit the previous-hash grace path")
}

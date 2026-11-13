package hub

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

type localReissueRaceStore struct {
	store.Store
	once   *sync.Once
	hasher auth.SecretHasher
	winner string
}
type localReissueRaceWorkers struct {
	store.WorkerStore
	parent localReissueRaceStore
}

func (s localReissueRaceStore) Workers() store.WorkerStore {
	return localReissueRaceWorkers{s.Store.Workers(), s}
}
func (w localReissueRaceWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	row, err := w.WorkerStore.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	var rotationErr error
	w.parent.once.Do(func() {
		_, rotationErr = w.RotateAuthToken(ctx, store.RotateWorkerAuthTokenParams{ID: id, AuthTokenHash: w.parent.hasher.Hash(w.parent.winner), ExpectedAuthTokenHash: row.AuthTokenHash})
	})
	if rotationErr != nil {
		return nil, rotationErr
	}
	return row, nil
}

func TestReissueLocalWorkerTokenReportsLiveContentionWithoutLosingIdentity(t *testing.T) {
	server, credentials := registeredLocalWorkerForTest(t)
	before, err := server.store.Workers().GetByID(t.Context(), credentials.WorkerID)
	require.NoError(t, err)
	server.store = localReissueRaceStore{Store: server.store, once: &sync.Once{}, hasher: server.hasher, winner: "the-winning-token"}
	replacement, err := server.ReissueLocalWorkerToken(t.Context(), credentials.WorkerID)
	require.ErrorIs(t, err, store.ErrConflict)
	require.NotErrorIs(t, err, store.ErrNotFound, "a live-row conflict must not trigger Solo's identity-discard path")
	assert.Empty(t, replacement)
	after, err := server.store.Workers().GetByID(t.Context(), credentials.WorkerID)
	require.NoError(t, err)
	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.PublicKeys, after.PublicKeys)
	_, err = auth.AuthenticateWorkerBearer(t.Context(), server.store, server.hasher, "Bearer the-winning-token")
	require.NoError(t, err)
}

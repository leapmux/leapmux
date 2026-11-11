package storetest

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkerTabPublications(t *testing.T) {
	st := s.NewStore(t)
	owner := SeedUser(t, st, "publication-owner")
	other := SeedUser(t, st, "publication-other")
	worker := SeedWorker(t, st, owner.ID)
	key := store.WorkerTabPublicationKey{UserID: userid.MustNew(owner.ID), WorkerID: worker.ID, TabID: "tab"}
	_, err := st.WorkerTabPublications().Get(t.Context(), key)
	require.ErrorIs(t, err, store.ErrNotFound)
	input := store.InsertWorkerTabPublicationParams{WorkerTabPublicationKey: key, WorkerTabPublication: store.WorkerTabPublication{RequestHash: bytes.Repeat([]byte{1}, 32), ResponsePayload: []byte("published")}}
	require.NoError(t, st.WorkerTabPublications().Insert(t.Context(), input))
	got, err := st.WorkerTabPublications().Get(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, input.WorkerTabPublication, *got)
	require.Error(t, st.WorkerTabPublications().Insert(t.Context(), input), "the receipt cannot be overwritten")
	foreign := key
	foreign.UserID = userid.MustNew(other.ID)
	_, err = st.WorkerTabPublications().Get(t.Context(), foreign)
	require.ErrorIs(t, err, store.ErrNotFound)
	zero := key
	zero.UserID = userid.UserID{}
	_, err = st.WorkerTabPublications().Get(t.Context(), zero)
	require.ErrorIs(t, err, store.ErrNotFound)
	invalid := input
	invalid.WorkerTabPublicationKey = zero
	require.ErrorIs(t, st.WorkerTabPublications().Insert(t.Context(), invalid), store.ErrInvalidArgument)
	invalid = input
	invalid.RequestHash = []byte{1}
	require.ErrorIs(t, st.WorkerTabPublications().Insert(t.Context(), invalid), store.ErrInvalidArgument)
	invalid = input
	invalid.ResponsePayload = nil
	require.ErrorIs(t, st.WorkerTabPublications().Insert(t.Context(), invalid), store.ErrInvalidArgument)
	rollback := errors.New("rollback publication")
	transactional := input
	transactional.TabID = "transaction"
	err = st.RunInTransaction(t.Context(), func(tx store.Store) error {
		if err := tx.WorkerTabPublications().Insert(t.Context(), transactional); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	_, err = st.WorkerTabPublications().Get(t.Context(), transactional.WorkerTabPublicationKey)
	require.ErrorIs(t, err, store.ErrNotFound)
	require.NoError(t, st.RunInTransaction(t.Context(), func(tx store.Store) error { return tx.WorkerTabPublications().Insert(t.Context(), transactional) }))
	_, err = st.WorkerTabPublications().Get(t.Context(), transactional.WorkerTabPublicationKey)
	require.NoError(t, err)
	replacement := input
	replacement.ResponsePayload = []byte("canceled")
	require.NoError(t, st.WorkerTabPublications().Replace(t.Context(), replacement))
	require.NoError(t, st.WorkerTabPublications().Replace(t.Context(), replacement))
	got, err = st.WorkerTabPublications().Get(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, replacement.WorkerTabPublication, *got)
	for _, identity := range []struct {
		label    string
		key      store.WorkerTabPublicationKey
		expected error
	}{
		{"zero", zero, store.ErrInvalidArgument}, {"foreign", foreign, store.ErrNotFound},
	} {
		t.Run("replacement owner "+identity.label, func(t *testing.T) {
			denied := replacement
			denied.WorkerTabPublicationKey = identity.key
			denied.ResponsePayload = []byte("must not replace the receipt")
			require.ErrorIs(t, st.WorkerTabPublications().Replace(t.Context(), denied), identity.expected)
			preserved, err := st.WorkerTabPublications().Get(t.Context(), key)
			require.NoError(t, err)
			assert.Equal(t, replacement.WorkerTabPublication, *preserved)
		})
	}
	mismatch := replacement
	mismatch.RequestHash = bytes.Repeat([]byte{2}, 32)
	require.ErrorIs(t, st.WorkerTabPublications().Replace(t.Context(), mismatch), store.ErrNotFound)
	err = st.RunInTransaction(t.Context(), func(tx store.Store) error {
		if err := tx.WorkerTabPublications().Replace(t.Context(), input); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	got, err = st.WorkerTabPublications().Get(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, replacement.WorkerTabPublication, *got)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			candidate := input
			candidate.TabID = "concurrent"
			results <- st.WorkerTabPublications().Insert(t.Context(), candidate)
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "exactly one concurrent insert owns the result")
}

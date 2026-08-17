package storetest

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testUserPrefs(t *testing.T) {
	t.Run("GetPrefsForUpdate reads like GetPrefs; absent user is not found", func(t *testing.T) {
		st := s.NewStore(t)

		_, err := st.Users().GetPrefsForUpdate(ctx, "00000000000000000000000000")
		assert.ErrorIs(t, err, store.ErrNotFound)

		user := SeedUser(t, st, "prefs-for-update")
		prefs, err := st.Users().GetPrefsForUpdate(ctx, user.ID)
		require.NoError(t, err)
		plain, err := st.Users().GetPrefs(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, plain, prefs)
	})

	t.Run("concurrent per-key updates both survive", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "prefs-concurrent-merge")

		// Two writers touch different keys of the same blob. Without the
		// lock GetPrefsForUpdate takes, both read the same base document
		// and the second commit erases the first's key — the whole-blob
		// clobber the per-key merge exists to prevent.
		const writers = 2
		start := make(chan struct{})
		done := make(chan error, writers)
		for i := range writers {
			key := "theme"
			if i == 1 {
				key = "debug_logging"
			}
			go func() {
				<-start
				done <- st.RunInTransaction(ctx, func(tx store.Store) error {
					prefs, err := tx.Users().GetPrefsForUpdate(ctx, user.ID)
					if err != nil {
						return err
					}
					doc := map[string]json.RawMessage{}
					if prefs != "" {
						if err := json.Unmarshal([]byte(prefs), &doc); err != nil {
							return err
						}
					}
					encoded, err := json.Marshal(i)
					if err != nil {
						return err
					}
					doc[key] = encoded
					out, err := json.Marshal(doc)
					if err != nil {
						return err
					}
					return tx.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
						Prefs: string(out),
						ID:    user.ID,
					})
				})
			}()
		}
		close(start)
		for range writers {
			require.NoError(t, <-done)
		}

		prefs, err := st.Users().GetPrefs(ctx, user.ID)
		require.NoError(t, err)
		var doc map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(prefs), &doc))
		assert.Contains(t, doc, "theme", "the first writer's key survives the second commit")
		assert.Contains(t, doc, "debug_logging", "the second writer's key survives too")
	})
}

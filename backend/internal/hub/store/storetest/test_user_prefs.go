package storetest

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testUserPrefs(t *testing.T) {
	// INSIDE a transaction, which is the only place this method answers.
	// SELECT ... FOR UPDATE takes a row lock the enclosing transaction holds;
	// with no transaction the lock is taken and released at once, so the
	// caller reads a row it does not hold and every later write races what it
	// just read. Every caller already went through RunInTransaction, and the
	// store now refuses the shape that does not.
	t.Run("GetPrefsForUpdate reads like GetPrefs; absent user is not found", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "prefs-for-update")

		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, err := tx.Users().GetPrefsForUpdate(ctx, "00000000000000000000000000")
			assert.ErrorIs(t, err, store.ErrNotFound)

			prefs, err := tx.Users().GetPrefsForUpdate(ctx, user.ID)
			require.NoError(t, err)
			plain, err := tx.Users().GetPrefs(ctx, user.ID)
			require.NoError(t, err)
			assert.Equal(t, plain, prefs)
			return nil
		}))
	})

	// And the refusal itself, in every dialect. A locking read on the pool is
	// a caller mistake the store must not answer silently -- on the mysql
	// dialect it is additionally unretryable, because conflictRetryDBTX
	// cannot wrap QueryRowContext.
	t.Run("a locking read outside a transaction is refused", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "prefs-no-tx")

		_, err := st.Users().GetPrefsForUpdate(ctx, user.ID)
		assert.ErrorIs(t, err, store.ErrInvalidArgument)

		_, err = st.Settings().GetAllForUpdate(ctx)
		assert.ErrorIs(t, err, store.ErrInvalidArgument)
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

package storetest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testTransactions(t *testing.T) {
	t.Run("commit on success", func(t *testing.T) {
		st := s.NewStore(t)

		var userID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-user")
			userID = user.ID
			return nil
		})
		require.NoError(t, err)

		user, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, "tx-user", user.Username)
	})

	t.Run("rollback on error", func(t *testing.T) {
		st := s.NewStore(t)

		var userID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-rollback-user")
			userID = user.ID
			return errors.New("intentional error")
		})
		require.Error(t, err)

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("multiple operations in transaction", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			SeedUser(t, tx, "tx-multi-user")
			return nil
		})
		require.NoError(t, err)

		page, err := st.Users().ListAll(ctx, store.ListAllUsersParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, "tx-multi-user", page.Rows[0].Username)
	})

	t.Run("nested reads within transaction", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-read-user")

			got, err := tx.Users().GetByID(ctx, user.ID)
			require.NoError(t, err)
			assert.Equal(t, "tx-read-user", got.Username)

			return nil
		})
		require.NoError(t, err)
	})

	t.Run("rollback on error rolls back all entity types", func(t *testing.T) {
		st := s.NewStore(t)

		userID := id.Generate()
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			if err := tx.Users().Create(ctx, store.CreateUserParams{
				ID:            userID,
				Username:      "rollback-multi-user",
				PasswordHash:  "hash",
				DisplayName:   "RB",
				Email:         "rb@example.com",
				EmailVerified: true,
				PasswordSet:   true,
				IsAdmin:       false,
			}); err != nil {
				return err
			}
			return fmt.Errorf("intentional rollback")
		})
		require.Error(t, err)

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound, "user should be rolled back")
	})

	t.Run("transaction isolation", func(t *testing.T) {
		st := s.NewStore(t)

		var userID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-isolation-user")
			userID = user.ID

			_, err := tx.Users().GetByID(ctx, user.ID)
			require.NoError(t, err)

			return errors.New("intentional rollback")
		})
		require.Error(t, err)

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("registration key consume rolls back with outer transaction", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tx-registration-key-user")
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(5*time.Minute).UTC())

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			_, err := tx.RegistrationKeys().Consume(ctx, regID)
			if err != nil {
				return err
			}
			return errors.New("intentional rollback")
		})
		require.EqualError(t, err, "intentional rollback")

		consumed, err := st.RegistrationKeys().Consume(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, user.ID, consumed.CreatedBy)
	})
}

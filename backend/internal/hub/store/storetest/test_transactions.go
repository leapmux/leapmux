package storetest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testTransactions(t *testing.T) {
	t.Run("commit on success", func(t *testing.T) {
		st := s.NewStore(t)

		var orgID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			orgID = SeedOrg(t, tx, "tx-org", false)
			return nil
		})
		require.NoError(t, err)

		// Org should be visible outside the transaction.
		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, "tx-org", org.Name)
	})

	t.Run("rollback on error", func(t *testing.T) {
		st := s.NewStore(t)

		var orgID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			orgID = SeedOrg(t, tx, "tx-rollback-org", false)
			return errors.New("intentional error")
		})
		require.Error(t, err)

		// Org should NOT be visible after rollback.
		_, err = st.Orgs().GetByID(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("multiple operations in transaction", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			orgID := SeedOrg(t, tx, "tx-multi-org", true)
			SeedUser(t, tx, orgID, "tx-multi-user")
			return nil
		})
		require.NoError(t, err)

		// Both org and user should be visible.
		users, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, users, 1)
		assert.Equal(t, "tx-multi-user", users[0].Username)
	})

	t.Run("nested reads within transaction", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			orgID := SeedOrg(t, tx, "tx-read-org", true)

			// Read within the same transaction should see the org.
			org, err := tx.Orgs().GetByID(ctx, orgID)
			require.NoError(t, err)
			assert.Equal(t, "tx-read-org", org.Name)

			user := SeedUser(t, tx, orgID, "tx-read-user")

			// Read the user within the same transaction.
			got, err := tx.Users().GetByID(ctx, user.ID)
			require.NoError(t, err)
			assert.Equal(t, "tx-read-user", got.Username)

			return nil
		})
		require.NoError(t, err)
	})

	t.Run("rollback on error rolls back all entity types", func(t *testing.T) {
		st := s.NewStore(t)

		orgID := id.Generate()
		userID := id.Generate()
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			if err := tx.Orgs().Create(ctx, store.CreateOrgParams{
				ID: orgID, Name: "rollback-multi-org", IsPersonal: false,
			}); err != nil {
				return err
			}
			if err := tx.Users().Create(ctx, store.CreateUserParams{
				ID: userID, OrgID: orgID, Username: "rollback-multi-user",
				PasswordHash: "hash", DisplayName: "RB", Email: "rb@example.com",
				EmailVerified: true, PasswordSet: true, IsAdmin: false,
			}); err != nil {
				return err
			}
			return fmt.Errorf("intentional rollback")
		})
		require.Error(t, err)

		// Both org and user should be rolled back.
		_, err = st.Orgs().GetByID(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound, "org should be rolled back")

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound, "user should be rolled back")
	})

	t.Run("transaction isolation", func(t *testing.T) {
		st := s.NewStore(t)

		var orgID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			orgID = SeedOrg(t, tx, "tx-isolation-org", false)
			user := SeedUser(t, tx, orgID, "tx-isolation-user")

			// Verify data is visible inside the transaction.
			_, err := tx.Users().GetByID(ctx, user.ID)
			require.NoError(t, err)

			return errors.New("intentional rollback")
		})
		require.Error(t, err)

		// After rollback, the org should not be visible outside.
		_, err = st.Orgs().GetByID(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

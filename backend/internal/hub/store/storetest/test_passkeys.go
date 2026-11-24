package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testPasskeys(t *testing.T) {
	t.Run("create get list count update delete", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "passkey-owner")
		now := time.Now().UTC().Truncate(time.Millisecond)
		pkID := id.Generate()
		credID := []byte("cred-" + pkID)

		require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
			ID:           pkID,
			UserID:       user.ID,
			CredentialID: credID,
			PublicKey:    []byte("encrypted-public-key"),
			SignCount:    1,
			Transports:   `["usb"]`,
			FriendlyName: "YubiKey",
			CreatedAt:    now,
		}))

		got, err := st.PasskeyCredentials().GetByID(ctx, pkID)
		require.NoError(t, err)
		assert.Equal(t, user.ID, got.UserID)
		assert.Equal(t, credID, got.CredentialID)
		assert.Equal(t, []byte("encrypted-public-key"), got.PublicKey)
		assert.EqualValues(t, 1, got.SignCount)
		assert.Equal(t, "YubiKey", got.FriendlyName)

		byCred, err := st.PasskeyCredentials().GetByCredentialID(ctx, credID)
		require.NoError(t, err)
		assert.Equal(t, pkID, byCred.ID)

		list, err := st.PasskeyCredentials().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		require.Len(t, list, 1)

		count, err := st.PasskeyCredentials().CountByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, count)

		usedAt := now.Add(time.Minute)
		require.NoError(t, st.PasskeyCredentials().UpdateSignCount(ctx, store.UpdatePasskeySignCountParams{
			CredentialID: credID,
			UserID:       user.ID,
			SignCount:    5,
			LastUsedAt:   usedAt,
		}))
		got, err = st.PasskeyCredentials().GetByID(ctx, pkID)
		require.NoError(t, err)
		assert.EqualValues(t, 5, got.SignCount)
		require.NotNil(t, got.LastUsedAt)

		// A non-advancing target must miss (monotonic predicate).
		err = st.PasskeyCredentials().UpdateSignCount(ctx, store.UpdatePasskeySignCountParams{
			CredentialID: credID,
			UserID:       user.ID,
			SignCount:    5,
			LastUsedAt:   usedAt,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		require.NoError(t, st.PasskeyCredentials().UpdateSignCount(ctx, store.UpdatePasskeySignCountParams{
			CredentialID: credID,
			UserID:       user.ID,
			SignCount:    6,
			LastUsedAt:   usedAt,
		}))
		got, err = st.PasskeyCredentials().GetByID(ctx, pkID)
		require.NoError(t, err)
		assert.EqualValues(t, 6, got.SignCount)

		require.NoError(t, st.PasskeyCredentials().UpdateFriendlyName(ctx, pkID, user.ID, "Renamed"))
		got, err = st.PasskeyCredentials().GetByID(ctx, pkID)
		require.NoError(t, err)
		assert.Equal(t, "Renamed", got.FriendlyName)

		require.NoError(t, st.PasskeyCredentials().Delete(ctx, pkID, user.ID))
		_, err = st.PasskeyCredentials().GetByID(ctx, pkID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete all by user", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "passkey-multi")
		now := time.Now().UTC()
		for i := 0; i < 2; i++ {
			pkID := id.Generate()
			require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
				ID:           pkID,
				UserID:       user.ID,
				CredentialID: []byte("multi-" + pkID),
				PublicKey:    []byte("pk"),
				Transports:   "[]",
				FriendlyName: "pk",
				CreatedAt:    now,
			}))
		}
		require.NoError(t, st.PasskeyCredentials().DeleteAllByUser(ctx, user.ID))
		count, err := st.PasskeyCredentials().CountByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 0, count)
	})

	t.Run("blank user id refused on passkey list", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.PasskeyCredentials().ListByUser(ctx, "")
		assert.ErrorIs(t, err, store.ErrInvalidArgument)
	})
}

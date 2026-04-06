package storetest

import (
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testRegistrations(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte("pub"),
			MlkemPublicKey:  []byte("mlkem"),
			SlhdsaPublicKey: []byte("slhdsa"),
			ExpiresAt:       time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		reg, err := st.Registrations().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, regID, reg.ID)
		assert.Equal(t, "1.0.0", reg.Version)
		assert.Equal(t, []byte("pub"), reg.PublicKey)
		assert.Equal(t, []byte("mlkem"), reg.MlkemPublicKey)
		assert.Equal(t, []byte("slhdsa"), reg.SlhdsaPublicKey)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING, reg.Status)
		assert.Nil(t, reg.WorkerID)
		assert.Nil(t, reg.ApprovedBy)
		assert.False(t, reg.CreatedAt.IsZero())
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Registrations().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("approve", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "reg-org", true)
		user := SeedUser(t, st, orgID, "reg-approver")
		worker := SeedWorker(t, st, user.ID)

		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		workerID := worker.ID
		err = st.Registrations().Approve(ctx, store.ApproveRegistrationParams{
			ID:         regID,
			WorkerID:   &workerID,
			ApprovedBy: &user.ID,
		})
		require.NoError(t, err)

		reg, err := st.Registrations().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED, reg.Status)
		require.NotNil(t, reg.WorkerID)
		assert.Equal(t, workerID, *reg.WorkerID)
		require.NotNil(t, reg.ApprovedBy)
		assert.Equal(t, user.ID, *reg.ApprovedBy)
	})

	t.Run("expire pending", func(t *testing.T) {
		st := s.NewStore(t)

		// Create a registration that is already expired.
		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		err = st.Registrations().ExpirePending(ctx)
		require.NoError(t, err)

		reg, err := st.Registrations().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED, reg.Status)
	})

	t.Run("expire pending does not affect approved", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "reg-org", true)
		user := SeedUser(t, st, orgID, "reg-approver2")
		worker := SeedWorker(t, st, user.ID)

		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		workerID := worker.ID
		err = st.Registrations().Approve(ctx, store.ApproveRegistrationParams{
			ID:         regID,
			WorkerID:   &workerID,
			ApprovedBy: &user.ID,
		})
		require.NoError(t, err)

		err = st.Registrations().ExpirePending(ctx)
		require.NoError(t, err)

		reg, err := st.Registrations().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED, reg.Status)
	})

	t.Run("duplicate registration id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)

		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		err = st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(1 * time.Hour),
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("expire pending does not affect non-expired", func(t *testing.T) {
		st := s.NewStore(t)

		// Create an expired registration.
		expiredID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID: expiredID, Version: "1.0.0",
			PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
			ExpiresAt: time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		// Create a valid registration.
		validID := id.Generate()
		err = st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID: validID, Version: "1.0.0",
			PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
			ExpiresAt: time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		err = st.Registrations().ExpirePending(ctx)
		require.NoError(t, err)

		// Valid registration should still be fetchable and pending.
		reg, err := st.Registrations().GetByID(ctx, validID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING, reg.Status)
	})

	t.Run("approve sets all fields", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "reg-org", true)
		user := SeedUser(t, st, orgID, "reg-fields-approver")
		worker := SeedWorker(t, st, user.ID)

		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "2.0.0",
			PublicKey:       []byte("pk"),
			MlkemPublicKey:  []byte("ml"),
			SlhdsaPublicKey: []byte("sl"),
			ExpiresAt:       time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		workerID := worker.ID
		approverID := user.ID
		err = st.Registrations().Approve(ctx, store.ApproveRegistrationParams{
			ID:         regID,
			WorkerID:   &workerID,
			ApprovedBy: &approverID,
		})
		require.NoError(t, err)

		reg, err := st.Registrations().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED, reg.Status)
		require.NotNil(t, reg.WorkerID)
		assert.Equal(t, workerID, *reg.WorkerID)
		require.NotNil(t, reg.ApprovedBy)
		assert.Equal(t, approverID, *reg.ApprovedBy)
	})
}

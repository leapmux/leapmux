package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testRegistrations(t *testing.T) {
	// Share the store + a default org/user across the whole group. Each
	// subtest creates its key (and any extra users it needs) with fresh
	// IDs, so cross-subtest interference is bounded to data the test
	// itself queries — and every query is keyed by id. Subtests that
	// must observe another user's row create that second user inline.
	st := s.NewStore(t)
	orgID := SeedOrg(t, st, "regkey-org")
	user := SeedUser(t, st, orgID, "regkey-user")

	t.Run("create and get by id", func(t *testing.T) {
		expires := time.Now().Add(5 * time.Minute).UTC()
		regID := SeedRegistrationKey(t, st, user.ID, expires)

		got, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, regID, got.ID)
		assert.Equal(t, user.ID, got.CreatedBy)
		assert.WithinDuration(t, expires, got.ExpiresAt, time.Second)
		assert.False(t, got.CreatedAt.IsZero())
	})

	t.Run("get by id not found", func(t *testing.T) {
		_, err := st.RegistrationKeys().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("extend rewrites expires_at", func(t *testing.T) {
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(1*time.Minute).UTC())

		newExpires := time.Now().Add(10 * time.Minute).UTC()
		rows, err := st.RegistrationKeys().Extend(ctx, store.ExtendRegistrationKeyParams{
			ID:        regID,
			CreatedBy: user.ID,
			ExpiresAt: newExpires,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)

		got, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.WithinDuration(t, newExpires, got.ExpiresAt, time.Second)
	})

	t.Run("extend refuses dead row", func(t *testing.T) {
		// Already-expired row: liveness guard inside the UPDATE must
		// refuse to revive it. 0 rows-affected, no error.
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(-1*time.Minute).UTC())

		rows, err := st.RegistrationKeys().Extend(ctx, store.ExtendRegistrationKeyParams{
			ID:        regID,
			CreatedBy: user.ID,
			ExpiresAt: time.Now().Add(5 * time.Minute).UTC(),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), rows)
	})

	t.Run("extend refuses other user's row", func(t *testing.T) {
		intruder := SeedUser(t, st, orgID, "regkey-extend-intruder")
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(5*time.Minute).UTC())

		rows, err := st.RegistrationKeys().Extend(ctx, store.ExtendRegistrationKeyParams{
			ID:        regID,
			CreatedBy: intruder.ID,
			ExpiresAt: time.Now().Add(10 * time.Minute).UTC(),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), rows)
	})

	t.Run("soft delete moves expires into the past", func(t *testing.T) {
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(5*time.Minute).UTC())

		rows, err := st.RegistrationKeys().SoftDelete(ctx, store.SoftDeleteRegistrationKeyParams{
			ID:        regID,
			CreatedBy: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)

		got, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.True(t, got.ExpiresAt.Before(time.Now()), "expires_at not in past after SoftDelete: %s", got.ExpiresAt)
	})

	t.Run("soft delete refuses other user's row", func(t *testing.T) {
		intruder := SeedUser(t, st, orgID, "regkey-softdel-intruder")
		expires := time.Now().Add(5 * time.Minute).UTC()
		regID := SeedRegistrationKey(t, st, user.ID, expires)

		rows, err := st.RegistrationKeys().SoftDelete(ctx, store.SoftDeleteRegistrationKeyParams{
			ID:        regID,
			CreatedBy: intruder.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), rows)

		// Owner's row stays alive.
		got, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.WithinDuration(t, expires, got.ExpiresAt, time.Second)
	})

	t.Run("consume returns row and burns key", func(t *testing.T) {
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(5*time.Minute).UTC())

		consumed, err := st.RegistrationKeys().Consume(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, user.ID, consumed.CreatedBy)

		// A second consume must fail — the row is now soft-deleted.
		_, err = st.RegistrationKeys().Consume(ctx, regID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("consume rejects expired key", func(t *testing.T) {
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(-1*time.Minute).UTC())

		_, err := st.RegistrationKeys().Consume(ctx, regID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("duplicate id returns conflict", func(t *testing.T) {
		expires := time.Now().Add(5 * time.Minute).UTC()
		regID := SeedRegistrationKey(t, st, user.ID, expires)

		err := st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
			ID:        regID,
			CreatedBy: user.ID,
			ExpiresAt: expires,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("admin soft delete bypasses ownership", func(t *testing.T) {
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(5*time.Minute).UTC())

		rows, err := st.RegistrationKeys().AdminSoftDelete(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)

		got, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.True(t, got.ExpiresAt.Before(time.Now()), "expires_at should be in past after AdminSoftDelete")

		_, err = st.RegistrationKeys().Consume(ctx, regID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("admin soft delete missing returns zero rows", func(t *testing.T) {
		rows, err := st.RegistrationKeys().AdminSoftDelete(ctx, "nonexistent-id")
		require.NoError(t, err)
		assert.Equal(t, int64(0), rows)
	})

	t.Run("list admin hides expired by default", func(t *testing.T) {
		// Fresh owner per list subtest so the assertions can ignore keys
		// other subtests left behind on the shared store.
		listOrgID := SeedOrg(t, st, "regkey-list-org-default")
		owner := SeedUser(t, st, listOrgID, "regkey-list-default")

		live := SeedRegistrationKey(t, st, owner.ID, time.Now().Add(5*time.Minute).UTC())
		_ = SeedRegistrationKey(t, st, owner.ID, time.Now().Add(-1*time.Minute).UTC())

		rows, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{Limit: 50})
		require.NoError(t, err)

		ownerRows := filterRowsByCreator(rows, owner.ID)
		require.Len(t, ownerRows, 1)
		assert.Equal(t, live, ownerRows[0].ID)
		assert.Equal(t, owner.Username, ownerRows[0].CreatorUsername)
	})

	t.Run("list admin include expired surfaces revoked rows", func(t *testing.T) {
		listOrgID := SeedOrg(t, st, "regkey-list-org-incl")
		owner := SeedUser(t, st, listOrgID, "regkey-list-incl")

		live := SeedRegistrationKey(t, st, owner.ID, time.Now().Add(5*time.Minute).UTC())
		dead := SeedRegistrationKey(t, st, owner.ID, time.Now().Add(-1*time.Minute).UTC())

		rows, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			Limit:          50,
			IncludeExpired: true,
		})
		require.NoError(t, err)

		ownerRows := filterRowsByCreator(rows, owner.ID)
		ids := make([]string, 0, len(ownerRows))
		for _, r := range ownerRows {
			ids = append(ids, r.ID)
		}
		assert.ElementsMatch(t, []string{live, dead}, ids)
	})

	t.Run("list admin paginates by created_at cursor", func(t *testing.T) {
		listOrgID := SeedOrg(t, st, "regkey-list-org-page")
		owner := SeedUser(t, st, listOrgID, "regkey-list-page")

		// created_at is set by the SQL DEFAULT (strftime ms on SQLite,
		// CURRENT_TIMESTAMP(3) on MySQL, now() on PG). Consecutive seeds
		// can land in the same millisecond, which would tie the strict-`<`
		// cursor — sleep a few ms between seeds to keep the order
		// deterministic across all three backends.
		expires := time.Now().Add(5 * time.Minute).UTC()
		idOld := SeedRegistrationKey(t, st, owner.ID, expires)
		time.Sleep(5 * time.Millisecond)
		idMid := SeedRegistrationKey(t, st, owner.ID, expires)
		time.Sleep(5 * time.Millisecond)
		idNew := SeedRegistrationKey(t, st, owner.ID, expires)

		full, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{Limit: 100})
		require.NoError(t, err)

		ownerRows := filterRowsByCreator(full, owner.ID)
		require.Len(t, ownerRows, 3, "all three seeded keys should be visible without a cursor")
		assert.Equal(t, []string{idNew, idMid, idOld}, []string{ownerRows[0].ID, ownerRows[1].ID, ownerRows[2].ID},
			"DESC order should put newest-created first")

		cursor := ownerRows[1].CreatedAt.UTC().Format(time.RFC3339Nano)
		next, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			Cursor: cursor,
			Limit:  100,
		})
		require.NoError(t, err)

		afterCursor := filterRowsByCreator(next, owner.ID)
		require.Len(t, afterCursor, 1)
		assert.Equal(t, idOld, afterCursor[0].ID, "second page should contain only the oldest key")
	})
}

func filterRowsByCreator(rows []store.WorkerRegistrationKeyWithCreator, userID string) []store.WorkerRegistrationKeyWithCreator {
	out := make([]store.WorkerRegistrationKeyWithCreator, 0, len(rows))
	for _, r := range rows {
		if r.CreatedBy == userID {
			out = append(out, r)
		}
	}
	return out
}

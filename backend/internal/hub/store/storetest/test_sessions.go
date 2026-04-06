package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testSessions(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "sess-user")
		sess := SeedSession(t, st, user.ID)

		found, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, sess.ID, found.ID)
		assert.Equal(t, user.ID, found.UserID)
		assert.Equal(t, "test-agent", found.UserAgent)
		assert.Equal(t, "127.0.0.1", found.IPAddress)
		assert.False(t, found.CreatedAt.IsZero())
		assert.False(t, found.ExpiresAt.IsZero())
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Sessions().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("touch", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "touch-user")
		sess := SeedSession(t, st, user.ID)

		newExpiry := time.Now().Add(48 * time.Hour)
		// LastActiveAt is used as a gate in the WHERE clause (last_active_at < ?),
		// so use a future time to ensure the condition matches.
		newActive := time.Now().Add(1 * time.Minute)
		err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sess.ID,
			ExpiresAt:    newExpiry,
			LastActiveAt: newActive,
		})
		require.NoError(t, err)

		updated, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		assert.WithinDuration(t, newExpiry, updated.ExpiresAt, time.Second)
		// The SQL sets last_active_at to strftime('now'), not the passed value.
		assert.WithinDuration(t, time.Now(), updated.LastActiveAt, 5*time.Second)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "del-user")
		sess := SeedSession(t, st, user.ID)

		n, err := st.Sessions().Delete(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Sessions().GetByID(ctx, sess.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete nonexistent returns zero", func(t *testing.T) {
		st := s.NewStore(t)
		n, err := st.Sessions().Delete(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("delete by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "del-by-user")
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		err := st.Sessions().DeleteByUser(ctx, user.ID)
		require.NoError(t, err)

		sessions, err := st.Sessions().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, sessions)
		assert.Empty(t, sessions)
	})

	t.Run("delete others", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "others-user")
		keep := SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		err := st.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
			UserID: user.ID,
			KeepID: keep.ID,
		})
		require.NoError(t, err)

		sessions, err := st.Sessions().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		assert.Equal(t, keep.ID, sessions[0].ID)
	})

	t.Run("list by user id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "list-user")
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		sessions, err := st.Sessions().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, sessions, 2)
	})

	t.Run("validate with user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)

		userID := id.Generate()
		err := st.Users().Create(ctx, store.CreateUserParams{
			ID:            userID,
			OrgID:         orgID,
			Username:      "validate-user",
			PasswordHash:  "hash",
			DisplayName:   "Val User",
			Email:         "val@example.com",
			EmailVerified: true,
			PasswordSet:   true,
			IsAdmin:       true,
		})
		require.NoError(t, err)

		sess := SeedSession(t, st, userID)

		sw, err := st.Sessions().ValidateWithUser(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, userID, sw.UserID)
		assert.Equal(t, orgID, sw.OrgID)
		assert.Equal(t, "validate-user", sw.Username)
		assert.True(t, sw.IsAdmin)
		assert.True(t, sw.EmailVerified)
	})

	t.Run("validate with user not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Sessions().ValidateWithUser(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("expired session not returned", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "expired-user")

		sessID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessID,
			UserID:    user.ID,
			ExpiresAt: time.Now().Add(-1 * time.Hour), // Already expired.
			UserAgent: "test-agent",
			IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		_, err = st.Sessions().GetByID(ctx, sessID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list all active", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user1 := SeedUser(t, st, orgID, "active-user1")
		user2 := SeedUser(t, st, orgID, "active-user2")
		SeedSession(t, st, user1.ID)
		SeedSession(t, st, user2.ID)

		sessions, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Limit: 100,
		})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(sessions), 2)

		// Verify fields are populated.
		for _, s := range sessions {
			assert.NotEmpty(t, s.ID)
			assert.NotEmpty(t, s.UserID)
			assert.NotEmpty(t, s.Username)
			assert.False(t, s.CreatedAt.IsZero())
		}
	})

	t.Run("validate expired session not found", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "expired-validate-user")

		sessID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessID,
			UserID:    user.ID,
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "test-agent",
			IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		_, err = st.Sessions().ValidateWithUser(ctx, sessID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("touch stale is no-op", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "touch-stale-user")
		sess := SeedSession(t, st, user.ID)

		// First touch with a future LastActiveAt — should succeed.
		futureActive := time.Now().Add(1 * time.Minute)
		newExpiry := time.Now().Add(48 * time.Hour)
		err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sess.ID,
			ExpiresAt:    newExpiry,
			LastActiveAt: futureActive,
		})
		require.NoError(t, err)

		after1, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		assert.WithinDuration(t, newExpiry, after1.ExpiresAt, time.Second)

		// Second touch with a past LastActiveAt — should be a no-op
		// because the session's last_active_at is already beyond this value.
		staleActive := sess.CreatedAt.Add(-1 * time.Minute)
		staleExpiry := time.Now().Add(72 * time.Hour)
		err = st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sess.ID,
			ExpiresAt:    staleExpiry,
			LastActiveAt: staleActive,
		})
		require.NoError(t, err)

		after2, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		// ExpiresAt should NOT have changed to staleExpiry.
		assert.WithinDuration(t, newExpiry, after2.ExpiresAt, time.Second)
	})

	t.Run("touch non-existent", func(t *testing.T) {
		st := s.NewStore(t)

		// Touch a non-existent session should be a no-op (no error).
		err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           "nonexistent-sess",
			ExpiresAt:    time.Now().Add(24 * time.Hour),
			LastActiveAt: time.Now(),
		})
		require.NoError(t, err)
	})

	t.Run("delete others with single session", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "single-sess-user")
		sess := SeedSession(t, st, user.ID)

		// DeleteOthers when user has only one session should keep it.
		err := st.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
			UserID: user.ID,
			KeepID: sess.ID,
		})
		require.NoError(t, err)

		sessions, err := st.Sessions().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		assert.Equal(t, sess.ID, sessions[0].ID)
	})

	t.Run("list all active empty", func(t *testing.T) {
		st := s.NewStore(t)

		sessions, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Limit: 100,
		})
		require.NoError(t, err)
		require.NotNil(t, sessions)
		assert.Empty(t, sessions)
	})

	t.Run("list all active excludes expired", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "active-expired-user")

		// Create a valid session.
		SeedSession(t, st, user.ID)

		// Create an expired session.
		expiredID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        expiredID,
			UserID:    user.ID,
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "expired-agent",
			IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		sessions, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Limit: 100,
		})
		require.NoError(t, err)
		// Only the valid session should appear.
		require.Len(t, sessions, 1)
		assert.Equal(t, user.ID, sessions[0].UserID)
	})

	t.Run("list all active with limit", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "limit-user")
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		sessions, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Limit: 2,
		})
		require.NoError(t, err)
		assert.Len(t, sessions, 2)
	})

	t.Run("validate deleted user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "validate-del-user")
		sess := SeedSession(t, st, user.ID)

		// Delete the user (soft-delete).
		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		// ValidateWithUser should return ErrNotFound for a deleted user's session.
		_, err = st.Sessions().ValidateWithUser(ctx, sess.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list all active excludes sessions of deleted users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		alive := SeedUser(t, st, orgID, "active-alive-user")
		dead := SeedUser(t, st, orgID, "active-dead-user")
		SeedSession(t, st, alive.ID)
		SeedSession(t, st, dead.ID)

		err := st.Users().Delete(ctx, dead.ID)
		require.NoError(t, err)

		sessions, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Limit: 100,
		})
		require.NoError(t, err)
		for _, s := range sessions {
			assert.NotEqual(t, dead.ID, s.UserID, "should not include sessions of deleted users")
		}
	})

	t.Run("list by user excludes expired sessions", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "list-expired-user")

		// Create a valid session.
		SeedSession(t, st, user.ID)

		// Create an expired session.
		expiredID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID: expiredID, UserID: user.ID,
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "test-agent", IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		sessions, err := st.Sessions().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, sessions, 1)
	})

	t.Run("list all active with cursor returns next page", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "cursor-user")
		// Create 3 sessions with delays to ensure distinct last_active_at.
		for i := 0; i < 3; i++ {
			if i > 0 {
				time.Sleep(1100 * time.Millisecond)
			}
			SeedSession(t, st, user.ID)
		}

		// First page.
		page1, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Limit: 2,
		})
		require.NoError(t, err)
		require.Len(t, page1, 2)

		// Second page using last item's LastActiveAt as cursor (RFC3339Nano).
		cursor := page1[len(page1)-1].LastActiveAt.UTC().Format(time.RFC3339Nano)
		page2, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			Cursor: cursor,
			Limit:  2,
		})
		require.NoError(t, err)
		require.Len(t, page2, 1)

		// No overlap between pages.
		page1IDs := map[string]bool{page1[0].ID: true, page1[1].ID: true}
		assert.False(t, page1IDs[page2[0].ID], "page2 should not overlap with page1")
	})

	t.Run("duplicate session id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org", true)
		user := SeedUser(t, st, orgID, "dup-sess-user")
		sess := SeedSession(t, st, user.ID)

		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID: sess.ID, UserID: user.ID,
			ExpiresAt: time.Now().Add(24 * time.Hour),
			UserAgent: "test", IPAddress: "127.0.0.1",
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})
}

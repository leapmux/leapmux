package storetest

import (
	"errors"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testSessions(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
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
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "touch-user")
		sess := SeedSession(t, st, user.ID)

		newExpiry := time.Now().Add(48 * time.Hour)
		// LastActiveAt is used as a gate in the WHERE clause (last_active_at < ?),
		// so use a future time to ensure the condition matches.
		newActive := time.Now().Add(1 * time.Minute)
		n, err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sess.ID,
			ExpiresAt:    newExpiry,
			LastActiveAt: newActive,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n, "a matched touch reports one updated row")

		updated, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		assert.WithinDuration(t, newExpiry, updated.ExpiresAt, time.Second)
		// The SQL sets last_active_at to strftime('now'), not the passed value.
		assert.WithinDuration(t, time.Now(), updated.LastActiveAt, 5*time.Second)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "del-user")
		sess := SeedSession(t, st, user.ID)

		n, err := st.Sessions().Delete(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Sessions().GetByID(ctx, sess.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete publishes session revocation event", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "del-event-user")
		sess := SeedSession(t, st, user.ID)

		n, err := st.Sessions().Delete(ctx, sess.ID)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, store.RevocationEventKindSession, events[0].Event.Kind)
		assert.Equal(t, sess.ID, events[0].Event.SubjectID)
		assert.Equal(t, user.ID, events[0].Event.UserID)
	})

	t.Run("delete nonexistent returns zero", func(t *testing.T) {
		st := s.NewStore(t)
		n, err := st.Sessions().Delete(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("delete by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "del-by-user")
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		err := st.Sessions().DeleteByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)

		sessions := ListAllSessions(t, st, user.ID)
		require.NotNil(t, sessions)
		assert.Empty(t, sessions)
	})

	t.Run("delete others", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "others-user")
		keep := SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		err := st.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
			UserID: userid.MustNew(user.ID),
			KeepID: keep.ID,
		})
		require.NoError(t, err)

		sessions := ListAllSessions(t, st, user.ID)
		require.Len(t, sessions, 1)
		assert.Equal(t, keep.ID, sessions[0].ID)
	})

	t.Run("list by user id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "list-user")
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		sessions := ListAllSessions(t, st, user.ID)
		assert.Len(t, sessions, 2)
	})

	t.Run("validate with user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")

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

	t.Run("user revocation invalidates stale session generation", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "generation-user")
		sess := SeedSession(t, st, user.ID)

		sw, err := st.Sessions().ValidateWithUser(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), sw.AuthGeneration)

		_, err = st.Users().RevokeUserTokens(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		_, err = st.Sessions().ValidateWithUser(ctx, sess.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		n, err := st.Sessions().RefreshAuthGeneration(ctx, store.RefreshSessionAuthGenerationParams{
			SessionID: sess.ID,
			UserID:    userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		sw, err = st.Sessions().ValidateWithUser(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), sw.AuthGeneration)
	})

	t.Run("validate with user not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Sessions().ValidateWithUser(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("expired session not returned", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "expired-user")

		sessID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessID,
			UserID:    userid.MustNew(user.ID),
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
		orgID := SeedOrg(t, st, "sess-org")
		user1 := SeedUser(t, st, orgID, "active-user1")
		user2 := SeedUser(t, st, orgID, "active-user2")
		SeedSession(t, st, user1.ID)
		SeedSession(t, st, user2.ID)

		page, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Limit: 100},
		})
		require.NoError(t, err)
		sessions := page.Rows
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
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "expired-validate-user")

		sessID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessID,
			UserID:    userid.MustNew(user.ID),
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
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "touch-stale-user")
		sess := SeedSession(t, st, user.ID)

		// First touch with a future LastActiveAt — should succeed.
		futureActive := time.Now().Add(1 * time.Minute)
		newExpiry := time.Now().Add(48 * time.Hour)
		n, err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sess.ID,
			ExpiresAt:    newExpiry,
			LastActiveAt: futureActive,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n, "the first (matching) touch updates one row")

		after1, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		assert.WithinDuration(t, newExpiry, after1.ExpiresAt, time.Second)

		// Second touch with a past LastActiveAt — should be a no-op
		// because the session's last_active_at is already beyond this value.
		// It must report zero rows so the interceptor does not slide in-memory
		// lifecycle deadlines past the un-advanced DB expiry (see touchSession).
		staleActive := sess.CreatedAt.Add(-1 * time.Minute)
		staleExpiry := time.Now().Add(72 * time.Hour)
		n, err = st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sess.ID,
			ExpiresAt:    staleExpiry,
			LastActiveAt: staleActive,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n, "a stale (below-threshold) touch matches no row")

		after2, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		// ExpiresAt should NOT have changed to staleExpiry.
		assert.WithinDuration(t, newExpiry, after2.ExpiresAt, time.Second)
	})

	t.Run("touch non-existent", func(t *testing.T) {
		st := s.NewStore(t)

		// Touch a non-existent session should be a no-op (no error) and report
		// zero rows updated.
		n, err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           "nonexistent-sess",
			ExpiresAt:    time.Now().Add(24 * time.Hour),
			LastActiveAt: time.Now(),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n, "touching a missing session matches no row")
	})

	t.Run("touch rolls back with transaction", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "touch-tx-user")
		sess := SeedSession(t, st, user.ID)

		rollbackErr := errors.New("rollback touch")
		newExpiry := time.Now().Add(48 * time.Hour)
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			n, err := tx.Sessions().Touch(ctx, store.TouchSessionParams{
				ID:           sess.ID,
				ExpiresAt:    newExpiry,
				LastActiveAt: time.Now().Add(1 * time.Minute),
			})
			require.NoError(t, err)
			assert.Equal(t, int64(1), n, "the in-transaction touch updates one row before rollback")
			return rollbackErr
		})
		require.ErrorIs(t, err, rollbackErr)

		after, err := st.Sessions().GetByID(ctx, sess.ID)
		require.NoError(t, err)
		assert.WithinDuration(t, sess.ExpiresAt, after.ExpiresAt, time.Second)
	})

	t.Run("delete others with single session", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "single-sess-user")
		sess := SeedSession(t, st, user.ID)

		// DeleteOthers when user has only one session should keep it.
		err := st.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
			UserID: userid.MustNew(user.ID),
			KeepID: sess.ID,
		})
		require.NoError(t, err)

		sessions := ListAllSessions(t, st, user.ID)
		require.Len(t, sessions, 1)
		assert.Equal(t, sess.ID, sessions[0].ID)
	})

	t.Run("list all active empty", func(t *testing.T) {
		st := s.NewStore(t)

		page, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Limit: 100},
		})
		require.NoError(t, err)
		require.NotNil(t, page.Rows)
		assert.Empty(t, page.Rows)
	})

	t.Run("list all active excludes expired", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "active-expired-user")

		// Create a valid session.
		SeedSession(t, st, user.ID)

		// Create an expired session.
		expiredID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        expiredID,
			UserID:    userid.MustNew(user.ID),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "expired-agent",
			IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		page, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Limit: 100},
		})
		require.NoError(t, err)
		// Only the valid session should appear.
		require.Len(t, page.Rows, 1)
		assert.Equal(t, user.ID, page.Rows[0].UserID)
	})

	t.Run("list all active with limit", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "limit-user")
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)
		SeedSession(t, st, user.ID)

		page, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Limit: 2},
		})
		require.NoError(t, err)
		assert.Len(t, page.Rows, 2)
	})

	t.Run("validate deleted user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "validate-del-user")
		sess := SeedSession(t, st, user.ID)

		// Delete the user (soft-delete).
		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		// ValidateWithUser should return ErrNotFound for a deleted user's session.
		_, err = st.Sessions().ValidateWithUser(ctx, sess.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list all active surfaces sessions of soft-deleted users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		alive := SeedUser(t, st, orgID, "active-alive-user")
		dead := SeedUser(t, st, orgID, "active-dead-user")
		SeedSession(t, st, alive.ID)
		SeedSession(t, st, dead.ID)

		err := st.Users().Delete(ctx, dead.ID)
		require.NoError(t, err)

		page, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Limit: 100},
		})
		require.NoError(t, err)
		// The soft-deleted user's still-live session must surface for operator audit
		// (LEFT JOIN with u.deleted_at IS NULL in the join condition, NOT an INNER
		// JOIN that drops the row), with UserDeleted set and an empty username
		// since the soft-deleted user row no longer satisfies the join -- the
		// presentation layer decides the placeholder. This is the audit surface
		// for "every active session"; hiding soft-deleted owners' sessions
		// would let a just-deleted user's sessions linger invisibly until expiry.
		var sawDead bool
		for _, s := range page.Rows {
			if s.UserID == dead.ID {
				sawDead = true
				assert.True(t, s.UserDeleted, "soft-deleted owner's session must surface with UserDeleted set")
				assert.Empty(t, s.Username, "soft-deleted owner must surface with an empty username")
			}
		}
		assert.True(t, sawDead, "soft-deleted user's live session must be listed for audit (LEFT JOIN)")
	})

	t.Run("list by user excludes expired sessions", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "list-expired-user")

		// Create a valid session.
		SeedSession(t, st, user.ID)

		// Create an expired session.
		expiredID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID: expiredID, UserID: userid.MustNew(user.ID),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "test-agent", IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		sessions := ListAllSessions(t, st, user.ID)
		assert.Len(t, sessions, 1)
	})

	t.Run("list by user pages by last_active_at", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "sess-page-user")
		other := SeedUser(t, st, orgID, "sess-page-other")
		s1 := SeedSession(t, st, user.ID)
		s2 := SeedSession(t, st, user.ID)
		s3 := SeedSession(t, st, user.ID)
		// The other user's session is the most recent overall; it must not
		// leak into user's pages even across a cursor boundary.
		leak := SeedSession(t, st, other.ID)
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, s1.ID, base.Add(1*time.Second)))
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, s2.ID, base.Add(2*time.Second)))
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, s3.ID, base.Add(3*time.Second)))
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, leak.ID, base.Add(10*time.Second)))

		// ListByUserID pages on (last_active_at DESC, id DESC) -- NOT
		// created_at; a wrong PageCursor column or ORDER BY would misorder or
		// drop rows across this boundary.
		page, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{UserID: userid.MustNew(user.ID), PageParams: store.PageParams{Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		assert.Equal(t, s3.ID, page.Rows[0].ID)
		assert.Equal(t, s2.ID, page.Rows[1].ID)
		require.True(t, page.HasMore())

		page, err = st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{UserID: userid.MustNew(user.ID), PageParams: store.PageParams{Cursor: page.NextCursor, Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, s1.ID, page.Rows[0].ID)
		assert.False(t, page.HasMore())
		assert.Empty(t, page.NextCursor)
	})

	t.Run("list all active with cursor returns next page", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "cursor-user")
		// Create 3 sessions with delays to ensure distinct last_active_at.
		for i := 0; i < 3; i++ {
			if i > 0 {
				time.Sleep(1100 * time.Millisecond)
			}
			SeedSession(t, st, user.ID)
		}

		// First page.
		res1, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Limit: 2},
		})
		require.NoError(t, err)
		assert.True(t, res1.HasMore())
		page1 := res1.Rows
		require.Len(t, page1, 2)

		// Second page using last item's composite (last_active_at, id) cursor.
		cursor := store.EncodeCursor(page1[len(page1)-1].LastActiveAt, page1[len(page1)-1].ID)
		res2, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			PageParams: store.PageParams{Cursor: cursor, Limit: 2},
		})
		require.NoError(t, err)
		page2 := res2.Rows
		require.Len(t, page2, 1)

		// No overlap between pages.
		page1IDs := map[string]bool{page1[0].ID: true, page1[1].ID: true}
		assert.False(t, page1IDs[page2[0].ID], "page2 should not overlap with page1")
	})

	t.Run("list all active cursor survives same-millisecond tie", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-tie-org")
		user := SeedUser(t, st, orgID, "sess-tie-user")

		// Three sessions: two share an identical last_active_at millisecond and
		// the third is strictly older. The cursor orders on last_active_at, so
		// the tie is forced on that column.
		tie := time.Now().UTC().Truncate(time.Millisecond)
		older := SeedSession(t, st, user.ID)
		tiedA := SeedSession(t, st, user.ID)
		tiedB := SeedSession(t, st, user.ID)
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, older.ID, tie.Add(-time.Second)))
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, tiedA.ID, tie))
		require.NoError(t, st.TestHelper().SetLastActiveAt(ctx, tiedB.ID, tie))

		seen := pageThroughByOne(t, func(cursor string) (store.Page[store.ActiveSession], error) {
			return st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
				PageParams: store.PageParams{Cursor: cursor, Limit: 1},
			})
		})
		assert.ElementsMatch(t, []string{older.ID, tiedA.ID, tiedB.ID}, seen,
			"same-millisecond sessions must not be skipped across page boundaries")
	})

	t.Run("duplicate session id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "sess-org")
		user := SeedUser(t, st, orgID, "dup-sess-user")
		sess := SeedSession(t, st, user.ID)

		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID: sess.ID, UserID: userid.MustNew(user.ID),
			ExpiresAt: time.Now().Add(24 * time.Hour),
			UserAgent: "test", IPAddress: "127.0.0.1",
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("list all active rejects malformed cursor with ErrInvalidCursor", func(t *testing.T) {
		// Pins the store-level cursor-decode contract for a non-ListWorkers
		// method: a stale, truncated, or hand-edited --cursor must surface as
		// store.ErrInvalidCursor (not a generic store fault and not a silent
		// restart from page one) so the RPC and CLI layers can classify it as
		// bad client input. worker_mgmt_service_test.go covers the ListWorkers
		// RPC's InvalidArgument mapping; this covers the five sibling list
		// methods' shared decode path through store.ParseCursor.
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cursor-err-org")
		SeedUser(t, st, orgID, "cursor-err-user")

		cases := map[string]string{
			"missing delimiter": "2026-07-20T12:34:56.789Z",
			"empty id":          "2026-07-20T12:34:56.789Z_",
			"bad timestamp":     "not-a-time_abc",
		}
		for name, bad := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
					PageParams: store.PageParams{Cursor: bad, Limit: 10},
				})
				assert.ErrorIs(t, err, store.ErrInvalidCursor,
					"cursor %q must surface as ErrInvalidCursor, not a generic store fault", bad)
			})
		}
	})
}

// The per-user MUTATIONS must REFUSE an unminted caller, not no-op.
//
// These are the destructive half of the store's per-user surface, and their
// polarity is inverted from an ownership read: binding "" would address every
// blank-owner row, while silently affecting nothing would report a successful
// revocation that revoked nothing -- an operator's one containment action
// failing quietly. Both are worse than an error, so a zero id is refused.
//
// The methods below are exactly those whose ONLY channel is an error. A method
// that also returns a rows-affected count is deliberately absent: there, a
// refusal is already distinguishable as 0 rows, and every caller maps that to
// NotFound the same way it maps "someone else's row".
func (s *Suite) testZeroIDMutationsRefused(t *testing.T) {
	st := s.NewStore(t)
	orgID := SeedOrg(t, st, "zero-bulk-org")
	user := SeedUser(t, st, orgID, "zero-bulk-user")
	ownerID := userid.MustNew(user.ID)

	// Bulk: every row belonging to one user.
	require.ErrorIs(t, st.Sessions().DeleteByUser(ctx, userid.UserID{}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.Workspaces().SoftDeleteAllByUser(ctx, userid.UserID{}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.OAuthTokens().DeleteByUser(ctx, userid.UserID{}), store.ErrInvalidArgument)

	_, err := st.Users().RevokeUserTokens(ctx, userid.UserID{})
	require.ErrorIs(t, err, store.ErrInvalidArgument)
	_, err = st.APITokens().RevokeByUser(ctx, userid.UserID{})
	require.ErrorIs(t, err, store.ErrInvalidArgument)
	_, err = st.DelegationTokens().RevokeByUser(ctx, userid.UserID{})
	require.ErrorIs(t, err, store.ErrInvalidArgument)

	// Scoped: one row, or one user's rows within a narrower scope. These used
	// to return nil -- reporting that a mutation the store refused had
	// SUCCEEDED. ChangePassword is the sharpest case: it calls DeleteOthers and
	// answers 200, so a nil here told the user every other device had been
	// signed out while every session stayed live.
	require.ErrorIs(t, st.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
		UserID: userid.UserID{}, KeepID: "keep",
	}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
		UserID: userid.UserID{}, ProviderID: "github",
	}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.OAuthTokens().DeleteByUserAndProvider(ctx, store.DeleteOAuthTokensByUserAndProviderParams{
		UserID: userid.UserID{}, ProviderID: "github",
	}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.WorkspaceSections().UpdatePosition(ctx, store.UpdateWorkspaceSectionPositionParams{
		ID: "sec", UserID: userid.UserID{}, Position: "a",
	}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.WorkspaceSections().UpdateSidebarPosition(ctx, store.UpdateWorkspaceSectionSidebarPositionParams{
		ID: "sec", UserID: userid.UserID{}, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT, Position: "a",
	}), store.ErrInvalidArgument)
	require.ErrorIs(t, st.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
		UserID: userid.UserID{}, WorkspaceID: "ws",
	}), store.ErrInvalidArgument)

	// Control: each still works for a real user, so the refusals above are
	// about the id rather than the operation being broken. Without a control a
	// refusal assertion passes for any reason at all, including the method
	// having become unconditionally broken.
	require.NoError(t, st.Sessions().DeleteByUser(ctx, ownerID))
	n, err := st.Users().RevokeUserTokens(ctx, ownerID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "control: a real user's tokens revoke")

	// The scoped mutations address no row for this fixture, but a real id must
	// reach the query rather than being refused before it.
	require.NoError(t, st.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
		UserID: ownerID, KeepID: "keep",
	}), "control: a real caller reaches the query")
	require.NoError(t, st.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
		UserID: ownerID, ProviderID: "github",
	}), "control: a real caller reaches the query")
	require.NoError(t, st.OAuthTokens().DeleteByUserAndProvider(ctx, store.DeleteOAuthTokensByUserAndProviderParams{
		UserID: ownerID, ProviderID: "github",
	}), "control: a real caller reaches the query")
	require.NoError(t, st.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
		UserID: ownerID, WorkspaceID: "ws",
	}), "control: a real caller reaches the query")
}

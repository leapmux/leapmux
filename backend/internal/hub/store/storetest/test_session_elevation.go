package storetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// testSessionElevation is the cross-dialect conformance suite for session
// elevation ("sudo mode").
//
// It runs against every backend because the three statements are three
// different pieces of SQL: SQLite clamps with min()/max() over canonical
// strings, Postgres with LEAST/GREATEST over timestamptz, MySQL with the
// same functions over DATETIME(3) plus a microsecond interval. A cap that
// holds on one dialect and not another is exactly the drift a shared suite
// exists to catch.
func (s *Suite) testSessionElevation(t *testing.T) {
	now := func() time.Time { return time.Now().UTC() }

	t.Run("a fresh session carries no elevation", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-fresh")
		sess := SeedSession(t, st, user.ID)

		row, err := st.Sessions().GetByID(ctx, sess.ID, now())
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt)
		assert.Nil(t, row.ElevationExpiresAt)

		joined, err := st.Sessions().ValidateWithUser(ctx, sess.ID, now())
		require.NoError(t, err)
		assert.Nil(t, joined.ElevationProvenAt, "the hot auth path must carry the same absence")
		assert.Nil(t, joined.ElevationExpiresAt)
	})

	t.Run("elevate stamps both columns and the hot path reads them back", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-grant")
		sess := SeedSession(t, st, user.ID)
		at := now()

		n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)

		joined, err := st.Sessions().ValidateWithUser(ctx, sess.ID, at)
		require.NoError(t, err)
		require.NotNil(t, joined.ElevationProvenAt)
		require.NotNil(t, joined.ElevationExpiresAt)
		assert.WithinDuration(t, at, *joined.ElevationProvenAt, time.Second)
		assert.WithinDuration(t, at.Add(2*time.Hour), *joined.ElevationExpiresAt, time.Second)
	})

	t.Run("the grant clamps to the absolute cap", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-grant-cap")
		sess := SeedSession(t, st, user.ID)
		at := now()
		ceiling := at.Add(store.ElevationMaxTotal)

		// The caller asks for a week. The STORE owns the ceiling, so the row
		// must carry the cap and not the request. The slide cannot repair an
		// over-long grant afterwards -- it only moves a deadline FORWARD --
		// so the grant is the writer that has to hold the cap here.
		n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(7 * 24 * time.Hour),
		}, at)
		require.NoError(t, err)
		require.EqualValues(t, 1, n, "an over-long request still elevates -- clamped, not refused")

		row, err := st.Sessions().GetByID(ctx, sess.ID, at)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationProvenAt)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, at, *row.ElevationProvenAt, time.Second,
			"the anchor is the caller's instant, untouched by the clamp")
		assert.WithinDuration(t, ceiling, *row.ElevationExpiresAt, time.Second,
			"the grant must store the cap, not the week the caller asked for")
	})

	t.Run("elevate refuses another user's session", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "elev-owner")
		other := SeedUser(t, st, "elev-other")
		sess := SeedSession(t, st, owner.ID)
		at := now()

		n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(other.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n, "the owner equality lives in the statement, not in a caller's check")

		row, err := st.Sessions().GetByID(ctx, sess.ID, at)
		require.NoError(t, err)
		assert.Nil(t, row.ElevationExpiresAt)
	})

	t.Run("elevate refuses an expired session", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-expired")
		sess := SeedSession(t, st, user.ID)
		// SeedSession expires 24h out; judge it from a clock a week later.
		later := now().Add(7 * 24 * time.Hour)

		n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  later,
			ElevationExpiresAt: later.Add(time.Hour),
		}, later)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n, "a dead session must not be elevatable")
	})

	t.Run("the slide moves the deadline forward and never the anchor", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-slide")
		sess := SeedSession(t, st, user.ID)
		at := now().Add(-time.Hour)

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, now())
		require.NoError(t, err)

		n, err := st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: now().Add(2 * time.Hour),
		}, now())
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)

		row, err := st.Sessions().GetByID(ctx, sess.ID, now())
		require.NoError(t, err)
		require.NotNil(t, row.ElevationProvenAt)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, at, *row.ElevationProvenAt, time.Second,
			"the anchor must NEVER slide: it is what fixes the absolute cap")
		assert.WithinDuration(t, now().Add(2*time.Hour), *row.ElevationExpiresAt, time.Minute)
	})

	t.Run("the slide clamps to the absolute cap", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-cap")
		sess := SeedSession(t, st, user.ID)
		// Anchored 7.5h ago with an 8h cap: only 30 minutes of ceiling left,
		// while the caller asks for two more hours. The stored deadline
		// starts BELOW the ceiling, so a slide that lands on the ceiling is
		// visible as a change rather than as the value that was already there
		// -- an upper-limit assertion against a pre-set ceiling passes even
		// when the UPDATE matches no row at all.
		anchor := now().Add(-7*time.Hour - 30*time.Minute)
		ceiling := anchor.Add(8 * time.Hour)
		before := now().Add(10 * time.Minute)

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  anchor,
			ElevationExpiresAt: before,
		}, now())
		require.NoError(t, err)

		n, err := st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: now().Add(2 * time.Hour),
		}, now())
		require.NoError(t, err)
		assert.EqualValues(t, 1, n, "the slide must actually write the row it clamps")

		row, err := st.Sessions().GetByID(ctx, sess.ID, now())
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		// Both limits. The upper one is the clamp; the lower one is what
		// separates "clamped to the ceiling" from "wrote nothing" and from
		// "collapsed back to the anchor".
		assert.False(t, row.ElevationExpiresAt.After(ceiling.Add(time.Second)),
			"the clamp lives in SQL, so no caller can extend past the ceiling (got %s, ceiling %s)",
			row.ElevationExpiresAt, ceiling)
		assert.WithinDuration(t, ceiling, *row.ElevationExpiresAt, time.Second,
			"the slide must reach the ceiling, not stop short of it or collapse to the anchor")
	})

	t.Run("the slide is monotone and never resurrects a lapsed window", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-monotone")
		sess := SeedSession(t, st, user.ID)
		at := now()

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)

		// A LATE request asking for an earlier deadline must not shorten it.
		_, err = st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: at.Add(time.Minute),
		}, at)
		require.NoError(t, err)
		row, err := st.Sessions().GetByID(ctx, sess.ID, at)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, at.Add(2*time.Hour), *row.ElevationExpiresAt, time.Second)

		// A slide must not revive a session whose window ALREADY closed:
		// otherwise a mutation would grant the elevation the hub refused it.
		lapsed := SeedSession(t, st, user.ID)
		_, err = st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          lapsed.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at.Add(-3 * time.Hour),
			ElevationExpiresAt: at.Add(-time.Hour),
		}, at)
		require.NoError(t, err)
		n, err := st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      lapsed.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n)
		row, err = st.Sessions().GetByID(ctx, lapsed.ID, at)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.True(t, row.ElevationExpiresAt.Before(at), "a lapsed window must stay lapsed")
	})

	t.Run("the slide grants nothing to a session that never elevated", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-never")
		sess := SeedSession(t, st, user.ID)

		n, err := st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: now().Add(2 * time.Hour),
		}, now())
		require.NoError(t, err)
		assert.EqualValues(t, 0, n)

		row, err := st.Sessions().GetByID(ctx, sess.ID, now())
		require.NoError(t, err)
		assert.Nil(t, row.ElevationExpiresAt,
			"a slide must never be able to CREATE an elevation nobody proved")
	})

	t.Run("drop clears both columns and is idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-drop")
		sess := SeedSession(t, st, user.ID)
		at := now()

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(time.Hour),
		}, at)
		require.NoError(t, err)

		n, err := st.Sessions().DropElevation(ctx, store.DropSessionElevationParams{
			SessionID: sess.ID,
			UserID:    userid.MustNew(user.ID),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)

		row, err := st.Sessions().GetByID(ctx, sess.ID, at)
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt)
		assert.Nil(t, row.ElevationExpiresAt)

		n, err = st.Sessions().DropElevation(ctx, store.DropSessionElevationParams{
			SessionID: sess.ID,
			UserID:    userid.MustNew(user.ID),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n, "dropping twice is the state the caller asked for, not an error")
	})

	t.Run("drop refuses another user's session", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "elev-drop-owner")
		other := SeedUser(t, st, "elev-drop-other")
		sess := SeedSession(t, st, owner.ID)
		at := now()

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(owner.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(time.Hour),
		}, at)
		require.NoError(t, err)

		n, err := st.Sessions().DropElevation(ctx, store.DropSessionElevationParams{
			SessionID: sess.ID,
			UserID:    userid.MustNew(other.ID),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n)

		row, err := st.Sessions().GetByID(ctx, sess.ID, at)
		require.NoError(t, err)
		assert.NotNil(t, row.ElevationExpiresAt, "another user must not be able to end this elevation")
	})

	// The slide carries the SAME owner term Elevate and Drop do, and it is
	// the statement every sensitive action runs -- so a dialect that lost
	// the term would let one account extend another's window on every
	// mutation, and nothing in the suite would say so.
	t.Run("the slide refuses another user's session", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "elev-slide-owner")
		other := SeedUser(t, st, "elev-slide-other")
		sess := SeedSession(t, st, owner.ID)
		at := now()

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(owner.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(time.Hour),
		}, at)
		require.NoError(t, err)

		n, err := st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(other.ID),
			WindowDeadline: at.Add(4 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n)

		row, err := st.Sessions().GetByID(ctx, sess.ID, at)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, at.Add(time.Hour), *row.ElevationExpiresAt, time.Second,
			"another user must not be able to extend this elevation")
	})

	// A fresh ceremony restarts BOTH columns, which is what makes the
	// eight-hour cap a per-elevation ceiling rather than a per-session one:
	// a user who verifies again gets a whole new maximum window instead of
	// adding to the old anchor. Only the deadline moving would leave the
	// second elevation capped by the FIRST ceremony's clock.
	t.Run("a second elevation restarts the anchor, not only the deadline", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-restart")
		sess := SeedSession(t, st, user.ID)
		first := now()

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  first,
			ElevationExpiresAt: first.Add(time.Hour),
		}, first)
		require.NoError(t, err)

		second := first.Add(3 * time.Hour)
		n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  second,
			ElevationExpiresAt: second.Add(time.Hour),
		}, second)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)

		row, err := st.Sessions().GetByID(ctx, sess.ID, second)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationProvenAt)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, second, *row.ElevationProvenAt, time.Second,
			"the anchor must move to the new ceremony, or the cap still counts from the old one")
		assert.WithinDuration(t, second.Add(time.Hour), *row.ElevationExpiresAt, time.Second)

		// And the cap now counts from the SECOND ceremony: a slide may reach
		// eight hours past it, which the first anchor would have refused.
		_, err = st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: second.Add(7 * time.Hour),
		}, second.Add(30*time.Minute))
		require.NoError(t, err)
		row, err = st.Sessions().GetByID(ctx, sess.ID, second)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, second.Add(7*time.Hour), *row.ElevationExpiresAt, time.Second,
			"the cap is measured from the second anchor, so seven hours past it is admitted")
	})

	// Elevate and Drop emit the durable user_info signal so a cross-process
	// UserInfo cache re-reads the deadline. The SLIDE deliberately does not:
	// a stale SHORTER deadline fails closed and expires on its own, and an
	// event per sensitive action would be pure churn.
	t.Run("grant and drop emit a user_info event; the slide does not", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "elev-events")
		sess := SeedSession(t, st, user.ID)
		at := now()

		// publishedKinds drains whatever is pending and reports the kinds,
		// so each step below observes ONLY the events it caused.
		publishedKinds := func() []string {
			_, err := st.RevocationEvents().PublishPending(ctx, 100)
			require.NoError(t, err)
			events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 100)
			require.NoError(t, err)
			return store.MapSlice(events, func(e store.PublishedRevocationEvent) string { return e.Event.Kind })
		}
		baseline := len(publishedKinds())

		_, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          sess.ID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		afterGrant := publishedKinds()
		require.Len(t, afterGrant, baseline+1, "the grant must emit exactly one event")
		assert.Equal(t, store.RevocationEventKindUserInfo, afterGrant[len(afterGrant)-1],
			"a grant drops the cached UserInfo; it must NOT be a session revocation, which would sign the user out")

		_, err = st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sess.ID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: at.Add(3 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.Len(t, publishedKinds(), len(afterGrant),
			"a slide emits nothing: a stale SHORTER deadline fails closed")

		_, err = st.Sessions().DropElevation(ctx, store.DropSessionElevationParams{
			SessionID: sess.ID,
			UserID:    userid.MustNew(user.ID),
		}, at)
		require.NoError(t, err)
		afterDrop := publishedKinds()
		require.Len(t, afterDrop, len(afterGrant)+1, "the drop must emit exactly one event")
		assert.Equal(t, store.RevocationEventKindUserInfo, afterDrop[len(afterDrop)-1],
			"a drop MUST invalidate: a cached longer deadline fails open")
	})
}

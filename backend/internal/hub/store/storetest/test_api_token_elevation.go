package storetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// testAPITokenElevation is the cross-dialect conformance suite for a
// COMMAND-LINE credential's step-up window.
//
// The api_tokens twin of testSessionElevation, and it runs for the same
// reason: the three statements are three different pieces of SQL, and a cap
// that holds on one dialect and not another is exactly the drift a shared
// suite exists to catch. The cases here are the ones that differ from a
// session's, plus the two whose failure would be silent -- the absolute cap
// and the owner equality.
func (s *Suite) testAPITokenElevation(t *testing.T) {
	now := func() time.Time { return time.Now().UTC() }

	seedToken := func(t *testing.T, st store.Store, userID string) string {
		t.Helper()
		tokenID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           userid.MustNew(userID),
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "operator-laptop",
			GrantedScopes:    authscope.NonAdminGrant().String(),
			SecretHash:       []byte("hash"),
		}))
		return tokenID
	}

	t.Run("a fresh credential carries no elevation", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tok-elev-fresh")
		tokenID := seedToken(t, st, user.ID)

		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt)
		assert.Nil(t, row.ElevationExpiresAt)
	})

	t.Run("elevate stamps both columns, and drop clears both", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tok-elev-grant")
		tokenID := seedToken(t, st, user.ID)
		at := now()

		n, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
			TokenID:            tokenID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)

		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationProvenAt)
		require.NotNil(t, row.ElevationExpiresAt)
		// Through the same reader the admission uses, which refuses half a
		// stored pair.
		assert.True(t, auth.NewElevation(row.ElevationProvenAt, row.ElevationExpiresAt).IsCurrent(at))

		dropped, err := st.APITokens().DropElevation(ctx, store.DropAPITokenElevationParams{
			TokenID: tokenID,
			UserID:  userid.MustNew(user.ID),
		}, now())
		require.NoError(t, err)
		assert.EqualValues(t, 1, dropped)

		row, err = st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt, "both columns clear together, or the pair is unreadable")
		assert.Nil(t, row.ElevationExpiresAt)
	})

	t.Run("the grant clamps to the absolute cap", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tok-elev-grant-cap")
		tokenID := seedToken(t, st, user.ID)
		at := now()
		ceiling := at.Add(store.ElevationMaxTotal)

		// The same rule a session gets, on the credential file: the STORE
		// owns the ceiling, so a caller that asks for a week gets the cap.
		// The slide cannot repair an over-long grant afterwards, because it
		// only moves a deadline FORWARD.
		n, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
			TokenID:            tokenID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(7 * 24 * time.Hour),
		}, at)
		require.NoError(t, err)
		require.EqualValues(t, 1, n, "an over-long request still elevates -- clamped, not refused")

		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationProvenAt)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.WithinDuration(t, at, *row.ElevationProvenAt, time.Second,
			"the anchor is the caller's instant, untouched by the clamp")
		assert.WithinDuration(t, ceiling, *row.ElevationExpiresAt, time.Second,
			"the grant must store the cap, not the week the caller asked for")
	})

	// The owner equality lives INSIDE each statement, which is what makes a
	// grant that specifies somebody else's credential a no-op rather than a
	// check the handler has to remember.
	t.Run("another user's credential is untouched", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "tok-elev-owner")
		stranger := SeedUser(t, st, "tok-elev-stranger")
		tokenID := seedToken(t, st, owner.ID)
		at := now()

		n, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
			TokenID:            tokenID,
			UserID:             userid.MustNew(stranger.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n, "the owner equality is in the statement")

		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.Nil(t, row.ElevationExpiresAt)
	})

	// A REVOKED credential cannot be elevated. It authenticates nothing, so a
	// window on it would be a window on a credential the hub already refused.
	t.Run("a revoked credential cannot be elevated", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tok-elev-revoked")
		tokenID := seedToken(t, st, user.ID)
		revoked, err := st.APITokens().Revoke(ctx, tokenID)
		require.NoError(t, err)
		require.EqualValues(t, 1, revoked)

		at := now()
		n, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
			TokenID:            tokenID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at,
			ElevationExpiresAt: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n)
	})

	t.Run("the slide clamps to the absolute cap", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tok-elev-cap")
		tokenID := seedToken(t, st, user.ID)
		// Anchored 7.5h ago with an 8h cap: only 30 minutes of ceiling left,
		// while the caller asks for two more hours. The stored deadline
		// starts BELOW the ceiling, so a slide that lands on the ceiling is
		// visible as a change rather than as the value that was already
		// there -- an upper-limit assertion against a pre-set ceiling passes
		// even when the UPDATE matches no row at all.
		anchor := now().Add(-7*time.Hour - 30*time.Minute)
		ceiling := anchor.Add(8 * time.Hour)

		_, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
			TokenID:            tokenID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  anchor,
			ElevationExpiresAt: now().Add(10 * time.Minute),
		}, now())
		require.NoError(t, err)

		n, err := st.APITokens().SlideElevation(ctx, store.SlideAPITokenElevationParams{
			TokenID:        tokenID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: now().Add(2 * time.Hour),
		}, now())
		require.NoError(t, err)
		assert.EqualValues(t, 1, n, "the slide must actually write the row it clamps")

		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		require.NotNil(t, row.ElevationProvenAt)
		assert.WithinDuration(t, anchor, *row.ElevationProvenAt, time.Second,
			"the anchor must NEVER slide: it is what fixes the absolute cap")
		// Both limits. The upper one is the clamp; the lower one is what
		// separates "clamped to the ceiling" from "wrote nothing".
		assert.False(t, row.ElevationExpiresAt.After(ceiling.Add(time.Second)),
			"the clamp lives in SQL, so no caller can extend past the ceiling (got %s, ceiling %s)",
			row.ElevationExpiresAt, ceiling)
		assert.WithinDuration(t, ceiling, *row.ElevationExpiresAt, time.Second,
			"the slide must reach the ceiling, not stop short of it or collapse to the anchor")
	})

	t.Run("the slide never resurrects a lapsed window", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tok-elev-lapsed")
		tokenID := seedToken(t, st, user.ID)
		at := now()

		_, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
			TokenID:            tokenID,
			UserID:             userid.MustNew(user.ID),
			ElevationProvenAt:  at.Add(-3 * time.Hour),
			ElevationExpiresAt: at.Add(-time.Hour),
		}, at.Add(-3*time.Hour))
		require.NoError(t, err)

		n, err := st.APITokens().SlideElevation(ctx, store.SlideAPITokenElevationParams{
			TokenID:        tokenID,
			UserID:         userid.MustNew(user.ID),
			WindowDeadline: at.Add(2 * time.Hour),
		}, at)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n, "a lapsed window is re-proven, never extended")

		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		require.NotNil(t, row.ElevationExpiresAt)
		assert.True(t, row.ElevationExpiresAt.Before(at))
	})
}

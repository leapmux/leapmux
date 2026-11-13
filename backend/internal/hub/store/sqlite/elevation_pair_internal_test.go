package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	storetest "github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestElevationColumnsAreWritableOnlyAsAPair verifies the schema constraint
// for the elevation columns. All three schemas require elevation_proven_at
// and elevation_expires_at to be both NULL or both present. auth.NewElevation
// also refuses a partial pair when it reads stored state, because TiDB can
// leave CHECK enforcement disabled.
//
// It has to be tested through RAW SQL. The store API cannot produce a half
// pair -- ElevateUserSession writes both, the slide guards on the anchor's
// presence, and the drop clears both -- which is exactly why nothing else
// here would notice a migration that dropped the constraint.
func TestElevationColumnsAreWritableOnlyAsAPair(t *testing.T) {
	ctx := context.Background()
	st, db := newSessionTestStore(t)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "elevpair", PasswordHash: "hash",
		DisplayName: "Elevation Pair", FirstCredentialExempt: true,
	}))
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{SecretHash: storetest.FixtureSecretHash("ssssssssssssssssssssssssssssssssssssssssssssssss"),
		ID: sessionID, UserID: userid.MustNew(userID),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))

	const stamp = "2026-08-26T12:00:00.000Z"

	t.Run("a deadline with no anchor is refused", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			`UPDATE user_sessions SET elevation_expires_at = ? WHERE id = ?`, stamp, sessionID)
		require.Error(t, err, "a deadline the slide cannot clamp must not be storable")
		assert.Contains(t, err.Error(), "CHECK")
	})

	t.Run("an anchor with no deadline is refused", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			`UPDATE user_sessions SET elevation_proven_at = ? WHERE id = ?`, stamp, sessionID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CHECK")
	})

	t.Run("both together are accepted", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			`UPDATE user_sessions SET elevation_proven_at = ?, elevation_expires_at = ? WHERE id = ?`,
			stamp, stamp, sessionID)
		require.NoError(t, err)

		row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
		require.NoError(t, err)
		require.NotNil(t, row.ElevationProvenAt)
		require.NotNil(t, row.ElevationExpiresAt)
	})

	t.Run("clearing both together is accepted", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			`UPDATE user_sessions SET elevation_proven_at = NULL, elevation_expires_at = NULL WHERE id = ?`,
			sessionID)
		require.NoError(t, err)

		row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt)
		assert.Nil(t, row.ElevationExpiresAt)
	})
}

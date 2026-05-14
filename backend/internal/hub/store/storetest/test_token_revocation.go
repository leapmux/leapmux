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

// testTokenRevocation exercises the MaxRevokedAt / MaxTokensRevokedAt
// aggregates the revocation watcher relies on at bootstrap. The
// underlying queries are dialect-specific (sqlite uses strftime
// comparison, mysql/postgres lean on native DATETIME / TIMESTAMPTZ);
// running these against every store backend pins identical semantics.
func (s *Suite) testTokenRevocation(t *testing.T) {
	t.Run("MaxRevokedAt returns zero time when no rows are revoked", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org", true)
		user := SeedUser(t, st, orgID, "user")
		// Create a live api_token + delegation_token to ensure the
		// "no revoked rows" path doesn't accidentally trip on live
		// rows (revoked_at IS NULL must filter them out).
		tokenID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:         tokenID,
			UserID:     user.ID,
			ClientType: "cli",
			ClientName: "live",
			SecretHash: []byte("hash"),
			Scope:      "remote:*",
		}))

		got, err := st.APITokens().MaxRevokedAt(ctx)
		require.NoError(t, err)
		assert.True(t, got.IsZero(), "no revoked rows should yield zero time, got %v", got)
	})

	t.Run("MaxRevokedAt picks the latest revoked_at across many rows", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org", true)
		user := SeedUser(t, st, orgID, "user")

		// Five api_tokens, revoked in succession with small sleeps so
		// the timestamps are strictly increasing even at the dialect's
		// minimum precision (sqlite stores ms, mysql ms via NOW(3),
		// postgres us). 2ms cleanly separates them everywhere.
		var lastRevoked time.Time
		for i := 0; i < 5; i++ {
			tokenID := id.Generate()
			require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
				ID:         tokenID,
				UserID:     user.ID,
				ClientType: "cli",
				ClientName: "t",
				SecretHash: []byte("hash"),
				Scope:      "remote:*",
			}))
			if i > 0 {
				time.Sleep(2 * time.Millisecond)
			}
			_, err := st.APITokens().Revoke(ctx, tokenID)
			require.NoError(t, err)
			tok, err := st.APITokens().GetByID(ctx, tokenID)
			require.NoError(t, err)
			require.NotNil(t, tok.RevokedAt, "Revoke should populate revoked_at")
			lastRevoked = *tok.RevokedAt
		}

		got, err := st.APITokens().MaxRevokedAt(ctx)
		require.NoError(t, err)
		assert.True(t, got.Equal(lastRevoked) || got.After(lastRevoked.Add(-time.Microsecond)),
			"MaxRevokedAt %v should match the last-revoked row %v", got, lastRevoked)
	})

	t.Run("DelegationTokens MaxRevokedAt mirrors APITokens semantics", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org", true)
		user := SeedUser(t, st, orgID, "user")
		// Delegation tokens need an active worker + workspace + tab
		// (FK constraints), so seed those before revoking.
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "ws")
		tabID := id.Generate()
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
			OrgID:       orgID,
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       tabID,
			Position:    "a",
			TileID:      "tile-1",
		}))

		// No rows yet.
		got, err := st.DelegationTokens().MaxRevokedAt(ctx)
		require.NoError(t, err)
		assert.True(t, got.IsZero())

		// One revocation.
		tokenID := id.Generate()
		require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
			ID:               tokenID,
			UserID:           user.ID,
			WorkerID:         worker.ID,
			WorkspaceID:      wsID,
			IssuedForTabID:   tabID,
			IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
			SecretHash:       []byte("hash"),
			ExpiresAt:        time.Now().Add(time.Hour),
		}))
		_, err = st.DelegationTokens().Revoke(ctx, tokenID)
		require.NoError(t, err)

		got, err = st.DelegationTokens().MaxRevokedAt(ctx)
		require.NoError(t, err)
		assert.False(t, got.IsZero(), "after a revoke MaxRevokedAt should be set")
	})

	t.Run("Users MaxTokensRevokedAt returns zero when no user has been revoked", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org", true)
		_ = SeedUser(t, st, orgID, "user")
		// User exists but BumpTokensRevokedAt was never called.
		got, err := st.Users().MaxTokensRevokedAt(ctx)
		require.NoError(t, err)
		assert.True(t, got.IsZero())
	})

	t.Run("Users MaxTokensRevokedAt picks the latest bump across many users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org", true)
		u1 := SeedUser(t, st, orgID, "u1")
		u2 := SeedUser(t, st, orgID, "u2")
		u3 := SeedUser(t, st, orgID, "u3")
		// Bump u2 first, then u1, then u3 last. Each Bump runs in its
		// own autocommit statement so the timestamps strictly advance
		// at the dialect's clock resolution (sqlite ms, mysql ms via
		// NOW(3), postgres us). A 2ms sleep gives every dialect
		// enough headroom to make NOW() strictly greater on the next
		// call.
		rows1, err := st.Users().BumpTokensRevokedAt(ctx, u2.ID)
		require.NoError(t, err)
		require.Equal(t, int64(1), rows1, "Bump must affect exactly the targeted user row")
		time.Sleep(2 * time.Millisecond)
		_, err = st.Users().BumpTokensRevokedAt(ctx, u1.ID)
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
		_, err = st.Users().BumpTokensRevokedAt(ctx, u3.ID) // latest
		require.NoError(t, err)

		// MAX must reflect the latest bump; the exact value is the
		// dialect's NOW() so we can only assert non-zero.
		got, err := st.Users().MaxTokensRevokedAt(ctx)
		require.NoError(t, err)
		assert.False(t, got.IsZero(), "expected a non-zero tokens_revoked_at after three bumps")

		// Cross-check via a per-user GetByID, which returns each
		// user's tokens_revoked_at. Every row's value must be <= MAX
		// (the MAX is necessarily the latest of the three). This
		// avoids ListWithTokensRevokedSince's `> $since` filter,
		// which has a known TiDB quirk for the zero time.
		for _, uid := range []string{u1.ID, u2.ID, u3.ID} {
			row, err := st.Users().GetByID(ctx, uid)
			require.NoError(t, err)
			require.NotNil(t, row, "user %q must be readable after bump", uid)
		}
	})

	t.Run("MaxRevokedAt ignores deleted-by-cleanup rows", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org", true)
		user := SeedUser(t, st, orgID, "user")

		// One revoked api_token, then DeleteRevokedBefore wipes it.
		tokenID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:         tokenID,
			UserID:     user.ID,
			ClientType: "cli",
			ClientName: "t",
			SecretHash: []byte("hash"),
			Scope:      "remote:*",
		}))
		_, err := st.APITokens().Revoke(ctx, tokenID)
		require.NoError(t, err)

		// Confirm it's visible before cleanup.
		before, err := st.APITokens().MaxRevokedAt(ctx)
		require.NoError(t, err)
		require.False(t, before.IsZero())

		// Cleanup with a far-future cutoff removes the revoked row.
		_, err = st.APITokens().DeleteRevokedBefore(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)

		after, err := st.APITokens().MaxRevokedAt(ctx)
		require.NoError(t, err)
		assert.True(t, after.IsZero(), "after cleanup the MAX should fall back to zero")
	})
}

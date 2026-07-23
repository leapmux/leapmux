package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedAPITokenForList inserts a live api_token row with minimal fields for the
// listing tests (the admin listing does not surface the secrets).
func seedAPITokenForList(t *testing.T, st store.Store, userID, clientType, clientName string) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  clientType,
		ClientName:  clientName,
		SecretHash:  []byte("hash"),
		RefreshHash: []byte("refresh"),
		Scope:       "remote:*",
	}))
	return tokenID
}

func seedDelegationTokenForList(t *testing.T, st store.Store, userID, workspaceID string) string {
	t.Helper()
	worker := SeedWorker(t, st, userID)
	tokenID := id.Generate()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		WorkerID:    worker.ID,
		WorkspaceID: workspaceID,
		SecretHash:  []byte("hash"),
		RefreshHash: []byte("refresh"),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	return tokenID
}

// pinTokenCreatedAt backdates a token row's created_at to a distinct pinned
// instant so paging tests can assert exact (created_at DESC, id DESC) page
// contents instead of racing the insert timestamps.
func pinTokenCreatedAt(t *testing.T, st store.TestableStore, entity store.TestEntity, tokenID string, at time.Time) {
	t.Helper()
	require.NoError(t, st.TestHelper().SetCreatedAt(context.Background(), entity, tokenID, at))
}

func (s *Suite) testTokenListing(t *testing.T) {
	ctx := context.Background()

	t.Run("api tokens list all joins owner and pages through", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "alice")
		bob := SeedUser(t, st, orgID, "bob")
		t1 := seedAPITokenForList(t, st, alice.ID, "cli", "alice-cli")
		t2 := seedAPITokenForList(t, st, bob.ID, "cli", "bob-cli")
		t3 := seedAPITokenForList(t, st, bob.ID, "integration", "bot")
		// Pin distinct created_at instants so each page's exact contents and
		// order are assertable, not just the union's membership.
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		pinTokenCreatedAt(t, st, store.EntityAPITokens, t1, base.Add(1*time.Second))
		pinTokenCreatedAt(t, st, store.EntityAPITokens, t2, base.Add(2*time.Second))
		pinTokenCreatedAt(t, st, store.EntityAPITokens, t3, base.Add(3*time.Second))

		// First page: the two newest by (created_at DESC, id DESC), owners
		// carried by the LEFT JOIN (proving the per-user fanout is gone).
		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{PageParams: store.PageParams{Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		assert.Equal(t, t3, page.Rows[0].ID)
		assert.Equal(t, t2, page.Rows[1].ID)
		assert.Equal(t, "bob", page.Rows[0].OwnerUsername)
		assert.Equal(t, "bob", page.Rows[1].OwnerUsername)
		require.True(t, page.HasMore())

		// Second page: the remaining row, then clean termination -- no
		// HasMore, no dangling cursor.
		page, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{PageParams: store.PageParams{Cursor: page.NextCursor, Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, t1, page.Rows[0].ID)
		assert.Equal(t, "alice", page.Rows[0].OwnerUsername)
		assert.False(t, page.HasMore())
		assert.Empty(t, page.NextCursor)
	})

	t.Run("api tokens list all client_type filter", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		user := SeedUser(t, st, orgID, "filter-user")
		seedAPITokenForList(t, st, user.ID, "cli", "a")
		seedAPITokenForList(t, st, user.ID, "integration", "b")

		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{ClientType: "cli", PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, "cli", page.Rows[0].ClientType)
	})

	t.Run("api tokens list all client_type filter composes with pagination", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		user := SeedUser(t, st, orgID, "filter-page-user")
		c1 := seedAPITokenForList(t, st, user.ID, "cli", "c1")
		i1 := seedAPITokenForList(t, st, user.ID, "integration", "i1")
		c2 := seedAPITokenForList(t, st, user.ID, "cli", "c2")
		i2 := seedAPITokenForList(t, st, user.ID, "integration", "i2")
		c3 := seedAPITokenForList(t, st, user.ID, "cli", "c3")
		// Interleave the two client types in time so the cursor from page one
		// (at c2) straddles integration rows on both sides: i2 is newer and i1
		// is older than the cursor, and neither may leak into a "cli" page --
		// the filter conjunct and the keyset conjunct must compose.
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		for i, tok := range []string{c1, i1, c2, i2, c3} {
			pinTokenCreatedAt(t, st, store.EntityAPITokens, tok, base.Add(time.Duration(i+1)*time.Second))
		}

		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{ClientType: "cli", PageParams: store.PageParams{Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		assert.Equal(t, c3, page.Rows[0].ID)
		assert.Equal(t, c2, page.Rows[1].ID)
		require.True(t, page.HasMore())

		page, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{ClientType: "cli", PageParams: store.PageParams{Cursor: page.NextCursor, Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, c1, page.Rows[0].ID)
		assert.False(t, page.HasMore())
	})

	t.Run("api tokens list all filters by user and paginates", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "byuser-alice")
		bob := SeedUser(t, st, orgID, "byuser-bob")
		a1 := seedAPITokenForList(t, st, alice.ID, "cli", "a1")
		b1 := seedAPITokenForList(t, st, bob.ID, "cli", "b1")
		a2 := seedAPITokenForList(t, st, alice.ID, "cli", "a2")
		a3 := seedAPITokenForList(t, st, alice.ID, "cli", "a3")
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		for i, tok := range []string{a1, b1, a2, a3} {
			pinTokenCreatedAt(t, st, store.EntityAPITokens, tok, base.Add(time.Duration(i+1)*time.Second))
		}

		// The UserID filter dispatches to the ByUser query twin; bob's token is
		// newer than a2 and must not leak into alice's pages, even across the
		// cursor boundary.
		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{UserID: &alice.ID, PageParams: store.PageParams{Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		assert.Equal(t, a3, page.Rows[0].ID)
		assert.Equal(t, a2, page.Rows[1].ID)
		assert.Equal(t, "byuser-alice", page.Rows[0].OwnerUsername)
		require.True(t, page.HasMore())

		page, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{UserID: &alice.ID, PageParams: store.PageParams{Cursor: page.NextCursor, Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, a1, page.Rows[0].ID)
		assert.False(t, page.HasMore())
	})

	t.Run("delegation tokens list all filters by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "del-byuser-alice")
		bob := SeedUser(t, st, orgID, "del-byuser-bob")
		ws := SeedWorkspace(t, st, orgID, alice.ID, "ws")
		d1 := seedDelegationTokenForList(t, st, alice.ID, ws)
		_ = seedDelegationTokenForList(t, st, bob.ID, ws)
		d3 := seedDelegationTokenForList(t, st, alice.ID, ws)

		page, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{UserID: &alice.ID, PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 2, "only alice's delegation tokens must be listed")
		ids := []string{page.Rows[0].ID, page.Rows[1].ID}
		assert.ElementsMatch(t, []string{d1, d3}, ids)
		for _, r := range page.Rows {
			assert.Equal(t, "del-byuser-alice", r.OwnerUsername)
		}
	})

	t.Run("api tokens list all surfaces soft-deleted owner as (deleted)", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		dead := SeedUser(t, st, orgID, "dead-owner")
		token := seedAPITokenForList(t, st, dead.ID, "cli", "dead-cli")

		// Soft-delete the owner; the token row survives (no ON DELETE CASCADE
		// on a soft-delete). The LEFT JOIN ... AND u.deleted_at IS NULL no
		// longer matches, so the row surfaces with OwnerDeleted set and an
		// empty username -- the presentation layer decides the placeholder.
		require.NoError(t, st.Users().Delete(ctx, dead.ID))

		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		var found bool
		for _, r := range page.Rows {
			if r.ID == token {
				found = true
				assert.True(t, r.OwnerDeleted, "soft-deleted owner's token must surface with OwnerDeleted set")
				assert.Empty(t, r.OwnerUsername, "soft-deleted owner must surface with an empty username")
			}
		}
		require.True(t, found, "soft-deleted owner's live token must still be listed for audit")
	})

	t.Run("delegation tokens list all joins owner", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "del-alice")
		bob := SeedUser(t, st, orgID, "del-bob")
		ws := SeedWorkspace(t, st, orgID, alice.ID, "ws")
		d1 := seedDelegationTokenForList(t, st, alice.ID, ws)
		d2 := seedDelegationTokenForList(t, st, bob.ID, ws)

		page, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		owners := map[string]string{}
		for _, r := range page.Rows {
			owners[r.ID] = r.OwnerUsername
		}
		assert.Equal(t, "del-alice", owners[d1])
		assert.Equal(t, "del-bob", owners[d2])
	})

	t.Run("delegation tokens list all pages through and surfaces (deleted) owner", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "del-page-alice")
		bob := SeedUser(t, st, orgID, "del-page-bob")
		ws := SeedWorkspace(t, st, orgID, alice.ID, "ws")
		d1 := seedDelegationTokenForList(t, st, alice.ID, ws)
		d2 := seedDelegationTokenForList(t, st, bob.ID, ws)
		d3 := seedDelegationTokenForList(t, st, alice.ID, ws)
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		pinTokenCreatedAt(t, st, store.EntityDelegationTokens, d1, base.Add(1*time.Second))
		pinTokenCreatedAt(t, st, store.EntityDelegationTokens, d2, base.Add(2*time.Second))
		pinTokenCreatedAt(t, st, store.EntityDelegationTokens, d3, base.Add(3*time.Second))

		// Soft-delete bob: the still-live token must keep surfacing (audit),
		// with OwnerDeleted set mid-walk (the presentation layer decides the
		// placeholder).
		require.NoError(t, st.Users().Delete(ctx, bob.ID))

		page, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{PageParams: store.PageParams{Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		assert.Equal(t, d3, page.Rows[0].ID)
		assert.Equal(t, "del-page-alice", page.Rows[0].OwnerUsername)
		assert.False(t, page.Rows[0].OwnerDeleted)
		assert.Equal(t, d2, page.Rows[1].ID)
		assert.True(t, page.Rows[1].OwnerDeleted,
			"soft-deleted owner's delegation token must surface with OwnerDeleted set")
		assert.Empty(t, page.Rows[1].OwnerUsername)
		require.True(t, page.HasMore())

		page, err = st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{PageParams: store.PageParams{Cursor: page.NextCursor, Limit: 2}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, d1, page.Rows[0].ID)
		assert.False(t, page.HasMore())
		assert.Empty(t, page.NextCursor)
	})

	t.Run("api tokens list all include revoked forensics opt-in", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "rev-alice")
		bob := SeedUser(t, st, orgID, "rev-bob")
		a1 := seedAPITokenForList(t, st, alice.ID, "cli", "a1")
		a2 := seedAPITokenForList(t, st, alice.ID, "cli", "a2")
		b1 := seedAPITokenForList(t, st, bob.ID, "cli", "b1")
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		for i, tok := range []string{a1, a2, b1} {
			pinTokenCreatedAt(t, st, store.EntityAPITokens, tok, base.Add(time.Duration(i+1)*time.Second))
		}

		n, err := st.APITokens().Revoke(ctx, a2)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)

		// (a) The default listing stays live-only: the revoked row vanishes.
		page, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		liveIDs := make([]string, 0, len(page.Rows))
		for _, r := range page.Rows {
			liveIDs = append(liveIDs, r.ID)
		}
		assert.ElementsMatch(t, []string{a1, b1}, liveIDs,
			"default ListAll must omit the revoked row")

		// (b) IncludeRevoked surfaces the revoked row with its RevokedAt set and
		// the owner username still riding the JOIN.
		page, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{IncludeRevoked: true, PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 3)
		byID := map[string]store.APITokenWithOwner{}
		for _, r := range page.Rows {
			byID[r.ID] = r
		}
		require.Contains(t, byID, a2, "IncludeRevoked must list the revoked row")
		require.NotNil(t, byID[a2].RevokedAt, "revoked row must carry its RevokedAt")
		assert.Equal(t, "rev-alice", byID[a2].OwnerUsername)
		require.Contains(t, byID, a1)
		assert.Nil(t, byID[a1].RevokedAt, "live row must not carry a RevokedAt")

		// (c) IncludeRevoked composes with the UserID filter: only alice's rows,
		// revoked one included.
		page, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{UserID: &alice.ID, IncludeRevoked: true, PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		userIDs := make([]string, 0, len(page.Rows))
		for _, r := range page.Rows {
			userIDs = append(userIDs, r.ID)
			assert.Equal(t, alice.ID, r.UserID)
		}
		assert.ElementsMatch(t, []string{a1, a2}, userIDs)

		// (d) IncludeRevoked composes with the keyset cursor: a Limit-1 walk
		// visits every row exactly once, in (created_at DESC, id DESC) order.
		var visited []string
		cursor := ""
		for i := 0; ; i++ {
			require.Less(t, i, 10, "cursor walk did not terminate")
			page, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{IncludeRevoked: true, PageParams: store.PageParams{Cursor: cursor, Limit: 1}})
			require.NoError(t, err)
			for _, r := range page.Rows {
				visited = append(visited, r.ID)
			}
			if !page.HasMore() {
				break
			}
			cursor = page.NextCursor
		}
		assert.Equal(t, []string{b1, a2, a1}, visited,
			"Limit-1 cursor walk must visit every row exactly once")
	})

	t.Run("delegation tokens list all include revoked forensics opt-in", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tok-org")
		alice := SeedUser(t, st, orgID, "del-rev-alice")
		bob := SeedUser(t, st, orgID, "del-rev-bob")
		ws := SeedWorkspace(t, st, orgID, alice.ID, "ws")
		d1 := seedDelegationTokenForList(t, st, alice.ID, ws)
		d2 := seedDelegationTokenForList(t, st, alice.ID, ws)
		d3 := seedDelegationTokenForList(t, st, bob.ID, ws)
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		for i, tok := range []string{d1, d2, d3} {
			pinTokenCreatedAt(t, st, store.EntityDelegationTokens, tok, base.Add(time.Duration(i+1)*time.Second))
		}

		n, err := st.DelegationTokens().Revoke(ctx, d2)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)

		// (a) The default listing stays live-only: the revoked row vanishes.
		page, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		liveIDs := make([]string, 0, len(page.Rows))
		for _, r := range page.Rows {
			liveIDs = append(liveIDs, r.ID)
		}
		assert.ElementsMatch(t, []string{d1, d3}, liveIDs,
			"default ListAll must omit the revoked row")

		// (b) IncludeRevoked surfaces the revoked row with its RevokedAt set and
		// the owner username still riding the JOIN.
		page, err = st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{IncludeRevoked: true, PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 3)
		byID := map[string]store.DelegationTokenWithOwner{}
		for _, r := range page.Rows {
			byID[r.ID] = r
		}
		require.Contains(t, byID, d2, "IncludeRevoked must list the revoked row")
		require.NotNil(t, byID[d2].RevokedAt, "revoked row must carry its RevokedAt")
		assert.Equal(t, "del-rev-alice", byID[d2].OwnerUsername)
		require.Contains(t, byID, d1)
		assert.Nil(t, byID[d1].RevokedAt, "live row must not carry a RevokedAt")

		// (c) IncludeRevoked composes with the UserID filter: only alice's rows,
		// revoked one included.
		page, err = st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{UserID: &alice.ID, IncludeRevoked: true, PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		userIDs := make([]string, 0, len(page.Rows))
		for _, r := range page.Rows {
			userIDs = append(userIDs, r.ID)
			assert.Equal(t, alice.ID, r.UserID)
		}
		assert.ElementsMatch(t, []string{d1, d2}, userIDs)

		// (d) IncludeRevoked composes with the keyset cursor: a Limit-1 walk
		// visits every row exactly once, in (created_at DESC, id DESC) order.
		var visited []string
		cursor := ""
		for i := 0; ; i++ {
			require.Less(t, i, 10, "cursor walk did not terminate")
			page, err = st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{IncludeRevoked: true, PageParams: store.PageParams{Cursor: cursor, Limit: 1}})
			require.NoError(t, err)
			for _, r := range page.Rows {
				visited = append(visited, r.ID)
			}
			if !page.HasMore() {
				break
			}
			cursor = page.NextCursor
		}
		assert.Equal(t, []string{d3, d2, d1}, visited,
			"Limit-1 cursor walk must visit every row exactly once")
	})
}

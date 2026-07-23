package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaces(t *testing.T) {
	// Every ownership predicate here is a `WHERE owner_user_id = ?` bind, and a
	// zero caller id unwraps to "" -- which does NOT fail to match, it matches
	// every blank-owner row. owner_user_id is `NOT NULL REFERENCES users(id)`,
	// but a blank-id user row is representable in all three dialects, so the
	// bind has to be refused before the query rather than missed by it.
	t.Run("ownership gates refuse a zero caller id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-zeroid-org")
		owner := SeedUser(t, st, orgID, "ws-zeroid-owner")
		realWS := SeedWorkspace(t, st, orgID, owner.ID, "Real")

		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: "", OrgID: orgID, Username: "ws-blank-id-user",
			PasswordHash: "h", DisplayName: "Blank", PasswordSet: true,
		}))
		blankWS := "ws-blank-owner-gate"
		require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID: blankWS, OrgID: orgID, OwnerUserID: userid.UserID{}, Title: "blank-owner",
		}))
		// Control: the blank-owner row really exists, so the denials below are
		// about the zero id and not about a missing row.
		got, err := st.Workspaces().GetByID(ctx, blankWS)
		require.NoError(t, err)
		require.Equal(t, "", got.OwnerUserID)

		list, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.UserID{}, OrgID: orgID,
		})
		require.NoError(t, err)
		assert.Empty(t, list, "a zero caller id must not list blank-owner workspaces")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID: blankWS, OwnerUserID: userid.UserID{}, Title: "hijacked",
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a zero caller id must not rename a blank-owner workspace")

		n, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID: blankWS, OwnerUserID: userid.UserID{},
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a zero caller id must not delete a blank-owner workspace")

		after, err := st.Workspaces().GetByID(ctx, blankWS)
		require.NoError(t, err)
		assert.Equal(t, "blank-owner", after.Title, "neither refused mutation may have landed")

		// Control: the gate still WORKS for a real owner, so the refusals above
		// are not the gate simply denying everything.
		list, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(owner.ID), OrgID: orgID,
		})
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, realWS, list[0].ID)
	})

	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "My Workspace")

		ws, err := st.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, ws.ID)
		assert.Equal(t, orgID, ws.OrgID)
		assert.Equal(t, user.ID, ws.OwnerUserID)
		assert.Equal(t, "My Workspace", ws.Title)
		assert.False(t, ws.IsDeleted)
		assert.False(t, ws.CreatedAt.IsZero())
		assert.Nil(t, ws.DeletedAt)
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Workspaces().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by ids", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-byid-org")
		user := SeedUser(t, st, orgID, "ws-byid-user")
		wsA := SeedWorkspace(t, st, orgID, user.ID, "A")
		wsB := SeedWorkspace(t, st, orgID, user.ID, "B")
		wsDeleted := SeedWorkspace(t, st, orgID, user.ID, "Deleted")
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsDeleted,
			OwnerUserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)

		got, err := st.Workspaces().ListByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, got)

		got, err = st.Workspaces().ListByIDs(ctx, []string{wsA, wsB, wsDeleted, "missing-ws"})
		require.NoError(t, err)
		ids := make(map[string]struct{}, len(got))
		for _, ws := range got {
			ids[ws.ID] = struct{}{}
		}
		assert.Contains(t, ids, wsA)
		assert.Contains(t, ids, wsB)
		assert.NotContains(t, ids, wsDeleted, "soft-deleted workspaces should be excluded")
		assert.NotContains(t, ids, "missing-ws")
	})

	t.Run("list accessible", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-list-user")
		SeedWorkspace(t, st, orgID, user.ID, "WS 1")
		SeedWorkspace(t, st, orgID, user.ID, "WS 2")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 2)
	})

	// Tiebreaker: workspaces with identical created_at must come back in
	// a deterministic order across refreshes. ListAccessible orders by
	// (created_at DESC, id DESC) — created_at is only millisecond-
	// precision and rapid-fire seeding (or batch ops) easily ties two
	// rows in the same ms. Without the `id` tiebreaker the planner picks
	// its own order, so the sidebar shuffled workspaces on every page
	// refresh.
	t.Run("list accessible stable order on created_at ties", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-order-org")
		user := SeedUser(t, st, orgID, "ws-order-user")
		// Seed in a tight loop so at least some pairs share a millisecond.
		// We don't rely on hitting the tie path on every iteration —
		// instead we assert the result matches the explicit
		// (created_at DESC, id DESC) sort the SQL promises.
		const N = 8
		seeded := make([]string, 0, N)
		for i := 0; i < N; i++ {
			seeded = append(seeded, SeedWorkspace(t, st, orgID, user.ID, "WS"))
		}

		first, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, first, N)

		// Pin the SQL contract: ORDER BY created_at DESC, id DESC.
		for i := 0; i+1 < len(first); i++ {
			a, b := first[i], first[i+1]
			switch {
			case a.CreatedAt.Equal(b.CreatedAt):
				assert.Greaterf(t, a.ID, b.ID,
					"tie on created_at must break by id DESC (got %q then %q at %v)",
					a.ID, b.ID, a.CreatedAt)
			default:
				assert.Truef(t, a.CreatedAt.After(b.CreatedAt),
					"created_at must be non-increasing (got %v then %v)", a.CreatedAt, b.CreatedAt)
			}
		}

		// Multiple calls return the same order regardless of any planner
		// caching, ANALYZE state, or repeated DISTINCT evaluation.
		for trial := 0; trial < 3; trial++ {
			got, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
				UserID: userid.MustNew(user.ID),
				OrgID:  orgID,
			})
			require.NoError(t, err)
			require.Len(t, got, N)
			for i := range got {
				assert.Equalf(t, first[i].ID, got[i].ID,
					"trial %d position %d: ListAccessible order changed across calls", trial, i)
			}
		}

		// Every seeded id is in the result (sanity, distinct from order).
		gotIDs := make(map[string]struct{}, len(first))
		for _, ws := range first {
			gotIDs[ws.ID] = struct{}{}
		}
		for _, want := range seeded {
			assert.Contains(t, gotIDs, want)
		}
	})

	t.Run("rename", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-rename-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Old Title")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          wsID,
			OwnerUserID: userid.MustNew(user.ID),
			Title:       "New Title",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		ws, err := st.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, "New Title", ws.Title)
	})

	t.Run("rename wrong owner", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-rename-wrong")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Title")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          wsID,
			OwnerUserID: userid.MustNew("other-user"),
			Title:       "Hacked",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-del-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Delete Me")

		n, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// GetByID should not return soft-deleted workspaces.
		_, err = st.Workspaces().GetByID(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// GetByIDIncludeDeleted should still return it.
		ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
		require.NoError(t, err)
		assert.True(t, ws.IsDeleted)
		assert.NotNil(t, ws.DeletedAt)
	})

	t.Run("soft delete all by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-delall-user")
		ws1 := SeedWorkspace(t, st, orgID, user.ID, "WS A")
		ws2 := SeedWorkspace(t, st, orgID, user.ID, "WS B")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)

		for _, wsID := range []string{ws1, ws2} {
			ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
			require.NoError(t, err)
			assert.True(t, ws.IsDeleted)
		}
	})

	t.Run("non-owner sees nothing in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		owner := SeedUser(t, st, orgID, "ws-own-owner")
		other := SeedUser(t, st, orgID, "ws-own-other")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Owner Only WS")

		// Workspace access is owner-only: another user in the same org
		// never sees someone else's workspace.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(other.ID),
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Empty(t, workspaces)

		// The owner does.
		workspaces, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(owner.ID),
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsID, workspaces[0].ID)
	})

	t.Run("soft deleted not in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-acclist-user")
		SeedWorkspace(t, st, orgID, user.ID, "Alive")
		delID := SeedWorkspace(t, st, orgID, user.ID, "Dead")

		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          delID,
			OwnerUserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 1)
		assert.Equal(t, "Alive", workspaces[0].Title)
	})

	t.Run("list accessible empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-empty-user")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.NotNil(t, workspaces)
		assert.Empty(t, workspaces)
	})

	t.Run("rename non-existent", func(t *testing.T) {
		st := s.NewStore(t)

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          "nonexistent",
			OwnerUserID: userid.MustNew("nonexistent"),
			Title:       "New",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("soft delete already deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-deldel-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Double Delete")

		n, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// The is_deleted = 0 guard makes the second soft-delete match zero rows on
		// EVERY dialect (MySQL is configured with ClientFoundRows=true, so it too
		// reports matched -- not changed -- rows). A concurrent delete that lost
		// the race therefore sees rows-affected == 0, which the service maps to
		// NotFound instead of reporting success for a workspace the winner
		// already deleted.
		n, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n, "second soft-delete must match zero rows on every dialect")

		// The workspace should still be soft-deleted.
		_, err = st.Workspaces().GetByID(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("soft delete all by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-delall-empty-user")

		// Should be a no-op when user has no workspaces.
		err := st.Workspaces().SoftDeleteAllByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
	})

	t.Run("get by id include deleted returns non-deleted workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		user := SeedUser(t, st, orgID, "ws-incl-nondel-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Live WS")

		ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, ws.ID)
		assert.False(t, ws.IsDeleted)
		assert.Nil(t, ws.DeletedAt)
	})

	t.Run("soft delete all by user does not affect other users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org")
		userA := SeedUser(t, st, orgID, "ws-sdabu-userA")
		userB := SeedUser(t, st, orgID, "ws-sdabu-userB")
		SeedWorkspace(t, st, orgID, userA.ID, "A's WS")
		bWS := SeedWorkspace(t, st, orgID, userB.ID, "B's WS")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, userid.MustNew(userA.ID))
		require.NoError(t, err)

		// B's workspace should be untouched.
		ws, err := st.Workspaces().GetByID(ctx, bWS)
		require.NoError(t, err)
		assert.False(t, ws.IsDeleted)
	})

	t.Run("list accessible isolates by org", func(t *testing.T) {
		st := s.NewStore(t)
		orgA := SeedOrg(t, st, "iso-orgA")
		orgB := SeedOrg(t, st, "iso-orgB")
		owner := SeedUser(t, st, orgA, "iso-owner")
		wsA := SeedWorkspace(t, st, orgA, owner.ID, "WS in A")
		SeedWorkspace(t, st, orgB, owner.ID, "WS in B")

		// ListAccessible for orgA should only return wsA even though the
		// owner also has a workspace homed in orgB.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(owner.ID), OrgID: orgA,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsA, workspaces[0].ID)
	})

}

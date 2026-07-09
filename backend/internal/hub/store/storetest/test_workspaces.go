package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaces(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
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
		orgID := SeedOrg(t, st, "ws-byid-org", false)
		user := SeedUser(t, st, orgID, "ws-byid-user")
		wsA := SeedWorkspace(t, st, orgID, user.ID, "A")
		wsB := SeedWorkspace(t, st, orgID, user.ID, "B")
		wsDeleted := SeedWorkspace(t, st, orgID, user.ID, "Deleted")
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsDeleted,
			OwnerUserID: user.ID,
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
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-list-user")
		SeedWorkspace(t, st, orgID, user.ID, "WS 1")
		SeedWorkspace(t, st, orgID, user.ID, "WS 2")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 2)
	})

	// Tiebreaker: workspaces with identical created_at must come back in
	// a deterministic order across refreshes. ListAccessible orders by
	// (created_at DESC, id DESC) — created_at is only millisecond-
	// precision and rapid-fire seeding (or batch ops) easily ties two
	// rows in the same ms. Without the `id` tiebreaker the planner picked
	// its own order via the SELECT DISTINCT over the LEFT JOIN to
	// workspace_access, so the sidebar shuffled workspaces on every
	// page refresh.
	t.Run("list accessible stable order on created_at ties", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-order-org", false)
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
			UserID: user.ID,
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
				UserID: user.ID,
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
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-rename-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Old Title")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
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
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-rename-wrong")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Title")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          wsID,
			OwnerUserID: "other-user",
			Title:       "Hacked",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-del-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Delete Me")

		n, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
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
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-delall-user")
		ws1 := SeedWorkspace(t, st, orgID, user.ID, "WS A")
		ws2 := SeedWorkspace(t, st, orgID, user.ID, "WS B")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, user.ID)
		require.NoError(t, err)

		for _, wsID := range []string{ws1, ws2} {
			ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
			require.NoError(t, err)
			assert.True(t, ws.IsDeleted)
		}
	})

	t.Run("shared workspace appears in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		owner := SeedUser(t, st, orgID, "ws-share-owner")
		viewer := SeedUser(t, st, orgID, "ws-share-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Shared WS")

		// Viewer cannot see it yet.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Empty(t, workspaces)

		// Grant access.
		err = st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID,
			UserID:      viewer.ID,
		})
		require.NoError(t, err)

		// Now viewer can see the shared workspace.
		workspaces, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsID, workspaces[0].ID)
	})

	t.Run("soft-deleted shared workspace excluded from accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		owner := SeedUser(t, st, orgID, "ws-sddel-owner")
		viewer := SeedUser(t, st, orgID, "ws-sddel-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "To Be Deleted")

		// Grant access and verify visible.
		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID,
			UserID:      viewer.ID,
		})
		require.NoError(t, err)

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)

		// Soft-delete the workspace.
		_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: owner.ID,
		})
		require.NoError(t, err)

		// Viewer should no longer see it.
		workspaces, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Empty(t, workspaces)
	})

	t.Run("soft deleted not in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-acclist-user")
		SeedWorkspace(t, st, orgID, user.ID, "Alive")
		delID := SeedWorkspace(t, st, orgID, user.ID, "Dead")

		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          delID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 1)
		assert.Equal(t, "Alive", workspaces[0].Title)
	})

	t.Run("list accessible empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-empty-user")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
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
			OwnerUserID: "nonexistent",
			Title:       "New",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("soft delete already deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-deldel-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Double Delete")

		n, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Second soft-delete is idempotent (may return 0 or 1 depending on backend).
		_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)

		// The workspace should still be soft-deleted.
		_, err = st.Workspaces().GetByID(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("soft delete all by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-delall-empty-user")

		// Should be a no-op when user has no workspaces.
		err := st.Workspaces().SoftDeleteAllByUser(ctx, user.ID)
		require.NoError(t, err)
	})

	t.Run("get by id include deleted returns non-deleted workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
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
		orgID := SeedOrg(t, st, "ws-org", false)
		userA := SeedUser(t, st, orgID, "ws-sdabu-userA")
		userB := SeedUser(t, st, orgID, "ws-sdabu-userB")
		SeedWorkspace(t, st, orgID, userA.ID, "A's WS")
		bWS := SeedWorkspace(t, st, orgID, userB.ID, "B's WS")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, userA.ID)
		require.NoError(t, err)

		// B's workspace should be untouched.
		ws, err := st.Workspaces().GetByID(ctx, bWS)
		require.NoError(t, err)
		assert.False(t, ws.IsDeleted)
	})

	t.Run("list accessible isolates by org", func(t *testing.T) {
		st := s.NewStore(t)
		orgA := SeedOrg(t, st, "iso-orgA", false)
		orgB := SeedOrg(t, st, "iso-orgB", false)
		owner := SeedUser(t, st, orgA, "iso-owner")
		viewer := SeedUser(t, st, orgA, "iso-viewer")
		wsA := SeedWorkspace(t, st, orgA, owner.ID, "WS in A")
		wsB := SeedWorkspace(t, st, orgB, owner.ID, "WS in B")

		// Grant viewer access to both workspaces.
		for _, wsID := range []string{wsA, wsB} {
			err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
				WorkspaceID: wsID, UserID: viewer.ID,
			})
			require.NoError(t, err)
		}

		// ListAccessible for orgA should only return wsA.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID, OrgID: orgA,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsA, workspaces[0].ID)
	})

	t.Run("list all accessible spans orgs and includes owned", func(t *testing.T) {
		st := s.NewStore(t)
		orgA := SeedOrg(t, st, "aa-orgA", false)
		orgB := SeedOrg(t, st, "aa-orgB", false)
		viewer := SeedUser(t, st, orgA, "aa-viewer") // member of orgA only
		ownerA := SeedUser(t, st, orgA, "aa-ownerA") // shares a same-org workspace
		ownerB := SeedUser(t, st, orgB, "aa-ownerB") // shares a cross-org workspace
		ownedByViewer := SeedWorkspace(t, st, orgA, viewer.ID, "viewer own")
		sameOrgShared := SeedWorkspace(t, st, orgA, ownerA.ID, "same-org shared")
		crossOrgShared := SeedWorkspace(t, st, orgB, ownerB.ID, "cross-org shared")
		deletedShared := SeedWorkspace(t, st, orgB, ownerB.ID, "deleted shared")
		unrelated := SeedWorkspace(t, st, orgB, ownerB.ID, "unrelated, not shared")

		for _, wsID := range []string{sameOrgShared, crossOrgShared, deletedShared} {
			require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
				WorkspaceID: wsID, UserID: viewer.ID,
			}))
		}
		// A soft-deleted grant must not surface.
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID: deletedShared, OwnerUserID: ownerB.ID,
		})
		require.NoError(t, err)

		accessible, err := st.Workspaces().ListAllAccessible(ctx, viewer.ID)
		require.NoError(t, err)
		ids := make([]string, len(accessible))
		for i, w := range accessible {
			ids[i] = w.ID
		}
		// Owned (any org) + same-org grant + cross-org grant all surface, with no
		// org filter; the soft-deleted grant and a workspace never shared do not.
		assert.ElementsMatch(t, []string{ownedByViewer, sameOrgShared, crossOrgShared}, ids)
		assert.NotContains(t, ids, deletedShared, "soft-deleted workspace must be excluded")
		assert.NotContains(t, ids, unrelated, "a workspace not owned and not granted must be excluded")

		// Each returned workspace carries its true owning org so the caller can
		// route follow-up reads; the cross-org one is in orgB.
		for _, w := range accessible {
			if w.ID == crossOrgShared {
				assert.Equal(t, orgB, w.OrgID)
			}
		}

		// The owner sees their OWN workspaces (owner-or-grant), across orgs, minus
		// the soft-deleted one -- and not another user's owned/shared rows.
		ownerAccessible, err := st.Workspaces().ListAllAccessible(ctx, ownerB.ID)
		require.NoError(t, err)
		ownerIDs := make([]string, len(ownerAccessible))
		for i, w := range ownerAccessible {
			ownerIDs[i] = w.ID
		}
		assert.ElementsMatch(t, []string{crossOrgShared, unrelated}, ownerIDs)
	})

	t.Run("list all accessible dedups owned-and-granted and orders newest-first", func(t *testing.T) {
		// Exercises the two boundaries the UNION-of-indexed-seeks form must
		// preserve: a workspace the user both OWNS and was explicitly GRANTED
		// collapses to one row (UNION, not UNION ALL), and the trailing ORDER BY
		// over the union result ranks every branch newest-first across orgs.
		st := s.NewStore(t)
		orgA := SeedOrg(t, st, "dedup-orgA", false)
		orgB := SeedOrg(t, st, "dedup-orgB", false)
		viewer := SeedUser(t, st, orgA, "dedup-viewer") // member of orgA only
		granter := SeedUser(t, st, orgB, "dedup-granter")

		// Sleep between creates so created_at ordering is deterministic (some
		// backends only have millisecond precision, and ids are random nanoids
		// that carry no creation order).
		ownedA := SeedWorkspace(t, st, orgA, viewer.ID, "owned in A") // oldest
		time.Sleep(5 * time.Millisecond)
		ownedB := SeedWorkspace(t, st, orgB, viewer.ID, "owned in B") // owned in a second org
		time.Sleep(5 * time.Millisecond)
		// Owned AND granted to the same viewer: UNION must dedup it to one row.
		ownedAndGranted := SeedWorkspace(t, st, orgA, viewer.ID, "owned and granted")
		require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: ownedAndGranted, UserID: viewer.ID,
		}))
		time.Sleep(5 * time.Millisecond)
		// Granted-only in an org the viewer neither owns nor belongs to.
		grantedOnly := SeedWorkspace(t, st, orgB, granter.ID, "granted only") // newest
		require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: grantedOnly, UserID: viewer.ID,
		}))

		accessible, err := st.Workspaces().ListAllAccessible(ctx, viewer.ID)
		require.NoError(t, err)
		ids := make([]string, len(accessible))
		for i, w := range accessible {
			ids[i] = w.ID
		}

		// Owned-across-orgs + owned-and-granted (once) + granted-only, newest-first.
		assert.Equal(t, []string{grantedOnly, ownedAndGranted, ownedB, ownedA}, ids,
			"results must be deduped and ordered by created_at DESC (newest first)")

		// Explicit dedup guard: the owned-and-granted workspace appears exactly
		// once. UNION ALL (or a regressed DISTINCT) would surface it twice.
		count := 0
		for _, wid := range ids {
			if wid == ownedAndGranted {
				count++
			}
		}
		assert.Equal(t, 1, count, "a workspace both owned and granted must appear exactly once")
	})
}

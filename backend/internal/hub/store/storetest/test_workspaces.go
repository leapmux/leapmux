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
	// every blank-owner row. TWO independent things stop that, and this pins
	// both so neither can be dropped on the assumption the other still holds:
	//
	//  1. No blank-owner row can exist to be matched. owner_user_id is
	//     `NOT NULL REFERENCES users(id)`, and CreateUserParams.Validate now
	//     refuses the blank users.id that is the only parent such a row could
	//     hang off. (The database still permits the shape via raw SQL, which is
	//     why 2 remains load-bearing rather than belt-and-braces.)
	//  2. The gates refuse the zero bind before the query runs, so a zero
	//     caller reaches nothing -- including rows owned by someone real.
	t.Run("a blank workspace owner is unrepresentable and a zero caller reaches nothing", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "ws-zeroid-owner")
		realWS := SeedWorkspace(t, st, owner.ID, "Real")

		// The seam, closed at its source.
		require.ErrorIs(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: "", Username: "ws-blank-id-user",
			PasswordHash: "h", DisplayName: "Blank", FirstCredentialExempt: true,
		}), store.ErrInvalidArgument,
			"a blank users.id is the parent key every blank-owner row needs")

		// And therefore closed for the child row too: with no blank-id parent,
		// the FK has nothing to point at.
		require.Error(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID: "ws-blank-owner-gate", OwnerUserID: userid.UserID{}, Title: "blank-owner",
		}), "a blank-owner workspace has no parent user row to reference")

		// The gate assertions below target the REAL owner's workspace, not a
		// blank-owner one. That is what keeps them non-vacuous now that no
		// blank-owner row can exist: if a gate stopped binding the owner at all,
		// the zero caller would reach realWS and each assertion would fail.
		list, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.UserID{},
		})
		require.NoError(t, err)
		assert.Empty(t, list, "a zero caller id must list nothing, not every workspace")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID: realWS, OwnerUserID: userid.UserID{}, Title: "hijacked",
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a zero caller id must not rename someone else's workspace")

		n, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID: realWS, OwnerUserID: userid.UserID{},
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a zero caller id must not delete someone else's workspace")

		after, err := st.Workspaces().GetByID(ctx, realWS)
		require.NoError(t, err)
		assert.Equal(t, "Real", after.Title, "neither refused mutation may have landed")

		// Control: the gate still WORKS for a real owner, so the refusals above
		// are not the gate simply denying everything.
		list, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(owner.ID),
		})
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, realWS, list[0].ID)
	})

	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "ws-user")
		wsID := SeedWorkspace(t, st, user.ID, "My Workspace")

		ws, err := st.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, ws.ID)
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
		user := SeedUser(t, st, "ws-byid-user")
		wsA := SeedWorkspace(t, st, user.ID, "A")
		wsB := SeedWorkspace(t, st, user.ID, "B")
		wsDeleted := SeedWorkspace(t, st, user.ID, "Deleted")
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
		user := SeedUser(t, st, "ws-list-user")
		SeedWorkspace(t, st, user.ID, "WS 1")
		SeedWorkspace(t, st, user.ID, "WS 2")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
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
		user := SeedUser(t, st, "ws-order-user")
		// Seed in a tight loop so at least some pairs share a millisecond.
		// We don't rely on hitting the tie path on every iteration —
		// instead we assert the result matches the explicit
		// (created_at DESC, id DESC) sort the SQL promises.
		const N = 8
		seeded := make([]string, 0, N)
		for i := 0; i < N; i++ {
			seeded = append(seeded, SeedWorkspace(t, st, user.ID, "WS"))
		}

		first, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
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
		user := SeedUser(t, st, "ws-rename-user")
		wsID := SeedWorkspace(t, st, user.ID, "Old Title")

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
		user := SeedUser(t, st, "ws-rename-wrong")
		wsID := SeedWorkspace(t, st, user.ID, "Title")

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
		user := SeedUser(t, st, "ws-del-user")
		wsID := SeedWorkspace(t, st, user.ID, "Delete Me")

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
		user := SeedUser(t, st, "ws-delall-user")
		ws1 := SeedWorkspace(t, st, user.ID, "WS A")
		ws2 := SeedWorkspace(t, st, user.ID, "WS B")

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
		owner := SeedUser(t, st, "ws-own-owner")
		other := SeedUser(t, st, "ws-own-other")
		wsID := SeedWorkspace(t, st, owner.ID, "Owner Only WS")

		// Workspace access is owner-only: another user
		// never sees someone else's workspace.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(other.ID),
		})
		require.NoError(t, err)
		assert.Empty(t, workspaces)

		// The owner does.
		workspaces, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(owner.ID),
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsID, workspaces[0].ID)
	})

	t.Run("soft deleted not in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "ws-acclist-user")
		SeedWorkspace(t, st, user.ID, "Alive")
		delID := SeedWorkspace(t, st, user.ID, "Dead")

		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          delID,
			OwnerUserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 1)
		assert.Equal(t, "Alive", workspaces[0].Title)
	})

	t.Run("list accessible empty", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "ws-empty-user")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(user.ID),
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
		user := SeedUser(t, st, "ws-deldel-user")
		wsID := SeedWorkspace(t, st, user.ID, "Double Delete")

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
		user := SeedUser(t, st, "ws-delall-empty-user")

		// Should be a no-op when user has no workspaces.
		err := st.Workspaces().SoftDeleteAllByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
	})

	t.Run("get by id include deleted returns non-deleted workspace", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "ws-incl-nondel-user")
		wsID := SeedWorkspace(t, st, user.ID, "Live WS")

		ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, ws.ID)
		assert.False(t, ws.IsDeleted)
		assert.Nil(t, ws.DeletedAt)
	})

	t.Run("soft delete all by user does not affect other users", func(t *testing.T) {
		st := s.NewStore(t)
		userA := SeedUser(t, st, "ws-sdabu-userA")
		userB := SeedUser(t, st, "ws-sdabu-userB")
		SeedWorkspace(t, st, userA.ID, "A's WS")
		bWS := SeedWorkspace(t, st, userB.ID, "B's WS")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, userid.MustNew(userA.ID))
		require.NoError(t, err)

		// B's workspace should be untouched.
		ws, err := st.Workspaces().GetByID(ctx, bWS)
		require.NoError(t, err)
		assert.False(t, ws.IsDeleted)
	})

	t.Run("list accessible returns owned workspaces", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "iso-owner")
		other := SeedUser(t, st, "iso-other")
		wsOwned := SeedWorkspace(t, st, owner.ID, "Owned")
		SeedWorkspace(t, st, other.ID, "Other")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userid.MustNew(owner.ID),
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsOwned, workspaces[0].ID)
	})

}

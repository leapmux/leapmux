package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testWorkspaceTabIndex covers the bulk read and write paths of
// WorkspaceTabIndexStore. The per-row write side is also exercised
// indirectly by the manager-integration suite; here we focus on the
// bulk read (cross-workspace list) and bulk write (upsert / delete)
// surfaces.
func (s *Suite) testWorkspaceTabIndex(t *testing.T) {
	t.Run("bulk upsert and delete with mixed rendered flags", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "bulk-tabidx-user")
		worker := SeedWorker(t, st, user.ID)
		wsA := SeedWorkspace(t, st, user.ID, "A")
		wsB := SeedWorkspace(t, st, user.ID, "B")

		owner := userid.MustNew(user.ID)
		rows := []store.UpsertTabParams{
			{UserID: owner, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o1", TileID: "tile-a", Position: "a0", Rendered: true},
			{UserID: owner, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o2", TileID: "tile-a", Position: "a1", Rendered: false},
			{UserID: owner, WorkspaceID: wsB, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o3", TileID: "tile-b", Position: "b0", Rendered: true},
		}
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, rows))

		// Every row is an owned row, rendered or not. The owned view is read
		// per (owner, worker) -- there is no owner-blind by-workspace read,
		// deliberately (see store.GetOwnedTabParams) -- so group the worker's
		// rows by workspace to check the wsA/wsB split.
		byWS := ownedTabIDsByWorkspace(t, st, owner, worker.ID)
		assert.ElementsMatch(t, []string{"o1", "o2"}, byWS[wsA])
		assert.ElementsMatch(t, []string{"o3"}, byWS[wsB])
		assert.ElementsMatch(t, []string{"o1", "o3"}, renderedTabIDs(t, st, owner, wsA, wsB),
			"the rendered reads see only the rows whose flag is set")

		// Bulk upsert again with one row's position changed and another's flag
		// flipped: the conflict path must fire and update both in place rather
		// than erroring on the duplicate primary key.
		rows[0].Position = "a-updated"
		rows[1].Rendered = true
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, rows))
		row, err := st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{UserID: owner, TabID: "o1"})
		require.NoError(t, err)
		assert.Equal(t, "a-updated", row.Position)
		assert.ElementsMatch(t, []string{"o1", "o2", "o3"}, renderedTabIDs(t, st, owner, wsA, wsB))

		// Bulk delete a subset: only o1 and o3 remain after the call.
		require.NoError(t, st.WorkspaceTabIndex().BulkDelete(ctx, []store.TabIndexKey{
			{UserID: owner, TabID: "o2"},
		}))
		byWS = ownedTabIDsByWorkspace(t, st, owner, worker.ID)
		assert.ElementsMatch(t, []string{"o1"}, byWS[wsA])
		assert.ElementsMatch(t, []string{"o3"}, byWS[wsB], "the delete named o2 only")
		assert.ElementsMatch(t, []string{"o1", "o3"}, renderedTabIDs(t, st, owner, wsA, wsB))

		// Empty inputs must be no-ops, not errors.
		assert.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkDelete(ctx, nil))
	})

	t.Run("owned worker rows include authoritative archive state", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tabidx-archive-state")
		owner := userid.MustNew(user.ID)
		worker := SeedWorker(t, st, user.ID)
		activeWorkspace := SeedWorkspace(t, st, user.ID, "active")
		archivedWorkspace := SeedWorkspace(t, st, user.ID, "archived")

		for _, section := range []store.CreateWorkspaceSectionParams{
			{ID: "active-section", UserID: owner, Name: "In progress", Position: "a", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT},
			{ID: "archive-section", UserID: owner, Name: "Archived", Position: "z", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT},
		} {
			require.NoError(t, st.WorkspaceSections().Create(ctx, section))
		}
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: owner, WorkspaceID: activeWorkspace, SectionID: "active-section", Position: "a",
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: owner, WorkspaceID: archivedWorkspace, SectionID: "archive-section", Position: "a",
		}))
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, []store.UpsertTabParams{
			{UserID: owner, WorkspaceID: activeWorkspace, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "active-agent", TileID: "active-tile", Position: "a", Rendered: true},
			{UserID: owner, WorkspaceID: archivedWorkspace, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "archived-terminal", TileID: "archive-tile", Position: "a", Rendered: false},
		}))

		rows, err := st.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
			UserID: owner, WorkerID: worker.ID,
		})
		require.NoError(t, err)
		states := make(map[string]leapmuxv1.WorkspaceArchiveState, len(rows))
		for _, row := range rows {
			states[row.TabID] = row.ArchiveState
		}
		assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, states["active-agent"])
		assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, states["archived-terminal"])
	})

	// DeleteWorkspace soft-deletes the workspace and deletes its placement in
	// one transaction, and the tab rows stay until the lifecycle drain
	// tombstones the tabs. In that window the worker must read the tabs as
	// ARCHIVED, whatever section held the workspace. A missing placement used
	// to read as ACTIVE, so a reconcile pass cleared the archive flag of an
	// archived workspace's tabs and resumed their agents just before the tabs
	// closed.
	t.Run("owned worker rows report a soft-deleted workspace's tabs as archived", func(t *testing.T) {
		for name, sectionType := range map[string]leapmuxv1.SectionType{
			"archived before the delete": leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
			"active before the delete":   leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS,
		} {
			t.Run(name, func(t *testing.T) {
				st := s.NewStore(t)
				user := SeedUser(t, st, "tabidx-deleted-archive-state")
				owner := userid.MustNew(user.ID)
				worker := SeedWorker(t, st, user.ID)
				workspace := SeedWorkspace(t, st, user.ID, "deleted")
				require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
					ID: "section", UserID: owner, Name: "Section", Position: "a", SectionType: sectionType, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT,
				}))
				require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
					UserID: owner, WorkspaceID: workspace, SectionID: "section", Position: "a",
				}))
				require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, []store.UpsertTabParams{
					{UserID: owner, WorkspaceID: workspace, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "agent", TileID: "tile", Position: "a", Rendered: true},
				}))

				// The store half of DeleteWorkspace, with no lifecycle drain.
				deleted, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: workspace, OwnerUserID: owner})
				require.NoError(t, err)
				require.Equal(t, int64(1), deleted)
				require.NoError(t, st.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
					UserID: owner, WorkspaceID: workspace,
				}))

				rows, err := st.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
					UserID: owner, WorkerID: worker.ID,
				})
				require.NoError(t, err)
				require.Len(t, rows, 1, "the tab row stays until the lifecycle drain tombstones it")
				assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, rows[0].ArchiveState)
			})
		}
	})

	// A tab row's (workspace_id, user_id) REFERENCES workspaces(id,
	// owner_user_id), and workspaces.owner_user_id REFERENCES users(id). That
	// chain is what makes a blank-owner row UNREPRESENTABLE rather than merely
	// undeletable: before the users(id) tie, a blank owner was insertable while
	// every delete path refused to bind one, so the row was creatable, kept an
	// agent/terminal alive through the worker reconciler (which reads every
	// owned row as the authority on "this tab still exists"), and no API could
	// clear it.
	//
	// The case runs against every dialect because the constraint is declared
	// three times, once per migration, and only an integration run exercises
	// postgres/mysql.
	t.Run("the database refuses a blank-owner tab row", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tabidx-blank-owner-insert")
		worker := SeedWorker(t, st, user.ID)
		ws := SeedWorkspace(t, st, user.ID, "blank-owner-ws")

		// userid.UserID's zero value is constructible (Go cannot forbid it), so
		// the type alone cannot stop this row from being ASSEMBLED -- the
		// foreign key is what stops it being stored.
		//
		// The refusal text is logged rather than asserted on: it is the evidence
		// the constraint is live in THIS dialect, and pinning three dialects'
		// driver strings would break on a driver upgrade without telling anyone
		// anything about the schema.
		for _, rendered := range []bool{true, false} {
			blank := store.UpsertTabParams{
				UserID: userid.UserID{}, WorkspaceID: ws, WorkerID: worker.ID,
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "orphan",
				TileID: "tile-1", Position: "a2", Rendered: rendered,
			}
			for _, c := range []struct {
				name string
				run  func() error
			}{
				{"UpsertOwned", func() error { return st.WorkspaceTabIndex().UpsertOwned(ctx, blank) }},
				{"BulkUpsert", func() error {
					return st.WorkspaceTabIndex().BulkUpsert(ctx, []store.UpsertTabParams{blank})
				}},
			} {
				err := c.run()
				if assert.Errorf(t, err, "%s (rendered=%v): a blank owner identifies no user, so the row must not be storable", c.name, rendered) {
					t.Logf("%s (rendered=%v) refused the blank owner with: %v", c.name, rendered, err)
				}
			}
		}

		// Nothing landed: the refusal is the DB's, so it leaves no partial row.
		// The read is the owner-blind test helper because the row under test
		// has a BLANK owner -- no owner-scoped read can name it, and asserting
		// through one would pass whether or not the row exists.
		got, err := st.TestHelper().ListAllOwnedTabs(ctx)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	// A tab row must name a workspace its own user owns. Before the composite
	// key, workspace_id was a plain foreign key, so any user's row could name
	// any existing workspace, and every reader had to bind the row's owner to
	// keep a stranger's row out of the owner's view.
	t.Run("refuses a tab row whose user does not own its workspace", func(t *testing.T) {
		st := s.NewStore(t)
		userA := SeedUser(t, st, "tabidx-fk-a")
		userB := SeedUser(t, st, "tabidx-fk-b")
		ownerA := userid.MustNew(userA.ID)
		ownerB := userid.MustNew(userB.ID)
		worker := SeedWorker(t, st, userA.ID)
		wsA := SeedWorkspace(t, st, userA.ID, "A's")
		wsB := SeedWorkspace(t, st, userB.ID, "B's")

		foreign := store.UpsertTabParams{
			UserID: ownerB, WorkspaceID: wsA, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "t1", TileID: "tile", Position: "a0", Rendered: true,
		}
		require.Error(t, st.WorkspaceTabIndex().UpsertOwned(ctx, foreign),
			"B's row must not name A's workspace")
		require.Error(t, st.WorkspaceTabIndex().BulkUpsert(ctx, []store.UpsertTabParams{foreign}),
			"the bulk path must refuse it too")
		all, err := st.TestHelper().ListAllOwnedTabs(ctx)
		require.NoError(t, err)
		require.Empty(t, all, "the refused writes must store nothing")

		own := foreign
		own.UserID = ownerA
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, own), "A's row in A's workspace is valid")

		// The key is checked on the update branch too: moving A's row into B's
		// workspace fails and leaves the row where it was.
		moved := own
		moved.WorkspaceID = wsB
		require.Error(t, st.WorkspaceTabIndex().UpsertOwned(ctx, moved))
		require.Error(t, st.WorkspaceTabIndex().BulkUpsert(ctx, []store.UpsertTabParams{moved}))
		row, err := st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{UserID: ownerA, TabID: "t1"})
		require.NoError(t, err)
		assert.Equal(t, wsA, row.WorkspaceID)
	})

	t.Run("rendered reads exclude unrendered rows, and an upsert flips the flag", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tabidx-rendered-flag")
		owner := userid.MustNew(user.ID)
		worker := SeedWorker(t, st, user.ID)
		ws := SeedWorkspace(t, st, user.ID, "Flag WS")

		rows := []store.UpsertTabParams{
			{UserID: owner, WorkspaceID: ws, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "r1", TileID: "leaf", Position: "a0", Rendered: true},
			{UserID: owner, WorkspaceID: ws, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "h1", TileID: "split", Position: "a1", Rendered: false},
		}
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, rows))

		assert.Equal(t, []string{"r1"}, renderedTabIDs(t, st, owner, ws))
		_, err := st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: owner, WorkspaceID: ws, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "h1",
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "a hidden row is not a rendered tab")
		_, err = st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			UserID: owner, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "h1",
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "a hidden row cannot be located")
		hidden, err := st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{UserID: owner, TabID: "h1"})
		require.NoError(t, err)
		assert.False(t, hidden.Rendered)
		visible, err := st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: owner, WorkspaceID: ws, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "r1",
		})
		require.NoError(t, err)
		assert.True(t, visible.Rendered)

		// Owned reads see both rows.
		owned, err := st.WorkspaceTabIndex().ListOwnedTabsByWorkspace(ctx, store.ListOwnedTabsByWorkspaceParams{
			UserID: owner, WorkspaceID: ws,
		})
		require.NoError(t, err)
		ownedIDs := make([]string, 0, len(owned))
		for _, ref := range owned {
			ownedIDs = append(ownedIDs, ref.TabID)
		}
		assert.ElementsMatch(t, []string{"r1", "h1"}, ownedIDs)
		assert.ElementsMatch(t, []string{"r1", "h1"}, ownedTabIDsByWorkspace(t, st, owner, worker.ID)[ws])

		// Flip both flags: the rendered reads now see h1 only.
		rows[0].Rendered = false
		rows[1].Rendered = true
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, rows))
		assert.Equal(t, []string{"h1"}, renderedTabIDs(t, st, owner, ws))
		located, err := st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			UserID: owner, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "h1",
		})
		require.NoError(t, err)
		assert.Equal(t, "split", located.TileID)
	})

	// The bulk delete binds `WHERE (user_id, tab_id) IN ((?, ?), ...)`. A zero
	// owner unwraps to "", and "" does not fail to match -- it MATCHES every
	// blank-owner row -- so an unfiltered blank key deletes rows the caller
	// never named. This case runs against every dialect precisely because that
	// is where the three can disagree: the guard lives in the store
	// (store.FilterTabIndexKeys, applied by sqlutil for sqlite/mysql and by the
	// postgres adapter directly), not in any one caller.
	//
	// Two layers stand between a blank key and a blank-owner row, and they fail
	// in different directions, so this case pins both:
	//
	//   - Nothing to reap. The composite workspace key admits a blank-owner tab
	//     row only once a workspace owned by a blank-ID USER exists, and
	//     CreateUserParams.Validate refuses that id -- asserted below, because
	//     it is the load-bearing half and a silent deletion of the old seed
	//     would hide its regression.
	//   - Nothing to bind. The guard refuses the key regardless, which still
	//     matters: the closure above covers this API, not the database, whose
	//     TEXT keys still accept "" through raw SQL.
	//
	// The cross-dialect half is what makes it a storetest rather than a unit
	// test: FilterTabIndexKeys' own refusal and drop-reporting are pinned
	// directly in store's owner_filter_test.go, but whether each dialect ROUTES
	// through it -- sqlutil for sqlite/mysql, the postgres adapter directly --
	// is only observable here.
	t.Run("bulk delete skips a blank owner without cancelling valid keys", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tabidx-blank-owner")
		owner := userid.MustNew(user.ID)
		worker := SeedWorker(t, st, user.ID)
		ws := SeedWorkspace(t, st, user.ID, "blank-owner-ws")

		require.NoError(t, st.WorkspaceTabIndex().BulkUpsert(ctx, []store.UpsertTabParams{
			{UserID: owner, WorkspaceID: ws, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "keep", TileID: "tile-1", Position: "a0", Rendered: true},
			{UserID: owner, WorkspaceID: ws, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "drop", TileID: "tile-1", Position: "a1", Rendered: true},
		}))

		// The blank-owner ROW this case used to seed is no longer creatable: it
		// needed a blank-ID user as its parent, and CreateUserParams.Validate
		// refuses that id now. Pin the closure itself rather than deleting the
		// setup silently -- this is the assertion that fails if the seam reopens.
		require.ErrorIs(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: "", Username: "tabidx-blank-id-user",
			PasswordHash: "h", DisplayName: "Blank", FirstCredentialExempt: true,
		}), store.ErrInvalidArgument,
			"a blank users.id is the only parent a blank-owner tab row could hang off")

		// Mixed batch, and the blank key names a tab id that really exists. Two
		// distinct regressions land here: a guard that ABORTS on the blank key
		// leaves "drop" behind, and a guard that BINDS "" for it -- via a dialect
		// that forgot to route through FilterTabIndexKeys -- would reap "keep"
		// the moment the database ever holds a blank-owner row. The surviving
		// id set separates the two.
		require.NoError(t, st.WorkspaceTabIndex().BulkDelete(ctx, []store.TabIndexKey{
			{UserID: userid.UserID{}, TabID: "keep"},
			{UserID: owner, TabID: "drop"},
		}))
		got, err := st.TestHelper().ListAllOwnedTabs(ctx)
		require.NoError(t, err)
		ids := make([]string, 0, len(got))
		for _, r := range got {
			ids = append(ids, r.TabID)
		}
		assert.ElementsMatch(t, []string{"keep"}, ids,
			"the real key deletes -- one unusable key must not cancel its neighbours -- and the blank key deletes nothing, not even the tab id it names")
	})

	// Every owned read binds user_id. The three below are the ones a foreign
	// tenant's row can reach when it does not: workspace_tab_owned is keyed by
	// (user_id, tab_id), tab ids are client-minted (a FILE tab id is
	// `file-<millis>-<counter>`, so two clients collide readily), and worker_id
	// does not constrain a row's owner -- it is a plain column that identifies a
	// worker with an owner of its own. (workspace_id does: the composite key
	// ties a row to a workspace its own user owns, so the stranger's rows below
	// sit in the stranger's own workspace.)
	//
	// The foreign row is inserted FIRST on purpose: GetOwned is a `:one`, so an
	// owner-blind predicate returns whichever row the index visits first, and
	// seeding the stranger first is what makes that arbitrary winner
	// observable instead of accidentally right.
	t.Run("owned reads are scoped to the requesting owner", func(t *testing.T) {
		st := s.NewStore(t)
		userA := SeedUser(t, st, "tabidx-scope-a")
		userB := SeedUser(t, st, "tabidx-scope-b")
		ownerA := userid.MustNew(userA.ID)
		workerA := SeedWorker(t, st, userA.ID)
		workerB := SeedWorker(t, st, userB.ID)
		wsA := SeedWorkspace(t, st, userA.ID, "Scope A")
		wsB := SeedWorkspace(t, st, userB.ID, "Scope B")

		const sharedTabID = "file-1700000000000-1"
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: userid.MustNew(userB.ID), WorkspaceID: wsB, WorkerID: workerB.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-b", Position: "b0", Rendered: true,
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: ownerA, WorkspaceID: wsA, WorkerID: workerA.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-a", Position: "a0", Rendered: true,
		}))
		// A foreign row hosted on the REQUESTING owner's worker: worker_id
		// alone is not a tenancy scope.
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: userid.MustNew(userB.ID), WorkspaceID: wsB, WorkerID: workerA.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "b-only", TileID: "tile-b", Position: "b1", Rendered: true,
		}))
		// Both owners' rows are really there -- without this the assertions
		// below could pass on an empty table.
		all, err := st.TestHelper().ListAllOwnedTabs(ctx)
		require.NoError(t, err)
		require.Len(t, all, 3)

		row, err := st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{
			UserID: ownerA, TabID: sharedTabID,
		})
		require.NoError(t, err)
		assert.Equal(t, userA.ID, row.UserID, "GetOwned must return the requesting owner's row")
		assert.Equal(t, workerA.ID, row.WorkerID,
			"and with it the requesting owner's worker -- this is what gates a delegation mint")

		owned, err := st.WorkspaceTabIndex().ListOwnedTabsByWorkspace(ctx, store.ListOwnedTabsByWorkspaceParams{
			UserID: ownerA, WorkspaceID: wsA,
		})
		require.NoError(t, err)
		require.Len(t, owned, 1, "a workspace delete must not fan out to a stranger's worker")
		assert.Equal(t, workerA.ID, owned[0].WorkerID)
		// The tab identity travels with the worker, which is the reason this
		// read returns rows rather than a DISTINCT worker projection: the
		// Worker stores no workspace id, so the cleanup call has to name the
		// tabs it wants torn down.
		assert.Equal(t, sharedTabID, owned[0].TabID)
		strangers, err := st.WorkspaceTabIndex().ListOwnedTabsByWorkspace(ctx, store.ListOwnedTabsByWorkspaceParams{
			UserID: ownerA, WorkspaceID: wsB,
		})
		require.NoError(t, err)
		assert.Empty(t, strangers, "a caller who does not own the workspace sees none of its tabs")

		byWorker, err := st.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
			UserID: ownerA, WorkerID: workerA.ID,
		})
		require.NoError(t, err)
		ids := make([]string, 0, len(byWorker))
		for _, r := range byWorker {
			ids = append(ids, r.TabID)
		}
		assert.ElementsMatch(t, []string{sharedTabID}, ids, "only the requesting owner's rows")

		// A zero owner names nobody, so it must read nothing rather than
		// binding "" and matching every blank-owner row (see OwnerFilter).
		_, err = st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{TabID: sharedTabID})
		assert.ErrorIs(t, err, store.ErrNotFound, "an unminted owner owns nothing")
		blankOwned, err := st.WorkspaceTabIndex().ListOwnedTabsByWorkspace(ctx, store.ListOwnedTabsByWorkspaceParams{
			WorkspaceID: wsA,
		})
		require.NoError(t, err)
		assert.Empty(t, blankOwned)
		blankTabs, err := st.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
			WorkerID: workerA.ID,
		})
		require.NoError(t, err)
		assert.Empty(t, blankTabs)
	})

	t.Run("list rendered by workspace ids", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tabidx-user")
		worker := SeedWorker(t, st, user.ID)
		wsA := SeedWorkspace(t, st, user.ID, "A")
		wsB := SeedWorkspace(t, st, user.ID, "B")
		wsUnreferenced := SeedWorkspace(t, st, user.ID, "Unreferenced")

		// Two tabs in A, one in B.
		for _, r := range []struct{ ws, tab, tile, pos string }{
			{wsA, "a1", "tile-a", "a0"},
			{wsA, "a2", "tile-a", "a1"},
			{wsB, "b1", "tile-b", "b0"},
		} {
			require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
				UserID: userid.MustNew(user.ID), WorkspaceID: r.ws, WorkerID: worker.ID,
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: r.tab,
				TileID: r.tile, Position: r.pos, Rendered: true,
			}))
		}

		// Empty input: nil with no DB call.
		got, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{UserID: userid.MustNew(user.ID)})
		require.NoError(t, err)
		assert.Empty(t, got)

		// Single id behaves like the per-workspace variant.
		got, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{UserID: userid.MustNew(user.ID), WorkspaceIDs: []string{wsA}})
		require.NoError(t, err)
		assert.Len(t, got, 2)
		for _, row := range got {
			assert.Equal(t, wsA, row.WorkspaceID)
		}

		// Multi-id: result groups by workspace_id then position. Missing
		// ids are silently dropped; empty workspaces return nothing.
		got, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{UserID: userid.MustNew(user.ID), WorkspaceIDs: []string{wsA, wsB, wsUnreferenced, "missing"}})
		require.NoError(t, err)
		require.Len(t, got, 3)
		byWS := map[string][]string{}
		for _, row := range got {
			byWS[row.WorkspaceID] = append(byWS[row.WorkspaceID], row.TabID)
		}
		assert.ElementsMatch(t, []string{"a1", "a2"}, byWS[wsA])
		assert.ElementsMatch(t, []string{"b1"}, byWS[wsB])
		assert.Empty(t, byWS[wsUnreferenced])
		assert.Empty(t, byWS["missing"])
	})

	t.Run("locate accessible rendered is owner-only", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "locate-owner")
		other := SeedUser(t, st, "locate-other")
		worker := SeedWorker(t, st, owner.ID)
		wsID := SeedWorkspace(t, st, owner.ID, "Locate WS")
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: userid.MustNew(owner.ID), WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "loc1", TileID: "tile", Position: "a0", Rendered: true,
		}))

		// The owner locates the tab.
		row, err := st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			TabID: "loc1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, UserID: userid.MustNew(owner.ID),
		})
		require.NoError(t, err)
		assert.Equal(t, wsID, row.WorkspaceID)

		// A non-owner gets ErrNotFound.
		_, err = st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			TabID: "loc1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, UserID: userid.MustNew(other.ID),
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "locate must be owner-only")

		// The owner cannot locate a tab in a soft-deleted workspace.
		_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(owner.ID)})
		require.NoError(t, err)
		_, err = st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			TabID: "loc1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, UserID: userid.MustNew(owner.ID),
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "a soft-deleted workspace's tabs are unreachable")
	})

	// Client-minted tab ids are unique only within a user, so two owners can
	// hold the same tab id, each in a workspace of their own. Every rendered
	// read binds the row's owner, so neither owner reaches the other's row --
	// and a caller who specifies a workspace they do not own sees nothing, because
	// the composite key makes every row of that workspace its owner's.
	t.Run("rendered reads are scoped to the row owner", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "rendered-owner")
		other := SeedUser(t, st, "rendered-other")
		worker := SeedWorker(t, st, owner.ID)
		wsID := SeedWorkspace(t, st, owner.ID, "Rendered WS")
		otherWS := SeedWorkspace(t, st, other.ID, "Other WS")

		ownerID := userid.MustNew(owner.ID)
		otherID := userid.MustNew(other.ID)
		const sharedTab = "shared-tab"
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: otherID, WorkspaceID: otherWS, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab, TileID: "tile-other", Position: "a0", Rendered: true,
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: ownerID, WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab, TileID: "tile-owner", Position: "a0", Rendered: true,
		}))

		row, err := st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: ownerID, WorkspaceID: wsID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		require.NoError(t, err)
		assert.Equal(t, "tile-owner", row.TileID, "GetRendered must return the CALLER's row")

		_, err = st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: otherID, WorkspaceID: wsID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "a caller who does not own the workspace reaches none of its tabs")

		row, err = st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: otherID, WorkspaceID: otherWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		require.NoError(t, err)
		assert.Equal(t, "tile-other", row.TileID, "control: the other owner still reaches their own row")

		got, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{
			UserID: ownerID, WorkspaceIDs: []string{wsID, otherWS},
		})
		require.NoError(t, err)
		require.Len(t, got, 1, "the listing must not leak the other owner's row, even for a workspace id the caller specifies")
		assert.Equal(t, "tile-owner", got[0].TileID)

		located, err := st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			UserID: ownerID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		require.NoError(t, err)
		assert.Equal(t, "tile-owner", located.TileID, "LIMIT 1 must not return the foreign row")

		// A zero owner is a refusal, not a wildcard: "" would MATCH every
		// blank-owner row rather than none.
		_, err = st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			WorkspaceID: wsID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "an unminted caller owns no rendered tab")
		got, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{
			WorkspaceIDs: []string{wsID},
		})
		require.NoError(t, err)
		assert.Empty(t, got, "an unminted caller lists no rendered tabs")
	})
}

// renderedTabIDs lists the tab ids of owner's rendered rows across the given
// workspaces, in the listing's own order.
func renderedTabIDs(t *testing.T, st store.TestableStore, owner userid.UserID, workspaceIDs ...string) []string {
	t.Helper()
	rows, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{
		UserID: owner, WorkspaceIDs: workspaceIDs,
	})
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		assert.True(t, r.Rendered, "a rendered read returned row %s with its flag unset", r.TabID)
		ids = append(ids, r.TabID)
	}
	return ids
}

// ownedTabIDsByWorkspace groups one (owner, worker) pair's owned tab ids by
// workspace id. The owned view has no owner-blind by-workspace read -- see
// store.GetOwnedTabParams -- so a test that wants the per-workspace split
// starts from the owner-scoped by-worker list.
func ownedTabIDsByWorkspace(t *testing.T, st store.TestableStore, owner userid.UserID, workerID string) map[string][]string {
	t.Helper()
	rows, err := st.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
		UserID: owner, WorkerID: workerID,
	})
	require.NoError(t, err)
	out := make(map[string][]string, len(rows))
	for _, r := range rows {
		out[r.WorkspaceID] = append(out[r.WorkspaceID], r.TabID)
	}
	return out
}

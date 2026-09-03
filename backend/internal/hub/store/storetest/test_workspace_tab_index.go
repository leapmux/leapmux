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
	t.Run("bulk upsert and delete owned and rendered", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "bulk-tabidx-user")
		worker := SeedWorker(t, st, user.ID)
		wsA := SeedWorkspace(t, st, user.ID, "A")
		wsB := SeedWorkspace(t, st, user.ID, "B")

		owner := userid.MustNew(user.ID)
		ownedRows := []store.UpsertOwnedTabParams{
			{UserID: owner, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o1", TileID: "tile-a", Position: "a0"},
			{UserID: owner, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o2", TileID: "tile-a", Position: "a1"},
			{UserID: owner, WorkspaceID: wsB, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o3", TileID: "tile-b", Position: "b0"},
		}
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, ownedRows))

		// Verify all three rows landed. The owned view is read per (owner,
		// worker) -- there is no owner-blind by-workspace read, deliberately
		// (see store.GetOwnedTabParams) -- so group the worker's rows by
		// workspace to check the wsA/wsB split.
		byWS := ownedTabIDsByWorkspace(t, st, owner, worker.ID)
		assert.ElementsMatch(t, []string{"o1", "o2"}, byWS[wsA])
		assert.ElementsMatch(t, []string{"o3"}, byWS[wsB])

		// Bulk upsert again with one row's position changed: the
		// conflict path must fire and update in place rather than
		// erroring on the duplicate primary key.
		ownedRows[0].Position = "a-updated"
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, ownedRows))
		row, err := st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{UserID: owner, TabID: "o1"})
		require.NoError(t, err)
		assert.Equal(t, "a-updated", row.Position)

		// Same set, but for the rendered view.
		renderedRows := []store.UpsertRenderedTabParams{
			{UserID: owner, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "r1", TileID: "tile-a", Position: "a0"},
			{UserID: owner, WorkspaceID: wsB, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "r2", TileID: "tile-b", Position: "b0"},
		}
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertRendered(ctx, renderedRows))
		gotRendered, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{UserID: owner, WorkspaceIDs: []string{wsA, wsB}})
		require.NoError(t, err)
		assert.Len(t, gotRendered, 2)

		// Bulk delete a subset of owned rows: only o1 and o3 should
		// remain after the call (o2 is deleted).
		require.NoError(t, st.WorkspaceTabIndex().BulkDeleteOwned(ctx, []store.TabIndexKey{
			{UserID: owner, TabID: "o2"},
		}))
		byWS = ownedTabIDsByWorkspace(t, st, owner, worker.ID)
		assert.ElementsMatch(t, []string{"o1"}, byWS[wsA])
		assert.ElementsMatch(t, []string{"o3"}, byWS[wsB], "the delete named o2 only")

		// Bulk delete every rendered row in one call: both r1 and
		// r2 should be gone.
		require.NoError(t, st.WorkspaceTabIndex().BulkDeleteRendered(ctx, []store.TabIndexKey{
			{UserID: owner, TabID: "r1"},
			{UserID: owner, TabID: "r2"},
		}))
		gotRendered, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{UserID: owner, WorkspaceIDs: []string{wsA, wsB}})
		require.NoError(t, err)
		assert.Empty(t, gotRendered)

		// Empty inputs must be no-ops, not errors.
		assert.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkUpsertRendered(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkDeleteOwned(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkDeleteRendered(ctx, nil))
	})

	// workspace_tab_owned.user_id REFERENCES users(id), mirroring every sibling
	// CRDT table. That FK is what makes a blank-owner row UNREPRESENTABLE rather
	// than merely undeletable: before it, a blank owner was insertable while
	// every delete path refused to bind one, so the row was creatable, kept an
	// agent/terminal alive through the worker reconciler (which reads _owned as
	// the authority on "this tab still exists"), and no API could clear it.
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
		// the type alone cannot stop this row from being ASSEMBLED -- the FK is
		// what stops it being stored. store.UpsertRenderedTabParams is an alias
		// of store.UpsertOwnedTabParams, so one literal covers both views.
		//
		// The refusal text is logged rather than asserted on: it is the evidence
		// the constraint is live in THIS dialect, and pinning three dialects'
		// driver strings would break on a driver upgrade without telling anyone
		// anything about the schema.
		blank := store.UpsertOwnedTabParams{
			UserID: userid.UserID{}, WorkspaceID: ws, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "orphan",
			TileID: "tile-1", Position: "a2",
		}
		for _, c := range []struct {
			name string
			run  func() error
		}{
			{"UpsertOwned", func() error { return st.WorkspaceTabIndex().UpsertOwned(ctx, blank) }},
			{"BulkUpsertOwned", func() error {
				return st.WorkspaceTabIndex().BulkUpsertOwned(ctx, []store.UpsertOwnedTabParams{blank})
			}},
			{"UpsertRendered", func() error { return st.WorkspaceTabIndex().UpsertRendered(ctx, blank) }},
			{"BulkUpsertRendered", func() error {
				return st.WorkspaceTabIndex().BulkUpsertRendered(ctx, []store.UpsertRenderedTabParams{blank})
			}},
		} {
			err := c.run()
			if assert.Errorf(t, err, "%s: a blank owner names no user, so the row must not be storable", c.name) {
				t.Logf("%s refused the blank owner with: %v", c.name, err)
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

	// The bulk deletes bind `WHERE (user_id, tab_id) IN ((?, ?), ...)`. A zero
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
	//   - Nothing to reap. `user_id REFERENCES users(id)` admits a blank-owner
	//     tab row only once a blank-ID USER exists, and CreateUserParams.Validate
	//     now refuses that id -- asserted below, because it is the load-bearing
	//     half and a silent deletion of the old seed would hide its regression.
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

		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, []store.UpsertOwnedTabParams{
			{UserID: owner, WorkspaceID: ws, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "keep", TileID: "tile-1", Position: "a0"},
			{UserID: owner, WorkspaceID: ws, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "drop", TileID: "tile-1", Position: "a1"},
		}))

		// The blank-owner ROW this case used to seed is no longer creatable: it
		// needed a blank-ID user as its FK parent, and CreateUserParams.Validate
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
		require.NoError(t, st.WorkspaceTabIndex().BulkDeleteOwned(ctx, []store.TabIndexKey{
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
	// `file-<millis>-<counter>`, so two clients collide readily), and neither
	// workspace_id nor worker_id constrains a row's owner -- both are plain FKs
	// to rows that name an owner of their own.
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
		// One workspace, owned by A. workspace_tab_owned.workspace_id is a
		// plain FK to workspaces(id), so B's rows may name it too.
		wsA := SeedWorkspace(t, st, userA.ID, "Scope A")

		const sharedTabID = "file-1700000000000-1"
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
			UserID: userid.MustNew(userB.ID), WorkspaceID: wsA, WorkerID: workerB.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-b", Position: "b0",
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
			UserID: ownerA, WorkspaceID: wsA, WorkerID: workerA.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-a", Position: "a0",
		}))
		// A foreign row hosted on the REQUESTING owner's worker: worker_id
		// alone is not a tenancy scope either.
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
			UserID: userid.MustNew(userB.ID), WorkspaceID: wsA, WorkerID: workerA.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "b-only", TileID: "tile-b", Position: "b1",
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
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsA, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
			TileID: "tile-a", Position: "a0",
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsA, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a2",
			TileID: "tile-a", Position: "a1",
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsB, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "b1",
			TileID: "tile-b", Position: "b0",
		}))

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
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			UserID: userid.MustNew(owner.ID), WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "loc1", TileID: "tile", Position: "a0",
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

	// The rendered view is keyed on (user_id, tab_id) and its workspace_id is a
	// plain FK to workspaces(id) -- nothing ties a row's owner to the
	// workspace's. So a foreign row naming an owned workspace is REPRESENTABLE,
	// and the reads that verified only the caller's access to the workspace
	// returned it: GetRendered picked whichever row the index visited first,
	// ListRenderedByWorkspaceIDs listed both, and LocateAccessibleRendered's
	// owner filter was on the WORKSPACE rather than on the row.
	//
	// This is the same argument already written into store.GetOwnedTabParams;
	// the rendered twin was simply never given the predicate.
	t.Run("rendered reads are scoped to the row owner, not just the workspace", func(t *testing.T) {
		st := s.NewStore(t)
		owner := SeedUser(t, st, "rendered-owner")
		other := SeedUser(t, st, "rendered-other")
		worker := SeedWorker(t, st, owner.ID)
		wsID := SeedWorkspace(t, st, owner.ID, "Rendered WS")

		ownerID := userid.MustNew(owner.ID)
		otherID := userid.MustNew(other.ID)
		// Same tab_id under two owners: client-minted ids are unique only
		// within a user, so this collision is ordinary, not adversarial.
		const sharedTab = "shared-tab"
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			UserID: ownerID, WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab, TileID: "tile-owner", Position: "a0",
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			UserID: otherID, WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab, TileID: "tile-other", Position: "a0",
		}))

		row, err := st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: ownerID, WorkspaceID: wsID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		require.NoError(t, err)
		assert.Equal(t, "tile-owner", row.TileID, "GetRendered must return the CALLER's row, not whichever the index reached first")

		row, err = st.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			UserID: otherID, WorkspaceID: wsID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		require.NoError(t, err)
		assert.Equal(t, "tile-other", row.TileID, "control: the other owner still reaches their own row")

		got, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{
			UserID: ownerID, WorkspaceIDs: []string{wsID},
		})
		require.NoError(t, err)
		require.Len(t, got, 1, "the listing must not leak the other owner's row for the same workspace")
		assert.Equal(t, "tile-owner", got[0].TileID)

		located, err := st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			UserID: ownerID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: sharedTab,
		})
		require.NoError(t, err)
		assert.Equal(t, "tile-owner", located.TileID,
			"the workspace-owner filter proves the CALLER owns the workspace, not that the ROW does; LIMIT 1 could otherwise return the foreign row")

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

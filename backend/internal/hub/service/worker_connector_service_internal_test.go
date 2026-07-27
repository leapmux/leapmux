package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// tabSyncJournal is a minimal crdt.Journal that records, per user, the
// tab ids every committed batch tombstoned. The worker tab-sync tests
// assert tenancy by reading it: a tombstone that lands in the wrong
// user's journal is exactly the cross-tenant bug under test.
type tabSyncJournal struct {
	mu       sync.Mutex
	byUser   map[string][]string
	batchIDs []string
}

func newTabSyncJournal() *tabSyncJournal {
	return &tabSyncJournal{byUser: map[string][]string{}}
}

func (j *tabSyncJournal) LoadState(context.Context, string) (*leapmuxv1.UserCrdtState, []*leapmuxv1.OpBatch, error) {
	return nil, nil, nil
}

func (j *tabSyncJournal) CommitBatch(_ context.Context, c crdt.CommitBatch) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.batchIDs = append(j.batchIDs, c.Batch.GetBatchId())
	for _, op := range c.Batch.GetOps() {
		if body, ok := op.GetBody().(*leapmuxv1.CrdtOp_TombstoneTab); ok {
			j.byUser[c.UserID] = append(j.byUser[c.UserID], body.TombstoneTab.GetTabId())
		}
	}
	return nil
}

func (j *tabSyncJournal) LookupRecentBatchID(context.Context, string, string) (*crdt.RecentBatchRecord, error) {
	return nil, crdt.ErrNotFound
}

func (j *tabSyncJournal) AdvanceEpoch(context.Context, string, int64, time.Time) error { return nil }

func (j *tabSyncJournal) CompactBatch(context.Context, crdt.CompactBatch) error { return nil }

func (j *tabSyncJournal) CleanupExpiredRecentBatchIDs(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (j *tabSyncJournal) tombstonedTabs(userID string) []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.byUser[userID]...)
}

func (j *tabSyncJournal) committedBatchIDs() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.batchIDs...)
}

// tabSyncAuth allows every workspace/worker; the tab-sync tests are
// about which tenant a tombstone is submitted to, not the auth matrix.
type tabSyncAuth struct{}

func (tabSyncAuth) CanAccessWorkspace(context.Context, string, string) (bool, error) {
	return true, nil
}
func (tabSyncAuth) CanUseWorker(context.Context, string, string) (bool, error) { return true, nil }

// recordingRegistry wraps a real crdt.Registry and records every user
// id Get was asked for, in call order. It delegates to the wrapped
// registry so the managers it hands out are genuinely running.
type recordingRegistry struct {
	mu         sync.Mutex
	gotUserIDs []string
	inner      *crdt.Registry
	// failFor makes Get fail for one user, standing in for the tenants
	// Registry.Get genuinely refuses (a blank owner) without needing a
	// blank-owner row the users(id) FK forbids.
	failFor string
}

func (r *recordingRegistry) Get(ctx context.Context, userID string) (*crdt.Manager, error) {
	r.mu.Lock()
	r.gotUserIDs = append(r.gotUserIDs, userID)
	fail := r.failFor != "" && r.failFor == userID
	r.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("no manager for %q", userID)
	}
	return r.inner.Get(ctx, userID)
}

func (r *recordingRegistry) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.gotUserIDs...)
}

func newRecordingRegistry(t *testing.T, j crdt.Journal) *recordingRegistry {
	t.Helper()
	inner := crdt.NewRegistry(func(ctx context.Context, userID userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(userID, j, tabSyncAuth{}, slog.New(slog.DiscardHandler), time.Now)
		if err := m.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return m, nil
	}, slog.New(slog.DiscardHandler), crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { inner.Shutdown(5 * time.Second) })
	return &recordingRegistry{inner: inner}
}

// TestHandleWorkspaceTabsSync_TombstonesOnlyTheRegistrant pins the tenancy of
// the stale-tombstone batch. workspace_tab_owned is keyed by (user_id, tab_id)
// and nothing ties a row's owner to the registrant of the worker it names, so
// two owners' rows can legitimately name worker w1 -- but ListOwnedByWorker
// binds the registrant, so only that owner's rows reach the classification.
//
// The foreign row must therefore be untouched: not fetched, not tombstoned. A
// tombstone landing in bob's document would materialize a record for a tab he
// has never seen (SubmitInternal skips the per-op auth check), while alice's
// real row survives and her tabs live forever.
func TestHandleWorkspaceTabsSync_TombstonesOnlyTheRegistrant(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	userA := storetest.SeedUser(t, st, "alice")
	userB := storetest.SeedUser(t, st, "bob")
	seedOwnedTab(t, st, userA.ID, "alice ws", "tab-a")
	seedOwnedTab(t, st, userB.ID, "bob ws", "tab-b")
	ctx := context.Background()

	journal := newTabSyncJournal()
	reg := newRecordingRegistry(t, journal)
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, reg, nil)

	var sent []*leapmuxv1.ConnectResponse
	conn := &workermgr.Conn{
		WorkerID: "w1",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent = append(sent, msg)
			return nil
		},
	}

	// Empty worker report: the worker hosts nothing, so every row the hub
	// listed for this registrant is stale.
	svc.handleWorkspaceTabsSync(ctx, conn, "w1", userA.ID, "req-1", &leapmuxv1.WorkspaceTabsSync{})

	assert.Equal(t, []string{userA.ID}, reg.seen(),
		"only the registrant's manager may be resolved")
	assert.Equal(t, []string{"tab-a"}, journal.tombstonedTabs(userA.ID))
	assert.Empty(t, journal.tombstonedTabs(userB.ID),
		"a row owned by someone other than the registrant is out of scope entirely")
	assert.Len(t, sent, 1, "the worker must still get its sync response")

	// Batch ids must stay unique per submit within one sync, otherwise a
	// second batch would dedup against the first.
	ids := journal.committedBatchIDs()
	seen := map[string]bool{}
	for _, id := range ids {
		require.False(t, seen[id], fmt.Sprintf("duplicate batch id %q", id))
		seen[id] = true
	}
}

// TestHandleWorkspaceTabsSync_ManagerGetFailureStillResponds pins the error
// shape of the tombstone submit: an unresolvable manager is logged and skipped,
// and the worker still gets its sync response. Returning early instead would
// leave the worker waiting on a round-trip it uses to mark its initial sync
// complete.
func TestHandleWorkspaceTabsSync_ManagerGetFailureStillResponds(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	userA := storetest.SeedUser(t, st, "alice")
	seedOwnedTab(t, st, userA.ID, "w1", "tab-a")

	journal := newTabSyncJournal()
	reg := newRecordingRegistry(t, journal)
	reg.failFor = userA.ID
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, reg, nil)

	sent := 0
	conn := &workermgr.Conn{
		WorkerID: "w1",
		SendFn:   func(*leapmuxv1.ConnectResponse) error { sent++; return nil },
	}

	svc.handleWorkspaceTabsSync(context.Background(), conn, "w1", userA.ID, "req-1", &leapmuxv1.WorkspaceTabsSync{})

	assert.Equal(t, []string{userA.ID}, reg.seen())
	assert.Empty(t, journal.tombstonedTabs(userA.ID), "nothing commits when the manager cannot be resolved")
	assert.Equal(t, 1, sent, "the worker still gets its sync response")
}

// TestHandleWorkspaceTabsSync_ForeignOwnerTabIDCollisionIsInvisible pins what
// the owner predicate bought. Tab ids are client-chosen and the table is unique
// on (user_id, tab_id), so two owners can hold the SAME id on one worker; the
// worker's report carries no user axis at all, so nothing on this side could
// disambiguate them. An owner-blind query put both in a map keyed by (tab_type,
// tab_id), where one silently displaced the other and the survivor decided the
// classification -- here, bob's workspace would be pushed onto alice's tab as a
// reassignment.
//
// With the predicate bound, bob's row never enters the comparison.
func TestHandleWorkspaceTabsSync_ForeignOwnerTabIDCollisionIsInvisible(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	userA := storetest.SeedUser(t, st, "alice")
	userB := storetest.SeedUser(t, st, "bob")
	wsA := seedOwnedTab(t, st, userA.ID, "alice ws", "dup-tab")
	seedOwnedTab(t, st, userB.ID, "bob ws", "dup-tab")

	journal := newTabSyncJournal()
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, newRecordingRegistry(t, journal), nil)
	var sent []*leapmuxv1.ConnectResponse
	conn := &workermgr.Conn{WorkerID: "w1", SendFn: func(msg *leapmuxv1.ConnectResponse) error {
		sent = append(sent, msg)
		return nil
	}}

	// The worker hosts the tab, in alice's workspace, exactly as her row says.
	svc.handleWorkspaceTabsSync(context.Background(), conn, "w1", userA.ID, "req-1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: wsA, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "dup-tab"},
		},
	})

	require.Len(t, sent, 1)
	require.NotNil(t, sent[0].GetWorkspaceTabsSyncResp(), "the sync is acknowledged")
	// The tombstone journal is now the only observable: the response carries no
	// per-tab classification (see WorkspaceTabsSyncResponse). Alice's tab
	// matched her own row, so nothing of hers is stale -- and Bob's
	// identically-named row must not be dragged in by the id collision.
	assert.Empty(t, journal.tombstonedTabs(userA.ID))
	assert.Empty(t, journal.tombstonedTabs(userB.ID),
		"the foreign owner's identically-named row is out of scope, not stale")
}

// TestHandleWorkspaceTabsSync_BlankRegistrantIsRefused pins the fail-closed
// path: with no owner to scope the query by, the hub cannot tell which rows the
// worker's tabs correspond to, and an empty hub view would classify EVERY tab
// the worker reported as an orphan -- an instruction to drop every agent and
// terminal it hosts. Refuse the whole exchange instead.
func TestHandleWorkspaceTabsSync_BlankRegistrantIsRefused(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	userA := storetest.SeedUser(t, st, "alice")
	wsA := seedOwnedTab(t, st, userA.ID, "alice ws", "tab-a")

	logs := captureDefaultLogger(t)
	journal := newTabSyncJournal()
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, newRecordingRegistry(t, journal), nil)
	sent := 0
	conn := &workermgr.Conn{WorkerID: "w1", SendFn: func(*leapmuxv1.ConnectResponse) error { sent++; return nil }}

	svc.handleWorkspaceTabsSync(context.Background(), conn, "w1", "", "req-1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: wsA, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-a"},
			{WorkspaceId: wsA, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "not-in-crdt"},
		},
	})

	assert.Equal(t, 0, sent, "no classification is sent when the exchange cannot be scoped")
	assert.Empty(t, journal.tombstonedTabs(userA.ID))
	// Silent to the worker by design, so the log is the only operator signal.
	assert.Contains(t, logs.String(), "worker has a blank registrant")
}

// seedOwnedTab writes one workspace_tab_owned row for `worker` w1, creating the
// owner's workspace first (the row's workspace_id FK requires it). It returns
// the workspace id so callers can echo it in a worker tab report.
func seedOwnedTab(t *testing.T, st store.Store, ownerID, title, tabID string) string {
	t.Helper()
	wsID := storetest.SeedWorkspace(t, st, ownerID, title)
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(context.Background(), store.UpsertOwnedTabParams{
		UserID:      userid.MustNew(ownerID),
		WorkspaceID: wsID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		WorkerID:    "w1",
		TileID:      "tile-" + tabID,
		Position:    "a0",
	}))
	return wsID
}

// captureDefaultLogger redirects slog's default logger into a buffer for the
// duration of one test. handleWorkspaceTabsSync logs through the package-level
// slog functions, so this is the only seam onto its diagnostics.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

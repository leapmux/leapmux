package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// Tests in this file do NOT call t.Parallel(), unlike the rest of the package.
// They swap slog's default handler to capture log output, and that handler is
// process-global: run alongside a sibling, the buffer they assert on collects
// whatever the sibling logged too. Everything else here owns its own store,
// server and temp dirs and runs parallel.

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

func (j *tabSyncJournal) ListBatchesAfter(context.Context, string, *leapmuxv1.HLC, *leapmuxv1.HLC, int, int) ([]crdt.ResumeBatch, []crdt.CorruptRow, error) {
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
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, reg, sendq.NewMaxBytesPoolForTest())

	var mu sync.Mutex
	var sent []*leapmuxv1.ConnectResponse
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(msg *leapmuxv1.ConnectResponse) error {
		mu.Lock()
		sent = append(sent, msg)
		mu.Unlock()
		return nil
	})

	// Empty worker report: the worker hosts nothing, so every row the hub
	// listed for this registrant is stale.
	svc.handleWorkspaceTabsSync(ctx, conn, "w1", userA.ID, "req-1", &leapmuxv1.WorkerTabInventory{})

	assert.Equal(t, []string{userA.ID}, reg.seen(),
		"only the registrant's manager may be resolved")
	assert.Equal(t, []string{"tab-a"}, journal.tombstonedTabs(userA.ID))
	assert.Empty(t, journal.tombstonedTabs(userB.ID),
		"a row owned by someone other than the registrant is out of scope entirely")
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sent) == 1
	}, time.Second, time.Millisecond, "the worker must still get its sync response")

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
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, reg, sendq.NewMaxBytesPoolForTest())

	var sent atomic.Int32
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(*leapmuxv1.ConnectResponse) error {
		sent.Add(1)
		return nil
	})

	svc.handleWorkspaceTabsSync(context.Background(), conn, "w1", userA.ID, "req-1", &leapmuxv1.WorkerTabInventory{})

	assert.Equal(t, []string{userA.ID}, reg.seen())
	assert.Empty(t, journal.tombstonedTabs(userA.ID), "nothing commits when the manager cannot be resolved")
	require.Eventually(t, func() bool { return sent.Load() == 1 }, time.Second, time.Millisecond,
		"the worker still gets its sync response")
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
	_ = seedOwnedTab(t, st, userA.ID, "alice ws", "dup-tab")
	seedOwnedTab(t, st, userB.ID, "bob ws", "dup-tab")

	journal := newTabSyncJournal()
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, newRecordingRegistry(t, journal), sendq.NewMaxBytesPoolForTest())
	var mu sync.Mutex
	var sent []*leapmuxv1.ConnectResponse
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(msg *leapmuxv1.ConnectResponse) error {
		mu.Lock()
		sent = append(sent, msg)
		mu.Unlock()
		return nil
	})

	// The worker hosts the tab, in alice's workspace, exactly as her row says.
	svc.handleWorkspaceTabsSync(context.Background(), conn, "w1", userA.ID, "req-1", &leapmuxv1.WorkerTabInventory{
		Tabs: []*leapmuxv1.TabRef{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "dup-tab"},
		},
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sent) == 1
	}, time.Second, time.Millisecond)
	mu.Lock()
	require.NotNil(t, sent[0].GetWorkerTabInventoryResp(), "the sync is acknowledged")
	mu.Unlock()
	// The tombstone journal is now the only observable: the response carries no
	// per-tab classification (see WorkerTabInventoryResponse). Alice's tab
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
	_ = seedOwnedTab(t, st, userA.ID, "alice ws", "tab-a")

	logs := testutil.CaptureDefaultLogger(t)
	journal := newTabSyncJournal()
	svc := NewWorkerConnectorService(st, nil, nil, nil, nil, nil, newRecordingRegistry(t, journal), sendq.NewMaxBytesPoolForTest())
	sent := 0
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(msg *leapmuxv1.ConnectResponse) error {
		sent++
		return nil
	})

	svc.handleWorkspaceTabsSync(context.Background(), conn, "w1", "", "req-1", &leapmuxv1.WorkerTabInventory{
		Tabs: []*leapmuxv1.TabRef{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-a"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "not-in-crdt"},
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

// closeWorkerChannels used to branch on a shutdown channel and skip everything below
// it, which meant the Hub had two teardown paths for a disconnecting worker and
// only ever exercised the second one on the way out. These pin the single path:
// the channels go, whatever else is happening to the process.
func TestCloseWorkerChannels_ClosesTheWorkersChannels(t *testing.T) {
	cMgr := channelmgr.New(0)
	svc := NewWorkerConnectorService(nil, nil, cMgr, nil, nil, nil, nil, sendq.NewMaxBytesPoolForTest())

	cMgr.RegisterWithAuthInfo("ch1", "w1", "u1", channelmgr.AuthInfo{}, nil)
	cMgr.RegisterWithAuthInfo("ch2", "w1", "u1", channelmgr.AuthInfo{}, nil)
	cMgr.RegisterWithAuthInfo("ch-other", "w2", "u1", channelmgr.AuthInfo{}, nil)

	svc.closeWorkerChannels("w1")

	assert.False(t, cMgr.Exists("ch1"))
	assert.False(t, cMgr.Exists("ch2"))
	assert.True(t, cMgr.Exists("ch-other"), "another worker's channels are not this worker's to close")
}

// The channels a relay disconnect already closed are the common case at
// shutdown, and they must leave no trace: nothing to close means nothing to say.
func TestCloseWorkerChannels_IsSilentWhenThereIsNothingToClose(t *testing.T) {
	cMgr := channelmgr.New(0)
	svc := NewWorkerConnectorService(nil, nil, cMgr, nil, nil, nil, nil, sendq.NewMaxBytesPoolForTest())
	buf := testutil.CaptureDefaultLogger(t)

	svc.closeWorkerChannels("w-with-no-channels")

	assert.Empty(t, buf.String())
}

// TestStreamEndReason pins the vocabulary of the connect-time disconnect log:
// the two shapes that are the worker's own doing (no observed error, and a
// clean EOF) collapse to the one healthy phrase, and everything else keeps
// its verbatim error so an operator reads the actual transport failure.
func TestStreamEndReason(t *testing.T) {
	assert.Equal(t, "worker closed the stream", streamEndReason(nil),
		"no observed receive error is the handler exiting, not the stream failing")
	assert.Equal(t, "worker closed the stream", streamEndReason(io.EOF),
		"a clean EOF is the worker's own hang-up")
	assert.Equal(t,
		"worker stream failed: read unix /hub.sock->: i/o timeout",
		streamEndReason(errors.New("read unix /hub.sock->: i/o timeout")))
}

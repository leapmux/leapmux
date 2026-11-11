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
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

// Tests that capture slog's default logger do not call t.Parallel().
// The logger is process-global, so concurrent tests could add unrelated
// lines to the captured output. Other tests in this file may run in parallel.

// tabSyncJournal is a minimal crdt.Journal that records, per user, the
// tab ids every committed batch tombstoned. The worker tab-sync tests
// assert tenancy by reading it: a tombstone that lands in the wrong
// user's journal is exactly the cross-tenant bug under test.
type tabSyncJournal struct {
	publications map[string]*crdt.PublicationReceipt
	mu           sync.Mutex
	byUser       map[string][]string
	batchIDs     []string
	commitErr    error
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
	if j.commitErr != nil {
		return j.commitErr
	}
	if c.Publication != nil {
		j.recordPublication(c.UserID, *c.Publication)
	}
	j.batchIDs = append(j.batchIDs, c.Batch.GetBatchId())
	for _, op := range c.Batch.GetOps() {
		if body, ok := op.GetBody().(*leapmuxv1.CrdtOp_TombstoneTab); ok {
			j.byUser[c.UserID] = append(j.byUser[c.UserID], body.TombstoneTab.GetTab().GetTabId())
		}
	}
	return nil
}

func (j *tabSyncJournal) LookupRecentBatchID(context.Context, string, string) (*crdt.RecentBatchRecord, error) {
	return nil, crdt.ErrNotFound
}

func (j *tabSyncJournal) CompactBatch(context.Context, crdt.CompactBatch) error { return nil }

func (j *tabSyncJournal) CleanupExpiredRecentBatchIDs(context.Context, string, time.Time, int64) (int64, error) {
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

// BroadcastWorkersChanged forwards to the wrapped registry; it records nothing.
func (r *recordingRegistry) BroadcastWorkersChanged(userID string) {
	r.inner.BroadcastWorkersChanged(userID)
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

func seedInventoryTab(t *testing.T, reg *recordingRegistry, owner, tabID string) *crdt.Manager {
	t.Helper()
	manager, err := reg.inner.Get(t.Context(), owner)
	require.NoError(t, err)
	manager.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.Workspaces["ws"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "ws", RootNodeId: "root"}
		state.Nodes["root"] = &leapmuxv1.NodeRecord{NodeId: "root", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}}
		state.Tabs[tabID] = &leapmuxv1.TabRecord{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: "root"}, WorkerId: &leapmuxv1.LWWString{Value: "w1"}, Position: &leapmuxv1.LWWString{Value: "a0"}}
	})
	return manager
}

func TestWorkerInventorySegments_ReconcilesOnlyOnCompletion(t *testing.T) {
	journal := newTabSyncJournal()
	reg := newRecordingRegistry(t, journal)
	seedInventoryTab(t, reg, "alice", "present")
	seedInventoryTab(t, reg, "alice", "stale")
	seedInventoryTab(t, reg, "bob", "stale")
	svc := &WorkerConnectorService{crdtRegistry: reg}
	inventory, err := svc.beginWorkerInventory(t.Context(), "w1", "alice")
	require.NoError(t, err)
	var sent atomic.Int32
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(*leapmuxv1.ConnectResponse) error { sent.Add(1); return nil })
	require.NoError(t, svc.handleWorkspaceTabsSync(t.Context(), conn, "", &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "present"}}}, inventory))
	require.Empty(t, journal.tombstonedTabs("alice"))
	require.Zero(t, sent.Load())
	// This tab was not a candidate when the connection was admitted.
	seedInventoryTab(t, reg, "alice", "new")
	require.NoError(t, svc.handleWorkspaceTabsSync(t.Context(), conn, "", &leapmuxv1.WorkerTabInventory{Complete: true}, inventory))
	assert.Equal(t, []string{"stale"}, journal.tombstonedTabs("alice"))
	assert.Empty(t, journal.tombstonedTabs("bob"))
	assert.Equal(t, []string{"alice", "alice"}, reg.seen())
	testutil.AssertEventually(t, func() bool { return sent.Load() == 1 })
	require.Error(t, svc.handleWorkspaceTabsSync(t.Context(), conn, "", &leapmuxv1.WorkerTabInventory{Complete: true}, inventory))
}

func TestWorkerInventorySegments_RejectsMalformedReports(t *testing.T) {
	for _, report := range []*leapmuxv1.WorkerTabInventory{
		{}, {Tabs: []*leapmuxv1.TabIdent{nil}, Complete: true},
		{Tabs: []*leapmuxv1.TabIdent{{TabId: "tab"}}, Complete: true},
		{Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}}, Complete: true},
		{Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType(-1), TabId: "tab"}}, Complete: true},
		{Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType(999), TabId: "tab"}}, Complete: true},
	} {
		t.Run(fmt.Sprint(report), func(t *testing.T) {
			svc := &WorkerConnectorService{}
			conn := workermgrtest.NewConnWithWrite(t, "w1", func(*leapmuxv1.ConnectResponse) error { t.Error("malformed inventory was acknowledged"); return nil })
			require.Error(t, svc.handleWorkspaceTabsSync(t.Context(), conn, "", report, &workerInventory{}))
		})
	}
}

func TestWorkerInventoryAdmission_RefusesMissingOwnerOrManager(t *testing.T) {
	journal := newTabSyncJournal()
	reg := newRecordingRegistry(t, journal)
	reg.failFor = "alice"
	svc := &WorkerConnectorService{crdtRegistry: reg}
	_, err := svc.beginWorkerInventory(t.Context(), "w1", "")
	require.ErrorContains(t, err, "blank registrant")
	_, err = svc.beginWorkerInventory(t.Context(), "w1", "alice")
	require.ErrorContains(t, err, "no manager")
	assert.Empty(t, journal.committedBatchIDs())
}

func TestWorkerInventorySegments_ForeignIdentityCollisionIsInvisible(t *testing.T) {
	journal := newTabSyncJournal()
	reg := newRecordingRegistry(t, journal)
	seedInventoryTab(t, reg, "alice", "same")
	seedInventoryTab(t, reg, "bob", "same")
	svc := &WorkerConnectorService{crdtRegistry: reg}
	inventory, err := svc.beginWorkerInventory(t.Context(), "w1", "alice")
	require.NoError(t, err)
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(*leapmuxv1.ConnectResponse) error { return nil })
	require.NoError(t, svc.handleWorkspaceTabsSync(t.Context(), conn, "", &leapmuxv1.WorkerTabInventory{Complete: true, Tabs: []*leapmuxv1.TabIdent{{TabId: "same", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}}}, inventory))
	assert.Empty(t, journal.tombstonedTabs("alice"))
	assert.Empty(t, journal.tombstonedTabs("bob"))
}

// closeWorkerChannels used to branch on a shutdown channel and skip everything below
// it, which meant the Hub had two teardown paths for a disconnecting worker and
// only ever exercised the second one on the way out. These pin the single path:
// the channels go, whatever else is happening to the process.
func TestCloseWorkerChannels_ClosesTheWorkersChannels(t *testing.T) {
	cMgr := channelmgr.New(0)
	svc := NewWorkerConnectorService(WorkerConnectorServiceDeps{
		Store:             nil,
		WorkerManager:     nil,
		ChannelManager:    cMgr,
		PendingRequests:   nil,
		Notifier:          nil,
		Registry:          nil,
		QueuePool:         sendq.NewMaxBytesPoolForTest(),
		Clock:             quartz.NewReal(),
		DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil),
	})

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
	svc := NewWorkerConnectorService(WorkerConnectorServiceDeps{
		Store:             nil,
		WorkerManager:     nil,
		ChannelManager:    cMgr,
		PendingRequests:   nil,
		Notifier:          nil,
		Registry:          nil,
		QueuePool:         sendq.NewMaxBytesPoolForTest(),
		Clock:             quartz.NewReal(),
		DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil),
	})
	buf := testutil.CaptureDefaultLogger(t)

	svc.closeWorkerChannels("w-with-no-channels")

	assert.Empty(t, buf.String())
}

// TestStreamEndReason verifies that nil, EOF, and accepted WebSocket close
// statuses produce the healthy close text. Other errors keep their text.
func TestStreamEndReason(t *testing.T) {
	abnormalClose := websocket.CloseError{Code: websocket.StatusInternalError, Reason: "x"}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"no observed error", nil, "worker closed the stream"},
		{"eof", io.EOF, "worker closed the stream"},
		{"normal close", websocket.CloseError{Code: websocket.StatusNormalClosure}, "worker closed the stream"},
		{"going away", websocket.CloseError{Code: websocket.StatusGoingAway}, "worker closed the stream"},
		{"no status", websocket.CloseError{Code: websocket.StatusNoStatusRcvd}, "worker closed the stream"},
		{"abnormal close", abnormalClose, "worker stream failed: " + abnormalClose.Error()},
		{"transport failure", errors.New("read unix /hub.sock->: i/o timeout"), "worker stream failed: read unix /hub.sock->: i/o timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, streamEndReason(tc.err))
		})
	}
}

func TestValidateWorkerHello(t *testing.T) {
	t.Parallel()

	// A complete, valid set: every defect case below starts from it, so exactly
	// one thing is wrong with each.
	complete := workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	hello := func(mode leapmuxv1.EncryptionMode, mutate func(*leapmuxv1.WorkerPublicKeys)) *leapmuxv1.ConnectRequest {
		keys := proto.Clone(complete).(*leapmuxv1.WorkerPublicKeys)
		if mutate != nil {
			mutate(keys)
		}
		return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{Hello: &leapmuxv1.WorkerHello{
			Keys:           keys,
			EncryptionMode: mode,
		}}}
	}
	classic := leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC
	pq := leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM

	for name, msg := range map[string]*leapmuxv1.ConnectRequest{
		"classic with every key":      hello(classic, nil),
		"post-quantum with every key": hello(pq, nil),
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			got, err := validateWorkerHello(msg)
			require.NoError(t, err)
			assert.Same(t, msg.GetHello(), got)
		})
	}

	// A first message that is not a hello, or a mode that is not supported.
	for name, msg := range map[string]*leapmuxv1.ConnectRequest{
		"a nil message":       nil,
		"a tab inventory":     {Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{}}},
		"an unspecified mode": hello(leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED, nil),
		"an unknown mode":     hello(leapmuxv1.EncryptionMode(99), nil),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := validateWorkerHello(msg)
			require.Error(t, err)
		})
	}

	t.Run("refuses a hello with no keys", func(t *testing.T) {
		_, err := validateWorkerHello(&leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{
			Hello: &leapmuxv1.WorkerHello{EncryptionMode: classic},
		}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	// Every key defect, each on an otherwise complete set, and each error must
	// identify the key that is wrong.
	for name, tc := range map[string]struct {
		msg *leapmuxv1.ConnectRequest
		key string
	}{
		"classic with mlkem but no slhdsa": {
			msg: hello(classic, func(k *leapmuxv1.WorkerPublicKeys) { k.Slhdsa = nil }), key: "slhdsa",
		},
		"classic with slhdsa but no mlkem": {
			msg: hello(classic, func(k *leapmuxv1.WorkerPublicKeys) { k.Mlkem = nil }), key: "mlkem",
		},
		"a short x25519": {
			msg: hello(classic, func(k *leapmuxv1.WorkerPublicKeys) { k.X25519 = k.X25519[:31] }), key: "x25519",
		},
		"a long x25519": {
			msg: hello(classic, func(k *leapmuxv1.WorkerPublicKeys) { k.X25519 = append(k.X25519, 0) }), key: "x25519",
		},
		"a short mlkem": {
			msg: hello(pq, func(k *leapmuxv1.WorkerPublicKeys) { k.Mlkem = k.Mlkem[:100] }), key: "mlkem",
		},
		"a short slhdsa": {
			msg: hello(pq, func(k *leapmuxv1.WorkerPublicKeys) { k.Slhdsa = k.Slhdsa[:10] }), key: "slhdsa",
		},
		"an mlkem key of the right size filled with 0xff": {
			msg: hello(pq, func(k *leapmuxv1.WorkerPublicKeys) {
				for i := range k.Mlkem {
					k.Mlkem[i] = 0xFF
				}
			}),
			key: "mlkem",
		},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := validateWorkerHello(tc.msg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key, "the error must identify the defective key")
		})
	}
}

// The queue pool and the clock carry no nil-tolerant default, so the
// constructor refuses each one rather than let the first stream handler panic.
func TestNewWorkerConnectorServiceRequiresAQueuePoolAndAClock(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, "service: NewWorkerConnectorService requires a queue pool", func() {
		NewWorkerConnectorService(WorkerConnectorServiceDeps{
			Store:             nil,
			WorkerManager:     nil,
			ChannelManager:    nil,
			PendingRequests:   nil,
			Notifier:          nil,
			Registry:          nil,
			QueuePool:         nil,
			Clock:             quartz.NewReal(),
			DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil),
		})
	})
	assert.PanicsWithValue(t, "service: NewWorkerConnectorService requires a clock", func() {
		NewWorkerConnectorService(WorkerConnectorServiceDeps{
			Store:             nil,
			WorkerManager:     nil,
			ChannelManager:    nil,
			PendingRequests:   nil,
			Notifier:          nil,
			Registry:          nil,
			QueuePool:         sendq.NewMaxBytesPoolForTest(),
			Clock:             nil,
			DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil),
		})
	})
}

// scriptedWorkerStream answers each Receive from a channel that the test feeds.
// A Receive with nothing to answer blocks, as a real stream does.
type scriptedWorkerStream struct {
	results chan workerReceiveResult
}

func newScriptedWorkerStream() *scriptedWorkerStream {
	return &scriptedWorkerStream{results: make(chan workerReceiveResult, 8)}
}

func (s *scriptedWorkerStream) Receive() (*leapmuxv1.ConnectRequest, error) {
	result := <-s.results
	return result.msg, result.err
}

func (s *scriptedWorkerStream) feed(results ...workerReceiveResult) {
	for _, result := range results {
		s.results <- result
	}
}

func inventoryMessage(requestID string) *leapmuxv1.ConnectRequest {
	return &leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload:   &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{}},
	}
}

// Each case below ends inside a synctest bubble, which fails the test when a
// goroutine outlives the bubble's main goroutine. That is the proof that the
// pump stops: no case can observe an exit any other way.

// The hello wait and the main loop read one channel, so the pump delivers every
// outcome in stream order, and a receive error ends it.
func TestStartWorkerReceivePump_DeliversInOrderAndStopsAfterAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := newScriptedWorkerStream()
		received := startWorkerReceivePump(t.Context(), stream)

		stream.feed(
			workerReceiveResult{msg: inventoryMessage("first")},
			workerReceiveResult{msg: inventoryMessage("second")},
			workerReceiveResult{err: io.EOF},
		)
		assert.Equal(t, "first", (<-received).msg.GetRequestId())
		assert.Equal(t, "second", (<-received).msg.GetRequestId())
		assert.ErrorIs(t, (<-received).err, io.EOF)
		synctest.Wait()
	})
}

// A handler that stopped reading leaves the pump parked on a full channel, and
// the connection's context is what ends it.
func TestStartWorkerReceivePump_StopsWhenTheContextEndsWithAFullChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		stream := newScriptedWorkerStream()
		received := startWorkerReceivePump(ctx, stream)

		stream.feed(
			workerReceiveResult{msg: inventoryMessage("buffered")},
			workerReceiveResult{msg: inventoryMessage("read ahead")},
		)
		synctest.Wait()
		require.Len(t, received, 1, "precondition: the channel is full, and the pump holds the next message")

		cancel()
		synctest.Wait()
		assert.Equal(t, "buffered", (<-received).msg.GetRequestId(), "a parked pump delivers nothing more")
	})
}

// A handler that returns ends the stream, which unblocks Receive with an error,
// and cancels the context. The pump stops whichever way that error goes.
func TestStartWorkerReceivePump_StopsWhenTheStreamEndsAfterTheContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		stream := newScriptedWorkerStream()
		startWorkerReceivePump(ctx, stream)
		synctest.Wait()

		cancel()
		stream.feed(workerReceiveResult{err: errors.New("the stream ended")})
		synctest.Wait()
	})
}

// receiveHello reads the stream only through the pump's channel, so each outcome
// of the hello wait is one result on that channel.
func TestReceiveHello_TakesItsResultFromThePump(t *testing.T) {
	t.Parallel()

	svc := &WorkerConnectorService{clock: quartz.NewReal()}

	t.Run("reports a worker that closed the stream", func(t *testing.T) {
		received := make(chan workerReceiveResult, 1)
		received <- workerReceiveResult{err: io.EOF}
		hello, err := svc.receiveHello(t.Context(), received)
		require.NoError(t, err)
		assert.Nil(t, hello)
	})

	t.Run("returns a receive failure", func(t *testing.T) {
		failure := errors.New("the transport failed")
		received := make(chan workerReceiveResult, 1)
		received <- workerReceiveResult{err: failure}
		_, err := svc.receiveHello(t.Context(), received)
		assert.ErrorIs(t, err, failure)
	})

	t.Run("refuses a first message that is not a hello", func(t *testing.T) {
		received := make(chan workerReceiveResult, 1)
		received <- workerReceiveResult{msg: inventoryMessage("first")}
		_, err := svc.receiveHello(t.Context(), received)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a hello")
		requireStreamRefusal(t, err, websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage)
	})

	t.Run("accepts a normal close", func(t *testing.T) {
		received := make(chan workerReceiveResult, 1)
		received <- workerReceiveResult{err: websocket.CloseError{Code: websocket.StatusNormalClosure}}
		hello, err := svc.receiveHello(t.Context(), received)
		require.NoError(t, err)
		assert.Nil(t, hello)
	})
	t.Run("preserves a malformed frame refusal", func(t *testing.T) {
		refusal := refuseWorkerStream(websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage, channelwire.ErrMalformedFrame)
		received := make(chan workerReceiveResult, 1)
		received <- workerReceiveResult{err: refusal}
		_, err := svc.receiveHello(t.Context(), received)
		assert.Same(t, refusal, err)
	})
	t.Run("reports the hello timeout", func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		newTimer, stopTimer := testutil.NewTimerTraps(t, clock, workerHelloTimerTag)
		ctx := testutil.DeadlineContext(t)
		result := make(chan error, 1)
		go func() {
			_, err := (&WorkerConnectorService{clock: clock}).receiveHello(ctx, make(chan workerReceiveResult))
			result <- err
		}()
		delay := testutil.WaitForTimer(t, ctx, newTimer)
		assert.Equal(t, 10*time.Second, delay)
		testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
		requireStreamRefusal(t, <-result, websocket.StatusProtocolError, channelwire.WorkerCloseReasonHelloTimeout)
	})

	t.Run("returns the context's error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := svc.receiveHello(ctx, make(chan workerReceiveResult))
		assert.ErrorIs(t, err, context.Canceled)
	})
}

// A read that the identity cap ended and a read that a disconnect ended both leave
// ctx.Err() as context.Canceled. Only the cause tells an operator that the cap
// fired and held the owner's delivery lock for its whole length.
func TestOwnerUsername_LogsWhyTheReadEnded(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	st := hubtestutil.OpenTestStore(t)
	svc := NewWorkerConnectorService(WorkerConnectorServiceDeps{
		Store:             st,
		WorkerManager:     nil,
		ChannelManager:    nil,
		PendingRequests:   nil,
		Notifier:          nil,
		Registry:          nil,
		QueuePool:         sendq.NewMaxBytesPoolForTest(),
		Clock:             quartz.NewReal(),
		DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil),
	})
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.DeadlineExceeded)

	username, ok := svc.ownerUsername(ctx, "owner-1")

	assert.False(t, ok)
	assert.Empty(t, username)
	assert.Contains(t, logs.String(), "cause=\"context deadline exceeded\"")
}

func TestNewWorkerConnectorServiceRequiresDeregisterEffects(t *testing.T) {
	assert.PanicsWithValue(t, "service: NewWorkerConnectorService requires deregistration effects", func() {
		NewWorkerConnectorService(WorkerConnectorServiceDeps{
			Store:             nil,
			WorkerManager:     nil,
			ChannelManager:    nil,
			PendingRequests:   nil,
			Notifier:          nil,
			Registry:          nil,
			QueuePool:         sendq.NewMaxBytesPoolForTest(),
			Clock:             quartz.NewReal(),
			DeregisterEffects: nil,
		})
	})
}

func requireStreamRefusal(t *testing.T, err error, status websocket.StatusCode, reason string) {
	t.Helper()
	var refusal *workerStreamRefusal
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, status, refusal.status)
	assert.Equal(t, reason, refusal.reason)
}

func TestWorkerStreamRefusal(t *testing.T) {
	t.Parallel()
	cause := errors.New("invalid hello")
	err := refuseWorkerStream(websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage, cause)
	requireStreamRefusal(t, err, websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage)
	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), cause.Error())
}

func TestConnectionEndReason(t *testing.T) {
	t.Parallel()
	reqCtx, stop := context.WithCancel(t.Context())
	stop()
	assert.Equal(t, "request context ended (hub shutdown or transport closed)", connectionEndReason(reqCtx, reqCtx))
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{errWorkerStoppedAnsweringPings, "worker stopped answering pings"},
		{context.Canceled, "connection cancelled (fenced or replaced)"},
	} {
		connCtx, cancel := context.WithCancelCause(t.Context())
		cancel(tc.cause)
		assert.Equal(t, tc.want, connectionEndReason(t.Context(), connCtx))
	}
}

func TestWorkerClosedStream(t *testing.T) {
	t.Parallel()
	for _, code := range []websocket.StatusCode{websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd} {
		err := fmt.Errorf("receive: %w", websocket.CloseError{Code: code})
		assert.True(t, workerClosedStream(err))
		assert.Equal(t, "worker closed the stream", streamEndReason(err))
	}
	assert.True(t, workerClosedStream(io.EOF))
	for _, err := range []error{nil, errors.New("failed"), websocket.CloseError{Code: websocket.StatusInternalError, Reason: "x"}} {
		assert.False(t, workerClosedStream(err))
		if err != nil {
			assert.Contains(t, streamEndReason(err), "worker stream failed:")
		}
	}
}

func TestWorkerInventory_FailedCommitIsNotAcknowledged(t *testing.T) {
	journal := newTabSyncJournal()
	reg := newRecordingRegistry(t, journal)
	seedInventoryTab(t, reg, "alice", "stale")
	svc := &WorkerConnectorService{crdtRegistry: reg}
	inventory, err := svc.beginWorkerInventory(t.Context(), "w1", "alice")
	require.NoError(t, err)
	journal.mu.Lock()
	journal.commitErr = errors.New("commit unavailable")
	journal.mu.Unlock()
	var sent atomic.Int32
	conn := workermgrtest.NewConnWithWrite(t, "w1", func(*leapmuxv1.ConnectResponse) error { sent.Add(1); return nil })
	err = svc.handleWorkspaceTabsSync(t.Context(), conn, "", &leapmuxv1.WorkerTabInventory{Complete: true}, inventory)
	var refusal *workerStreamRefusal
	require.ErrorAs(t, err, &refusal)
	assert.Zero(t, sent.Load())
	assert.Empty(t, journal.tombstonedTabs("alice"))
}

func (j *tabSyncJournal) recordPublication(userID string, r crdt.PublicationReceipt) {
	if j.publications == nil {
		j.publications = make(map[string]*crdt.PublicationReceipt)
	}
	copy := r
	copy.RequestHash = append([]byte(nil), r.RequestHash...)
	copy.Response = proto.CloneOf(r.Response)
	j.publications[userID+"/"+r.WorkerID+"/"+r.TabID] = &copy
}
func (j *tabSyncJournal) LookupTabPublication(_ context.Context, userID, workerID, tabID string) (*crdt.PublicationReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.publications[userID+"/"+workerID+"/"+tabID]
	if r == nil {
		return nil, crdt.ErrNotFound
	}
	copy := *r
	copy.RequestHash = append([]byte(nil), r.RequestHash...)
	copy.Response = proto.CloneOf(r.Response)
	return &copy, nil
}
func (j *tabSyncJournal) CommitRejectedTabPublication(_ context.Context, userID string, r crdt.PublicationReceipt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.commitErr != nil {
		return j.commitErr
	}
	j.recordPublication(userID, r)
	return nil
}

func (*tabSyncJournal) LookupHostedTabState(_ context.Context, _, _, tabID string) (*leapmuxv1.WorkerTabState, error) {
	return &leapmuxv1.WorkerTabState{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID}, ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE}, nil
}

package crdt_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// orphanTabAuthChecker accepts every workspace write but rejects
// CanUseWorker for the configured "bad" worker. Used by the audit-
// log test to simulate "worker was revoked / deleted while a tab
// still pointed at it".
type orphanTabAuthChecker struct {
	badWorker string
}

func newIncrementingClock(start int64) func() time.Time {
	var mu sync.Mutex
	clock := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
}

func (orphanTabAuthChecker) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (a orphanTabAuthChecker) CanUseWorker(_ context.Context, workerID, _ string) (bool, error) {
	return workerID != a.badWorker, nil
}

// errorWorkerAuthChecker returns a lookup ERROR for CanUseWorker on the
// configured worker, simulating a transient DB failure during the orphan audit.
type errorWorkerAuthChecker struct {
	failWorker string
}

func (errorWorkerAuthChecker) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (a errorWorkerAuthChecker) CanUseWorker(_ context.Context, workerID, _ string) (bool, error) {
	if workerID == a.failWorker {
		return false, errors.New("worker lookup failed (simulated DB hiccup)")
	}
	return true, nil
}

// TestManager_AuditOrphanTabTombstone is the regression test for the
// hub-side audit log of "user tombstoned a tab pinned to a worker
// they can't reach". Concretely: seed a tab on worker "w-gone" via
// the internal path (which skips worker-ref validation), then
// submit a `TombstoneTabOp` as a user the auth checker reports
// `CanUseWorker("w-gone")=false` for. The manager must emit a
// `Warn`-level log record carrying the tab + workspace + worker +
// principal so operators can audit the fallback path.
func TestManager_AuditOrphanTabTombstone(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	j := newFakeJournal()
	now := newIncrementingClock(1_000)
	auth := orphanTabAuthChecker{badWorker: "w-gone"}
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, auth, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")

	// Seed the tab via SubmitInternal so the worker-ref validation
	// in validate.go (which gates client writes) is bypassed.
	// Represents the legacy state: a tab written when "w-gone" was
	// still accessible.
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-orphan", "root-1", "w-gone", "p1")},
	})
	require.NoError(t, err)

	// Now a user (principal "alice") tombstones the orphan tab.
	// CanUseWorker("w-gone", "alice") returns false, so the audit
	// log MUST fire.
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	tombstoneBatch := &leapmuxv1.OpBatch{
		BatchId: "ts-batch",
		Ops: []*leapmuxv1.CrdtOp{
			{OpId: "ts-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-orphan",
			}}},
		},
	}
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "alice", OriginClient: "cli-alice",
		Batches: []*leapmuxv1.OpBatch{tombstoneBatch},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted(), "tombstone must commit: %v", results[0])
	mgr.WaitForAudits()

	rec := findLogRecord(t, buf, "orphan tab tombstoned")
	require.NotNil(t, rec, "audit log entry missing — manager.auditOrphanTabTombstones didn't fire")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "user-1", rec["user_id"])
	assert.Equal(t, "ws-1", rec["workspace_id"])
	assert.Equal(t, "tab-orphan", rec["tab_id"])
	assert.Equal(t, "w-gone", rec["worker_id"])
	assert.Equal(t, "alice", rec["principal_id"])
	assert.Equal(t, "ts-batch", rec["batch_id"])
	assert.Equal(t, "ts-op", rec["op_id"])
}

// TestManager_AuditSkipsNormalTombstone pins the other side: when
// the principal CAN use the worker, the close is a routine UI/CLI
// action and we don't want to spam the audit log. Same setup as
// above, except CanUseWorker returns true for the seeded worker.
func TestManager_AuditSkipsNormalTombstone(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	j := newFakeJournal()
	now := newIncrementingClock(1_000)
	// allowAll → CanUseWorker always returns true.
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, allowAll{}, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-live", "root-1", "w-ok", "p1")},
	})
	require.NoError(t, err)

	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	tombstone := &leapmuxv1.OpBatch{
		BatchId: "ts2",
		Ops: []*leapmuxv1.CrdtOp{
			{OpId: "ts2-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-live",
			}}},
		},
	}
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "alice", OriginClient: "cli-alice",
		Batches: []*leapmuxv1.OpBatch{tombstone},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	mgr.WaitForAudits()

	rec := findLogRecord(t, buf, "orphan tab tombstoned")
	assert.Nil(t, rec, "routine close must NOT trigger the orphan audit log")
}

// TestManager_AuditSkipsInternalTombstone confirms the in.Internal
// guard. Hub-driven reconcile sweeps (e.g. worker reconnect tab
// sync) MUST NOT be treated as fallback events — the log is meant
// to flag user-initiated closes against an inaccessible worker.
func TestManager_AuditSkipsInternalTombstone(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	j := newFakeJournal()
	now := newIncrementingClock(1_000)
	auth := orphanTabAuthChecker{badWorker: "w-gone"}
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, auth, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-orphan", "root-1", "w-gone", "p1")},
	})
	require.NoError(t, err)

	// Tombstone via the Internal path — hub-driven, not user.
	_, err = mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		PrincipalID: "hub",
		Batches: []*leapmuxv1.OpBatch{{
			BatchId: "internal-ts",
			Ops: []*leapmuxv1.CrdtOp{
				{OpId: "internal-ts-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-orphan",
				}}},
			},
		}},
	})
	require.NoError(t, err)
	mgr.WaitForAudits()

	rec := findLogRecord(t, buf, "orphan tab tombstoned")
	assert.Nil(t, rec, "internal tombstone (hub reconcile sweep) must NOT trigger the audit log")
}

// TestManager_AuditInconclusiveOnWorkerLookupError pins the transient-failure
// arm of the orphan audit: when CanUseWorker ERRORS (a DB hiccup), the tombstone
// must NOT be mislabeled as a confirmed orphan cleanup, but the audit must leave
// an INCONCLUSIVE breadcrumb rather than going silently blank -- otherwise a
// genuinely-orphaned tab tombstoned during an outage vanishes from the record.
func TestManager_AuditInconclusiveOnWorkerLookupError(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	j := newFakeJournal()
	now := newIncrementingClock(1_000)
	auth := errorWorkerAuthChecker{failWorker: "w-flaky"}
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, auth, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-orphan", "root-1", "w-flaky", "p1")},
	})
	require.NoError(t, err)

	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	tombstone := &leapmuxv1.OpBatch{
		BatchId: "ts-flaky",
		Ops: []*leapmuxv1.CrdtOp{
			{OpId: "ts-flaky-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-orphan",
			}}},
		},
	}
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "alice", OriginClient: "cli-alice",
		Batches: []*leapmuxv1.OpBatch{tombstone},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	mgr.WaitForAudits()

	// A lookup error must NOT be reported as a confirmed orphan cleanup.
	assert.Nil(t, findLogRecord(t, buf, "orphan tab tombstoned"),
		"a worker-lookup error must not be reported as a confirmed orphan")
	// ...but it MUST leave an inconclusive breadcrumb, not a silent blind spot.
	rec := findLogRecord(t, buf, "orphan tab audit inconclusive")
	require.NotNil(t, rec, "inconclusive audit breadcrumb missing on worker-lookup error")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "w-flaky", rec["worker_id"])
	assert.Equal(t, "alice", rec["principal_id"])
	assert.Equal(t, "ts-flaky", rec["batch_id"])
}

// findLogRecord scans a JSONL slog buffer for the first record whose
// "msg" contains `needle` (substring). Returns the decoded record or
// nil when nothing matches. Returns nil rather than failing so the
// caller can assert presence/absence.
func findLogRecord(t *testing.T, buf *bytes.Buffer, needle string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("non-JSON log line: %q", line)
		}
		if msg, _ := rec["msg"].(string); strings.Contains(msg, needle) {
			return rec
		}
	}
	return nil
}

// The post-commit orphan-tab audit runs on a background goroutine that Stop()
// drains via auditWG.Wait(). If its CanUseWorker lookup hangs (a locked workers
// table, a dead pool connection) the goroutine never returns, so an unbounded
// context.Background() would wedge Stop() and hang a hub Shutdown. The lookup is
// bounded by crdt.OrphanAuditLookupTimeout; on expiry the audit takes its existing
// inconclusive path. This test shortens that timeout and proves Stop() returns
// even when the auth checker never answers.
type hangingWorkerAuthChecker struct{}

func (hangingWorkerAuthChecker) CanAccessWorkspace(context.Context, string, string) (bool, error) {
	return true, nil
}
func (hangingWorkerAuthChecker) CanUseWorker(ctx context.Context, _, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func TestManager_AuditLookupTimeoutDoesNotWedgeStop(t *testing.T) {
	old := crdt.OrphanAuditLookupTimeout
	crdt.OrphanAuditLookupTimeout = 50 * time.Millisecond
	defer func() { crdt.OrphanAuditLookupTimeout = old }()

	j := newFakeJournal()
	now := newIncrementingClock(2_000)
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, hangingWorkerAuthChecker{}, slog.Default(), now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()

	seedRootInternal(t, mgr, "ws-h", "root-h")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-h", "tab-h", "root-h", "w-h", "p-h")},
	})
	require.NoError(t, err)

	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	tombstoneBatch := &leapmuxv1.OpBatch{
		BatchId: "ts-h",
		Ops: []*leapmuxv1.CrdtOp{
			{OpId: "ts-h-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-h",
			}}},
		},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "alice", OriginClient: "cli",
		Batches: []*leapmuxv1.OpBatch{tombstoneBatch},
	})
	require.NoError(t, err, "the tombstone must commit and spawn the audit goroutine")

	// Stop() drains auditWG.Wait(); with an unbounded lookup the hanging auth
	// would block it forever. The timeout must let Stop() return promptly.
	done := make(chan struct{})
	go func() {
		cancel()
		mgr.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Manager.Stop() wedged: the audit goroutine's CanUseWorker lookup " +
			"was not bounded, so a hanging auth checker hangs the hub Shutdown")
	}
}

// recordingNudger captures every NudgeReconcile call.
type recordingNudger struct {
	mu    sync.Mutex
	calls []string
}

func (n *recordingNudger) NudgeReconcile(workerID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, workerID)
}

func (n *recordingNudger) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}

// TestManager_TombstoneNudgesTheHostingWorker pins the convergence nudge.
//
// A tab close is a CRDT op. The worker learns of it through the E2EE close RPC
// or, when that never arrives, through its orphan reconciler -- which runs
// hourly and was triggered only by the worker's own reconnect handshake. So a
// close whose RPC failed against a still-connected worker, or one a peer client
// or `leapmux remote tab close` performed CRDT-only, left the agent subprocess
// running for up to an hour. The nudge makes convergence round-trip bound.
//
// Asserting DEDUPLICATION as well as delivery: a batch closing several tabs on
// one machine must nudge it once, not once per tab.
func TestManager_TombstoneNudgesTheHostingWorker(t *testing.T) {
	nudger := &recordingNudger{}
	j := newFakeJournal()
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, orphanTabAuthChecker{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)), newIncrementingClock(1_000),
		crdt.WithReconcileNudger(nudger))
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{
			addTabBatch(t, "seed-a", "tab-a", "root-1", "w-host", "p1"),
			addTabBatch(t, "seed-b", "tab-b", "root-1", "w-host", "p2"),
		},
	})
	require.NoError(t, err)
	// Only tombstones should nudge; the adds above must not have.
	require.Empty(t, nudger.snapshot(), "adding a tab has no local state to release")

	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "alice", OriginClient: "cli-alice",
		Batches: []*leapmuxv1.OpBatch{{
			BatchId: "ts-batch",
			Ops: []*leapmuxv1.CrdtOp{
				{OpId: "ts-a", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-a",
				}}},
				{OpId: "ts-b", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-b",
				}}},
			},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted(), "tombstone must commit: %v", results[0])
	mgr.WaitForAudits()
	// Nudges are dispatched on their own goroutine (an uninterruptible worker
	// send must not run on the single per-user submit loop), and they are NOT
	// tracked by auditWG, so WaitForAudits alone would leave this racy.
	mgr.WaitForNudges()

	assert.Equal(t, []string{"w-host"}, nudger.snapshot(),
		"the hosting worker must be nudged exactly once for a batch that closed two of its tabs")
}

// TestManager_InternalTombstoneNudgesTheHostingWorker covers the path a
// workspace delete actually takes. applyLifecycleDelete tombstones the
// workspace's tabs via SubmitInternal, so the batch arrives with Internal=true.
//
// The nudge used to sit inside the `!in.Internal` block that gates the orphan
// AUDIT, which meant deleting a workspace never nudged the worker still hosting
// its tabs -- so those agent subprocesses kept running until the worker's hourly
// reconcile tick. The frontend's own per-worker CleanupWorkspace RPC is
// fire-and-forget (`.catch(() => {})`), so nothing else covered it.
func TestManager_InternalTombstoneNudgesTheHostingWorker(t *testing.T) {
	nudger := &recordingNudger{}
	j := newFakeJournal()
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, orphanTabAuthChecker{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)), newIncrementingClock(1_000),
		crdt.WithReconcileNudger(nudger))
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{
			addTabBatch(t, "seed-a", "tab-a", "root-1", "w-host", "p1"),
		},
	})
	require.NoError(t, err)
	require.Empty(t, nudger.snapshot(), "adding a tab has no local state to release")

	// The shape applyLifecycleDelete produces: an INTERNAL batch of tombstones.
	results, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{{
			BatchId: "internal-ts",
			Ops: []*leapmuxv1.CrdtOp{
				{OpId: "ts-a", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-a",
				}}},
			},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted(), "internal tombstone must commit: %v", results[0])
	mgr.WaitForNudges()

	assert.Equal(t, []string{"w-host"}, nudger.snapshot(),
		"an internal tombstone batch -- how a workspace delete closes its tabs -- must still nudge the hosting worker")
}

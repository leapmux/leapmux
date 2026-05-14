package crdt_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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

func (orphanTabAuthChecker) CanWriteWorkspace(_ context.Context, _, _, _ string) bool {
	return true
}
func (orphanTabAuthChecker) CanReadWorkspace(_ context.Context, _, _, _ string) bool { return true }
func (a orphanTabAuthChecker) CanUseWorker(_ context.Context, _, workerID, _ string) bool {
	return workerID != a.badWorker
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
	var (
		clockMu sync.Mutex
		clock   = int64(1_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	auth := orphanTabAuthChecker{badWorker: "w-gone"}
	mgr := crdt.NewManager("org", j, auth, logger, now)
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
		OrgID:   "org",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-orphan", "root-1", "w-gone", "p1")},
	})
	require.NoError(t, err)

	// Now a user (principal "alice") tombstones the orphan tab.
	// CanUseWorker("w-gone", "alice") returns false, so the audit
	// log MUST fire.
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	tombstoneBatch := &leapmuxv1.OpBatch{
		BatchId: "ts-batch",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "ts-op", Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-orphan",
			}}},
		},
	}
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "alice", OriginClient: "cli-alice",
		Batches: []*leapmuxv1.OpBatch{tombstoneBatch},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted(), "tombstone must commit: %v", results[0])
	mgr.WaitForAudits()

	rec := findLogRecord(t, buf, "orphan tab tombstoned")
	require.NotNil(t, rec, "audit log entry missing — manager.auditOrphanTabTombstones didn't fire")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "org", rec["org_id"])
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
	clock := int64(1_000)
	now := func() time.Time {
		clock++
		return time.UnixMilli(clock)
	}
	// allowAll → CanUseWorker always returns true.
	mgr := crdt.NewManager("org", j, allowAll{}, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		OrgID:   "org",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-live", "root-1", "w-ok", "p1")},
	})
	require.NoError(t, err)

	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	tombstone := &leapmuxv1.OpBatch{
		BatchId: "ts2",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "ts2-op", Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-live",
			}}},
		},
	}
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "alice", OriginClient: "cli-alice",
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
	clock := int64(1_000)
	now := func() time.Time {
		clock++
		return time.UnixMilli(clock)
	}
	auth := orphanTabAuthChecker{badWorker: "w-gone"}
	mgr := crdt.NewManager("org", j, auth, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	seedRootInternal(t, mgr, "ws-1", "root-1")
	_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		OrgID:   "org",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-tab", "tab-orphan", "root-1", "w-gone", "p1")},
	})
	require.NoError(t, err)

	// Tombstone via the Internal path — hub-driven, not user.
	_, err = mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		OrgID: "org", PrincipalID: "hub",
		Batches: []*leapmuxv1.OpBatch{{
			BatchId: "internal-ts",
			Ops: []*leapmuxv1.OrgOp{
				{OpId: "internal-ts-op", Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
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

package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// containsTombstoneTab reports whether batch carries at least one
// TombstoneTabOp. The audit hook below is the only consumer; the
// guard short-circuits the goroutine spawn for the (overwhelmingly
// common) non-tombstone batch.
func containsTombstoneTab(batch *leapmuxv1.OpBatch) bool {
	for _, op := range batch.GetOps() {
		if _, ok := op.GetBody().(*leapmuxv1.OrgOp_TombstoneTab); ok {
			return true
		}
	}
	return false
}

// auditOrphanTabTombstones logs each TombstoneTabOp whose target tab
// is pinned to a worker the submitting principal cannot use, with
// enough context for operators to reconstruct the event.
//
// This is the durable signal for "user removed a tab whose worker
// was deleted / revoked from under them" — the CLI's
// `agent close` / `terminal close` fallback path. Without this log
// the only record of the cleanup is the user's stdout envelope,
// which doesn't survive past the shell session. The hub-side
// reconnect tab-sync covers cases where a still-alive worker
// reappears with a different tab set; it can't cover deleted
// workers, so this log is the only breadcrumb for that scenario.
//
// Runs on a background goroutine spawned from processBatch after
// commit. pre is the pre-commit state snapshot — immutable once the
// commit has landed, so reads here don't need m.mu. The auditWG
// counter ensures Stop() drains in-flight audits.
func (m *Manager) auditOrphanTabTombstones(in SubmitInput, batch *leapmuxv1.OpBatch, res ValidationResult, pre *leapmuxv1.OrgCrdtState) {
	if m.auth == nil {
		return
	}
	// Dedupe `(workerID, principalID)` pairs across the batch so a
	// workspace-delete or grid-collapse that tombstones N tabs
	// pinned to the same worker doesn't issue N identical
	// CanUseWorker DB lookups. Realistic batches concentrate on a
	// handful of distinct (worker, principal) pairs even when they
	// touch dozens of tabs.
	canUseCache := map[[2]string]bool{}
	// Tracks (worker, principal) pairs whose usability lookup failed, so the
	// inconclusive-audit diagnostic below is emitted at most once per pair even
	// when several tombstones in the batch pin the same worker.
	inconclusiveLogged := map[[2]string]struct{}{}
	auditCtx := context.Background()
	canUse := func(workerID, batchID string) bool {
		key := [2]string{workerID, in.PrincipalID}
		if v, ok := canUseCache[key]; ok {
			return v
		}
		// auditCtx (context.Background): this runs on a background
		// goroutine spawned AFTER commit, decoupled from the request
		// ctx (which may already be cancelled by the time we get
		// here). auditWG, drained by Stop(), bounds the lifetime.
		v, err := m.auth.CanUseWorker(auditCtx, m.orgID, workerID, in.PrincipalID)
		if err != nil {
			// A transient worker-lookup failure must not be read as "worker
			// gone": that would mislabel a legitimate tombstone as orphan
			// cleanup. Treat it as usable (no orphan breadcrumb) and don't
			// cache the verdict, so a later op re-checks -- but record ONCE per
			// (worker, principal) that the orphan audit could NOT conclude, so a
			// genuinely-orphaned tab tombstoned during a DB hiccup is a visible
			// inconclusive breadcrumb rather than a silent blind spot.
			if _, done := inconclusiveLogged[key]; !done {
				inconclusiveLogged[key] = struct{}{}
				m.logger.Warn("crdt: orphan tab audit inconclusive (worker lookup failed)",
					"worker_id", workerID,
					"principal_id", in.PrincipalID,
					"batch_id", batchID,
					"error", err,
				)
			}
			return true
		}
		canUseCache[key] = v
		return v
	}
	for _, op := range batch.GetOps() {
		body, ok := op.GetBody().(*leapmuxv1.OrgOp_TombstoneTab)
		if !ok {
			continue
		}
		tabID := body.TombstoneTab.GetTabId()
		// Read from PRE-commit state. applyTombstoneTab replaces
		// the TabRecord with a stripped {tab_type, tab_id,
		// tombstone_at} shell post-commit, so any reads from
		// `working` / post-state would lose worker_id.
		rec := pre.GetTabs()[tabID]
		if rec == nil {
			continue
		}
		workerID := rec.GetWorkerId().GetValue()
		if workerID == "" {
			// No worker_id was ever pinned (file tab pre-resolve,
			// half-committed write). No orphan to flag.
			continue
		}
		if canUse(workerID, batch.GetBatchId()) {
			// Routine close: principal can use the worker, so
			// this is a normal `agent close` / `terminal close`
			// or UI-driven tombstone. Skipping these keeps the
			// log focused on the fallback path.
			continue
		}
		ref := EntityRef{Kind: EntityKindTab, TabType: body.TombstoneTab.GetTabType(), TabID: tabID}
		workspaceID := res.AffectedEntities[ref].Pre
		// org_id is already stamped on m.logger via NewManager's
		// logger.With("org_id", orgID); don't double-stamp.
		m.logger.Warn("crdt: orphan tab tombstoned (worker inaccessible to principal)",
			"workspace_id", workspaceID,
			"tab_id", tabID,
			"tab_type", body.TombstoneTab.GetTabType().String(),
			"worker_id", workerID,
			"principal_id", in.PrincipalID,
			"batch_id", batch.GetBatchId(),
			"op_id", op.GetOpId(),
		)
	}
}

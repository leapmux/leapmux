package crdt

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// OrphanAuditLookupTimeout bounds the worker-lookup the post-commit orphan-tab
// audit runs. The audit goroutine is decoupled from the request ctx (which may
// already be cancelled by the time commit finishes), but Stop() drains it via
// auditWG.Wait(), so an unbounded lookup would let a stalled DB connection hang
// a hub Shutdown. On expiry the lookup fails and the audit logs its existing
// "inconclusive" breadcrumb -- the same outcome any transient lookup fault
// produces. Exported (and a var) so tests can shorten it.
var OrphanAuditLookupTimeout = 5 * time.Second

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

// tombstonedTabWorkerIDs resolves, for each TombstoneTab op in batch, the worker
// id the tab was pinned to in the pre-commit state. It runs SYNCHRONOUSLY at the
// spawn site (processBatch, before the audit goroutine is launched) so the
// background goroutine captures only this small map, not the whole (potentially
// multi-MB) OrgCrdtState -- which the goroutine would otherwise pin for the
// CanUseWorker DB lookup's duration, past the m.state = working swap that makes
// preState otherwise GC-eligible. Tabs already shelled out of pre or pinned to no
// worker are omitted, the same cases auditOrphanTabTombstones skips on a missing
// key.
func tombstonedTabWorkerIDs(batch *leapmuxv1.OpBatch, pre *leapmuxv1.OrgCrdtState) map[string]string {
	out := make(map[string]string)
	for _, op := range batch.GetOps() {
		body, ok := op.GetBody().(*leapmuxv1.OrgOp_TombstoneTab)
		if !ok {
			continue
		}
		tabID := body.TombstoneTab.GetTabId()
		if tabID == "" {
			continue
		}
		rec := pre.GetTabs()[tabID]
		if rec == nil {
			continue
		}
		if wid := rec.GetWorkerId().GetValue(); wid != "" {
			out[tabID] = wid
		}
	}
	return out
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
// Runs on a background goroutine spawned from processBatch after commit. The
// tombstoned tabs' worker ids are resolved from the pre-commit state
// SYNCHRONOUSLY, before the goroutine is spawned (tombstonedTabWorkerIDs), so the
// goroutine captures only that small map instead of the whole pre-commit
// OrgCrdtState -- which would otherwise pin the old generation (potentially
// multi-MB) for the CanUseWorker DB lookup's duration, past the m.state = working
// swap that makes it otherwise GC-eligible. The auditWG counter ensures Stop()
// drains in-flight audits.
func (m *Manager) auditOrphanTabTombstones(in SubmitInput, batch *leapmuxv1.OpBatch, res ValidationResult, workerIDs map[string]string) {
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
	// Bound the lookup: this goroutine is spawned after commit, decoupled from
	// the request ctx (which may already be cancelled), but Stop() drains it via
	// auditWG.Wait() -- so an unbounded context.Background() would let a stalled
	// DB lookup (locked workers table, dead pool connection) wedge the goroutine
	// forever and hang a hub Shutdown. The timeout bounds that: on expiry the
	// existing inconclusive path logs once per (worker, principal) and treats the
	// tab as usable, which is the same outcome any other transient lookup fault
	// already produces below.
	auditCtx, cancelAudit := context.WithTimeout(context.Background(), OrphanAuditLookupTimeout)
	defer cancelAudit()
	canUse := func(workerID, batchID string) bool {
		key := [2]string{workerID, in.PrincipalID}
		if v, ok := canUseCache[key]; ok {
			return v
		}
		// auditCtx (background, timeout-bounded): this runs on a background
		// goroutine spawned AFTER commit, decoupled from the request
		// ctx (which may already be cancelled by the time we get
		// here). The timeout (OrphanAuditLookupTimeout) bounds the
		// lifetime a stalled lookup can keep the goroutine -- and thus
		// Stop()'s auditWG.Wait() -- alive.
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
		// workerIDs was resolved from the PRE-commit state synchronously before
		// the goroutine spawned (tombstonedTabWorkerIDs): applyTombstoneTab
		// replaces the TabRecord with a stripped {tab_type, tab_id, tombstone_at}
		// shell post-commit, so reads from `working` / post-state would lose
		// worker_id. Absence covers both "tab already a shell" and "no worker_id
		// was ever pinned" (a file tab pre-resolve, a half-committed write) --
		// neither is an orphan to flag.
		workerID, ok := workerIDs[tabID]
		if !ok {
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

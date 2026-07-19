package crdt

import (
	"context"
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"google.golang.org/protobuf/proto"
)

// LifecycleOpType is the kind of workspace lifecycle event recorded in
// lifecycle_outbox. Kept as a stringly-encoded typed alias so the
// JSON payload and the DB column stay readable while the Go switch
// statements get the benefit of constant-safety (typos at the call
// sites become compile errors).
type LifecycleOpType string

const (
	LifecycleOpCreate LifecycleOpType = "create"
	LifecycleOpRename LifecycleOpType = "rename"
	LifecycleOpDelete LifecycleOpType = "delete"
)

// LifecyclePayload is the body of a single lifecycle_outbox row. The
// CRDT manager parses this into a batch of internal-origin ops plus
// an associated event broadcast. Encoded as JSON because it's
// hub-internal — no cross-language consumer.
type LifecyclePayload struct {
	OpType      LifecycleOpType `json:"op_type"`
	WorkspaceID string          `json:"workspace_id"`
	Title       string          `json:"title,omitempty"`
	NewTitle    string          `json:"new_title,omitempty"`
	RootNodeID  string          `json:"root_node_id,omitempty"`
	OpBytes     [][]byte        `json:"op_bytes,omitempty"` // each is proto.Marshal(OrgOp)
	WorkerIDs   []string        `json:"worker_ids,omitempty"`
}

// EncodeLifecyclePayload returns the bytes for a lifecycle_outbox.payload.
func EncodeLifecyclePayload(p LifecyclePayload, ops []*leapmuxv1.OrgOp) ([]byte, error) {
	for _, op := range ops {
		bytes, err := proto.Marshal(op)
		if err != nil {
			return nil, fmt.Errorf("marshal op: %w", err)
		}
		p.OpBytes = append(p.OpBytes, bytes)
	}
	return json.Marshal(p)
}

// DecodeLifecyclePayload parses a lifecycle_outbox.payload row.
func DecodeLifecyclePayload(data []byte) (LifecyclePayload, []*leapmuxv1.OrgOp, error) {
	var p LifecyclePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return p, nil, fmt.Errorf("decode lifecycle payload: %w", err)
	}
	ops := make([]*leapmuxv1.OrgOp, 0, len(p.OpBytes))
	for _, b := range p.OpBytes {
		op := &leapmuxv1.OrgOp{}
		if err := proto.Unmarshal(b, op); err != nil {
			return p, nil, fmt.Errorf("unmarshal op: %w", err)
		}
		ops = append(ops, op)
	}
	return p, ops, nil
}

// SubmitLifecycle drains the lifecycle_outbox for this manager's org
// and applies each row inside its own transaction.
//
// lifecycleMu makes the drain single-consumer by construction. Each
// lifecycle RPC calls this post-commit on its own request goroutine, so
// two mutations in the same org would otherwise drain concurrently: the
// second drain's ListPending could return a still-pending create row
// beside its own delete row, and re-applying that create against the
// first drain's in-flight processing can re-add the workspace to
// m.state after the delete removed it, re-expand subscriber filters
// after the delete's contraction (a phantom workspace in live
// subscriptions until reconnect), and broadcast a Created event for a
// workspace that no longer exists. Serializing the whole
// list-process-consume pass turns the "rows are drained sequentially"
// assumption the row-apply logic rests on into a property of this
// method rather than a convention each caller must uphold.
//
// A row that fails to apply is skipped WITHOUT being consumed, so it retries on
// the next drain -- rather than aborting the whole batch. Aborting let a
// persistently-failing head-of-line row (e.g. a create whose read-ACL LOOKUP
// keeps faulting) strand every INDEPENDENT workspace's rows behind it
// indefinitely, since the only drain trigger is the next lifecycle RPC (no
// periodic re-drain). But per-workspace ORDER must still hold: applying a delete
// X after its create X failed would re-introduce the "phantom / no-prior-create"
// hazard above, so once a workspace's row fails this pass, every LATER row for the
// same workspace is deferred with it (blocked). The first error is returned so the
// caller logs the fault; a transient fault clears on the next drain and the
// deferred rows apply then, in order.
func (m *Manager) SubmitLifecycle(ctx context.Context, reader LifecycleOutboxReader) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	rows, err := reader.ListPendingLifecycleOutbox(ctx, m.orgID)
	if err != nil {
		return fmt.Errorf("list outbox: %w", err)
	}
	blocked := map[string]bool{}
	var firstErr error
	for _, row := range rows {
		payload, ops, err := DecodeLifecyclePayload(row.Payload)
		if err != nil {
			// Decode is a pure function of row.Payload, so this failure is
			// permanent: retrying it can never succeed, and leaving the row
			// pending would re-fault and re-log on EVERY future drain for the
			// org's lifetime while occupying a ListPendingLifecycleOutbox slot
			// (enough corrupt rows would clog the page and starve healthy rows
			// behind it). Consume it after one Error log instead. The row cannot
			// be scoped to a workspace either way, so consuming changes nothing
			// about per-workspace ordering -- a later valid row for the corrupt
			// row's (unknowable) workspace was never going to be deferred behind
			// it.
			//
			// The Error log is deduped by row ID: if the consume itself fails
			// (a transient DB write fault), the row stays pending and the next
			// drain re-decodes it. Re-logging the SAME corrupt row at Error on
			// every drain would amplify one bad row into permanent log noise for
			// the org's life, so log Error only the first time and Warn on the
			// repeats the caller can do nothing about. Entries are cleared once
			// the row is successfully consumed, so the set stays bounded by the
			// number of currently-stuck corrupt rows.
			m.logRowFaultOnce(&m.undecodableLogged, row.ID,
				"discarding undecodable lifecycle row",
				"undecodable lifecycle row still pending after a failed consume", err)
			if cerr := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); cerr != nil {
				// Consumption failing is a transient store fault: leave the row
				// for the next drain to discard.
				m.logger.Error("consume undecodable lifecycle row", "id", row.ID, "err", cerr)
			} else {
				delete(m.undecodableLogged, row.ID)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if blocked[payload.WorkspaceID] {
			// An earlier row for this workspace failed this pass; defer this one to
			// a later drain so per-workspace order is preserved.
			continue
		}
		if err := m.applyLifecycleRow(ctx, row, payload, ops, reader); err != nil {
			// An apply failure is most often transient (a create whose read-ACL
			// lookup faulted), so the row is NOT consumed -- it retries on the next
			// drain. But re-logging the SAME failing row at Error on every drain
			// would amplify one stuck row into permanent log noise for the org's
			// life (the only drain trigger is the next lifecycle RPC), exactly the
			// amplification the decode-failure path's undecodableLogged exists to
			// prevent. Dedupe the same way: Error on the first sighting, Warn on
			// the repeats the operator can do nothing about until the fault clears.
			// The set is cleared on success, so it stays bounded by the number of
			// currently-stuck rows.
			m.logRowFaultOnce(&m.applyFailedLogged, row.ID,
				"process lifecycle row",
				"lifecycle row still failing after a prior error", err)
			blocked[payload.WorkspaceID] = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// A successful apply clears any prior failure record so a row that
		// transiently faulted (and was deduped above) does not keep the entry
		// pinned once the fault clears.
		delete(m.applyFailedLogged, row.ID)
	}
	return firstErr
}

// logRowFaultOnce logs a lifecycle-outbox row fault at Error on the FIRST sighting
// of id and at Warn on every repeat, recording id in *seen so a fault that
// persists across drains (the only drain trigger is the next lifecycle RPC) is not
// re-logged at Error every time -- amplifying one stuck row into permanent log
// noise for the org's life. seen is lazily allocated (nil until the first fault);
// each caller clears the entry once its row stops faulting, so the set stays
// bounded by the rows currently stuck. Shared by the undecodable-row and
// apply-failure paths, whose surrounding consume-vs-retry logic differs but whose
// once-then-warn dedup policy is identical -- so the policy lives here once rather
// than in two hand-synced copies.
func (m *Manager) logRowFaultOnce(seen *map[int64]bool, id int64, errMsg, warnMsg string, err error) {
	if *seen == nil {
		*seen = make(map[int64]bool)
	}
	if !(*seen)[id] {
		(*seen)[id] = true
		m.logger.Error(errMsg, "id", id, "err", err)
	} else {
		m.logger.Warn(warnMsg, "id", id, "err", err)
	}
}

func (m *Manager) applyLifecycleRow(ctx context.Context, row LifecycleOutboxRow, payload LifecyclePayload, ops []*leapmuxv1.OrgOp, reader LifecycleOutboxReader) error {
	switch payload.OpType {
	case LifecycleOpCreate:
		return m.applyLifecycleCreate(ctx, row, payload, ops, reader)
	case LifecycleOpRename:
		return m.applyLifecycleRename(ctx, row, payload, reader)
	case LifecycleOpDelete:
		return m.applyLifecycleDelete(ctx, row, payload, ops, reader)
	default:
		return fmt.Errorf("unknown op_type %q", payload.OpType)
	}
}

func (m *Manager) applyLifecycleCreate(ctx context.Context, row LifecycleOutboxRow, p LifecyclePayload, ops []*leapmuxv1.OrgOp, reader LifecycleOutboxReader) error {
	wsID := p.WorkspaceID
	// Add the (workspace_id, root_node_id="") map entry first so the
	// SetWorkspaceRootNodeOp below has somewhere to land.
	m.MutateInternal(func(state *leapmuxv1.OrgCrdtState) {
		if _, exists := state.Workspaces[wsID]; !exists {
			state.Workspaces[wsID] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: wsID}
		}
	})

	rollback := func() {
		m.MutateInternal(func(state *leapmuxv1.OrgCrdtState) { delete(state.Workspaces, wsID) })
		// Undo the optimistic filter expansion so a future
		// CanAccessWorkspace flip can't smuggle stale visibility through.
		m.contractSubscribersForWorkspace(wsID)
	}

	// Expand each existing subscriber's Filter to include the new
	// workspace BEFORE SubmitInternal broadcasts the seed batch.
	// Otherwise the seed `SetNodeRegister` (root LEAF) and
	// `SetWorkspaceRootNode` ops are dropped at the broadcast filter
	// (subscribers don't yet have `wsID` in their allow-set), the
	// frontend never learns the workspace's root_node_id, and the
	// agent tab the user just opened never lands in the CRDT
	// projection. A read-ACL LOOKUP failure here must not silently drop
	// the seed: roll back the optimistic add and return WITHOUT consuming
	// the outbox row so the create retries whenever the outbox is next drained
	// -- the next lifecycle mutation in this org, not a periodic tick (the fixed
	// BatchId + idempotent MutateInternal make retry safe).
	if err := m.ExpandSubscribersForWorkspace(ctx, wsID); err != nil {
		rollback()
		return fmt.Errorf("expand subscribers for workspace %s: %w", wsID, err)
	}

	batch := &leapmuxv1.OpBatch{
		BatchId: "lifecycle-create-" + wsID,
		Ops:     ops,
	}
	results, err := m.SubmitInternal(ctx, SubmitInput{
		OrgID:        m.orgID,
		Epoch:        m.currentEpoch(),
		Batches:      []*leapmuxv1.OpBatch{batch},
		PrincipalID:  HubReservedPrincipal,
		OriginClient: m.hubClientID,
	})
	if err != nil {
		rollback()
		return err
	}
	// A ROOT_IMMUTABLE rejection on re-drain means the seed already landed on a
	// prior drain whose MarkLifecycleOutboxConsumed then failed (a transient DB
	// write fault) and whose dedup row has since expired past DedupTTL. The
	// workspace exists, the root is set, the filter expansion already happened,
	// and the user may have added tabs in the meantime -- so rolling back would
	// corrupt CRDT state (delete(state.Workspaces, wsID) while the DB row, root
	// node, and tabs stay behind) and strand the row in a permanent rejection
	// loop. Treat ROOT_IMMUTABLE as the idempotent-success signal it is: the
	// create already happened, so do NOT roll back, consume the row, and
	// re-broadcast (subscribers either see it as new or dedupe via state).
	for _, r := range results {
		if rj := r.GetRejected(); rj != nil {
			if rj.GetReason() == leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_IMMUTABLE {
				m.logger.Warn("lifecycle create re-drained after a prior apply; treating as idempotent",
					"workspace_id", wsID, "row_id", row.ID)
				continue
			}
			rollback()
			return fmt.Errorf("seed batch rejected: %v", rj.GetReason())
		}
	}
	// Broadcast the Created event BEFORE consuming the outbox row, mirroring
	// applyLifecycleDelete's order: a consume-then-broadcast window that a
	// process crash lands in would leave the row consumed -- so no later drain
	// retries it -- while no subscriber ever saw the Created event, and the
	// workspace would be invisible in live subscriptions until a reconnect. The
	// retry the order exists to make reachable is now SAFE because the
	// ROOT_IMMUTABLE arm above treats a re-seed as idempotent success rather than
	// the rollback that would corrupt state.
	m.broadcastWorkspaceCreatedEvent(wsID, p.Title, p.RootNodeID)
	if err := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); err != nil {
		return err
	}
	return nil
}

func (m *Manager) applyLifecycleRename(ctx context.Context, row LifecycleOutboxRow, p LifecyclePayload, reader LifecycleOutboxReader) error {
	// Broadcast BEFORE consuming the outbox row, mirroring applyLifecycleDelete's
	// order (and its rationale): a consume-then-broadcast window that a process
	// crash or a hard cancel lands in would leave the row consumed -- so no later
	// drain retries it -- while no subscriber ever saw the Rename event. The DB
	// title is already updated, but every live subscriber keeps the stale title
	// until a reconnect whose OrgMaterialized carries no title. Rename is
	// idempotent on retry (re-broadcasting the same title is a no-op for
	// subscribers, and no seed batch re-validates), so broadcasting first costs
	// nothing on the retry path the order exists to make reachable.
	m.BroadcastWorkspaceRenamed(p.WorkspaceID, p.NewTitle)
	if err := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); err != nil {
		return err
	}
	return nil
}

func (m *Manager) applyLifecycleDelete(ctx context.Context, row LifecycleOutboxRow, p LifecyclePayload, ops []*leapmuxv1.OrgOp, reader LifecycleOutboxReader) error {
	// The workspace_service writes the outbox row with no ops — the
	// manager owns the in-memory enumeration so we capture EVERY live
	// entity even ones projection repair has hidden. Without this the
	// next CreateWorkspace's seed batch fails completeness validation
	// on left-over orphan-root NodeRecords whose workspace just got
	// deleted (Rule 15 — every live parentless node must be a
	// registered root).
	wsID := p.WorkspaceID
	// Walk the live state under m.mu.RLock — the enumeration is a
	// single synchronous pass, so the lock is held only for the walk
	// itself. A bare read of m.state from this (request-handler)
	// goroutine would race with the manager goroutine's writes under
	// m.mu.Lock(); cloning via m.State() would allocate a multi-MB copy
	// of every workspace just to enumerate one.
	var enumOps []*leapmuxv1.OrgOp
	m.WithStateRLock(func(state *leapmuxv1.OrgCrdtState) {
		enumOps = enumerateWorkspaceDeleteOps(state, wsID)
	})
	combined := make([]*leapmuxv1.OrgOp, 0, len(ops)+len(enumOps))
	combined = append(combined, ops...)
	combined = append(combined, enumOps...)
	if len(combined) > 0 {
		batch := &leapmuxv1.OpBatch{BatchId: "lifecycle-delete-" + p.WorkspaceID, Ops: combined}
		results, err := m.SubmitInternal(ctx, SubmitInput{
			OrgID:        m.orgID,
			Epoch:        m.currentEpoch(),
			Batches:      []*leapmuxv1.OpBatch{batch},
			PrincipalID:  HubReservedPrincipal,
			OriginClient: m.hubClientID,
		})
		if err != nil {
			return err
		}
		for _, r := range results {
			if rj := r.GetRejected(); rj != nil {
				return fmt.Errorf("delete batch rejected: %v", rj.GetReason())
			}
		}
	}
	m.MutateInternal(func(state *leapmuxv1.OrgCrdtState) { delete(state.Workspaces, p.WorkspaceID) })
	// Broadcast the deletion and contract subscriber filters BEFORE marking the
	// outbox row consumed. The in-memory state already reflects the delete (the
	// tombstone batch above ran and the Workspaces map entry is gone), so the
	// broadcast describes committed state either way -- but ordering it before
	// the consume closes a window the old order left open: if MarkLifecycleOutboxConsumed
	// fails (a transient DB write fault) the row stays pending and the only re-drain
	// trigger is the next lifecycle RPC in this org, which for a delete that removed
	// the org's last workspace may not come for a long time. Subscribers would keep
	// the dead workspace in their Filter and keep routing tabs to it until reconnect.
	// Broadcasting first means a consume failure delays only the outbox bookkeeping,
	// not the subscriber-visible event. A successful re-drain re-broadcasts, which
	// is idempotent on the subscriber side (deleting an already-absent workspace and
	// re-running the lifecycle-changed refresh).
	m.BroadcastWorkspaceDeleted(p.WorkspaceID, p.WorkerIDs)
	// Drop the deleted workspace from every subscriber's Filter so a
	// long-lived subscription doesn't accumulate dead workspace_ids
	// across the manager's lifetime. Mirrors the rollback path's
	// contraction in applyLifecycleCreate.
	m.contractSubscribersForWorkspace(p.WorkspaceID)
	if err := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); err != nil {
		return err
	}
	return nil
}

// enumerateWorkspaceDeleteOps walks the in-memory state and produces
// the tombstone batch required to clean up a workspace: every live tab
// whose resolved workspace is `wsID`, every live floating window whose
// `workspace_id` register is `wsID` (descendants first, then the
// window's root, then the FloatingWindowRecord), and every live node
// reachable from `wsID`'s root (leaves first, ancestors after, root
// last). Order matters for parent-before-child invariants surfaced by
// projection repair; the validator's apply step is order-agnostic but
// keeping a deterministic order keeps the parity tests reproducible.
func enumerateWorkspaceDeleteOps(state *leapmuxv1.OrgCrdtState, wsID string) []*leapmuxv1.OrgOp {
	if state == nil || wsID == "" {
		return nil
	}
	wsRec, ok := state.GetWorkspaces()[wsID]
	if !ok {
		return nil
	}
	// Build the live parent→children adjacency once and reuse it for
	// every collectSubtreeTombstones call below. Without this, each
	// floating window's subtree walk re-scans every node in the org —
	// O(N·W) for a workspace with W floating windows.
	children := BuildLiveChildrenIndex(state)
	var out []*leapmuxv1.OrgOp

	// Membership set: every live node reachable from the workspace's
	// main-layout root plus every floating-window subtree owned by this
	// workspace. Built once with O(membership) DFS so the tab filter
	// below avoids the per-tab parent-chain walk
	// (resolveTileWorkspace) — that walk is O(depth) per tab and was
	// the dominant cost for workspaces with many tabs.
	nodesInWorkspace := map[string]bool{}
	if rootID := wsRec.GetRootNodeId(); rootID != "" {
		collectSubtreeIDs(children, rootID, nodesInWorkspace)
	}
	for _, fw := range state.GetFloatingWindows() {
		if !HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if fw.GetWorkspaceId().GetValue() != wsID {
			continue
		}
		if rootID := fw.GetRootNodeId(); rootID != "" {
			collectSubtreeIDs(children, rootID, nodesInWorkspace)
		}
	}

	// 1. Tabs whose tile is inside the workspace's reachable membership.
	for tabID, tab := range state.GetTabs() {
		if !HLCIsZero(tab.GetTombstoneAt()) {
			continue
		}
		if !nodesInWorkspace[tab.GetTileId().GetValue()] {
			continue
		}
		out = append(out, &leapmuxv1.OrgOp{
			OpId: id.Generate(),
			Body: &leapmuxv1.OrgOp_TombstoneTab{
				TombstoneTab: &leapmuxv1.TombstoneTabOp{TabType: tab.GetTabType(), TabId: tabID},
			},
		})
	}

	// 2. Floating windows belonging to wsID — tombstone every live
	//    subtree node (leaves first, then ancestors, then the root),
	//    then the window. The validator's root-protection exception
	//    requires the window-tombstone op to live in the same batch
	//    as the root-tombstone op.
	for winID, fw := range state.GetFloatingWindows() {
		if !HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if fw.GetWorkspaceId().GetValue() != wsID {
			continue
		}
		rootID := fw.GetRootNodeId()
		if rootID != "" {
			subtreeOps := collectSubtreeTombstones(state, rootID, children)
			out = append(out, subtreeOps...)
		}
		out = append(out, &leapmuxv1.OrgOp{
			OpId: id.Generate(),
			Body: &leapmuxv1.OrgOp_TombstoneFloatingWindow{
				TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{WindowId: winID},
			},
		})
	}

	// 3. The workspace's main-layout subtree (leaves first, then
	//    ancestors, finally the root).
	if rootID := wsRec.GetRootNodeId(); rootID != "" {
		out = append(out, collectSubtreeTombstones(state, rootID, children)...)
	}

	return out
}

// collectSubtreeIDs walks `children` from rootID and adds every
// reachable node id to `out`. Cycles are guarded by the membership
// check on `out` itself, so callers can pass a shared map when
// computing a union of multiple subtrees.
func collectSubtreeIDs(children map[string][]string, rootID string, out map[string]bool) {
	if rootID == "" {
		return
	}
	queue := []string{rootID}
	out[rootID] = true
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, child := range children[next] {
			if out[child] {
				continue
			}
			out[child] = true
			queue = append(queue, child)
		}
	}
}

// collectSubtreeTombstones returns TombstoneNodeOp ops covering every
// live node in the subtree rooted at `rootID` (children before parents
// so the order matches the plan's "descendants leaves-first, ancestors
// after" rule). The root itself is included at the end of the slice.
// `children` is the prebuilt live parent→children adjacency (passed
// in so the caller can amortize its construction across multiple
// subtree walks).
func collectSubtreeTombstones(state *leapmuxv1.OrgCrdtState, rootID string, children map[string][]string) []*leapmuxv1.OrgOp {
	if rootID == "" {
		return nil
	}
	root := state.GetNodes()[rootID]
	if root == nil || !HLCIsZero(root.GetTombstoneAt()) {
		return nil
	}
	order := subtreePostOrder(children, rootID)
	ops := make([]*leapmuxv1.OrgOp, 0, len(order))
	for _, nodeID := range order {
		ops = append(ops, &leapmuxv1.OrgOp{
			OpId: id.Generate(),
			Body: &leapmuxv1.OrgOp_TombstoneNode{
				TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: nodeID},
			},
		})
	}
	return ops
}

// subtreePostOrder returns every reachable descendant of rootID
// (rootID included last), descendants-first. Iterative — the parent is
// pushed once but only emitted after every descendant has been
// visited, matching the recursive post-order shape without growing the
// goroutine stack. Cycles are guarded by a visited set.
func subtreePostOrder(children map[string][]string, rootID string) []string {
	if rootID == "" {
		return nil
	}
	type frame struct {
		id      string
		nextIdx int
	}
	stack := []frame{{id: rootID}}
	visited := map[string]bool{rootID: true}
	out := []string{}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		kids := children[top.id]
		if top.nextIdx < len(kids) {
			child := kids[top.nextIdx]
			top.nextIdx++
			if visited[child] {
				continue
			}
			visited[child] = true
			stack = append(stack, frame{id: child})
			continue
		}
		out = append(out, top.id)
		stack = stack[:len(stack)-1]
	}
	return out
}

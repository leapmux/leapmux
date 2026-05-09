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
func (m *Manager) SubmitLifecycle(ctx context.Context, reader LifecycleOutboxReader) error {
	rows, err := reader.ListPendingLifecycleOutbox(ctx, m.orgID)
	if err != nil {
		return fmt.Errorf("list outbox: %w", err)
	}
	for _, row := range rows {
		if err := m.processLifecycleRow(ctx, row, reader); err != nil {
			m.logger.Error("process lifecycle row", "id", row.ID, "err", err)
			return err
		}
	}
	return nil
}

func (m *Manager) processLifecycleRow(ctx context.Context, row LifecycleOutboxRow, reader LifecycleOutboxReader) error {
	payload, ops, err := DecodeLifecyclePayload(row.Payload)
	if err != nil {
		return err
	}
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

	// Expand each existing subscriber's Filter to include the new
	// workspace BEFORE SubmitInternal broadcasts the seed batch.
	// Otherwise the seed `SetNodeRegister` (root LEAF) and
	// `SetWorkspaceRootNode` ops are dropped at the broadcast filter
	// (subscribers don't yet have `wsID` in their allow-set), the
	// frontend never learns the workspace's root_node_id, and the
	// agent tab the user just opened never lands in the CRDT
	// projection.
	m.ExpandSubscribersForWorkspace(ctx, wsID)

	batch := &leapmuxv1.OpBatch{
		BatchId: "lifecycle-create-" + wsID,
		Ops:     ops,
	}
	rollback := func() {
		m.MutateInternal(func(state *leapmuxv1.OrgCrdtState) { delete(state.Workspaces, wsID) })
		// Undo the optimistic filter expansion so a future
		// CanReadWorkspace flip can't smuggle stale visibility through.
		m.contractSubscribersForWorkspace(wsID)
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
	for _, r := range results {
		if rj := r.GetRejected(); rj != nil {
			rollback()
			return fmt.Errorf("seed batch rejected: %v", rj.GetReason())
		}
	}
	if err := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); err != nil {
		return err
	}
	m.BroadcastWorkspaceCreated(ctx, wsID, p.Title, p.RootNodeID)
	return nil
}

func (m *Manager) applyLifecycleRename(ctx context.Context, row LifecycleOutboxRow, p LifecyclePayload, reader LifecycleOutboxReader) error {
	if err := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); err != nil {
		return err
	}
	m.BroadcastWorkspaceRenamed(p.WorkspaceID, p.NewTitle)
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
	if err := reader.MarkLifecycleOutboxConsumed(ctx, row.ID, m.now()); err != nil {
		return err
	}
	m.BroadcastWorkspaceDeleted(p.WorkspaceID, p.WorkerIDs)
	// Drop the deleted workspace from every subscriber's Filter so a
	// long-lived subscription doesn't accumulate dead workspace_ids
	// across the manager's lifetime. Mirrors the rollback path's
	// contraction in applyLifecycleCreate.
	m.contractSubscribersForWorkspace(p.WorkspaceID)
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

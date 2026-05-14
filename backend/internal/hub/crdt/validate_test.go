package crdt_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// allowAll returns an AuthChecker that accepts every workspace.
type allowAll struct{}

func (allowAll) CanWriteWorkspace(_ context.Context, _, _, _ string) bool { return true }
func (allowAll) CanReadWorkspace(_ context.Context, _, _, _ string) bool  { return true }
func (allowAll) CanUseWorker(_ context.Context, _, _, _ string) bool      { return true }

// denyAll returns an AuthChecker that rejects every workspace.
type denyAll struct{}

func (denyAll) CanWriteWorkspace(_ context.Context, _, _, _ string) bool { return false }
func (denyAll) CanReadWorkspace(_ context.Context, _, _, _ string) bool  { return false }
func (denyAll) CanUseWorker(_ context.Context, _, _, _ string) bool      { return false }

// onlyOwner returns an AuthChecker that accepts only workspaces
// owned by the given principal id.
type onlyOwner struct {
	allowed map[string]bool // workspaceID set
}

func (o onlyOwner) CanWriteWorkspace(_ context.Context, _, workspaceID, _ string) bool {
	return o.allowed[workspaceID]
}
func (o onlyOwner) CanReadWorkspace(_ context.Context, _, workspaceID, _ string) bool {
	return o.allowed[workspaceID]
}
func (o onlyOwner) CanUseWorker(_ context.Context, _, _, _ string) bool { return true }

// workerScope is an AuthChecker variant that accepts every workspace
// (the per-op auth check is orthogonal to worker_ref validation) but
// gates `CanUseWorker` to a fixed allow-list. Used by the
// `validateWorkerRefs` tests to assert the new BATCH_REJECTION_
// INVALID_WORKER_REF gate.
type workerScope struct {
	workers map[string]bool
}

func (workerScope) CanWriteWorkspace(_ context.Context, _, _, _ string) bool { return true }
func (workerScope) CanReadWorkspace(_ context.Context, _, _, _ string) bool  { return true }
func (s workerScope) CanUseWorker(_ context.Context, _, workerID, _ string) bool {
	return s.workers[workerID]
}

func TestValidate_TabPlacementInvariant_OrphanTile(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")

	// Tab points at a non-existent tile.
	tab := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "ghost"},
	}, hlcAt(10, 0, "a"))
	worker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w1"},
	}, hlcAt(10, 1, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 2, "a"))

	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{tab, worker, pos}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID, res.Reason)
}

func TestValidate_ParentImmutable_RejectsReParent(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Existing node `root1` already has its parent_id at "" (root).
	// Try to write parent_id="other" — should reject.
	op := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "other"},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_PARENT_IMMUTABLE, res.Reason)
}

func TestValidate_HubOnlyOp_RejectsClient(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetWorkspaceRootNodeOp{
		WorkspaceId: "w1", RootNodeId: "another",
	}, hlcAt(10, 0, "a"))
	op.Body = &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
		WorkspaceId: "w1", RootNodeId: "another",
	}}
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, false /* not internal */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, res.Reason)
}

func TestValidate_TabIDCollisionAcrossTypes(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// First op claims tab_id=X under TAB_TYPE_AGENT.
	a := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "X",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	// Second op claims the same tab_id under TAB_TYPE_TERMINAL.
	b := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "X",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 1, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{a, b}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_ID_COLLISION_ACROSS_TYPES, res.Reason)
}

func TestValidate_ValueDomain_OpacityOutOfRange(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fw",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: 1.5},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, res.Reason)
}

// TestValidate_WorkerRef_AcceptsAccessibleWorker proves the happy
// path: a SetTabRegister(worker_id=X) where the principal can use X
// commits without trouble. Pairs with the rejection test below so
// the validation doesn't regress to "always allow" or "always deny".
func TestValidate_WorkerRef_AcceptsAccessibleWorker(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	tab := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	worker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w-ok"},
	}, hlcAt(10, 1, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 2, "a"))
	auth := workerScope{workers: map[string]bool{"w-ok": true}}
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{tab, worker, pos}, false, "p1", auth)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"a SetTabRegister(worker_id) with an accessible worker must not be rejected")
}

// TestValidate_WorkerRef_RejectsInaccessibleWorker is the regression
// test for `leapmux remote tab open --worker-id f` silently
// committing a tab pinned to a non-existent worker. The CRDT layer
// must refuse the whole batch with BATCH_REJECTION_INVALID_WORKER_REF
// so trustless clients can't smuggle garbage worker_ids in.
func TestValidate_WorkerRef_RejectsInaccessibleWorker(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	tab := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	worker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "f"},
	}, hlcAt(10, 1, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 2, "a"))
	auth := workerScope{workers: map[string]bool{"w-ok": true}} // "f" not allowed
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{tab, worker, pos}, false, "p1", auth)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INVALID_WORKER_REF, res.Reason)
	assert.Equal(t, worker.GetOpId(), res.OffendingOpID,
		"OffendingOpID must point at the SetTabRegister(worker_id) op, not the tile/position siblings")
}

// TestValidate_WorkerRef_EmptyWorkerIDSkipsCheck pins the contract
// of validateWorkerRefs alone: an empty worker_id is not a real
// reference, so it MUST NOT trip the worker-ref gate (the broader
// completeness check may still reject — that's a separate concern).
// Without this carve-out, a denying AuthChecker would block tabs
// from clearing their worker_id register.
func TestValidate_WorkerRef_EmptyWorkerIDSkipsCheck(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	clearWorker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: ""},
	}, hlcAt(10, 1, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{clearWorker}, false, "p1", denyAll{})
	assert.NotEqual(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INVALID_WORKER_REF, res.Reason,
		"an empty worker_id MUST NOT trip the worker-ref gate, even with a denying AuthChecker")
}

// TestValidate_WorkerRef_SkippedUnderInternal asserts that
// hub-internal SubmitOps paths (CreateWorkspace lifecycle, etc.) are
// not subject to worker-ref validation — they may write canonical
// worker_ids the requesting principal couldn't see.
func TestValidate_WorkerRef_SkippedUnderInternal(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	tab := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	worker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "ignored-by-internal"},
	}, hlcAt(10, 1, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 2, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{tab, worker, pos}, true /* internal */, "", denyAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"internal=true must skip the worker-ref check regardless of auth verdict")
}

func TestValidate_TombstonedTarget_Rejects(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Tombstone root1 (allowed in this test seam — the validator rule
	// rejects this as root_node_protected, but for the tombstone check
	// itself we test against a non-root tombstoned node).
	crdt.Apply(pre, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(pre, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root1"},
	}, hlcAt(2, 1, "a")))
	crdt.Apply(pre, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "child"}, hlcAt(3, 0, "a")))

	// Try to set a register on the tombstoned child.
	op := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "v"},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TOMBSTONED_TARGET, res.Reason)
}

func TestValidate_AuthCheck_DenyForbiddenWorkspace(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	worker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w1"},
	}, hlcAt(10, 1, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 2, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op, worker, pos}, false /* not internal */, "p1", denyAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, res.Reason)
}

func TestValidate_AuthCheck_AllowsOwnerWrite(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	worker := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w1"},
	}, hlcAt(10, 1, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 2, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op, worker, pos}, false, "p1", onlyOwner{allowed: map[string]bool{"w1": true}})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason, "owner write should pass")
}

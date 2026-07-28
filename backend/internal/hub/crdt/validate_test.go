package crdt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// allowAll returns an AuthChecker that accepts every workspace.
type allowAll struct{}

func (allowAll) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) { return true, nil }
func (allowAll) CanUseWorker(_ context.Context, _, _ string) (bool, error)       { return true, nil }

// denyAll returns an AuthChecker that rejects every workspace.
type denyAll struct{}

func (denyAll) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) { return false, nil }
func (denyAll) CanUseWorker(_ context.Context, _, _ string) (bool, error)       { return false, nil }

// onlyOwner returns an AuthChecker that accepts only workspaces
// owned by the given principal id.
type onlyOwner struct {
	allowed map[string]bool // workspaceID set
}

func (o onlyOwner) CanAccessWorkspace(_ context.Context, workspaceID, _ string) (bool, error) {
	return o.allowed[workspaceID], nil
}
func (o onlyOwner) CanUseWorker(_ context.Context, _, _ string) (bool, error) { return true, nil }

// workerScope is an AuthChecker variant that accepts every workspace
// (the per-op auth check is orthogonal to worker_ref validation) but
// gates `CanUseWorker` to a fixed allow-list. Used by the
// `validateWorkerRefs` tests to assert the new BATCH_REJECTION_
// INVALID_WORKER_REF gate.
type workerScope struct {
	workers map[string]bool
}

func (workerScope) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (s workerScope) CanUseWorker(_ context.Context, workerID, _ string) (bool, error) {
	return s.workers[workerID], nil
}

// erroringAuth is an AuthChecker whose every predicate fails with a transient
// store error, exercising the retryable-vs-forbidden distinction: a lookup
// failure must NOT collapse into a permanent FORBIDDEN op-rejection.
type erroringAuth struct{ err error }

func (e erroringAuth) CanAccessWorkspace(context.Context, string, string) (bool, error) {
	return false, e.err
}
func (e erroringAuth) CanUseWorker(context.Context, string, string) (bool, error) {
	return false, e.err
}

// TestValidate_TransientAuthLookupError_SurfacesAsErrNotForbidden guards the J1
// fix: when the per-op permission lookup fails transiently (a store error), the
// validator must set result.Err (a retryable signal) rather than reject the op
// as BATCH_REJECTION_FORBIDDEN_WORKSPACE, which would silently drop a user's edit
// on a brief DB hiccup.
func TestValidate_TransientAuthLookupError_SurfacesAsErrNotForbidden(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// A live tab whose tombstone triggers a CanAccessWorkspace(preWS=w1) check.
	pre.Tabs["tA"] = &leapmuxv1.TabRecord{
		TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:    "tA",
		TileId:   &leapmuxv1.LWWString{Value: "root1", Hlc: hlcAt(1, 0, "seed")},
		WorkerId: &leapmuxv1.LWWString{Value: "wkr", Hlc: hlcAt(1, 1, "seed")},
		Position: &leapmuxv1.LWWString{Value: "p", Hlc: hlcAt(1, 2, "seed")},
	}
	tomb := stamped(&leapmuxv1.TombstoneTabOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
	}, hlcAt(10, 0, "a"))

	boom := errors.New("db unavailable")
	res, working := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{tomb}, false /* not internal */, "p1", erroringAuth{err: boom})

	require.ErrorIs(t, res.Err, boom, "a transient permission-lookup failure must surface as a retryable error")
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"a transient lookup error must NOT be a permanent FORBIDDEN op-rejection")
	assert.Nil(t, working)
}

// workerRefErrorAuth allows all workspace access but fails the worker lookup, so
// a SetTabRegisterOp.worker_id write clears the write-auth check and reaches
// validateWorkerRefs' CanUseWorker call.
type workerRefErrorAuth struct{ err error }

func (workerRefErrorAuth) CanAccessWorkspace(context.Context, string, string) (bool, error) {
	return true, nil
}
func (w workerRefErrorAuth) CanUseWorker(context.Context, string, string) (bool, error) {
	return false, w.err
}

// TestValidate_TransientWorkerLookupError_SurfacesAsErr guards the other half of
// the J1 fix: the worker-ref gate (validateWorkerRefs) must also treat a
// transient CanUseWorker store error as retryable (result.Err) rather than a
// permanent INVALID_WORKER_REF rejection that would silently drop the edit.
func TestValidate_TransientWorkerLookupError_SurfacesAsErr(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	pre.Tabs["tA"] = &leapmuxv1.TabRecord{
		TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:    "tA",
		TileId:   &leapmuxv1.LWWString{Value: "root1", Hlc: hlcAt(1, 0, "seed")},
		WorkerId: &leapmuxv1.LWWString{Value: "wkr", Hlc: hlcAt(1, 1, "seed")},
		Position: &leapmuxv1.LWWString{Value: "p", Hlc: hlcAt(1, 2, "seed")},
	}
	// Re-point the existing tab at a different worker so validateWorkerRefs runs
	// CanUseWorker on the new id (an in-place edit clears the write check first).
	op := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr2"},
	}, hlcAt(10, 0, "a"))

	boom := errors.New("db unavailable")
	res, working := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, false /* not internal */, "p1", workerRefErrorAuth{err: boom})

	require.ErrorIs(t, res.Err, boom, "a transient worker-lookup failure must surface as a retryable error")
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"a transient worker-lookup error must NOT be a permanent INVALID_WORKER_REF rejection")
	assert.Nil(t, working)
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

	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{tab, worker, pos}, true, "p1", allowAll{})
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_PARENT_IMMUTABLE, res.Reason)
}

func TestValidate_HubOnlyOp_RejectsClient(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetWorkspaceRootNodeOp{
		WorkspaceId: "w1", RootNodeId: "another",
	}, hlcAt(10, 0, "a"))
	op.Body = &leapmuxv1.CrdtOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
		WorkspaceId: "w1", RootNodeId: "another",
	}}
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, false /* not internal */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, res.Reason)
}

// SetWorkspaceRegisterOp / TombstoneWorkspaceOp are the lifecycle create/delete
// ops that now carry workspace-map membership through the serialized pipeline.
// Both must be rejected as HUB_ONLY_OP when a client sends them (only the
// internal lifecycle path may submit them) and accepted under internal=true.
func TestValidate_SetWorkspaceRegister_HubOnlyGate(t *testing.T) {
	pre := crdt.NewState("user-1")
	op := &leapmuxv1.CrdtOp{
		OpId: "ws-reg", CanonicalHlc: hlcAt(10, 0, "a"),
		Body: &leapmuxv1.CrdtOp_SetWorkspaceRegister{
			SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{WorkspaceId: "w1"},
		},
	}
	// Client path (internal=false): rejected as hub-only.
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, false, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, res.Reason)

	// Internal path (internal=true): accepted.
	res, working := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, true, "hub", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason)
	require.NotNil(t, working)
	assert.Contains(t, working.GetWorkspaces(), "w1", "internal SetWorkspaceRegisterOp should seed the record")
}

func TestValidate_TombstoneWorkspace_HubOnlyGate(t *testing.T) {
	// Seed a workspace record with NO live root node, so removing the
	// record alone doesn't orphan anything (the lifecycle delete path
	// always pairs TombstoneWorkspace with the subtree tombstones; this
	// isolates the hub-only gate from the completeness check).
	pre := crdt.NewState("user-1")
	pre.Workspaces["w1"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w1"}
	op := &leapmuxv1.CrdtOp{
		OpId: "ws-del", CanonicalHlc: hlcAt(10, 0, "a"),
		Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{
			TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"},
		},
	}
	// Client path (internal=false): rejected as hub-only.
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, false, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, res.Reason)

	// Internal path (internal=true): accepted, record removed.
	res, working := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, true, "hub", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason)
	require.NotNil(t, working)
	assert.NotContains(t, working.GetWorkspaces(), "w1", "internal TombstoneWorkspaceOp should remove the record")
}

// TestValidate_HubOnlyOps_RejectEveryClientArm pins that EVERY hub-only op
// body is rejected as HUB_ONLY_OP on the client path. The hub-only gate is
// the access boundary that keeps clients from seeding/removing workspace
// records or re-assigning roots directly; this table-driven guard fails if
// a future hub-only op is added to the proto without a matching arm in
// validateSetOnce (it would slip through as UNSPECIFIED on the client path).
func TestValidate_HubOnlyOps_RejectEveryClientArm(t *testing.T) {
	hlc := hlcAt(10, 0, "a")
	hubOnlyOps := map[string]*leapmuxv1.CrdtOp{
		"SetWorkspaceRootNode": {OpId: "root", CanonicalHlc: hlc, Body: &leapmuxv1.CrdtOp_SetWorkspaceRootNode{
			SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{WorkspaceId: "w1"},
		}},
		"SetWorkspaceRegister": {OpId: "reg", CanonicalHlc: hlc, Body: &leapmuxv1.CrdtOp_SetWorkspaceRegister{
			SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{WorkspaceId: "w1"},
		}},
		"TombstoneWorkspace": {OpId: "del", CanonicalHlc: hlc, Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{
			TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"},
		}},
	}
	for name, op := range hubOnlyOps {
		t.Run(name, func(t *testing.T) {
			res, _ := crdt.ValidateBatch(context.Background(), crdt.NewState("user-1"), []*leapmuxv1.CrdtOp{op}, false, "p1", allowAll{})
			assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, res.Reason,
				"client-sent %s must be rejected as hub-only", name)
		})
	}
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{a, b}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_ID_COLLISION_ACROSS_TYPES, res.Reason)
}

func TestValidate_ValueDomain_OpacityOutOfRange(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fw",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: 1.5},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, true, "p1", allowAll{})
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{tab, worker, pos}, false, "p1", auth)
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{tab, worker, pos}, false, "p1", auth)
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{clearWorker}, false, "p1", denyAll{})
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{tab, worker, pos}, true /* internal */, "", denyAll{})
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op}, true, "p1", allowAll{})
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op, worker, pos}, false /* not internal */, "p1", denyAll{})
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
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.CrdtOp{op, worker, pos}, false, "p1", onlyOwner{allowed: map[string]bool{"w1": true}})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason, "owner write should pass")
}

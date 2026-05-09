package crdt_test

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// TestValidate_RootImmutable_FloatingWindow_RejectsRetargetingRoot
// covers Rule 16's floating-window arm: once a window has been seeded
// (any pre-batch state), any later batch attempting to write a fresh
// root_node_id is rejected as root_immutable.
func TestValidate_RootImmutable_FloatingWindow_RejectsRetargetingRoot(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Seed an existing floating window. We don't need a complete record;
	// the validator only checks existence in the FloatingWindows map.
	pre.FloatingWindows["fw"] = &leapmuxv1.FloatingWindowRecord{
		WindowId:   "fw",
		RootNodeId: "fwroot",
	}
	op := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fw",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId{RootNodeId: "another"},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true /* internal: bypass auth, not set-once */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_IMMUTABLE, res.Reason,
		"writing root_node_id to a pre-existing window must reject as root_immutable")
}

// TestValidate_RootImmutable_WorkspaceRoot_RejectsRetargetingRoot
// covers Rule 16's workspace arm: once a workspace's root_node_id has
// been populated, any further SetWorkspaceRootNodeOp is rejected.
func TestValidate_RootImmutable_WorkspaceRoot_RejectsRetargetingRoot(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := &leapmuxv1.OrgOp{
		OpId:         "op-retarget",
		CanonicalHlc: hlcAt(10, 0, "a"),
		Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w1", RootNodeId: "another",
		}},
	}
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true /* internal */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_IMMUTABLE, res.Reason,
		"writing root_node_id to a workspace whose root is already populated must reject as root_immutable")
}

// TestValidate_RootNodeProtected_ClientCannotTombstoneWorkspaceRoot
// covers Rule 12: a workspace root cannot be tombstoned by a client
// batch (only the internal lifecycle path may do so via the bypass).
func TestValidate_RootNodeProtected_ClientCannotTombstoneWorkspaceRoot(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "root1"}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, false /* client */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_PROTECTED, res.Reason,
		"clients cannot tombstone a workspace root")
}

// TestValidate_RootNodeProtected_InternalLifecycleBypassAllowed
// covers the internal-bypass path: hub-driven lifecycle batches DO
// tombstone workspace roots as part of DeleteWorkspace cleanup.
func TestValidate_RootNodeProtected_InternalLifecycleBypassAllowed(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "root1"}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true /* internal */, "p1", allowAll{})
	assert.NotEqual(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_PROTECTED, res.Reason,
		"internal lifecycle path bypasses root protection on workspace roots")
}

// TestValidate_RootNodeProtected_ClientCanTombstoneEmptyFloatingWindowRoot
// covers Rule 12's exception: a TombstoneNode(window_root) IS allowed
// when the same batch tombstones the window AND the subtree is empty.
func TestValidate_RootNodeProtected_ClientCanTombstoneEmptyFloatingWindowRoot(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Add a floating window whose root is a separate leaf node.
	pre.Nodes["fwroot"] = &leapmuxv1.NodeRecord{
		NodeId: "fwroot",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlcAt(1, 0, "seed")},
	}
	pre.FloatingWindows["fw"] = &leapmuxv1.FloatingWindowRecord{
		WindowId:    "fw",
		RootNodeId:  "fwroot",
		WorkspaceId: &leapmuxv1.LWWString{Value: "w1", Hlc: hlcAt(1, 0, "seed")},
		X:           &leapmuxv1.LWWDouble{Value: 0, Hlc: hlcAt(1, 1, "seed")},
		Y:           &leapmuxv1.LWWDouble{Value: 0, Hlc: hlcAt(1, 2, "seed")},
		Width:       &leapmuxv1.LWWDouble{Value: 100, Hlc: hlcAt(1, 3, "seed")},
		Height:      &leapmuxv1.LWWDouble{Value: 100, Hlc: hlcAt(1, 4, "seed")},
		Opacity:     &leapmuxv1.LWWDouble{Value: 1, Hlc: hlcAt(1, 5, "seed")},
	}
	// Same-batch close: tombstone the root + the window.
	closeRoot := stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "fwroot"}, hlcAt(10, 0, "a"))
	closeWin := stamped(&leapmuxv1.TombstoneFloatingWindowOp{WindowId: "fw"}, hlcAt(10, 1, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{closeRoot, closeWin}, false /* client */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"closing an empty floating window in a paired batch must succeed: got %v at %q", res.Reason, res.OffendingOpID)
}

// completeWindowOps builds the full set of register writes a new
// floating window needs to satisfy completeness validation. Callers
// supply the window_id, root_node_id, and starting HLC; the helper
// returns the 7 ops in stable order.
func completeWindowOps(windowID, rootNodeID, workspaceID string, baseHLC int64) []*leapmuxv1.OrgOp {
	return []*leapmuxv1.OrgOp{
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId{RootNodeId: rootNodeID},
		}, hlcAt(baseHLC, 0, "a")),
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId{WorkspaceId: workspaceID},
		}, hlcAt(baseHLC, 1, "a")),
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_X{X: 10},
		}, hlcAt(baseHLC, 2, "a")),
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Y{Y: 20},
		}, hlcAt(baseHLC, 3, "a")),
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Width{Width: 100},
		}, hlcAt(baseHLC, 4, "a")),
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Height{Height: 100},
		}, hlcAt(baseHLC, 5, "a")),
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: windowID,
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: 1},
		}, hlcAt(baseHLC, 6, "a")),
	}
}

// TestValidate_RootNodeNotUnique_RejectsAliasing covers Rule 13: a
// SetFloatingWindowRegister(root_node_id=X) is rejected when X is
// already registered as another root (workspace or floating window).
func TestValidate_RootNodeNotUnique_RejectsAliasing(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Try to seed a NEW floating window whose root_node_id is the same
	// node that's already a workspace root. The window batch is otherwise
	// complete so completeness validation passes.
	batch := completeWindowOps("fw", "root1", "w1", 10)
	res, _ := crdt.ValidateBatch(context.Background(), pre, batch, true /* internal */, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE, res.Reason,
		"aliasing a workspace root as a floating-window root must reject")
}

// TestValidate_RootNodeNotUnique_RejectsTombstonedCandidate covers
// the "candidate is tombstoned" arm of Rule 13.
func TestValidate_RootNodeNotUnique_RejectsTombstonedCandidate(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Add a tombstoned node and try to assign it as a window root.
	pre.Nodes["dead"] = &leapmuxv1.NodeRecord{
		NodeId:      "dead",
		TombstoneAt: hlcAt(2, 0, "a"),
	}
	batch := completeWindowOps("fw", "dead", "w1", 10)
	res, _ := crdt.ValidateBatch(context.Background(), pre, batch, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE, res.Reason,
		"a tombstoned candidate must reject as not-unique")
}

// TestValidate_RootNodeNotUnique_RejectsParentedCandidate covers the
// "candidate has a non-empty parent_id" arm of Rule 13. Floating-window
// roots and workspace roots must be parentless (the validator owns the
// "alias the workspace root subtree" invariant).
func TestValidate_RootNodeNotUnique_RejectsParentedCandidate(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Add a leaf node with a parent. Trying to alias it as a window
	// root must reject.
	pre.Nodes["child"] = &leapmuxv1.NodeRecord{
		NodeId:   "child",
		ParentId: "root1",
		Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlcAt(2, 0, "a")},
		Position: &leapmuxv1.LWWString{Value: "p", Hlc: hlcAt(2, 1, "a")},
	}
	batch := completeWindowOps("fw", "child", "w1", 10)
	res, _ := crdt.ValidateBatch(context.Background(), pre, batch, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE, res.Reason,
		"a parented candidate must reject as not-unique")
}

// TestValidate_IncompleteRecord_TabMissingWorker covers Rule 15: a
// live tab record without worker_id must reject as incomplete_record.
func TestValidate_IncompleteRecord_TabMissingWorker(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	tile := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tInc",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a"))
	pos := stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tInc",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"},
	}, hlcAt(10, 1, "a"))
	// No worker_id op → the post-batch live tab is incomplete.
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{tile, pos}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INCOMPLETE_RECORD, res.Reason)
}

// TestValidate_IncompleteRecord_SplitNodeMissingDirection covers the
// SPLIT-kind arm of Rule 15.
func TestValidate_IncompleteRecord_SplitNodeMissingDirection(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Promote root1 to SPLIT without setting direction/ratios.
	kind := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{kind}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INCOMPLETE_RECORD, res.Reason)
}

// TestValidate_FloatingMoveWithNonRootDescendants_Rejected covers
// Rule 14: a cross-workspace move on a non-empty floating window is
// rejected. v1 only allows empty-window moves.
func TestValidate_FloatingMoveWithNonRootDescendants_Rejected(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	pre.Workspaces["w2"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w2", RootNodeId: "root2"}
	pre.Nodes["root2"] = &leapmuxv1.NodeRecord{
		NodeId: "root2",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlcAt(1, 0, "seed")},
	}
	// Floating window with a non-root child. The root is a SPLIT so we
	// must populate direction/ratios for completeness validation to
	// pass.
	pre.Nodes["fwroot"] = &leapmuxv1.NodeRecord{
		NodeId:    "fwroot",
		Kind:      &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT, Hlc: hlcAt(1, 0, "seed")},
		Direction: &leapmuxv1.LWWDirection{Value: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL, Hlc: hlcAt(1, 1, "seed")},
		Ratios: &leapmuxv1.LWWDoubles{
			Value: &leapmuxv1.DoubleList{Values: []float64{1.0}},
			Hlc:   hlcAt(1, 2, "seed"),
		},
	}
	pre.Nodes["fwchild"] = &leapmuxv1.NodeRecord{
		NodeId:   "fwchild",
		ParentId: "fwroot",
		Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlcAt(1, 1, "seed")},
		Position: &leapmuxv1.LWWString{Value: "p", Hlc: hlcAt(1, 2, "seed")},
	}
	pre.FloatingWindows["fw"] = &leapmuxv1.FloatingWindowRecord{
		WindowId:    "fw",
		RootNodeId:  "fwroot",
		WorkspaceId: &leapmuxv1.LWWString{Value: "w1", Hlc: hlcAt(1, 0, "seed")},
		X:           &leapmuxv1.LWWDouble{Value: 0, Hlc: hlcAt(1, 1, "seed")},
		Y:           &leapmuxv1.LWWDouble{Value: 0, Hlc: hlcAt(1, 2, "seed")},
		Width:       &leapmuxv1.LWWDouble{Value: 100, Hlc: hlcAt(1, 3, "seed")},
		Height:      &leapmuxv1.LWWDouble{Value: 100, Hlc: hlcAt(1, 4, "seed")},
		Opacity:     &leapmuxv1.LWWDouble{Value: 1, Hlc: hlcAt(1, 5, "seed")},
	}
	// Try to move the non-empty window to w2.
	move := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fw",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId{WorkspaceId: "w2"},
	}, hlcAt(20, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{move}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FLOATING_MOVE_WITH_DESCENDANTS, res.Reason)
}

// TestValidate_FloatingMove_EmptyWindow_Allowed is the inverse of the
// above: an empty window (no live descendants, no tabs) IS movable.
func TestValidate_FloatingMove_EmptyWindow_Allowed(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	pre.Workspaces["w2"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w2", RootNodeId: "root2"}
	pre.Nodes["root2"] = &leapmuxv1.NodeRecord{
		NodeId: "root2",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlcAt(1, 0, "seed")},
	}
	pre.Nodes["fwroot"] = &leapmuxv1.NodeRecord{
		NodeId: "fwroot",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlcAt(1, 0, "seed")},
	}
	pre.FloatingWindows["fw"] = &leapmuxv1.FloatingWindowRecord{
		WindowId:    "fw",
		RootNodeId:  "fwroot",
		WorkspaceId: &leapmuxv1.LWWString{Value: "w1", Hlc: hlcAt(1, 0, "seed")},
		X:           &leapmuxv1.LWWDouble{Value: 0, Hlc: hlcAt(1, 1, "seed")},
		Y:           &leapmuxv1.LWWDouble{Value: 0, Hlc: hlcAt(1, 2, "seed")},
		Width:       &leapmuxv1.LWWDouble{Value: 100, Hlc: hlcAt(1, 3, "seed")},
		Height:      &leapmuxv1.LWWDouble{Value: 100, Hlc: hlcAt(1, 4, "seed")},
		Opacity:     &leapmuxv1.LWWDouble{Value: 1, Hlc: hlcAt(1, 5, "seed")},
	}
	move := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fw",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId{WorkspaceId: "w2"},
	}, hlcAt(20, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{move}, true, "p1", allowAll{})
	require.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"empty floating window must be movable; got %v at %q", res.Reason, res.OffendingOpID)
}

// TestValidate_ValueDomain_NegativeZero_NormalizedNotRejected confirms
// the plan invariant: -0.0 is silently normalized to +0.0 inside Apply,
// NOT rejected at validation time. The plan explicitly calls this out.
// math.Copysign is required because Go's `-0.0` constant is folded to
// `0.0` at parse time; staticcheck flags the literal as misleading.
func TestValidate_ValueDomain_NegativeZero_NormalizedNotRejected(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Negative-zero opacity — finite, in [0,1] (since -0 == 0). Must pass.
	negZero := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fwnew",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: math.Copysign(0, -1)},
	}, hlcAt(10, 0, "a"))
	// Note: this is an isolated op test; we don't expect to reach apply
	// without the full completeness — but the value-domain rule
	// specifically must not reject -0.
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{negZero}, true, "p1", allowAll{})
	assert.NotEqual(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, res.Reason,
		"-0.0 must not be rejected by value-domain validation; got %v", res.Reason)
}

// TestValidate_ValueDomain_RejectsNaNRatios covers the NaN rejection
// arm of value-domain validation for ratios.
func TestValidate_ValueDomain_RejectsNaNRatios(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field: &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{
			Values: []float64{math.NaN(), 0.5},
		}},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, res.Reason)
}

// TestValidate_ValueDomain_RejectsNegativeRatios covers the negative-
// component rejection arm of value-domain validation.
func TestValidate_ValueDomain_RejectsNegativeRatios(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field: &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{
			Values: []float64{-0.5, 1.5},
		}},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, res.Reason)
}

// TestValidate_ValueDomain_RejectsZeroWidth covers width/height > 0.
func TestValidate_ValueDomain_RejectsZeroWidth(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "fwnew",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Width{Width: 0},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, res.Reason)
}

// TestValidate_ValueDomain_RejectsTooManyGridRows covers the
// MaxGridDimension cap.
func TestValidate_ValueDomain_RejectsTooManyGridRows(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	op := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Rows{Rows: crdt.MaxGridDimension + 1},
	}, hlcAt(10, 0, "a"))
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{op}, true, "p1", allowAll{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, res.Reason)
}

// TestValidate_PureDelete_OnlyPreWorkspacePermissionRequired covers
// the auth-rule's tombstone-exception: tombstoning a tab only requires
// write access to the pre-batch workspace.
func TestValidate_PureDelete_OnlyPreWorkspacePermissionRequired(t *testing.T) {
	pre := seedWorkspaceWithRoot("w1", "root1")
	// Seed a live tab on w1.
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
	// Caller only has write on w1 (the pre-workspace). No other
	// workspaces should be required.
	res, _ := crdt.ValidateBatch(context.Background(), pre, []*leapmuxv1.OrgOp{tomb}, false /* client */, "p1", onlyOwner{allowed: map[string]bool{"w1": true}})
	require.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"pure-delete should only require pre-workspace write; got %v at %q", res.Reason, res.OffendingOpID)
}

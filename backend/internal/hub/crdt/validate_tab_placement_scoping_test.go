package crdt_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// allowAllAuth implements crdt.AuthChecker by always returning true.
// Tab placement scoping is structural — auth outcomes don't gate it —
// so any auth shim works.
type allowAllAuth struct{}

func (allowAllAuth) CanWriteWorkspace(context.Context, string, string, string) bool {
	return true
}
func (allowAllAuth) CanReadWorkspace(context.Context, string, string, string) bool { return true }
func (allowAllAuth) CanUseWorker(context.Context, string, string, string) bool     { return true }

// tabPlacementCheck scoping: pre-existing tabs that already passed
// placement must not be re-validated when the batch carries only tab
// register/tombstone ops. A structural op (tombstone-node, kind flip,
// root register, floating-window register) re-enables the full walk.

func mkState() *leapmuxv1.OrgCrdtState {
	// Workspace with a root LEAF tile and one valid AGENT tab on it.
	wsID := "ws-1"
	rootID := "root-tile"
	tabID := "tab-A"
	hl := func(p int64) *leapmuxv1.HLC { return &leapmuxv1.HLC{Physical: p, ClientId: "seed"} }
	return &leapmuxv1.OrgCrdtState{
		OrgId: "org-1",
		Nodes: map[string]*leapmuxv1.NodeRecord{
			rootID: {
				NodeId: rootID,
				Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hl(1)},
			},
		},
		Tabs: map[string]*leapmuxv1.TabRecord{
			tabID: {
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:    tabID,
				TileId:   &leapmuxv1.LWWString{Value: rootID, Hlc: hl(2)},
				WorkerId: &leapmuxv1.LWWString{Value: "w-1", Hlc: hl(2)},
				Position: &leapmuxv1.LWWString{Value: "a0", Hlc: hl(2)},
			},
		},
		Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
			wsID: {WorkspaceId: wsID, RootNodeId: rootID},
		},
		MaxHlc:       hl(10),
		CurrentEpoch: 1,
	}
}

func TestValidate_TabPlacementSkipsPreExistingTabsWhenNoStructuralOp(t *testing.T) {
	// Pre-state has a valid tab "tab-A". The batch reassigns its
	// position only — no tombstones, no kind flips. The scoping
	// optimization must keep the batch valid; if the full walk
	// were running, the corrupt orphan tab "tab-orphan" below would
	// trigger a rejection.
	pre := mkState()
	// Inject a pre-existing INVALID tab whose tile points at a
	// non-existent leaf. tabPlacementCheck used to scan every tab on
	// every batch and would have rejected on this. With scoping, the
	// orphan is left alone unless the batch transitively touches it.
	pre.Tabs["tab-orphan"] = &leapmuxv1.TabRecord{
		TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:    "tab-orphan",
		TileId:   &leapmuxv1.LWWString{Value: "ghost-tile", Hlc: &leapmuxv1.HLC{Physical: 5}},
		WorkerId: &leapmuxv1.LWWString{Value: "w-x", Hlc: &leapmuxv1.HLC{Physical: 5}},
		Position: &leapmuxv1.LWWString{Value: "o0", Hlc: &leapmuxv1.HLC{Physical: 5}},
	}
	batch := []*leapmuxv1.OrgOp{
		{
			OpId:         "op-pos",
			CanonicalHlc: &leapmuxv1.HLC{Physical: 20, ClientId: "hub"},
			Body: &leapmuxv1.OrgOp_SetTabRegister{
				SetTabRegister: &leapmuxv1.SetTabRegisterOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabId:   "tab-A",
					Field:   &leapmuxv1.SetTabRegisterOp_Position{Position: "a1"},
				},
			},
		},
	}
	res, _ := crdt.ValidateBatch(context.Background(), pre, batch, false, "principal", allowAllAuth{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason,
		"scoped check must not surface the pre-existing orphan tab")
}

func TestValidate_TabPlacementRejectsTouchedTabWithInvalidTile(t *testing.T) {
	// Sanity: the scoping doesn't accidentally skip the tab the batch
	// actually touches. Set a tab's tile_id to a non-existent node and
	// the batch must reject.
	pre := mkState()
	batch := []*leapmuxv1.OrgOp{
		{
			OpId:         "op-move",
			CanonicalHlc: &leapmuxv1.HLC{Physical: 20, ClientId: "hub"},
			Body: &leapmuxv1.OrgOp_SetTabRegister{
				SetTabRegister: &leapmuxv1.SetTabRegisterOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabId:   "tab-A",
					Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "ghost-tile"},
				},
			},
		},
	}
	res, _ := crdt.ValidateBatch(context.Background(), pre, batch, false, "principal", allowAllAuth{})
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID, res.Reason)
}

func TestValidate_TabPlacementStructuralOpReEnablesFullWalk(t *testing.T) {
	// When a structural op (TombstoneNode here) is in the batch, the
	// full walk runs again — and the pre-existing orphan tab is now
	// detected and surfaces a rejection. Without this fallback the
	// orphan could persist after a tree-restructuring batch.
	pre := mkState()
	pre.Tabs["tab-orphan"] = &leapmuxv1.TabRecord{
		TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:    "tab-orphan",
		TileId:   &leapmuxv1.LWWString{Value: "ghost-tile", Hlc: &leapmuxv1.HLC{Physical: 5}},
		WorkerId: &leapmuxv1.LWWString{Value: "w-x", Hlc: &leapmuxv1.HLC{Physical: 5}},
		Position: &leapmuxv1.LWWString{Value: "o0", Hlc: &leapmuxv1.HLC{Physical: 5}},
	}
	// A second, structurally-unrelated leaf exists so we can safely
	// tombstone an unused node without violating root protection.
	pre.Nodes["extra-leaf"] = &leapmuxv1.NodeRecord{
		NodeId:   "extra-leaf",
		ParentId: "", // orphan node, never referenced — safe to tombstone
		Kind: &leapmuxv1.LWWNodeKind{
			Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: &leapmuxv1.HLC{Physical: 3},
		},
	}
	batch := []*leapmuxv1.OrgOp{
		{
			OpId:         "op-tomb",
			CanonicalHlc: &leapmuxv1.HLC{Physical: 20, ClientId: "hub"},
			Body: &leapmuxv1.OrgOp_TombstoneNode{
				TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: "extra-leaf"},
			},
		},
	}
	res, _ := crdt.ValidateBatch(context.Background(), pre, batch, false, "principal", allowAllAuth{})
	// With the structural op present, the full walk runs and finds
	// the pre-existing orphan.
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID, res.Reason)
	assert.Equal(t, "tab-orphan", res.OffendingOpID)
}

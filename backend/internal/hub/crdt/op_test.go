package crdt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// TestOpTarget_ClassifiesEveryOpKind pins the EntityRef each OrgOp body
// resolves to. The broadcast filter's EntityKindWorkspaceRoot arm
// (manager_broadcast.go: keep on preVisible || postVisible, distinct from the
// preVisible && postVisible rule for other entities) depends on the two
// workspace membership ops classifying as EntityKindWorkspaceRoot; this is the
// direct assertion that the lifecycle create/delete ops reach that arm.
func TestOpTarget_ClassifiesEveryOpKind(t *testing.T) {
	cases := []struct {
		name string
		op   *leapmuxv1.OrgOp
		want crdt.EntityRef
	}{
		{
			name: "SetWorkspaceRegister -> WorkspaceRoot",
			op: &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_SetWorkspaceRegister{
				SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{WorkspaceId: "w1"},
			}},
			want: crdt.EntityRef{Kind: crdt.EntityKindWorkspaceRoot, WorkspaceID: "w1"},
		},
		{
			name: "TombstoneWorkspace -> WorkspaceRoot",
			op: &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_TombstoneWorkspace{
				TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"},
			}},
			want: crdt.EntityRef{Kind: crdt.EntityKindWorkspaceRoot, WorkspaceID: "w1"},
		},
		{
			name: "SetWorkspaceRootNode -> WorkspaceRoot",
			op: &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{
				SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{WorkspaceId: "w1", RootNodeId: "r"},
			}},
			want: crdt.EntityRef{Kind: crdt.EntityKindWorkspaceRoot, WorkspaceID: "w1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crdt.OpTarget(tc.op))
		})
	}
}

// TestOpTarget_UnknownBodyIsKindUnknown pins the fall-through: a nil body
// yields EntityKindUnknown, which batchVisibleOpsEvent treats as not-visible
// (IsAllowed("") is false) so a malformed op is dropped rather than crashing.
func TestOpTarget_UnknownBodyIsKindUnknown(t *testing.T) {
	ref := crdt.OpTarget(&leapmuxv1.OrgOp{})
	assert.Equal(t, crdt.EntityKindUnknown, ref.Kind)
}

// TestIsTombstoneOp pins the membership of the tombstone-op set. The set is
// load-bearing in two places: validate.go skips the redundant post-state
// workspace walk for these ops, and validate.go's post-pin pins postW to preW
// so they record a stable {Pre, Post} transition (no OUT) and reach
// subscribers via the raw-op-in-Batch path. TombstoneWorkspace is included so
// a workspace delete does not produce a spurious OUT transition that would
// call removed() — which returns nil for EntityKindWorkspaceRoot (the proto
// EntityRemoved oneof has no workspace variant) and would ship a 0-byte WS
// frame without the removed() nil-guard.
func TestIsTombstoneOp(t *testing.T) {
	cases := []struct {
		name string
		op   *leapmuxv1.OrgOp
		want bool
	}{
		{"TombstoneNode", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_TombstoneNode{
			TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: "n1"}}}, true},
		{"TombstoneTab", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_TombstoneTab{
			TombstoneTab: &leapmuxv1.TombstoneTabOp{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1"}}}, true},
		{"TombstoneFloatingWindow", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_TombstoneFloatingWindow{
			TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{WindowId: "fw1"}}}, true},
		{"TombstoneWorkspace", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_TombstoneWorkspace{
			TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"}}}, true},
		{"SetWorkspaceRegister", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_SetWorkspaceRegister{
			SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{WorkspaceId: "w1"}}}, false},
		{"SetWorkspaceRootNode", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{
			SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{WorkspaceId: "w1", RootNodeId: "r"}}}, false},
		{"SetNodeRegister", &leapmuxv1.OrgOp{Body: &leapmuxv1.OrgOp_SetNodeRegister{
			SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{NodeId: "n1"}}}, false},
		{"nil body", &leapmuxv1.OrgOp{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crdt.IsTombstoneOp(tc.op))
		})
	}
}

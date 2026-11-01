package crdt_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func TestBatchBodyHashExcludesTheSubmissionEpoch(t *testing.T) {
	first := positionBatchAtEpoch(1, "first", "p1")
	retry := positionBatchAtEpoch(2, "second", "p1")
	firstHash, err := crdt.BatchBodyHash(first)
	require.NoError(t, err)
	retryHash, err := crdt.BatchBodyHash(retry)
	require.NoError(t, err)
	assert.Equal(t, firstHash, retryHash)
	changedHash, err := crdt.BatchBodyHash(positionBatchAtEpoch(2, "second", "p2"))
	require.NoError(t, err)
	assert.NotEqual(t, firstHash, changedHash)
}

// TestOpTarget_ClassifiesEveryOpKind pins the EntityRef each CrdtOp body
// resolves to. The broadcast filter's EntityKindWorkspaceRoot arm
// (manager_broadcast.go: keep on preVisible || postVisible, distinct from the
// preVisible && postVisible rule for other entities) depends on the two
// workspace membership ops classifying as EntityKindWorkspaceRoot; this is the
// direct assertion that the lifecycle create/delete ops reach that arm.
func TestOpTarget_ClassifiesEveryOpKind(t *testing.T) {
	cases := []struct {
		name string
		op   *leapmuxv1.CrdtOp
		want crdt.EntityRef
	}{
		{
			name: "TombstoneWorkspace -> WorkspaceRoot",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{
				TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"},
			}},
			want: crdt.EntityRef{Kind: crdt.EntityKindWorkspaceRoot, WorkspaceID: "w1"},
		},
		{
			name: "CreateWorkspace -> WorkspaceRoot",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_CreateWorkspace{
				CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{WorkspaceId: "w1", RootNodeId: "r"},
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
// yields EntityKindUnknown, which opVisibleForSubscriber treats as not-visible
// (IsAllowed("") is false) so a malformed op is dropped rather than crashing.
func TestOpTarget_UnknownBodyIsKindUnknown(t *testing.T) {
	ref := crdt.OpTarget(&leapmuxv1.CrdtOp{})
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
		op   *leapmuxv1.CrdtOp
		want bool
	}{
		{"TombstoneNode", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneNode{
			TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: "n1"}}}, true},
		{"TombstoneTab", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneTab{
			TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1"}}}}, true},
		{"TombstoneFloatingWindow", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneFloatingWindow{
			TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{WindowId: "fw1"}}}, true},
		{"TombstoneWorkspace", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{
			TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"}}}, true},
		{"CreateWorkspace", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_CreateWorkspace{
			CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{WorkspaceId: "w1", RootNodeId: "r"}}}, false},
		{"SetNodeRegister", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{
			SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{NodeId: "n1"}}}, false},
		{"nil body", &leapmuxv1.CrdtOp{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crdt.IsTombstoneOp(tc.op))
		})
	}
}

// The ops a hub journal or a browser checkpoint stored under the previous
// CrdtOp numbering, hand-encoded with the old field layout. CrdtOp.body is
// numbered so that none of them decodes as a different destructive op: each
// must either fail to decode, or decode into an op that applies as a no-op. In
// particular no record appears under the empty tab id and no workspace record
// is removed.
func TestOldStoredOpsNeverApplyAsADifferentOp(t *testing.T) {
	// A 48-character nanoid, the shape of every stored id.
	const workspaceID = "Wk1aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789_-abcdefg"
	require.Len(t, workspaceID, 48)
	const tabID = "t1"

	appendString := func(b []byte, field protowire.Number, v string) []byte {
		b = protowire.AppendTag(b, field, protowire.BytesType)
		return protowire.AppendString(b, v)
	}
	appendVarint := func(b []byte, field protowire.Number, v uint64) []byte {
		b = protowire.AppendTag(b, field, protowire.VarintType)
		return protowire.AppendVarint(b, v)
	}
	appendMessage := func(b []byte, field protowire.Number, msg []byte) []byte {
		b = protowire.AppendTag(b, field, protowire.BytesType)
		return protowire.AppendBytes(b, msg)
	}
	// CrdtOp{op_id=1, canonical_hlc=4, <body field>=body}.
	oldOp := func(bodyField protowire.Number, body []byte) []byte {
		var hlc []byte
		hlc = appendVarint(hlc, 1, 100)
		hlc = appendString(hlc, 3, "hub")
		var op []byte
		op = appendString(op, 1, "old-op")
		op = appendMessage(op, 4, hlc)
		return appendMessage(op, bodyField, body)
	}

	var setWorkspaceRegister []byte // old SetWorkspaceRegisterOp{workspace_id=1}
	setWorkspaceRegister = appendString(setWorkspaceRegister, 1, workspaceID)
	var setTabRegister []byte // old SetTabRegisterOp{tab_type=1 varint, tab_id=2, tile_id=10}
	setTabRegister = appendVarint(setTabRegister, 1, uint64(leapmuxv1.TabType_TAB_TYPE_AGENT))
	setTabRegister = appendString(setTabRegister, 2, tabID)
	setTabRegister = appendString(setTabRegister, 10, "moved-tile")
	var tombstoneTab []byte // old TombstoneTabOp{tab_type=1 varint, tab_id=2}
	tombstoneTab = appendVarint(tombstoneTab, 1, uint64(leapmuxv1.TabType_TAB_TYPE_AGENT))
	tombstoneTab = appendString(tombstoneTab, 2, tabID)

	cases := []struct {
		name  string
		bytes []byte
	}{
		{"set_workspace_register at the old field 17", oldOp(17, setWorkspaceRegister)},
		{"set_tab_register with a varint tab_type at field 1", oldOp(12, setTabRegister)},
		{"tombstone_tab with a varint tab_type at field 1", oldOp(13, tombstoneTab)},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			var op leapmuxv1.CrdtOp
			if err := proto.Unmarshal(tc.bytes, &op); err != nil {
				return // A loud decode failure is an accepted outcome.
			}
			assert.False(t, crdt.OpTarget(&op).HasIdentity() && crdt.OpTarget(&op).Kind == crdt.EntityKindTab,
				"an old op must not decode into a tab identity")

			state := crdt.NewState("user-1")
			state.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: workspaceID, RootNodeId: "root"}
			state.Tabs[tabID] = &leapmuxv1.TabRecord{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:   tabID,
				TileId:  &leapmuxv1.LWWString{Value: "root", Hlc: hlcAt(1, 0, "a")},
			}
			before := crdt.CloneState(state)

			crdt.Apply(state, &op)

			assert.NotContains(t, state.Tabs, "", "no record may appear under the empty tab id")
			assert.Contains(t, state.Workspaces, workspaceID, "no workspace record may be removed")
			assert.True(t, proto.Equal(before.GetTabs()[tabID], state.GetTabs()[tabID]), "the stored tab must not change")
			assert.True(t, proto.Equal(before.GetWorkspaces()[workspaceID], state.GetWorkspaces()[workspaceID]),
				"the stored workspace must not change")
			assert.Len(t, state.Tabs, len(before.Tabs))
			assert.Len(t, state.Workspaces, len(before.Workspaces))
		})
	}
}

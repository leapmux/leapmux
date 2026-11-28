package crdt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func TestStateFromMaterialized(t *testing.T) {
	t.Run("nil snapshot returns empty initialized state", func(t *testing.T) {
		state := crdt.StateFromMaterialized(nil)
		require.NotNil(t, state)
		assert.Empty(t, state.GetUserId())
		assert.NotNil(t, state.Nodes)
		assert.NotNil(t, state.Tabs)
		assert.NotNil(t, state.FloatingWindows)
		assert.NotNil(t, state.Workspaces)
		assert.NotNil(t, state.MaxHlc)
		assert.EqualValues(t, 1, state.GetCurrentEpoch())
	})

	t.Run("deep-clones all maps, records, max HLC, and epoch", func(t *testing.T) {
		src := &leapmuxv1.UserMaterialized{
			UserId: "user-1",
			Nodes: map[string]*leapmuxv1.NodeRecord{
				"root-1": {NodeId: "root-1", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}},
			},
			Tabs: map[string]*leapmuxv1.TabRecord{
				"tab-1": {TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			},
			FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
				"win-1": {WindowId: "win-1", RootNodeId: "froot-1"},
			},
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
				"ws-1": {WorkspaceId: "ws-1", RootNodeId: "root-1"},
			},
			MaxHlc:       &leapmuxv1.HLC{Physical: 42, Logical: 3, ClientId: "hub"},
			CurrentEpoch: 7,
		}

		state := crdt.StateFromMaterialized(src)
		assert.Equal(t, "user-1", state.GetUserId())
		assert.True(t, proto.Equal(src.Nodes["root-1"], state.Nodes["root-1"]))
		assert.NotSame(t, src.Nodes["root-1"], state.Nodes["root-1"])
		assert.True(t, proto.Equal(src.Tabs["tab-1"], state.Tabs["tab-1"]))
		assert.NotSame(t, src.Tabs["tab-1"], state.Tabs["tab-1"])
		assert.True(t, proto.Equal(src.FloatingWindows["win-1"], state.FloatingWindows["win-1"]))
		assert.NotSame(t, src.FloatingWindows["win-1"], state.FloatingWindows["win-1"])
		assert.True(t, proto.Equal(src.Workspaces["ws-1"], state.Workspaces["ws-1"]))
		assert.NotSame(t, src.Workspaces["ws-1"], state.Workspaces["ws-1"])
		assert.True(t, proto.Equal(src.MaxHlc, state.GetMaxHlc()))
		assert.NotSame(t, src.MaxHlc, state.GetMaxHlc())
		assert.EqualValues(t, 7, state.GetCurrentEpoch())

		// Deleting from the returned state must not mutate the source snapshot's maps.
		delete(state.Tabs, "tab-1")
		assert.Contains(t, src.Tabs, "tab-1")
	})

	t.Run("nil MaxHlc keeps zero non-nil MaxHlc", func(t *testing.T) {
		state := crdt.StateFromMaterialized(&leapmuxv1.UserMaterialized{
			UserId:       "user-2",
			CurrentEpoch: 3,
		})
		require.NotNil(t, state.GetMaxHlc())
		assert.True(t, crdt.HLCIsZero(state.GetMaxHlc()))
		assert.EqualValues(t, 3, state.GetCurrentEpoch())
	})

	t.Run("skips nil record entries without panicking", func(t *testing.T) {
		src := &leapmuxv1.UserMaterialized{
			UserId: "user-3",
			Nodes: map[string]*leapmuxv1.NodeRecord{
				"nil-node": nil,
				"node-1":   {NodeId: "node-1"},
			},
			Tabs: map[string]*leapmuxv1.TabRecord{
				"nil-tab": nil,
				"tab-1":   {TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			},
			FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
				"nil-win": nil,
				"win-1":   {WindowId: "win-1"},
			},
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
				"nil-ws": nil,
				"ws-1":   {WorkspaceId: "ws-1"},
			},
		}

		var state *leapmuxv1.UserCrdtState
		require.NotPanics(t, func() {
			state = crdt.StateFromMaterialized(src)
		})
		assert.NotContains(t, state.Nodes, "nil-node")
		assert.Contains(t, state.Nodes, "node-1")
		assert.NotSame(t, src.Nodes["node-1"], state.Nodes["node-1"])
		assert.NotContains(t, state.Tabs, "nil-tab")
		assert.Contains(t, state.Tabs, "tab-1")
		assert.NotSame(t, src.Tabs["tab-1"], state.Tabs["tab-1"])
		assert.NotContains(t, state.FloatingWindows, "nil-win")
		assert.Contains(t, state.FloatingWindows, "win-1")
		assert.NotSame(t, src.FloatingWindows["win-1"], state.FloatingWindows["win-1"])
		assert.NotContains(t, state.Workspaces, "nil-ws")
		assert.Contains(t, state.Workspaces, "ws-1")
		assert.NotSame(t, src.Workspaces["ws-1"], state.Workspaces["ws-1"])
	})

	t.Run("crdt.Apply does not mutate source UserMaterialized", func(t *testing.T) {
		hlc := func(p int64) *leapmuxv1.HLC { return &leapmuxv1.HLC{Physical: p, Logical: 1, ClientId: "hub"} }
		src := &leapmuxv1.UserMaterialized{
			UserId: "user-1",
			Nodes: map[string]*leapmuxv1.NodeRecord{
				"node-1": {
					NodeId:   "node-1",
					Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlc(10)},
					Position: &leapmuxv1.LWWString{Value: "a0", Hlc: hlc(10)},
				},
			},
			Tabs: map[string]*leapmuxv1.TabRecord{
				"tab-1": {
					TabId:    "tab-1",
					TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
					TileId:   &leapmuxv1.LWWString{Value: "node-1", Hlc: hlc(10)},
					WorkerId: &leapmuxv1.LWWString{Value: "w-1", Hlc: hlc(10)},
				},
			},
			FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
				"win-1": {
					WindowId:    "win-1",
					WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1", Hlc: hlc(10)},
					X:           &leapmuxv1.LWWDouble{Value: 10, Hlc: hlc(10)},
				},
			},
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
				"ws-1": {WorkspaceId: "ws-1"},
			},
			MaxHlc:       hlc(10),
			CurrentEpoch: 4,
		}
		want := proto.Clone(src).(*leapmuxv1.UserMaterialized)

		state := crdt.StateFromMaterialized(src)

		// Mutate MaxHlc in place before applying ops to confirm MaxHlc itself is unaliased.
		state.MaxHlc.Physical = 15
		assert.EqualValues(t, 10, src.GetMaxHlc().GetPhysical())

		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(20),
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: "node-1",
				Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root-1"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(21),
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: "node-1",
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "b1"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(22),
			Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Tab:   &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-1"},
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "node-2"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(23),
			Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Tab:   &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-1"},
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w-2"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(24),
			Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				WindowId: "win-1",
				Field:    &leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId{RootNodeId: "froot-1"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(25),
			Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				WindowId: "win-1",
				Field:    &leapmuxv1.SetFloatingWindowRegisterOp_X{X: 250},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(26),
			Body: &leapmuxv1.CrdtOp_CreateWorkspace{CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{
				WorkspaceId: "ws-1",
				RootNodeId:  "root-1",
			}},
		})

		// Confirm every op actually modified the derived state.
		assert.EqualValues(t, 26, state.GetMaxHlc().GetPhysical())
		assert.Equal(t, "root-1", state.Nodes["node-1"].GetParentId())
		assert.Equal(t, "b1", state.Nodes["node-1"].GetPosition().GetValue())
		assert.Equal(t, "node-2", state.Tabs["tab-1"].GetTileId().GetValue())
		assert.Equal(t, "w-2", state.Tabs["tab-1"].GetWorkerId().GetValue())
		assert.Equal(t, "froot-1", state.FloatingWindows["win-1"].GetRootNodeId())
		assert.Equal(t, float64(250), state.FloatingWindows["win-1"].GetX().GetValue())
		assert.Equal(t, "root-1", state.Workspaces["ws-1"].GetRootNodeId())

		// Confirm the caller-owned UserMaterialized proto is untouched.
		assert.True(t, proto.Equal(want, src))
	})
}

func TestApplyEntityMaterialized(t *testing.T) {
	t.Run("nil state or nil message or empty entity is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() {
			crdt.ApplyEntityMaterialized(nil, &leapmuxv1.EntityMaterialized{})
			crdt.ApplyEntityMaterialized(crdt.NewState("u"), nil)
			crdt.ApplyEntityMaterialized(crdt.NewState("u"), &leapmuxv1.EntityMaterialized{})
			crdt.ApplyEntityMaterialized(crdt.NewState("u"), &leapmuxv1.EntityMaterialized{
				Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: nil},
			})
			crdt.ApplyEntityMaterialized(crdt.NewState("u"), &leapmuxv1.EntityMaterialized{
				Entity: &leapmuxv1.EntityMaterialized_Node{Node: nil},
			})
			crdt.ApplyEntityMaterialized(crdt.NewState("u"), &leapmuxv1.EntityMaterialized{
				Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: nil},
			})
		})
	})

	t.Run("deep-clones, inserts, and replaces every entity kind and ignores blank ids", func(t *testing.T) {
		// Start from a bare struct with nil maps to verify lazy map allocation.
		state := &leapmuxv1.UserCrdtState{UserId: "u"}

		tab1 := &leapmuxv1.TabRecord{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
		tab1Updated := &leapmuxv1.TabRecord{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: tab1},
		})
		assert.True(t, proto.Equal(tab1, state.Tabs["tab-1"]))
		assert.NotSame(t, tab1, state.Tabs["tab-1"])
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: tab1Updated},
		})
		assert.True(t, proto.Equal(tab1Updated, state.Tabs["tab-1"]))
		assert.NotSame(t, tab1Updated, state.Tabs["tab-1"])
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: &leapmuxv1.TabRecord{}},
		})
		assert.Len(t, state.Tabs, 1)

		node1 := &leapmuxv1.NodeRecord{NodeId: "node-1"}
		node1Updated := &leapmuxv1.NodeRecord{NodeId: "node-1", ParentId: "root-1"}
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Node{Node: node1},
		})
		assert.True(t, proto.Equal(node1, state.Nodes["node-1"]))
		assert.NotSame(t, node1, state.Nodes["node-1"])
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Node{Node: node1Updated},
		})
		assert.True(t, proto.Equal(node1Updated, state.Nodes["node-1"]))
		assert.NotSame(t, node1Updated, state.Nodes["node-1"])
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Node{Node: &leapmuxv1.NodeRecord{}},
		})
		assert.Len(t, state.Nodes, 1)

		win1 := &leapmuxv1.FloatingWindowRecord{WindowId: "win-1"}
		win1Updated := &leapmuxv1.FloatingWindowRecord{WindowId: "win-1", RootNodeId: "froot-1"}
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: win1},
		})
		assert.True(t, proto.Equal(win1, state.FloatingWindows["win-1"]))
		assert.NotSame(t, win1, state.FloatingWindows["win-1"])
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: win1Updated},
		})
		assert.True(t, proto.Equal(win1Updated, state.FloatingWindows["win-1"]))
		assert.NotSame(t, win1Updated, state.FloatingWindows["win-1"])
		crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: &leapmuxv1.FloatingWindowRecord{}},
		})
		assert.Len(t, state.FloatingWindows, 1)
	})

	t.Run("crdt.Apply does not mutate source EntityMaterialized messages", func(t *testing.T) {
		hlc := func(p int64) *leapmuxv1.HLC { return &leapmuxv1.HLC{Physical: p, ClientId: "hub"} }
		state := crdt.NewState("u")

		tabMsg := &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Tab{
				Tab: &leapmuxv1.TabRecord{
					TabId:    "tab-1",
					TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
					TileId:   &leapmuxv1.LWWString{Value: "node-1", Hlc: hlc(1)},
					WorkerId: &leapmuxv1.LWWString{Value: "w-1", Hlc: hlc(1)},
				},
			},
		}
		nodeMsg := &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Node{
				Node: &leapmuxv1.NodeRecord{
					NodeId:   "node-1",
					Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlc(1)},
					Position: &leapmuxv1.LWWString{Value: "a0", Hlc: hlc(1)},
				},
			},
		}
		winMsg := &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{
				FloatingWindow: &leapmuxv1.FloatingWindowRecord{
					WindowId:    "win-1",
					WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1", Hlc: hlc(1)},
					Width:       &leapmuxv1.LWWDouble{Value: 400, Hlc: hlc(1)},
				},
			},
		}

		wantTab := proto.Clone(tabMsg).(*leapmuxv1.EntityMaterialized)
		wantNode := proto.Clone(nodeMsg).(*leapmuxv1.EntityMaterialized)
		wantWin := proto.Clone(winMsg).(*leapmuxv1.EntityMaterialized)

		crdt.ApplyEntityMaterialized(state, tabMsg)
		crdt.ApplyEntityMaterialized(state, nodeMsg)
		crdt.ApplyEntityMaterialized(state, winMsg)

		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(10),
			Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Tab:   &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-1"},
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "node-9"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(11),
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: "node-1",
				Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root-9"},
			}},
		})
		crdt.Apply(state, &leapmuxv1.CrdtOp{
			CanonicalHlc: hlc(12),
			Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				WindowId: "win-1",
				Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Width{Width: 800},
			}},
		})

		// Confirm mutations applied to state.
		assert.Equal(t, "node-9", state.Tabs["tab-1"].GetTileId().GetValue())
		assert.Equal(t, "root-9", state.Nodes["node-1"].GetParentId())
		assert.Equal(t, float64(800), state.FloatingWindows["win-1"].GetWidth().GetValue())

		// Confirm caller-owned EntityMaterialized messages are untouched.
		assert.True(t, proto.Equal(wantTab, tabMsg))
		assert.True(t, proto.Equal(wantNode, nodeMsg))
		assert.True(t, proto.Equal(wantWin, winMsg))
	})
}

func TestApplyEntityRemoved(t *testing.T) {
	t.Run("nil state or nil message or nil maps do not panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			crdt.ApplyEntityRemoved(nil, &leapmuxv1.EntityRemoved{})
			crdt.ApplyEntityRemoved(crdt.NewState("u"), nil)
			crdt.ApplyEntityRemoved(&leapmuxv1.UserCrdtState{}, &leapmuxv1.EntityRemoved{
				Entity: &leapmuxv1.EntityRemoved_Tab{Tab: &leapmuxv1.TabIdent{TabId: "tab-1"}},
			})
			crdt.ApplyEntityRemoved(&leapmuxv1.UserCrdtState{}, &leapmuxv1.EntityRemoved{
				Entity: &leapmuxv1.EntityRemoved_NodeId{NodeId: "node-1"},
			})
			crdt.ApplyEntityRemoved(&leapmuxv1.UserCrdtState{}, &leapmuxv1.EntityRemoved{
				Entity: &leapmuxv1.EntityRemoved_WindowId{WindowId: "win-1"},
			})
		})
	})

	t.Run("removes each entity kind without affecting siblings", func(t *testing.T) {
		state := crdt.NewState("u")
		state.Tabs["tab-1"] = &leapmuxv1.TabRecord{TabId: "tab-1"}
		state.Tabs["tab-2"] = &leapmuxv1.TabRecord{TabId: "tab-2"}
		state.Nodes["node-1"] = &leapmuxv1.NodeRecord{NodeId: "node-1"}
		state.Nodes["node-2"] = &leapmuxv1.NodeRecord{NodeId: "node-2"}
		state.FloatingWindows["win-1"] = &leapmuxv1.FloatingWindowRecord{WindowId: "win-1"}
		state.FloatingWindows["win-2"] = &leapmuxv1.FloatingWindowRecord{WindowId: "win-2"}

		crdt.ApplyEntityRemoved(state, &leapmuxv1.EntityRemoved{
			Entity: &leapmuxv1.EntityRemoved_Tab{Tab: &leapmuxv1.TabIdent{TabId: "tab-1"}},
		})
		crdt.ApplyEntityRemoved(state, &leapmuxv1.EntityRemoved{
			Entity: &leapmuxv1.EntityRemoved_NodeId{NodeId: "node-1"},
		})
		crdt.ApplyEntityRemoved(state, &leapmuxv1.EntityRemoved{
			Entity: &leapmuxv1.EntityRemoved_WindowId{WindowId: "win-1"},
		})

		assert.NotContains(t, state.Tabs, "tab-1")
		assert.Contains(t, state.Tabs, "tab-2")
		assert.NotContains(t, state.Nodes, "node-1")
		assert.Contains(t, state.Nodes, "node-2")
		assert.NotContains(t, state.FloatingWindows, "win-1")
		assert.Contains(t, state.FloatingWindows, "win-2")
	})
}

func TestOpAffectsTabProjection(t *testing.T) {
	tests := []struct {
		name string
		op   *leapmuxv1.CrdtOp
		want bool
	}{
		{name: "nil op", op: nil, want: false},
		{name: "empty body", op: &leapmuxv1.CrdtOp{}, want: false},

		// SetTabRegisterOp fields
		{
			name: "tab tile_id affects projection",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "leaf-1"},
			}}},
			want: true,
		},
		{
			name: "tab worker_id affects projection",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "worker-1"},
			}}},
			want: true,
		},
		{
			name: "tab position is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a0"},
			}}},
			want: false,
		},
		{
			name: "tab display_mode is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_DisplayMode{DisplayMode: 1},
			}}},
			want: false,
		},
		{
			name: "tab file_view_mode is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_FileViewMode{FileViewMode: 2},
			}}},
			want: false,
		},
		{
			name: "tab file_diff_base is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_FileDiffBase{FileDiffBase: "HEAD"},
			}}},
			want: false,
		},
		{
			name: "tab nil field is layout-only",
			op:   &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{}}},
			want: false,
		},

		// Lifecycle and tombstone ops
		{name: "tombstone tab", op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{}}}, want: true},
		{name: "revive tab", op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_ReviveTab{ReviveTab: &leapmuxv1.ReviveTabOp{}}}, want: true},
		{name: "create workspace", op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_CreateWorkspace{CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{}}}, want: true},
		{name: "tombstone workspace", op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{}}}, want: true},
		{name: "tombstone node", op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneNode{TombstoneNode: &leapmuxv1.TombstoneNodeOp{}}}, want: true},
		{name: "tombstone floating window", op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneFloatingWindow{TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{}}}, want: true},

		// SetNodeRegisterOp fields
		{
			name: "node parent_id affects projection",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root-1"},
			}}},
			want: true,
		},
		{
			name: "node kind is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
			}}},
			want: false,
		},
		{
			name: "node position is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: "a0"},
			}}},
			want: false,
		},
		{
			name: "node direction is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_Direction{Direction: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL},
			}}},
			want: false,
		},
		{
			name: "node ratios is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}},
			}}},
			want: false,
		},
		{
			name: "node rows is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_Rows{Rows: 2},
			}}},
			want: false,
		},
		{
			name: "node cols is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_Cols{Cols: 2},
			}}},
			want: false,
		},
		{
			name: "node row_ratios is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_RowRatios{RowRatios: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}},
			}}},
			want: false,
		},
		{
			name: "node col_ratios is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				Field: &leapmuxv1.SetNodeRegisterOp_ColRatios{ColRatios: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}},
			}}},
			want: false,
		},

		// SetFloatingWindowRegisterOp fields
		{
			name: "floating window root_node_id affects projection",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId{RootNodeId: "froot-1"},
			}}},
			want: true,
		},
		{
			name: "floating window workspace_id affects projection",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId{WorkspaceId: "ws-2"},
			}}},
			want: true,
		},
		{
			name: "floating window x is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_X{X: 100},
			}}},
			want: false,
		},
		{
			name: "floating window y is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_Y{Y: 200},
			}}},
			want: false,
		},
		{
			name: "floating window width is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_Width{Width: 640},
			}}},
			want: false,
		},
		{
			name: "floating window height is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_Height{Height: 480},
			}}},
			want: false,
		},
		{
			name: "floating window opacity is layout-only",
			op: &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{
				Field: &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: 0.9},
			}}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, crdt.OpAffectsTabProjection(tc.op))
		})
	}
}

func TestDiffOwnedTabs(t *testing.T) {
	tab := func(id, ws, worker, tile string) crdt.OwnedTab {
		return crdt.OwnedTab{
			UserID:      "u-1",
			WorkspaceID: ws,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       id,
			WorkerID:    worker,
			TileID:      tile,
			Position:    "a0",
		}
	}

	t.Run("empty prev and next returns nil", func(t *testing.T) {
		assert.Nil(t, crdt.DiffOwnedTabs(crdt.NewState("u-1"), nil, nil, false))
		assert.Nil(t, crdt.DiffOwnedTabs(crdt.NewState("u-1"), map[string]crdt.OwnedTab{}, map[string]crdt.OwnedTab{}, true))
	})

	t.Run("unchanged workspace emits no transition even when tile or worker changes", func(t *testing.T) {
		prev := map[string]crdt.OwnedTab{"t-1": tab("t-1", "ws-1", "w-1", "leaf-1")}
		next := map[string]crdt.OwnedTab{"t-1": tab("t-1", "ws-1", "w-2", "leaf-2")}
		assert.Empty(t, crdt.DiffOwnedTabs(crdt.NewState("u-1"), prev, next, false))
	})

	t.Run("opened tab sets Current and Reconciled", func(t *testing.T) {
		opened := tab("t-1", "ws-1", "w-1", "leaf-1")
		gotLive := crdt.DiffOwnedTabs(crdt.NewState("u-1"), nil, map[string]crdt.OwnedTab{"t-1": opened}, false)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionOpened,
			TabID:      "t-1",
			Current:    opened,
			Reconciled: false,
		}}, gotLive)

		gotReconciled := crdt.DiffOwnedTabs(crdt.NewState("u-1"), nil, map[string]crdt.OwnedTab{"t-1": opened}, true)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionOpened,
			TabID:      "t-1",
			Current:    opened,
			Reconciled: true,
		}}, gotReconciled)
	})

	t.Run("closed tab via tombstone keeps record in state and reports closed", func(t *testing.T) {
		state := crdt.NewState("u-1")
		state.Tabs["t-1"] = &leapmuxv1.TabRecord{
			TabId:       "t-1",
			TombstoneAt: &leapmuxv1.HLC{Physical: 100},
		}
		prev := map[string]crdt.OwnedTab{"t-1": tab("t-1", "ws-1", "w-1", "leaf-1")}
		got := crdt.DiffOwnedTabs(state, prev, nil, false)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionClosed,
			TabID:      "t-1",
			Previous:   prev["t-1"],
			Reconciled: false,
		}}, got)
	})

	t.Run("orphaned tab whose tile disappeared keeps live record in state and reports closed", func(t *testing.T) {
		state := crdt.NewState("u-1")
		state.Tabs["t-1"] = &leapmuxv1.TabRecord{TabId: "t-1"}
		prev := map[string]crdt.OwnedTab{"t-1": tab("t-1", "ws-1", "w-1", "leaf-gone")}
		got := crdt.DiffOwnedTabs(state, prev, nil, false)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionClosed,
			TabID:      "t-1",
			Previous:   prev["t-1"],
			Reconciled: false,
		}}, got)
	})

	t.Run("removed tab absent from state during live batch reports left scope", func(t *testing.T) {
		state := crdt.NewState("u-1")
		prev := map[string]crdt.OwnedTab{"t-1": tab("t-1", "ws-1", "w-1", "leaf-1")}
		got := crdt.DiffOwnedTabs(state, prev, nil, false)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionLeftScope,
			TabID:      "t-1",
			Previous:   prev["t-1"],
			Current:    crdt.OwnedTab{},
			Reconciled: false,
		}}, got)
	})

	t.Run("missing tab during post-gap reconciliation reports closed even without record", func(t *testing.T) {
		prev := map[string]crdt.OwnedTab{"t-1": tab("t-1", "ws-1", "w-1", "leaf-1")}
		got := crdt.DiffOwnedTabs(nil, prev, nil, true)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionClosed,
			TabID:      "t-1",
			Previous:   prev["t-1"],
			Reconciled: true,
		}}, got)
	})

	t.Run("moved between watched workspaces reports Previous, Current, and Reconciled", func(t *testing.T) {
		before := tab("t-1", "ws-1", "w-1", "leaf-1")
		after := tab("t-1", "ws-2", "w-1", "leaf-2")
		got := crdt.DiffOwnedTabs(crdt.NewState("u-1"),
			map[string]crdt.OwnedTab{"t-1": before},
			map[string]crdt.OwnedTab{"t-1": after},
			true,
		)
		assert.Equal(t, []crdt.TabTransition{{
			Kind:       crdt.TabTransitionMoved,
			TabID:      "t-1",
			Previous:   before,
			Current:    after,
			Reconciled: true,
		}}, got)
	})

	t.Run("deterministic sorted tab ID ordering across mixed transitions", func(t *testing.T) {
		state := crdt.NewState("u-1")
		state.Tabs["tab-b"] = &leapmuxv1.TabRecord{TabId: "tab-b", TombstoneAt: &leapmuxv1.HLC{Physical: 50}}
		prev := map[string]crdt.OwnedTab{
			"tab-d": tab("tab-d", "ws-1", "w-1", "leaf-1"), // left scope (not in state.Tabs)
			"tab-b": tab("tab-b", "ws-1", "w-1", "leaf-1"), // closed (tombstone in state.Tabs)
			"tab-c": tab("tab-c", "ws-1", "w-1", "leaf-1"), // moved within scope
			"tab-e": tab("tab-e", "ws-1", "w-1", "leaf-1"), // unchanged
		}
		next := map[string]crdt.OwnedTab{
			"tab-a": tab("tab-a", "ws-1", "w-1", "leaf-1"), // opened
			"tab-c": tab("tab-c", "ws-2", "w-1", "leaf-2"), // moved within scope
			"tab-e": tab("tab-e", "ws-1", "w-1", "leaf-1"), // unchanged
		}

		got := crdt.DiffOwnedTabs(state, prev, next, false)
		require.Len(t, got, 4)
		assert.Equal(t, []string{"tab-a", "tab-b", "tab-c", "tab-d"},
			[]string{got[0].TabID, got[1].TabID, got[2].TabID, got[3].TabID})
		assert.Equal(t, crdt.TabTransitionOpened, got[0].Kind)
		assert.Equal(t, crdt.TabTransitionClosed, got[1].Kind)
		assert.Equal(t, crdt.TabTransitionMoved, got[2].Kind)
		assert.Equal(t, "ws-2", got[2].Current.WorkspaceID)
		assert.Equal(t, crdt.TabTransitionLeftScope, got[3].Kind)
		assert.Empty(t, got[3].Current.WorkspaceID)
	})

	t.Run("zero-change diff returns nil with zero allocations and single-change diff is deterministic", func(t *testing.T) {
		state := crdt.NewState("u-1")
		prev := map[string]crdt.OwnedTab{
			"tab-1": tab("tab-1", "ws-1", "w-1", "leaf-1"),
			"tab-2": tab("tab-2", "ws-1", "w-1", "leaf-2"),
			"tab-3": tab("tab-3", "ws-2", "w-2", "leaf-3"),
		}
		same := map[string]crdt.OwnedTab{
			"tab-1": tab("tab-1", "ws-1", "w-1", "leaf-1"),
			"tab-2": tab("tab-2", "ws-1", "w-1", "leaf-2"),
			"tab-3": tab("tab-3", "ws-2", "w-2", "leaf-3"),
		}
		assert.Nil(t, crdt.DiffOwnedTabs(state, prev, same, false))
		allocs := testing.AllocsPerRun(50, func() {
			if got := crdt.DiffOwnedTabs(state, prev, same, false); got != nil {
				t.Fatalf("expected nil diff, got %v", got)
			}
		})
		assert.Zero(t, allocs, "zero-change diff must not allocate")

		singleChange := map[string]crdt.OwnedTab{
			"tab-1": tab("tab-1", "ws-1", "w-1", "leaf-1"),
			"tab-2": tab("tab-2", "ws-2", "w-1", "leaf-9"),
			"tab-3": tab("tab-3", "ws-2", "w-2", "leaf-3"),
		}
		want := []crdt.TabTransition{{
			Kind:     crdt.TabTransitionMoved,
			TabID:    "tab-2",
			Previous: prev["tab-2"],
			Current:  singleChange["tab-2"],
		}}
		for range 10 {
			assert.Equal(t, want, crdt.DiffOwnedTabs(state, prev, singleChange, false))
		}
	})
}

func TestClientStateLifecycleProjectionAndDiff(t *testing.T) {
	projectOwned := func(state *leapmuxv1.UserCrdtState) map[string]crdt.OwnedTab {
		out := map[string]crdt.OwnedTab{}
		for _, row := range crdt.Project(state).OwnedTabs {
			out[row.TabID] = *row
		}
		return out
	}

	hlc := func(p int64) *leapmuxv1.HLC { return &leapmuxv1.HLC{Physical: p, ClientId: "hub"} }

	snapshot := &leapmuxv1.UserMaterialized{
		UserId: "u-1",
		Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
			"ws-1": {WorkspaceId: "ws-1", RootNodeId: "root-1"},
			"ws-2": {WorkspaceId: "ws-2", RootNodeId: "root-2"},
		},
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"root-1": {NodeId: "root-1", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlc(1)}},
			"root-2": {NodeId: "root-2", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlc(1)}},
		},
		Tabs: map[string]*leapmuxv1.TabRecord{
			"tab-1": {
				TabId:    "tab-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				TileId:   &leapmuxv1.LWWString{Value: "root-1", Hlc: hlc(1)},
				WorkerId: &leapmuxv1.LWWString{Value: "w-1", Hlc: hlc(1)},
			},
		},
		MaxHlc:       hlc(1),
		CurrentEpoch: 1,
	}

	state := crdt.StateFromMaterialized(snapshot)
	prev := projectOwned(state)
	require.Contains(t, prev, "tab-1")

	// 1. EntityMaterialized brings a new tab into scope.
	crdt.ApplyEntityMaterialized(state, &leapmuxv1.EntityMaterialized{
		Entity: &leapmuxv1.EntityMaterialized_Tab{
			Tab: &leapmuxv1.TabRecord{
				TabId:    "tab-2",
				TabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
				TileId:   &leapmuxv1.LWWString{Value: "root-1", Hlc: hlc(2)},
				WorkerId: &leapmuxv1.LWWString{Value: "w-1", Hlc: hlc(2)},
			},
		},
	})
	next := projectOwned(state)
	transitions := crdt.DiffOwnedTabs(state, prev, next, false)
	require.Len(t, transitions, 1)
	assert.Equal(t, crdt.TabTransitionOpened, transitions[0].Kind)
	assert.Equal(t, "tab-2", transitions[0].TabID)
	prev = next

	// 2. Move tab-1 from ws-1 to ws-2 via SetTabRegisterOp.
	moveOp := &leapmuxv1.CrdtOp{
		CanonicalHlc: hlc(3),
		Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			Tab:   &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-1"},
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root-2"},
		}},
	}
	require.True(t, crdt.OpAffectsTabProjection(moveOp))
	crdt.Apply(state, moveOp)
	next = projectOwned(state)
	transitions = crdt.DiffOwnedTabs(state, prev, next, false)
	require.Len(t, transitions, 1)
	assert.Equal(t, crdt.TabTransitionMoved, transitions[0].Kind)
	assert.Equal(t, "ws-1", transitions[0].Previous.WorkspaceID)
	assert.Equal(t, "ws-2", transitions[0].Current.WorkspaceID)
	prev = next

	// 3. Tombstone tab-1 via TombstoneTabOp -> TabTransitionClosed (record remains).
	closeOp := &leapmuxv1.CrdtOp{
		CanonicalHlc: hlc(4),
		Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
			Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-1"},
		}},
	}
	require.True(t, crdt.OpAffectsTabProjection(closeOp))
	crdt.Apply(state, closeOp)
	next = projectOwned(state)
	transitions = crdt.DiffOwnedTabs(state, prev, next, false)
	require.Len(t, transitions, 1)
	assert.Equal(t, crdt.TabTransitionClosed, transitions[0].Kind)
	assert.Equal(t, "tab-1", transitions[0].TabID)
	prev = next

	// 4. EntityRemoved removes tab-2 from scope -> TabTransitionLeftScope with empty Current.
	crdt.ApplyEntityRemoved(state, &leapmuxv1.EntityRemoved{
		Entity: &leapmuxv1.EntityRemoved_Tab{Tab: &leapmuxv1.TabIdent{TabId: "tab-2"}},
	})
	next = projectOwned(state)
	transitions = crdt.DiffOwnedTabs(state, prev, next, false)
	require.Len(t, transitions, 1)
	assert.Equal(t, crdt.TabTransitionLeftScope, transitions[0].Kind)
	assert.Equal(t, "tab-2", transitions[0].TabID)
	assert.Equal(t, "ws-1", transitions[0].Previous.WorkspaceID)
	assert.Empty(t, transitions[0].Current.WorkspaceID)
}

func TestTabTransitionKindString(t *testing.T) {
	assert.Equal(t, "opened", crdt.TabTransitionOpened.String())
	assert.Equal(t, "closed", crdt.TabTransitionClosed.String())
	assert.Equal(t, "moved", crdt.TabTransitionMoved.String())
	assert.Equal(t, "left_scope", crdt.TabTransitionLeftScope.String())
	assert.Equal(t, "TabTransitionKind(0)", crdt.TabTransitionKind(0).String())
}

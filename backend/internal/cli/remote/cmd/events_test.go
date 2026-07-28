package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestOpKindName_ClassifiesEveryCrdtOpBody pins that `events watch` renders
// a snake_case label for every CrdtOp body kind. A new op body that the hub
// broadcasts (e.g. the hub-only SetWorkspaceRegister / TombstoneWorkspace
// lifecycle ops) must get a real label here, not "unknown" — operators
// filter `events watch` JSON by `type`, so an "unknown" label silently
// breaks scripts watching the lifecycle path.
func TestOpKindName_ClassifiesEveryCrdtOpBody(t *testing.T) {
	cases := []struct {
		name string
		op   *leapmuxv1.CrdtOp
		want string
	}{
		{"SetNodeRegister", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{}}}, "set_node_register"},
		{"TombstoneNode", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneNode{TombstoneNode: &leapmuxv1.TombstoneNodeOp{}}}, "tombstone_node"},
		{"SetTabRegister", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{}}}, "set_tab_register"},
		{"TombstoneTab", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{}}}, "tombstone_tab"},
		{"SetFloatingWindowRegister", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: &leapmuxv1.SetFloatingWindowRegisterOp{}}}, "set_floating_window_register"},
		{"TombstoneFloatingWindow", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneFloatingWindow{TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{}}}, "tombstone_floating_window"},
		{"SetWorkspaceRootNode", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{}}}, "set_workspace_root_node"},
		// The two hub-only lifecycle ops added when workspace create/delete
		// moved onto the serialized submit pipeline — now broadcast to
		// subscribers, so `events watch` must label them, not "unknown".
		{"SetWorkspaceRegister", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetWorkspaceRegister{SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{}}}, "set_workspace_register"},
		{"TombstoneWorkspace", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{}}}, "tombstone_workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, opKindName(tc.op))
		})
	}
}

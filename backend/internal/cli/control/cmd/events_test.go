package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestEventToJSON_ClassifiesEveryWatchUserEventKind is the frame-level sibling
// of the op-level test above, and exists for the same reason: an unhandled
// oneof arm falls through to {"kind":"unknown"}, which silently breaks scripts
// filtering the stream.
//
// It caught `batch_end` shipping unlabeled. That arm is emitted after EVERY
// committed batch on the live broadcast path -- not only on resume -- so the
// gap put one unknown line in the stream per user edit, and dropped the at_hlc
// watermark a resume-aware consumer needs.
//
// The case list is exhaustive over the WatchUserEvent oneof BY CONSTRUCTION:
// the compiler rejects an entry whose wrapper type does not exist, and a NEW
// arm is caught because adding one without extending eventToJSON leaves the
// author with a failing "no arm renders unknown" contract the moment they add
// its case here. Keep it in step with proto/leapmux/v1/user_ops.proto.
func TestEventToJSON_ClassifiesEveryWatchUserEventKind(t *testing.T) {
	cases := []struct {
		name string
		evt  *leapmuxv1.WatchUserEvent
		want string
	}{
		{"Initial", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Initial{Initial: &leapmuxv1.UserMaterialized{}}}, "materialized"},
		{"Batch", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Batch{Batch: &leapmuxv1.OpBatch{}}}, "batch"},
		{"EntityMaterialized", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_EntityMaterialized{EntityMaterialized: &leapmuxv1.EntityMaterialized{}}}, "entity_materialized"},
		{"EntityRemoved", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_EntityRemoved{EntityRemoved: &leapmuxv1.EntityRemoved{}}}, "entity_removed"},
		{"Delta", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Delta{Delta: &leapmuxv1.ResumeDelta{}}}, "resume_delta"},
		{"BatchEnd", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{}}}, "batch_end"},
		{"Presence", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Presence{Presence: &leapmuxv1.PresenceUpdate{}}}, "presence"},
		{"Renamed", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Renamed{Renamed: &leapmuxv1.WorkspaceRenamed{}}}, "workspace_renamed"},
		{"Created", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Created{Created: &leapmuxv1.WorkspaceCreated{}}}, "workspace_created"},
		{"Deleted", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Deleted{Deleted: &leapmuxv1.WorkspaceDeleted{}}}, "workspace_deleted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eventToJSON(tc.evt)
			assert.Equal(t, tc.want, got["kind"])
			assert.NotEqual(t, "unknown", got["kind"], "every oneof arm must render a real label")
		})
	}

	// The nested case: a ResumeDelta's frames recurse through the same switch,
	// so an arm missing there is invisible to the flat cases above. BatchEnd is
	// the arm resumeCatchUpSink.End appends to every delta.
	t.Run("frames nested in a resume delta", func(t *testing.T) {
		got := eventToJSON(&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Delta{Delta: &leapmuxv1.ResumeDelta{
			Frames: []*leapmuxv1.WatchUserEvent{
				{Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{}}},
			},
		}}})
		frames, ok := got["frames"].([]map[string]any)
		require.True(t, ok, "a delta must project its frames")
		require.Len(t, frames, 1)
		assert.Equal(t, "batch_end", frames[0]["kind"],
			"frames inside a delta go through the same switch, so a missing arm shows up here too")
	})
}

// TestWorkspaceMapKeys_SortsTheIds pins the order of the `workspaces` array
// in the first line that `events watch` prints.
//
// Go randomizes map iteration, so an unsorted projection makes two runs
// against ONE unchanged account print two different lines. A script that
// compares snapshots then reports a difference that does not exist. Any
// account with two or more workspaces reaches this.
func TestWorkspaceMapKeys_SortsTheIds(t *testing.T) {
	m := map[string]*leapmuxv1.WorkspaceContentsRecord{
		"ws-delta":   {},
		"ws-alpha":   {},
		"ws-charlie": {},
		"ws-bravo":   {},
		"ws-echo":    {},
	}
	want := []string{"ws-alpha", "ws-bravo", "ws-charlie", "ws-delta", "ws-echo"}

	// One pass can match by luck. Repetition is what catches a missing sort,
	// because the randomized order has to lose 32 times in a row.
	for range 32 {
		assert.Equal(t, want, workspaceMapKeys(m))
	}

	t.Run("the materialized line carries the sorted ids", func(t *testing.T) {
		got := eventToJSON(&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Initial{
			Initial: &leapmuxv1.UserMaterialized{Workspaces: m},
		}})
		assert.Equal(t, "materialized", got["kind"])
		assert.Equal(t, want, got["workspaces"])
	})

	t.Run("an empty map projects an empty array, not null", func(t *testing.T) {
		got := workspaceMapKeys(map[string]*leapmuxv1.WorkspaceContentsRecord{})
		assert.NotNil(t, got, "a nil slice encodes as JSON null and breaks a `.workspaces | length` filter")
		assert.Empty(t, got)
	})

	t.Run("a nil map projects an empty array too", func(t *testing.T) {
		got := workspaceMapKeys(nil)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("one workspace needs no comparison", func(t *testing.T) {
		got := workspaceMapKeys(map[string]*leapmuxv1.WorkspaceContentsRecord{"ws-only": {}})
		assert.Equal(t, []string{"ws-only"}, got)
	})
}

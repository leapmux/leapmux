package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// TestOpKindName_ClassifiesEveryCrdtOpBody pins that `layout watch` renders
// a snake_case label for every CrdtOp body kind. A new op body that the hub
// broadcasts (e.g. the hub-only CreateWorkspace / TombstoneWorkspace
// lifecycle ops) must get a real label here, not "unknown" — operators
// filter `layout watch` JSON by `type`, so an "unknown" label silently
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
		{"ReviveTab", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_ReviveTab{ReviveTab: &leapmuxv1.ReviveTabOp{}}}, "revive_tab"},
		// The two hub-only lifecycle ops that carry workspace create/delete on
		// the serialized submit pipeline — broadcast to subscribers, so
		// `layout watch` must label them, not "unknown".
		{"CreateWorkspace", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_CreateWorkspace{CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{}}}, "create_workspace"},
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
		{"WorkersChanged", &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_WorkersChanged{WorkersChanged: &leapmuxv1.WorkersChanged{}}}, "workers_changed"},
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
// in the first line that `layout watch` prints.
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

// TestOpTargetSummary_PrintsTheCLITabTypeToken pins how `layout watch` identifies
// the tab that an op targets. Every tab op prints the CLI's own token
// ("agent", "terminal"), the same one the removal and materialize lines
// print, so one tab reads the same in every line of the stream. The shared
// target JSON alone would print the proto enum name instead. An op that
// targets no tab prints no tab_type at all.
func TestOpTargetSummary_PrintsTheCLITabTypeToken(t *testing.T) {
	agent := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}
	terminal := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1"}
	tabCases := []struct {
		name string
		op   *leapmuxv1.CrdtOp
		tab  *leapmuxv1.TabIdent
		kind string
	}{
		{"set_tab_register", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: agent}}}, agent, "set_tab_register"},
		{"tombstone_tab", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: terminal}}}, terminal, "tombstone_tab"},
		{"revive_tab", &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_ReviveTab{ReviveTab: &leapmuxv1.ReviveTabOp{Tab: agent}}}, agent, "revive_tab"},
	}
	for _, tc := range tabCases {
		t.Run(tc.name, func(t *testing.T) {
			got := opTargetSummary(tc.op)
			removed := removedEntitySummary(&leapmuxv1.EntityRemoved{Entity: &leapmuxv1.EntityRemoved_Tab{Tab: tc.tab}})
			assert.Equal(t, map[string]any{
				"tab_id":   tc.tab.GetTabId(),
				"tab_type": resolve.TabTypeWireName(tc.tab.GetTabType()),
				"type":     tc.kind,
			}, got)
			assert.Equal(t, removed["tab_type"], got["tab_type"], "the op line and the removal line must state the tab type alike")
			assert.NotEqual(t, tc.tab.GetTabType().String(), got["tab_type"], "the proto enum name must not leak into the stream")
		})
	}

	t.Run("an op on a node prints no tab type", func(t *testing.T) {
		got := opTargetSummary(&leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{NodeId: "node-1"}}})
		assert.Equal(t, map[string]any{"node_id": "node-1", "type": "set_node_register"}, got)
	})
	t.Run("an op on a workspace prints no tab type", func(t *testing.T) {
		got := opTargetSummary(&leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_CreateWorkspace{CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{WorkspaceId: "ws-1"}}})
		assert.Equal(t, map[string]any{"workspace_id": "ws-1", "type": "create_workspace"}, got)
	})
}

func TestLayoutWatchEvent_TextLineAndTSV(t *testing.T) {
	cases := []struct {
		name string
		evt  *leapmuxv1.WatchUserEvent
		want string
	}{
		{
			"materialized",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Initial{Initial: &leapmuxv1.UserMaterialized{
				CurrentEpoch: 4,
				Workspaces:   map[string]*leapmuxv1.WorkspaceContentsRecord{"ws-1": {}, "ws-2": {}},
				MaxHlc:       &leapmuxv1.HLC{Physical: 100, Logical: 2, ClientId: "c1"},
			}}},
			"materialized workspaces=2 epoch=4",
		},
		{
			"batch with ops",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Batch{Batch: &leapmuxv1.OpBatch{
				BatchId: "batch-1234567890",
				Ops: []*leapmuxv1.CrdtOp{
					{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{}}},
				},
			}}},
			"batch batch-12 (1 ops: set_tab_register)",
		},
		{
			"entity materialized tab",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_EntityMaterialized{EntityMaterialized: &leapmuxv1.EntityMaterialized{
				Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: &leapmuxv1.TabRecord{TabId: "tab-1234567890"}},
			}}},
			"entity_materialized tab tab-1234",
		},
		{
			"entity removed node",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_EntityRemoved{EntityRemoved: &leapmuxv1.EntityRemoved{
				Entity: &leapmuxv1.EntityRemoved_NodeId{NodeId: "node-1234567890"},
			}}},
			"entity_removed node node-123",
		},
		{
			"resume delta",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Delta{Delta: &leapmuxv1.ResumeDelta{
				CurrentEpoch: 9,
				Frames: []*leapmuxv1.WatchUserEvent{
					{Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{}}},
				},
			}}},
			"resume_delta frames=1 epoch=9",
		},
		{
			"batch end with HLC",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{
				AtHlc: &leapmuxv1.HLC{Physical: 500, Logical: 3},
			}}},
			"batch_end hlc=500:3",
		},
		{
			"batch end without HLC",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{}}},
			"batch_end",
		},
		{
			"workspace created",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Created{Created: &leapmuxv1.WorkspaceCreated{WorkspaceId: "ws-1234567890", Title: "Alpha"}}},
			`workspace_created ws-12345 "Alpha"`,
		},
		{
			"workspace deleted",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Deleted{Deleted: &leapmuxv1.WorkspaceDeleted{WorkspaceId: "ws-1234567890"}}},
			"workspace_deleted ws-12345",
		},
		{
			"presence",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Presence{Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "ws-1234567890", ActiveClientId: "client-999999"}}},
			"presence workspace=ws-12345 client=client-9",
		},
		{
			"workers changed",
			&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_WorkersChanged{WorkersChanged: &leapmuxv1.WorkersChanged{}}},
			"workers_changed",
		},
		{
			"nil event never prints <nil>",
			nil,
			"unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := layoutWatchEvent{event: tc.evt}
			line := ev.TextLine()
			assert.Equal(t, tc.want, line)
			assert.NotContains(t, line, "<nil>")

			var tsvBuf bytes.Buffer
			p := &cli.Printer{Out: &tsvBuf, Err: &bytes.Buffer{}, Format: cli.FormatTSV}
			require.NoError(t, p.Event(ev))
			assert.Equal(t, tc.want+"\n", tsvBuf.String(), "TSV must render readable text rather than dashes for nested fields")

			raw, err := json.Marshal(ev)
			require.NoError(t, err)
			wantJSON, err := json.Marshal(eventToJSON(tc.evt))
			require.NoError(t, err)
			assert.JSONEq(t, string(wantJSON), string(raw))
		})
	}
}

func TestLayoutWatch_ExplicitMissingWorkspaceFailsBeforeSubscribing(t *testing.T) {
	userEvents := newFakeUserEvents()
	hub := &recordingHub{}
	startSpawnIPC(t, hub, newEventsWorker(), func(r *controlipc.Router) {
		r.HubStreams = userEvents
	})
	// recordingHub returns PermissionDenied for GetWorkspace when materialized is nil;
	// verify layout watch rejects id:missing before opening WatchUser.
	o := runControl(t, "layout", "watch", "id:missing", "--json")
	require.NotEqual(t, cli.ExitOK, o.Code)
	select {
	case ids := <-userEvents.workspaceIDs:
		t.Fatalf("layout watch subscribed to WatchUser with %v before verifying the workspace", ids)
	default:
	}

	// Verify NotFound specifically when the Hub answers GetWorkspace with NotFound.
	notFoundHub := &getWorkspaceErrorHub{err: connect.NewError(connect.CodeNotFound, "workspace not found")}
	startSpawnIPC(t, &recordingHub{}, newEventsWorker(), func(r *controlipc.Router) {
		r.Hub = notFoundHub
		r.HubStreams = userEvents
	})
	o2 := runControl(t, "layout", "watch", "id:missing", "--json")
	o2.requireFailure(t, cli.CodeNotFound)
	select {
	case ids := <-userEvents.workspaceIDs:
		t.Fatalf("layout watch subscribed to WatchUser with %v after GetWorkspace failed", ids)
	default:
	}
}

type getWorkspaceErrorHub struct {
	recordingHub
	err error
}

func (h *getWorkspaceErrorHub) CallInner(ctx context.Context, userID userid.UserID, procedure string, payload []byte) ([]byte, error) {
	if procedure == leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure {
		return nil, h.err
	}
	return h.recordingHub.CallInner(ctx, userID, procedure, payload)
}

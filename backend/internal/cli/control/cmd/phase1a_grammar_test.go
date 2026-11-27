package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestPhase1A_CanonicalAliasesResolveInCommandTree(t *testing.T) {
	cases := []struct {
		args      []string
		wantUsage string
	}{
		{[]string{"ag", "ls", "--help"}, "Usage:\n  leapmux agent list [flags]\n"},
		{[]string{"ag", "msg", "--help"}, "Usage:\n  leapmux agent messages [agent] [flags]\n"},
		{[]string{"ag", "req", "--help"}, "Usage:\n  leapmux agent requests [agent]... [flags]\n"},
		{[]string{"ag", "q", "ls", "--help"}, "Usage:\n  leapmux agent queue list [agent] [flags]\n"},
		{[]string{"ag", "q", "rm", "--help"}, "Usage:\n  leapmux agent queue remove <agent> <input>... [flags]\n"},
		{[]string{"ag", "rm", "--help"}, "Usage:\n  leapmux agent close <agent>... [flags]\n"},
		{[]string{"term", "ls", "--help"}, "Usage:\n  leapmux terminal list [flags]\n"},
		{[]string{"term", "cat", "--help"}, "Usage:\n  leapmux terminal read [terminal] [flags]\n"},
		{[]string{"term", "rm", "--help"}, "Usage:\n  leapmux terminal close <terminal>... [flags]\n"},
		{[]string{"ws", "ls", "--help"}, "Usage:\n  leapmux workspace list [flags]\n"},
		{[]string{"ws", "rm", "--help"}, "Usage:\n  leapmux workspace delete <workspace> [flags]\n"},
		{[]string{"wt", "ls", "--help"}, "Usage:\n  leapmux worktree list [flags]\n"},
		{[]string{"ev", "--help"}, "Usage:\n  leapmux events [flags]\n"},
		{[]string{"notif", "ls", "--help"}, "Usage:\n  leapmux notification list [flags]\n"},
		{[]string{"notif", "rm", "--help"}, "Usage:\n  leapmux notification clear [notification-id]... [flags]\n"},
		{[]string{"tab", "mv", "--help"}, "Usage:\n  leapmux tab move <tab> [flags]\n"},
		{[]string{"tab", "rm", "--help"}, "Usage:\n  leapmux tab close <tab>... [flags]\n"},
		{[]string{"file", "cat", "--help"}, "Usage:\n  leapmux file read <path> [flags]\n"},
		{[]string{"git", "branch", "ls", "--help"}, "Usage:\n  leapmux git branch list [flags]\n"},
		{[]string{"tun", "fwd", "--help"}, "Usage:\n  leapmux tunnel forward <spec> [flags]\n"},
		{[]string{"tun", "nc", "--help"}, "Usage:\n  leapmux tunnel connect <target> [flags]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.wantUsage, func(t *testing.T) {
			o := runControlText(t, tc.args...)
			require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
			assert.Contains(t, o.Stdout, tc.wantUsage)
		})
	}
}

func TestPhase1A_RemovedCommandsAreRejected(t *testing.T) {
	for _, args := range [][]string{
		{"tile", "list"},
		{"git", "branches"},
		{"git", "worktrees"},
		{"git", "worktree", "get"},
		{"admin", "user", "list-sessions"},
	} {
		o := runControl(t, args...)
		assert.Equal(t, cli.ExitUsage, o.Code, "removed command %v must fail with usage", args)
	}
}

type phase1aWorker struct {
	sentNotifications    []*leapmuxv1.NotifyRequest
	clearedNotifications []string
	listedNotifications  []*leapmuxv1.UserNotification
	clearedStatuses      []string
	listedStatuses       []*leapmuxv1.TabStatusEntry
	interrupted          []string
	queuedItems          []*leapmuxv1.QueuedAgentInput
	removedInputs        []string
	inspectedBranches    []*leapmuxv1.InspectBranchDeletionRequest
	listedDirs           []string
	worktreeGetReqs      []*leapmuxv1.GetGitWorktreeRequest
	closedTabs           []string
}

func (w *phase1aWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerPrivateServiceNotifyProcedure:
		var in leapmuxv1.NotifyRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.sentNotifications = append(w.sentNotifications, &in)
		writeProtoResponse(writer, &leapmuxv1.NotifyResponse{Id: "notif-1", Title: in.GetTitle(), Body: in.GetBody()})
	case leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListNotificationsResponse{Notifications: w.listedNotifications})
	case leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure:
		var in leapmuxv1.ClearNotificationRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.clearedNotifications = append(w.clearedNotifications, in.GetId())
		writeProtoResponse(writer, &leapmuxv1.ClearNotificationResponse{Cleared: true})
	case leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListTabStatusesResponse{Statuses: w.listedStatuses})
	case leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure:
		var in leapmuxv1.SetTabStatusRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.clearedStatuses = append(w.clearedStatuses, in.GetKey())
		writeProtoResponse(writer, &leapmuxv1.SetTabStatusResponse{Key: in.GetKey()})
	case leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure:
		var in leapmuxv1.InterruptAgentRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.interrupted = append(w.interrupted, in.GetAgentId())
		writeProtoResponse(writer, &leapmuxv1.InterruptAgentResponse{})
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListAgentInputQueueResponse{
			Snapshot: &leapmuxv1.AgentInputQueueSnapshot{Items: w.queuedItems},
		})
	case leapmuxv1connect.WorkerAgentInputServiceDeleteQueuedAgentInputProcedure:
		var in leapmuxv1.DeleteQueuedAgentInputRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.removedInputs = append(w.removedInputs, in.GetInputId())
		writeProtoResponse(writer, &leapmuxv1.DeleteQueuedAgentInputResponse{})
	case leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure:
		var in leapmuxv1.InspectBranchDeletionRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.inspectedBranches = append(w.inspectedBranches, &in)
		writeProtoResponse(writer, &leapmuxv1.InspectBranchDeletionResponse{
			BranchName: in.GetBranchNameHint(),
			GitState: &leapmuxv1.BranchGitState{
				UnpushedCommitCount: 2,
				DiffAdded:           5,
				DiffDeleted:         1,
				UpstreamExists:      true,
				OriginExists:        true,
				CanPush:             true,
			},
		})
	case leapmuxv1connect.WorkerFileServiceListDirectoryProcedure:
		var in leapmuxv1.ListDirectoryRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.listedDirs = append(w.listedDirs, in.GetPath())
		writeProtoResponse(writer, &leapmuxv1.ListDirectoryResponse{
			Listings: []*leapmuxv1.DirectoryListing{{
				Path:         in.GetPath(),
				TotalEntries: 1,
				Entries: []*leapmuxv1.FileInfo{
					{Name: "README.md", Path: in.GetPath() + "/README.md"},
				},
			}},
		})
	case leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure:
		var in leapmuxv1.GetGitWorktreeRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.worktreeGetReqs = append(w.worktreeGetReqs, &in)
		writeProtoResponse(writer, &leapmuxv1.GetGitWorktreeResponse{
			RepoRoot: "/repo",
			Worktree: &leapmuxv1.GitWorktreeEntry{Path: "/repo", Branch: "main"},
		})
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListAgentsResponse{
			Agents: []*leapmuxv1.AgentInfo{
				{Id: "ag-aaaa1111", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: "ag-bbbb2222", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			},
		})
	case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListTerminalsResponse{})
	case leapmuxv1connect.WorkerAgentServiceRenameAgentProcedure:
		var in leapmuxv1.RenameAgentRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		writeProtoResponse(writer, &leapmuxv1.RenameAgentResponse{Title: in.GetTitle()})
	case leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure:
		var in leapmuxv1.RenameTerminalRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		writeProtoResponse(writer, &leapmuxv1.RenameTerminalResponse{Title: in.GetTitle()})
	case leapmuxv1connect.WorkerTabServiceCloseTabProcedure:
		var in leapmuxv1.CloseTabRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.closedTabs = append(w.closedTabs, in.GetTab().GetTabId())
		writeProtoResponse(writer, &leapmuxv1.CloseTabResponse{})
	default:
		writeProtoResponse(writer, &leapmuxv1.NotifyResponse{})
	}
}

func TestPhase1A_NotificationSendAndVariadicClear(t *testing.T) {
	w := &phase1aWorker{
		listedNotifications: []*leapmuxv1.UserNotification{
			{Id: "n-1", Title: "One"},
			{Id: "n-2", Title: "Two"},
		},
	}
	startSpawnIPC(t, &recordingHub{}, w)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	sent := runControl(t, "notification", "send", "Build done", "--body", "green").Data(t)
	assert.Equal(t, "notif-1", sent["id"])
	require.Len(t, w.sentNotifications, 1)
	assert.Equal(t, "Build done", w.sentNotifications[0].GetTitle())

	// Mutual exclusion and empty errors before any RPC.
	runControl(t, "notification", "clear").requireFailure(t, cli.CodeUsage)
	runControl(t, "notification", "clear", "n-1", "--all").requireFailure(t, cli.CodeUsage)

	// Variadic clear of two IDs.
	batch := runControl(t, "notification", "clear", "n-1", "n-2").Data(t)
	assert.Equal(t, []any{"n-1", "n-2"}, batch["cleared"])
	assert.Equal(t, []string{"n-1", "n-2"}, w.clearedNotifications)

	// Clear --all enumerates and clears every notification.
	w.clearedNotifications = nil
	all := runControl(t, "notification", "clear", "--all").Data(t)
	assert.Equal(t, []any{"n-1", "n-2"}, all["cleared"])
	assert.Equal(t, []string{"n-1", "n-2"}, w.clearedNotifications)
}

func TestPhase1A_GitBranchInspectAndWorktreeDefaultSelf(t *testing.T) {
	w := &phase1aWorker{}
	startSpawnIPC(t, &recordingHub{}, w)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.WorkingDirVariable, "/repo")

	data := runControl(t, "git", "branch", "inspect", "feat/x").Data(t)
	assert.Equal(t, "feat/x", data["branch_name"])
	assert.Equal(t, float64(2), data["unpushed_commits"])
	assert.Equal(t, true, data["can_push"])
	require.Len(t, w.inspectedBranches, 1)
	assert.Equal(t, "/repo", w.inspectedBranches[0].GetPath())

	wt := runControl(t, "worktree", "get").Data(t)
	assert.Equal(t, "/repo", wt["path"])
	assert.Equal(t, "main", wt["branch"])
	require.Len(t, w.worktreeGetReqs, 1)
	assert.True(t, w.worktreeGetReqs[0].GetSelf())
}

func TestPhase1A_ReadOnlyDefaultsToSelfAndWorkingDir(t *testing.T) {
	w := &phase1aWorker{
		listedStatuses: []*leapmuxv1.TabStatusEntry{
			{Key: "build", Status: &leapmuxv1.TabStatus{Text: "Running"}},
			{Key: "lint", Status: &leapmuxv1.TabStatus{Text: "OK"}},
		},
	}
	hub := &recordingHub{
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-self"},
			TileId:      "tile-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
	}
	startSpawnIPC(t, hub, w)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.TabIDVariable, "tab-self")
	t.Setenv(controlenv.WorkingDirVariable, "/work/project")

	// tab status list with no argument defaults to self.
	statusList := runControl(t, "tab", "status", "list").Data(t)
	assert.Equal(t, "tab-self", statusList["id"])

	// tab status clear with variadic keys and --all.
	runControl(t, "tab", "status", "clear", "self", "build", "--all").requireFailure(t, cli.CodeUsage)
	runControl(t, "tab", "status", "clear", "self", "build", "--key", "lint").requireFailure(t, cli.CodeUsage)

	cleared := runControl(t, "tab", "status", "clear", "self", "build", "lint").Data(t)
	assert.Equal(t, []any{"build", "lint"}, cleared["cleared"])
	assert.Equal(t, []string{"build", "lint"}, w.clearedStatuses)

	w.clearedStatuses = nil
	clearedAll := runControl(t, "tab", "status", "clear", "self", "--all").Data(t)
	assert.Equal(t, []any{"build", "lint"}, clearedAll["cleared"])

	// file list with no path defaults to $LEAPMUX_CONTROL_WORKING_DIR.
	files := runControl(t, "file", "list").Data(t)
	listings, ok := files["listings"].([]any)
	require.True(t, ok)
	require.Len(t, listings, 1)
	assert.Equal(t, []string{"/work/project"}, w.listedDirs)
}

func TestPhase1A_SymmetricTypedVerbsAndVariadicTargets(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "tile-1").
		leafNode("tile-1", "", "0|a:").
		tab("ag-aaaa1111", "tile-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("ag-bbbb2222", "tile-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("tm-cccc3333", "tile-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	hub := &recordingHub{
		materialized: state,
		onSubmitOps:  tabCloseHub().onSubmitOps,
	}
	w := &phase1aWorker{
		queuedItems: []*leapmuxv1.QueuedAgentInput{
			{Id: "in-1111", Text: "first", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED},
			{Id: "in-2222", Text: "second", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED},
		},
	}
	startSpawnIPC(t, hub, w)

	// Type enforcement: agent rename/close refuse terminal tabs; terminal rename/close refuse agent tabs.
	runControl(t, "agent", "rename", "tm-cccc3333", "Nope").requireFailure(t, cli.CodeNotFound)
	runControl(t, "terminal", "rename", "ag-aaaa1111", "Nope").requireFailure(t, cli.CodeNotFound)

	// Typed rename succeeds for matching kind.
	renamed := runControl(t, "agent", "rename", "ag-aaaa1111", "New Agent").Data(t)
	assert.Equal(t, "ag-aaaa1111", renamed["id"])
	assert.Equal(t, "New Agent", renamed["title"])
	renamedTerm := runControl(t, "terminal", "rename", "tm-cccc3333", "New Term").Data(t)
	assert.Equal(t, "tm-cccc3333", renamedTerm["id"])
	assert.Equal(t, "New Term", renamedTerm["title"])

	// Variadic agent interrupt.
	interrupted := runControl(t, "agent", "interrupt", "ag-aaaa1111", "ag-bbbb2222").Data(t)
	agents, ok := interrupted["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 2)
	assert.Equal(t, []string{"ag-aaaa1111", "ag-bbbb2222"}, w.interrupted)

	// Variadic agent queue remove deletes back-to-front so removing the head
	// never promotes a later queued input into dispatch before it is deleted.
	removed := runControl(t, "agent", "queue", "remove", "ag-aaaa1111", "in-1111", "in-2222").Data(t)
	assert.Equal(t, []any{"in-1111", "in-2222"}, removed["input_ids"])
	assert.Equal(t, []string{"in-2222", "in-1111"}, w.removedInputs)

	// Variadic agent close.
	closed := runControl(t, "agent", "close", "ag-aaaa1111", "ag-bbbb2222").Data(t)
	closedRows, ok := closed["closed"].([]any)
	require.True(t, ok)
	require.Len(t, closedRows, 2)
	assert.Equal(t, []string{"ag-aaaa1111", "ag-bbbb2222"}, w.closedTabs)
}

func TestPhase1A_VersionTextRendering(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, versionResult{CLI: buildInfo{Formatted: "1.0.0"}}.RenderText(&buf))
	assert.Equal(t, "1.0.0\n", buf.String())

	buf.Reset()
	require.NoError(t, versionResult{
		CLI:    buildInfo{Formatted: "1.0.0"},
		Hub:    &buildInfo{Formatted: "1.0.1"},
		Worker: &buildInfo{Formatted: "1.0.2"},
	}.RenderText(&buf))
	assert.Equal(t, "cli:    1.0.0\nhub:    1.0.1\nworker: 1.0.2\n", buf.String())
}

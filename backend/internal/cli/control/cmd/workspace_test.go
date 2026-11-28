package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// TestRunWorkspaceCleanupFanout_AllSuccess verifies the happy-path
// orchestration: every worker_id from DeleteWorkspace gets a
// CleanupWorkspace, and every entry reports "ok".
func TestRunWorkspaceCleanupFanout_AllSuccess(t *testing.T) {
	var (
		mu     sync.Mutex
		called []string
	)
	caller := func(_ context.Context, workerID string, _ []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		mu.Lock()
		called = append(called, workerID)
		mu.Unlock()
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1", "w2", "w3"), caller)
	assert.Equal(t, "ok", status)
	// Fan-out runs workers in parallel, so observed call order is
	// non-deterministic — assert set-equality, then check the result
	// slice preserves input order (the user-facing contract).
	assert.ElementsMatch(t, []string{"w1", "w2", "w3"}, called)
	require.Len(t, entries, 3)
	for i, wid := range []string{"w1", "w2", "w3"} {
		assert.Equal(t, wid, entries[i].WorkerID)
		assert.Equal(t, "ok", entries[i].Status)
	}
}

// TestRunWorkspaceCleanupFanout_PartialFailure verifies the most
// important user-visible property: when one worker's cleanup fails,
// every other worker's result still lands in the entries slice and
// overall status downgrades to "partial". Without this, a
// deterministic-failure on worker A could silently swallow workers
// B and C.
func TestRunWorkspaceCleanupFanout_PartialFailure(t *testing.T) {
	caller := func(_ context.Context, workerID string, _ []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		if workerID == "w2" {
			return nil, errors.New("network unreachable")
		}
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1", "w2", "w3"), caller)
	assert.Equal(t, "partial", status, "any failure must downgrade overall status to 'partial'")
	require.Len(t, entries, 3)

	assert.Equal(t, "ok", entries[0].Status)
	assert.Equal(t, "failed", entries[1].Status)
	assert.Contains(t, entries[1].Error, "network unreachable")
	assert.Equal(t, "ok", entries[2].Status, "siblings of a failed worker must still surface a result")
}

// TestRunWorkspaceCleanupFanout_AllFailure pins the all-failed case:
// the per-worker entries still get assembled (the user wants to see
// what was attempted) and overall status is "partial".
func TestRunWorkspaceCleanupFanout_AllFailure(t *testing.T) {
	caller := func(_ context.Context, _ string, _ []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		return nil, errors.New("worker offline")
	}
	status, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1", "w2"), caller)
	assert.Equal(t, "partial", status)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, "failed", e.Status)
	}
}

// TestRunWorkspaceCleanupFanout_NoWorkers covers the empty-tabs case
// (workspace had no tabs at delete time): the hub-side delete is the
// only step, no fan-out is needed, and overall status is "ok".
func TestRunWorkspaceCleanupFanout_NoWorkers(t *testing.T) {
	called := false
	caller := func(_ context.Context, _ string, _ []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		called = true
		return nil, nil
	}
	status, entries := runWorkspaceCleanupFanout(context.Background(), nil, caller)
	assert.Equal(t, "ok", status)
	assert.Empty(t, entries)
	assert.False(t, called, "no workers means caller should not be invoked")
}

// TestRunWorkspaceCleanupFanout_SuccessEntryHoldsOnlyTheWorkerAndStatus pins
// the whole entry of a worker that cleaned up. CleanupWorkspaceResponse
// carries no fields, so the entry states the worker and the outcome and
// nothing else: no empty list, and no error key on a success.
func TestRunWorkspaceCleanupFanout_SuccessEntryHoldsOnlyTheWorkerAndStatus(t *testing.T) {
	caller := func(_ context.Context, _ string, _ []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}
	_, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1"), caller)
	require.Len(t, entries, 1)
	assert.Equal(t, map[string]any{"worker_id": "w1", "status": "ok"}, jsonMap(t, entries[0]))
}

// cleanupDispatcher stands in for the worker's CleanupWorkspace handler and
// records the requests that reach it.
type cleanupDispatcher struct {
	mu       sync.Mutex
	methods  []string
	requests []*leapmuxv1.CleanupWorkspaceRequest
}

func (d *cleanupDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	if req.GetMethod() != leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	var in leapmuxv1.CleanupWorkspaceRequest
	if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
		_ = w.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	d.mu.Lock()
	d.requests = append(d.requests, &in)
	d.mu.Unlock()
	_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{})
}

// The production caller sends each worker its own tab identities through the
// worker's CleanupWorkspace procedure. The worker closes exactly the tabs it
// receives, so a tab that loses its type or id on the way is a tab that the
// delete leaves open.
func TestCliCleanupCaller_SendsTheTabIdentitiesToTheWorker(t *testing.T) {
	disp := &cleanupDispatcher{}
	startSpawnIPC(t, &recordingHub{}, disp)
	c, err := control.NewClientFromEnv("")
	require.NoError(t, err)

	tabs := []*leapmuxv1.TabIdent{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a-1"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "f-1"},
	}
	resp, err := cliCleanupCaller(c)(t.Context(), "worker-A", tabs)
	require.NoError(t, err)
	assert.NotNil(t, resp, "a success returns the worker's reply")

	disp.mu.Lock()
	defer disp.mu.Unlock()
	assert.Equal(t, []string{leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure}, disp.methods)
	require.Len(t, disp.requests, 1)
	got := disp.requests[0].GetTabs()
	require.Len(t, got, len(tabs))
	for i, want := range tabs {
		assert.Equal(t, want.GetTabType(), got[i].GetTabType())
		assert.Equal(t, want.GetTabId(), got[i].GetTabId())
	}
}

// A worker that refuses the cleanup returns no reply, so the fan-out reports
// that worker as failed instead of reading an empty success.
func TestCliCleanupCaller_ReturnsTheWorkersRefusal(t *testing.T) {
	startSpawnIPC(t, &recordingHub{}, inBandErrorDispatcher{code: codes.Unavailable, message: "worker is shutting down"})
	c, err := control.NewClientFromEnv("")
	require.NoError(t, err)

	resp, err := cliCleanupCaller(c)(t.Context(), "worker-A", nil)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "worker is shutting down")
}

// TestRunWorkspaceCleanupFanout_RoutesEachWorkerItsOwnTabs pins the routing the
// tab-list shape introduced: CleanupWorkspace no longer takes a workspace id, so
// the fan-out has to hand every worker exactly the tabs IT hosts.
//
// Getting this wrong is not a cosmetic mis-send. A worker acts on the ids it is
// given -- closeTabCommon closes them and drops their worktree links -- and tab
// ids are unique per user, not per worker, so a tab id leaked into the wrong
// worker's list is a live tab on that machine that the request would close.
func TestRunWorkspaceCleanupFanout_RoutesEachWorkerItsOwnTabs(t *testing.T) {
	var (
		mu   sync.Mutex
		seen = map[string][]string{}
	)
	caller := func(_ context.Context, workerID string, tabs []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		ids := make([]string, 0, len(tabs))
		for _, t := range tabs {
			ids = append(ids, t.GetTabId())
		}
		mu.Lock()
		seen[workerID] = ids
		mu.Unlock()
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	workerTabs := []*leapmuxv1.WorkerTabs{
		{WorkerId: "w1", Tabs: []*leapmuxv1.TabIdent{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t-1"},
		}},
		{WorkerId: "w2", Tabs: []*leapmuxv1.TabIdent{
			{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "f-2"},
		}},
	}

	status, _ := runWorkspaceCleanupFanout(context.Background(), workerTabs, caller)
	assert.Equal(t, "ok", status)
	assert.Equal(t, []string{"a-1", "t-1"}, seen["w1"])
	assert.Equal(t, []string{"f-2"}, seen["w2"], "w2 must not be handed w1's tab ids")
}

// TestRunWorkspaceCleanupFanout_EmptyTabGroupIsStillCalled pins the
// best-effort half of the contract.
//
// The hub groups the tabs it read inside the delete transaction, so a worker it
// names normally arrives WITH tabs -- but the wire shape permits an empty group,
// and a worker in the list must still be called rather than skipped. Calling it
// with nothing to close is a harmless no-op the reconciler covers; skipping it
// would drop that worker from the per-worker status the user is shown.
func TestRunWorkspaceCleanupFanout_EmptyTabGroupIsStillCalled(t *testing.T) {
	var (
		mu     sync.Mutex
		called = map[string]int{}
	)
	caller := func(_ context.Context, workerID string, tabs []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		assert.Emptyf(t, tabs, "%s arrived with an empty group, so it must be called with no tabs", workerID)
		mu.Lock()
		called[workerID]++
		mu.Unlock()
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(),
		[]*leapmuxv1.WorkerTabs{{WorkerId: "w-empty"}}, caller)

	assert.Equal(t, "ok", status)
	assert.Equal(t, 1, called["w-empty"], "a worker with an empty tab group must still be called")
	require.Len(t, entries, 1)
	assert.Equal(t, "w-empty", entries[0].WorkerID)
}

// TestRunWorkspaceCleanupFanout_ContextPropagates verifies the
// per-worker call receives the same context the CLI was given
// (carries deadlines, cancellation). Important so a Ctrl-C between
// workers cancels the in-flight call.
func TestRunWorkspaceCleanupFanout_ContextPropagates(t *testing.T) {
	type ctxKey string
	const key ctxKey = "k"
	parent := context.WithValue(context.Background(), key, "v")

	caller := func(ctx context.Context, _ string, _ []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		assert.Equal(t, "v", ctx.Value(key), "fan-out must propagate the caller's context")
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}
	_, _ = runWorkspaceCleanupFanout(parent, workerTabsFor("w1"), caller)
}

// TestRunWorkspaceList_InsideWorkerSpawn is the end-to-end regression
// test for `leapmux control workspace list` run from inside a
// worker-spawned agent. The spawn injects LEAPMUX_CONTROL_USER_ID into
// every agent process (see controlipc/server.go); the command must
// ignore it and issue exactly one hub call. It used to bind the value
// to a --user-id flag whose resolver leg called the (now-deleted)
// hub GetUser RPC -- which the agent's delegation bearer may not
// invoke -- aborting an otherwise-legal ListWorkspaces with
// `resolve_failed`.
func TestRunWorkspaceList_InsideWorkerSpawn(t *testing.T) {
	// No worker.* call, so the spawn needs no local dispatcher.
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)
	// The worker injects this into every spawned agent (see
	// controlipc.EnvVars).
	t.Setenv("LEAPMUX_CONTROL_USER_ID", "u-spawn")

	data := runControl(t, "workspace", "list").Data(t)
	rows, ok := data["workspaces"].([]any)
	require.True(t, ok, "a list lives under its plural noun: %v", data)
	require.Len(t, rows, 1)
	// The whole row, so a field that the struct drops or renames fails
	// here. The stub sends no created_at, and an absent instant is omitted.
	assert.Equal(t, map[string]any{"id": "ws-1", "created_by": "u-spawn", "title": "First"}, rows[0])
	// ListWorkspaces and ListSections run concurrently, so check set equality.
	assert.ElementsMatch(t, []string{
		leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
		leapmuxv1connect.SectionServiceListSectionsProcedure,
	}, hub.called(),
		"a session-scoped list reads workspaces and sections without resolving any entity-id env var")
}

// workerTabsFor builds the per-worker fan-out shape for tests that only care
// about which workers are called, not which tabs each gets.
func workerTabsFor(workerIDs ...string) []*leapmuxv1.WorkerTabs {
	out := make([]*leapmuxv1.WorkerTabs, 0, len(workerIDs))
	for _, wid := range workerIDs {
		out = append(out, &leapmuxv1.WorkerTabs{WorkerId: wid})
	}
	return out
}

// `workspace get` and `workspace list` print created_at as the same formatted
// string every other listing verb prints, not as a protojson Timestamp object,
// and omit it when the hub sends none.
func TestWorkspaceRowOf_PrintsCreatedAtAsAString(t *testing.T) {
	created := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	row := jsonMap(t, workspaceRowOf(&leapmuxv1.Workspace{
		Id: "ws-1", CreatedBy: "u-1", Title: "First", CreatedAt: timestamppb.New(created),
	}))
	assert.Equal(t, map[string]any{
		"id":         "ws-1",
		"created_by": "u-1",
		"title":      "First",
		"created_at": timefmt.Format(created),
	}, row)

	row = jsonMap(t, workspaceRowOf(&leapmuxv1.Workspace{Id: "ws-2"}))
	assert.NotContains(t, row, "created_at", "an absent timestamp is omitted, not printed as the epoch")
}

// TestWorkspaceRowOf_CarriesEveryFieldOfWorkspace is the tripwire for the
// explicit row. A field that the Workspace message gains prints nowhere until
// workspaceRowOf prints it, and an operator's script then reads an absent
// key, not an error.
func TestWorkspaceRowOf_CarriesEveryFieldOfWorkspace(t *testing.T) {
	workspace := &leapmuxv1.Workspace{}
	assertRowMapperCarriesEveryField(t, rowMapper{
		name:   "workspaceRowOf",
		msg:    workspace,
		render: func() any { return workspaceRowOf(workspace) },
		keys: map[string]string{
			"id":         "id",
			"created_by": "created_by",
			"title":      "title",
			"created_at": "created_at",
		},
	})
}

func TestLiveTabsOfWorkspace_SortedDeterministicallyAndIncludesFloatingWindows(t *testing.T) {
	rootID := fullTestID("root-ws")
	fwRootID := fullTestID("fw-root")
	b := newStateBuilder().
		workspace("ws-1", rootID).
		leafNode(rootID, "", "a0").
		leafNode(fwRootID, "", "a0").
		tab("tab-z", rootID, leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("tab-a", fwRootID, leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-m", rootID, leapmuxv1.TabType_TAB_TYPE_FILE).
		tombstonedTab("tab-dead", rootID, leapmuxv1.TabType_TAB_TYPE_AGENT)
	b.st.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		"fw-1": {
			WindowId:    "fw-1",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  fwRootID,
		},
	}

	got := liveTabsOfWorkspace(b.st, "ws-1")
	require.Len(t, got, 3)
	assert.Equal(t, []string{"tab-a", "tab-m", "tab-z"}, []string{got[0].TabID, got[1].TabID, got[2].TabID})
}

type funcHubClient func(ctx context.Context, userID userid.UserID, procedure string, payload []byte) ([]byte, error)

func (f funcHubClient) CallInner(ctx context.Context, userID userid.UserID, procedure string, payload []byte) ([]byte, error) {
	return f(ctx, userID, procedure, payload)
}

func TestWorkspaceUnarchive_NonexistentExplicitIDReturnsNotFound(t *testing.T) {
	missingID := fullTestID("missing-ws")
	customHub := funcHubClient(func(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error) {
		switch procedure {
		case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
			return nil, connect.NewError(connect.CodeNotFound, "workspace not found")
		case leapmuxv1connect.SectionServiceListSectionsProcedure:
			return proto.Marshal(&leapmuxv1.ListSectionsResponse{
				Sections: []*leapmuxv1.Section{
					{Id: "sec-prog", Name: "In progress", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS},
					{Id: "sec-arch", Name: "Archived", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED},
				},
			})
		default:
			return nil, connect.NewError(connect.CodePermissionDenied, "unexpected procedure: "+procedure)
		}
	})
	startSpawnIPC(t, &recordingHub{}, nil, func(r *controlipc.Router) {
		r.Hub = customHub
	})

	o := runControl(t, "workspace", "unarchive", "id:"+missingID)
	o.requireFailure(t, cli.CodeNotFound)
}

func TestWorkspaceGet_ExplicitIDCallsGetWorkspaceOnce(t *testing.T) {
	wsID := fullTestID("ws-1")
	var (
		mu    sync.Mutex
		calls []string
	)
	customHub := funcHubClient(func(_ context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error) {
		mu.Lock()
		calls = append(calls, procedure)
		mu.Unlock()
		if procedure == leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure {
			return proto.Marshal(&leapmuxv1.GetWorkspaceResponse{
				Workspace: &leapmuxv1.Workspace{Id: wsID, Title: "Alpha"},
			})
		}
		return nil, connect.NewError(connect.CodePermissionDenied, "unexpected procedure: "+procedure)
	})
	startSpawnIPC(t, &recordingHub{}, nil, func(r *controlipc.Router) {
		r.Hub = customHub
	})

	data := runControl(t, "workspace", "get", "id:"+wsID).Data(t)
	assert.Equal(t, wsID, data["id"])
	assert.Equal(t, "Alpha", data["title"])
	assert.Equal(t, []string{leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure}, calls)
}

func TestWorkspaceMove_ValidatesPositionFlagsBeforeDialing(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)

	o := runControl(t, "workspace", "move", "ws-1")
	f := o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "specify --section or a position flag")
	assert.Empty(t, hub.called())

	o = runControl(t, "workspace", "move", "ws-1", "--section", "In progress", "--first", "--last")
	o.requireFailure(t, cli.CodeUsage)
	assert.Empty(t, hub.called())

	o = runControl(t, "workspace", "move", "ws-1", "--section", "In progress", "--before", "   ")
	o.requireFailure(t, cli.CodeUsage)
	assert.Empty(t, hub.called())
}

func TestWorkspaceMove_OptionalSectionReordersWithinCurrentSection(t *testing.T) {
	ws1 := fullTestID("ws-move-1")
	ws2 := fullTestID("ws-move-2")
	var (
		mu       sync.Mutex
		moveReqs []*leapmuxv1.MoveWorkspaceRequest
	)
	customHub := funcHubClient(func(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error) {
		switch procedure {
		case leapmuxv1connect.SectionServiceListSectionsProcedure:
			return proto.Marshal(&leapmuxv1.ListSectionsResponse{
				Sections: []*leapmuxv1.Section{
					{Id: "sec-prog", Name: "In progress", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS},
					{Id: "sec-client", Name: "Client Work", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM},
				},
				Items: []*leapmuxv1.SectionItem{
					{SectionId: "sec-client", WorkspaceId: ws2, Position: "m"},
					{SectionId: "sec-client", WorkspaceId: ws1, Position: "n"},
				},
			})
		case leapmuxv1connect.WorkspaceServiceMoveWorkspaceProcedure:
			var in leapmuxv1.MoveWorkspaceRequest
			if err := proto.Unmarshal(payload, &in); err != nil {
				return nil, err
			}
			mu.Lock()
			moveReqs = append(moveReqs, &in)
			mu.Unlock()
			return proto.Marshal(&leapmuxv1.MoveWorkspaceResponse{})
		default:
			return nil, connect.NewError(connect.CodeUnimplemented, "unexpected procedure: "+procedure)
		}
	})
	startSpawnIPC(t, &recordingHub{}, nil, func(r *controlipc.Router) {
		r.Hub = customHub
	})

	data := runControl(t, "workspace", "move", "id:"+ws1, "--first").Data(t)
	assert.Equal(t, ws1, data["id"])
	assert.Equal(t, "sec-client", data["section_id"])
	assert.Equal(t, "Client Work", data["section_name"])
	assert.NotEmpty(t, data["position"])

	require.Len(t, moveReqs, 1)
	assert.Equal(t, ws1, moveReqs[0].GetWorkspaceId())
	assert.Equal(t, "sec-client", moveReqs[0].GetSectionId())
	assert.Less(t, moveReqs[0].GetPosition(), "m", "--first must rank before the existing first workspace in the same section")
}

type deleteGuardDispatcher struct {
	mu                sync.Mutex
	inspectErrCode    codes.Code
	inspectErrorHint  string
	workingAgent      bool
	dirtyWorktree     bool
	sharedPath        string
	inspectCloseCalls int
}

func (d *deleteGuardDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var in leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		state := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
		if d.workingAgent {
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
		}
		agents := make([]*leapmuxv1.AgentInfo, 0, len(in.GetTabIds()))
		for _, id := range in.GetTabIds() {
			agents = append(agents, &leapmuxv1.AgentInfo{Id: id, ActivityState: state})
		}
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: agents})
	case leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure:
		d.inspectCloseCalls++
		if d.inspectErrCode != codes.OK {
			_ = rw.SendError(int32(d.inspectErrCode), "git inspect failed")
			return
		}
		var in leapmuxv1.InspectTabsCloseRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		items := make([]*leapmuxv1.TabCloseInspection, 0, len(in.GetTabs()))
		for _, tab := range in.GetTabs() {
			item := &leapmuxv1.TabCloseInspection{Tab: tab}
			switch {
			case d.inspectErrorHint != "":
				item.ErrorHint = d.inspectErrorHint
			case d.dirtyWorktree:
				item.ShouldPrompt = true
				item.IsWorktree = true
				item.BranchName = "feature/wip"
				item.WorktreePath = d.sharedPath
				item.GitState = &leapmuxv1.BranchGitState{HasUncommittedChanges: true}
			default:
				item.GitState = &leapmuxv1.BranchGitState{}
			}
			items = append(items, item)
		}
		writeProtoResponse(rw, &leapmuxv1.InspectTabsCloseResponse{Items: items})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestWorkspaceDeleteGuard_FailsClosedAndPrioritizesBusyTabs(t *testing.T) {
	rootID := fullTestID("root-ws")
	tabID := fullTestID("ag-guard")

	buildState := func() *leapmuxv1.UserMaterialized {
		b := newStateBuilder().
			workspace("ws-1", rootID).
			leafNode(rootID, "", "a0").
			tab(tabID, rootID, leapmuxv1.TabType_TAB_TYPE_AGENT)
		b.st.Tabs[tabID].WorkerId = &leapmuxv1.LWWString{Value: "worker-A"}
		return b.st
	}

	t.Run("fails closed on non-unavailable InspectTabsClose error", func(t *testing.T) {
		disp := &deleteGuardDispatcher{inspectErrCode: codes.Internal}
		hub := &recordingHub{workers: workerRows("worker-A"), materialized: buildState()}
		startSpawnIPC(t, hub, disp)

		o := runControl(t, "workspace", "delete", "ws-1")
		f := o.Failure(t)
		assert.Equal(t, cli.CodeInternal, f.Code)
		assert.Contains(t, f.Message, "git inspect failed")
		assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure)
	})

	t.Run("fails closed on non-empty error_hint from InspectTabsClose", func(t *testing.T) {
		disp := &deleteGuardDispatcher{
			inspectErrorHint: "git state unavailable: dubious ownership",
		}
		hub := &recordingHub{workers: workerRows("worker-A"), materialized: buildState()}
		startSpawnIPC(t, hub, disp)

		o := runControl(t, "workspace", "delete", "ws-1")
		f := o.requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "dubious ownership")
		assert.Equal(t, 1, disp.inspectCloseCalls)
		assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure)
	})

	t.Run("busy tab takes priority over dirty worktree", func(t *testing.T) {
		disp := &deleteGuardDispatcher{
			workingAgent:  true,
			dirtyWorktree: true,
			sharedPath:    "/repo/wip",
		}
		hub := &recordingHub{workers: workerRows("worker-A"), materialized: buildState()}
		startSpawnIPC(t, hub, disp)

		o := runControl(t, "workspace", "delete", "ws-1")
		f := o.requireFailure(t, cli.CodeTabBusyRefused)
		assert.Contains(t, f.Message, "agent turn is in progress")
		assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure)
	})

	t.Run("seenPath deduplicates per Worker while independent Workers inspect identical paths", func(t *testing.T) {
		disp := &deleteGuardDispatcher{
			dirtyWorktree: true,
			sharedPath:    "/repo/same-path",
		}
		startSpawnIPC(t, &recordingHub{}, disp)
		c, err := control.NewClientFromEnv("")
		require.NoError(t, err)
		ipcClient, ok := c.(*control.WorkerIPCClient)
		require.True(t, ok)

		// Within one Worker, two sibling tabs sharing /repo/same-path issue one
		// InspectTabsClose call and report one deduplicated dirty reason.
		refs := []crdt.TabRef{
			{TabID: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			{TabID: fullTestID("ag-sib"), TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		}
		w := workerCall{ipc: ipcClient, workerID: "worker-A"}
		dirtyA, errA := inspectTabsCloseDirtyOnChannel(t.Context(), w, refs)
		require.NoError(t, errA)
		require.Len(t, dirtyA, 1)
		assert.Equal(t, 1, disp.inspectCloseCalls, "sibling tabs on the same Worker should share one InspectTabsClose RPC")

		// A separate Worker invocation maintains its own seenPath map and inspects /repo/same-path again.
		dirtyB, errB := inspectTabsCloseDirtyOnChannel(t.Context(), w, refs[:1])
		require.NoError(t, errB)
		require.Len(t, dirtyB, 1)
		assert.Equal(t, 2, disp.inspectCloseCalls, "each Worker must inspect /repo/same-path independently")
	})
}

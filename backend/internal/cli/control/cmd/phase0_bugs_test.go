package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// B01: `file list` in default text mode must print file and directory rows
// (not drop Entries behind a scalar-only directoryListing table), and `-q` must
// print full entry paths.
func TestFileListResult_RenderTextAndQuiet(t *testing.T) {
	res := fileListResult{
		Listings: []directoryListing{
			{
				Path:         "/src/app",
				Truncated:    true,
				TotalEntries: 10,
				Entries: []fileInfo{
					{Name: "main.go", Path: "/src/app/main.go", IsDir: false, Size: 512, Permissions: "-rw-r--r--"},
					{Name: "internal", Path: "/src/app/internal", IsDir: true, Size: 0, Permissions: "drwxr-xr-x"},
				},
			},
			{
				Path: "/src/app/internal",
				Entries: []fileInfo{
					{Name: "cli.go", Path: "/src/app/internal/cli.go", IsDir: false, Size: 128, Permissions: "-rw-r--r--"},
				},
			},
		},
		Unreadable: &unreadableDirectory{Path: "/src/app/secret", Reason: "permission denied"},
	}

	var textBuf bytes.Buffer
	p := &cli.Printer{Out: &textBuf, Err: &bytes.Buffer{}}
	require.NoError(t, p.Result(res))
	assert.Contains(t, textBuf.String(), "main.go", "file list text mode must print file names")
	assert.Contains(t, textBuf.String(), "internal", "file list text mode must print directory names")
	assert.Contains(t, textBuf.String(), "NAME", "file list text mode must render fileInfo table headers")
	assert.Contains(t, textBuf.String(), "/src/app/internal:", "multi-directory file list must print directory headers")
	assert.Contains(t, textBuf.String(), "unreadable: /src/app/secret (permission denied)", "unreadable directory must be rendered")

	var quietBuf bytes.Buffer
	qp := &cli.Printer{Out: &quietBuf, Err: &bytes.Buffer{}, Quiet: true}
	require.NoError(t, qp.Result(res))
	assert.Equal(t, "/src/app/main.go\n/src/app/internal\n/src/app/internal/cli.go\n", quietBuf.String())
}

// B02: `tab close` must preserve worktree_removal, worktree_path, worktree_id,
// worker_close_failure, and worker_close_failure_detail from CloseTabResponse.
func TestCloseTabOnWorker_PreservesWorktreeAndFailureFields(t *testing.T) {
	isolateCLIEnv(t)
	tabID := fullTestID("tab-wt-fail")
	wsID := "ws-1"
	tileID := fullTestID("tile-wt-fail")
	workerID := "worker-A"

	state := newStateBuilder().workspace(wsID, tileID).leafNode(tileID, "", "").st
	state.Tabs[tabID] = &leapmuxv1.TabRecord{
		TabId:    tabID,
		TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		TileId:   &leapmuxv1.LWWString{Value: tileID},
		WorkerId: &leapmuxv1.LWWString{Value: workerID},
		Position: &leapmuxv1.LWWString{Value: "a0"},
	}
	hub := &recordingHub{materialized: state, onSubmitOps: tabCloseHub().onSubmitOps}
	disp := &closeTabOutcomeDispatcher{tabID: tabID}
	startSpawnIPC(t, hub, disp)

	res := runControl(t, "tab", "close", tabID, "--worktree=discard")
	assert.Equal(t, cli.ExitFailure, res.Code, "failed worktree discard must exit non-zero")
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Stdout), &env))
	data := env.Data
	assert.Equal(t, "partial_failure", data["status"])
	assert.Equal(t, "failed", data["worktree_removal"])
	assert.Equal(t, "/tmp/wt-feature", data["worktree_path"])
	assert.Equal(t, "wt-123", data["worktree_id"])
	assert.Equal(t, "failed to remove git worktree", data["worker_close_failure"])
	assert.Equal(t, "directory not empty", data["worker_close_failure_detail"])
}

type closeTabOutcomeDispatcher struct {
	tabID string
}

func (d *closeTabOutcomeDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure:
		writeProtoResponse(rw, &leapmuxv1.InspectLastTabCloseResponse{
			Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
			WorktreePath: "/tmp/wt-feature",
			WorkingDir:   "/tmp/wt-feature",
			ShouldPrompt: true,
		})
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{
			Agents: []*leapmuxv1.AgentInfo{{Id: d.tabID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE}},
		})
	case leapmuxv1connect.WorkerTabServiceCloseTabProcedure:
		writeProtoResponse(rw, &leapmuxv1.CloseTabResponse{
			WorktreeRemoval: leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED,
			WorktreePath:    "/tmp/wt-feature",
			WorktreeId:      "wt-123",
			FailureMessage:  "failed to remove git worktree",
			FailureDetail:   "directory not empty",
		})
	default:
		writeProtoResponse(rw, &leapmuxv1.CloseTabResponse{})
	}
}

// B03: `layout get` must isolate TabsByTile to the requested workspace, sort
// tabs by LexoRank position, include floating windows, and render tab IDs in
// text mode.
func TestLayoutTree_IsolatesWorkspaceTabsSortsAndRendersTabs(t *testing.T) {
	wsA := fullTestID("ws-layout-a")
	wsB := fullTestID("ws-layout-b")
	tileA := fullTestID("tile-layout-a")
	tileB := fullTestID("tile-layout-b")
	floatWinID := fullTestID("float-win-a")
	floatLeaf := fullTestID("float-leaf-a")
	tabA1 := fullTestID("tab-a-first")
	tabA2 := fullTestID("tab-a-second")
	tabFloat := fullTestID("tab-a-float")
	tabOtherWS := fullTestID("tab-b-other")

	state := newStateBuilder().
		workspace(wsA, tileA).leafNode(tileA, "", "").
		workspace(wsB, tileB).leafNode(tileB, "", "").
		leafNode(floatLeaf, "", "").st
	state.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		floatWinID: {
			WindowId:    floatWinID,
			WorkspaceId: &leapmuxv1.LWWString{Value: wsA},
			RootNodeId:  floatLeaf,
		},
	}
	state.Tabs = map[string]*leapmuxv1.TabRecord{
		tabA2:      {TabId: tabA2, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TileId: &leapmuxv1.LWWString{Value: tileA}, Position: &leapmuxv1.LWWString{Value: "b0"}},
		tabA1:      {TabId: tabA1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: tileA}, Position: &leapmuxv1.LWWString{Value: "a0"}},
		tabFloat:   {TabId: tabFloat, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TileId: &leapmuxv1.LWWString{Value: floatLeaf}, Position: &leapmuxv1.LWWString{Value: "a0"}},
		tabOtherWS: {TabId: tabOtherWS, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: tileB}, Position: &leapmuxv1.LWWString{Value: "a0"}},
	}

	tree := buildLayoutTreeForWorkspace(state, wsA, true)
	require.NotContains(t, tree.TabsByTile, tileB, "tabs from another workspace must not leak into layout get")
	require.Len(t, tree.TabsByTile[tileA], 2)
	assert.Equal(t, tabA1, tree.TabsByTile[tileA][0].ID, "tabs must be ordered by LexoRank position")
	assert.Equal(t, tabA2, tree.TabsByTile[tileA][1].ID)
	require.Len(t, tree.FloatingWindows, 1)
	assert.Equal(t, floatWinID, tree.FloatingWindows[0].ID)
	require.Len(t, tree.TabsByTile[floatLeaf], 1)

	var textBuf bytes.Buffer
	require.NoError(t, tree.RenderText(&textBuf))
	assert.Contains(t, textBuf.String(), cli.ShortID(tabA1), "layout get text mode must render tab IDs")
	assert.Contains(t, textBuf.String(), "agent", "layout get text mode must render tab types")
	assert.Contains(t, textBuf.String(), cli.ShortID(floatWinID), "layout get text mode must render floating windows")
}

// B04: `control version` inside a LeapMux tab (with LEAPMUX_CONTROL_SOCK set
// and no stored credentials) must query Hub and Worker builds over local IPC.
func TestRunVersion_UsesLocalIPCInsideTab(t *testing.T) {
	isolateCLIEnv(t)
	hub := &recordingHub{
		systemBuild: &leapmuxv1.BuildInfo{Version: "2.1.0", CommitHash: "hub1234"},
	}
	disp := &versionIPCWorker{
		workerBuild: &leapmuxv1.BuildInfo{Version: "2.1.0", CommitHash: "wrk5678"},
	}
	startSpawnIPC(t, hub, disp)

	data := runControl(t, "version").Data(t)
	require.Contains(t, data, "hub", "control version inside a tab must include hub build via local IPC")
	hubMap := data["hub"].(map[string]any)
	assert.Equal(t, "2.1.0", hubMap["version"])
	require.Contains(t, data, "worker", "control version inside a tab must include worker build via local IPC")
	workerMap := data["worker"].(map[string]any)
	assert.Equal(t, "2.1.0", workerMap["version"])
}

type versionIPCWorker struct {
	workerBuild *leapmuxv1.BuildInfo
}

func (w *versionIPCWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() == leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure {
		writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{Build: w.workerBuild})
		return
	}
	writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{})
}

// B05: `auth login` must NOT delete or revoke an existing working credential
// when the new login fails before completing.
func TestRunAuthLogin_KeepsExistingCredentialOnFailedLogin(t *testing.T) {
	isolateCLIEnv(t)

	revoked := false
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/revoke":
			revoked = true
			w.WriteHeader(http.StatusOK)
		case "/oauth/device-authorization":
			http.Error(w, "refused", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	o := runControl(t, "auth", "login", "--hub", srv.URL, "--device-code")
	o.requireFailure(t, cli.CodeInternal)

	assert.False(t, revoked, "a failed login must not revoke the existing working credential")
	loaded, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err, "a failed login must keep the existing working credential on disk")
	assert.Equal(t, "local-name", loaded.Username)
}

// B06: `tab move <tab> --first` and `--last` must reorder within the tab's
// current tile without requiring a redundant `--tile` flag, while omitting all
// destination/placement flags still fails with a usage error.
func TestRunTabMove_FirstAndLastWithoutTileFlag(t *testing.T) {
	isolateCLIEnv(t)
	tab1 := fullTestID("tab-mv-1")
	tab2 := fullTestID("tab-mv-2")
	wsID := "ws-1"
	tileID := "root-1"
	workerID := "worker-A"

	state := newStateBuilder().workspace(wsID, tileID).leafNode(tileID, "", "").st
	state.Tabs = map[string]*leapmuxv1.TabRecord{
		tab1: {TabId: tab1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: tileID}, WorkerId: &leapmuxv1.LWWString{Value: workerID}, Position: &leapmuxv1.LWWString{Value: "a0"}},
		tab2: {TabId: tab2, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: tileID}, WorkerId: &leapmuxv1.LWWString{Value: workerID}, Position: &leapmuxv1.LWWString{Value: "b0"}},
	}
	hub := &recordingHub{materialized: state, onSubmitOps: tabCloseHub().onSubmitOps}
	startSpawnIPC(t, hub, nil)

	data := runControl(t, "tab", "move", tab2, "--first").Data(t)
	assert.Equal(t, tab2, data["id"])
	assert.Equal(t, tileID, data["tile_id"])

	dataLast := runControl(t, "tab", "move", tab1, "--last").Data(t)
	assert.Equal(t, tab1, dataLast["id"])
	assert.Equal(t, tileID, dataLast["tile_id"])

	runControl(t, "tab", "move", tab1).requireFailure(t, cli.CodeUsage)
}

// B09: `terminal list` must render terminal-specific columns (no TYPE or
// PROVIDER columns), and `terminal get` Short must not claim "shell".
func TestTerminalListAndGet_MetadataAccuracy(t *testing.T) {
	cmd := NewCommand()
	termGet, _, err := cmd.Find([]string{"terminal", "get"})
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(termGet.Short), "shell", "terminal get Short must not promise an absent shell field")

	var buf bytes.Buffer
	p := &cli.Printer{Out: &buf, Err: &bytes.Buffer{}}
	exitCode := int32(0)
	require.NoError(t, p.Result(terminalList{
		Terminals: []terminalListRow{{
			ID:          fullTestID("term-row-1"),
			Title:       "Tests",
			Status:      "ready",
			Size:        "120x40",
			WorkingDir:  "/src/app",
			Worker:      "imac",
			WorkspaceID: fullTestID("ws-1"),
			ExitCode:    &exitCode,
		}},
	}))
	out := buf.String()
	assert.Contains(t, out, "SIZE")
	assert.Contains(t, out, "120x40")
	assert.NotContains(t, out, "PROVIDER")
}

// B14: Negative --limit must be rejected client-side on file read and git read,
// and approve/deny/answer completion must not suggest "self".
func TestPhase0_B14_ValidationAndCompletion(t *testing.T) {
	runControl(t, "file", "read", "/tmp/a", "--limit", "-1").requireFailure(t, cli.CodeUsage)
	runControl(t, "git", "read", "/tmp/a", "--limit", "-1").requireFailure(t, cli.CodeUsage)

	t.Setenv(controlenv.TabIDVariable, fullTestID("self-agent"))
	t.Setenv(controlenv.TabTypeVariable, "agent")
	for _, verb := range []string{"approve", "deny", "answer"} {
		root := NewCommand()
		o := runControlText(t, "__complete", "agent", verb, "sel")
		_ = root
		assert.NotContains(t, o.Stdout, "self\n", "agent %s must not complete self", verb)
	}
}

// B08: -q must return meaningful lines on agentResult (input_id), tabStatusList
// (keys), tileSplitResult (new tile ID), and refuse -q on version/whoami.
func TestPhase0_B08_QuietLines(t *testing.T) {
	var buf bytes.Buffer
	p := &cli.Printer{Out: &buf, Err: &bytes.Buffer{}, Quiet: true}
	require.NoError(t, p.Result(agentResult{ID: "agent-1", InputID: "input-9"}))
	assert.Equal(t, "input-9\n", buf.String(), "agent send -q must print input_id when present")

	buf.Reset()
	require.NoError(t, p.Result(tabStatusListResult{
		ID: "tab-1",
		Statuses: []tabStatusRow{
			{Key: "build", Text: "ok"},
			{Key: "lint", Text: "ok"},
		},
	}))
	assert.Equal(t, "build\nlint\n", buf.String(), "tab status list -q must print status keys")

	buf.Reset()
	require.NoError(t, p.Result(tileSplitResult{
		SplitTileID:                "split-1",
		LeafTileWithExistingTabsID: "leaf-old",
		LeafTileWithEmptyTabsID:    "leaf-new",
	}))
	assert.Equal(t, "leaf-new\n", buf.String(), "tile split -q must print the new leaf tile ID")

	buf.Reset()
	require.NoError(t, p.Result(controlRequestList{Requests: []controlRequestRow{
		{ID: "req-1", AgentID: "ag-1"},
		{ID: "req-2", AgentID: "ag-1"},
	}}))
	assert.Equal(t, "req-1\nreq-2\n", buf.String(), "single-agent agent requests -q prints handles")

	buf.Reset()
	require.NoError(t, p.Result(controlRequestList{Requests: []controlRequestRow{
		{ID: "req-1", AgentID: "ag-1"},
		{ID: "req-2", AgentID: "ag-2"},
	}}))
	assert.Equal(t, "ag-1\treq-1\nag-2\treq-2\n", buf.String(), "multi-agent agent requests -q prints agent_id<TAB>handle")

	buf.Reset()
	require.NoError(t, p.Result(adminSessionPage{Sessions: []adminSessionRow{{ID: "sess-1"}}}))
	assert.Equal(t, "sess-1\n", buf.String())

	buf.Reset()
	require.NoError(t, p.Result(adminAPITokenListResult{Tokens: []adminAPITokenRow{{CredentialLifetime: CredentialLifetime{ID: "tok-1"}}}}))
	assert.Equal(t, "tok-1\n", buf.String())

	buf.Reset()
	require.NoError(t, p.Result(adminAppListResult{Apps: []AppRegistrationRow{{ClientID: "app-1"}}}))
	assert.Equal(t, "app-1\n", buf.String())

	isolateCLIEnv(t)
	for _, args := range [][]string{
		{"version", "-q"},
		{"whoami", "-q"},
		{"file", "read", "/tmp/a", "-q"},
		{"git", "status", "/tmp/a", "-q"},
		{"git", "read", "/tmp/a", "-q"},
	} {
		o := runControlText(t, args...)
		assert.Equal(t, cli.ExitUsage, o.Code, "command %v must reject -q", args)
	}
}

// B12: `workspace delete` must refuse when a workspace has a busy tab unless
// `--allow-busy` is passed.
func TestWorkspaceDelete_RefusesBusyTabWithoutAllowBusy(t *testing.T) {
	isolateCLIEnv(t)
	wsID := "ws-1"
	tileID := fullTestID("tile-busy-del")
	tabID := fullTestID("agent-busy-del")
	workerID := "worker-A"

	state := newStateBuilder().workspace(wsID, tileID).leafNode(tileID, "", "").st
	state.Tabs = map[string]*leapmuxv1.TabRecord{
		tabID: {TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: tileID}, WorkerId: &leapmuxv1.LWWString{Value: workerID}, Position: &leapmuxv1.LWWString{Value: "a0"}},
	}
	hub := &recordingHub{materialized: state}
	disp := &busyAgentDispatcher{agentID: tabID}
	startSpawnIPC(t, hub, disp)

	runControl(t, "workspace", "delete", wsID).requireFailure(t, cli.CodeTabBusyRefused)
	assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure,
		"DeleteWorkspace must not be called when a tab is busy and --allow-busy is absent")

	// With --allow-busy, the busy guard lets the request proceed to DeleteWorkspace.
	runControl(t, "workspace", "delete", wsID, "--allow-busy").requireFailure(t, cli.CodePermissionDenied)
	assert.Contains(t, hub.called(), leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure,
		"--allow-busy must bypass the busy guard and dispatch DeleteWorkspace")
}

type busyAgentDispatcher struct {
	agentID string
}

func (d *busyAgentDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() == leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{
			Agents: []*leapmuxv1.AgentInfo{{
				Id:            d.agentID,
				ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			}},
		})
		return
	}
	writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{})
}

// B10: `admin worker get` must accept a unique ID prefix, matching its own
// --help example, and fail closed with candidates when a prefix is ambiguous.
func TestAdminWorkerGet_ResolvesUniqueIDPrefix(t *testing.T) {
	isolateCLIEnv(t)
	fullWorkerID := fullTestID("admin-wrk-pref")
	fullWorkerID2 := fullTestID("admin-wrk-other")
	prefix := fullWorkerID[:12]

	mux := http.NewServeMux()
	h := &adminPrefixFakeHub{
		workers: []*leapmuxv1.AdminWorker{
			{Id: fullWorkerID, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, OwnerUsername: "alice"},
			{Id: fullWorkerID2, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, OwnerUsername: "bob"},
		},
	}
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterAdminWorkerServiceHandler(connectServer, h)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_admin_tok",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserID:      "u-admin",
		Username:    "alice",
	}))

	data := runControl(t, "admin", "worker", "get", prefix, "--hub", srv.URL).Data(t)
	assert.Equal(t, fullWorkerID, data["id"])

	f := runControl(t, "admin", "worker", "get", "admin-wrk-", "--hub", srv.URL).requireFailure(t, cli.CodeAmbiguousTarget)
	assert.Len(t, f.Candidates, 2)
}

type adminPrefixFakeHub struct {
	leapmuxv1connect.UnimplementedAdminWorkerServiceHandler
	workers []*leapmuxv1.AdminWorker
}

func (h *adminPrefixFakeHub) ListWorkers(_ context.Context, _ *leapmuxv1.AdminWorkerServiceListWorkersRequest) (*leapmuxv1.AdminWorkerServiceListWorkersResponse, error) {
	return &leapmuxv1.AdminWorkerServiceListWorkersResponse{Workers: h.workers}, nil
}

func (h *adminPrefixFakeHub) GetWorker(_ context.Context, req *leapmuxv1.AdminWorkerServiceGetWorkerRequest) (*leapmuxv1.AdminWorkerServiceGetWorkerResponse, error) {
	for _, w := range h.workers {
		if w.GetId() == req.GetId() {
			return &leapmuxv1.AdminWorkerServiceGetWorkerResponse{Worker: w}, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, "worker not found")
}

// B14: Empty `agent requests` and `agent sessions` must not print stderr prose
// under --json or -q.
func TestReportControlRequests_NoEmptyNoticeInJSONOrQuiet(t *testing.T) {
	var errBuf bytes.Buffer
	p := &cli.Printer{Out: &bytes.Buffer{}, Err: &errBuf, Format: cli.FormatJSON}
	_, err := reportControlRequests(p, controlRequestList{}, nil, 1)
	require.NoError(t, err)
	assert.Empty(t, errBuf.String(), "--json must not print empty-state stderr prose")

	errBuf.Reset()
	qp := &cli.Printer{Out: &bytes.Buffer{}, Err: &errBuf, Quiet: true}
	_, err = reportControlRequests(qp, controlRequestList{}, nil, 1)
	require.NoError(t, err)
	assert.Empty(t, errBuf.String(), "-q must not print empty-state stderr prose")
}

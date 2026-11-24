package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

type phase2bWorkerDispatcher struct {
	workerName string
	agents     []*leapmuxv1.AgentInfo
	terminals  []*leapmuxv1.TerminalInfo
	processes  []*leapmuxv1.TerminalProcesses
	providers  []leapmuxv1.AgentProvider
	shells     []string
}

func (d *phase2bWorkerDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch {
	case strings.HasSuffix(req.GetMethod(), "/Ping"):
		writeProtoResponse(rw, &leapmuxv1.PingResponse{})
	case strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo"):
		writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name:  d.workerName,
			Build: &leapmuxv1.BuildInfo{Version: "dev"},
		})
	case strings.HasSuffix(req.GetMethod(), "/ListAgents"):
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: d.agents})
	case strings.HasSuffix(req.GetMethod(), "/ListTerminals"):
		writeProtoResponse(rw, &leapmuxv1.ListTerminalsResponse{Terminals: d.terminals})
	case strings.HasSuffix(req.GetMethod(), "/InspectTerminalProcesses"):
		writeProtoResponse(rw, &leapmuxv1.InspectTerminalProcessesResponse{Terminals: d.processes})
	case strings.HasSuffix(req.GetMethod(), "/ListAvailableProviders"):
		writeProtoResponse(rw, &leapmuxv1.ListAvailableProvidersResponse{Providers: d.providers})
	case strings.HasSuffix(req.GetMethod(), "/ListAvailableShells"):
		writeProtoResponse(rw, &leapmuxv1.ListAvailableShellsResponse{Shells: d.shells})
	default:
		_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
	}
}

func TestTreeCommand_RendersHierarchyAndFlatAndArchivedFooter(t *testing.T) {
	wsActive := fullTestID("ws-active")
	wsArchived := fullTestID("ws-arch")
	agParent := fullTestID("ag-parent")
	agChild := fullTestID("ag-child")
	termID := fullTestID("tm-tests")

	hub := &recordingHub{
		workspaces: []*leapmuxv1.Workspace{
			{Id: wsActive, Title: "CLI Redesign"},
			{Id: wsArchived, Title: "Old Archive"},
		},
		sections: &leapmuxv1.ListSectionsResponse{
			Sections: []*leapmuxv1.Section{
				{Id: "sec-1", Name: "In Progress", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM},
				{Id: "sec-arch", Name: "Archived", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED},
			},
			Items: []*leapmuxv1.SectionItem{
				{SectionId: "sec-1", WorkspaceId: wsActive},
				{SectionId: "sec-arch", WorkspaceId: wsArchived},
			},
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{WorkspaceId: wsActive, TileId: "tile-1", WorkerId: "worker-A", Tab: &leapmuxv1.TabIdent{TabId: agParent, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
			{WorkspaceId: wsActive, TileId: "tile-1", WorkerId: "worker-A", Tab: &leapmuxv1.TabIdent{TabId: agChild, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
			{WorkspaceId: wsActive, TileId: "tile-2", WorkerId: "worker-A", Tab: &leapmuxv1.TabIdent{TabId: termID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}},
		},
	}
	worker := &phase2bWorkerDispatcher{
		workerName: "devbox",
		agents: []*leapmuxv1.AgentInfo{
			{Id: agParent, Title: "Lead", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, GitStatus: &leapmuxv1.GitRepoStatus{Branch: "feat/cli"}},
			{Id: agChild, Title: "Sub", ParentAgentId: agParent, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER},
		},
		terminals: []*leapmuxv1.TerminalInfo{
			{TerminalId: termID, Title: "Tests", Status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY},
		},
		processes: []*leapmuxv1.TerminalProcesses{
			{TerminalId: termID, Processes: []*leapmuxv1.TerminalProcess{{Pid: 42, Name: "go"}}},
		},
	}
	startSpawnIPC(t, hub, worker)
	t.Setenv(controlenv.TabIDVariable, agParent)

	out := runControlText(t, "tree")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.Contains(t, out.Stdout, "In Progress")
	assert.Contains(t, out.Stdout, `"CLI Redesign"`)
	assert.Contains(t, out.Stdout, "◀ here")
	assert.Contains(t, out.Stdout, "subagent")
	assert.Contains(t, out.Stdout, "(running: go)")
	assert.Contains(t, out.Stdout, "Archived (1 workspace; pass --archived to show)")
	assert.NotContains(t, out.Stdout, "Old Archive")

	// --archived shows the archived workspace too.
	archOut := runControlText(t, "tree", "--archived")
	require.Equal(t, cli.ExitOK, archOut.Code, archOut.Stderr)
	assert.Contains(t, archOut.Stdout, "Old Archive")

	// --flat renders a flat table.
	flatOut := runControlText(t, "tree", "--flat")
	require.Equal(t, cli.ExitOK, flatOut.Code, flatOut.Stderr)
	assert.Contains(t, flatOut.Stdout, "TYPE")
	assert.Contains(t, flatOut.Stdout, cli.ShortID(agParent))
	assert.Contains(t, flatOut.Stdout, cli.ShortID(termID))

	// -q prints all tab IDs.
	qOut := runControlText(t, "tree", "-q")
	require.Equal(t, cli.ExitOK, qOut.Code, qOut.Stderr)
	assert.Contains(t, qOut.Stdout, agParent)
	assert.Contains(t, qOut.Stdout, agChild)
	assert.Contains(t, qOut.Stdout, termID)
}

func TestWhoami_EnrichesInsideTabContext(t *testing.T) {
	wsID := fullTestID("ws-ctx")
	agID := fullTestID("ag-self")
	parentID := fullTestID("ag-parent")
	tab := &leapmuxv1.WorkspaceTab{
		WorkspaceId: wsID, TileId: "tile-self", WorkerId: "worker-A",
		Tab: &leapmuxv1.TabIdent{TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}
	hub := &recordingHub{
		locateTab:  tab,
		workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "My Workspace"}},
	}
	worker := &phase2bWorkerDispatcher{
		workerName: "laptop",
		agents: []*leapmuxv1.AgentInfo{{
			Id: agID, Title: "Implementer", WorkingDir: "/repo/app",
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			ParentAgentId: parentID,
			GitStatus:     &leapmuxv1.GitRepoStatus{Branch: "main"},
		}},
	}
	startSpawnIPC(t, hub, worker)
	t.Setenv(controlenv.TabIDVariable, agID)
	t.Setenv(controlenv.TabTypeVariable, "agent")

	data := runControl(t, "whoami").Data(t)
	assert.Equal(t, wsID, data["workspace_id"])
	assert.Equal(t, "tile-self", data["tile_id"])
	assert.Equal(t, "laptop", data["worker_name"])
}

func TestDoctor_ReportsHealthyIPCAndWorkers(t *testing.T) {
	hub := &recordingHub{
		systemBuild: &leapmuxv1.BuildInfo{Version: "dev"},
		workers:     []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
	}
	worker := &phase2bWorkerDispatcher{
		workerName: "devbox",
		providers:  []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
		shells:     []string{"/bin/zsh", "/bin/bash"},
	}
	startSpawnIPC(t, hub, worker)

	out := runControl(t, "doctor")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	data := out.Data(t)
	assert.Equal(t, true, data["healthy"])
	workers, ok := data["workers"].([]any)
	require.True(t, ok)
	require.Len(t, workers, 1)
	w0 := workers[0].(map[string]any)
	assert.Equal(t, "devbox", w0["name"])
	assert.EqualValues(t, 1, w0["providers"])
	assert.EqualValues(t, 2, w0["shells"])
}

func TestLayoutTree_BoxDrawingAndWatchTextLine(t *testing.T) {
	tree := layoutTree{
		WorkspaceID: "ws-12345678",
		RootNodeID:  "split-1",
		Tree: &layoutNode{
			NodeID:    "split-1",
			Kind:      "split",
			Direction: "vertical",
			Ratios:    []float64{0.5, 0.5},
			Children: []*layoutNode{
				{NodeID: "leaf-left-1234", Kind: "leaf"},
				{NodeID: "leaf-right-5678", Kind: "leaf"},
			},
		},
		TabsByTile: map[string][]layoutTab{
			"leaf-left-1234": {{ID: "tab-abcd1234", Type: "agent"}},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, tree.RenderText(&buf))
	text := buf.String()
	assert.Contains(t, text, "└─ split")
	assert.Contains(t, text, "50% / 50%")
	assert.Contains(t, text, "[agent tab-abcd]")

	ev := layoutWatchEvent{data: eventToJSON(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Renamed{
			Renamed: &leapmuxv1.WorkspaceRenamed{WorkspaceId: "ws-1234567890", Title: "Renamed"},
		},
	})}
	assert.Contains(t, ev.TextLine(), `workspace_renamed ws-12345 "Renamed"`)
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"kind":"workspace_renamed"`)
}

func TestWithHubHint_SubstitutesRealHubURL(t *testing.T) {
	root := cli.Group("leapmux", "Root",
		cli.NewLeaf(cli.Leaf{
			Use:   "probe",
			Short: "Probe",
			Run: func(*cli.Invocation) (any, error) {
				return nil, fmt.Errorf("call: %w", connect.NewError(connect.CodeUnauthenticated, "expired"))
			},
		}))
	root.PersistentFlags().String("hub", "", "hub")
	addHubHints(root)

	err := cli.Run(context.Background(), root, []string{"probe", "--hub", "https://my-hub.example.com"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	got := cli.FromError(err)
	require.Equal(t, cli.CodeNotLoggedIn, got.Code)
	assert.Equal(t, []string{`run "leapmux auth login --hub https://my-hub.example.com" to sign in again`}, got.Hints)
}

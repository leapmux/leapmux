package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type phase3bHub struct {
	fn func(method string, body []byte) (proto.Message, error)
}

func (h *phase3bHub) CallInner(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error) {
	msg, err := h.fn(procedure, payload)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(msg)
}

type phase3bWorker struct {
	fn func(method string, body []byte) (proto.Message, error)
}

func (w *phase3bWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	msg, err := w.fn(req.GetMethod(), req.GetPayload())
	if err != nil {
		_ = rw.SendError(13, err.Error())
		return
	}
	writeProtoResponse(rw, msg)
}

func startPhase3BIPC(t *testing.T, hubFn func(string, []byte) (proto.Message, error), workerFn func(string, []byte) (proto.Message, error)) {
	t.Helper()
	var disp controlipc.LocalDispatcher
	if workerFn != nil {
		disp = &phase3bWorker{fn: workerFn}
	}
	startSpawnIPC(t, &recordingHub{}, disp, func(r *controlipc.Router) {
		r.Hub = &phase3bHub{fn: hubFn}
	})
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
}

func TestTunnelSpecParsingAndBridging(t *testing.T) {
	spec, err := parseTunnelForwardSpec("5432:127.0.0.1:5432")
	require.NoError(t, err)
	assert.Equal(t, tunnelForwardSpec{BindAddr: "127.0.0.1", LocalPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432}, spec)

	spec2, err := parseTunnelForwardSpec("0.0.0.0:8080:localhost:3000")
	require.NoError(t, err)
	assert.Equal(t, tunnelForwardSpec{BindAddr: "0.0.0.0", LocalPort: 8080, TargetHost: "localhost", TargetPort: 3000}, spec2)

	specIPv6, err := parseTunnelForwardSpec("[::1]:0:[::1]:22")
	require.NoError(t, err)
	assert.Equal(t, tunnelForwardSpec{BindAddr: "::1", LocalPort: 0, TargetHost: "::1", TargetPort: 22}, specIPv6)

	_, err = parseTunnelForwardSpec("bad")
	assert.Error(t, err)

	host, port, err := parseTunnelTarget("127.0.0.1:22")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, uint16(22), port)

	// Exercise serveTunnelForward end-to-end with an echo server over net.Pipe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- serveTunnelForward(ctx, ln, "remote.local", 9000, func(_ context.Context, h string, p uint16) (net.Conn, error) {
			assert.Equal(t, "remote.local", h)
			assert.Equal(t, uint16(9000), p)
			a, b := net.Pipe()
			go func() {
				defer func() { _ = b.Close() }()
				_, _ = io.Copy(b, b)
			}()
			return a, nil
		}, nil)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write([]byte("ping-tunnel"))
	require.NoError(t, err)
	buf := make([]byte, len("ping-tunnel"))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping-tunnel", string(buf))
	_ = conn.Close()
	cancel()
	require.NoError(t, <-done)

	// Exercise bridgeStdioToConn.
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()
	c2Done := make(chan struct{})
	go func() {
		defer close(c2Done)
		defer func() { _ = c2.Close() }()
		b := make([]byte, 5)
		_, _ = io.ReadFull(c2, b)
		_, _ = c2.Write([]byte("reply:" + string(b)))
	}()
	t.Cleanup(func() {
		_ = c1.Close()
		_ = c2.Close()
		<-c2Done
	})
	var out bytes.Buffer
	require.NoError(t, bridgeStdioToConn(t.Context(), bytes.NewBufferString("hello"), &out, c1))
	<-c2Done
	assert.Equal(t, "reply:hello", out.String())
}

func TestSectionAndWorkspaceArchiveMove(t *testing.T) {
	sections := []*leapmuxv1.Section{
		{Id: "sec-inprog", Name: "In progress", Position: "a", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT},
		{Id: "sec-arch", Name: "Archived", Position: "z", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT},
		{Id: "sec-custom", Name: "Client Work", Position: "m", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM, Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT},
	}
	items := []*leapmuxv1.SectionItem{
		{WorkspaceId: "ws-11111111", SectionId: "sec-inprog", Position: "a"},
	}

	var lastMove *leapmuxv1.MoveWorkspaceRequest
	var lastCreated *leapmuxv1.CreateSectionRequest
	var lastRenamed *leapmuxv1.RenameSectionRequest
	var lastDeleted *leapmuxv1.DeleteSectionRequest
	var lastMovedSec *leapmuxv1.MoveSectionRequest

	startPhase3BIPC(t, func(method string, body []byte) (proto.Message, error) {
		switch method {
		case leapmuxv1connect.SectionServiceListSectionsProcedure:
			return &leapmuxv1.ListSectionsResponse{Sections: sections, Items: items}, nil
		case leapmuxv1connect.SectionServiceCreateSectionProcedure:
			var req leapmuxv1.CreateSectionRequest
			if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
				return nil, err
			}
			lastCreated = &req
			return &leapmuxv1.CreateSectionResponse{
				Section: &leapmuxv1.Section{Id: "sec-new", Name: req.GetName(), Sidebar: req.GetSidebar(), Position: "n", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM},
			}, nil
		case leapmuxv1connect.SectionServiceRenameSectionProcedure:
			var req leapmuxv1.RenameSectionRequest
			if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
				return nil, err
			}
			lastRenamed = &req
			return &leapmuxv1.RenameSectionResponse{}, nil
		case leapmuxv1connect.SectionServiceMoveSectionProcedure:
			var req leapmuxv1.MoveSectionRequest
			if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
				return nil, err
			}
			lastMovedSec = &req
			return &leapmuxv1.MoveSectionResponse{}, nil
		case leapmuxv1connect.SectionServiceDeleteSectionProcedure:
			var req leapmuxv1.DeleteSectionRequest
			if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
				return nil, err
			}
			lastDeleted = &req
			return &leapmuxv1.DeleteSectionResponse{}, nil
		case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
			return &leapmuxv1.ListWorkspacesResponse{
				Workspaces: []*leapmuxv1.Workspace{{Id: "ws-11111111", Title: "Alpha"}},
			}, nil
		case leapmuxv1connect.WorkspaceServiceMoveWorkspaceProcedure:
			var req leapmuxv1.MoveWorkspaceRequest
			if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
				return nil, err
			}
			lastMove = &req
			return &leapmuxv1.MoveWorkspaceResponse{}, nil
		default:
			t.Errorf("unexpected hub method: %s", method)
			return nil, fmt.Errorf("unexpected hub method: %s", method)
		}
	}, nil)

	listData := runControl(t, "section", "list").Data(t)
	assert.Len(t, listData["sections"], 3)

	created := runControl(t, "section", "create", "Ops", "--sidebar", "right").Data(t)
	assert.Equal(t, "sec-new", created["id"])
	require.NotNil(t, lastCreated)
	assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, lastCreated.GetSidebar())

	runControl(t, "section", "rename", "Client Work", "Clients")
	require.NotNil(t, lastRenamed)
	assert.Equal(t, "sec-custom", lastRenamed.GetSectionId())
	assert.Equal(t, "Clients", lastRenamed.GetName())

	runControl(t, "section", "move", "Client Work", "--first")
	require.NotNil(t, lastMovedSec)
	assert.Equal(t, "sec-custom", lastMovedSec.GetSectionId())
	assert.NotEmpty(t, lastMovedSec.GetPosition())

	runControl(t, "section", "delete", "Client Work")
	require.NotNil(t, lastDeleted)
	assert.Equal(t, "sec-custom", lastDeleted.GetSectionId())

	arch := runControl(t, "workspace", "archive", "ws-11111111").Data(t)
	assert.Equal(t, true, arch["archived"])
	require.NotNil(t, lastMove)
	assert.Equal(t, "sec-arch", lastMove.GetSectionId())
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, lastMove.GetExpectedArchiveState())

	items[0].SectionId = "sec-arch"
	unarch := runControl(t, "workspace", "unarchive", "ws-11111111").Data(t)
	assert.Equal(t, false, unarch["archived"])
	assert.Equal(t, "sec-inprog", lastMove.GetSectionId())
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, lastMove.GetExpectedArchiveState())

	items[0].SectionId = "sec-inprog"
	moved := runControl(t, "workspace", "move", "ws-11111111", "--section", "Client Work", "--first").Data(t)
	assert.Equal(t, "sec-custom", moved["section_id"])
	assert.NotEmpty(t, lastMove.GetPosition())
}

func TestWindowLifecycleAndFileSet(t *testing.T) {
	var submitted []*leapmuxv1.CrdtOp
	mat := &leapmuxv1.UserMaterialized{
		UserId:       "u-1",
		CurrentEpoch: 1,
		Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
			"ws-11111111": {WorkspaceId: "ws-11111111", RootNodeId: "node-root"},
		},
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"node-root":   {NodeId: "node-root", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}},
			"node-fwroot": {NodeId: "node-fwroot", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}},
		},
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
			"fw-11111111": {
				WindowId:    "fw-11111111",
				WorkspaceId: &leapmuxv1.LWWString{Value: "ws-11111111"},
				RootNodeId:  "node-fwroot",
				X:           &leapmuxv1.LWWDouble{Value: 10},
				Y:           &leapmuxv1.LWWDouble{Value: 20},
				Width:       &leapmuxv1.LWWDouble{Value: 500},
				Height:      &leapmuxv1.LWWDouble{Value: 400},
				Opacity:     &leapmuxv1.LWWDouble{Value: 1},
			},
		},
		Tabs: map[string]*leapmuxv1.TabRecord{
			"file-tab-1111": {
				TabType:  leapmuxv1.TabType_TAB_TYPE_FILE,
				TabId:    "file-tab-1111",
				TileId:   &leapmuxv1.LWWString{Value: "node-root"},
				WorkerId: &leapmuxv1.LWWString{Value: "worker-A"},
			},
		},
	}

	startPhase3BIPC(t, func(method string, body []byte) (proto.Message, error) {
		switch method {
		case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
			return &leapmuxv1.ListWorkspacesResponse{Workspaces: []*leapmuxv1.Workspace{{Id: "ws-11111111", Title: "Main"}}}, nil
		case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
			return &leapmuxv1.ListTabsResponse{
				Tabs: []*leapmuxv1.WorkspaceTab{
					{
						Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-tab-1111"},
						WorkspaceId: "ws-11111111",
						TileId:      "node-root",
						WorkerId:    "worker-A",
					},
				},
			}, nil
		case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
			return &leapmuxv1.LocateTabResponse{
				Tab: &leapmuxv1.WorkspaceTab{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-tab-1111"},
					WorkspaceId: "ws-11111111",
					TileId:      "node-root",
					WorkerId:    "worker-A",
				},
			}, nil
		case leapmuxv1connect.UserCRDTGetMaterializedProcedure:
			return &leapmuxv1.GetMaterializedResponse{State: mat}, nil
		case leapmuxv1connect.UserCRDTSubmitOpsProcedure:
			var req leapmuxv1.SubmitOpsRequest
			if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
				return nil, err
			}
			submitted = nil
			for _, b := range req.GetBatches() {
				submitted = append(submitted, b.GetOps()...)
			}
			return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}}, nil
		default:
			t.Errorf("unexpected hub method: %s", method)
			return nil, fmt.Errorf("unexpected hub method: %s", method)
		}
	}, nil)

	listed := runControl(t, "window", "list").Data(t)
	assert.Len(t, listed["windows"], 1)

	created := runControl(t, "window", "create", "ws-11111111", "--x", "50", "--y", "60", "--width", "700", "--height", "500", "--opacity", "0.9").Data(t)
	assert.NotEmpty(t, created["id"])
	assert.Len(t, submitted, 8)

	runControl(t, "window", "move", "fw-11111111", "--x", "120", "--y", "140")
	assert.Len(t, submitted, 2)

	runControl(t, "window", "resize", "fw-11111111", "--width", "800", "--height", "600")
	assert.Len(t, submitted, 2)

	runControl(t, "window", "set-opacity", "fw-11111111", "0.75")
	assert.Len(t, submitted, 1)

	closed := runControl(t, "window", "close", "fw-11111111").Data(t)
	assert.Equal(t, true, closed["closed"])
	assert.GreaterOrEqual(t, len(submitted), 2)

	fset := runControl(t, "file", "set", "file-tab-1111", "--display-mode", "split", "--view-mode", "unified_diff", "--diff-base", "HEAD~1").Data(t)
	assert.Equal(t, "split", fset["display_mode"])
	assert.Equal(t, "unified_diff", fset["view_mode"])
	assert.Equal(t, "HEAD~1", fset["diff_base"])
	assert.Len(t, submitted, 3)
}

func TestSettingsWorkerPingQuakePSAndGitStatus(t *testing.T) {
	d, err := parseDetachByte("ctrl-]")
	require.NoError(t, err)
	assert.Equal(t, byte(0x1d), d)
	d2, err := parseDetachByte("ctrl-b")
	require.NoError(t, err)
	assert.Equal(t, byte(0x02), d2)
	_, err = parseDetachByte("bad")
	assert.Error(t, err)

	var sentInput *leapmuxv1.SendInputRequest
	var imgPayload *leapmuxv1.RegisterTabPayloadRequest

	startPhase3BIPC(t,
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerManagementServiceListWorkersProcedure:
				return &leapmuxv1.ListWorkersResponse{Workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}, nil
			case leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:
				return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A", Online: true}}, nil
			case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
				return &leapmuxv1.ListWorkspacesResponse{Workspaces: []*leapmuxv1.Workspace{{Id: "ws-11111111", Title: "Main"}}}, nil
			case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
				return &leapmuxv1.GetWorkspaceResponse{Workspace: &leapmuxv1.Workspace{Id: "ws-11111111", Title: "Main"}}, nil
			case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
				return &leapmuxv1.ListTabsResponse{
					Tabs: []*leapmuxv1.WorkspaceTab{
						{
							Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-11111111"},
							WorkspaceId: "ws-11111111",
							TileId:      "tile-1",
							WorkerId:    "worker-A",
						},
						{
							Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-11111111"},
							WorkspaceId: "ws-11111111",
							TileId:      "tile-1",
							WorkerId:    "worker-A",
						},
					},
				}, nil
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				var req leapmuxv1.LocateTabRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				return &leapmuxv1.LocateTabResponse{
					Tab: &leapmuxv1.WorkspaceTab{
						Tab:         &leapmuxv1.TabIdent{TabType: req.GetTabType(), TabId: req.GetTabId()},
						WorkspaceId: "ws-11111111",
						TileId:      "tile-1",
						WorkerId:    "worker-A",
					},
				}, nil
			case leapmuxv1connect.UserCRDTGetMaterializedProcedure:
				return &leapmuxv1.GetMaterializedResponse{
					State: &leapmuxv1.UserMaterialized{
						UserId:       "u-1",
						CurrentEpoch: 1,
						Workspaces:   map[string]*leapmuxv1.WorkspaceContentsRecord{"ws-11111111": {WorkspaceId: "ws-11111111", RootNodeId: "tile-1"}},
						Nodes:        map[string]*leapmuxv1.NodeRecord{"tile-1": {NodeId: "tile-1", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}}},
					},
				}, nil
			default:
				t.Errorf("unexpected hub method: %s", method)
				return nil, fmt.Errorf("unexpected hub method: %s", method)
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerSystemServicePingProcedure:
				return &leapmuxv1.PingResponse{}, nil
			case leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure:
				return &leapmuxv1.InspectTerminalProcessesResponse{
					Terminals: []*leapmuxv1.TerminalProcesses{
						{
							TerminalId: "term-11111111",
							Processes:  []*leapmuxv1.TerminalProcess{{Pid: 4242, Name: "go"}},
							TotalCount: 3,
						},
					},
				}, nil
			case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
				return &leapmuxv1.ListTerminalsResponse{
					Terminals: []*leapmuxv1.TerminalInfo{
						{
							TerminalId: "quake-11111111",
							Title:      "Quake",
							WorkingDir: "/repo",
							Cols:       80,
							Rows:       24,
							Screen:     []byte("quake-output\r\n"),
							Quake:      true,
							Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
						},
					},
				}, nil
			case leapmuxv1connect.WorkerTerminalServiceSendInputProcedure:
				var req leapmuxv1.SendInputRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				sentInput = &req
				return &leapmuxv1.SendInputResponse{}, nil
			case leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure:
				return &leapmuxv1.GetGitInfoResponse{
					IsGitRepo:        true,
					RepoRoot:         "/repo",
					MainWorktreeRoot: "/repo",
					CurrentBranch:    "main",
				}, nil
			case leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure:
				return &leapmuxv1.GetGitFileStatusResponse{
					RepoRoot: "/repo",
					Status: &leapmuxv1.GitRepoStatus{
						Branch:     "main",
						Ahead:      2,
						Behind:     1,
						Conflicted: true,
						Stashed:    true,
						Toplevel:   "/repo/wt",
					},
				}, nil
			case leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure:
				var req leapmuxv1.RegisterTabPayloadRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				imgPayload = &req
				return &leapmuxv1.RegisterTabPayloadResponse{
					Publication: &leapmuxv1.PublishedTab{
						Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabId: req.GetCreation().GetTabId()},
						WorkspaceId: "ws-11111111",
						TileId:      "tile-1",
						Position:    "a",
					},
				}, nil
			default:
				t.Errorf("unexpected worker method: %s", method)
				return nil, fmt.Errorf("unexpected worker method: %s", method)
			}
		},
	)

	// Worker ping
	ping := runControl(t, "worker", "ping", "worker-A").Data(t)
	assert.Equal(t, "worker-A", ping["id"])

	// Terminal ps
	ps := runControl(t, "terminal", "ps", "term-11111111").Data(t)
	procs := ps["processes"].([]any)
	require.Len(t, procs, 1)
	assert.Equal(t, "go", procs[0].(map[string]any)["name"])

	// Terminal quake get/read/run/send
	qg := runControl(t, "terminal", "quake", "get", "--dir", "/repo", "--worker", "worker-A").Data(t)
	assert.Equal(t, "quake-11111111", qg["id"])
	qr := runControl(t, "terminal", "quake", "read", "--dir", "/repo", "--worker", "worker-A").Data(t)
	assert.Equal(t, []any{"quake-output"}, qr["lines"])
	runControl(t, "terminal", "quake", "run", "echo hi", "--dir", "/repo", "--worker", "worker-A")
	require.NotNil(t, sentInput)
	assert.Equal(t, "quake-11111111", sentInput.GetTerminalId())
	assert.Equal(t, []byte("echo hi"), sentInput.GetData())
	runControl(t, "terminal", "quake", "send", "--key", "ctrl-c", "--dir", "/repo", "--worker", "worker-A")
	assert.Equal(t, []string{"ctrl-c"}, sentInput.GetKeys())

	// Rich git status
	gs := runControl(t, "git", "status", "--dir", "/repo", "--worker", "worker-A").Data(t)
	assert.Equal(t, float64(2), gs["ahead"])
	assert.Equal(t, float64(1), gs["behind"])
	assert.Equal(t, true, gs["conflicted"])
	assert.Equal(t, true, gs["stashed"])
	assert.Equal(t, "/repo/wt", gs["toplevel"])
	assert.Equal(t, "/repo", gs["main_worktree_root"])

	// tab open-image
	img := runControl(t, "tab", "open-image", "agent-11111111", "42", "--index", "2", "--title", "Diagram", "--workspace", "ws-11111111", "--worker", "worker-A").Data(t)
	assert.Equal(t, "image", img["type"])
	require.NotNil(t, imgPayload)
	assert.Equal(t, int64(42), imgPayload.GetPayload().GetImage().GetSeq())
	assert.Equal(t, int32(2), imgPayload.GetPayload().GetImage().GetImageIndex())
}

func TestEventsStream_ExpandedKindsAndPrivateEvents(t *testing.T) {
	clk := quartz.NewMock(t)
	var out bytes.Buffer
	p := &cli.Printer{Out: &out, Err: &bytes.Buffer{}, Format: cli.FormatJSON}
	s := newEventStream(eventStreamConfig{
		printer: &eventPrinter{p: p, filter: kindFilter{}},
		clock:   clk,
	})
	s.tabs["agent-1"] = scopedTab{ID: "agent-1", Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkspaceID: "ws-1", WorkerID: "w-1"}
	fs := &feedState{feed: &workerFeed{workerID: "w-1"}}

	require.NoError(t, s.onAgentEvent(fs, &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_GoalChanged{
			GoalChanged: &leapmuxv1.AgentGoalSnapshot{Goal: &leapmuxv1.AgentGoal{Objective: "Ship v2"}},
		},
	}))
	require.NoError(t, s.onAgentEvent(fs, &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_InputQueueRevision{InputQueueRevision: 7},
	}))
	require.NoError(t, s.onPrivateEvent(workerPrivateOutput{
		workerID: "w-1",
		event: &leapmuxv1.WorkerPrivateEvent{
			Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
				TabRenamed: &leapmuxv1.TabRenamed{TabId: "agent-1", Title: "Renamed"},
			},
		},
	}))
	require.NoError(t, s.onPrivateEvent(workerPrivateOutput{
		workerID: "w-1",
		event: &leapmuxv1.WorkerPrivateEvent{
			Event: &leapmuxv1.WorkerPrivateEvent_TabStatusChanged{
				TabStatusChanged: &leapmuxv1.TabStatusChanged{
					TabId:  "agent-1",
					Status: &leapmuxv1.TabStatus{Text: "Building"},
				},
			},
		},
	}))
	require.NoError(t, s.onPrivateEvent(workerPrivateOutput{
		workerID: "w-1",
		event: &leapmuxv1.WorkerPrivateEvent{
			Event: &leapmuxv1.WorkerPrivateEvent_UserNotification{
				UserNotification: &leapmuxv1.UserNotification{Id: "notif-1", Title: "Done", Body: "All tests pass"},
			},
		},
	}))
	require.NoError(t, s.onPrivateEvent(workerPrivateOutput{
		workerID: "w-1",
		event: &leapmuxv1.WorkerPrivateEvent{
			Event: &leapmuxv1.WorkerPrivateEvent_UserNotificationCleared{
				UserNotificationCleared: &leapmuxv1.UserNotificationCleared{Id: "notif-1"},
			},
		},
	}))

	text := out.String()
	assert.Contains(t, text, `"kind":"agent.goal"`)
	assert.Contains(t, text, `"kind":"agent.queue"`)
	assert.Contains(t, text, `"kind":"tab.renamed"`)
	assert.Contains(t, text, `"kind":"tab.status"`)
	assert.Contains(t, text, `"kind":"notification.created"`)
	assert.Contains(t, text, `"kind":"notification.cleared"`)
}

type phase3bDirectHub struct {
	leapmuxv1connect.UnimplementedAdminNetworkServiceHandler
	leapmuxv1connect.UnimplementedUserServiceHandler
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler
	lastNewPassword string
	lastDeregister  string
	lastKeyExtended string
	lastKeyRevoked  string
	lastKeyEmailed  string
}

func (h *phase3bDirectHub) GetListenStatus(_ context.Context, _ *leapmuxv1.GetListenStatusRequest) (*leapmuxv1.GetListenStatusResponse, error) {
	return &leapmuxv1.GetListenStatusResponse{
		BaseTcpAddresses:  []string{"127.0.0.1:4327"},
		Configured:        []string{"100.64.0.1:4327"},
		LocalIpcAddresses: []string{"unix:/tmp/hub.sock"},
		Bound: []*leapmuxv1.BoundAddress{
			{Address: "127.0.0.1:4327", Source: "listen"},
			{Address: "100.64.0.1:4327", Source: "extra"},
		},
		Interfaces: []*leapmuxv1.NetworkInterface{
			{Name: "lo0", Up: true, Addresses: []*leapmuxv1.NetworkInterfaceAddress{{Ip: "127.0.0.1", Loopback: true}}},
		},
	}, nil
}

func (h *phase3bDirectHub) ChangePassword(_ context.Context, req *leapmuxv1.ChangePasswordRequest) (*leapmuxv1.ChangePasswordResponse, error) {
	h.lastNewPassword = req.GetNewPassword()
	return &leapmuxv1.ChangePasswordResponse{User: &leapmuxv1.User{Id: "u-1", Username: "alice"}}, nil
}

func (h *phase3bDirectHub) ListWorkers(_ context.Context, _ *leapmuxv1.ListWorkersRequest) (*leapmuxv1.ListWorkersResponse, error) {
	return &leapmuxv1.ListWorkersResponse{Workers: []*leapmuxv1.Worker{{Id: "worker-old1", Online: false}}}, nil
}

func (h *phase3bDirectHub) DeregisterWorker(_ context.Context, req *leapmuxv1.DeregisterWorkerRequest) (*leapmuxv1.DeregisterWorkerResponse, error) {
	h.lastDeregister = req.GetWorkerId()
	return &leapmuxv1.DeregisterWorkerResponse{}, nil
}

func (h *phase3bDirectHub) ExtendRegistrationKey(_ context.Context, req *leapmuxv1.ExtendRegistrationKeyRequest) (*leapmuxv1.ExtendRegistrationKeyResponse, error) {
	h.lastKeyExtended = req.GetRegistrationKey()
	return &leapmuxv1.ExtendRegistrationKeyResponse{ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute))}, nil
}

func (h *phase3bDirectHub) RevokeRegistrationKey(_ context.Context, req *leapmuxv1.RevokeRegistrationKeyRequest) (*leapmuxv1.RevokeRegistrationKeyResponse, error) {
	h.lastKeyRevoked = req.GetRegistrationKey()
	return &leapmuxv1.RevokeRegistrationKeyResponse{}, nil
}

func (h *phase3bDirectHub) EmailRegistrationInstructions(_ context.Context, req *leapmuxv1.EmailRegistrationInstructionsRequest) (*leapmuxv1.EmailRegistrationInstructionsResponse, error) {
	h.lastKeyEmailed = req.GetRegistrationKey()
	return &leapmuxv1.EmailRegistrationInstructionsResponse{}, nil
}

func TestAdminNetworkAuthChangePasswordAndWorkerKeys(t *testing.T) {
	h := &phase3bDirectHub{}
	mux := http.NewServeMux()
	cs := connect.NewServer()
	leapmuxv1connect.RegisterAdminNetworkServiceHandler(cs, h)
	leapmuxv1connect.RegisterUserServiceHandler(cs, h)
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(cs, h)
	connecthttp.Mount(mux, cs, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_CONTROL_TOKEN", "")
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_test_token",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserID:      "u-1",
		Username:    "alice",
	}))

	netData := runControl(t, "admin", "network", "status", "--hub", srv.URL).Data(t)
	assert.Len(t, netData["bound"], 2)
	assert.Equal(t, []any{"unix:/tmp/hub.sock"}, netData["local_ipc_addresses"])

	pwData := runControl(t, "auth", "change-password", "--password", "s3cr3t-pass!", "--hub", srv.URL).Data(t)
	assert.Equal(t, true, pwData["changed"])
	assert.Equal(t, "s3cr3t-pass!", h.lastNewPassword)

	dereg := runControl(t, "worker", "deregister", "worker-old1", "--hub", srv.URL).Data(t)
	assert.Equal(t, true, dereg["deregistered"])
	assert.Equal(t, "worker-old1", h.lastDeregister)

	ext := runControl(t, "worker", "key", "extend", "lmx_reg_123", "--hub", srv.URL).Data(t)
	assert.Equal(t, "lmx_reg_123", ext["key"])
	assert.Equal(t, "lmx_reg_123", h.lastKeyExtended)

	rev := runControl(t, "worker", "key", "revoke", "lmx_reg_123", "--hub", srv.URL).Data(t)
	assert.Equal(t, true, rev["revoked"])
	assert.Equal(t, "lmx_reg_123", h.lastKeyRevoked)

	em := runControl(t, "worker", "key", "email", "lmx_reg_123", "--hub", srv.URL).Data(t)
	assert.Equal(t, true, em["sent"])
	assert.Equal(t, "lmx_reg_123", h.lastKeyEmailed)
}

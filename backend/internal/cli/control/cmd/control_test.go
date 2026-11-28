package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestWorkerScope_WhitespaceEnvAndAmbientTabFallback(t *testing.T) {
	t.Run("whitespace-only LEAPMUX_CONTROL_WORKER_ID falls back to sole online worker", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.WorkerIDVariable, "   \n\t  ")

		out := runControl(t, "worker", "info").ok(t)
		assert.Equal(t, "worker-A", envelopeData(t, out)["id"])
	})

	t.Run("blank worker id with ambient LEAPMUX_CONTROL_TAB_ID derives worker from calling tab", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			// Two online workers ensure autoSelectSoleWorker cannot guess.
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
				{Id: "worker-B", Online: true},
			},
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-self"},
				TileId:      "tile-1",
				WorkerId:    "worker-A",
				WorkspaceId: "ws-1",
			},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.WorkerIDVariable, "  ")
		t.Setenv(controlenv.TabIDVariable, "tab-self")

		out := runControl(t, "worker", "info").ok(t)
		assert.Equal(t, "worker-A", envelopeData(t, out)["id"])
		assert.Contains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	})

	t.Run("unlocatable ambient LEAPMUX_CONTROL_TAB_ID falls back to sole online worker", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			workers:        []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
			unlocatableTab: true,
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.TabIDVariable, "quake-1")

		out := runControl(t, "worker", "info").ok(t)
		assert.Equal(t, "worker-A", envelopeData(t, out)["id"])
	})

	t.Run("hub outage on ambient LEAPMUX_CONTROL_TAB_ID surfaces the error", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			workers:      []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
			locateTabErr: connect.NewError(connect.CodeUnavailable, "hub unreachable"),
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.TabIDVariable, "tab-self")

		f := runControl(t, "worker", "info").requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "hub unreachable")
	})
}

func TestWorkerWatchTransport_Call(t *testing.T) {
	t.Run("hub mode reuses the open E2EE channel without a second handshake", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		harness := startHubChannelHarness(t, &recordingHub{}, disp)
		peer, err := control.NewClientFromEnv(harness.url)
		require.NoError(t, err)
		_, isHub := peer.(*control.HubClient)
		require.True(t, isHub)

		wt, err := newWorkerWatchTransport(t.Context(), peer, "worker-A")
		require.NoError(t, err)
		defer wt.close()
		require.Equal(t, 1, harness.channelOpens())

		var first, second leapmuxv1.GetWorkerSystemInfoResponse
		require.NoError(t, wt.Call(t.Context(), leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &first))
		require.NoError(t, wt.Call(t.Context(), leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &second))
		assert.Equal(t, "build-box", first.GetName())
		assert.Equal(t, "build-box", second.GetName())
		assert.Equal(t, 1, harness.channelOpens(), "Call must reuse the transport's open E2EE channel")
	})

	t.Run("hub mode preserves in-band worker error codes", func(t *testing.T) {
		harness := startHubChannelHarness(t, &recordingHub{}, inBandErrorDispatcher{
			code:    codes.NotFound,
			message: "no such agent",
		})
		peer, err := control.NewClientFromEnv(harness.url)
		require.NoError(t, err)

		wt, err := newWorkerWatchTransport(t.Context(), peer, "worker-A")
		require.NoError(t, err)
		defer wt.close()

		var resp leapmuxv1.ListAgentsResponse
		err = wt.Call(t.Context(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{}, &resp)
		require.Error(t, err)
		var coded *codedRPCError
		require.True(t, errors.As(err, &coded))
		assert.Equal(t, "rpc_error", coded.Code)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
		assert.Equal(t, 1, harness.channelOpens())
	})

	t.Run("local IPC mode routes unary calls and preserves in-band errors", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		startSpawnIPC(t, &recordingHub{}, disp)
		peer, err := control.NewClientFromEnv("")
		require.NoError(t, err)
		_, isIPC := peer.(*control.WorkerIPCClient)
		require.True(t, isIPC)

		wt, err := newWorkerWatchTransport(t.Context(), peer, "worker-A")
		require.NoError(t, err)
		defer wt.close()

		var resp leapmuxv1.GetWorkerSystemInfoResponse
		require.NoError(t, wt.Call(t.Context(), leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp))
		assert.Equal(t, "build-box", resp.GetName())

		// Verify in-band refusal over local IPC as well.
		startSpawnIPC(t, &recordingHub{}, inBandErrorDispatcher{code: codes.PermissionDenied, message: "denied"})
		peerDenied, err := control.NewClientFromEnv("")
		require.NoError(t, err)
		wtDenied, err := newWorkerWatchTransport(t.Context(), peerDenied, "worker-A")
		require.NoError(t, err)
		defer wtDenied.close()

		err = wtDenied.Call(t.Context(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{}, &leapmuxv1.ListAgentsResponse{})
		require.Error(t, err)
		assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
	})

	t.Run("nil receiver fails with usage error without panicking", func(t *testing.T) {
		var nilTransport *workerWatchTransport
		err := nilTransport.Call(t.Context(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{}, nil)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})
}

func TestResolveTab_WorkspaceScopeRefusesCrossWorkspaceTabs(t *testing.T) {
	t.Run("ambient self tab in another workspace is refused", func(t *testing.T) {
		hub := &recordingHub{
			workspaces: []*leapmuxv1.Workspace{
				{Id: "ws-1", Title: "One"},
				{Id: "ws-other", Title: "Other"},
			},
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2"},
				TileId:      "tile-2",
				WorkerId:    "worker-A",
				WorkspaceId: "ws-other",
			},
		}
		startSpawnIPC(t, hub, nil)
		t.Setenv(controlenv.TabIDVariable, "agent-2")

		f := runControl(t, "tab", "get", "--workspace", "title:One").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "agent-2")
		assert.Contains(t, f.Message, "ws-1")
	})

	t.Run("explicit id: tab in another workspace is refused", func(t *testing.T) {
		hub := &recordingHub{
			workspaces: []*leapmuxv1.Workspace{
				{Id: "ws-1", Title: "One"},
				{Id: "ws-other", Title: "Other"},
			},
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2"},
				TileId:      "tile-2",
				WorkerId:    "worker-A",
				WorkspaceId: "ws-other",
			},
		}
		startSpawnIPC(t, hub, nil)

		f := runControl(t, "tab", "get", "id:agent-2", "--workspace", "title:One").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "agent-2")
		assert.Contains(t, f.Message, "ws-1")
	})
}

type hubErrorStub struct {
	leapmuxv1connect.UnimplementedWorkspaceServiceHandler
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler
	leapmuxv1connect.UnimplementedSectionServiceHandler

	getWorkspaceErr   error
	listWorkspacesErr error
	listWorkersErr    error
	listSectionsErr   error
	workspaces        []*leapmuxv1.Workspace
}

func (h *hubErrorStub) GetWorkspace(_ context.Context, req *leapmuxv1.GetWorkspaceRequest) (*leapmuxv1.GetWorkspaceResponse, error) {
	if h.getWorkspaceErr != nil {
		return nil, h.getWorkspaceErr
	}
	for _, ws := range h.workspaces {
		if ws.GetId() == req.GetWorkspaceId() {
			return &leapmuxv1.GetWorkspaceResponse{Workspace: ws}, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, "workspace not found")
}

func (h *hubErrorStub) ListWorkspaces(_ context.Context, _ *leapmuxv1.ListWorkspacesRequest) (*leapmuxv1.ListWorkspacesResponse, error) {
	if h.listWorkspacesErr != nil {
		return nil, h.listWorkspacesErr
	}
	return &leapmuxv1.ListWorkspacesResponse{Workspaces: h.workspaces}, nil
}

func (h *hubErrorStub) ListTabs(_ context.Context, _ *leapmuxv1.ListTabsRequest) (*leapmuxv1.ListTabsResponse, error) {
	return &leapmuxv1.ListTabsResponse{}, nil
}

func (h *hubErrorStub) ListWorkers(_ context.Context, _ *leapmuxv1.ListWorkersRequest) (*leapmuxv1.ListWorkersResponse, error) {
	if h.listWorkersErr != nil {
		return nil, h.listWorkersErr
	}
	return &leapmuxv1.ListWorkersResponse{Page: &leapmuxv1.PageResponse{}}, nil
}

func (h *hubErrorStub) ListSections(_ context.Context, _ *leapmuxv1.ListSectionsRequest) (*leapmuxv1.ListSectionsResponse, error) {
	if h.listSectionsErr != nil {
		return nil, h.listSectionsErr
	}
	return &leapmuxv1.ListSectionsResponse{}, nil
}

func startHubErrorStub(t *testing.T, stub *hubErrorStub) *control.HubClient {
	t.Helper()
	isolateCLIEnv(t)
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkspaceServiceHandler(server, stub)
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(server, stub)
	leapmuxv1connect.RegisterSectionServiceHandler(server, stub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := hubtransporttest.NewServer(t, mux)
	t.Setenv("LEAPMUX_HUB", srv.URL)
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_test_secret", UserID: "u-test",
	}))
	c, err := control.NewClientOrAnonymous(srv.URL)
	require.NoError(t, err)
	return c
}

func TestListWorkspaceTabs_RejectsMissingExplicitWorkspaceID(t *testing.T) {
	startHubErrorStub(t, &hubErrorStub{
		workspaces: []*leapmuxv1.Workspace{{Id: "ws-existing", Title: "Existing"}},
	})

	f := runControl(t, "tab", "list", "--workspace", "id:ws-missing").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, "ws-missing")
}

func TestSessionHelpers_PropagateHubErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     connect.Code
		wantCode cli.Code
	}{
		{"unavailable", connect.CodeUnavailable, cli.CodeUnavailable},
		{"unauthenticated", connect.CodeUnauthenticated, cli.CodeNotLoggedIn},
	} {
		t.Run("autoSelectSoleWorker/"+tc.name, func(t *testing.T) {
			c := startHubErrorStub(t, &hubErrorStub{
				listWorkersErr: connect.NewError(tc.code, "workers failed"),
			})
			s := &session{ctx: t.Context(), c: c}
			_, err := s.autoSelectSoleWorker()
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, cli.FromError(err).Code)
		})

		t.Run("defaultWorkspaceID/ListWorkspaces/"+tc.name, func(t *testing.T) {
			c := startHubErrorStub(t, &hubErrorStub{
				listWorkspacesErr: connect.NewError(tc.code, "workspaces failed"),
			})
			s := &session{ctx: t.Context(), c: c}
			_, err := s.defaultWorkspaceID()
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, cli.FromError(err).Code)
		})

		t.Run("defaultWorkspaceID/ListSections/"+tc.name, func(t *testing.T) {
			ipc := &scriptedIPC{
				callInner: func(req *leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error) {
					switch req.GetMethod() {
					case "hub." + leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
						return marshalReply(t, &leapmuxv1.ListWorkspacesResponse{
							Workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
						}), nil
					case "hub." + leapmuxv1connect.SectionServiceListSectionsProcedure:
						return nil, connect.NewError(tc.code, "sections failed")
					default:
						return nil, connect.NewError(connect.CodeUnimplemented, req.GetMethod())
					}
				},
			}
			c := startScriptedIPC(t, ipc)
			s := &session{ctx: t.Context(), c: c}
			_, err := s.defaultWorkspaceID()
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, cli.FromError(err).Code)
		})
	}
}

func TestHasTargetWorkspaceScope_UsesFlagAnnotation(t *testing.T) {
	root := NewCommand()
	for _, path := range [][]string{
		{"tab", "get"},
		{"agent", "send"},
		{"agent", "requests"},
		{"agent", "search"},
		{"terminal", "ps"},
		{"terminal", "get"},
	} {
		cmd, _, err := root.Find(path)
		require.NoError(t, err)
		require.NoError(t, cmd.Flags().Set("workspace", "ws-1"))
		s := &session{inv: &cli.Invocation{Command: cmd}}
		assert.True(t, s.hasTargetWorkspaceScope(), "expected target workspace scope on %v", path)
	}

	for _, path := range [][]string{
		{"tab", "list"},
		{"tab", "move"},
		{"window", "move"},
		{"notify"},
		{"context", "use"},
		{"tree"},
		{"workspace", "apply"},
		{"events"},
		{"agent", "open"},
		{"agent", "fork"},
		{"agent", "list"},
		{"terminal", "open"},
		{"terminal", "list"},
		{"file", "open"},
	} {
		cmd, _, err := root.Find(path)
		require.NoError(t, err)
		require.NoError(t, cmd.Flags().Set("workspace", "ws-1"))
		s := &session{inv: &cli.Invocation{Command: cmd}}
		assert.False(t, s.hasTargetWorkspaceScope(), "must NOT treat --workspace as target scope on %v", path)
	}
}

func TestSessionDepsLocateTab_HandlesNilCacheSafely(t *testing.T) {
	hub := &recordingHub{
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"},
			TileId:      "tile-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
	}
	startSpawnIPC(t, hub, nil)
	c, err := control.NewClientFromEnv("")
	require.NoError(t, err)

	// Zero-value located cache must not panic when deps().LocateTab caches a hit.
	s := &session{ctx: t.Context(), c: c}
	var row *leapmuxv1.WorkspaceTab
	require.NotPanics(t, func() {
		row, err = s.deps().LocateTab(t.Context(), resolve.KindAgent.TabType(), "agent-1")
	})
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, "agent-1", row.GetTab().GetTabId())

	// Second call hits cache; invalidateLocatedTab forces a fresh hub lookup.
	_, err = s.deps().LocateTab(t.Context(), resolve.KindAgent.TabType(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 1, countOf(hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure))

	s.invalidateLocatedTab("agent-1")
	_, err = s.deps().LocateTab(t.Context(), resolve.KindAgent.TabType(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 2, countOf(hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure))
}

func TestResolveChildAgents_FallsBackToLocalIPCWhoami(t *testing.T) {
	parentID := fullTestID("parent-whoami")
	childID := fullTestID("child-whoami")

	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: childID, ParentAgentId: parentID, Title: "Child", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "tile-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: childID}, TileId: "tile-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	ipc := &scriptedIPC{
		callInner: func(req *leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error) {
			if req.GetMethod() == "worker."+leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
				return marshalReply(t, &leapmuxv1.ListAgentsResponse{Agents: w.agents}), nil
			}
			return nil, connect.NewError(connect.CodeUnimplemented, req.GetMethod())
		},
	}
	_ = hub
	c := startScriptedIPCWithWhoami(t, ipc, &leapmuxv1.WhoamiResponse{
		TabId:    parentID,
		TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkerId: "worker-A",
	})
	t.Setenv(controlenv.TabIDVariable, "")
	t.Setenv(controlenv.TabTypeVariable, "")
	t.Setenv(controlenv.WorkerIDVariable, "")

	s := &session{ctx: t.Context(), c: c, located: newLocatedTabCache()}
	assert.Equal(t, parentID, s.selfTabID())

	targets, err := s.resolveVariadicTabs(resolve.KindAgent, []string{"children"}, resolve.Need{WorkerID: true})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, childID, targets[0].ID)
	assert.Equal(t, "worker-A", targets[0].WorkerID)
}

type whoamiScriptedIPC struct {
	*scriptedIPC
	whoami *leapmuxv1.WhoamiResponse
}

func (w *whoamiScriptedIPC) Whoami(_ context.Context, _ *leapmuxv1.WhoamiRequest) (*leapmuxv1.WhoamiResponse, error) {
	return w.whoami, nil
}

func startScriptedIPCWithWhoami(t *testing.T, base *scriptedIPC, whoami *leapmuxv1.WhoamiResponse) *control.WorkerIPCClient {
	t.Helper()
	isolateCLIEnv(t)
	server := connect.NewServer()
	leapmuxv1connect.RegisterControlIPCServiceHandler(server, &whoamiScriptedIPC{scriptedIPC: base, whoami: whoami})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	socketURL := hubtransporttest.NewSocketServer(t, "ctrl-whoami", mux)
	c, err := control.NewLocalClient(socketURL, "ipc-token")
	require.NoError(t, err)
	return c
}

func TestSectionIndex_LookupAndArchiveState(t *testing.T) {
	var nilIdx *sectionIndex
	assert.Nil(t, nilIdx.sectionOf("ws-1"))
	assert.False(t, nilIdx.isArchived("ws-1"))
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, nilIdx.archiveState("ws-1"))

	idx := indexSections(&leapmuxv1.ListSectionsResponse{
		Sections: []*leapmuxv1.Section{
			{Id: "sec-main", Name: "In progress", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS},
			{Id: "sec-arch", Name: "Archived", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED},
		},
		Items: []*leapmuxv1.SectionItem{
			{WorkspaceId: "ws-active", SectionId: "sec-main"},
			{WorkspaceId: "ws-old", SectionId: "sec-arch"},
		},
	})
	assert.Equal(t, "sec-main", idx.sectionOf("ws-active").GetId())
	assert.False(t, idx.isArchived("ws-active"))
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, idx.archiveState("ws-active"))
	assert.True(t, idx.isArchived("ws-old"))
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, idx.archiveState("ws-old"))
	assert.Nil(t, idx.sectionOf("ws-unassigned"))
}

func TestNoticeDiscoveredLocalHub_UsesHubSourceProvenance(t *testing.T) {
	const localHub = "unix:/tmp/solo/hub/hub.sock"

	t.Run("prints notice only for HubSourceDiscovered in text mode", func(t *testing.T) {
		var stderr bytes.Buffer
		inv := &cli.Invocation{
			Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: cli.FormatText},
		}
		noticeDiscoveredLocalHub(inv, &control.HubClient{
			HubURL: localHub,
			Source: control.HubSourceDiscovered,
		})
		assert.Contains(t, stderr.String(), "using discovered local Hub "+localHub+" (override with --hub or LEAPMUX_HUB)")
	})

	t.Run("suppresses notice for non-discovered sources", func(t *testing.T) {
		cases := []struct {
			name   string
			format cli.Format
			peer   control.Peer
		}{
			{name: "json format", format: cli.FormatJSON, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceDiscovered}},
			{name: "explicit flag", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceFlag}},
			{name: "env var", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceEnv}},
			{name: "saved context", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceContext}},
			{name: "sole credential", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceSingleCredential}},
			{name: "unspecified source", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceUnspecified}},
			{name: "worker IPC peer", format: cli.FormatText, peer: &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}},
			{name: "nil peer", format: cli.FormatText, peer: nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var stderr bytes.Buffer
				inv := &cli.Invocation{
					Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: tc.format},
				}
				noticeDiscoveredLocalHub(inv, tc.peer)
				assert.Empty(t, stderr.String())
			})
		}
	})
}

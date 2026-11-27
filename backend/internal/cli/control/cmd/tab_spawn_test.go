package cmd

import (
	"context"
	"regexp"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type creationPublicationDispatcher struct {
	mu             sync.Mutex
	calls          []recordedWorkerCall
	failOpen       bool
	invalidReceipt string
}

func (d *creationPublicationDispatcher) DispatchWith(_ context.Context, _ channel.Caller, r *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	var request proto.Message
	var creation *leapmuxv1.TabCreation
	var kind leapmuxv1.TabType
	switch r.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
		request = &leapmuxv1.OpenAgentRequest{}
		kind = leapmuxv1.TabType_TAB_TYPE_AGENT
	case leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure:
		request = &leapmuxv1.OpenTerminalRequest{}
		kind = leapmuxv1.TabType_TAB_TYPE_TERMINAL
	case leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure:
		request = &leapmuxv1.RegisterTabPayloadRequest{}
		kind = leapmuxv1.TabType_TAB_TYPE_FILE
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method")
		return
	}
	if err := proto.Unmarshal(r.GetPayload(), request); err != nil {
		_ = w.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	d.mu.Lock()
	d.calls = append(d.calls, recordedWorkerCall{method: r.GetMethod(), in: request})
	d.mu.Unlock()
	switch request := request.(type) {
	case *leapmuxv1.OpenAgentRequest:
		creation = request.GetCreation()
	case *leapmuxv1.OpenTerminalRequest:
		creation = request.GetCreation()
	case *leapmuxv1.RegisterTabPayloadRequest:
		creation = request.GetCreation()
	}
	if creation.GetPlacement() == nil {
		_ = w.SendError(int32(codes.InvalidArgument), "creation intent is required")
		return
	}
	if d.failOpen {
		_ = w.SendError(int32(codes.Unavailable), "publication outcome remains unknown")
		return
	}
	publication := &leapmuxv1.PublishedTab{Tab: &leapmuxv1.TabIdent{TabType: kind, TabId: creation.GetTabId()}, WorkspaceId: creation.GetPlacement().GetWorkspaceId(), TileId: "root-1", Position: "hub-rank"}
	switch d.invalidReceipt {
	case "id":
		publication.Tab.TabId = "different"
	case "type":
		publication.Tab.TabType = leapmuxv1.TabType_TAB_TYPE_IMAGE
	case "workspace":
		publication.WorkspaceId = "different"
	case "tile":
		publication.TileId = ""
	case "position":
		publication.Position = ""
	}
	var response proto.Message
	switch kind {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		response = &leapmuxv1.OpenAgentResponse{Agent: &leapmuxv1.AgentInfo{Id: creation.GetTabId(), WorkerId: "worker-A"}, Publication: publication}
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		response = &leapmuxv1.OpenTerminalResponse{TerminalId: creation.GetTabId(), Publication: publication}
	default:
		response = &leapmuxv1.RegisterTabPayloadResponse{Publication: publication}
	}
	payload, err := proto.Marshal(response)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
}

func (d *creationPublicationDispatcher) recorded() []recordedWorkerCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recordedWorkerCall(nil), d.calls...)
}

func TestTabCreationCLI_PassesPlacementAndUsesTheWorkerReceipt(t *testing.T) {
	for _, transport := range tabOpenTransports {
		for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
			for _, spec := range []positionSpec{{kind: positionFirst}, {kind: positionLast}, {kind: positionBefore, refID: "anchor"}, {kind: positionAfter, refID: "anchor"}} {
				t.Run(transport.name+"/"+kind.String()+"/"+flagLabelForPositionKind(spec.kind), func(t *testing.T) {
					state := newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").tabAt("anchor", "root-1", "m", leapmuxv1.TabType_TAB_TYPE_AGENT).st
					hub, dispatcher := &recordingHub{materialized: state}, &creationPublicationDispatcher{}
					hubFlag := transport.start(t, hub, dispatcher)
					client, err := control.NewClientFromEnv(hubFlag)
					require.NoError(t, err)
					var resultID, resultTile, resultPosition string
					openErr := withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
						if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
							result, err := openAgentAndAddTab(t.Context(), client, worker, openAgentArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1", WorkingDir: "/src", Position: spec})
							if err == nil {
								resultID, resultTile, resultPosition = result.Agent.GetId(), result.TileID, result.Position
							}
							return err
						}
						result, err := openTerminalAndAddTab(t.Context(), client, worker, openTerminalArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1", WorkingDir: "/src", Position: spec})
						if err == nil {
							resultID, resultTile, resultPosition = result.TerminalID, result.TileID, result.Position
						}
						return err
					})
					require.NoError(t, openErr)
					calls := dispatcher.recorded()
					require.Len(t, calls, 1)
					var creation *leapmuxv1.TabCreation
					if request, ok := calls[0].in.(*leapmuxv1.OpenAgentRequest); ok {
						creation = request.GetCreation()
					} else {
						creation = calls[0].in.(*leapmuxv1.OpenTerminalRequest).GetCreation()
					}
					require.NotNil(t, creation)
					assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9]{48}$`), creation.GetTabId())
					assert.Equal(t, creation.GetTabId(), resultID)
					assert.Equal(t, "ws-1", creation.GetPlacement().GetWorkspaceId())
					assert.Equal(t, "root-1", creation.GetPlacement().GetTileId())
					assert.Equal(t, spec.refID, creation.GetPlacement().GetRelativeTabId())
					expected := map[positionKind]leapmuxv1.TabInsertion{positionFirst: leapmuxv1.TabInsertion_TAB_INSERTION_FIRST, positionLast: leapmuxv1.TabInsertion_TAB_INSERTION_LAST, positionBefore: leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, positionAfter: leapmuxv1.TabInsertion_TAB_INSERTION_AFTER}
					assert.Equal(t, expected[spec.kind], creation.GetPlacement().GetInsertion())
					assert.Equal(t, "root-1", resultTile)
					assert.Equal(t, "hub-rank", resultPosition)
					assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
				})
			}
		}
	}
}

func TestTabCreationCLI_RejectsAMismatchedOrIncompleteReceipt(t *testing.T) {
	for _, transport := range tabOpenTransports {
		for _, invalid := range []string{"id", "type", "workspace", "tile", "position"} {
			t.Run(transport.name+"/"+invalid, func(t *testing.T) {
				dispatcher := &creationPublicationDispatcher{invalidReceipt: invalid}
				hub := &recordingHub{}
				client, err := control.NewClientFromEnv(transport.start(t, hub, dispatcher))
				require.NoError(t, err)
				openErr := withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
					_, err := openTerminalAndAddTab(t.Context(), client, worker, openTerminalArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1"})
					return err
				})
				assert.Equal(t, "internal", failureOf(t, openErr).Error["code"])
				require.Len(t, dispatcher.recorded(), 1, "a malformed reply does not authorize compensating close")
				assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
			})
		}
	}
}

func TestTabCreationCLI_PreservesAnUncertainWorkerOperation(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			dispatcher, hub := &creationPublicationDispatcher{failOpen: true}, &recordingHub{}
			client, err := control.NewClientFromEnv(transport.start(t, hub, dispatcher))
			require.NoError(t, err)
			openErr := withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
				_, err := openAgentAndAddTab(t.Context(), client, worker, openAgentArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1"})
				return err
			})
			assert.Contains(t, failureOf(t, openErr).Error["message"], "publication outcome remains unknown")
			calls := dispatcher.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, calls[0].method)
			assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9]{48}$`), calls[0].in.(*leapmuxv1.OpenAgentRequest).GetCreation().GetTabId())
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
		})
	}
}

func TestTabCreationCLI_SendsTheInitialMessageWithTheCreation(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			dispatcher, hub := &creationPublicationDispatcher{}, &recordingHub{}
			client, err := control.NewClientFromEnv(transport.start(t, hub, dispatcher))
			require.NoError(t, err)
			var result *openAgentResult
			err = withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
				var openErr error
				result, openErr = openAgentAndAddTab(t.Context(), client, worker, openAgentArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1", InitialMessage: new("inspect the workspace")})
				return openErr
			})
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, "hub-rank", result.Position)
			calls := dispatcher.recorded()
			require.Len(t, calls, 1)
			open := calls[0].in.(*leapmuxv1.OpenAgentRequest)
			assert.Equal(t, open.GetCreation().GetTabId(), result.Agent.GetId())
			assert.Equal(t, "inspect the workspace", open.GetInitialMessage())
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
		})
	}
}

func TestTabCreationCLI_PublishesFilePresentationWithTheEncryptedPayload(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			dispatcher := &creationPublicationDispatcher{}
			hub := &recordingHub{workers: workerRows("worker-A"), materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st}
			hubFlag := transport.start(t, hub, dispatcher)
			args := []string{"file", "open", "/private/report.txt", "--workspace", "ws-1", "--worker", "worker-A", "--tile", "root-1", "--dir", "/private", "--display-mode", "source", "--view-mode", "head", "--first"}
			o := runControl(t, append(args, testHubArgs(hubFlag)...)...)
			require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
			data := o.Data(t)
			assert.Equal(t, "hub-rank", data["position"])
			assert.Equal(t, "/private/report.txt", data["path"])
			calls := dispatcher.recorded()
			require.Len(t, calls, 1)
			request := calls[0].in.(*leapmuxv1.RegisterTabPayloadRequest)
			assert.Equal(t, "/private/report.txt", request.GetPayload().GetFile().GetFilePath())
			assert.Equal(t, "/private", request.GetPayload().GetWorkingDir())
			assert.Equal(t, leapmuxv1.TabInsertion_TAB_INSERTION_FIRST, request.GetCreation().GetPlacement().GetInsertion())
			assert.Equal(t, leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_SOURCE, request.GetCreation().GetPresentation().GetDisplayMode())
			assert.Equal(t, leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_HEAD, request.GetCreation().GetPresentation().GetFileViewMode())
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
		})
	}
}

// An unknown presentation token is refused before any call: the hub and the
// worker see nothing.
func TestTabCreationCLI_RejectsAnUnknownPresentationBeforeCalls(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			for _, flag := range []string{"--display-mode", "--view-mode"} {
				t.Run(flag, func(t *testing.T) {
					dispatcher := &creationPublicationDispatcher{}
					hub := &recordingHub{workers: workerRows("worker-A"), materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st}
					hubFlag := transport.start(t, hub, dispatcher)
					args := []string{"file", "open", "/private/report.txt", "--workspace", "ws-1", "--worker", "worker-A", "--tile", "root-1", flag, "2", "--first"}
					o := runControl(t, append(args, testHubArgs(hubFlag)...)...)
					f := o.requireFailure(t, "usage")
					assert.Contains(t, f.Message, flag+" must be one of")
					assert.Empty(t, hub.called())
					assert.Empty(t, dispatcher.recorded())
				})
			}
		})
	}
}

// Every presentation token reaches the worker as its enum value, and an
// absent flag leaves the register unset.
func TestTabCreationCLI_PreservesPresentationValues(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			for _, display := range append([]string{""}, enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED)...) {
				t.Run("display="+display, func(t *testing.T) {
					dispatcher := &creationPublicationDispatcher{}
					hub := &recordingHub{workers: workerRows("worker-A"), materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st}
					hubFlag := transport.start(t, hub, dispatcher)
					args := []string{"file", "open", "/private/report.txt", "--workspace", "ws-1", "--worker", "worker-A", "--tile", "root-1", "--first"}
					args = append(args, testHubArgs(hubFlag)...)
					if display != "" {
						args = append(args, "--display-mode", display)
					}
					o := runControl(t, args...)
					require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
					calls := dispatcher.recorded()
					require.Len(t, calls, 1)
					presentation := calls[0].in.(*leapmuxv1.RegisterTabPayloadRequest).GetCreation().GetPresentation()
					require.NotNil(t, presentation)
					assert.Equal(t, display, enumToken(presentation.GetDisplayMode()))
					assert.Equal(t, display != "", presentation.DisplayMode != nil)
					assert.Nil(t, presentation.FileViewMode, "an absent flag leaves the register unset")
				})
			}
			for _, viewMode := range append([]string{""}, enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED)...) {
				t.Run("view="+viewMode, func(t *testing.T) {
					dispatcher := &creationPublicationDispatcher{}
					hub := &recordingHub{workers: workerRows("worker-A"), materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st}
					hubFlag := transport.start(t, hub, dispatcher)
					args := []string{"file", "open", "/private/report.txt", "--workspace", "ws-1", "--worker", "worker-A", "--tile", "root-1", "--first"}
					args = append(args, testHubArgs(hubFlag)...)
					if viewMode != "" {
						args = append(args, "--view-mode", viewMode)
					}
					o := runControl(t, args...)
					require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
					calls := dispatcher.recorded()
					require.Len(t, calls, 1)
					presentation := calls[0].in.(*leapmuxv1.RegisterTabPayloadRequest).GetCreation().GetPresentation()
					require.NotNil(t, presentation)
					assert.Equal(t, viewMode, enumToken(presentation.GetFileViewMode()))
					assert.Equal(t, viewMode != "", presentation.FileViewMode != nil)
					assert.Nil(t, presentation.DisplayMode, "an absent flag leaves the register unset")
				})
			}
		})
	}
}

// REMOVALS-4: `file open` rejects an empty or whitespace-only path before
// opening any network session.
func TestFileOpen_RejectsEmptyPathBeforeOpeningSession(t *testing.T) {
	clearRemoteEnv(t)
	for _, badPath := range []string{"", "   \t"} {
		o := runControl(t, "file", "open", badPath, "--hub", "https://stub.invalid")
		f := o.requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "empty")
	}
}

// REMOVALS-6: mutually exclusive position flags fail before opening a session
// across `agent open`, `terminal open`, `file open`, and `agent fork`.
func TestOpenCommands_RejectMutuallyExclusivePositionFlagsBeforeOpeningSession(t *testing.T) {
	clearRemoteEnv(t)
	cases := []struct {
		name string
		args []string
	}{
		{"agent open", []string{"agent", "open", "--first", "--last"}},
		{"terminal open", []string{"terminal", "open", "--first", "--before", fullTestID("ref-1")}},
		{"file open", []string{"file", "open", "/tmp/a.txt", "--before", fullTestID("ref-1"), "--after", fullTestID("ref-2")}},
		{"agent fork", []string{"agent", "fork", fullTestID("src-1"), "--first", "--last"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := runControl(t, append(tc.args, "--hub", "https://stub.invalid")...)
			f := o.requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "mutually exclusive")
		})
	}
}

// RIPPLE-4: `--before` / `--after` without explicit `--tile` or `--workspace`
// follows the reference tab's workspace and tile instead of pinning to the
// caller's own ambient tab (in `agent open`) or source agent (in `agent fork`).
func TestRelativePlacementFollowsTargetTabAcrossWorkspaces(t *testing.T) {
	selfID := fullTestID("self-ag")
	refID := fullTestID("ref-tab")
	ws1 := fullTestID("ws-one")
	ws2 := fullTestID("ws-two")
	tile1 := fullTestID("tl-one")
	tile2 := fullTestID("tl-two")

	tabSelf := &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: selfID},
		WorkspaceId: ws1,
		TileId:      tile1,
		WorkerId:    "worker-A",
	}
	tabRef := &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: refID},
		WorkspaceId: ws2,
		TileId:      tile2,
		WorkerId:    "worker-A",
	}

	var capturedOpen *leapmuxv1.OpenAgentRequest
	startPhase3BIPC(t,
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				var req leapmuxv1.LocateTabRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				if req.GetTabId() == refID {
					return &leapmuxv1.LocateTabResponse{Tab: tabRef}, nil
				}
				return &leapmuxv1.LocateTabResponse{Tab: tabSelf}, nil
			case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
				return &leapmuxv1.GetWorkspaceResponse{Workspace: &leapmuxv1.Workspace{Id: ws2}}, nil
			case leapmuxv1connect.WorkspaceServiceLocateTileProcedure:
				return &leapmuxv1.LocateTileResponse{WorkspaceId: ws2}, nil
			case leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:
				return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A", Online: true}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tabSelf, tabRef}}, nil
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
				return &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:            selfID,
						Title:         "Source",
						WorkerId:      "worker-A",
						WorkingDir:    "/repo",
						AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
					}},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
				var req leapmuxv1.OpenAgentRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				capturedOpen = &req
				return &leapmuxv1.OpenAgentResponse{
					Agent: &leapmuxv1.AgentInfo{Id: req.GetCreation().GetTabId(), WorkerId: "worker-A", Title: req.GetTitle()},
					Publication: &leapmuxv1.PublishedTab{
						Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: req.GetCreation().GetTabId()},
						WorkspaceId: req.GetCreation().GetPlacement().GetWorkspaceId(),
						TileId:      req.GetCreation().GetPlacement().GetTileId(),
						Position:    "hub-rank",
					},
				}, nil
			default:
				return nil, assert.AnError
			}
		},
	)
	t.Setenv(controlenv.TabIDVariable, selfID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	t.Run("agent open --before", func(t *testing.T) {
		capturedOpen = nil
		data := runControl(t, "agent", "open", "--provider", "claude", "--before", refID, "--no-focus").Data(t)
		assert.Equal(t, ws2, data["workspace_id"])
		assert.Equal(t, tile2, data["tile_id"])
		require.NotNil(t, capturedOpen)
		pl := capturedOpen.GetCreation().GetPlacement()
		assert.Equal(t, ws2, pl.GetWorkspaceId())
		assert.Equal(t, tile2, pl.GetTileId())
		assert.Equal(t, refID, pl.GetRelativeTabId())
		assert.Equal(t, leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, pl.GetInsertion())
	})

	t.Run("agent fork --after", func(t *testing.T) {
		capturedOpen = nil
		data := runControl(t, "agent", "fork", selfID, "--after", refID, "--no-focus").Data(t)
		assert.Equal(t, ws2, data["workspace_id"])
		assert.Equal(t, tile2, data["tile_id"])
		require.NotNil(t, capturedOpen)
		pl := capturedOpen.GetCreation().GetPlacement()
		assert.Equal(t, ws2, pl.GetWorkspaceId())
		assert.Equal(t, tile2, pl.GetTileId())
		assert.Equal(t, refID, pl.GetRelativeTabId())
		assert.Equal(t, leapmuxv1.TabInsertion_TAB_INSERTION_AFTER, pl.GetInsertion())
	})
}

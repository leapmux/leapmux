package cmd

import (
	"context"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type creationPublicationDispatcher struct {
	mu             sync.Mutex
	calls          []recordedWorkerCall
	failOpen       bool
	initialFailure bool
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
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		request = &leapmuxv1.EnqueueAgentInputRequest{}
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
	case *leapmuxv1.EnqueueAgentInputRequest:
		if d.initialFailure {
			_ = w.SendError(int32(codes.Internal), "initial input failed")
			return
		}
		_ = w.SendResponse(channelwire.NewPayloadResponse(nil))
		return
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
					client, err := requireClient(hubFlag)
					require.NoError(t, err)
					var resultID, resultTile, resultPosition string
					var openErr error
					output := withCapturedStdout(t, func() {
						openErr = withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
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
					})
					require.NoError(t, openErr, "stdout: %s", output)
					calls := dispatcher.recorded()
					require.Len(t, calls, 1)
					var creation *leapmuxv1.TabCreation
					if request, ok := calls[0].in.(*leapmuxv1.OpenAgentRequest); ok {
						creation = request.GetCreation()
					} else {
						creation = calls[0].in.(*leapmuxv1.OpenTerminalRequest).GetCreation()
					}
					require.NotNil(t, creation)
					assert.NotEmpty(t, creation.GetTabId())
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
				client, err := requireClient(transport.start(t, hub, dispatcher))
				require.NoError(t, err)
				var openErr error
				output := withCapturedStdout(t, func() {
					openErr = withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
						_, err := openTerminalAndAddTab(t.Context(), client, worker, openTerminalArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1"})
						return err
					})
				})
				require.Error(t, openErr)
				envelopes := decodeEnvelopes(t, output)
				require.Len(t, envelopes, 1)
				assert.Equal(t, "invalid_worker_response", envelopes[0]["error"]["code"])
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
			client, err := requireClient(transport.start(t, hub, dispatcher))
			require.NoError(t, err)
			var openErr error
			output := withCapturedStdout(t, func() {
				openErr = withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
					_, err := openAgentAndAddTab(t.Context(), client, worker, openAgentArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1"})
					return err
				})
			})
			require.Error(t, openErr)
			envelopes := decodeEnvelopes(t, output)
			require.Len(t, envelopes, 1)
			assert.Contains(t, envelopes[0]["error"]["message"], "publication outcome remains unknown")
			calls := dispatcher.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, calls[0].method)
			assert.NotEmpty(t, calls[0].in.(*leapmuxv1.OpenAgentRequest).GetCreation().GetTabId())
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
		})
	}
}

func TestTabCreationCLI_ReportsInitialInputFailureAfterPublication(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			dispatcher, hub := &creationPublicationDispatcher{initialFailure: true}, &recordingHub{}
			client, err := requireClient(transport.start(t, hub, dispatcher))
			require.NoError(t, err)
			var result *openAgentResult
			output := withCapturedStdout(t, func() {
				err = withWorkerChannel(t.Context(), client, "worker-A", func(worker workerCall) error {
					var openErr error
					result, openErr = openAgentAndAddTab(t.Context(), client, worker, openAgentArgs{WorkspaceID: "ws-1", WorkerID: "worker-A", TileID: "root-1", InitialMessage: "inspect the workspace"})
					return openErr
				})
			})
			require.NoError(t, err, "stdout: %s", output)
			require.NotNil(t, result)
			assert.Contains(t, result.InitialMsgWarn, "initial input failed")
			assert.Equal(t, "hub-rank", result.Position)
			calls := dispatcher.recorded()
			require.Len(t, calls, 2)
			creation := calls[0].in.(*leapmuxv1.OpenAgentRequest).GetCreation()
			input := calls[1].in.(*leapmuxv1.EnqueueAgentInputRequest)
			assert.Equal(t, creation.GetTabId(), input.GetAgentId())
			assert.Equal(t, "inspect the workspace", input.GetText())
			assert.NotEmpty(t, input.GetInputId())
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
		})
	}
}

func TestTabCreationCLI_PublishesFilePresentationWithTheEncryptedPayload(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			dispatcher := &creationPublicationDispatcher{}
			hub := &recordingHub{materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st}
			hubFlag := transport.start(t, hub, dispatcher)
			var openErr error
			output := withCapturedStdout(t, func() {
				openErr = RunTabOpen(fakeCmdCtx{}, []string{"--hub", hubFlag, "--type", "file", "--workspace-id", "ws-1", "--worker-id", "worker-A", "--tile-id", "root-1", "--path", "/private/report.txt", "--working-dir", "/private", "--display-mode", "2", "--file-view-mode", "2", "--first"})
			})
			require.NoError(t, openErr, "stdout: %s", output)
			envelopes := decodeEnvelopes(t, output)
			require.Len(t, envelopes, 1)
			assert.Equal(t, "hub-rank", envelopes[0]["data"]["position"])
			assert.Equal(t, "/private/report.txt", envelopes[0]["data"]["path"])
			calls := dispatcher.recorded()
			require.Len(t, calls, 1)
			request := calls[0].in.(*leapmuxv1.RegisterTabPayloadRequest)
			assert.Equal(t, "/private/report.txt", request.GetPayload().GetFile().GetFilePath())
			assert.Equal(t, "/private", request.GetPayload().GetWorkingDir())
			assert.Equal(t, leapmuxv1.TabInsertion_TAB_INSERTION_FIRST, request.GetCreation().GetPlacement().GetInsertion())
			assert.Equal(t, int32(2), request.GetCreation().GetPresentation().GetDisplayMode())
			assert.Equal(t, int32(2), request.GetCreation().GetPresentation().GetFileViewMode())
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
		})
	}
}

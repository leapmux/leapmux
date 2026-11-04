package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/workerkeys"
)

// hubChannelUser is the identity that the fake hub authenticates a channel as.
// The seeded credential records the same user, because tunnel.OpenChannel
// refuses a channel when the two disagree.
const hubChannelUser = "u-hub"

// hubChannelHarness runs the hub transport of a `leapmux control` command in
// this process. The hub answers its Connect RPCs from a recordingHub and relays
// the /ws/channel frames to a real channel.Manager, which stands in for the
// worker, so the CLI opens a real tunnel.Channel over a real Noise handshake.
//
// startSpawnIPC covers the local-IPC transport, which has no channel. A command
// that shares a channel across its calls needs this harness: only a real
// tunnel.Channel fails a call after Close, and only a real one ends with its
// lifetime context.
type hubChannelHarness struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler
	leapmuxv1connect.UnimplementedWorkspaceServiceHandler
	leapmuxv1connect.UnimplementedUserCRDTHandler
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler

	url string

	hub    *recordingHub
	worker *channel.Manager
	keys   *leapmuxv1.WorkerPublicKeys
	// ctx ends with the test, and it ends every relay goroutine.
	ctx   context.Context
	opens atomic.Int32

	mu sync.Mutex
	// relays maps a channel id to the socket that carries its frames. The hub
	// learns the socket from the first frame that the client sends on it.
	relays map[string]*websocket.Conn
}

// startHubChannelHarness starts the hub and the worker, seeds the credential
// that a command needs to reach them, and clears every LEAPMUX_CONTROL_* value
// that would select the local-IPC transport. A test passes the returned url as
// --hub. disp answers the worker calls of harnessWorkerProcedures.
func startHubChannelHarness(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher) *hubChannelHarness {
	t.Helper()
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_CONTROL_TOKEN", "")
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	keys, err := workerkeys.Of(key)
	require.NoError(t, err)

	h := &hubChannelHarness{hub: hub, keys: keys, ctx: t.Context(), relays: map[string]*websocket.Conn{}}
	h.worker = channel.NewManager(key, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, h.sendToClient, h.trySendToClient, 0, 0)
	h.worker.SetDispatcher(harnessDispatcher(disp))
	t.Cleanup(h.worker.CloseAll)

	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterChannelServiceHandler(connectServer, h)
	leapmuxv1connect.RegisterWorkspaceServiceHandler(connectServer, h)
	leapmuxv1connect.RegisterUserCRDTHandler(connectServer, h)
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(connectServer, h)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	mux.HandleFunc(contracts.WSRouteChannel, h.relayFrames)
	h.url = hubtransporttest.NewServer(t, mux).URL

	require.NoError(t, control.SaveCredentials(h.url, control.CredentialFile{
		HubURL: h.url, AccessToken: "lmx_test_secret", UserID: hubChannelUser,
	}))
	return h
}

// harnessWorkerProcedures lists the worker calls that the harness forwards to
// the test's dispatcher. channel.Dispatcher matches a method exactly and has no
// catch-all, so a new procedure under test goes here.
var harnessWorkerProcedures = []string{
	leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
	leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure,
	leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure,
	leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
	leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure,
	leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure,
}

// harnessDispatcher answers the Ping that tunnel.OpenChannel sends to verify a
// new session, and forwards harnessWorkerProcedures to disp. The Ping stays out
// of disp, so a dispatcher records only the calls of the command under test.
func harnessDispatcher(disp controlipc.LocalDispatcher) *channel.Dispatcher {
	dispatcher := channel.NewDispatcher()
	dispatcher.Handle(leapmuxv1connect.WorkerSystemServicePingProcedure, channel.Registration{},
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
			_ = w.SendResponse(channelwire.NewPayloadResponse(nil))
		})
	for _, procedure := range harnessWorkerProcedures {
		dispatcher.Handle(procedure, channel.Registration{}, disp.DispatchWith)
	}
	return dispatcher
}

// channelOpens returns how many channels the hub opened, each of which is one
// Noise handshake.
func (h *hubChannelHarness) channelOpens() int {
	return int(h.opens.Load())
}

// sendToClient relays one frame from the worker to the client's socket.
func (h *hubChannelHarness) sendToClient(msg *leapmuxv1.ConnectRequest) error {
	frame := msg.GetChannelMessageResp()
	if frame == nil {
		return nil
	}
	h.mu.Lock()
	ws := h.relays[frame.GetChannelId()]
	h.mu.Unlock()
	if ws == nil {
		return fmt.Errorf("no relay socket for channel %q", frame.GetChannelId())
	}
	return channelwire.WriteChannelMessage(h.ctx, ws, frame)
}

func (h *hubChannelHarness) trySendToClient(msg *leapmuxv1.ConnectRequest) bool {
	return h.sendToClient(msg) == nil
}

// relayFrames serves /ws/channel. It hands every frame of one socket to the
// worker, and it closes the worker's session when the socket ends.
func (h *hubChannelHarness) relayFrames(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{contracts.WSSubprotocolChannelRelay}})
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(channelwire.WSReadLimit)

	var channelID string
	defer func() {
		if channelID != "" {
			h.worker.HandleClose(channelID)
		}
	}()
	for {
		frame, err := channelwire.ReadChannelMessage(h.ctx, ws)
		if err != nil {
			return
		}
		if channelID == "" {
			channelID = frame.GetChannelId()
			h.mu.Lock()
			h.relays[channelID] = ws
			h.mu.Unlock()
		}
		h.worker.HandleMessage(frame)
	}
}

func (h *hubChannelHarness) GetWorkerHandshakeParams(context.Context, *leapmuxv1.GetWorkerHandshakeParamsRequest) (*leapmuxv1.GetWorkerHandshakeParamsResponse, error) {
	if err := h.hub.refuseChannelOpen; err != nil {
		return nil, err
	}
	return &leapmuxv1.GetWorkerHandshakeParamsResponse{
		Keys:           h.keys,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}, nil
}

func (h *hubChannelHarness) OpenChannel(_ context.Context, req *leapmuxv1.OpenChannelRequest) (*leapmuxv1.OpenChannelResponse, error) {
	channelID := "channel-" + strconv.Itoa(int(h.opens.Add(1)))
	opened := h.worker.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        channelID,
		UserId:           hubChannelUser,
		HandshakePayload: req.GetHandshakePayload(),
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ALL},
	})
	if reason := opened.GetError(); reason != "" {
		return nil, connect.NewError(connect.CodeInternal, reason)
	}
	return &leapmuxv1.OpenChannelResponse{
		ChannelId:        channelID,
		HandshakePayload: opened.GetHandshakePayload(),
		UserId:           hubChannelUser,
		MaxMessageSize:   opened.GetMaxMessageSize(),
	}, nil
}

func (h *hubChannelHarness) CloseChannel(_ context.Context, req *leapmuxv1.CloseChannelRequest) (*leapmuxv1.CloseChannelResponse, error) {
	h.worker.HandleClose(req.GetChannelId())
	return &leapmuxv1.CloseChannelResponse{}, nil
}

func (h *hubChannelHarness) GetWorkspace(ctx context.Context, req *leapmuxv1.GetWorkspaceRequest) (*leapmuxv1.GetWorkspaceResponse, error) {
	return answerFromRecordingHub(ctx, h.hub, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, req, &leapmuxv1.GetWorkspaceResponse{})
}

func (h *hubChannelHarness) LocateTab(ctx context.Context, req *leapmuxv1.LocateTabRequest) (*leapmuxv1.LocateTabResponse, error) {
	return answerFromRecordingHub(ctx, h.hub, leapmuxv1connect.WorkspaceServiceLocateTabProcedure, req, &leapmuxv1.LocateTabResponse{})
}

func (h *hubChannelHarness) GetMaterialized(ctx context.Context, req *leapmuxv1.GetMaterializedRequest) (*leapmuxv1.GetMaterializedResponse, error) {
	return answerFromRecordingHub(ctx, h.hub, leapmuxv1connect.UserCRDTGetMaterializedProcedure, req, &leapmuxv1.GetMaterializedResponse{})
}

func (h *hubChannelHarness) SubmitOps(ctx context.Context, req *leapmuxv1.SubmitOpsRequest) (*leapmuxv1.SubmitOpsResponse, error) {
	return answerFromRecordingHub(ctx, h.hub, leapmuxv1connect.UserCRDTSubmitOpsProcedure, req, &leapmuxv1.SubmitOpsResponse{})
}

func (h *hubChannelHarness) GetWorker(ctx context.Context, req *leapmuxv1.GetWorkerRequest) (*leapmuxv1.GetWorkerResponse, error) {
	return answerFromRecordingHub(ctx, h.hub, leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure, req, &leapmuxv1.GetWorkerResponse{})
}

// answerFromRecordingHub answers one typed hub RPC through hub, the stub that
// the local-IPC harness asks, so a test states one hub for both transports.
func answerFromRecordingHub[Out proto.Message](ctx context.Context, hub *recordingHub, procedure string, in proto.Message, out Out) (Out, error) {
	payload, err := proto.Marshal(in)
	if err != nil {
		return out, connect.NewError(connect.CodeInternal, err.Error())
	}
	answer, err := hub.CallInner(ctx, userid.MustNew(hubChannelUser), procedure, payload)
	if err != nil {
		return out, err
	}
	if err := proto.Unmarshal(answer, out); err != nil {
		return out, connect.NewError(connect.CodeInternal, err.Error())
	}
	return out, nil
}

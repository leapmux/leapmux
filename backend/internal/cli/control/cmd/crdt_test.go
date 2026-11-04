package cmd

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hubrpc"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestCRDTSubmitBatchPinsTheFirstSubmissionEpoch(t *testing.T) {
	client, requests := newCRDTSubmitTestClient(t, &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{BatchId: "batch"}}})
	bootstrap := &CRDTBootstrap{Epoch: 7}
	batch := &leapmuxv1.OpBatch{BatchId: "batch", Ops: []*leapmuxv1.CrdtOp{{OpId: "operation"}}}
	for _, epoch := range []int64{7, 8} {
		bootstrap.Epoch = epoch
		_, err := crdtSubmitBatch(context.Background(), client, bootstrap, "workspace", batch)
		require.NoError(t, err)
		sent := <-requests
		require.Len(t, sent.GetBatches(), 1)
		assert.EqualValues(t, 7, sent.GetBatches()[0].GetEpoch())
		assert.Equal(t, "batch", sent.GetBatches()[0].GetBatchId())
		require.Len(t, sent.GetBatches()[0].GetOps(), 1)
		assert.Equal(t, "operation", sent.GetBatches()[0].GetOps()[0].GetOpId())
	}
	assert.EqualValues(t, 7, batch.GetEpoch())
}

func TestCRDTSubmitBatchKeepsItsEpochAfterAnRPCFailure(t *testing.T) {
	var attempts atomic.Int32
	client, requests := newCRDTSubmitTestClientWithResponder(t, func() (*leapmuxv1.SubmitOpsResponse, error) {
		if attempts.Add(1) == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, "hub temporarily unavailable")
		}
		return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{
			BatchId: "retry",
			Outcome: &leapmuxv1.BatchResult_Committed{Committed: &leapmuxv1.BatchCommitted{Epoch: 8}},
		}}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bootstrap := &CRDTBootstrap{Epoch: 7}
	batch := &leapmuxv1.OpBatch{BatchId: "retry", Ops: []*leapmuxv1.CrdtOp{{OpId: "operation"}}}
	_, err := crdtSubmitBatch(ctx, client, bootstrap, "workspace", batch)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.EqualValues(t, 1, attempts.Load())
	first := <-requests
	require.Len(t, first.GetBatches(), 1)
	assert.EqualValues(t, 7, first.GetBatches()[0].GetEpoch())
	bootstrap.Epoch = 8
	_, err = crdtSubmitBatch(ctx, client, bootstrap, "workspace", batch)
	require.NoError(t, err)
	require.EqualValues(t, 2, attempts.Load())
	second := <-requests
	assert.True(t, proto.Equal(first, second), "an uncertain retry must preserve the submitted batch identity and body")
	assert.EqualValues(t, 7, batch.GetEpoch())
}

func TestCRDTSubmitBatchPreservesAnAssignedEpoch(t *testing.T) {
	client, requests := newCRDTSubmitTestClient(t, &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{BatchId: "retry"}}})
	_, err := crdtSubmitBatch(context.Background(), client, &CRDTBootstrap{Epoch: 9}, "workspace", &leapmuxv1.OpBatch{BatchId: "retry", Epoch: 3})
	require.NoError(t, err)
	assert.EqualValues(t, 3, (<-requests).GetBatches()[0].GetEpoch())
}

func TestCRDTSubmitBatchRefusesMissingInputsBeforeTransport(t *testing.T) {
	for _, input := range []struct {
		name      string
		bootstrap *CRDTBootstrap
		batch     *leapmuxv1.OpBatch
		message   string
	}{
		{name: "bootstrap", batch: &leapmuxv1.OpBatch{}, message: "bootstrap is nil"},
		{name: "batch", bootstrap: &CRDTBootstrap{Epoch: 1}, message: "batch is nil"},
	} {
		t.Run(input.name, func(t *testing.T) {
			_, err := crdtSubmitBatch(context.Background(), nil, input.bootstrap, "workspace", input.batch)
			require.ErrorContains(t, err, input.message)
		})
	}
}

func TestCRDTSubmitBatchRefusesAnInvalidResultCount(t *testing.T) {
	for _, count := range []int{0, 2} {
		results := make([]*leapmuxv1.BatchResult, count)
		for i := range results {
			results[i] = &leapmuxv1.BatchResult{BatchId: "batch"}
		}
		client, _ := newCRDTSubmitTestClient(t, &leapmuxv1.SubmitOpsResponse{Results: results})
		_, err := crdtSubmitBatch(context.Background(), client, &CRDTBootstrap{Epoch: 1}, "workspace", &leapmuxv1.OpBatch{BatchId: "batch"})
		assert.ErrorContains(t, err, "expected 1 result")
	}
}

type crdtSubmitTestHub struct {
	leapmuxv1connect.UnimplementedUserCRDTHandler
	requests chan *leapmuxv1.SubmitOpsRequest
	respond  func() (*leapmuxv1.SubmitOpsResponse, error)
}

func (s *crdtSubmitTestHub) SubmitOps(ctx context.Context, request *leapmuxv1.SubmitOpsRequest) (*leapmuxv1.SubmitOpsResponse, error) {
	select {
	case s.requests <- proto.Clone(request).(*leapmuxv1.SubmitOpsRequest):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.respond()
}

func newCRDTSubmitTestClient(t *testing.T, response *leapmuxv1.SubmitOpsResponse) (*control.Client, <-chan *leapmuxv1.SubmitOpsRequest) {
	t.Helper()
	return newCRDTSubmitTestClientWithResponder(t, func() (*leapmuxv1.SubmitOpsResponse, error) { return response, nil })
}

func newCRDTSubmitTestClientWithResponder(t *testing.T, respond func() (*leapmuxv1.SubmitOpsResponse, error)) (*control.Client, <-chan *leapmuxv1.SubmitOpsRequest) {
	t.Helper()
	isolateCLIEnv(t)
	hub := &crdtSubmitTestHub{requests: make(chan *leapmuxv1.SubmitOpsRequest, 2), respond: respond}
	server := connect.NewServer()
	leapmuxv1connect.RegisterUserCRDTHandler(server, hub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	transport := hubtransporttest.NewServer(t, mux)
	client, err := control.NewClientOrAnonymous(transport.URL)
	require.NoError(t, err)
	return client, hub.requests
}

// scriptedIPC is a ControlIPCService that answers each call with what a test
// scripts, and records the requests it received. It stands in for the worker's
// IPC server where the real router cannot produce the answer under test, such
// as an in-band error on a hub call.
type scriptedIPC struct {
	leapmuxv1connect.UnimplementedControlIPCServiceHandler
	// callInner answers CallInner.
	callInner func(*leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error)
	// streamFrames are the frames StreamInner sends before it waits for the
	// caller to end the stream.
	streamFrames []*leapmuxv1.InnerStreamMessage

	mu      sync.Mutex
	unaries []*leapmuxv1.CallInnerRequest
	streams []*leapmuxv1.StreamInnerRequest
}

func (s *scriptedIPC) CallInner(_ context.Context, req *leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error) {
	s.mu.Lock()
	s.unaries = append(s.unaries, proto.Clone(req).(*leapmuxv1.CallInnerRequest))
	s.mu.Unlock()
	return s.callInner(req)
}

func (s *scriptedIPC) StreamInner(ctx context.Context, req *leapmuxv1.StreamInnerRequest, stream leapmuxv1connect.ControlIPCServiceStreamInnerServerStream) error {
	s.mu.Lock()
	s.streams = append(s.streams, proto.Clone(req).(*leapmuxv1.StreamInnerRequest))
	s.mu.Unlock()
	for _, frame := range s.streamFrames {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s *scriptedIPC) unaryRequests() []*leapmuxv1.CallInnerRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*leapmuxv1.CallInnerRequest(nil), s.unaries...)
}

func (s *scriptedIPC) streamRequests() []*leapmuxv1.StreamInnerRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*leapmuxv1.StreamInnerRequest(nil), s.streams...)
}

// startScriptedIPC serves ipc on a local socket and returns a worker-IPC
// client for it, the client a worker-spawned agent holds.
func startScriptedIPC(t *testing.T, ipc *scriptedIPC) *control.Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
	isolateCLIEnv(t)
	server := connect.NewServer()
	leapmuxv1connect.RegisterControlIPCServiceHandler(server, ipc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	socketURL := hubtransporttest.NewSocketServer(t, "crdt-ipc", mux)
	c, err := control.NewLocalClient(socketURL, "ipc-token")
	require.NoError(t, err)
	require.True(t, c.IsWorkerIPC())
	return c
}

// marshalReply answers a CallInner with msg as its payload.
func marshalReply(t *testing.T, msg proto.Message) *leapmuxv1.InnerRpcResponse {
	t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(t, err)
	return channelwire.NewPayloadResponse(payload)
}

// The bootstrap of a worker-spawned client asks the hub's GetMaterialized
// through the worker's IPC, and seeds its clock and epoch from the answer.
func TestCRDTBootstrap_LocalIPCFetchesTheSnapshot(t *testing.T) {
	state := &leapmuxv1.UserMaterialized{
		CurrentEpoch: 4,
		MaxHlc:       &leapmuxv1.HLC{Physical: 99, Logical: 3, ClientId: "peer"},
		Workspaces:   map[string]*leapmuxv1.WorkspaceContentsRecord{"ws-1": {WorkspaceId: "ws-1", RootNodeId: "root-1"}},
	}
	ipc := &scriptedIPC{callInner: func(*leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error) {
		return marshalReply(t, &leapmuxv1.GetMaterializedResponse{State: state}), nil
	}}
	c := startScriptedIPC(t, ipc)

	bs, err := crdtBootstrap(t.Context(), c, []string{"ws-1"})

	require.NoError(t, err)
	assert.True(t, proto.Equal(state, bs.State), "the snapshot is the one the hub answered")
	assert.EqualValues(t, 4, bs.Epoch)
	assert.Equal(t, originClientID(), bs.OriginClient)
	requests := ipc.unaryRequests()
	require.Len(t, requests, 1, "the bootstrap is one unary call")
	assert.Equal(t, "hub."+leapmuxv1connect.UserCRDTGetMaterializedProcedure, requests[0].GetMethod())
	assert.Empty(t, requests[0].GetTargetWorkerId(), "a hub call specifies no worker")
	var sent leapmuxv1.GetMaterializedRequest
	require.NoError(t, proto.Unmarshal(requests[0].GetPayload(), &sent))
	assert.Equal(t, []string{"ws-1"}, sent.GetWorkspaceIds())
}

// An in-band error keeps its code on the local-IPC transport, as it does on
// the hub transport. The bootstrap used to rebuild it from the message alone,
// so a caller that classifies by code read every refusal as unknown.
func TestCRDTBootstrap_LocalIPCKeepsTheCodeOfAnInBandError(t *testing.T) {
	ipc := &scriptedIPC{callInner: func(*leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error) {
		return channelwire.NewErrorResponse(int32(codes.NotFound), "no such workspace"), nil
	}}
	c := startScriptedIPC(t, ipc)

	bs, err := crdtBootstrap(t.Context(), c, []string{"ws-1"})

	require.Error(t, err)
	assert.Nil(t, bs)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "no such workspace")
}

// A failure that the worker relays as a transport error keeps its code too.
func TestCRDTBootstrap_LocalIPCKeepsTheCodeOfARelayedFailure(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)
	c, err := requireClient("")
	require.NoError(t, err)

	bs, err := crdtBootstrap(t.Context(), c, []string{"ws-1"})

	require.Error(t, err)
	assert.Nil(t, bs)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, []string{leapmuxv1connect.UserCRDTGetMaterializedProcedure}, hub.called())
}

// A reply that carries no snapshot is refused: the bootstrap has no epoch or
// clock to seed, and every op it then builds would carry a zero epoch.
func TestCRDTBootstrap_RefusesAReplyWithNoSnapshot(t *testing.T) {
	for name, reply := range map[string]*leapmuxv1.InnerRpcResponse{
		"empty payload":  {},
		"no state field": marshalReply(t, &leapmuxv1.GetMaterializedResponse{}),
	} {
		t.Run(name, func(t *testing.T) {
			ipc := &scriptedIPC{callInner: func(*leapmuxv1.CallInnerRequest) (*leapmuxv1.InnerRpcResponse, error) {
				return reply, nil
			}}
			c := startScriptedIPC(t, ipc)

			bs, err := crdtBootstrap(t.Context(), c, []string{"ws-1"})

			require.Error(t, err)
			assert.Nil(t, bs)
			assert.Contains(t, err.Error(), "the hub returned no materialized state")
		})
	}
}

// watchUserEventFrame wraps evt as one frame of a WatchUser stream.
func watchUserEventFrame(t *testing.T, evt *leapmuxv1.WatchUserEvent) *leapmuxv1.InnerStreamMessage {
	t.Helper()
	payload, err := proto.Marshal(evt)
	require.NoError(t, err)
	return &leapmuxv1.InnerStreamMessage{Payload: payload}
}

// The local WatchUser stream hands each decoded event to onEvent, and ends
// when onEvent asks it to stop.
func TestStreamWatchUserLocal_DeliversEventsUntilOnEventStops(t *testing.T) {
	initial := &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Initial{Initial: &leapmuxv1.UserMaterialized{CurrentEpoch: 2}}}
	ipc := &scriptedIPC{streamFrames: []*leapmuxv1.InnerStreamMessage{
		// A frame with no payload carries no event and is skipped.
		{},
		watchUserEventFrame(t, initial),
	}}
	c := startScriptedIPC(t, ipc)

	var got []*leapmuxv1.WatchUserEvent
	err := streamWatchUserLocal(testutil.DeadlineContext(t), c, []string{"ws-1"}, func(evt *leapmuxv1.WatchUserEvent) (bool, error) {
		got = append(got, evt)
		return true, nil
	})

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, proto.Equal(initial, got[0]))
	requests := ipc.streamRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, "hub."+hubrpc.WatchUserStream, requests[0].GetMethod())
	assert.NotEmpty(t, requests[0].GetClientRequestId(), "the router keys the stream by its client request id")
	var sent leapmuxv1.WatchUserRequest
	require.NoError(t, proto.Unmarshal(requests[0].GetPayload(), &sent))
	assert.Equal(t, []string{"ws-1"}, sent.GetWorkspaceIds())
}

// An in-band error frame ends the stream with its code, as an in-band error
// does on every other local-IPC call. It used to end the stream with the
// message alone.
func TestStreamWatchUserLocal_KeepsTheCodeOfAnInBandError(t *testing.T) {
	ipc := &scriptedIPC{streamFrames: []*leapmuxv1.InnerStreamMessage{
		{Error: &leapmuxv1.RpcError{Code: int32(codes.PermissionDenied), Message: "this bearer cannot watch the user"}},
	}}
	c := startScriptedIPC(t, ipc)

	delivered := 0
	err := streamWatchUserLocal(testutil.DeadlineContext(t), c, nil, func(*leapmuxv1.WatchUserEvent) (bool, error) {
		delivered++
		return false, nil
	})

	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "this bearer cannot watch the user")
	assert.Zero(t, delivered, "an error frame carries no event")
}

package controlipc_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// fakeLocalDispatcher records the last call. respPayload is what the
// dispatcher writes back on a unary call; emitStream frames are pushed
// to the ResponseWriter on streaming calls before the dispatcher
// returns. The router's streaming path only terminates on context
// cancellation, so streaming tests pair this with a manual cancel.
type fakeLocalDispatcher struct {
	mu          sync.Mutex
	gotMethod   string
	gotPayload  []byte
	gotUserID   userid.UserID
	respPayload []byte
	emitStream  [][]byte
}

func (f *fakeLocalDispatcher) DispatchWith(_ context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	f.mu.Lock()
	f.gotUserID = caller.UserID
	f.gotMethod = req.GetMethod()
	f.gotPayload = req.GetPayload()
	stream := f.emitStream
	final := f.respPayload
	f.mu.Unlock()

	for _, frame := range stream {
		_ = w.SendStream(&leapmuxv1.InnerStreamMessage{Payload: frame})
	}
	if final != nil {
		_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: final})
	}
}

// fakeCrossWorker records cross-worker calls so the test can verify
// the router routes correctly.
type fakeCrossWorker struct {
	mu          sync.Mutex
	callTarget  string
	callMethod  string
	callPayload []byte
	resp        []byte
	respErr     error
	// bindCtrl, when set, is handed to the router's bindCtrl callback so
	// UpdateStream tests can assert delivery on the cross-worker path.
	bindCtrl channel.StreamController
	// hold blocks StreamInner until closed, so UpdateStream can land while
	// the canceller entry is still live.
	hold <-chan struct{}
}

func (f *fakeCrossWorker) CallInner(_ context.Context, target string, _ userid.UserID, method string, payload []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callTarget, f.callMethod, f.callPayload = target, method, payload
	return f.resp, f.respErr
}

func (f *fakeCrossWorker) StreamInner(ctx context.Context, target string, _ userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage), bindCtrl func(channel.StreamController)) error {
	f.mu.Lock()
	f.callTarget, f.callMethod, f.callPayload = target, method, payload
	hold := f.hold
	ctrl := f.bindCtrl
	resp := f.resp
	respErr := f.respErr
	f.mu.Unlock()
	if respErr != nil {
		return respErr
	}
	if bindCtrl != nil {
		if ctrl != nil {
			bindCtrl(ctrl)
		} else {
			bindCtrl(&recordingStreamController{})
		}
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	onMsg(&leapmuxv1.InnerStreamMessage{Payload: resp, End: true})
	return nil
}

// fakeStreams records the release calls so the test asserts the
// synthetic-stream-id lifecycle. Every id the router mints must be released,
// or the watcher registrations keyed by it leak for the worker's lifetime.
type fakeStreams struct {
	mu       sync.Mutex
	released []string
}

func (f *fakeStreams) ReleaseLocalStream(streamID string) {
	f.mu.Lock()
	f.released = append(f.released, streamID)
	f.mu.Unlock()
}

func TestRouter_CallInner_LocalDispatch(t *testing.T) {
	dispatcher := &fakeLocalDispatcher{respPayload: []byte("hello")}
	streams := &fakeStreams{}
	r := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("user-1"),
		LocalDispatcher: dispatcher,
		Streams:         streams,
	}
	resp, err := r.CallInner(context.Background(),
		controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
		"worker."+leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, []byte("payload"),
		"worker-A")
	require.NoError(t, err)
	require.Nil(t, resp.GetError(), "unexpected error: %s", resp.GetError().GetMessage())
	assert.Equal(t, []byte("hello"), resp.GetPayload())

	// The dispatcher saw the procedure (namespace stripped) and the
	// request user id propagated from the router.
	assert.Equal(t, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, dispatcher.gotMethod)
	assert.Equal(t, []byte("payload"), dispatcher.gotPayload)
	assert.Equal(t, "user-1", dispatcher.gotUserID.String())

	// The dispatch minted a synthetic localipc:* stream id and released it
	// when it finished. The release is the load-bearing half: a local-IPC id
	// never reaches the channel manager's close callback, so nothing else would
	// ever sweep the watcher registrations keyed by it.
	require.Len(t, streams.released, 1)
	assert.True(t, strings.HasPrefix(streams.released[0], "localipc:"))
}

// TestRouter_LocalStreamID_IncludesTokenIdentity asserts the synthetic
// stream id format is `localipc:<token-identity>:<request-id>` so log
// correlation can attribute streams to the spawning agent/terminal/user
// without inspecting an external auth map. Plan reference: line 476
// ("localipc:<token_id>:<request_id>" — we use a stable per-bearer
// segment in place of the literal token, since raw token strings are
// secret).
func TestRouter_LocalStreamID_IncludesTokenIdentity(t *testing.T) {
	cases := []struct {
		name        string
		info        controlipc.TokenInfo
		wantSegment string
	}{
		{
			name:        "agent",
			info:        controlipc.TokenInfo{UserID: userid.MustNew("u-1"), TabID: "agent-XYZ", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			wantSegment: "agent-agent-XYZ", // prefix "agent-" + the TabID value.
		},
		{
			name:        "terminal",
			info:        controlipc.TokenInfo{UserID: userid.MustNew("u-1"), TabID: "term-7", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
			wantSegment: "terminal-term-7",
		},
		{
			name:        "user-only-fallback",
			info:        controlipc.TokenInfo{UserID: userid.MustNew("u-9")},
			wantSegment: "user-u-9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &fakeLocalDispatcher{respPayload: []byte("ok")}
			streams := &fakeStreams{}
			r := &controlipc.Router{
				WorkerID:        "worker-A",
				UserID:          tc.info.UserID,
				LocalDispatcher: dispatcher,
				Streams:         streams,
			}
			_, err := r.CallInner(context.Background(), tc.info,
				"worker."+leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, []byte("p"), "worker-A")
			require.NoError(t, err)
			require.Len(t, streams.released, 1)
			got := streams.released[0]
			parts := strings.SplitN(got, ":", 3)
			require.Len(t, parts, 3, "stream id %q must have prefix:token-id:request-id shape", got)
			assert.Equal(t, "localipc", parts[0])
			assert.Equal(t, tc.wantSegment, parts[1])
			assert.NotEmpty(t, parts[2], "request-id segment must be present")
		})
	}
}

// TestRouter_LocalStreamID_PerCallRequestSegmentChanges asserts the
// request-id segment differs across calls from the same bearer so
// every WatchEvents registration has a distinct row in the watcher
// map (otherwise the cleanup keyed by stream-id would deregister
// concurrent siblings).
func TestRouter_LocalStreamID_PerCallRequestSegmentChanges(t *testing.T) {
	dispatcher := &fakeLocalDispatcher{respPayload: []byte("ok")}
	streams := &fakeStreams{}
	r := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-1"),
		LocalDispatcher: dispatcher,
		Streams:         streams,
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), TabID: "agent-X", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
	for i := 0; i < 3; i++ {
		_, err := r.CallInner(context.Background(), info,
			"worker."+leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, []byte("p"), "worker-A")
		require.NoError(t, err)
	}
	require.Len(t, streams.released, 3)
	// Token-identity segment is stable; request-id segment varies.
	for _, sid := range streams.released {
		parts := strings.SplitN(sid, ":", 3)
		require.Len(t, parts, 3)
		assert.Equal(t, "agent-agent-X", parts[1])
	}
	seen := map[string]struct{}{}
	for _, sid := range streams.released {
		seen[sid] = struct{}{}
	}
	assert.Len(t, seen, 3, "each call must produce a unique request-id segment: %v", streams.released)
}

func TestRouter_CallInner_CrossWorker(t *testing.T) {
	cross := &fakeCrossWorker{resp: []byte("from-B")}
	r := &controlipc.Router{
		WorkerID:    "worker-A",
		UserID:      userid.MustNew("user-1"),
		CrossWorker: cross,
	}
	resp, err := r.CallInner(context.Background(),
		controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
		"worker."+leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, []byte("hi"),
		"worker-B")
	require.NoError(t, err)
	assert.Equal(t, []byte("from-B"), resp.GetPayload())
	assert.Equal(t, "worker-B", cross.callTarget)
	assert.Equal(t, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, cross.callMethod)
	// The request's workspace_id must flow through the channel pool
	// key so different workspaces don't share a delegation-scoped
	// Noise session against the same (target, user) pair.
}

// TestRouter_CallInner_FilesystemMethodCrossWorkerDispatchesUnconditionally
// pins the post-Phase-1 model: file/git RPCs to a sibling worker
// dispatch through `crossworker.Client` like any other inner-RPC.
// There is no extra gate — workers aren't shareable, the user owns
// every worker they can target, and the standalone CLI has the same
// access via its own bearer. A regression that re-introduced an
// access check here would silently break cross-worker file/git for
// every worker-spawned agent that didn't think to opt in.
func TestRouter_CallInner_FilesystemMethodCrossWorkerDispatchesUnconditionally(t *testing.T) {
	cross := &fakeCrossWorker{resp: []byte("file-bytes")}
	r := &controlipc.Router{
		WorkerID:    "worker-A",
		UserID:      userid.MustNew("user-1"),
		CrossWorker: cross,
	}
	for _, procedure := range []string{
		leapmuxv1connect.WorkerFileServiceListDirectoryProcedure,
		leapmuxv1connect.WorkerFileServiceListFilesystemRootsProcedure,
		leapmuxv1connect.WorkerFileServiceReadFileProcedure,
		leapmuxv1connect.WorkerFileServiceStatFileProcedure,
		leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure,
	} {
		cross.callTarget = ""
		cross.callMethod = ""
		resp, err := r.CallInner(context.Background(),
			controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
			"worker."+procedure, []byte(`{}`), "worker-B")
		require.NoError(t, err, procedure)
		assert.Equal(t, []byte("file-bytes"), resp.GetPayload(), procedure)
		assert.Equal(t, "worker-B", cross.callTarget, procedure)
		assert.Equal(t, procedure, cross.callMethod, "the sibling receives the procedure without the namespace")
	}
}

func TestRouter_CallInner_HubNamespace(t *testing.T) {
	hub := &fakeHubClient{resp: []byte("hub-ok")}
	r := &controlipc.Router{
		UserID: userid.MustNew("user-1"),
		Hub:    hub,
	}
	resp, err := r.CallInner(context.Background(),
		controlipc.TokenInfo{UserID: userid.MustNew("user-1")},
		"hub.ListWorkspaces", []byte("{}"),
		"")
	require.NoError(t, err)
	assert.Equal(t, []byte("hub-ok"), resp.GetPayload())
	assert.Equal(t, "ListWorkspaces", hub.lastMethod)
	assert.Equal(t, "user-1", hub.lastUserID.String())
	// Empty request workspace falls back to the spawning agent's
	// workspace so methods without a workspace_id field (e.g.
	// ListWorkspaces) still get a delegation scope.
}

func TestRouter_CallInner_HubNamespace_ForwardsRequestWorkspace(t *testing.T) {
	hub := &fakeHubClient{resp: []byte("ok")}
	r := &controlipc.Router{
		UserID: userid.MustNew("user-1"),
		Hub:    hub,
	}
	_, err := r.CallInner(context.Background(),
		controlipc.TokenInfo{UserID: userid.MustNew("user-1")},
		"hub.LocateTab", []byte("{}"), "")
	require.NoError(t, err)
}

func TestRouter_CallInner_HubNamespace_PropagatesError(t *testing.T) {
	hub := &fakeHubClient{respErr: errors.New("boom")}
	r := &controlipc.Router{
		UserID: userid.MustNew("user-1"),
		Hub:    hub,
	}
	_, err := r.CallInner(context.Background(),
		controlipc.TokenInfo{UserID: userid.MustNew("user-1")},
		"hub.LocateTab", []byte("{}"), "")
	require.Error(t, err)
	// An uncoded hub failure still surfaces as CodeInternal, so callers can
	// distinguish transport failure from "no hub configured" (Unimplemented)
	// and "workspace out of scope" (PermissionDenied).
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// TestRouter_CallInner_PreservesUpstreamCode pins the property that makes every
// code-sensitive CLI decision work on the local-IPC transport.
//
// Both relay arms used to wrap unconditionally in connect.CodeInternal, which
// left the originating code readable only as text in the message. Downstream
// that made cmd.isWorkerUnreachable and cmd.isNotFoundOrForbidden unable to
// match for a worker-spawned agent, so `tab close` on an offline sibling worker
// reported inspect_failed instead of falling back to a CRDT-only tombstone.
func TestRouter_CallInner_PreservesUpstreamCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		code connect.Code
	}{
		{"offline worker", connect.CodeUnavailable},
		{"missing workspace", connect.CodeNotFound},
		{"denied", connect.CodePermissionDenied},
	} {
		t.Run("hub namespace: "+tc.name, func(t *testing.T) {
			hub := &fakeHubClient{respErr: connect.NewError(tc.code, "upstream")}
			r := &controlipc.Router{
				UserID: userid.MustNew("user-1"),
				Hub:    hub,
			}
			_, err := r.CallInner(context.Background(),
				controlipc.TokenInfo{UserID: userid.MustNew("user-1")},
				"hub.LocateTab", []byte("{}"), "")
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err),
				"the originating code must survive the relay, not collapse to Internal")
		})

		t.Run("cross-worker namespace: "+tc.name, func(t *testing.T) {
			cw := &fakeCrossWorker{respErr: connect.NewError(tc.code, "upstream")}
			r := &controlipc.Router{
				UserID:      userid.MustNew("user-1"),
				WorkerID:    "wkr-self",
				CrossWorker: cw,
			}
			_, err := r.CallInner(context.Background(),
				controlipc.TokenInfo{UserID: userid.MustNew("user-1")},
				"worker."+leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, []byte("{}"), "wkr-other")
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err),
				"the originating code must survive the relay, not collapse to Internal")
		})
	}
}

func TestRouter_CallInner_HubNamespace_NotConfigured(t *testing.T) {
	r := &controlipc.Router{UserID: userid.MustNew("u-1")}
	_, err := r.CallInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("u-1")},
		"hub.LocateTab", []byte("{}"), "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestRouter_CallInner_UnknownNamespaceRejected(t *testing.T) {
	r := &controlipc.Router{UserID: userid.MustNew("u")}
	_, err := r.CallInner(context.Background(), controlipc.TokenInfo{}, "garbage.method", nil, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRouter_CallInner_LocalDispatcher_NilReturnsUnimplemented(t *testing.T) {
	r := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u")}
	resp, err := r.CallInner(context.Background(), controlipc.TokenInfo{}, "worker.X", nil, "A")
	require.NoError(t, err)
	assert.NotNil(t, resp.GetError())
	assert.Contains(t, resp.GetError().GetMessage(), "local dispatcher not configured")
}

func TestRouter_StreamInner_LocalDispatch(t *testing.T) {
	// Streaming handlers emit until ctx cancellation (matching the
	// real WatchEvents handler's lifecycle); we cancel after frames
	// arrive at the test sink so the router unblocks.
	dispatcher := &fakeLocalDispatcher{
		emitStream: [][]byte{[]byte("a"), []byte("b")},
	}
	streams := &fakeStreams{}
	r := &controlipc.Router{
		WorkerID:        "A",
		UserID:          userid.MustNew("u"),
		LocalDispatcher: dispatcher,
		Streams:         streams,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	got := make(chan []byte, 4)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(ctx, controlipc.TokenInfo{}, "worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-1",
			func(env *leapmuxv1.InnerStreamMessage) error {
				got <- env.GetPayload()
				return nil
			})
	}()

	for i := 0; i < 2; i++ {
		select {
		case payload := <-got:
			if i == 0 {
				assert.Equal(t, []byte("a"), payload)
			} else {
				assert.Equal(t, []byte("b"), payload)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("expected stream frame %d", i)
		}
	}
	cancel()
	select {
	case err := <-streamErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamInner didn't return after ctx cancel")
	}
	// The stream minted a synthetic localipc:* id and released it when it ended,
	// exactly as a unary call does.
	require.Len(t, streams.released, 1)
	assert.True(t, strings.HasPrefix(streams.released[0], "localipc:"),
		"the released id is the stream's synthetic local id, got %q", streams.released[0])
}

// TestRouter_StreamInner_TerminalResponsePayloadForwarded pins that a
// streaming handler that signals end-of-stream via SendResponse with a
// non-empty payload has that payload delivered to the IPC consumer as
// a terminal envelope (End=true). The earlier streamCollector
// silently dropped resp.Payload — a streaming-shaped sender that
// emitted a single final frame via SendResponse (a fast-path that
// produced one terminal message, or a unary-shaped result reaching a
// streaming ResponseWriter) ended the stream with the payload lost.
func TestRouter_StreamInner_TerminalResponsePayloadForwarded(t *testing.T) {
	dispatcher := &fakeLocalDispatcher{
		// No intermediate frames; the entire response rides on the
		// terminal SendResponse.
		respPayload: []byte("final-bytes"),
	}
	r := &controlipc.Router{
		WorkerID:        "A",
		UserID:          userid.MustNew("u"),
		LocalDispatcher: dispatcher,
		Streams:         &fakeStreams{},
	}

	got := make(chan *leapmuxv1.InnerStreamMessage, 4)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(context.Background(), controlipc.TokenInfo{}, "worker.OneShotStream", []byte("{}"), "A", "req-1",
			func(env *leapmuxv1.InnerStreamMessage) error {
				got <- env
				return nil
			})
	}()

	select {
	case env := <-got:
		assert.Equal(t, []byte("final-bytes"), env.GetPayload())
		assert.True(t, env.GetEnd())
		assert.Nil(t, env.GetError())
	case <-time.After(2 * time.Second):
		t.Fatal("expected terminal payload envelope")
	}
	select {
	case err := <-streamErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamInner didn't return after terminal SendResponse")
	}
}

// TestRouter_StreamInner_TerminalEmptyResponseNoExtraEnvelope pins the
// "done, nothing more" path: an empty SendResponse (the slowStreamDispatcher
// pattern) must NOT push a synthetic empty envelope through onMsg —
// downstream consumers can't tell that apart from a real empty frame
// and would mis-render a trailing blank.
func TestRouter_StreamInner_TerminalEmptyResponseNoExtraEnvelope(t *testing.T) {
	stop := make(chan struct{})
	dispatcher := &slowStreamDispatcher{stop: stop, emitted: &atomic.Int32{}}
	r := &controlipc.Router{
		WorkerID:        "A",
		UserID:          userid.MustNew("u"),
		LocalDispatcher: dispatcher,
		Streams:         &fakeStreams{},
	}

	received := make(chan *leapmuxv1.InnerStreamMessage, 32)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(context.Background(), controlipc.TokenInfo{}, "worker.Slow", []byte("{}"), "A", "req-1",
			func(env *leapmuxv1.InnerStreamMessage) error {
				received <- env
				return nil
			})
	}()

	// Let at least one streamed frame land, then signal the handler to
	// terminate via SendResponse{} (empty payload).
	select {
	case env := <-received:
		assert.Equal(t, []byte("tick"), env.GetPayload())
		assert.False(t, env.GetEnd())
	case <-time.After(2 * time.Second):
		t.Fatal("expected at least one streamed frame")
	}
	close(stop)

	select {
	case err := <-streamErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamInner didn't return after terminal SendResponse{}")
	}
	// Drain any in-flight ticks. None of them should be the End-marked
	// envelope: the empty SendResponse path emits nothing.
	close(received)
	for env := range received {
		assert.False(t, env.GetEnd(), "empty SendResponse must not synthesize an End envelope")
	}
}

func TestRouter_StreamInner_Cancellable(t *testing.T) {
	// Slow dispatcher: emits frames forever until ctx cancellation.
	emitted := atomic.Int32{}
	dispatcher := &slowStreamDispatcher{
		stop:    make(chan struct{}),
		emitted: &emitted,
	}
	t.Cleanup(func() { close(dispatcher.stop) })
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}

	ctx, cancel := context.WithCancel(context.Background())
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(ctx, controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte{}, "A", "req-x",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	// Wait until we've seen at least a few frames so we know the
	// dispatcher loop is live, then cancel.
	deadline := time.After(2 * time.Second)
	for emitted.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("dispatcher never emitted; cancellation harness broken")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	select {
	case <-streamErr:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamInner didn't return after ctx cancellation")
	}
}

func TestRouter_UpdateStreamCancel_RetiresTheStream(t *testing.T) {
	stop := make(chan struct{})
	dispatcher := &slowStreamDispatcher{
		stop:    stop,
		emitted: &atomic.Int32{},
	}
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	})
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte{}, "A", "req-cancel",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	for dispatcher.emitted.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	require.NoError(t, r.UpdateStream("req-cancel", nil, true))
	select {
	case <-streamErr:
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateStream with cancel did not unblock the streamer")
	}
}

func TestRouter_UpdateStream_DeliversToController(t *testing.T) {
	ctrl := &recordingStreamController{}
	ready := make(chan struct{})
	dispatcher := &ctrlStreamDispatcher{ctrl: ctrl, ready: ready}
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}
	go func() {
		_ = r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-update",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	<-ready
	require.NoError(t, r.UpdateStream("req-update", []byte("revision"), false))
	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return len(ctrl.payloads) == 1 && string(ctrl.payloads[0]) == "revision"
	}, time.Second, 10*time.Millisecond)
}

func TestRouter_UpdateStream_CrossWorkerDeliversToBoundController(t *testing.T) {
	ctrl := &recordingStreamController{}
	hold := make(chan struct{})
	cross := &fakeCrossWorker{bindCtrl: ctrl, hold: hold}
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		CrossWorker: cross,
	}
	done := make(chan error, 1)
	go func() {
		done <- r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "B", "req-xw-update",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	// Give StreamInner time to bind the controller before UpdateStream.
	require.Eventually(t, func() bool {
		_, ok := r.StreamCancellers.Load("req-xw-update")
		return ok
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, r.UpdateStream("req-xw-update", []byte("sibling-revision"), false))
	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return len(ctrl.payloads) == 1 && string(ctrl.payloads[0]) == "sibling-revision"
	}, time.Second, 10*time.Millisecond)
	close(hold)
	require.NoError(t, <-done)
}

func TestRouter_UpdateStreamCancel_CrossWorkerCallsOnCancel(t *testing.T) {
	ctrl := &recordingStreamController{}
	hold := make(chan struct{})
	cross := &fakeCrossWorker{bindCtrl: ctrl, hold: hold}
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		CrossWorker: cross,
	}
	done := make(chan error, 1)
	go func() {
		done <- r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "B", "req-xw-cancel",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	require.Eventually(t, func() bool {
		_, ok := r.StreamCancellers.Load("req-xw-cancel")
		return ok
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, r.UpdateStream("req-xw-cancel", nil, true))
	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return ctrl.cancelled
	}, time.Second, 10*time.Millisecond)
	// A cancel through UpdateStream cancels the stream ctx, which unblocks hold via ctx.Done.
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestRouter_UpdateStream_UnknownOrEmptyIdIsNoop(t *testing.T) {
	r := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u")}
	// Must not panic — empty and unbound ids are quiet no-ops, for a
	// revision and for a cancel alike.
	require.NoError(t, r.UpdateStream("", []byte("x"), false))
	require.NoError(t, r.UpdateStream("no-such-req", []byte("x"), false))
	require.NoError(t, r.UpdateStream("", nil, true))
	require.NoError(t, r.UpdateStream("no-such-req", nil, true))
}

func TestRouter_UpdateStreamCancel_CallsOnCancel(t *testing.T) {
	ctrl := &recordingStreamController{}
	ready := make(chan struct{})
	order := make(chan string, 2)
	dispatcher := &ctrlStreamDispatcher{ctrl: ctrl, ready: ready, onCancelOrder: order}
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}
	go func() {
		_ = r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-oncancel",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	<-ready
	require.NoError(t, r.UpdateStream("req-oncancel", nil, true))
	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return ctrl.cancelled
	}, time.Second, 10*time.Millisecond)
	first := <-order
	assert.Equal(t, "cancel", first, "OnCancel must run before the stream ctx is cancelled")
}

// TestRouter_BindStream_RefusesAfterCancel pins the TOCTOU fix: when a
// cancel through UpdateStream retires an entry between the initial
// StreamCancellers.Store (in StreamInner) and the handler's BindStream call,
// BindStream must return
// ok=false so the handler runs its own retirement. Without it, the controller
// was installed on a retired entry, never received OnCancel from the registry,
// and leaked on its bgCtx for the process lifetime.
func TestRouter_BindStream_RefusesAfterCancel(t *testing.T) {
	ctrl := &recordingStreamController{}
	dispatcher := &cancelRaceDispatcher{
		ctrl:       ctrl,
		registered: make(chan struct{}),
		proceed:    make(chan struct{}),
		bindResult: make(chan bool, 1),
	}
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}

	done := make(chan error, 1)
	go func() {
		done <- r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-bind-race",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()

	// Wait for the handler to register its entry but BEFORE it calls BindStream
	// (the dispatcher parks on ready until we release it), then retire it.
	<-dispatcher.registered
	require.NoError(t, r.UpdateStream("req-bind-race", nil, true))
	close(dispatcher.proceed)

	// The bind must observe the retirement and return ok=false. The handler
	// then owns its ctx-based retirement (the <-ctx.Done() in DispatchWith).
	require.Eventually(t, func() bool {
		select {
		case res := <-dispatcher.bindResult:
			return !res
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "BindStream must return ok=false when a cancel retired the entry")

	// The late-bound controller must never receive OnCancel from the registry
	// (it was never installed); the handler's ctx cancel is the sole retirement.
	assert.False(t, ctrl.cancelled, "late controller must not receive registry OnCancel")

	// Drain the StreamInner goroutine.
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

type cancelRaceDispatcher struct {
	ctrl       *recordingStreamController
	registered chan struct{}
	proceed    chan struct{}
	bindResult chan bool
}

func (d *cancelRaceDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	// Simulate the watchSession handler: the entry is already stored by
	// StreamInner, so signal that the cancel race window is open, wait for it
	// to be retired, then attempt the bind.
	close(d.registered)
	<-d.proceed
	release, ok := w.BindStream(d.ctrl)
	d.bindResult <- ok
	if ok {
		defer release()
	}
	<-ctx.Done()
}

type recordingStreamController struct {
	mu           sync.Mutex
	payloads     [][]byte
	cancelled    bool
	onCancelHook func()
}

func (c *recordingStreamController) OnClientFrame(payload []byte) {
	c.mu.Lock()
	c.payloads = append(c.payloads, append([]byte(nil), payload...))
	c.mu.Unlock()
}

func (c *recordingStreamController) OnCancel() {
	if c.onCancelHook != nil {
		c.onCancelHook()
	}
	c.mu.Lock()
	c.cancelled = true
	c.mu.Unlock()
}

type ctrlStreamDispatcher struct {
	ctrl          *recordingStreamController
	ready         chan struct{}
	onCancelOrder chan string
}

func (d *ctrlStreamDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	release, _ := w.BindStream(d.ctrl)
	defer release()
	if d.onCancelOrder != nil {
		orig := d.ctrl.onCancelHook
		d.ctrl.onCancelHook = func() {
			d.onCancelOrder <- "cancel"
			if orig != nil {
				orig()
			}
		}
	}
	close(d.ready)
	<-ctx.Done()
	if d.onCancelOrder != nil {
		d.onCancelOrder <- "ctx"
	}
}

func TestEnvVars_AgentSetsAllExpected(t *testing.T) {
	envs := controlipc.EnvVars("unix:/tmp/sock", "raw-token", controlipc.TokenInfo{
		UserID: userid.MustNew("u-1"),
		// present on TokenInfo for delegation scoping; intentionally NOT emitted as env
		WorkerID:      "worker-A",
		TabID:         "agent-1",
		TabType:       leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkingDir:    "/work/dir",
		AgentProvider: "claude-code",
	})
	want := map[string]string{
		"LEAPMUX_CONTROL_SOCK":           "unix:/tmp/sock",
		"LEAPMUX_CONTROL_TOKEN":          "raw-token",
		"LEAPMUX_CONTROL_USER_ID":        "u-1",
		"LEAPMUX_CONTROL_WORKER_ID":      "worker-A",
		"LEAPMUX_CONTROL_TAB_ID":         "agent-1",
		"LEAPMUX_CONTROL_TAB_TYPE":       "agent",
		"LEAPMUX_CONTROL_WORKING_DIR":    "/work/dir",
		"LEAPMUX_CONTROL_AGENT_PROVIDER": "claude-code",
	}
	got := map[string]string{}
	for _, e := range envs {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		got[k] = v
	}
	for k, v := range want {
		assert.Equal(t, v, got[k], "agent spawn must set %s=%s", k, v)
	}
	// Workspace id, tile id, and the legacy unsuffixed / _AGENT / _TERMINAL
	// names are intentionally NOT injected — workspace/tile can change
	// after spawn (cross-workspace move, tile drag) and a stale env var
	// would mislead `leapmux control` invocations; the legacy names were
	// replaced by the _ID-suffixed canonical set.
	forbidden := []string{
		"LEAPMUX_CONTROL_WORKSPACE",
		"LEAPMUX_CONTROL_WORKSPACE_ID",
		"LEAPMUX_CONTROL_TILE",
		"LEAPMUX_CONTROL_TILE_ID",
		"LEAPMUX_CONTROL_USER",
		"LEAPMUX_CONTROL_WORKER",
		"LEAPMUX_CONTROL_AGENT",
		"LEAPMUX_CONTROL_TERMINAL",
		"LEAPMUX_CONTROL_ORG",
		"LEAPMUX_CONTROL_ORG_ID",
	}
	for _, k := range forbidden {
		_, present := got[k]
		assert.False(t, present, "%s must NOT be injected", k)
	}
}

func TestEnvVars_TerminalTabTypeIsTerminal(t *testing.T) {
	envs := controlipc.EnvVars("unix:/tmp/sock", "raw-token", controlipc.TokenInfo{
		UserID:   userid.MustNew("u-1"),
		WorkerID: "worker-A",
		TabID:    "term-1",
		TabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	})
	got := map[string]string{}
	for _, e := range envs {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		got[k] = v
	}
	assert.Equal(t, "term-1", got["LEAPMUX_CONTROL_TAB_ID"], "terminal spawn must set LEAPMUX_CONTROL_TAB_ID")
	assert.Equal(t, "terminal", got["LEAPMUX_CONTROL_TAB_TYPE"], "terminal spawn must set LEAPMUX_CONTROL_TAB_TYPE=terminal")
	for _, k := range []string{"LEAPMUX_CONTROL_AGENT", "LEAPMUX_CONTROL_TERMINAL"} {
		_, present := got[k]
		assert.False(t, present, "legacy %s must NOT be injected", k)
	}
}

// --- Test fakes ---

type fakeHubClient struct {
	mu          sync.Mutex
	lastUserID  userid.UserID
	lastMethod  string
	lastPayload []byte
	resp        []byte
	respErr     error
}

func (f *fakeHubClient) CallInner(_ context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUserID = userID
	f.lastMethod = method
	f.lastPayload = append([]byte(nil), payload...)
	return f.resp, f.respErr
}

// slowStreamDispatcher emits frames at ~5ms intervals until either
// stop is closed or the ResponseWriter signals an error (which
// happens when the request context is cancelled).
type slowStreamDispatcher struct {
	stop    chan struct{}
	emitted *atomic.Int32
}

func (d *slowStreamDispatcher) DispatchWith(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	for {
		select {
		case <-d.stop:
			_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{})
			return
		case <-time.After(5 * time.Millisecond):
			err := w.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("tick")})
			d.emitted.Add(1)
			if err != nil {
				return
			}
		}
	}
}

// Compile-time interface assertions so a refactor that breaks the
// fakes' contract surfaces at build time, not test time.
var (
	_ controlipc.LocalDispatcher   = (*fakeLocalDispatcher)(nil)
	_ controlipc.LocalDispatcher   = (*slowStreamDispatcher)(nil)
	_ controlipc.CrossWorkerClient = (*fakeCrossWorker)(nil)
	_ controlipc.HubClient         = (*fakeHubClient)(nil)
	_ controlipc.LocalStreams      = (*fakeStreams)(nil)
	_ controlipc.CrossWorkerClient = (*framesCrossWorker)(nil)
	_ controlipc.HubStreamer       = (*fakeHubStreamer)(nil)
)

// asyncErrorDispatcher returns from the handler WITHOUT terminating the stream,
// then delivers the terminal error frame from another goroutine -- exactly the
// shape WatchEvents has (it "returns after registering watchers + completing
// the synchronous replay", and live broadcasts arrive later on WatcherManager
// goroutines), and the shape a streaming denial now has.
type asyncErrorDispatcher struct {
	release chan struct{}
}

func (d *asyncErrorDispatcher) DispatchWith(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	go func() {
		<-d.release
		_ = w.SendStream(channelwire.NewStreamError(7, "boom"))
	}()
}

// TestRouter_StreamInner_AsyncTerminalErrorIsReported is the regression for a
// lost stream error.
//
// streamCollector used to close its `done` channel inside finish() and assign
// c.err AFTERWARDS, so wait() was released BEFORE the write landed: streamLocal
// read c.err concurrently with the writer and, on the unlucky scheduling,
// reported a failed stream to the `leapmux control` caller as success. The type's
// own comment claimed close-of-done supplied the happens-before edge; it
// supplied the opposite one.
//
// Run under -race, which catches the unsynchronized access even on an
// interleaving that happens to return the right value. Repeated because the
// window is small.
func TestRouter_StreamInner_AsyncTerminalErrorIsReported(t *testing.T) {
	for i := range 50 {
		release := make(chan struct{})
		r := &controlipc.Router{
			WorkerID:        "A",
			UserID:          userid.MustNew("u"),
			LocalDispatcher: &asyncErrorDispatcher{release: release},
			Streams:         &fakeStreams{},
		}

		streamErr := make(chan error, 1)
		go func() {
			streamErr <- r.StreamInner(context.Background(), controlipc.TokenInfo{}, "worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-1",
				func(*leapmuxv1.InnerStreamMessage) error { return nil })
		}()
		close(release)

		select {
		case err := <-streamErr:
			require.Error(t, err, "iteration %d: a terminal error frame must reach the caller, never be dropped as success", i)
			assert.Contains(t, err.Error(), "boom")
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: StreamInner never returned", i)
		}
	}
}

// funcDispatcher runs its own function as the local dispatcher.
type funcDispatcher func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter)

func (f funcDispatcher) DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	f(ctx, caller, req, w)
}

// The router keys a live stream's entry by its client request id. StreamInner
// removes the entry when it returns, so the map holds only live streams.
func TestRouterRemovesTheStreamEntryWhenTheStreamEnds(t *testing.T) {
	r := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u")}
	var registeredDuringDispatch bool
	r.LocalDispatcher = funcDispatcher(func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
		_, registeredDuringDispatch = r.StreamCancellers.Load("req-ends")
		_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})

	require.NoError(t, r.StreamInner(context.Background(), controlipc.TokenInfo{},
		"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, nil, "", "req-ends",
		func(*leapmuxv1.InnerStreamMessage) error { return nil }))

	assert.True(t, registeredDuringDispatch, "the stream holds its entry while it runs")
	_, registered := r.StreamCancellers.Load("req-ends")
	assert.False(t, registered, "the ended stream leaves no entry behind")
}

// The cleanup runs in a defer, so a handler that panics leaves no entry
// behind either: no entry can outlive its stream, and none needs a sweep.
func TestRouterRemovesTheStreamEntryWhenTheHandlerPanics(t *testing.T) {
	r := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u")}
	var registeredDuringDispatch bool
	r.LocalDispatcher = funcDispatcher(func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
		_, registeredDuringDispatch = r.StreamCancellers.Load("req-panic")
		panic("handler bug")
	})

	recovered := func() (value any) {
		defer func() { value = recover() }()
		_ = r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, nil, "", "req-panic",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
		return nil
	}()

	require.Equal(t, "handler bug", recovered, "the handler's panic reaches the caller")
	assert.True(t, registeredDuringDispatch, "the stream holds its entry while it runs")
	_, registered := r.StreamCancellers.Load("req-panic")
	assert.False(t, registered, "the deferred cleanup removes the entry of a panicking stream")
}

// A cancel retires the stream and ignores any payload that rides with it. It
// removes the entry at once, before the stream returns, so a revision that
// arrives later finds no stream and reaches no controller.
func TestRouter_UpdateStreamCancel_DropsThePayloadAndRemovesTheEntry(t *testing.T) {
	ctrl := &recordingStreamController{}
	ready := make(chan struct{})
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: &ctrlStreamDispatcher{ctrl: ctrl, ready: ready},
	}
	done := make(chan error, 1)
	go func() {
		done <- r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-cancel-payload",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	<-ready

	require.NoError(t, r.UpdateStream("req-cancel-payload", []byte("not a revision"), true))
	_, registered := r.StreamCancellers.Load("req-cancel-payload")
	assert.False(t, registered, "the cancel removes the entry before the stream returns")
	require.NoError(t, r.UpdateStream("req-cancel-payload", []byte("late revision"), false),
		"a revision for a retired stream is a quiet no-op")

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled stream did not return")
	}
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	assert.True(t, ctrl.cancelled, "the cancel reaches the controller")
	assert.Empty(t, ctrl.payloads, "neither the payload of the cancel nor the late revision reaches the controller")
}

// A revision that overflows the inbox of a stream whose controller is not bound
// yet returns an error, so the client learns that its revision was lost. The
// same overflow retires the stream: the controller is never bound, and the
// stream returns.
func TestRouter_UpdateStream_OverflowReportsAnErrorAndRetiresTheStream(t *testing.T) {
	dispatcher := &cancelRaceDispatcher{
		ctrl:       &recordingStreamController{},
		registered: make(chan struct{}),
		proceed:    make(chan struct{}),
		bindResult: make(chan bool, 1),
	}
	r := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u"), LocalDispatcher: dispatcher}
	done := make(chan error, 1)
	go func() {
		done <- r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "A", "req-overflow",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
	}()
	<-dispatcher.registered

	// The inbox limit belongs to the channel package, so the loop finds it
	// instead of restating it. The cap only keeps a broken limit from spinning.
	var overflow error
	for range 10_000 {
		if overflow = r.UpdateStream("req-overflow", []byte("revision"), false); overflow != nil {
			break
		}
	}
	require.ErrorIs(t, overflow, channel.ErrStreamInboxFull)

	close(dispatcher.proceed)
	assert.False(t, <-dispatcher.bindResult, "an overflowed stream refuses its controller")
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the overflowed stream did not return")
	}
	_, registered := r.StreamCancellers.Load("req-overflow")
	assert.False(t, registered, "the retired stream leaves no entry behind")
}

// A local unary handler reports its failure in-band: CallInner succeeds at the
// transport level and returns a response whose error field carries the code
// and the message. A handler that answers nothing gives an empty success.
func TestRouter_CallInner_LocalFailureStaysInBand(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reply       func(w channel.ResponseWriter)
		wantError   *leapmuxv1.RpcError
		wantPayload []byte
	}{
		{
			name:      "an error the handler sends",
			reply:     func(w channel.ResponseWriter) { _ = w.SendError(5, "not found") },
			wantError: &leapmuxv1.RpcError{Code: 5, Message: "not found"},
		},
		{
			name:      "a response that carries an error",
			reply:     func(w channel.ResponseWriter) { _ = w.SendResponse(channelwire.NewErrorResponse(9, "refused")) },
			wantError: &leapmuxv1.RpcError{Code: 9, Message: "refused"},
		},
		{
			name: "an error after a response",
			reply: func(w channel.ResponseWriter) {
				_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: []byte("partial")})
				_ = w.SendError(13, "failed late")
			},
			wantError: &leapmuxv1.RpcError{Code: 13, Message: "failed late"},
		},
		{
			name:  "no reply",
			reply: func(channel.ResponseWriter) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u")}
			r.LocalDispatcher = funcDispatcher(func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
				tc.reply(w)
			})

			resp, err := r.CallInner(context.Background(), controlipc.TokenInfo{},
				"worker."+leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, nil, "")
			require.NoError(t, err, "an in-band failure is not a transport error")
			require.NotNil(t, resp)
			if tc.wantError == nil {
				assert.Nil(t, resp.GetError())
			} else {
				require.NotNil(t, resp.GetError())
				assert.Equal(t, tc.wantError.GetCode(), resp.GetError().GetCode())
				assert.Equal(t, tc.wantError.GetMessage(), resp.GetError().GetMessage())
			}
			assert.Equal(t, tc.wantPayload, resp.GetPayload())
		})
	}
}

// framesCrossWorker plays a sibling worker's stream: it hands each frame to the
// router and then returns err, as crossworker.Client.StreamInner does.
type framesCrossWorker struct {
	frames []*leapmuxv1.InnerStreamMessage
	err    error
}

func (*framesCrossWorker) CallInner(context.Context, string, userid.UserID, string, []byte) ([]byte, error) {
	return nil, errors.New("framesCrossWorker serves streams only")
}

func (f *framesCrossWorker) StreamInner(_ context.Context, _ string, _ userid.UserID, _ string, _ []byte, onMsg func(*leapmuxv1.InnerStreamMessage), _ func(channel.StreamController)) error {
	for _, frame := range f.frames {
		onMsg(frame)
	}
	return f.err
}

// A sibling worker's frames reach the client unchanged, an error frame
// included: its code and message, and its End, are what the client reads to
// stop.
func TestRouter_StreamInner_CrossWorkerForwardsEachFrameUnchanged(t *testing.T) {
	data := &leapmuxv1.InnerStreamMessage{Payload: []byte("event")}
	failure := channelwire.NewStreamError(7, "denied")
	streamErr := errors.New("stream error (code 7): denied")
	r := &controlipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		CrossWorker: &framesCrossWorker{frames: []*leapmuxv1.InnerStreamMessage{data, failure}, err: streamErr},
	}

	var got []*leapmuxv1.InnerStreamMessage
	err := r.StreamInner(context.Background(), controlipc.TokenInfo{},
		"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, nil, "B", "",
		func(msg *leapmuxv1.InnerStreamMessage) error {
			got = append(got, msg)
			return nil
		})

	require.ErrorIs(t, err, streamErr, "the sibling's own error is what the stream returns")
	require.Len(t, got, 2)
	assert.Same(t, data, got[0])
	assert.Same(t, failure, got[1], "the error frame is forwarded as it is, not rebuilt")
	require.NotNil(t, got[1].GetError())
	assert.Equal(t, int32(7), got[1].GetError().GetCode())
	assert.Equal(t, "denied", got[1].GetError().GetMessage())
	assert.True(t, got[1].GetEnd())
}

// fakeHubStreamer plays the hub: it records the call and hands each payload to
// the router.
type fakeHubStreamer struct {
	payloads   [][]byte
	gotUserID  userid.UserID
	gotMethod  string
	gotPayload []byte
}

func (f *fakeHubStreamer) StreamHub(_ context.Context, userID userid.UserID, method string, payload []byte, onPayload func([]byte) error) error {
	f.gotUserID, f.gotMethod, f.gotPayload = userID, method, payload
	for _, p := range f.payloads {
		if err := onPayload(p); err != nil {
			return err
		}
	}
	return nil
}

// A hub stream carries only payloads. The router wraps each one in a stream
// frame with no End and no error, and a frame that the client cannot take stops
// the stream with the client's error.
func TestRouter_StreamInner_HubNamespaceWrapsEachPayload(t *testing.T) {
	t.Run("each payload becomes one frame", func(t *testing.T) {
		hub := &fakeHubStreamer{payloads: [][]byte{[]byte("a"), []byte("b")}}
		r := &controlipc.Router{UserID: userid.MustNew("user-1"), HubStreams: hub}

		var got []*leapmuxv1.InnerStreamMessage
		require.NoError(t, r.StreamInner(context.Background(), controlipc.TokenInfo{},
			"hub.WatchUser", []byte("request"), "", "",
			func(msg *leapmuxv1.InnerStreamMessage) error {
				got = append(got, msg)
				return nil
			}))

		require.Len(t, got, 2)
		for i, want := range []string{"a", "b"} {
			assert.Equal(t, want, string(got[i].GetPayload()))
			assert.False(t, got[i].GetEnd(), "a hub payload does not end the stream")
			assert.Nil(t, got[i].GetError())
		}
		assert.Equal(t, "WatchUser", hub.gotMethod, "the hub receives the method without its namespace")
		assert.Equal(t, []byte("request"), hub.gotPayload)
		assert.Equal(t, "user-1", hub.gotUserID.String(), "the hub call runs as the router's user")
	})

	t.Run("a delivery failure stops the stream", func(t *testing.T) {
		hub := &fakeHubStreamer{payloads: [][]byte{[]byte("a"), []byte("b")}}
		r := &controlipc.Router{UserID: userid.MustNew("user-1"), HubStreams: hub}
		sendErr := errors.New("client went away")

		calls := 0
		err := r.StreamInner(context.Background(), controlipc.TokenInfo{}, "hub.WatchUser", nil, "", "",
			func(*leapmuxv1.InnerStreamMessage) error {
				calls++
				return sendErr
			})

		require.ErrorIs(t, err, sendErr)
		assert.Equal(t, 1, calls, "no payload follows a failed delivery")
	})

	t.Run("a router with no hub streamer refuses", func(t *testing.T) {
		r := &controlipc.Router{UserID: userid.MustNew("user-1")}
		err := r.StreamInner(context.Background(), controlipc.TokenInfo{}, "hub.WatchUser", nil, "", "",
			func(*leapmuxv1.InnerStreamMessage) error { return nil })
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	})
}

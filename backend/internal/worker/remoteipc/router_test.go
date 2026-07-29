package remoteipc_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
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

func (f *fakeLocalDispatcher) DispatchWith(_ context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	f.mu.Lock()
	f.gotUserID = userID
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
}

func (f *fakeCrossWorker) CallInner(_ context.Context, target string, _ userid.UserID, method string, payload []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callTarget, f.callMethod, f.callPayload = target, method, payload
	return f.resp, f.respErr
}

func (f *fakeCrossWorker) StreamInner(_ context.Context, target string, _ userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage)) error {
	f.mu.Lock()
	f.callTarget, f.callMethod, f.callPayload = target, method, payload
	f.mu.Unlock()
	if f.respErr != nil {
		return f.respErr
	}
	onMsg(&leapmuxv1.InnerStreamMessage{Payload: f.resp, End: true})
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
	r := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("user-1"),
		LocalDispatcher: dispatcher,
		Streams:         streams,
	}
	resp, err := r.CallInner(context.Background(),
		remoteipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
		"worker.OpenAgent", []byte("payload"),
		"worker-A")
	require.NoError(t, err)
	require.False(t, resp.GetIsError(), "unexpected error: %s", resp.GetErrorMessage())
	assert.Equal(t, []byte("hello"), resp.GetPayload())

	// The dispatcher saw the bare method (namespace stripped) and the
	// request user id propagated from the router.
	assert.Equal(t, "OpenAgent", dispatcher.gotMethod)
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
		info        remoteipc.TokenInfo
		wantSegment string
	}{
		{
			name:        "agent",
			info:        remoteipc.TokenInfo{UserID: userid.MustNew("u-1"), TabID: "agent-XYZ", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			wantSegment: "agent-agent-XYZ", // prefix "agent-" + the TabID value.
		},
		{
			name:        "terminal",
			info:        remoteipc.TokenInfo{UserID: userid.MustNew("u-1"), TabID: "term-7", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
			wantSegment: "terminal-term-7",
		},
		{
			name:        "user-only-fallback",
			info:        remoteipc.TokenInfo{UserID: userid.MustNew("u-9")},
			wantSegment: "user-u-9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &fakeLocalDispatcher{respPayload: []byte("ok")}
			streams := &fakeStreams{}
			r := &remoteipc.Router{
				WorkerID:        "worker-A",
				UserID:          tc.info.UserID,
				LocalDispatcher: dispatcher,
				Streams:         streams,
			}
			_, err := r.CallInner(context.Background(), tc.info,
				"worker.OpenAgent", []byte("p"), "worker-A")
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
	r := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-1"),
		LocalDispatcher: dispatcher,
		Streams:         streams,
	}
	info := remoteipc.TokenInfo{UserID: userid.MustNew("u-1"), TabID: "agent-X", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
	for i := 0; i < 3; i++ {
		_, err := r.CallInner(context.Background(), info,
			"worker.OpenAgent", []byte("p"), "worker-A")
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
	r := &remoteipc.Router{
		WorkerID:    "worker-A",
		UserID:      userid.MustNew("user-1"),
		CrossWorker: cross,
	}
	resp, err := r.CallInner(context.Background(),
		remoteipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
		"worker.SendAgentMessage", []byte("hi"),
		"worker-B")
	require.NoError(t, err)
	assert.Equal(t, []byte("from-B"), resp.GetPayload())
	assert.Equal(t, "worker-B", cross.callTarget)
	assert.Equal(t, "SendAgentMessage", cross.callMethod)
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
	r := &remoteipc.Router{
		WorkerID:    "worker-A",
		UserID:      userid.MustNew("user-1"),
		CrossWorker: cross,
	}
	for _, method := range []string{
		"worker.ListDirectory",
		"worker.ReadFile",
		"worker.StatFile",
		"worker.GitStatus",
	} {
		cross.callTarget = ""
		cross.callMethod = ""
		resp, err := r.CallInner(context.Background(),
			remoteipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
			method, []byte(`{}`), "worker-B")
		require.NoError(t, err, method)
		assert.Equal(t, []byte("file-bytes"), resp.GetPayload(), method)
		assert.Equal(t, "worker-B", cross.callTarget, method)
	}
}

func TestRouter_CallInner_HubNamespace(t *testing.T) {
	hub := &fakeHubClient{resp: []byte("hub-ok")}
	r := &remoteipc.Router{
		UserID: userid.MustNew("user-1"),
		Hub:    hub,
	}
	resp, err := r.CallInner(context.Background(),
		remoteipc.TokenInfo{UserID: userid.MustNew("user-1")},
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
	r := &remoteipc.Router{
		UserID: userid.MustNew("user-1"),
		Hub:    hub,
	}
	_, err := r.CallInner(context.Background(),
		remoteipc.TokenInfo{UserID: userid.MustNew("user-1")},
		"hub.GetTab", []byte("{}"), "")
	require.NoError(t, err)
}

func TestRouter_CallInner_HubNamespace_PropagatesError(t *testing.T) {
	hub := &fakeHubClient{respErr: errors.New("boom")}
	r := &remoteipc.Router{
		UserID: userid.MustNew("user-1"),
		Hub:    hub,
	}
	_, err := r.CallInner(context.Background(),
		remoteipc.TokenInfo{UserID: userid.MustNew("user-1")},
		"hub.GetTab", []byte("{}"), "")
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
			hub := &fakeHubClient{respErr: connect.NewError(tc.code, errors.New("upstream"))}
			r := &remoteipc.Router{
				UserID: userid.MustNew("user-1"),
				Hub:    hub,
			}
			_, err := r.CallInner(context.Background(),
				remoteipc.TokenInfo{UserID: userid.MustNew("user-1")},
				"hub.GetTab", []byte("{}"), "")
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err),
				"the originating code must survive the relay, not collapse to Internal")
		})

		t.Run("cross-worker namespace: "+tc.name, func(t *testing.T) {
			cw := &fakeCrossWorker{respErr: connect.NewError(tc.code, errors.New("upstream"))}
			r := &remoteipc.Router{
				UserID:      userid.MustNew("user-1"),
				WorkerID:    "wkr-self",
				CrossWorker: cw,
			}
			_, err := r.CallInner(context.Background(),
				remoteipc.TokenInfo{UserID: userid.MustNew("user-1")},
				"worker.GetTab", []byte("{}"), "wkr-other")
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err),
				"the originating code must survive the relay, not collapse to Internal")
		})
	}
}

func TestRouter_CallInner_HubNamespace_NotConfigured(t *testing.T) {
	r := &remoteipc.Router{UserID: userid.MustNew("u-1")}
	_, err := r.CallInner(context.Background(), remoteipc.TokenInfo{UserID: userid.MustNew("u-1")},
		"hub.GetTab", []byte("{}"), "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestRouter_CallInner_UnknownNamespaceRejected(t *testing.T) {
	r := &remoteipc.Router{UserID: userid.MustNew("u")}
	_, err := r.CallInner(context.Background(), remoteipc.TokenInfo{}, "garbage.method", nil, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRouter_CallInner_LocalDispatcher_NilReturnsUnimplemented(t *testing.T) {
	r := &remoteipc.Router{WorkerID: "A", UserID: userid.MustNew("u")}
	resp, err := r.CallInner(context.Background(), remoteipc.TokenInfo{}, "worker.X", nil, "A")
	require.NoError(t, err)
	assert.True(t, resp.GetIsError())
	assert.Contains(t, resp.GetErrorMessage(), "local dispatcher not configured")
}

func TestRouter_StreamInner_LocalDispatch(t *testing.T) {
	// Streaming handlers emit until ctx cancellation (matching the
	// real WatchEvents handler's lifecycle); we cancel after frames
	// arrive at the test sink so the router unblocks.
	dispatcher := &fakeLocalDispatcher{
		emitStream: [][]byte{[]byte("a"), []byte("b")},
	}
	streams := &fakeStreams{}
	r := &remoteipc.Router{
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
		streamErr <- r.StreamInner(ctx, remoteipc.TokenInfo{}, "worker.WatchEvents", []byte("{}"), "A", "req-1",
			func(env *leapmuxv1.StreamInnerEnvelope) error {
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
	require.Len(t, streams.released, 1)
	assert.Equal(t, streams.released[0], streams.released[0])
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
	r := &remoteipc.Router{
		WorkerID:        "A",
		UserID:          userid.MustNew("u"),
		LocalDispatcher: dispatcher,
		Streams:         &fakeStreams{},
	}

	got := make(chan *leapmuxv1.StreamInnerEnvelope, 4)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(context.Background(), remoteipc.TokenInfo{}, "worker.OneShotStream", []byte("{}"), "A", "req-1",
			func(env *leapmuxv1.StreamInnerEnvelope) error {
				got <- env
				return nil
			})
	}()

	select {
	case env := <-got:
		assert.Equal(t, []byte("final-bytes"), env.GetPayload())
		assert.True(t, env.GetEnd())
		assert.False(t, env.GetIsError())
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
	r := &remoteipc.Router{
		WorkerID:        "A",
		UserID:          userid.MustNew("u"),
		LocalDispatcher: dispatcher,
		Streams:         &fakeStreams{},
	}

	received := make(chan *leapmuxv1.StreamInnerEnvelope, 32)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(context.Background(), remoteipc.TokenInfo{}, "worker.Slow", []byte("{}"), "A", "req-1",
			func(env *leapmuxv1.StreamInnerEnvelope) error {
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
	r := &remoteipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}

	ctx, cancel := context.WithCancel(context.Background())
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(ctx, remoteipc.TokenInfo{},
			"worker.WatchEvents", []byte{}, "A", "req-x",
			func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
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

func TestRouter_CancelStream_ByClientRequestID(t *testing.T) {
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
	r := &remoteipc.Router{
		WorkerID: "A", UserID: userid.MustNew("u"),
		LocalDispatcher: dispatcher,
	}
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- r.StreamInner(context.Background(), remoteipc.TokenInfo{},
			"worker.WatchEvents", []byte{}, "A", "req-cancel",
			func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
	}()
	for dispatcher.emitted.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	r.CancelStream("req-cancel")
	select {
	case <-streamErr:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelStream didn't unblock the streamer")
	}
}

// TestRouter_SweepStaleCancellers pins the defense-in-depth reaper:
// entries registered before the cutoff are dropped AND their cancel
// function fires (so any goroutine still waiting on the stream
// context unblocks); fresh entries are left alone. Verifies the
// invariant the Server janitor relies on.
func TestRouter_SweepStaleCancellers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := &remoteipc.Router{Now: func() time.Time { return now }}

	// Register two streams: one ages well past the cutoff, the other
	// stays fresh by re-stamping its registeredAt after the clock
	// advances. The dropping path must fire the canceller for the
	// stale one so any goroutine still waiting on its context wakes
	// up — without that, the entry leaks AND its goroutine never
	// learns to exit.
	staleCalled := make(chan struct{}, 1)
	startStaleStream(t, r, "stale-req", staleCalled)

	now = now.Add(2 * time.Hour)

	freshCalled := make(chan struct{}, 1)
	startStaleStream(t, r, "fresh-req", freshCalled)

	// Cutoff between the two registrations (1 hour ago at "now").
	cutoff := now.Add(-time.Hour)
	dropped := r.SweepStaleCancellers(cutoff)
	assert.Equal(t, 1, dropped, "exactly one entry should be reaped")

	// Stale stream's context must have been cancelled.
	select {
	case <-staleCalled:
	case <-time.After(time.Second):
		t.Fatal("expected stale stream's cancel to fire")
	}

	// Fresh stream should still be running — no cancel signal yet.
	select {
	case <-freshCalled:
		t.Fatal("fresh stream cancel fired despite being newer than cutoff")
	case <-time.After(50 * time.Millisecond):
	}
}

// startStaleStream registers a stream on the router under clientReqID
// and signals `cancelled` when the stream's context is cancelled.
// Used by TestRouter_SweepStaleCancellers to observe per-entry cancel
// behavior.
func startStaleStream(t *testing.T, r *remoteipc.Router, clientReqID string, cancelled chan<- struct{}) {
	t.Helper()
	stop := make(chan struct{})
	dispatcher := &slowStreamDispatcher{stop: stop, emitted: &atomic.Int32{}}
	r.LocalDispatcher = dispatcher
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	})
	streamCh := make(chan error, 1)
	go func() {
		err := r.StreamInner(context.Background(), remoteipc.TokenInfo{},
			"worker.WatchEvents", []byte{}, "", clientReqID,
			func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
		streamCh <- err
		cancelled <- struct{}{}
	}()
	// Wait until the dispatcher has emitted at least one frame so we
	// know StreamInner registered the canceller in the map.
	for dispatcher.emitted.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEnvVars_AgentSetsAllExpected(t *testing.T) {
	envs := remoteipc.EnvVars("unix:/tmp/sock", "raw-token", remoteipc.TokenInfo{
		UserID: userid.MustNew("u-1"),
		// present on TokenInfo for delegation scoping; intentionally NOT emitted as env
		WorkerID:      "worker-A",
		TabID:         "agent-1",
		TabType:       leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkingDir:    "/work/dir",
		AgentProvider: "claude-code",
	})
	want := map[string]string{
		"LEAPMUX_REMOTE_SOCK":           "unix:/tmp/sock",
		"LEAPMUX_REMOTE_TOKEN":          "raw-token",
		"LEAPMUX_REMOTE_USER_ID":        "u-1",
		"LEAPMUX_REMOTE_WORKER_ID":      "worker-A",
		"LEAPMUX_REMOTE_TAB_ID":         "agent-1",
		"LEAPMUX_REMOTE_TAB_TYPE":       "agent",
		"LEAPMUX_REMOTE_WORKING_DIR":    "/work/dir",
		"LEAPMUX_REMOTE_AGENT_PROVIDER": "claude-code",
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
	// would mislead `leapmux remote` invocations; the legacy names were
	// replaced by the _ID-suffixed canonical set.
	forbidden := []string{
		"LEAPMUX_REMOTE_WORKSPACE",
		"LEAPMUX_REMOTE_WORKSPACE_ID",
		"LEAPMUX_REMOTE_TILE",
		"LEAPMUX_REMOTE_TILE_ID",
		"LEAPMUX_REMOTE_USER",
		"LEAPMUX_REMOTE_WORKER",
		"LEAPMUX_REMOTE_AGENT",
		"LEAPMUX_REMOTE_TERMINAL",
		"LEAPMUX_REMOTE_ORG",
		"LEAPMUX_REMOTE_ORG_ID",
	}
	for _, k := range forbidden {
		_, present := got[k]
		assert.False(t, present, "%s must NOT be injected", k)
	}
}

func TestEnvVars_TerminalTabTypeIsTerminal(t *testing.T) {
	envs := remoteipc.EnvVars("unix:/tmp/sock", "raw-token", remoteipc.TokenInfo{
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
	assert.Equal(t, "term-1", got["LEAPMUX_REMOTE_TAB_ID"], "terminal spawn must set LEAPMUX_REMOTE_TAB_ID")
	assert.Equal(t, "terminal", got["LEAPMUX_REMOTE_TAB_TYPE"], "terminal spawn must set LEAPMUX_REMOTE_TAB_TYPE=terminal")
	for _, k := range []string{"LEAPMUX_REMOTE_AGENT", "LEAPMUX_REMOTE_TERMINAL"} {
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

func (d *slowStreamDispatcher) DispatchWith(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
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
	_ remoteipc.LocalDispatcher   = (*fakeLocalDispatcher)(nil)
	_ remoteipc.LocalDispatcher   = (*slowStreamDispatcher)(nil)
	_ remoteipc.CrossWorkerClient = (*fakeCrossWorker)(nil)
	_ remoteipc.HubClient         = (*fakeHubClient)(nil)
	_ remoteipc.LocalStreams      = (*fakeStreams)(nil)
)

// errSentinel keeps an unused-import trap from snapping shut if we ever
// thin out the test list — `errors` is canonically useful in router
// tests.
var errSentinel = errors.New("sentinel")
var _ = errSentinel

// asyncErrorDispatcher returns from the handler WITHOUT terminating the stream,
// then delivers the terminal error frame from another goroutine -- exactly the
// shape WatchEvents has (it "returns after registering watchers + completing
// the synchronous replay", and live broadcasts arrive later on WatcherManager
// goroutines), and the shape a streaming denial now has.
type asyncErrorDispatcher struct {
	release chan struct{}
}

func (d *asyncErrorDispatcher) DispatchWith(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	go func() {
		<-d.release
		_ = w.SendStream(&leapmuxv1.InnerStreamMessage{
			IsError:      true,
			ErrorCode:    7,
			ErrorMessage: "boom",
			End:          true,
		})
	}()
}

// TestRouter_StreamInner_AsyncTerminalErrorIsReported is the regression for a
// lost stream error.
//
// streamCollector used to close its `done` channel inside finish() and assign
// c.err AFTERWARDS, so wait() was released BEFORE the write landed: streamLocal
// read c.err concurrently with the writer and, on the unlucky scheduling,
// reported a failed stream to the `leapmux remote` caller as success. The type's
// own comment claimed close-of-done supplied the happens-before edge; it
// supplied the opposite one.
//
// Run under -race, which catches the unsynchronized access even on an
// interleaving that happens to return the right value. Repeated because the
// window is small.
func TestRouter_StreamInner_AsyncTerminalErrorIsReported(t *testing.T) {
	for i := range 50 {
		release := make(chan struct{})
		r := &remoteipc.Router{
			WorkerID:        "A",
			UserID:          userid.MustNew("u"),
			LocalDispatcher: &asyncErrorDispatcher{release: release},
			Streams:         &fakeStreams{},
		}

		streamErr := make(chan error, 1)
		go func() {
			streamErr <- r.StreamInner(context.Background(), remoteipc.TokenInfo{}, "worker.WatchEvents", []byte("{}"), "A", "req-1",
				func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
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

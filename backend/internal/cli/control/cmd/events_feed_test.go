package cmd

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/tunnel"
)

// feedHarness runs one workerFeed against a fakeWatchTransport.
type feedHarness struct {
	t     *testing.T
	ctx   context.Context
	clock *quartz.Mock
	watch *fakeWatchTransport
	reads *agentReads
	feed  *workerFeed
	out   chan feedOutput
	// openErrs fails the next opens of the transport, one error each.
	mu       sync.Mutex
	openErrs []error
}

func startFeedHarness(t *testing.T, interest watchInterest, beforeStart func(clock *quartz.Mock)) *feedHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	var wg sync.WaitGroup
	// Registered first, so it runs after the cleanups of the traps.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	h := &feedHarness{t: t, ctx: ctx, clock: testutil.NewQuartzMock(t), watch: newFakeWatchTransport(), reads: newAgentReads(), out: make(chan feedOutput, 64)}
	if beforeStart != nil {
		beforeStart(h.clock)
	}
	h.feed = newWorkerFeed("worker-A", workerFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.openErrs) > 0 {
				err := h.openErrs[0]
				h.openErrs = h.openErrs[1:]
				return nil, nil, err
			}
			return h.watch, func() {}, nil
		},
		readAgents: h.reads.read,
		clock:      h.clock,
	}, h.out)
	h.feed.setInterest(interest)
	h.feed.start(ctx, &wg)
	return h
}

// next waits for the next output of the feed.
func (h *feedHarness) next() feedOutput {
	h.t.Helper()
	return receive(h.t, h.ctx, h.out)
}

func (h *feedHarness) requireNoOutput() {
	h.t.Helper()
	select {
	case out := <-h.out:
		h.t.Fatalf("unexpected output %+v", out)
	default:
	}
}

func (h *feedHarness) requireNoRead() {
	h.t.Helper()
	select {
	case agents := <-h.reads.started:
		h.t.Fatalf("unexpected read of %v", agents)
	default:
	}
}

// A change that arrives during the read of the agents can be newer than the
// read, so the feed sends it after the rows. Sent before them, the stale row
// would make the stream drop the newer change as a repeat.
func TestWorkerFeed_HoldsTheEventsThatArriveDuringTheRead(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}, terminals: []string{"t-1"}}, nil)
	req := receive(t, h.ctx, h.watch.opened)
	assert.Equal(t, []string{"a-1"}, watchedAgents(t, req))
	assert.Equal(t, []string{"t-1"}, watchedTerminals(t, req))

	h.watch.push(turnEndFrame("a-1"))
	assert.Equal(t, feedAgentEvent, h.next().kind, "an event before the acknowledgement passes at once")

	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started))
	h.watch.push(activityChangedFrame("a-1", idle))
	h.watch.push(bellFrame("t-1"))
	h.requireNoOutput()
	h.reads.answer(agentLevelRow("a-1", statusActive, working))

	levels := h.next()
	assert.Equal(t, feedLevels, levels.kind)
	assert.True(t, levels.first)
	assert.Equal(t, 1, levels.session)
	assert.Equal(t, working, levels.levels["a-1"].GetPublishedActivityState())
	change := h.next()
	assert.Equal(t, feedAgentEvent, change.kind)
	assert.Equal(t, idle, change.agent.GetActivityChanged().GetState())
	bell := h.next()
	assert.Equal(t, feedTerminalEvent, bell.kind)
	assert.NotNil(t, bell.terminal.GetBell(), "the notify frames of a terminal reach the feed")
}

// A watch with no agent needs no read: its acknowledgement makes it live.
func TestWorkerFeed_AWatchOfTerminalsIsLiveAtItsAcknowledgement(t *testing.T) {
	h := startFeedHarness(t, watchInterest{terminals: []string{"t-1"}}, nil)
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	levels := h.next()
	assert.Equal(t, feedLevels, levels.kind)
	assert.True(t, levels.first)
	assert.Empty(t, levels.levels)
	h.requireNoRead()
}

// A revision travels on the same stream, and its acknowledgement reads only
// the agents that the session did not read. An acknowledgement during a read
// asks for one read more, however many arrive.
func TestWorkerFeed_ReadsOnlyTheAgentsThatJoined(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}}, nil)
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started))

	h.feed.setInterest(watchInterest{agents: []string{"a-1", "a-2"}})
	assert.Equal(t, []string{"a-1", "a-2"}, watchedAgents(t, receive(t, h.ctx, h.watch.updated)))
	h.watch.ack()
	h.feed.setInterest(watchInterest{agents: []string{"a-1", "a-2", "a-3"}})
	receive(t, h.ctx, h.watch.updated)
	h.watch.ack()
	h.requireNoRead()
	h.reads.answer(agentLevelRow("a-1", statusActive, idle))
	first := h.next()
	assert.True(t, first.first)
	assert.Contains(t, first.levels, "a-1")

	assert.Equal(t, []string{"a-2", "a-3"}, receive(t, h.ctx, h.reads.started), "one read covers both revisions")
	h.reads.answer(agentLevelRow("a-2", statusActive, idle), agentLevelRow("a-3", statusActive, idle))
	second := h.next()
	assert.False(t, second.first)
	assert.Len(t, second.levels, 2)

	h.feed.setInterest(watchInterest{agents: []string{"a-2", "a-3"}})
	receive(t, h.ctx, h.watch.updated)
	h.watch.ack()
	// The sequencer reads the interest when it takes the acknowledgement. An
	// event after the acknowledgement comes out after it was taken.
	h.watch.push(turnEndFrame("a-2"))
	assert.Equal(t, feedAgentEvent, h.next().kind)
	h.requireNoRead()
	h.feed.setInterest(watchInterest{agents: []string{"a-1", "a-2", "a-3"}})
	receive(t, h.ctx, h.watch.updated)
	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started), "an agent that left and returned is read again")
}

// A session that ends during a read sends the events that the read held,
// then the end, and drops the read: it can no longer make the session live.
func TestWorkerFeed_ASessionThatEndsDuringAReadDropsTheRead(t *testing.T) {
	var reconnect *quartz.Trap
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}}, func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	})
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	receive(t, h.ctx, h.reads.started)
	h.watch.push(turnEndFrame("a-1"))
	h.watch.endStreamWithErr(connect.NewError(connect.CodeUnavailable, "worker disconnected"))

	held := h.next()
	assert.Equal(t, feedAgentEvent, held.kind, "a held event is real, so it still goes out")
	down := h.next()
	assert.Equal(t, feedDown, down.kind)
	assert.True(t, down.retrying)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(down.err), "the end reason of the stream")
	h.reads.answer(agentLevelRow("a-1", statusActive, idle))
	h.requireNoOutput()

	h.clock.Advance(testutil.WaitForTimer(t, h.ctx, reconnect)).MustWait(h.ctx)
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started))
	h.reads.answer(agentLevelRow("a-1", statusActive, working))
	levels := h.next()
	assert.True(t, levels.first)
	assert.Equal(t, 2, levels.session)
	assert.Equal(t, working, levels.levels["a-1"].GetPublishedActivityState(), "the result of the stale read never went out")
}

// Each tab that an acknowledgement rejects reaches the stream, marked as an
// agent or a terminal, before the levels that the acknowledgement asks for.
func TestWorkerFeed_ReportsTheTabsThatAnAcknowledgementRejects(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1", "a-2"}, terminals: []string{"t-1"}}, nil)
	receive(t, h.ctx, h.watch.opened)
	h.watch.push(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
		UpdateId:          h.watch.lastRequest().GetUpdateId(),
		RejectedAgents:    []*leapmuxv1.WatchRejection{{EntityId: "a-2", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND}},
		RejectedTerminals: []*leapmuxv1.WatchRejection{{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN}},
	}}})

	agent := h.next()
	assert.Equal(t, feedRejected, agent.kind)
	assert.Equal(t, "a-2", agent.rejection.GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND, agent.rejection.GetReason())
	assert.False(t, agent.rejectedTerminal)
	terminal := h.next()
	assert.Equal(t, feedRejected, terminal.kind)
	assert.Equal(t, "t-1", terminal.rejection.GetEntityId())
	assert.True(t, terminal.rejectedTerminal)

	assert.Equal(t, []string{"a-1", "a-2"}, receive(t, h.ctx, h.reads.started), "a rejection does not hold the acknowledgement back")
	h.reads.answer(agentLevelRow("a-1", statusActive, idle))
	levels := h.next()
	assert.Equal(t, feedLevels, levels.kind)
	assert.True(t, levels.first)
}

// An item that a session sends after the next session started belongs to a
// watch that ended, so the feed drops it.
func TestWorkerFeed_DropsTheLateItemsOfAnEndedSession(t *testing.T) {
	out := make(chan feedOutput, 8)
	q := newFeedSequencer(&workerFeed{workerID: "worker-A", source: workerFeedSource{clock: testutil.NewQuartzMock(t)}, out: out})
	t.Cleanup(q.stopAckTimer)
	ctx := testutil.DeadlineContext(t)
	require.True(t, q.input(ctx, feedInput{kind: inputSessionStarted, session: 2}))

	late := []feedInput{
		{kind: inputAgent, session: 1, agent: &leapmuxv1.AgentEvent{AgentId: "a-1", Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}}},
		{kind: inputTerminal, session: 1, terminal: &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}}}},
		{kind: inputDown, session: 1, err: errWatchEnded},
		{kind: inputAck, session: 1, ack: &leapmuxv1.WatchUpdateAck{}},
	}
	for _, in := range late {
		require.True(t, q.input(ctx, in))
	}
	require.Empty(t, out, "no late item of session 1 reaches the stream")
	assert.True(t, q.firstOwed, "a late end of session 1 does not end session 2")

	require.True(t, q.input(ctx, feedInput{kind: inputTerminal, session: 2, terminal: late[1].terminal}))
	assert.Equal(t, feedTerminalEvent, receive(t, ctx, out).kind, "an item of the current session passes")
}

// A refusal for good ends the feed without a reconnect.
func TestWorkerFeed_EndsWhenTheWorkerRefusesForGood(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}}, nil)
	h.mu.Lock()
	h.openErrs = []error{&codedRPCError{Code: "channel_open_failed", Cause: connect.NewError(connect.CodePermissionDenied, "no access to the worker")}}
	h.mu.Unlock()
	refused := h.next()
	assert.Equal(t, feedRefused, refused.kind)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(refused.err).Code)
	h.requireNoOutput()
}

// An offline Worker is down, and wakeUp ends the wait for the next session.
func TestWorkerFeed_WakeUpEndsTheBackoff(t *testing.T) {
	var reconnect *quartz.Trap
	h := startFeedHarness(t, watchInterest{terminals: []string{"t-1"}}, func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	})
	h.mu.Lock()
	h.openErrs = []error{&codedRPCError{Code: "channel_open_failed", Cause: errors.New("the worker is offline")}}
	h.mu.Unlock()
	down := h.next()
	assert.Equal(t, feedDown, down.kind)
	assert.True(t, down.retrying)
	assert.Equal(t, cli.CodeWorkerOffline, cli.FromError(down.err).Code)
	assert.Equal(t, activityReconnectInitial, testutil.WaitForTimer(t, h.ctx, reconnect))

	h.feed.wakeUp()
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	assert.True(t, h.next().first)
}

// A wake that arrives while a session runs is no news for the wait after
// it: that wait lasts its backoff.
func TestWorkerFeed_AWakeDuringASessionDoesNotCutTheNextWait(t *testing.T) {
	var reconnect *quartz.Trap
	h := startFeedHarness(t, watchInterest{terminals: []string{"t-1"}}, func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	})
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	h.next()
	h.feed.wakeUp()
	h.watch.endStreamWithErr(nil)
	assert.Equal(t, feedDown, h.next().kind)
	wait := testutil.WaitForTimer(t, h.ctx, reconnect)
	select {
	case <-h.watch.opened:
		t.Fatal("a wake from before the drop ended the backoff")
	default:
	}
	h.clock.Advance(wait).MustWait(h.ctx)
	receive(t, h.ctx, h.watch.opened)
}

// The interest of a feed lists each tab once, as a notify watch.
func TestWatchInterest(t *testing.T) {
	w := watchInterest{agents: []string{"a-1", "a-2"}, terminals: []string{"t-1"}}
	assert.False(t, w.empty())
	assert.True(t, watchInterest{}.empty())
	assert.True(t, w.equal(watchInterest{agents: []string{"a-1", "a-2"}, terminals: []string{"t-1"}}))
	assert.False(t, w.equal(watchInterest{agents: []string{"a-1"}, terminals: []string{"t-1"}}))
	assert.False(t, w.equal(watchInterest{agents: []string{"a-1", "a-2"}}))
	assert.Equal(t, []string{"a-1", "a-2", "t-1"}, w.tabIDs())
	req := w.request()
	assert.Equal(t, []string{"a-1", "a-2"}, watchedAgents(t, req))
	assert.Equal(t, []string{"t-1"}, watchedTerminals(t, req))
	for _, entry := range req.GetAgents() {
		assert.Zero(t, entry.GetReplayId(), "a notify watch replays nothing")
		assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_UNSPECIFIED, entry.GetReplay())
	}
	assert.Empty(t, watchInterest{}.request().GetAgents())
}

func TestRefusedForGood(t *testing.T) {
	for _, tc := range []struct {
		err  error
		good bool
	}{
		{nil, false},
		{errors.New("connection reset"), false},
		{connect.NewError(connect.CodeUnavailable, "down"), false},
		{&codedRPCError{Code: "channel_open_failed", Cause: errors.New("offline")}, false},
		{errWatchEnded, false},
		{errNoWatchAck, false},
		{connect.NewError(connect.CodePermissionDenied, "no"), true},
		{connect.NewError(connect.CodeUnauthenticated, "expired"), true},
		{connect.NewError(connect.CodeUnimplemented, "old worker"), true},
		{connect.NewError(connect.CodeNotFound, "no worker"), true},
		{&codedRPCError{Code: "channel_open_failed", Cause: connect.NewError(connect.CodePermissionDenied, "no")}, true},
	} {
		assert.Equal(t, tc.good, refusedForGood(tc.err), "%v", tc.err)
	}
	// The transports record an in-band refusal as it arrived, with its gRPC
	// code: 7 is PermissionDenied, and 14 is Unavailable.
	require.True(t, refusedForGood(&channelwire.RPCError{Code: 7, Message: "refused"}), "an in-band refusal keeps its code")
	require.False(t, refusedForGood(&channelwire.RPCError{Code: 14, Message: "shutting down"}))
}

// When noReconnect is true, a retryable error emits a non-retrying feedDown
// and stops both connect and feedSequencer cleanly without leaking goroutines.
func TestWorkerFeed_NoReconnectEmitsNonRetryingDownAndStopsSequencer(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	watch := newFakeWatchTransport()
	out := make(chan feedOutput, 8)
	feed := newWorkerFeed("worker-A", workerFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) {
			return watch, func() {}, nil
		},
		readAgents: newAgentReads().read,
		clock:      testutil.NewQuartzMock(t),
	}, out)
	feed.noReconnect = true
	feed.setInterest(watchInterest{terminals: []string{"t-1"}})

	var wg sync.WaitGroup
	feed.start(ctx, &wg)
	receive(t, ctx, watch.opened)
	watch.ack()
	require.Equal(t, feedLevels, receive(t, ctx, out).kind)

	watch.endStreamWithErr(connect.NewError(connect.CodeUnavailable, "worker restarting"))
	down := receive(t, ctx, out)
	assert.Equal(t, feedDown, down.kind)
	assert.False(t, down.retrying, "noReconnect must report retrying=false")
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(down.err))

	// Both connect and feedSequencer must exit on their own without canceling ctx.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	receive(t, ctx, done)
}

func TestClassifyWatchRejection(t *testing.T) {
	t.Parallel()
	disp, err := classifyWatchRejection("agent", "ag-1", leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED)
	assert.Equal(t, watchRejectionRetry, disp)
	assert.NoError(t, err)

	disp, err = classifyWatchRejection("terminal", "tm-1", leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND)
	assert.Equal(t, watchRejectionNotFound, disp)
	assert.NoError(t, err)

	disp, err = classifyWatchRejection("agent", "ag-1", leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN)
	assert.Equal(t, watchRejectionFailed, disp)
	require.Error(t, err)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "agent ag-1")

	disp, err = classifyWatchRejection("terminal", "tm-2", leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_UNSPECIFIED)
	assert.Equal(t, watchRejectionFailed, disp)
	require.Error(t, err)
	assert.Equal(t, cli.CodeInternal, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "terminal tm-2")
}

// testEventsChannelHub runs a minimal in-process ChannelService + /ws/channel
// relay backed by a real channel.Manager so tests can verify shared E2EE
// channel multiplexing over real tunnel.Channel instances.
type testEventsChannelHub struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler
	ctx      context.Context
	key      *noiseutil.CompositeKeypair
	keys     *leapmuxv1.WorkerPublicKeys
	manager  *channel.Manager
	opens    atomic.Int32
	mu       sync.Mutex
	relays   map[string]*websocket.Conn
	channels map[string]bool
	url      string
}

func startTestEventsChannelHub(t *testing.T, register func(*channel.Dispatcher)) (*testEventsChannelHub, *control.HubClient) {
	t.Helper()
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_CONTROL_TOKEN", "")
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	keys, err := workerkeys.Of(key)
	require.NoError(t, err)

	h := &testEventsChannelHub{
		ctx:      t.Context(),
		key:      key,
		keys:     keys,
		relays:   make(map[string]*websocket.Conn),
		channels: make(map[string]bool),
	}
	dispatcher := channel.NewDispatcher()
	dispatcher.Handle(leapmuxv1connect.WorkerSystemServicePingProcedure, channel.Registration{},
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
			_ = w.SendResponse(channelwire.NewPayloadResponse(nil))
		})
	register(dispatcher)
	h.manager = channel.NewManager("worker-A", key, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		h.sendToClient, func(msg *leapmuxv1.ConnectRequest) bool { return h.sendToClient(msg) == nil }, 0, 0)
	h.manager.SetDispatcher(dispatcher)
	t.Cleanup(h.manager.CloseAll)

	srv := connect.NewServer()
	leapmuxv1connect.RegisterChannelServiceHandler(srv, h)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(0))
	mux.HandleFunc(contracts.WSRouteChannel, h.relayFrames)
	h.url = hubtransporttest.NewServer(t, mux).URL

	require.NoError(t, control.SaveCredentials(h.url, control.CredentialFile{
		HubURL: h.url, AccessToken: "lmx_test_secret", UserID: hubChannelUser,
	}))
	client, err := control.NewClient(h.url)
	require.NoError(t, err)
	return h, client
}

func (h *testEventsChannelHub) sendToClient(msg *leapmuxv1.ConnectRequest) error {
	frame := msg.GetChannelMessageResp()
	if frame == nil {
		return nil
	}
	h.mu.Lock()
	ws := h.relays[frame.GetChannelId()]
	h.mu.Unlock()
	if ws == nil {
		return errors.New("no relay socket")
	}
	return channelwire.WriteChannelMessage(h.ctx, ws, frame)
}

func (h *testEventsChannelHub) relayFrames(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{contracts.WSSubprotocolChannelRelay}})
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(channelwire.WSReadLimit)
	var channelID string
	defer func() {
		if channelID != "" {
			h.manager.HandleClose(channelID)
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
			ok := h.channels[channelID]
			h.mu.Unlock()
			if !ok {
				return
			}
		}
		h.manager.HandleMessage(frame)
	}
}

func (h *testEventsChannelHub) GetWorkerHandshakeParams(context.Context, *leapmuxv1.GetWorkerHandshakeParamsRequest) (*leapmuxv1.GetWorkerHandshakeParamsResponse, error) {
	return &leapmuxv1.GetWorkerHandshakeParamsResponse{
		Keys:           h.keys,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}, nil
}

func (h *testEventsChannelHub) OpenChannel(_ context.Context, req *leapmuxv1.OpenChannelRequest) (*leapmuxv1.OpenChannelResponse, error) {
	channelID := "ch-" + strconv.Itoa(int(h.opens.Add(1)))
	opened := h.manager.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        channelID,
		UserId:           hubChannelUser,
		HandshakePayload: req.GetHandshakePayload(),
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ALL},
	})
	if reason := opened.GetError(); reason != "" {
		return nil, connect.NewError(connect.CodeInternal, reason)
	}
	h.mu.Lock()
	h.channels[channelID] = true
	h.mu.Unlock()
	return &leapmuxv1.OpenChannelResponse{
		ChannelId:        channelID,
		HandshakePayload: opened.GetHandshakePayload(),
		UserId:           hubChannelUser,
		MaxMessageSize:   opened.GetMaxMessageSize(),
	}, nil
}

func (h *testEventsChannelHub) CloseChannel(_ context.Context, req *leapmuxv1.CloseChannelRequest) (*leapmuxv1.CloseChannelResponse, error) {
	h.mu.Lock()
	delete(h.channels, req.GetChannelId())
	h.mu.Unlock()
	h.manager.HandleClose(req.GetChannelId())
	return &leapmuxv1.CloseChannelResponse{}, nil
}

// TestSharedWorkerChannels_MultiplexesWatchListAndPrivateAndIsolatesRefusal
// proves that WatchEvents, ListAgents, and WatchWorkerPrivateEvents share ONE
// E2EE channel per Worker in hub mode, and that a PermissionDenied refusal on
// WatchWorkerPrivateEvents leaves WatchEvents undisturbed on the same channel.
func TestSharedWorkerChannels_MultiplexesWatchListAndPrivateAndIsolatesRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()

	worker := newEventsWorker(agentLevelRow("ag-1", statusActive, idle))
	hub, client := startTestEventsChannelHub(t, func(d *channel.Dispatcher) {
		for _, proc := range []string{
			leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure,
			leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure,
			leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
		} {
			d.Handle(proc, channel.Registration{}, worker.DispatchWith)
		}
	})

	channels := newSharedWorkerChannels(client)
	feedOut := make(chan feedOutput, 16)
	privOut := make(chan workerPrivateOutput, 16)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	feed := newWorkerFeed("worker-A", workerFeedSourceWithChannels(client, "worker-A", channels), feedOut)
	feed.setInterest(watchInterest{agents: []string{"ag-1"}})
	feed.start(ctx, &wg)

	priv := newPrivateFeed("worker-A", workerPrivateSourceOf(client, "worker-A", channels), quartz.NewReal(), false, privOut)
	wg.Go(func() { priv.run(ctx) })

	watch := receive(t, ctx, worker.watches)
	receive(t, ctx, watch.seen)
	levels := receive(t, ctx, feedOut)
	require.Equal(t, feedLevels, levels.kind)
	require.True(t, levels.first)

	privStream := receive(t, ctx, worker.privateStreams)
	privStream.sendEvent(&leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
			TabRenamed: &leapmuxv1.TabRenamed{TabId: "ag-1", Title: "Shared"},
		},
	})
	privEv := receive(t, ctx, privOut)
	require.Equal(t, privateEvent, privEv.kind)
	assert.Equal(t, "Shared", privEv.event.GetTabRenamed().GetTitle())
	assert.Equal(t, int32(1), hub.opens.Load(), "WatchEvents, ListAgents, and WatchWorkerPrivateEvents must share one E2EE channel")

	// Refusing WatchWorkerPrivateEvents with PermissionDenied must NOT close the
	// shared channel or disturb WatchEvents.
	privStream.fail <- &leapmuxv1.RpcError{Code: int32(codes.PermissionDenied), Message: "private stream denied"}
	refused := receive(t, ctx, privOut)
	assert.Equal(t, privateRefused, refused.kind)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(refused.err).Code)

	watch.frames <- turnEndFrame("ag-1")
	ev := receive(t, ctx, feedOut)
	assert.Equal(t, feedAgentEvent, ev.kind)
	assert.Equal(t, int32(1), hub.opens.Load(), "WatchEvents must stay on the same single channel after private refusal")
}

func TestSharedWorkerChannels_AcquireAndRelease(t *testing.T) {
	t.Run("empty workerID returns an error", func(t *testing.T) {
		p := &sharedWorkerChannels{entries: make(map[string]*sharedWorkerChannelEntry)}
		ch, release, err := p.acquire(testutil.DeadlineContext(t), "")
		require.Error(t, err)
		assert.Nil(t, ch)
		assert.Nil(t, release)
		assert.Contains(t, err.Error(), "worker_id required")
	})

	t.Run("concurrent waiter receives leader open error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			openEntered := make(chan struct{})
			releaseOpen := make(chan struct{})
			cause := errors.New("relay refused")
			var openCalls atomic.Int32
			p := &sharedWorkerChannels{
				entries: make(map[string]*sharedWorkerChannelEntry),
				open: func(context.Context, context.Context, string) (*tunnel.Channel, error) {
					openCalls.Add(1)
					close(openEntered)
					<-releaseOpen
					return nil, cause
				},
			}

			type result struct {
				ch      *tunnel.Channel
				release func()
				err     error
			}
			leaderRes := make(chan result, 1)
			go func() {
				ch, rel, err := p.acquire(t.Context(), "worker-A")
				leaderRes <- result{ch, rel, err}
			}()
			<-openEntered

			waiterRes := make(chan result, 1)
			go func() {
				ch, rel, err := p.acquire(t.Context(), "worker-A")
				waiterRes <- result{ch, rel, err}
			}()
			synctest.Wait()

			close(releaseOpen)
			gotLeader := <-leaderRes
			gotWaiter := <-waiterRes

			assert.Equal(t, int32(1), openCalls.Load(), "waiter must share the in-flight open instead of opening again")
			for _, got := range []result{gotLeader, gotWaiter} {
				assert.Nil(t, got.ch)
				assert.Nil(t, got.release)
				var coded *codedRPCError
				require.ErrorAs(t, got.err, &coded)
				assert.Equal(t, "channel_open_failed", coded.Code)
				assert.ErrorIs(t, got.err, cause)
			}
		})
	})

	t.Run("leader context cancellation does not poison concurrent waiter", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		_, client := startTestEventsChannelHub(t, func(*channel.Dispatcher) {})
		realCh, err := client.OpenE2EEChannel(ctx, ctx, "worker-A")
		require.NoError(t, err)
		t.Cleanup(realCh.Close)

		var waiterRelease func()
		synctest.Test(t, func(t *testing.T) {
			open1Entered := make(chan struct{})
			var openCalls atomic.Int32
			p := &sharedWorkerChannels{
				entries: make(map[string]*sharedWorkerChannelEntry),
				open: func(opCtx, _ context.Context, _ string) (*tunnel.Channel, error) {
					if openCalls.Add(1) == 1 {
						close(open1Entered)
						<-opCtx.Done()
						return nil, opCtx.Err()
					}
					return realCh, nil
				},
			}

			leaderCtx, cancelLeader := context.WithCancel(t.Context())
			leaderErr := make(chan error, 1)
			go func() {
				_, _, err := p.acquire(leaderCtx, "worker-A")
				leaderErr <- err
			}()
			<-open1Entered

			type waiterOut struct {
				ch      *tunnel.Channel
				release func()
				err     error
			}
			waiterCh := make(chan waiterOut, 1)
			go func() {
				ch, rel, err := p.acquire(t.Context(), "worker-A")
				waiterCh <- waiterOut{ch, rel, err}
			}()
			synctest.Wait()

			cancelLeader()
			require.ErrorIs(t, <-leaderErr, context.Canceled)

			got := <-waiterCh
			require.NoError(t, got.err)
			assert.Same(t, realCh, got.ch)
			require.NotNil(t, got.release)
			waiterRelease = got.release
			assert.Equal(t, int32(2), openCalls.Load(), "waiter must retry p.open after leader cancels")
		})
		waiterRelease()
	})

	t.Run("canceled caller context closes newly opened channel", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		_, client := startTestEventsChannelHub(t, func(*channel.Dispatcher) {})
		callerCtx, cancelCaller := context.WithCancel(ctx)
		var openedCh *tunnel.Channel
		p := &sharedWorkerChannels{
			entries: make(map[string]*sharedWorkerChannelEntry),
			open: func(_ context.Context, lifeCtx context.Context, workerID string) (*tunnel.Channel, error) {
				ch, err := client.OpenE2EEChannel(ctx, lifeCtx, workerID)
				if err != nil {
					return nil, err
				}
				openedCh = ch
				cancelCaller()
				return ch, nil
			},
		}

		ch, release, err := p.acquire(callerCtx, "worker-A")
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, ch)
		assert.Nil(t, release)
		require.NotNil(t, openedCh)
		receive(t, ctx, openedCh.Context().Done())
		assert.Error(t, openedCh.Context().Err(), "newly opened channel must close when caller ctx is already canceled")

		p.mu.Lock()
		assert.Nil(t, p.entries["worker-A"])
		p.mu.Unlock()
	})

	t.Run("dead cached channel detaches without old release interfering with replacement", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		hub, client := startTestEventsChannelHub(t, func(*channel.Dispatcher) {})
		p := newSharedWorkerChannels(client)

		ch1, release1, err := p.acquire(ctx, "worker-A")
		require.NoError(t, err)
		require.Equal(t, int32(1), hub.opens.Load())

		ch1.Close()
		receive(t, ctx, ch1.Context().Done())

		ch2, release2, err := p.acquire(ctx, "worker-A")
		require.NoError(t, err)
		assert.NotSame(t, ch1, ch2)
		assert.NoError(t, ch2.Context().Err())
		assert.Equal(t, int32(2), hub.opens.Load())

		// Old holder's idempotent release must not disturb or remove the fresh channel.
		release1()
		release1()
		assert.NoError(t, ch2.Context().Err())

		ch2Again, release2Again, err := p.acquire(ctx, "worker-A")
		require.NoError(t, err)
		assert.Same(t, ch2, ch2Again)
		assert.Equal(t, int32(2), hub.opens.Load(), "fresh channel must remain cached after old holder releases")

		// Idempotent double release drops only one reference while the second holder remains.
		release2()
		release2()
		assert.NoError(t, ch2.Context().Err())

		release2Again()
		receive(t, ctx, ch2.Context().Done())
		assert.Error(t, ch2.Context().Err())

		p.mu.Lock()
		assert.Nil(t, p.entries["worker-A"])
		p.mu.Unlock()
	})
}

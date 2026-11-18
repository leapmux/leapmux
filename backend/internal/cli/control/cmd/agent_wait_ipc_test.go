package cmd

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// activityWorker is a fake Worker for `agent wait` and `agent send --wait`
// over the local IPC socket. It acknowledges each watch and keeps its
// stream open, so a test can push activity changes, and it answers the reads
// from its fields. mu guards every field.
//
// The handler of a watch sends every frame itself, until the stream ends.
// The IPC server closes the stream when the handler returns, and nothing
// orders a send from another goroutine before that close.
type activityWorker struct {
	mu     sync.Mutex
	agents map[string]leapmuxv1.AgentActivityState
	queue  *leapmuxv1.AgentInputQueueSnapshot
	rows   []*leapmuxv1.AgentChatMessage
	// streams holds the frames that each open watch still has to send.
	streams []chan *leapmuxv1.WatchEventsResponse
	methods []string
	// watched receives one value for each watch that the Worker acknowledged.
	watched chan struct{}
	// enqueued receives each message that reached the queue.
	enqueued chan *leapmuxv1.EnqueueAgentInputRequest
	// queueRead receives one value for each read of the queue, while a test
	// waits for one.
	queueRead chan struct{}
}

func newActivityWorker(agents map[string]leapmuxv1.AgentActivityState) *activityWorker {
	return &activityWorker{
		agents:    agents,
		queue:     &leapmuxv1.AgentInputQueueSnapshot{},
		watched:   make(chan struct{}, 8),
		enqueued:  make(chan *leapmuxv1.EnqueueAgentInputRequest, 8),
		queueRead: make(chan struct{}, 8),
	}
}

func (w *activityWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.mu.Lock()
	w.methods = append(w.methods, req.GetMethod())
	w.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure:
		var in leapmuxv1.WatchEventsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		frames := make(chan *leapmuxv1.WatchEventsResponse, 16)
		w.mu.Lock()
		w.streams = append(w.streams, frames)
		w.mu.Unlock()
		sendWatchFrame(rw, &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
			UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: in.GetUpdateId()},
		}})
		w.watched <- struct{}{}
		for {
			select {
			case frame := <-frames:
				sendWatchFrame(rw, frame)
			case <-ctx.Done():
				return
			}
		}
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var in leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		resp := &leapmuxv1.ListAgentsResponse{}
		w.mu.Lock()
		for _, id := range in.GetTabIds() {
			if state, open := w.agents[id]; open {
				// The exact state differs, as it does while a settle waits
				// out its debounce. A wait must read the published state,
				// because the changes that it receives are published states.
				resp.Agents = append(resp.Agents, &leapmuxv1.AgentInfo{
					Id: id, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, PublishedActivityState: state,
				})
			}
		}
		w.mu.Unlock()
		writeProtoResponse(rw, resp)
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		var in leapmuxv1.EnqueueAgentInputRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		w.queue = &leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{{Id: in.GetInputId(), State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}}}
		snapshot := proto.Clone(w.queue).(*leapmuxv1.AgentInputQueueSnapshot)
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: snapshot})
		w.enqueued <- &in
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
		w.mu.Lock()
		snapshot := proto.Clone(w.queue).(*leapmuxv1.AgentInputQueueSnapshot)
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.ListAgentInputQueueResponse{Snapshot: snapshot})
		select {
		case w.queueRead <- struct{}{}:
		default:
		}
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		var in leapmuxv1.ListAgentMessagesRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		writeProtoResponse(rw, w.listMessages(&in))
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

// listMessages answers ListAgentMessages from the rows, with the reply text
// only when the request asks for it, as the Worker does.
func (w *activityWorker) listMessages(in *leapmuxv1.ListAgentMessagesRequest) *leapmuxv1.ListAgentMessagesResponse {
	w.mu.Lock()
	defer w.mu.Unlock()
	var latest int64
	if n := len(w.rows); n > 0 {
		latest = w.rows[n-1].GetSeq()
	}
	resp := &leapmuxv1.ListAgentMessagesResponse{LatestSeq: &latest}
	for _, r := range w.rows {
		switch in.GetAnchor() {
		case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST:
			if r.GetSeq() != latest {
				continue
			}
		case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER:
			if r.GetSeq() <= in.GetCursorSeq() {
				continue
			}
		case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE:
			if r.GetSeq() >= in.GetCursorSeq() {
				continue
			}
		case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST,
			leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_UNSPECIFIED:
			// Every row, as one page.
		}
		out := proto.Clone(r).(*leapmuxv1.AgentChatMessage)
		if !in.GetIncludeReplyText() {
			out.ReplyText = nil
		}
		resp.Messages = append(resp.Messages, out)
	}
	return resp
}

func sendWatchFrame(rw channel.ResponseWriter, resp *leapmuxv1.WatchEventsResponse) {
	payload, err := proto.Marshal(resp)
	if err != nil {
		panic(err)
	}
	_ = rw.SendStream(&leapmuxv1.InnerStreamMessage{Payload: payload})
}

// setState changes the published state of agentID and pushes the change on
// every watch.
func (w *activityWorker) setState(agentID string, state leapmuxv1.AgentActivityState) {
	w.mu.Lock()
	w.agents[agentID] = state
	streams := slices.Clone(w.streams)
	w.mu.Unlock()
	for _, frames := range streams {
		frames <- activityChangedFrame(agentID, state)
	}
}

// deliver accepts the queued message as the Worker does, in one step: it
// leaves the queue, its row joins the transcript followed by rows, and
// activeTurn states whether its turn still runs.
func (w *activityWorker) deliver(inputID string, activeTurn bool, rows ...*leapmuxv1.AgentChatMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: activeTurn}
	next := int64(1)
	if n := len(w.rows); n > 0 {
		next = w.rows[n-1].GetSeq() + 1
	}
	w.rows = append(w.rows, userMessageRow(next, inputID))
	for _, r := range rows {
		next++
		r.Seq = next
		w.rows = append(w.rows, r)
	}
}

func (w *activityWorker) called() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.methods)
}

// startActivityWorker serves w as the Worker of the agent agentID, which the
// hub places on it.
func startActivityWorker(t *testing.T, w *activityWorker, agentID string) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
			TileId:      "root-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
	}, w)
}

func TestAgentWaitOverIPC_AnIdleAgentEndsTheWaitAtOnce(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startActivityWorker(t, w, id)

	data := runControl(t, "agent", "wait", id).Data(t)
	assert.Equal(t, []any{map[string]any{"id": id, "state": "idle", "reached": true}}, data["agents"])
}

func TestAgentWaitOverIPC_WaitsForTheChange(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: working})
	startActivityWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.watched)
		// The level read may still run: a change during it reaches the CLI
		// after the level, so the order of the two never matters here.
		w.setState(id, waiting)
	}()

	data := runControl(t, "agent", "wait", id, "--until", "waiting_for_user").Data(t)
	assert.Equal(t, []any{map[string]any{"id": id, "state": "waiting_for_user", "reached": true}}, data["agents"])
}

// `agent open --message` enqueues the first message, and the starting agent
// reports idle until it reads it. The wait goes on until that turn ends: here
// the agent then stops at a prompt, so a wait that ended at the first idle
// would report "idle" instead.
func TestAgentWaitOverIPC_WaitsForTheFirstMessageOfAStartingAgent(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{{Id: "first", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}}}
	startActivityWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.queueRead)
		w.setState(id, working)
		w.deliver("first", true, replyRow(0, "May I run the migration?"))
		w.setState(id, waiting)
	}()

	data := runControl(t, "agent", "wait", id).Data(t)
	assert.Equal(t, []any{map[string]any{"id": id, "state": "waiting_for_user", "reached": true}}, data["agents"])
}

func TestAgentWaitOverIPC_TimesOutWithExitSix(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: working})
	startActivityWorker(t, w, id)

	o := runControlText(t, "agent", "wait", id, "--timeout", "100ms")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "working", "the result prints whether the condition held or not")
	assert.Contains(t, o.Stderr, "the wait ended after 100ms")
}

func TestAgentWaitOverIPC_RefusesTheCallingAgent(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startActivityWorker(t, w, id)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", id)

	o := runControl(t, "agent", "wait", id)
	o.requireFailure(t, "usage")
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, "a refused wait watches nothing")
}

func TestAgentSendWaitOverIPC_PrintsTheReplyAfterTheAgentSettles(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	w.rows = []*leapmuxv1.AgentChatMessage{replyRow(1, "An answer to an earlier message.")}
	startActivityWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		in := receive(t, ctx, w.enqueued)
		w.setState(id, working)
		w.deliver(in.GetInputId(), false, replyRow(0, "Let me check."), replyRow(0, "All 12 tests pass."))
		w.setState(id, idle)
	}()

	data := runControl(t, "agent", "send", id, "Run the tests", "--wait").Data(t)
	assert.Equal(t, id, data["id"])
	assert.NotEmpty(t, data["input_id"])
	assert.Equal(t, "delivered", data["message"])
	assert.Equal(t, "idle", data["state"])
	assert.Equal(t, []any{"All 12 tests pass."}, data["replies"])
}

func TestAgentSendWaitOverIPC_PrintsEveryReplyWithAll(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startActivityWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		in := receive(t, ctx, w.enqueued)
		w.setState(id, working)
		w.deliver(in.GetInputId(), false, replyRow(0, "Let me check."), replyRow(0, "All 12 tests pass."))
		w.setState(id, idle)
	}()

	o := runControlText(t, "agent", "send", id, "Run the tests", "--wait", "--all")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "Let me check.\n\nAll 12 tests pass.\n", o.Stdout, "the text form prints the replies alone")
}

func TestAgentSendWaitOverIPC_RefusesAnAgentThatWaitsForTheUser(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: waiting})
	startActivityWorker(t, w, id)

	o := runControl(t, "agent", "send", id, "Run the tests", "--wait")
	f := o.requireFailure(t, "agent_waiting")
	assert.Contains(t, f.Message, id)
	assert.Equal(t, cli.ExitRefused, o.Code)
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, "the message never reached the queue")
}

func TestAgentSendWaitOverIPC_ExitsSixWhenTheAgentStopsAtAPrompt(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startActivityWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		in := receive(t, ctx, w.enqueued)
		w.setState(id, working)
		w.deliver(in.GetInputId(), true, replyRow(0, "May I delete the build directory?"))
		w.setState(id, waiting)
	}()

	o := runControl(t, "agent", "send", id, "Clean up", "--wait")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
	lines := o.lines()
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], `"state":"waiting_for_user"`)
	assert.Contains(t, lines[0], `"replies":["May I delete the build directory?"]`)
	assert.Contains(t, o.Stderr, "waits for an answer to a permission prompt")
}

func TestAgentSend_WaitFlagsNeedWait(t *testing.T) {
	clearRemoteEnv(t)
	for _, flag := range [][]string{{"--all"}, {"--timeout", "1m"}} {
		o := runControl(t, append([]string{"agent", "send", fullTestID("ag-1"), "hi", "--hub", "https://stub.invalid"}, flag...)...)
		f := o.requireFailure(t, "usage")
		assert.Contains(t, f.Message, "needs --wait")
	}
	o := runControl(t, "agent", "send", fullTestID("ag-1"), "hi", "--wait", "--timeout", "-1s", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "--timeout")
}

func TestAgentWait_RejectsAnUnknownState(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "wait", fullTestID("ag-1"), "--until", "waiting", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, `"waiting"`)
}

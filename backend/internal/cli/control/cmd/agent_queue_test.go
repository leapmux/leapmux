package cmd

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// queueWorker is a fake Worker that keeps one agent's input queue as the
// Worker's store does: an enqueue of an id that the queue holds, that the
// transcript holds, or that someone removed from the queue adds nothing, and
// each response carries the whole queue. It records every request of
// WorkerAgentInputService. mu guards every field.
type queueWorker struct {
	mu    sync.Mutex
	queue *leapmuxv1.AgentInputQueueSnapshot
	// delivered holds the ids of the inputs that left the queue for the
	// transcript.
	delivered map[string]bool
	// removed holds the ids of the inputs that a delete took out of the
	// queue. The Worker keeps the row of each one, so a repeat of its
	// request adds nothing.
	removed  map[string]bool
	requests []proto.Message
	// refuseCode and refuseMessage, when the code is set, are what every
	// mutation answers instead of applying.
	refuseCode    codes.Code
	refuseMessage string
}

func newQueueWorker(items ...*leapmuxv1.QueuedAgentInput) *queueWorker {
	return &queueWorker{queue: &leapmuxv1.AgentInputQueueSnapshot{Items: items}, delivered: map[string]bool{}, removed: map[string]bool{}}
}

func (w *queueWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var in proto.Message
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentInputQueueResponse{Snapshot: proto.CloneOf(w.queue)})
		return
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		in = &leapmuxv1.EnqueueAgentInputRequest{}
	case leapmuxv1connect.WorkerAgentInputServiceDeleteQueuedAgentInputProcedure:
		in = &leapmuxv1.DeleteQueuedAgentInputRequest{}
	case leapmuxv1connect.WorkerAgentInputServiceMoveQueuedAgentInputProcedure:
		in = &leapmuxv1.MoveQueuedAgentInputRequest{}
	case leapmuxv1connect.WorkerAgentInputServiceSetAgentInputQueuePausedProcedure:
		in = &leapmuxv1.SetAgentInputQueuePausedRequest{}
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	if err := proto.Unmarshal(req.GetPayload(), in); err != nil {
		_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	w.requests = append(w.requests, in)
	if w.refuseCode != codes.OK {
		_ = rw.SendError(int32(w.refuseCode), w.refuseMessage)
		return
	}
	switch in := in.(type) {
	case *leapmuxv1.EnqueueAgentInputRequest:
		w.enqueue(in)
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: proto.CloneOf(w.queue)})
	case *leapmuxv1.DeleteQueuedAgentInputRequest:
		held := len(w.queue.Items)
		w.queue.Items = slices.DeleteFunc(w.queue.Items, func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == in.GetInputId() })
		if len(w.queue.Items) < held {
			w.removed[in.GetInputId()] = true
		}
		writeProtoResponse(rw, &leapmuxv1.DeleteQueuedAgentInputResponse{Snapshot: proto.CloneOf(w.queue)})
	case *leapmuxv1.MoveQueuedAgentInputRequest:
		w.move(in)
		writeProtoResponse(rw, &leapmuxv1.MoveQueuedAgentInputResponse{Snapshot: proto.CloneOf(w.queue)})
	case *leapmuxv1.SetAgentInputQueuePausedRequest:
		w.queue.PauseReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
		if in.GetPaused() {
			w.queue.PauseReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
		}
		writeProtoResponse(rw, &leapmuxv1.SetAgentInputQueuePausedResponse{Snapshot: proto.CloneOf(w.queue)})
	}
}

// enqueue adds the input, unless the queue or the transcript holds its id, or
// someone removed the input with that id.
func (w *queueWorker) enqueue(in *leapmuxv1.EnqueueAgentInputRequest) {
	if w.delivered[in.GetInputId()] || w.removed[in.GetInputId()] || slices.ContainsFunc(w.queue.Items, func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == in.GetInputId() }) {
		return
	}
	item := &leapmuxv1.QueuedAgentInput{
		Id: in.GetInputId(), Kind: in.GetKind(), Text: in.GetText(), ScheduledAt: in.GetScheduledAt(),
		State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, ReorderSegment: 1,
	}
	for i, a := range in.GetAttachments() {
		item.Attachments = append(item.Attachments, &leapmuxv1.QueuedAgentInputAttachment{Filename: a.GetFilename(), MimeType: a.GetMimeType(), Size: int64(len(a.GetData())), Order: int32(i)})
	}
	w.queue.Items = append(w.queue.Items, item)
}

// move moves the input before another one, or to the end of its segment.
func (w *queueWorker) move(in *leapmuxv1.MoveQueuedAgentInputRequest) {
	from := slices.IndexFunc(w.queue.Items, func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == in.GetInputId() })
	if from < 0 || in.GetBeforeInputId() == in.GetInputId() {
		return
	}
	item := w.queue.Items[from]
	items := slices.Delete(slices.Clone(w.queue.Items), from, from+1)
	to := slices.IndexFunc(items, func(other *leapmuxv1.QueuedAgentInput) bool { return other.GetId() == in.GetBeforeInputId() })
	if in.GetBeforeInputId() == "" {
		to = 0
		for i, other := range items {
			if other.GetReorderSegment() == item.GetReorderSegment() {
				to = i + 1
			}
		}
	}
	w.queue.Items = slices.Insert(items, to, item)
}

// deliver moves the input from the queue to the transcript.
func (w *queueWorker) deliver(inputID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queue.Items = slices.DeleteFunc(w.queue.Items, func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == inputID })
	w.delivered[inputID] = true
}

func (w *queueWorker) recorded() []proto.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.requests)
}

func (w *queueWorker) itemIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ids []string
	for _, item := range w.queue.GetItems() {
		ids = append(ids, item.GetId())
	}
	return ids
}

func (w *queueWorker) refuse(code codes.Code, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refuseCode, w.refuseMessage = code, message
}

// queuedInput builds a queued user message of the segment.
func queuedInput(id, text string, segment uint32) *leapmuxv1.QueuedAgentInput {
	return &leapmuxv1.QueuedAgentInput{
		Id: id, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: text,
		State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, ReorderSegment: segment,
	}
}

// lockedInput builds an input that the agent reads now.
func lockedInput(id, text string) *leapmuxv1.QueuedAgentInput {
	item := queuedInput(id, text, 0)
	item.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING
	return item
}

// The ids of the inputs that the queue tests use. Each is a full id, and two
// of them share a prefix.
var (
	inputAlpha = fullTestID("in-alpha")
	inputBeta  = fullTestID("in-beta")
	inputBetaB = fullTestID("in-betb")
	inputGamma = fullTestID("in-gamma")
)

func TestAgentQueueListOverIPC_PrintsTheQueue(t *testing.T) {
	id := fullTestID("ag-2")
	scheduled := queuedInput(inputGamma, "Check the deploy", 2)
	scheduled.ScheduledAt = "2099-01-02T03:04:05.000Z"
	scheduled.Attachments = []*leapmuxv1.QueuedAgentInputAttachment{{Filename: "log.txt", MimeType: "text/plain", Size: 12}}
	w := newQueueWorker(lockedInput(inputAlpha, "Run the tests"), queuedInput(inputBeta, "Fix what fails\nthen commit", 1), scheduled)
	w.queue.PauseReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED
	w.queue.ActiveTurn = true
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "queue", "list", id).Data(t)
	assert.Equal(t, id, data["agent_id"])
	assert.Equal(t, true, data["paused"])
	assert.Equal(t, "interrupted", data["pause_reason"])
	assert.Equal(t, true, data["active_turn"])
	items := data["items"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, map[string]any{"id": inputAlpha, "kind": "user_message", "state": "dispatching", "text": "Run the tests", "reorder_segment": float64(0)}, items[0])
	assert.Equal(t, map[string]any{
		"id": inputGamma, "kind": "user_message", "state": "queued", "text": "Check the deploy", "reorder_segment": float64(2),
		"scheduled_at": "2099-01-02T03:04:05.000Z",
		"attachments":  []any{map[string]any{"filename": "log.txt", "mime_type": "text/plain", "size": float64(12)}},
	}, items[2])

	o := runControlText(t, "agent", "queue", "list", id)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "pause_reason: interrupted")
	assert.Contains(t, o.Stdout, "ID        KIND")
	assert.Equal(t, []string{cli.ShortID(inputBeta), "user_message", "-", "queued", "-", "Fix", "what", "fails", "then", "commit"},
		strings.Fields(queueTextLine(t, o.Stdout, cli.ShortID(inputBeta))), "a user message has no sender, so FROM prints a dash")
	assert.Contains(t, o.Stdout, "Fix what fails then commit", "a line break of the text does not break the table")
	assert.Empty(t, o.Stderr, "nothing but the pause holds the queue")

	o = runControlText(t, "agent", "queue", "list", id, "-q")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, inputAlpha+"\n"+inputBeta+"\n"+inputGamma+"\n", o.Stdout, "-q prints the id of each input")
}

// Without an agent, the list reads the queue of the agent that runs the
// command. Only the list has this default: a change of a queue states its
// agent (see TestAgentQueuePauseAndResumeOverIPC).
func TestAgentQueueListOverIPC_ListsTheQueueOfTheCallingAgent(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(queuedInput(inputAlpha, "Run the tests", 1))
	startAgentWorker(t, id, w)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", id)
	t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", "agent")

	data := runControl(t, "agent", "queue", "list").Data(t)
	assert.Equal(t, id, data["agent_id"])
	items := data["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, inputAlpha, items[0].(map[string]any)["id"])
}

func TestAgentQueueListOverIPC_PrintsAnEmptyQueue(t *testing.T) {
	id := fullTestID("ag-2")
	startAgentWorker(t, id, newQueueWorker())

	data := runControl(t, "agent", "queue", "list", id).Data(t)
	assert.Equal(t, false, data["paused"])
	assert.Equal(t, []any{}, data["items"], "an empty queue prints an empty list, not null")
	assert.NotContains(t, data, "pause_reason")
}

// The queue holds behind a first input that failed, that may not have
// reached the agent, or that LeapMux edits, though no pause holds it. The
// list says so, and a resume says so too, because a resume does not end it.
func TestAgentQueueOverIPC_StatesWhatHoldsTheQueue(t *testing.T) {
	id := fullTestID("ag-2")
	failed := queuedInput(inputAlpha, "Run the tests", 1)
	failed.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED
	failed.Error = "the provider refused the file voice.wav"
	edited := queuedInput(inputBeta, "Fix it", 1)
	edited.EditOwnerClientId = "browser-1"
	uncertain := queuedInput(inputGamma, "Deploy", 1)
	uncertain.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN
	echoing := queuedInput(inputBetaB, "Steered", 0)
	echoing.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO

	for name, tc := range map[string]struct {
		items []*leapmuxv1.QueuedAgentInput
		want  string
	}{
		"failed":           {[]*leapmuxv1.QueuedAgentInput{failed, edited}, "its first input " + inputAlpha + " failed (the provider refused the file voice.wav); remove it with \"leapmux control agent queue remove " + id + " " + inputAlpha + "\""},
		"edited":           {[]*leapmuxv1.QueuedAgentInput{edited, failed}, "LeapMux has its first input " + inputBeta + " open in an editor"},
		"uncertain":        {[]*leapmuxv1.QueuedAgentInput{uncertain}, "its first input " + inputGamma + " may not have reached the agent"},
		"after an echo":    {[]*leapmuxv1.QueuedAgentInput{echoing, failed}, "its first input " + inputAlpha + " failed"},
		"a queued head":    {[]*leapmuxv1.QueuedAgentInput{queuedInput(inputAlpha, "Run", 1), failed}, ""},
		"a dispatched one": {[]*leapmuxv1.QueuedAgentInput{lockedInput(inputAlpha, "Run")}, ""},
		"echoes alone":     {[]*leapmuxv1.QueuedAgentInput{echoing}, ""},
		"an empty queue":   {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newQueueWorker(tc.items...)
			w.queue.PauseReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
			startAgentWorker(t, id, w)
			for _, args := range [][]string{{"agent", "queue", "list", id}, {"agent", "queue", "resume", id}} {
				o := runControlText(t, args...)
				require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
				if tc.want == "" {
					assert.Empty(t, o.Stderr, args)
				} else {
					assert.Contains(t, o.Stderr, tc.want, args)
				}
			}
		})
	}

	t.Run("a pause alone", func(t *testing.T) {
		w := newQueueWorker(failed)
		startAgentWorker(t, id, w)
		o := runControlText(t, "agent", "queue", "pause", id)
		require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
		assert.Empty(t, o.Stderr, "a pause holds the queue anyway, so it states nothing more")
	})
}

func TestAgentQueueRemoveOverIPC_RemovesTheInputThatAPrefixGives(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(queuedInput(inputAlpha, "Run the tests", 1), queuedInput(inputBeta, "Fix", 1))
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "queue", "remove", id, "in-alp").Data(t)
	assert.Equal(t, map[string]any{"id": id, "input_id": inputAlpha}, data)
	requireRequests(t, w, &leapmuxv1.DeleteQueuedAgentInputRequest{AgentId: id, InputId: inputAlpha})
	assert.Equal(t, []string{inputBeta}, w.itemIDs())
}

// Over the hub, a queue command opens one channel to the Worker for all its
// calls: the read of the queue and the change.
func TestAgentQueueRemoveOverTheHub_OpensOneChannel(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(queuedInput(inputAlpha, "Run the tests", 1))
	harness := startHubChannelHarness(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:      &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: id},
			TileId:   "root-1",
			WorkerId: "worker-A", WorkspaceId: "ws-1",
		},
	}, w)

	data := runControl(t, "agent", "queue", "remove", id, "in-alp", "--hub", harness.url).Data(t)
	assert.Equal(t, map[string]any{"id": id, "input_id": inputAlpha}, data)
	assert.Empty(t, w.itemIDs())
	assert.Equal(t, 1, harness.channelOpens(), "the read and the removal share one channel")
}

func TestAgentQueueRemoveOverIPC_Refusals(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(lockedInput(inputAlpha, "Run the tests"), queuedInput(inputBeta, "Fix", 1), queuedInput(inputBetaB, "Fix again", 1))
	startAgentWorker(t, id, w)

	t.Run("an input that the agent reads", func(t *testing.T) {
		f := runControl(t, "agent", "queue", "remove", id, inputAlpha).requireFailure(t, cli.CodeConflict)
		assert.Contains(t, f.Message, "already reads input "+inputAlpha)
	})
	t.Run("an unknown input", func(t *testing.T) {
		f := runControl(t, "agent", "queue", "remove", id, "nope").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "holds no input nope")
		assert.Contains(t, f.Hints[0], "agent queue list "+id)
	})
	t.Run("an ambiguous prefix", func(t *testing.T) {
		f := runControl(t, "agent", "queue", "remove", id, "in-bet").requireFailure(t, cli.CodeAmbiguousTarget)
		require.Len(t, f.Candidates, 2)
		assert.Equal(t, inputBeta, f.Candidates[0].ID)
		assert.Equal(t, "Fix", f.Candidates[0].Title)
	})
	t.Run("an empty id", func(t *testing.T) {
		runControl(t, "agent", "queue", "remove", id, " ").requireFailure(t, cli.CodeUsage)
	})
	t.Run("no input", func(t *testing.T) {
		runControl(t, "agent", "queue", "remove", id).requireFailure(t, cli.CodeUsage)
	})
	assert.Empty(t, w.recorded(), "no refused removal reached the Worker")

	t.Run("a removal that the Worker refuses", func(t *testing.T) {
		w.refuse(codes.FailedPrecondition, "queued agent input conflicts with existing data")
		runControl(t, "agent", "queue", "remove", id, inputBeta).requireFailure(t, cli.CodeConflict)
		assert.Len(t, w.recorded(), 1)
	})
}

func TestAgentQueueMoveOverIPC_MovesInsideTheSegment(t *testing.T) {
	id := fullTestID("ag-2")
	// The scheduled input sits in a segment of its own, so --first and
	// --last mean the first and the last place of the segment of the input
	// that moves, not of the queue.
	scheduled := func() *leapmuxv1.QueuedAgentInput { return queuedInput(inputGamma, "Later", 2) }
	for name, tc := range map[string]struct {
		queue      []*leapmuxv1.QueuedAgentInput
		args       []string
		wantBefore string
		wantOrder  []string
		wantPlace  float64
	}{
		"before an input": {
			queue: []*leapmuxv1.QueuedAgentInput{scheduled(), queuedInput(inputAlpha, "Run", 1), queuedInput(inputBeta, "Fix", 1)},
			args:  []string{inputBeta, "--before", "in-alp"}, wantBefore: inputAlpha,
			wantOrder: []string{inputGamma, inputBeta, inputAlpha}, wantPlace: 2,
		},
		"first": {
			queue: []*leapmuxv1.QueuedAgentInput{scheduled(), queuedInput(inputBeta, "Fix", 1), queuedInput(inputAlpha, "Run", 1)},
			args:  []string{inputAlpha, "--first"}, wantBefore: inputBeta,
			wantOrder: []string{inputGamma, inputAlpha, inputBeta}, wantPlace: 2,
		},
		"last": {
			queue: []*leapmuxv1.QueuedAgentInput{queuedInput(inputBeta, "Fix", 1), queuedInput(inputAlpha, "Run", 1), scheduled()},
			args:  []string{inputBeta, "--last"}, wantBefore: "",
			wantOrder: []string{inputAlpha, inputBeta, inputGamma}, wantPlace: 2,
		},
		"before itself": {
			queue: []*leapmuxv1.QueuedAgentInput{queuedInput(inputAlpha, "Run", 1), queuedInput(inputBeta, "Fix", 1)},
			args:  []string{inputBeta, "--before", inputBeta}, wantBefore: inputBeta,
			wantOrder: []string{inputAlpha, inputBeta}, wantPlace: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newQueueWorker(tc.queue...)
			startAgentWorker(t, id, w)

			source := tc.args[0]
			data := runControl(t, append([]string{"agent", "queue", "move", id}, tc.args...)...).Data(t)
			assert.Equal(t, map[string]any{"id": id, "input_id": source, "position": tc.wantPlace}, data)
			requireRequests(t, w, &leapmuxv1.MoveQueuedAgentInputRequest{AgentId: id, InputId: source, BeforeInputId: tc.wantBefore})
			assert.Equal(t, tc.wantOrder, w.itemIDs())
		})
	}
}

func TestAgentQueueMoveOverIPC_Refusals(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(lockedInput(inputAlpha, "Run"), queuedInput(inputBeta, "Fix", 1), queuedInput(inputGamma, "Later", 2))
	startAgentWorker(t, id, w)

	for name, tc := range map[string]struct {
		args    []string
		code    cli.Code
		message string
	}{
		"no place":                 {[]string{inputBeta}, cli.CodeUsage, "--before, --first or --last"},
		"two places":               {[]string{inputBeta, "--first", "--last"}, cli.CodeUsage, "--first and --last exclude each other"},
		"another segment":          {[]string{inputBeta, "--before", inputGamma}, cli.CodeConflict, "another reorder segment"},
		"a locked source":          {[]string{inputAlpha, "--last"}, cli.CodeConflict, "already reads input " + inputAlpha},
		"a locked place":           {[]string{inputBeta, "--before", inputAlpha}, cli.CodeConflict, "already reads input " + inputAlpha},
		"an unknown place":         {[]string{inputBeta, "--before", "nope"}, cli.CodeNotFound, "holds no input nope"},
		"an empty place":           {[]string{inputBeta, "--before", ""}, cli.CodeUsage, "--before is empty"},
		"an unknown input to move": {[]string{"nope", "--first"}, cli.CodeNotFound, "holds no input nope"},
	} {
		t.Run(name, func(t *testing.T) {
			f := runControl(t, append([]string{"agent", "queue", "move", id}, tc.args...)...).requireFailure(t, tc.code)
			assert.Contains(t, f.Message, tc.message)
		})
	}
	assert.Empty(t, w.recorded(), "no refused move reached the Worker")

	// The queue can change between the read and the move, so the Worker can
	// refuse a move that the CLI judged valid. Its refusal is the result.
	t.Run("a move that the Worker refuses", func(t *testing.T) {
		w.refuse(codes.FailedPrecondition, "queued agent input conflicts with existing data")
		f := runControl(t, "agent", "queue", "move", id, inputBeta, "--last").requireFailure(t, cli.CodeConflict)
		assert.Contains(t, f.Message, "conflicts with existing data")
		requireRequests(t, w, &leapmuxv1.MoveQueuedAgentInputRequest{AgentId: id, InputId: inputBeta})
	})
}

func TestAgentQueuePauseAndResumeOverIPC(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(queuedInput(inputAlpha, "Run", 1))
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "queue", "pause", id).Data(t)
	assert.Equal(t, map[string]any{"id": id, "paused": true, "pause_reason": "manual"}, data)
	data = runControl(t, "agent", "queue", "resume", id).Data(t)
	assert.Equal(t, map[string]any{"id": id, "paused": false}, data)
	requireRequests(t, w,
		&leapmuxv1.SetAgentInputQueuePausedRequest{AgentId: id, Paused: true},
		&leapmuxv1.SetAgentInputQueuePausedRequest{AgentId: id, Paused: false},
	)

	for _, verb := range []string{"pause", "resume"} {
		f := runControl(t, "agent", "queue", verb).requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "missing argument <agent>", "a change of a queue names its agent")
	}
}

// requireRequests asserts that the Worker received exactly want, in order.
func requireRequests(t *testing.T, w *queueWorker, want ...proto.Message) {
	t.Helper()
	got := w.recorded()
	require.Len(t, got, len(want), "requests: %v", got)
	for i := range want {
		assert.True(t, proto.Equal(want[i], got[i]), "request %d:\nwant %v\ngot  %v", i, want[i], got[i])
	}
}

// startAgentWorker serves disp as the Worker of the agent agentID, which
// the hub places on worker-A.
func startAgentWorker(t *testing.T, agentID string, disp controlipc.LocalDispatcher) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:      &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
			TileId:   "root-1",
			WorkerId: "worker-A", WorkspaceId: "ws-1",
		},
	}, disp)
}

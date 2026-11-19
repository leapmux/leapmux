package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// retryWorker is the activityWorker with two parts of the real Worker that a
// retried `agent send --wait` needs: an enqueue that adds nothing for an
// input id that the queue or the transcript already holds, and transcript
// pages of at most the requested limit, with has_more, in each direction.
type retryWorker struct {
	*activityWorker
}

func (w *retryWorker) DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		var in leapmuxv1.EnqueueAgentInputRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		w.mu.Lock()
		held := slices.ContainsFunc(w.queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == in.GetInputId() }) ||
			slices.ContainsFunc(w.rows, func(row *leapmuxv1.AgentChatMessage) bool { return row.GetId() == in.GetInputId() })
		if !held {
			w.queue.Items = append(w.queue.Items, &leapmuxv1.QueuedAgentInput{Id: in.GetInputId(), State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED})
		}
		snapshot := proto.CloneOf(w.queue)
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: snapshot})
		if !held {
			w.enqueued <- &in
		}
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		var in leapmuxv1.ListAgentMessagesRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		resp := transcriptPage(w.rows, &in)
		w.mu.Unlock()
		writeProtoResponse(rw, resp)
	default:
		w.activityWorker.DispatchWith(ctx, caller, req, rw)
	}
}

// transcriptPage answers ListAgentMessages from rows, which hold ascending
// seqs, as the Worker pages them: at most the limit, in ascending seq, with
// has_more for more rows in the direction of the anchor.
func transcriptPage(rows []*leapmuxv1.AgentChatMessage, in *leapmuxv1.ListAgentMessagesRequest) *leapmuxv1.ListAgentMessagesResponse {
	limit := int(in.GetLimit())
	if limit <= 0 {
		limit = len(rows)
	}
	var page []*leapmuxv1.AgentChatMessage
	hasMore := false
	switch in.GetAnchor() {
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE:
		older := slices.DeleteFunc(slices.Clone(rows), func(row *leapmuxv1.AgentChatMessage) bool { return row.GetSeq() >= in.GetCursorSeq() })
		start := max(0, len(older)-limit)
		page, hasMore = older[start:], start > 0
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER:
		newer := slices.DeleteFunc(slices.Clone(rows), func(row *leapmuxv1.AgentChatMessage) bool { return row.GetSeq() <= in.GetCursorSeq() })
		end := min(len(newer), limit)
		page, hasMore = newer[:end], end < len(newer)
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST:
		end := min(len(rows), limit)
		page, hasMore = rows[:end], end < len(rows)
	default:
		start := max(0, len(rows)-limit)
		page, hasMore = rows[start:], start > 0
	}
	var latest int64
	if n := len(rows); n > 0 {
		latest = rows[n-1].GetSeq()
	}
	resp := &leapmuxv1.ListAgentMessagesResponse{HasMore: hasMore, LatestSeq: &latest}
	for _, row := range page {
		out := proto.CloneOf(row)
		if !in.GetIncludeReplyText() {
			out.ReplyText = nil
		}
		resp.Messages = append(resp.Messages, out)
	}
	return resp
}

// A retry of `agent send --wait --request-id` after the first run's message
// reached the agent queues nothing, and it prints the reply to the message
// of the first run. That message lies more than two pages back, behind the
// replies that followed it.
func TestAgentSendWaitOverIPC_ARetryPrintsTheReplyOfTheFirstRun(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})}
	w.rows = []*leapmuxv1.AgentChatMessage{replyRow(1, "An answer to an earlier message."), userMessageRow(2, inputID)}
	for seq := int64(3); seq <= 122; seq++ {
		w.rows = append(w.rows, replyRow(seq, fmt.Sprintf("Step %d.", seq)))
	}
	last := "All 12 tests pass."
	w.rows[len(w.rows)-1].ReplyText = &last
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1").Data(t)
	assert.Equal(t, inputID, data["input_id"])
	assert.Equal(t, "delivered", data["message"])
	assert.Equal(t, "idle", data["state"])
	assert.Equal(t, []any{"All 12 tests pass."}, data["replies"])
	assert.Empty(t, w.enqueued, "the retry queued no second message")
}

// A retry while the first run's message still waits in the queue waits for
// that message, as the first run did.
func TestAgentSendWaitOverIPC_ARetryWaitsForTheQueuedMessage(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: working})}
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true, Items: []*leapmuxv1.QueuedAgentInput{{Id: inputID, State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}}}
	startAgentWorker(t, id, w)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.queueRead)
		w.deliver(inputID, false, replyRow(0, "All 12 tests pass."))
		w.setState(id, idle)
	}()

	data := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1").Data(t)
	assert.Equal(t, inputID, data["input_id"])
	assert.Equal(t, []any{"All 12 tests pass."}, data["replies"])
	assert.Empty(t, w.enqueued, "the retry queued no second message")
}

// A retry after someone removed the first run's message from the queue queues
// nothing, so no reply can come. The command says so instead of waiting, and
// it ends as the first run ends when the removal happens while it waits: with
// not_found and exit status 6.
func TestAgentSendWaitOverIPC_ARetryOfARemovedMessageFails(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})}
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{}
	w.rows = []*leapmuxv1.AgentChatMessage{replyRow(1, "An answer to an earlier message.")}
	startAgentWorker(t, id, &removedInputWorker{w})

	o := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1")
	f := o.Failure(t)
	assert.Equal(t, cli.CodeNotFound, f.Code)
	assert.Contains(t, f.Message, "someone removed the message "+inputID)
	assert.Contains(t, f.Message, "this retry queued nothing")
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[0], "new --request-id")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "the wait ended without a reply, so the command exits 6")
}

// removedInputWorker answers an enqueue as the Worker answers a repeat of an
// input that someone removed from the queue: with the queue, which does not
// hold it. The Worker writes no transcript row of a removed input either.
type removedInputWorker struct {
	*retryWorker
}

func (w *removedInputWorker) DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() == leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure {
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: &leapmuxv1.AgentInputQueueSnapshot{}})
		return
	}
	w.retryWorker.DispatchWith(ctx, caller, req, rw)
}

func TestFindRowSeqBackward(t *testing.T) {
	var rows []*leapmuxv1.AgentChatMessage
	for seq := int64(1); seq <= 2*int64(contracts.MessagePageLimit)+5; seq++ {
		rows = append(rows, replyRow(seq, "r"))
	}
	pager := func(asked *[]*leapmuxv1.ListAgentMessagesRequest) func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
		return func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			*asked = append(*asked, proto.CloneOf(req))
			return transcriptPage(rows, req), nil
		}
	}

	t.Run("finds a row on the newest page with one read", func(t *testing.T) {
		var asked []*leapmuxv1.ListAgentMessagesRequest
		seq, found, err := findRowSeqBackward("reply-100", pager(&asked))
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(100), seq)
		require.Len(t, asked, 1)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, asked[0].GetAnchor())
		assert.Equal(t, int32(contracts.MessagePageLimit), asked[0].GetLimit())
	})
	t.Run("reads back page by page", func(t *testing.T) {
		var asked []*leapmuxv1.ListAgentMessagesRequest
		seq, found, err := findRowSeqBackward("reply-1", pager(&asked))
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(1), seq)
		require.Len(t, asked, 3)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE, asked[1].GetAnchor())
		assert.Equal(t, asked[1].GetCursorSeq()-int64(contracts.MessagePageLimit), asked[2].GetCursorSeq(), "each page starts before the oldest row of the last one")
	})
	t.Run("reports a missing row after the oldest page", func(t *testing.T) {
		var asked []*leapmuxv1.ListAgentMessagesRequest
		_, found, err := findRowSeqBackward("no-such-row", pager(&asked))
		require.NoError(t, err)
		assert.False(t, found)
		assert.Len(t, asked, 3, "the read ends at the page without more")
	})
	t.Run("ends at a page that does not move", func(t *testing.T) {
		calls := 0
		_, found, err := findRowSeqBackward("no-such-row", func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			calls++
			return &leapmuxv1.ListAgentMessagesResponse{Messages: []*leapmuxv1.AgentChatMessage{replyRow(7, "r")}, HasMore: true}, nil
		})
		require.NoError(t, err)
		assert.False(t, found)
		assert.Equal(t, 2, calls, "a Worker that answers the same page again cannot loop the read")
	})
	t.Run("ends at an empty page", func(t *testing.T) {
		_, found, err := findRowSeqBackward("no-such-row", func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			return &leapmuxv1.ListAgentMessagesResponse{HasMore: true}, nil
		})
		require.NoError(t, err)
		assert.False(t, found)
	})
	t.Run("returns a failed read", func(t *testing.T) {
		boom := errors.New("worker offline")
		_, _, err := findRowSeqBackward("reply-1", func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			return nil, boom
		})
		assert.ErrorIs(t, err, boom)
	})
}

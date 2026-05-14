package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote/streamevents"
)

// fakeAgentMessagesTransport is an in-memory streamevents.Transport
// for the agent messages tests. It records each WatchEventsRequest
// the Subscription fires so tests can assert cursor preservation
// across reconnects, and exposes a `pushFrame` hook that delivers
// synthetic AgentEvent frames at the registered callback.
type fakeAgentMessagesTransport struct {
	mu      sync.Mutex
	calls   []*leapmuxv1.WatchEventsRequest
	onFrame func(*leapmuxv1.WatchEventsResponse)
	cancel  context.CancelFunc
	done    chan struct{}
	opens   int32
}

func (t *fakeAgentMessagesTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (context.CancelFunc, <-chan struct{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	atomic.AddInt32(&t.opens, 1)
	t.calls = append(t.calls, req)
	t.onFrame = onFrame
	ctx, cancel := context.WithCancel(parentCtx)
	t.cancel = cancel
	t.done = make(chan struct{})
	go func() {
		<-ctx.Done()
		close(t.done)
	}()
	return cancel, t.done, nil
}

func (t *fakeAgentMessagesTransport) push(seq int64) {
	t.mu.Lock()
	cb := t.onFrame
	t.mu.Unlock()
	if cb != nil {
		cb(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
				Event: &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{
					Seq: seq,
				}},
			}},
		})
	}
}

func (t *fakeAgentMessagesTransport) lastRequest() *leapmuxv1.WatchEventsRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.calls) == 0 {
		return nil
	}
	return t.calls[len(t.calls)-1]
}

// runAgentMessagesTail spins up the same machinery `tailAgentMessages`
// uses but with our fake transport so we can drive frames + force
// reconnects. This mirrors the production helper one-to-one — the
// only thing we replace is the Transport implementation.
func runAgentMessagesTail(ctx context.Context, agentID string, startSeq int64,
	tr *fakeAgentMessagesTransport,
) (*bytes.Buffer, *streamevents.Subscription, *streamevents.AgentCursor, *sync.Mutex) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	mu := &sync.Mutex{}
	cursor := streamevents.NewAgentCursor()
	cursor.Track(agentID, startSeq)
	terms := streamevents.NewTerminalCursor()

	onAgent := func(ae *leapmuxv1.AgentEvent) {
		msg := ae.GetAgentMessage()
		if msg == nil {
			return
		}
		mu.Lock()
		_ = enc.Encode(msg)
		mu.Unlock()
	}
	sub := streamevents.NewSubscription(tr, cursor, terms, onAgent, nil, nil)
	require := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	require(sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: startSeq}},
	}))
	return buf, sub, cursor, mu
}

// TestAgentMessages_StreamingTail_EmitsMessagesInOrder confirms the
// happy path of `agent messages --follow`: each frame surfaces as a
// JSON line on stdout and the cursor advances. This pins the
// "messages stream in seq order, no gaps" guarantee from the plan's
// Phase 1a test surface.
func TestAgentMessages_StreamingTail_EmitsMessagesInOrder(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	buf, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(1)
	tr.push(2)
	tr.push(3)

	require.Eventually(t, func() bool {
		return cursor.Get("a-1") == 3
	}, time.Second, 10*time.Millisecond, "cursor must advance to seq=3")

	// Output must be three JSON lines, each carrying a seq.
	lines := splitNonEmpty(buf.String())
	require.Len(t, lines, 3)
	for i, ln := range lines {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(ln), &got))
		assert.EqualValues(t, i+1, got["seq"])
	}
}

// TestAgentMessages_ReconnectCarriesCursor exercises the resume-on-
// reconnect contract: after the cursor advances to N, a fresh
// Update (simulating reconnect after channel error) must include
// AfterSeq=N in the new WatchEventsRequest.
func TestAgentMessages_ReconnectCarriesCursor(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(5)
	require.Eventually(t, func() bool { return cursor.Get("a-1") == 5 },
		time.Second, 10*time.Millisecond)

	// Simulate reconnect: build a new request from the cursor and
	// call Update again — the resubscribe must carry seq=5.
	require.NoError(t, sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: cursor.Snapshot(map[string]struct{}{"a-1": {}}),
	}))

	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, int64(5), last.GetAgents()[0].GetAfterSeq(),
		"reconnect must carry the latest cursor forward")
}

// TestAgentMessages_StartSeqIsHonored: when `RunAgentMessages
// --follow` resumes from a non-zero `--after-seq`, the streaming
// subscription's first request must reflect that cursor. Otherwise a
// caller resuming from an earlier checkpoint would re-receive
// messages they've already processed.
func TestAgentMessages_StartSeqIsHonored(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, _, _ := runAgentMessagesTail(ctx, "a-1", 42, tr)
	t.Cleanup(sub.Cancel)

	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, int64(42), last.GetAgents()[0].GetAfterSeq())
}

// TestAgentMessages_OutOfOrderFramesDoNotRegressCursor: even if a
// frame with a lower seq somehow arrives (e.g. across a reconnect
// where the worker replayed older state), the cursor must not
// regress. Otherwise the next reconnect would request already-seen
// messages.
func TestAgentMessages_OutOfOrderFramesDoNotRegressCursor(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(10)
	require.Eventually(t, func() bool { return cursor.Get("a-1") == 10 },
		time.Second, 10*time.Millisecond)
	tr.push(3)
	// Wait a beat to make sure if the cursor was going to regress it
	// would have done so by now.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int64(10), cursor.Get("a-1"),
		"out-of-order seq=3 must not regress the cursor below 10")
}

// TestAgentMessages_CtxCancelStopsSubscription: cancelling the
// parent context (CTRL-C in production) must propagate through the
// transport so its done channel fires and `tailAgentMessages` can
// return cleanly. Without this, the CLI's exit would hang on the
// in-flight subscription.
func TestAgentMessages_CtxCancelStopsSubscription(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())

	_, sub, _, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	// Cancel parent ctx; the subscription's Done() should fire.
	cancel()

	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("ctx cancellation did not propagate to subscription done channel")
	}
}

// TestAgentMessages_OutputIsByteForByteCompatibleWithPolling pins the
// "external scripts written against the old polling output keep
// working byte-for-byte" guarantee from the plan. Each line must
// JSON-marshal directly to an AgentChatMessage shape (not wrapped in
// a `source` envelope, not a multi-line proto-pretty-print).
func TestAgentMessages_OutputIsByteForByteCompatibleWithPolling(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	buf, sub, _, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(11)
	require.Eventually(t, func() bool { return buf.Len() > 0 },
		time.Second, 10*time.Millisecond)

	// Each line must parse as an AgentChatMessage envelope with a
	// `seq` key — and NOT carry the `source` key the events command
	// adds to its lines (verifies we didn't accidentally unify the
	// two output paths).
	lines := splitNonEmpty(buf.String())
	require.NotEmpty(t, lines)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	assert.EqualValues(t, 11, got["seq"])
	_, hasSource := got["source"]
	assert.False(t, hasSource, "agent messages output must not carry the events `source` key")
}

// TestAgentMessages_TwoUpdatesShareCursor: the consumer that calls
// runAgentMessagesTail twice in succession (mimicking the reconnect
// loop in `tailAgentMessages`) must see the cursor preserved across
// both Update calls. Without this, every reconnect would reset to
// after_seq=0 and the user would see duplicate history.
func TestAgentMessages_TwoUpdatesShareCursor(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)
	tr.push(8)
	require.Eventually(t, func() bool { return cursor.Get("a-1") == 8 },
		time.Second, 10*time.Millisecond)

	// Reconnect using the cursor's current state (matches the
	// production reconnect loop).
	require.NoError(t, sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1", AfterSeq: cursor.Get("a-1")}},
	}))
	last := tr.lastRequest()
	require.NotNil(t, last)
	assert.Equal(t, int64(8), last.GetAgents()[0].GetAfterSeq(),
		"reconnect must request after_seq from the cursor, not zero")
}

func splitNonEmpty(s string) []string {
	out := []string{}
	for _, line := range bytesSplitLines([]byte(s)) {
		if len(line) > 0 {
			out = append(out, string(line))
		}
	}
	return out
}

func bytesSplitLines(b []byte) [][]byte {
	out := [][]byte{}
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

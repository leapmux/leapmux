package codewhale

import (
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestParseEnvelope(t *testing.T) {
	t.Parallel()
	raw := turnCompletedEvent(7, testTurnID, contracts.CodewhaleTurnStatusCompleted)
	env, ok := parseEnvelope(raw)
	require.True(t, ok)
	assert.Equal(t, uint64(7), env.Seq)
	assert.Equal(t, contracts.CodewhaleEventTurnCompleted, env.Event)
	assert.Equal(t, testThreadID, env.ThreadID)
	assert.Equal(t, testTurnID, env.TurnID)
	assert.Equal(t, raw, env.raw)

	for _, broken := range []string{`not json`, `{}`, `{"event":""}`, `[]`} {
		_, ok := parseEnvelope([]byte(broken))
		assert.False(t, ok, broken)
	}
}

func TestDispatchDropsARepeatAndAnotherThreadsEvent(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	message := itemEvent(5, "item.completed", "item_2", contracts.CodewhaleItemKindAgentMessage, "Hello.", nil)
	a.HandleOutput(message)
	// A reconnect replays the event it already dispatched.
	a.HandleOutput(message)
	// An older event is a replay too, whatever it holds.
	a.HandleOutput(itemEvent(4, "item.completed", "item_3", contracts.CodewhaleItemKindAgentMessage, "Older.", nil))
	// Another thread of the same runtime is not this agent's.
	other := []byte(`{"seq":9,"event":"item.completed","thread_id":"thr_other","payload":{"item":{"kind":"agent_message","detail":"Theirs."}}}`)
	a.HandleOutput(other)
	// An event with no number is dispatched: a feed that states none cannot repeat one.
	a.HandleOutput([]byte(`{"event":"item.completed","thread_id":"` + testThreadID + `","payload":{"item":{"id":"item_4","kind":"agent_message","detail":"Unnumbered."}}}`))
	// Not an event at all.
	a.HandleOutput([]byte(`not json`))

	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "Hello.")
	assert.Contains(t, string(messages[1].Content), "Unnumbered.")
	assert.Equal(t, uint64(5), a.lastSeq)
}

func TestTheStreamDeliversEventsAndResumesAfterADrop(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	retry := clock.Trap().NewTimer(streamRetryTimerTag)
	t.Cleanup(retry.Close)
	a, sink := newTestAgentWithClock(t, rt, clock)
	a.lastSeq = 3
	ctx := testutil.DeadlineContext(t)

	a.startEventStream(a.Context())
	query, err := url.ParseQuery(rt.awaitStream(t))
	require.NoError(t, err)
	assert.Equal(t, "3", query.Get(eventsQuerySinceSeq), "the stream resumes after the snapshot's sequence")
	requests := rt.requestsTo(http.MethodGet, threadPath(testThreadID, threadRouteEvents))
	require.Len(t, requests, 1)
	assert.Equal(t, "Bearer "+testToken, requests[0].Auth)

	rt.push(t, itemEvent(4, "item.completed", "item_2", contracts.CodewhaleItemKindAgentMessage, "First.", nil))
	require.Eventually(t, func() bool { return len(sink.Messages()) == 1 }, 30*time.Second, 5*time.Millisecond)

	// The runtime drops the stream. The agent waits its first backoff and
	// resumes after the last event it dispatched.
	rt.dropStreams()
	call := retry.MustWait(ctx)
	assert.Equal(t, streamRetryFirst, call.Duration)
	call.MustRelease(ctx)
	clock.Advance(streamRetryFirst).MustWait(ctx)
	query, err = url.ParseQuery(rt.awaitStream(t))
	require.NoError(t, err)
	assert.Equal(t, "4", query.Get(eventsQuerySinceSeq))

	rt.push(t, itemEvent(5, "item.completed", "item_3", contracts.CodewhaleItemKindAgentMessage, "Second.", nil))
	require.Eventually(t, func() bool { return len(sink.Messages()) == 2 }, 30*time.Second, 5*time.Millisecond)

	a.streamCancel()
	a.waitForStream()
}

func TestTheStreamBacksOffWhileTheRuntimeRefusesIt(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodGet, threadPath(testThreadID, threadRouteEvents), http.StatusServiceUnavailable, "starting")
	clock := testutil.NewQuartzMock(t)
	retry := clock.Trap().NewTimer(streamRetryTimerTag)
	t.Cleanup(retry.Close)
	a, _ := newTestAgentWithClock(t, rt, clock)
	ctx := testutil.DeadlineContext(t)

	a.startEventStream(a.Context())
	want := streamRetryFirst
	for range 8 {
		call := retry.MustWait(ctx)
		assert.Equal(t, want, call.Duration)
		call.MustRelease(ctx)
		clock.Advance(want).MustWait(ctx)
		want = min(want*2, streamRetryMax)
	}
	assert.Equal(t, streamRetryMax, want, "the backoff reaches its limit and stays there")

	// The stream reconnects, is refused again, and arms its next retry: nothing
	// stopped it yet. The trap holds that call, so the stop comes first and the
	// released wait sees it. A stop before the call would race the reconnect,
	// and a call that the trap still holds would never return.
	call := retry.MustWait(ctx)
	a.stopProcess()
	call.MustRelease(ctx)
	a.waitForStream()
}

func TestTheStreamEndsWithTheProcess(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, _ := newTestAgent(t, rt)
	a.startEventStream(a.Context())
	rt.awaitStream(t)
	a.stopProcess()
	rt.dropStreams()
	done := make(chan struct{})
	go func() {
		a.waitForStream()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the stream outlived its process")
	}
}

func TestTheStreamNeedsAThread(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	a.threadID = ""
	delivered, err := a.readEventStream(a.Context())
	assert.False(t, delivered)
	assert.ErrorContains(t, err, "no thread to follow")
}

// A connection that delivered an event resets the backoff. So when a runtime
// drops a healthy stream, the agent follows it again at once, and not after the
// delay that its earlier refusals built up.
func TestTheStreamBackoffResetsAfterAConnectionThatDelivered(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	var refuse atomic.Bool
	refuse.Store(true)
	rt.handle(http.MethodGet, threadPath(testThreadID, threadRouteEvents), func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			writeFakeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "starting", "status": 503}})
			return
		}
		rt.serveStream(w, r)
	})
	clock := testutil.NewQuartzMock(t)
	retry := clock.Trap().NewTimer(streamRetryTimerTag)
	t.Cleanup(retry.Close)
	a, sink := newTestAgentWithClock(t, rt, clock)
	ctx := testutil.DeadlineContext(t)

	a.startEventStream(a.Context())
	delays := []time.Duration{streamRetryFirst, 2 * streamRetryFirst}
	for i, want := range delays {
		call := retry.MustWait(ctx)
		assert.Equal(t, want, call.Duration, "each refusal doubles the backoff")
		call.MustRelease(ctx)
		if i == len(delays)-1 {
			refuse.Store(false)
		}
		clock.Advance(want).MustWait(ctx)
	}
	rt.awaitStream(t)
	rt.push(t, itemEvent(4, "item.completed", "item_2", contracts.CodewhaleItemKindAgentMessage, "First.", nil))
	require.Eventually(t, func() bool { return len(sink.Messages()) == 1 }, 30*time.Second, 5*time.Millisecond)

	rt.dropStreams()
	call := retry.MustWait(ctx)
	assert.Equal(t, streamRetryFirst, call.Duration, "the connection delivered, so the backoff starts over")
	a.stopProcess()
	call.MustRelease(ctx)
	a.waitForStream()
}

func TestTheStreamSkipsDataThatIsNotAnEvent(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newTestAgent(t, rt)
	a.startEventStream(a.Context())
	rt.awaitStream(t)

	rt.push(t, []byte(`not json`))
	rt.push(t, []byte(`{"seq":3}`))
	rt.push(t, itemEvent(4, "item.completed", "item_2", contracts.CodewhaleItemKindAgentMessage, "After.", nil))
	require.Eventually(t, func() bool { return len(sink.Messages()) == 1 }, 30*time.Second, 5*time.Millisecond)
	assert.Contains(t, string(sink.Messages()[0].Content), "After.")
	a.Mu.Lock()
	assert.Equal(t, uint64(4), a.lastSeq, "data that is not an event moves no sequence")
	a.Mu.Unlock()

	a.streamCancel()
	a.waitForStream()
}

// Each event the switch lists as ignored, each event of an ignored family, and
// an event of a later release persist nothing. Each one still moves the
// sequence, so a reconnect does not ask for it again.
func TestEventsTheWorkerIgnoresPersistNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	names := []string{
		eventThreadStarted, eventThreadForked, eventTurnLifecycle, eventTurnInterruptRequested, eventModelToolsSnapshot,
		"agent.spawned", "agent_mail.received", "tool_call.requested", "a_later.event",
	}
	for i, name := range names {
		a.HandleOutput(runtimeEvent(uint64(i+1), name, testTurnID, "", map[string]any{"turn": map[string]any{"id": "turn_x", "status": "in_progress"}}))
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Zero(t, sink.PublishedControlCount())
	assert.Empty(t, sink.TurnActives())
	assert.Equal(t, uint64(len(names)), a.lastSeq)
}

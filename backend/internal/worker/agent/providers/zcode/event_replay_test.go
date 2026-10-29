package zcode

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Event 12 arrives with 11 missing. The cursor stays at 10 until replay
// recovers both 11 and 12.
func TestZCodeEventReplay_GapHoldsPendingUntilReplayRecoversIt(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	// Establish a cursor at 10 through the legacy dispatch.
	a.dispatchMu.Lock()
	a.Mu.Lock()
	a.lastSeq = 10
	a.Mu.Unlock()
	a.dispatchMu.Unlock()

	// Event 12 arrives with 11 missing. The ordered coordinator holds it.
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(12, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(10), cursor, "the cursor must not advance past a gap")

	a.dispatchMu.Lock()
	pending := len(a.replay.pending)
	a.dispatchMu.Unlock()
	assert.Equal(t, 1, pending, "the gapped event must be held in pending")

	// Replay recovers 11 and 12.
	answerZCodeRequest(t, a, stdin, MethodSessionEvents, `{"eventSeq":12,"events":[
		{"eventId":"e11","sessionId":"sess-1","seq":11,"type":"session.titleUpdated","payload":{}},
		{"eventId":"e12","sessionId":"sess-1","seq":12,"type":"session.titleUpdated","payload":{}}
	]}`)
	require.NoError(t, a.replayZCodeEvents(context.Background()))

	a.Mu.Lock()
	cursor = a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(12), cursor, "replay must dispatch both the fetched and the pending event")

	a.dispatchMu.Lock()
	pending = len(a.replay.pending)
	a.dispatchMu.Unlock()
	assert.Equal(t, 0, pending, "the pending set must drain")
}

// A failed replay leaves later live events pending rather than advancing the
// cursor across the gap.
func TestZCodeEventReplay_FailedReplayLeavesLaterEventsPending(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	a.dispatchMu.Lock()
	a.Mu.Lock()
	a.lastSeq = 10
	a.Mu.Unlock()
	a.dispatchMu.Unlock()

	// Event 12 arrives with 11 missing.
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(12, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	// Replay fails.
	refuseZCodeRequest(t, a, stdin, MethodSessionEvents, -32601, "not found")
	require.Error(t, a.replayZCodeEvents(context.Background()))

	// A later event 13 joins the pending set.
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(13, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(10), cursor, "the cursor must stay at the gap")

	a.dispatchMu.Lock()
	pending := len(a.replay.pending)
	a.dispatchMu.Unlock()
	assert.Equal(t, 2, pending, "both later events must be held in pending")
}

// Sequence-zero events keep arrival-order dispatch even while a gap holds
// sequenced events in pending.
func TestZCodeEventReplay_SeqZeroDispatchesDuringAGap(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	a.dispatchMu.Lock()
	a.Mu.Lock()
	a.lastSeq = 10
	a.Mu.Unlock()
	a.dispatchMu.Unlock()

	// A gap event.
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(12, "future.event", `{}`))
	// A seq-0 event dispatches immediately.
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(0, "future.event", `{}`))

	assert.Equal(t, 1, sink.MessageCount(), "the seq-0 event must dispatch immediately")

	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(10), cursor, "the seq-0 event must not advance the cursor")

	a.dispatchMu.Lock()
	pending := len(a.replay.pending)
	a.dispatchMu.Unlock()
	assert.Equal(t, 1, pending, "the gapped event must still be pending")
}

// Concurrent replay callers are serialized by replayMu and both observe the
// recovered state.
func TestZCodeEventReplay_ConcurrentReplayCallersSerialize(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	a.dispatchMu.Lock()
	a.Mu.Lock()
	a.lastSeq = 10
	a.Mu.Unlock()
	a.dispatchMu.Unlock()

	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(12, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	// Answer two replay fetches. Each returns 11 and 12.
	for range 2 {
		answerZCodeRequest(t, a, stdin, MethodSessionEvents, `{"eventSeq":12,"events":[
			{"eventId":"e11","sessionId":"sess-1","seq":11,"type":"session.titleUpdated","payload":{}},
			{"eventId":"e12","sessionId":"sess-1","seq":12,"type":"session.titleUpdated","payload":{}}
		]}`)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			_ = a.replayZCodeEvents(context.Background())
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent replay callers deadlocked")
	}

	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(12), cursor, "both callers must observe the recovered cursor")
}

// Subscribe dispatches its response events through the coordinator and never
// assigns lastSeq = response.eventSeq. A gap between the last dispatched event
// and the response watermark triggers replay.
func TestZCodeEventReplay_SubscribeHighWatermarkDoesNotJumpCursor(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	// Answer the subscribe request. It returns events 1-3 and a high watermark
	// of 10 (events 4-10 are absent).
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{"sessionId":"sess-1","eventSeq":10,"events":[
		{"eventId":"e1","sessionId":"sess-1","seq":1,"type":"session.titleUpdated","payload":{}},
		{"eventId":"e2","sessionId":"sess-1","seq":2,"type":"session.titleUpdated","payload":{}},
		{"eventId":"e3","sessionId":"sess-1","seq":3,"type":"session.titleUpdated","payload":{}}
	]}`)
	require.NoError(t, a.subscribe(zcodeTestRPCTimeout))

	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(3), cursor, "lastSeq must advance only through dispatched events")

	a.dispatchMu.Lock()
	high := a.replay.highWatermark
	a.dispatchMu.Unlock()
	assert.Equal(t, int64(10), high, "the response watermark must be recorded")
}

// After four passes that leave a gap, the replay reports the missing range.
func TestZCodeEventReplay_ExhaustedPassesReportTheMissingRange(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.dispatchMu.Lock()
	a.replay.init()
	a.Mu.Lock()
	a.lastSeq = 10
	a.Mu.Unlock()
	a.dispatchMu.Unlock()

	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(15, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	// Each fetch returns nothing: the gap persists. Answer each sequentially.
	for i := range zcodeReplayPassLimit {
		go func(n int) {
			req := waitZCodeRequestNumber(t, stdin, MethodSessionEvents, n)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(`{"eventSeq":15,"events":[]}`)))
		}(i)
	}
	err := a.replayZCodeEvents(context.Background())
	require.Error(t, err, "exhausted passes must report failure")
	assert.Contains(t, err.Error(), "11", "the error must name the missing range")
	assert.Contains(t, err.Error(), "15", "the error must name the missing range")
}

// invalidateZCodeEventReplay discards pending events and bumps the generation,
// so an old-session event cannot enter the replacement session's dispatch.
func TestZCodeEventReplay_InvalidateDiscardsOldPendingEvents(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	a.dispatchMu.Lock()
	a.Mu.Lock()
	a.lastSeq = 10
	a.Mu.Unlock()
	a.dispatchMu.Unlock()

	// A gap event of the old session.
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(12, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	gen1 := a.replay.generation
	a.invalidateZCodeEventReplay()
	gen2 := a.replay.generation

	assert.Greater(t, gen2, gen1, "the generation must increment")

	a.dispatchMu.Lock()
	pending := len(a.replay.pending)
	a.dispatchMu.Unlock()
	assert.Equal(t, 0, pending, "old pending events must be discarded")
}

// Helpers.

// zcodeTestSequencedEvent builds a session event envelope with the given seq.
func zcodeTestSequencedEvent(seq int64, eventType, payload string) zcodeEventEnvelope {
	var raw json.RawMessage
	if payload != "" {
		raw = json.RawMessage(payload)
	}
	return zcodeEventEnvelope{
		EventID:   "evt",
		SessionID: "sess-1",
		Seq:       seq,
		Type:      eventType,
		Payload:   raw,
	}
}

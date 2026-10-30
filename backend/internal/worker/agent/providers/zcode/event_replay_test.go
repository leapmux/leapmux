package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestZCodeEventReplay_RejectsForeignSessionsBeforeOrdering(t *testing.T) {
	t.Parallel()
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct_%t", direct), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			feed := func(event zcodeEventEnvelope) {
				line, err := json.Marshal(map[string]any{"method": NotifySessionEvent, "params": event})
				require.NoError(t, err)
				if direct {
					a.HandleOutput(line)
				} else {
					a.handleOutput(providerkit.ParseLine(line))
				}
			}
			foreign := zcodeTestSequencedEvent(1, "future.event", `{"marker":"foreign"}`)
			foreign.SessionID = "retired"
			feed(foreign)
			feed(zcodeTestSequencedEvent(1, "future.event", `{"marker":"current"}`))
			require.Equal(t, 1, sink.MessageCount(), "the current session's event must reach the transcript")
			assert.Contains(t, string(sink.Messages()[0].Content), `"current"`)
		})
	}
}

func TestZCodeEventReplay_SkipsDuplicatesReceivedDuringTheFetch(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.lastSeq = 10
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(12, "future.event", `{"ordinal":12}`))
	requests := 0
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionEvents {
			return nil
		}
		requests++
		a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(10, "future.event", `{}`))
		return json.RawMessage(zcodeEventsReply(t, 12,
			zcodeFetchedEvent(t, 11, "future.event", `{"ordinal":11}`),
			zcodeFetchedEvent(t, 12, "future.event", `{"ordinal":12}`)))
	})
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	assert.Equal(t, 1, requests)
	require.Equal(t, 2, sink.MessageCount())
	assert.Contains(t, string(sink.Messages()[0].Content), `"ordinal":11`)
	assert.Contains(t, string(sink.Messages()[1].Content), `"ordinal":12`)
	assert.Empty(t, a.replay.pending)
}

func TestZCodeEventReplay_RetainsFetchedEventsAcrossAnIncompletePass(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.lastSeq = 10
	a.replay.highWatermark = 11
	requests := 0
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionEvents {
			return nil
		}
		requests++
		switch requests {
		case 1:
			return json.RawMessage(zcodeEventsReply(t, 13, zcodeFetchedEvent(t, 13, "future.event", `{"ordinal":13}`)))
		case 2:
			return json.RawMessage(zcodeEventsReply(t, 13, zcodeFetchedEvent(t, 12, "future.event", `{"ordinal":12}`)))
		default:
			return json.RawMessage(`{"events":[]}`)
		}
	})
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	assert.Equal(t, 2, requests)
	require.Equal(t, 2, sink.MessageCount())
	assert.Contains(t, string(sink.Messages()[0].Content), `"ordinal":12`)
	assert.Contains(t, string(sink.Messages()[1].Content), `"ordinal":13`)
}

func TestZCodeEventReplay_RecoversMoreThanOneNativeTailWindow(t *testing.T) {
	t.Parallel()
	for _, count := range []int{256, 257, 1000} {
		t.Run(fmt.Sprintf("events_%d", count), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.replay.highWatermark = int64(count)
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method != MethodSessionEvents {
					return nil
				}
				var params struct {
					AfterSeq int64 `json:"afterSeq"`
					Limit    int   `json:"limit"`
				}
				require.NoError(t, json.Unmarshal(request.Params, &params))
				var events []json.RawMessage
				for seq := params.AfterSeq + 1; seq <= int64(count); seq++ {
					events = append(events, zcodeFetchedEvent(t, seq, "future.event", fmt.Sprintf(`{"ordinal":%d}`, seq)))
				}
				// The native endpoint takes the newest tail after applying afterSeq.
				if params.Limit > 0 && len(events) > params.Limit {
					events = events[len(events)-params.Limit:]
				}
				response, err := json.Marshal(map[string]any{"events": events})
				require.NoError(t, err)
				return response
			})
			require.NoError(t, a.replayZCodeEvents(t.Context()))
			require.Equal(t, count, sink.MessageCount())
			for index, row := range sink.Messages() {
				var event struct {
					Payload struct {
						Ordinal int `json:"ordinal"`
					} `json:"payload"`
				}
				require.NoError(t, json.Unmarshal(row.Content, &event))
				assert.Equal(t, index+1, event.Payload.Ordinal)
			}
		})
	}
}

func TestZCodeEventReplay_SubscribePropagatesAFailedReplay(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionSubscribe:
			return json.RawMessage(`{"sessionId":"sess-1","eventSeq":3,"events":[]}`)
		case MethodSessionEvents:
			return json.RawMessage(`{}`)
		default:
			return nil
		}
	})
	err := a.subscribe(zcodeTestRPCTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no event list")
	assert.Zero(t, a.lastSeq)
}

func TestZCodeEventReplay_AnObsoleteSubscribeCannotChangeTheReplacement(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionSubscribe:
			var snapshot zcodeStateSnapshot
			snapshot.Session.SessionID = "replacement"
			a.applyCompleteSessionSnapshot(snapshot)
			return json.RawMessage(`{"sessionId":"sess-1","eventSeq":5,"events":[{"seq":1,"type":"future.event","payload":{}}]}`)
		case MethodSessionEvents:
			return json.RawMessage(`{"events":[]}`)
		default:
			return nil
		}
	})
	require.Error(t, a.subscribe(zcodeTestRPCTimeout))
	assert.Zero(t, a.lastSeq)
	assert.Zero(t, a.replay.highWatermark)
	assert.Zero(t, sink.MessageCount())
}

func TestZCodeEventReplay_SessionAdoptionRetiresAnInFlightFetch(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.replay.highWatermark = 1
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionEvents {
			return nil
		}
		var snapshot zcodeStateSnapshot
		snapshot.Session.SessionID = "replacement"
		a.applyCompleteSessionSnapshot(snapshot)
		return json.RawMessage(`{"events":[{"seq":1,"type":"future.event","payload":{"marker":"retired"}}]}`)
	})
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	assert.Zero(t, a.lastSeq)
	assert.Zero(t, sink.MessageCount())
	assert.Empty(t, a.replay.pending)
}

func TestZCodeEventReplay_CoalescesGapScheduling(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	scheduled := 0
	a.replayScheduler = func(func()) { scheduled++ }
	for seq := int64(2); seq < 102; seq++ {
		a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(seq, "future.event", `{}`))
	}
	assert.Equal(t, 1, scheduled, "one missing prefix needs one replay worker")
}

func TestZCodeEventReplay_DrainsLiveRecoveryEvenWhenTheFetchFails(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(2, "future.event", `{"ordinal":2}`))
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionEvents {
			return nil
		}
		a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(1, "future.event", `{"ordinal":1}`))
		return json.RawMessage(`{}`)
	})
	require.NoError(t, a.replayZCodeEvents(t.Context()), "live delivery already supplied the complete prefix")
	assert.Equal(t, int64(2), a.lastSeq)
	assert.Equal(t, 2, sink.MessageCount())
	assert.Empty(t, a.replay.pending)
}

func TestZCodeEventReplay_RechecksTheGapBeforeFetching(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	var worker func()
	a.replayScheduler = func(run func()) { worker = run }
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(2, "future.event", `{}`))
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(1, "future.event", `{}`))
	require.NotNil(t, worker)
	requests := 0
	zcodeRespondToRequestsForTest(t, a, func(zcodeSentRequest) json.RawMessage {
		requests++
		return json.RawMessage(`{"events":[]}`)
	})
	worker()
	assert.Zero(t, requests, "the live predecessor already closed the scheduled gap")
	assert.Nil(t, a.replay.worker)
	a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(4, "future.event", `{}`))
	assert.NotNil(t, a.replay.worker, "a later gap must receive a new worker")
}

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

	// Start after the previously delivered native history.
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

// Concurrent replay callers are serialized by the replay mutex and both observe the
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

	// The first fetch closes the gap for both callers.
	answerZCodeRequest(t, a, stdin, MethodSessionEvents, `{"eventSeq":12,"events":[
			{"eventId":"e11","sessionId":"sess-1","seq":11,"type":"session.titleUpdated","payload":{}},
			{"eventId":"e12","sessionId":"sess-1","seq":12,"type":"session.titleUpdated","payload":{}}
		]}`)

	var wg sync.WaitGroup
	errors := make(chan error, 2)
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			errors <- a.replayZCodeEvents(context.Background())
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
	for range 2 {
		require.NoError(t, <-errors)
	}
	assert.Len(t, stdin.RequestsFor(MethodSessionEvents), 1)
}

// Subscribe dispatches its response events through the coordinator and never
// assigns lastSeq = response.eventSeq. A gap between the last dispatched event
// and the response watermark triggers replay.
func TestZCodeEventReplay_SubscribeHighWatermarkDoesNotJumpCursor(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.dispatchMu.Lock()
	a.replay.init()
	a.dispatchMu.Unlock()

	// Answer the subscribe request. It returns events 1-3 and a high watermark
	// of 10 (events 4-10 are absent).
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionEvents:
			a.Mu.Lock()
			assert.Equal(t, int64(3), a.lastSeq, "the receipt alone must not advance across unavailable numbers")
			a.Mu.Unlock()
			return json.RawMessage(`{"events":[]}`)
		case MethodSessionSubscribe:
			return json.RawMessage(`{"sessionId":"sess-1","eventSeq":10,"events":[
		{"eventId":"e1","sessionId":"sess-1","seq":1,"type":"session.titleUpdated","payload":{}},
		{"eventId":"e2","sessionId":"sess-1","seq":2,"type":"session.titleUpdated","payload":{}},
		{"eventId":"e3","sessionId":"sess-1","seq":3,"type":"session.titleUpdated","payload":{}}
	]}`)
		default:
			return nil
		}
	})
	err := a.subscribe(zcodeTestRPCTimeout)
	require.NoError(t, err)

	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, int64(10), cursor, "a completed unrestricted replay confirms the retained range")

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

	// Each captured snapshot introduces a newer number beyond its request-start
	// checkpoint. The next pass must confirm that newer range.
	for i := range zcodeReplayPassLimit {
		go func(n int) {
			req := waitZCodeRequestNumber(t, stdin, MethodSessionEvents, n)
			next := int64(17 + n*2)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), zcodeEventsReply(t, next, zcodeFetchedEvent(t, next, contracts.ZCodeEventSessionTitleUpdated, `{}`))))
		}(i)
	}
	err := a.replayZCodeEvents(context.Background())
	require.Error(t, err, "exhausted passes must report failure")
	assert.Contains(t, err.Error(), "22", "the error must identify the missing range")
	assert.Contains(t, err.Error(), "23", "the error must identify the missing range")
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

func zcodeReplayCheckpointFixture(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.lastSeq = 1
	a.Mu.Unlock()
	a.dispatchMu.Lock()
	a.replay.highWatermark = 5
	a.dispatchMu.Unlock()
	return a, sink
}

func TestZCodeEventReplay_AuthoritativeFetchPermitsExpiredTransientSequences(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty %t", empty), func(t *testing.T) {
			t.Parallel()
			a, sink := zcodeReplayCheckpointFixture(t)
			requests := 0
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method != MethodSessionEvents {
					return nil
				}
				requests++
				var params map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(request.Params, &params))
				assert.NotContains(t, params, "limit")
				events := []json.RawMessage{}
				if !empty {
					events = append(events, zcodeFetchedEvent(t, 5, "future.event", `{"ordinal":5}`), zcodeFetchedEvent(t, 3, "future.event", `{"ordinal":3}`))
				}
				response, err := json.Marshal(map[string]any{"events": events})
				require.NoError(t, err)
				return response
			})
			require.NoError(t, a.replayZCodeEvents(t.Context()))
			assert.Equal(t, 1, requests)
			a.Mu.Lock()
			assert.Equal(t, int64(5), a.lastSeq)
			a.Mu.Unlock()
			if empty {
				assert.Empty(t, sink.Messages())
			} else {
				rows := sink.Messages()
				require.Len(t, rows, 2)
				assert.Contains(t, string(rows[0].Content), `"ordinal":3`)
				assert.Contains(t, string(rows[1].Content), `"ordinal":5`)
			}
		})
	}
}

func TestZCodeEventReplay_AuthorityStopsAtTheRequestStartCheckpoint(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live %t", live), func(t *testing.T) {
			t.Parallel()
			a, sink := zcodeReplayCheckpointFixture(t)
			requests := 0
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method != MethodSessionEvents {
					return nil
				}
				requests++
				if requests == 1 {
					if live {
						a.dispatchZCodeEventOrdered(zcodeTestSequencedEvent(8, "future.event", `{"ordinal":8}`))
						return json.RawMessage(`{"events":[]}`)
					}
					return zcodeEventsReply(t, 8, zcodeFetchedEvent(t, 8, "future.event", `{"ordinal":8}`))
				}
				a.Mu.Lock()
				assert.Equal(t, int64(5), a.lastSeq, "newer numbers need another completed fetch")
				a.Mu.Unlock()
				return zcodeEventsReply(t, 8, zcodeFetchedEvent(t, 7, "future.event", `{"ordinal":7}`))
			})
			require.NoError(t, a.replayZCodeEvents(t.Context()))
			assert.Equal(t, 2, requests)
			rows := sink.Messages()
			require.Len(t, rows, 2)
			assert.Contains(t, string(rows[0].Content), `"ordinal":7`)
			assert.Contains(t, string(rows[1].Content), `"ordinal":8`)
		})
	}
}

func TestZCodeEventReplay_MalformedResponseGrantsNoCheckpointAuthority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ response, reason string }{
		{`{"events":[{"sessionId":"sess-1","seq":0,"type":"future.event"}]}`, "positive event sequence"},
		{`{"events":[{"sessionId":"sess-1","seq":-1,"type":"future.event"}]}`, "positive event sequence"},
		{`{"events":[{"sessionId":"retired","seq":3,"type":"future.event"}]}`, "another session"},
		{`{"events":[{"sessionId":"sess-1","seq":3}]}`, "event type"},
		{`{}`, "no event list"},
		{`{"events":null}`, "no event list"},
	} {
		t.Run(test.reason+test.response, func(t *testing.T) {
			t.Parallel()
			a, sink := zcodeReplayCheckpointFixture(t)
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method == MethodSessionEvents {
					return json.RawMessage(test.response)
				}
				return nil
			})
			err := a.replayZCodeEvents(t.Context())
			require.ErrorContains(t, err, test.reason)
			a.Mu.Lock()
			assert.Equal(t, int64(1), a.lastSeq)
			a.Mu.Unlock()
			assert.Empty(t, sink.Messages())
			assert.Empty(t, a.replay.pending)
		})
	}
}

func TestZCodeEventReplay_SparseCheckpointCannotEnterANewLifetime(t *testing.T) {
	t.Parallel()
	for _, next := range []string{"sess-1", "replacement"} {
		t.Run(next, func(t *testing.T) {
			t.Parallel()
			a, sink := zcodeReplayCheckpointFixture(t)
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method != MethodSessionEvents {
					return nil
				}
				var snapshot zcodeStateSnapshot
				snapshot.Session.SessionID = next
				a.adoptCompleteZCodeSessionSnapshot(snapshot, true)
				return zcodeEventsReply(t, 5, zcodeFetchedEvent(t, 3, "future.event", `{"ordinal":3}`))
			})
			require.NoError(t, a.replayZCodeEvents(t.Context()))
			a.Mu.Lock()
			assert.Equal(t, int64(0), a.lastSeq)
			a.Mu.Unlock()
			assert.Empty(t, sink.Messages())
			assert.Empty(t, a.replay.pending)
		})
	}
}

func TestZCodeEventReplay_ArchiveBatchNeverDuplicatesTheLiveAccumulator(t *testing.T) {
	t.Parallel()
	for _, subscribe := range []bool{false, true} {
		t.Run(fmt.Sprintf("subscribe %t", subscribe), func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"messageId":"user"}`))
			a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"H"}`))
			a.dispatchMu.Lock()
			a.replay.highWatermark = 3
			a.dispatchMu.Unlock()
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method != MethodSessionEvents && request.Method != MethodSessionSubscribe {
					return nil
				}
				return json.RawMessage(`{"sessionId":"sess-1","eventSeq":3,"events":[{"eventId":"source-e","sessionId":"sess-1","turnId":"turn","seq":3,"type":"model.streaming","payload":{"kind":"text_delta","assistantMessageId":"message","partId":"part","delta":"e"}}]}`)
			})
			if subscribe {
				require.NoError(t, a.subscribe(zcodeTestRPCTimeout))
			} else {
				require.NoError(t, a.replayZCodeEvents(t.Context()))
			}
			a.HandleOutput(zcodeTurnEventLine(t, 4, "turn", contracts.ZCodeEventModelStreaming, `{"eventId":"source-llo","kind":"text_delta","assistantMessageId":"message","partId":"part","delta":"ello"}`))
			a.Mu.Lock()
			owner := a.turns["turn"]
			observed := owner.contentByScope[zcodeTextScope("message")].text()
			a.Mu.Unlock()
			assert.Equal(t, "H", observed, "uncertain batches must preserve the known prefix for canonical reconciliation")
		})
	}
}

func TestZCodeEventReplay_PendingLiveInputWinsAnArchiveCollision(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"messageId":"user"}`))
	a.dispatchMu.Lock()
	a.replay.highWatermark = 2
	a.dispatchMu.Unlock()
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionEvents {
			return nil
		}
		a.dispatchZCodeEventOrdered(zcodeEventEnvelope{Seq: 2, SessionID: "sess-1", TurnID: "turn", Type: contracts.ZCodeEventModelStreaming,
			Payload: json.RawMessage(`{"kind":"tool_call","assistantMessageId":"message","toolCallId":"tool","toolName":"Bash","input":{"chosen":"live"}}`)})
		return json.RawMessage(`{"events":[{"sessionId":"sess-1","turnId":"turn","seq":2,"type":"model.streaming","payload":{"kind":"tool_call","assistantMessageId":"message","toolCallId":"tool","toolName":"Bash","input":{"chosen":"archive"}}}]}`)
	})
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	a.Mu.Lock()
	invocation := a.resolveZCodeInvocationLocked(a.turns["turn"], "tool", "message", false)
	input := ""
	if invocation != nil {
		input = string(invocation.input)
	}
	a.Mu.Unlock()
	require.NotNil(t, invocation)
	assert.JSONEq(t, `{"chosen":"live"}`, input)
}

func TestZCodeEventReplay_CompleteToolInputReplacesUncertainFragments(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"messageId":"user"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"tool_input_start","assistantMessageId":"message","toolCallId":"tool","toolName":"Bash"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"tool_input_delta","assistantMessageId":"message","toolCallId":"tool","delta":"{\"x\":\"H"}`))
	a.dispatchMu.Lock()
	a.replay.highWatermark = 4
	a.dispatchMu.Unlock()
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionEvents {
			return nil
		}
		return json.RawMessage(`{"events":[{"sessionId":"sess-1","turnId":"turn","seq":4,"type":"model.streaming","payload":{"kind":"tool_input_delta","assistantMessageId":"message","toolCallId":"tool","delta":"e"}}]}`)
	})
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	a.HandleOutput(zcodeTurnEventLine(t, 5, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"tool_input_delta","assistantMessageId":"message","toolCallId":"tool","delta":"ello\"}"}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	invocation := a.resolveZCodeInvocationLocked(owner, "tool", "message", false)
	prefix := string(invocation.input)
	a.Mu.Unlock()
	assert.Equal(t, `{"x":"H`, prefix)
	a.HandleOutput(zcodeTurnEventLine(t, 6, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"tool_call","assistantMessageId":"message","toolCallId":"tool","toolName":"Bash","input":{"x":"Hello"}}`))
	a.Mu.Lock()
	input := string(invocation.input)
	a.Mu.Unlock()
	assert.JSONEq(t, `{"x":"Hello"}`, input)
}

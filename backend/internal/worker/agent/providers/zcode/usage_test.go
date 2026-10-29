package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type zcodeObservationSink struct {
	*agenttest.Sink
	onInfo func()
}

func (s *zcodeObservationSink) BroadcastSessionInfo(info map[string]any) {
	s.Sink.BroadcastSessionInfo(info)
	if callback := s.onInfo; callback != nil {
		s.onInfo = nil
		callback()
	}
}

func TestZCodeSessionObservations_CommitBeforeCallingTheSink(t *testing.T) {
	t.Parallel()
	for _, patch := range []bool{false, true} {
		t.Run(map[bool]string{false: "session read", true: "state patch"}[patch], func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &zcodeObservationSink{Sink: &agenttest.Sink{}}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"session":{"sessionId":"replacement"},"settings":{"model":{"current":{"providerId":"new","modelId":"B"}}}}`)
			sink.onInfo = func() { require.NoError(t, a.openSession("", zcodeTestRPCTimeout)) }
			body := `{"runtime":{"contextUsage":{"used":5000,"size":200000}},"model":{"current":{"providerId":"old","modelId":"A"}}}`
			if patch {
				a.handleZCodeStateUpdated(json.RawMessage(`{"sessionId":"sess-1","scope":"session","patch":` + body + `}`))
			} else {
				done := make(chan struct{})
				go func() { a.refreshZCodeUsageFromSession(); close(done) }()
				request := waitZCodeRequest(t, stdin, MethodSessionRead)
				a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"runtime":{"contextUsage":{"used":5000,"size":200000}},"settings":{"model":{"current":{"providerId":"old","modelId":"A"}}}}`)))
				<-done
			}
			assert.Equal(t, "replacement", a.sessionID)
			assert.Equal(t, "new/B", a.model)
		})
	}
}

func TestRefreshZCodeUsageFromSession_DoesNotDiscardUndeliveredEvents(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.lastSeq = 7
	done := make(chan struct{})
	go func() { a.refreshZCodeUsageFromSession(); close(done) }()
	request := waitZCodeRequest(t, stdin, MethodSessionRead)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"runtime":{"eventSeq":55,"stateRevision":99,"contextUsage":{"used":5000,"size":200000}}}`)))
	// The gap the snapshot reports is replayed before the fold. This fetch
	// cannot close it, so the fold holds back — and the undelivered events
	// stay undelivered rather than discarded.
	fetch := waitZCodeRequest(t, stdin, MethodSessionEvents)
	var params struct {
		AfterSeq int64 `json:"afterSeq"`
	}
	require.NoError(t, json.Unmarshal(fetch.Params, &params))
	assert.Equal(t, int64(7), params.AfterSeq, "the fetch asks for the gap the snapshot reports")
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, fetch), -32603, "temporary failure"))
	<-done
	a.Mu.Lock()
	assert.Zero(t, a.stateRevision, "the fold waits for the replay to close the gap")
	assert.Empty(t, a.latestContextUsage)
	assert.Equal(t, int64(7), a.lastSeq)
	a.Mu.Unlock()
	a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventTurnStarted, `{"turnId":"later"}`))
	assert.Equal(t, int64(8), a.lastSeq)
	assert.True(t, a.turnActive, "the actual notification must still reach its handler")
}

// A newer session/read gap dispatches before the observation folds: the reply
// reflects the state after the gap, so the events it outran must land first.
func TestRefreshZCodeUsageFromSession_ReplaysANewerGapBeforeApplyingTheObservation(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	sink := &zcodeObservationSink{Sink: &agenttest.Sink{}}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.lastSeq = 7
	// The fold broadcasts after its commit, so the replayed events are visible
	// to the hook only if the replay completed first.
	sink.onInfo = func() {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Equal(t, int64(9), a.lastSeq, "the gap must dispatch before the observation applies")
	}
	done := make(chan struct{})
	go func() { a.refreshZCodeUsageFromSession(); close(done) }()
	request := waitZCodeRequest(t, stdin, MethodSessionRead)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"runtime":{"eventSeq":9,"stateRevision":99,"contextUsage":{"used":5000,"size":200000}}}`)))
	fetch := waitZCodeRequest(t, stdin, MethodSessionEvents)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, fetch), zcodeEventsReply(t, 9,
		zcodeFetchedEvent(t, 8, contracts.ZCodeEventSessionTitleUpdated, ""),
		zcodeFetchedEvent(t, 9, contracts.ZCodeEventSessionTitleUpdated, ""),
	)))
	<-done
	assert.Equal(t, int64(9), a.lastSeq, "the cursor advances only through dispatched events")
	assert.Equal(t, int64(99), a.stateRevision)
	assert.Equal(t, int64(5000), a.latestContextUsage["context_tokens"])
}

func TestRefreshZCodeUsageFromSession_DropsAReadOfARetiredSession(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	done := make(chan struct{})
	go func() { a.refreshZCodeUsageFromSession(); close(done) }()
	request := waitZCodeRequest(t, stdin, MethodSessionRead)
	a.Mu.Lock()
	a.sessionID = "new"
	a.Mu.Unlock()
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"runtime":{"stateRevision":99,"eventSeq":55,"contextUsage":{"used":5000,"size":200000}},"settings":{"mode":{"current":"yolo"}}}`)))
	<-done
	assert.Zero(t, sink.SessionInfoCount())
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Zero(t, a.stateRevision)
	assert.Zero(t, a.lastSeq)
	assert.Empty(t, a.latestContextUsage)
	assert.NotEqual(t, "yolo", a.mode)
}

func TestZCodeContextUsageMap_EmptyReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, zcodeContextUsageMap(zcodeUsage{}, 200000),
		"zeros must not overwrite a real reading")
}

func TestZCodeContextUsageMap_MapsTokensAndTheOptionalWindow(t *testing.T) {
	t.Parallel()

	got := zcodeContextUsageMap(zcodeUsage{
		InputTokens:      10,
		OutputTokens:     4,
		CacheReadTokens:  2,
		CacheWriteTokens: 3,
		TotalTokens:      19,
	}, 128000)
	assert.Equal(t, map[string]any{
		"input_tokens":                int64(5),
		"output_tokens":               int64(4),
		"cache_read_input_tokens":     int64(2),
		"cache_creation_input_tokens": int64(3),
		"context_window":              int64(128000),
	}, got)

	withoutWindow := zcodeContextUsageMap(zcodeUsage{InputTokens: 1}, 0)
	_, hasWindow := withoutWindow["context_window"]
	assert.False(t, hasWindow, "a zero window is unknown, not a real size")
}

// ZCode's inputTokens is cache-inclusive, so the projection subtracts both cache
// buckets. One token of cache traffic then counts once: in its bucket, and not
// also inside the input.
func TestZCodeContextUsageMap_SubtractsTheCacheBucketsFromTheInput(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                     string
		input, cacheRead, cacheW int64
		ordinary                 int64
	}{
		{name: "no cache", input: 10, cacheRead: 0, cacheW: 0, ordinary: 10},
		{name: "partial cache hits", input: 10, cacheRead: 2, cacheW: 3, ordinary: 5},
		{name: "a full cache hit", input: 5, cacheRead: 2, cacheW: 3, ordinary: 0},
		{name: "over-clamped cache buckets", input: 3, cacheRead: 2, cacheW: 3, ordinary: 0},
		{name: "cache traffic with no input count", input: 0, cacheRead: 5, cacheW: 1, ordinary: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := zcodeContextUsageMap(zcodeUsage{
				InputTokens:      tc.input,
				CacheReadTokens:  tc.cacheRead,
				CacheWriteTokens: tc.cacheW,
			}, 0)
			assert.Equal(t, tc.ordinary, got["input_tokens"])
			assert.Equal(t, tc.cacheRead, got["cache_read_input_tokens"])
			assert.Equal(t, tc.cacheW, got["cache_creation_input_tokens"])
		})
	}
}

func TestZCodeContextUsageFromRuntime_NilOrUnusedReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, zcodeContextUsageFromRuntime(nil))
	assert.Nil(t, zcodeContextUsageFromRuntime(&zcodeContextUsage{}))
	assert.Nil(t, zcodeContextUsageFromRuntime(&zcodeContextUsage{Used: 0, Size: 200000}),
		"a zero used count is not a reading")
}

func TestZCodeContextUsageFromRuntime_PrefersHeldTokensAndCarriesCache(t *testing.T) {
	t.Parallel()

	got := zcodeContextUsageFromRuntime(&zcodeContextUsage{
		Used: 5000,
		Size: 200000,
		Cache: &struct {
			InputTokens      int64 `json:"inputTokens"`
			CacheReadTokens  int64 `json:"cacheReadTokens"`
			CacheWriteTokens int64 `json:"cacheWriteTokens"`
		}{InputTokens: 80, CacheReadTokens: 20, CacheWriteTokens: 5},
	})
	assert.Equal(t, int64(5000), got["context_tokens"],
		"context_tokens is what the context HOLDS, not the last request")
	assert.Equal(t, int64(200000), got["context_window"])
	assert.Equal(t, int64(55), got["input_tokens"],
		"the runtime breakdown states a cache-inclusive input, which the projection subtracts")
	assert.Equal(t, int64(20), got["cache_read_input_tokens"])
	assert.Equal(t, int64(5), got["cache_creation_input_tokens"])
}

// The runtime cache breakdown takes the same rule as the per-request usage.
func TestZCodeContextUsageFromRuntime_SubtractsTheCacheBucketsFromTheInput(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                     string
		input, cacheRead, cacheW int64
		ordinary                 int64
	}{
		{name: "no cache", input: 80, cacheRead: 0, cacheW: 0, ordinary: 80},
		{name: "partial cache hits", input: 80, cacheRead: 20, cacheW: 5, ordinary: 55},
		{name: "a full cache hit", input: 25, cacheRead: 20, cacheW: 5, ordinary: 0},
		{name: "over-clamped cache buckets", input: 10, cacheRead: 20, cacheW: 5, ordinary: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := zcodeContextUsageFromRuntime(&zcodeContextUsage{
				Used: 5000,
				Cache: &struct {
					InputTokens      int64 `json:"inputTokens"`
					CacheReadTokens  int64 `json:"cacheReadTokens"`
					CacheWriteTokens int64 `json:"cacheWriteTokens"`
				}{InputTokens: tc.input, CacheReadTokens: tc.cacheRead, CacheWriteTokens: tc.cacheW},
			})
			assert.Equal(t, tc.ordinary, got["input_tokens"])
			assert.Equal(t, tc.cacheRead, got["cache_read_input_tokens"])
			assert.Equal(t, tc.cacheW, got["cache_creation_input_tokens"])
			assert.Equal(t, int64(5000), got["context_tokens"])
		})
	}
}

func TestZCodeContextUsage_CostOrNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, (*zcodeContextUsage)(nil).costOrNil())
	assert.Nil(t, (&zcodeContextUsage{}).costOrNil())

	usage := &zcodeContextUsage{Cost: &zcodeCost{Amount: 1.25, Currency: "USD"}}
	cost := usage.costOrNil()
	require.NotNil(t, cost)
	assert.Equal(t, 1.25, cost.Amount)
	assert.Equal(t, zcodeUsageCurrencyUSD, cost.Currency)
}

func TestZCodeRuntimeObservation_NilIsANoop(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.applyZCodeSessionObservation("sess-1", zcodeStatePatchBody{}, nil)
	assert.Equal(t, 0, sink.SessionInfoCount())
}

// A cost in another currency must not fill total_cost_usd: that field names
// dollars, and a converted or relabelled amount would be a wrong number.
func TestZCodeRuntimeObservation_NonUSDCostIsDropped(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.applyZCodeSessionObservation("sess-1", zcodeStatePatchBody{Runtime: &zcodeRuntimeState{
		ContextUsage: &zcodeContextUsage{
			Used: 10,
			Cost: &zcodeCost{Amount: 1.2, Currency: "EUR"},
		},
	}}, nil)

	info := sink.LastSessionInfo()
	require.NotNil(t, info)
	_, hasCost := info["total_cost_usd"]
	assert.False(t, hasCost)
	a.Mu.Lock()
	known := a.sessionCostKnown
	a.Mu.Unlock()
	assert.False(t, known)
}

// A genuine zero (a free plan) is a reported cost, not an absence.
func TestZCodeRuntimeObservation_ZeroUSDIsReported(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.applyZCodeSessionObservation("sess-1", zcodeStatePatchBody{Runtime: &zcodeRuntimeState{
		ContextUsage: &zcodeContextUsage{
			Used: 10,
			Cost: &zcodeCost{Amount: 0, Currency: zcodeUsageCurrencyUSD},
		},
	}}, nil)

	info := sink.LastSessionInfo()
	require.NotNil(t, info)
	assert.Equal(t, float64(0), info["total_cost_usd"])
	a.Mu.Lock()
	known, amount := a.sessionCostKnown, a.sessionCostUsd
	a.Mu.Unlock()
	assert.True(t, known)
	assert.Equal(t, float64(0), amount)
}

func TestRecordZCodeUsage_DoesNotReplaceAuthoritativeContextTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.latestContextUsage = map[string]any{
		"context_tokens":              int64(5000),
		"input_tokens":                int64(1),
		"output_tokens":               int64(1),
		"cache_read_input_tokens":     int64(0),
		"cache_creation_input_tokens": int64(0),
	}
	a.Mu.Unlock()

	a.recordZCodeUsage(zcodeUsage{InputTokens: 80, OutputTokens: 20, CacheReadTokens: 4, CacheWriteTokens: 2})

	a.Mu.Lock()
	got := a.latestContextUsage
	a.Mu.Unlock()
	assert.Equal(t, int64(5000), got["context_tokens"],
		"the per-request counts must not make the fill gauge jump backwards after a compaction")
	assert.Equal(t, int64(74), got["input_tokens"])
	assert.Equal(t, int64(20), got["output_tokens"])
	assert.Equal(t, int64(4), got["cache_read_input_tokens"])
	assert.Equal(t, int64(2), got["cache_creation_input_tokens"])
}

func TestRecordZCodeUsage_FillsWhenNoAuthoritativeReadingExists(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.recordZCodeUsage(zcodeUsage{InputTokens: 80, OutputTokens: 20})

	info := sink.LastSessionInfo()
	require.NotNil(t, info)
	usage, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(80), usage["input_tokens"])
	_, hasHeld := usage["context_tokens"]
	assert.False(t, hasHeld, "per-request counts do not know what the context holds")
}

func TestRecordZCodeUsage_EmptyIsANoop(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.recordZCodeUsage(zcodeUsage{})
	assert.Equal(t, 0, sink.SessionInfoCount())
}

func TestZCodeAugmentWithUsage_EmptySnapshotReturnsRaw(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"turn.completed"}`)
	assert.Equal(t, raw, zcodeTurnContent(raw, zcodeUsageSnapshot{}).Original)
}

func TestZCodeTurnUsagePreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	raw := []byte(" {\"type\":\"turn.completed\",\"future\":9007199254740993,\"payload\":{\"response\":\"Done\",\"usage\":{\"inputTokens\":2,\"outputTokens\":1,\"totalTokens\":3}}} \n")
	event, ok := parseZCodeEvent(raw)
	require.True(t, ok)
	a.dispatchZCodeEvent(event)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, raw, messages[0].Content)
	assert.NotEmpty(t, messages[0].Metadata)
	assert.True(t, messages[0].TurnEnd)
}

func TestZCodeAugmentWithUsage_InjectsCostAndContext(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"turn.completed","payload":{}}`)
	out := zcodeTurnContent(raw, zcodeUsageSnapshot{
		ContextUsage: map[string]any{"context_tokens": int64(900)},
		CostUSD:      0.42,
		HasCost:      true,
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Metadata, &got))
	assert.Equal(t, raw, out.Original)
	assert.InDelta(t, 0.42, got["total_cost_usd"], 1e-9)
	usage, ok := got["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(900), usage["context_tokens"])
}

func TestZCodeAugmentWithUsage_MalformedJSONReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":`)
	assert.Equal(t, raw, zcodeTurnContent(raw, zcodeUsageSnapshot{
		HasCost: true, CostUSD: 1,
	}).Original)
}

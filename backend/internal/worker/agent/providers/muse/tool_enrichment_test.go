package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func completedNativeTool(t *testing.T, a *Agent) []byte {
	t.Helper()
	item := resultItem("item", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	return feed(t, a, contracts.MuseMethodItemCompleted, item)
}

func requireNativeResultEvidence(t *testing.T, stored *agent.StoredMessage, original []byte) {
	t.Helper()
	require.NotNil(t, stored)
	assert.Equal(t, original, stored.Content.Original)
	var supplemental struct {
		Records []json.RawMessage `json:"nativeRecords"`
	}
	require.NoError(t, json.Unmarshal(stored.Content.Supplemental, &supplemental))
	require.Len(t, supplemental.Records, 2)
	assert.Contains(t, string(supplemental.Records[1]), `exit_code`)
}

func TestMuseToolEnrichmentWaitsForItsRetainedNativeRow(t *testing.T) {
	a, base := newSubscribedNativeResultAgent(t)
	var queue agent.OutputQueue
	a.sink = agent.NewOrderedProviderServices(&queue, a.sink)
	a.sessions["session"].sink = a.sink
	base.PersistErr = assert.AnError
	original := completedNativeTool(t, a)
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	require.Nil(t, stored)
	require.True(t, queue.HasPending())
	base.PersistErr = nil
	require.NoError(t, queue.Flush())
	stored, err = a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	assert.False(t, queue.HasPending())
}

type museEnrichmentFailureSink struct {
	agent.ProviderServices
	readFailure       error
	reads             int
	enrichmentFailure error
	enrichments       int
	onEnrich          func(agent.MessageEnrichment) (bool, error)
}

func (s *museEnrichmentFailureSink) ReadToolResultForSession(spanID, sessionID string) (*agent.StoredMessage, error) {
	s.reads++
	if s.readFailure != nil {
		return nil, s.readFailure
	}
	return s.ProviderServices.ReadToolResultForSession(spanID, sessionID)
}
func (s *museEnrichmentFailureSink) ReadToolResultBySeq(sequence int64) (*agent.StoredMessage, error) {
	s.reads++
	if s.readFailure != nil {
		return nil, s.readFailure
	}
	return s.ProviderServices.ReadToolResultBySeq(sequence)
}

func (s *museEnrichmentFailureSink) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	s.enrichments++
	if s.onEnrich != nil {
		return s.onEnrich(change)
	}
	if s.enrichmentFailure != nil {
		return false, s.enrichmentFailure
	}
	return s.ProviderServices.EnrichMessage(change)
}

func TestMuseToolEnrichmentRetriesReadFailureWithoutAnotherNativeFrame(t *testing.T) {
	a, base := newSubscribedNativeResultAgent(t)
	failing := &museEnrichmentFailureSink{ProviderServices: a.sink, readFailure: assert.AnError}
	var queue agent.OutputQueue
	clock := testutil.NewQuartzMock(t)
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	a.sink = agent.NewOrderedProviderServices(&queue, failing)
	a.sessions["session"].sink = a.sink
	original := completedNativeTool(t, a)
	require.Equal(t, 1, failing.reads)
	require.True(t, queue.HasPending())
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, created)
	failing.readFailure = nil
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	queue.StopRetries()
	stored, err := base.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	assert.Equal(t, 1, failing.enrichments)
	assert.False(t, queue.HasPending())
}

func TestMuseToolEnrichmentPreservesUnknownSupplementNumbers(t *testing.T) {
	a, _ := newSubscribedNativeResultAgent(t)
	original := completedNativeTool(t, a)
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	require.NotNil(t, stored)
	_, err = a.sink.EnrichMessage(agent.MessageEnrichment{Seq: stored.Seq, SpanID: "item", OriginalContent: original, PreviousRevision: stored.Revision, SupplementalContent: []byte(`{"nativeRecords":[],"future":{"large":9007199254740993,"nested":[9223372036854775807]}}`)})
	require.NoError(t, err)
	a.enrichLogItems(a.sessions["session"])
	stored, err = a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	var supplemental map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored.Content.Supplemental, &supplemental))
	assert.JSONEq(t, `{"large":9007199254740993,"nested":[9223372036854775807]}`, string(supplemental["future"]))
	assert.Contains(t, string(supplemental["future"]), "9007199254740993")
	assert.Contains(t, string(supplemental["future"]), "9223372036854775807")
}

func TestMuseToolEnrichmentKeepsItsOriginalNativeSession(t *testing.T) {
	a, base := newSubscribedNativeResultAgent(t)
	original := completedNativeTool(t, a)
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	require.NotNil(t, stored)
	_, err = a.sink.EnrichMessage(agent.MessageEnrichment{Seq: stored.Seq, SpanID: "item", OriginalContent: original, PreviousRevision: stored.Revision, SupplementalContent: []byte(`{}`)})
	require.NoError(t, err)
	base.UpdateSessionID("replacement")
	a.enrichLogItems(a.sessions["session"])
	stored, err = a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	replacement, err := a.sink.ReadToolResultForSession("item", "replacement")
	require.NoError(t, err)
	assert.Nil(t, replacement)
}

func TestMuseToolEnrichmentAcceptsOnlyNativeFramingDifferencesOnReplay(t *testing.T) {
	a, _ := newSubscribedNativeResultAgent(t)
	original := completedNativeTool(t, a)
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	require.NotNil(t, stored)
	_, err = a.sink.EnrichMessage(agent.MessageEnrichment{Seq: stored.Seq, SpanID: "item", OriginalContent: original, PreviousRevision: stored.Revision, SupplementalContent: []byte(`{}`)})
	require.NoError(t, err)
	page := rewriteNativeInputJSON(t, original, nil, "jsonrpc")
	a.stateMu.Lock()
	state := a.sessions["session"]
	native := state.items["item"].nativeObject()
	state.items["item"].raw = nil
	state.items["item"].nativeItemRaw = native
	state.items["item"].retainedRevision = 0
	a.stateMu.Unlock()
	a.HandleOutput(page)
	stored, err = a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
}

type refusedMuseToolSink struct {
	agent.ProviderServices
	refuse bool
}

func (s *refusedMuseToolSink) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(s.ProviderServices)
}
func (s *refusedMuseToolSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.refuse && span.SpanID == "item" {
		return assert.AnError
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func TestMuseToolEnrichmentFollowsAFinalizationAcceptedRow(t *testing.T) {
	a, _ := newSubscribedNativeResultAgent(t)
	failing := &refusedMuseToolSink{ProviderServices: a.sink, refuse: true}
	a.sink = failing
	a.sessions["session"].sink = a.sink
	a.startTurn("session", "turn")
	original := completedNativeTool(t, a)
	queue := agent.OutputQueueFor(a.sink)
	require.False(t, queue.HasPending(), "a rejected row cannot reserve enrichment before its later retry")
	failing.refuse = false
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "completed"})
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	assert.False(t, queue.HasPending())
}

func TestMuseToolEnrichmentRetriesACompetingSupplementRevision(t *testing.T) {
	a, _ := newSubscribedNativeResultAgent(t)
	wrapped := &museEnrichmentFailureSink{ProviderServices: a.sink}
	var queue agent.OutputQueue
	a.sink = agent.NewOrderedProviderServices(&queue, wrapped)
	a.sessions["session"].sink = a.sink
	wrapped.onEnrich = func(change agent.MessageEnrichment) (bool, error) {
		wrapped.onEnrich = nil
		concurrent := change
		concurrent.WriteReceipt = nil
		concurrent.SupplementalContent = []byte(`{"external":{"large":9007199254740993}}`)
		_, err := wrapped.ProviderServices.EnrichMessage(concurrent)
		require.NoError(t, err)
		return false, nil
	}
	original := completedNativeTool(t, a)
	require.True(t, queue.HasPending())
	require.NoError(t, queue.Flush())
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	assert.Contains(t, string(stored.Content.Supplemental), `"large":9007199254740993`)
	assert.Equal(t, int64(2), stored.Revision)
	assert.Equal(t, 2, wrapped.enrichments)
}

func TestMuseToolEnrichmentRecognizesAnInnerCommitAfterOuterRefusal(t *testing.T) {
	a, _ := newSubscribedNativeResultAgent(t)
	wrapped := &museEnrichmentFailureSink{ProviderServices: a.sink}
	var queue agent.OutputQueue
	a.sink = agent.NewOrderedProviderServices(&queue, wrapped)
	a.sessions["session"].sink = a.sink
	wrapped.onEnrich = func(change agent.MessageEnrichment) (bool, error) {
		_, err := wrapped.ProviderServices.EnrichMessage(change)
		require.NoError(t, err)
		return false, assert.AnError
	}
	original := completedNativeTool(t, a)
	require.NoError(t, queue.Flush())
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	assert.Equal(t, int64(1), stored.Revision)
	assert.Equal(t, 1, wrapped.enrichments)
	assert.False(t, queue.HasPending())
}

func TestMuseToolEnrichmentObserverCanEnterTheDispatcher(t *testing.T) {
	a, base := newSubscribedNativeResultAgent(t)
	wrapped := &museEnrichmentFailureSink{ProviderServices: a.sink}
	var queue agent.OutputQueue
	a.sink = agent.NewOrderedProviderServices(&queue, wrapped)
	a.sessions["session"].sink = a.sink
	entered := false
	wrapped.onEnrich = func(change agent.MessageEnrichment) (bool, error) {
		if !a.dispatcher.mu.TryLock() {
			t.Error("the enrichment observer retains the dispatcher")
			return false, assert.AnError
		}
		a.dispatcher.mu.Unlock()
		if !a.dispatcher.finalizationMu.TryLock() {
			t.Error("the enrichment observer retains finalization")
			return false, assert.AnError
		}
		a.dispatcher.finalizationMu.Unlock()
		feed(t, a, "future/output", map[string]any{"sessionId": "session", "value": "observer"})
		entered = true
		return wrapped.ProviderServices.EnrichMessage(change)
	}
	original := completedNativeTool(t, a)
	require.True(t, entered)
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
	require.Len(t, base.Messages(), 2)
	assert.Contains(t, string(base.Messages()[1].Content), `"value":"observer"`)
	assert.False(t, queue.HasPending())
}

func TestMuseProofPrefixReadsAlsoEnrichAcceptedToolRows(t *testing.T) {
	a, base := testAgent(t)
	base.UpdateSessionID("session")
	state := a.sessions["session"]
	state.log.subscriptionID = 1
	original := completedNativeTool(t, a)
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodLogPage, method)
		raw, err := json.Marshal(map[string]any{"records": []json.RawMessage{origin, result}, "nextCursor": map[string]any{"stream": nativeStream{Kind: "session", ID: "session"}, "after": nativePosition{ID: "result", Sequence: 2}}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: raw}
	}})
	require.NoError(t, a.ensureNativePrefix(testutil.DeadlineContext(t), "session", state, nativePosition{ID: "result", Sequence: 2}))
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	requireNativeResultEvidence(t, stored, original)
}

func TestMuseMalformedLogGapAdmitsEnrichmentWithoutRelockingDispatch(t *testing.T) {
	a, base := testAgent(t)
	base.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	original := completedNativeTool(t, a)
	feed(t, a, methodLogGap, map[string]any{"subscriptionId": 1, "after": map[string]any{}, "next": map[string]any{}})
	stored, err := a.sink.ReadToolResultForSession("item", "session")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, original, stored.Content.Original)
	assert.Contains(t, string(stored.Content.Supplemental), contracts.MuseSupplementFieldUnavailable)
	assert.Contains(t, string(stored.Content.Supplemental), "gap")
}

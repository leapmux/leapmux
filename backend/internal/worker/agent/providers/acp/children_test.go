package acp

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newChildRouteBase creates a bare base whose provider tags subagent updates through _meta.parentToolCallId.
// Its spawn detector accepts a tool call titled "spawn".
func newChildRouteBase(t *testing.T) (*Base, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.hooks.ChildUpdateRoute = func(_ string, meta map[string]json.RawMessage) string {
		var parent string
		if json.Unmarshal(meta["parentToolCallId"], &parent) != nil {
			return ""
		}
		return parent
	}
	b.hooks.SubagentFromToolCall = func(tc ToolCallEnvelope) *SubagentObservation {
		if tc.Title != "spawn" {
			return nil
		}
		return &SubagentObservation{RowKey: tc.ToolCallID, ChildAgentKey: tc.ToolCallID, Title: "helper", Status: bgtask.StatusRunning, Spawns: true}
	}
	b.hooks.SubagentFromToolCallUpdate = func(tcu ToolCallUpdateEnvelope) *SubagentObservation {
		if tcu.ToolCallID != "call-spawn" || !StatusIsFinal(tcu.Status) {
			return nil
		}
		return &SubagentObservation{RowKey: tcu.ToolCallID, Status: FinalStatus(tcu.Status), CloseRow: true, Mode: ModeCloseOnly}
	}
	return b, sink
}

// sessionUpdate wraps one update in the params of a session/update of sessionID.
func sessionUpdate(t *testing.T, sessionID, update string) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "update": json.RawMessage(update)})
	require.NoError(t, err)
	return params
}

// registryProbeSink wraps a test agent's services and records each child that the base releases.
// It fails the registry writes that the test selects.
// The underlying test sink records no release and fails neither rename nor close.
type registryProbeSink struct {
	agent.ProviderServices
	mu         sync.Mutex
	released   []string
	failClose  bool
	failRename bool
}

func (s *registryProbeSink) CleanupChildAgent(childAgentID string) {
	s.mu.Lock()
	s.released = append(s.released, childAgentID)
	s.mu.Unlock()
	s.ProviderServices.CleanupChildAgent(childAgentID)
}

func (s *registryProbeSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	s.mu.Lock()
	fail := s.failClose
	s.mu.Unlock()
	if fail {
		return assert.AnError
	}
	return s.ProviderServices.CloseBackgroundTask(rowKey, status)
}

func (s *registryProbeSink) RenameBackgroundTask(oldKey, newKey string) error {
	s.mu.Lock()
	fail := s.failRename
	s.mu.Unlock()
	if fail {
		return assert.AnError
	}
	return s.ProviderServices.RenameBackgroundTask(oldKey, newKey)
}

// setFailClose selects whether each later close of a row fails.
func (s *registryProbeSink) setFailClose(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failClose = fail
}

// releasedChildren returns the id of each child agent that the base released,
// in order.
func (s *registryProbeSink) releasedChildren() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.released...)
}

// lookupBarrierSink holds registry reads until the number of arrived reads reaches waiters.
// It then releases every read, and later reads proceed immediately.
type lookupBarrierSink struct {
	agent.ProviderServices
	mu      sync.Mutex
	arrived int
	waiters int
	release chan struct{}
	entered chan struct{}
}

func (s *lookupBarrierSink) LookupBackgroundTask(rowKey string) (string, bgtask.Status, bool, error) {
	s.mu.Lock()
	s.arrived++
	if s.arrived == 1 && s.entered != nil {
		close(s.entered)
	}
	if s.arrived == s.waiters {
		close(s.release)
	}
	s.mu.Unlock()
	<-s.release
	return s.ProviderServices.LookupBackgroundTask(rowKey)
}

// assembledTexts returns the text of each assembled message of messages, in order.
func assembledTexts(t *testing.T, messages []agenttest.Message) []string {
	t.Helper()
	var texts []string
	for _, message := range messages {
		kind, text, _, ok := agenttest.DecodeAssembledMessage(message.Content)
		if !ok {
			continue
		}
		prefix := "text:"
		if kind == agent.AssembledMessageKindReasoning {
			prefix = "thought:"
		}
		texts = append(texts, prefix+text)
	}
	return texts
}

// childRefusalServices uses the fixture's real child identity checks.
// It records observation writes and keeps injected persistence failures untyped.
type childRefusalServices struct {
	agent.ProviderServices
	mu          sync.Mutex
	failure     error
	writes      []string
	ensureCalls int
}

func (s *childRefusalServices) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	s.mu.Lock()
	s.ensureCalls++
	failure := s.failure
	s.mu.Unlock()
	if failure != nil {
		return "", failure
	}
	return s.ProviderServices.EnsureChildAgent(spec)
}

func (s *childRefusalServices) setFailure(err error) {
	s.mu.Lock()
	s.failure = err
	s.mu.Unlock()
}

func (s *childRefusalServices) recordWrite(write string) {
	s.mu.Lock()
	s.writes = append(s.writes, write)
	s.mu.Unlock()
}

func (s *childRefusalServices) resetWrites() {
	s.mu.Lock()
	s.writes = nil
	s.mu.Unlock()
}

func (s *childRefusalServices) recordedWrites() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *childRefusalServices) validationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureCalls
}

func (s *childRefusalServices) UpsertBackgroundTask(task bgtask.Upsert) error {
	s.recordWrite("upsert")
	return s.ProviderServices.UpsertBackgroundTask(task)
}

func (s *childRefusalServices) PersistChildPrompt(id, prompt string) error {
	s.recordWrite("prompt")
	return s.ProviderServices.PersistChildPrompt(id, prompt)
}

func (s *childRefusalServices) PersistChildMessage(id string, source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	s.recordWrite("payload")
	return s.ProviderServices.PersistChildMessage(id, source, content, span)
}

func (s *childRefusalServices) PersistChildSubagentReport(write agent.ChildSubagentReportWrite) (bool, error) {
	s.recordWrite("report")
	return s.ProviderServices.PersistChildSubagentReport(write)
}

func (s *childRefusalServices) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	s.recordWrite("close")
	return s.ProviderServices.CloseBackgroundTask(rowKey, status)
}

func (s *childRefusalServices) CleanupChildAgent(id string) {
	s.recordWrite("cleanup")
	s.ProviderServices.CleanupChildAgent(id)
}

func (s *childRefusalServices) RenameBackgroundTask(from, to string) error {
	s.recordWrite("rename")
	return s.ProviderServices.RenameBackgroundTask(from, to)
}

func seedRefusalChild(t *testing.T, sink *agenttest.Sink, rowKey, sessionID string) *agenttest.Sink {
	t.Helper()
	id, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		SpawnSpanID: rowKey, ProviderChildKey: rowKey, AgentSessionID: sessionID,
	})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: id,
		Title: "Stored helper", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.PersistChildPrompt(id, "Stored prompt."))
	return sink.Child(id)
}

func newChildRefusalBase(t *testing.T, storedSession string) (*Base, *agenttest.Sink, *agenttest.Sink, *childRefusalServices) {
	t.Helper()
	b, sink := newChildRouteBase(t)
	child := seedRefusalChild(t, sink, "call-spawn", storedSession)
	services := &childRefusalServices{ProviderServices: agent.NewProviderServices(sink)}
	b.sink = services
	return b, sink, child, services
}

func validateRefusalChild(b *Base, rowKey, sessionID string) {
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: rowKey, ChildAgentKey: rowKey, ChildAgentSessionID: sessionID,
		Status: bgtask.StatusRunning, Mode: ModeCloseOnly,
	})
}

func childRefusalPlan(t *testing.T, text string) json.RawMessage {
	t.Helper()
	update, err := json.Marshal(map[string]any{
		"sessionUpdate": "plan", "entries": []map[string]string{{"content": text, "status": "pending"}},
	})
	require.NoError(t, err)
	return update
}

func TestChildIdentityRefusalBlocksRegistryAndCachedRoutes(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, stored string
		cached       bool
	}{
		{name: "different stored session", stored: "native-old"},
		{name: "empty stored session"},
		{name: "cached conversation", stored: "native-old", cached: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			b, sink, child, _ := newChildRefusalBase(t, scenario.stored)
			if scenario.cached {
				require.True(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Accepted step.")))
			}
			messages := child.Messages()
			row, found := sink.BackgroundTask("call-spawn")
			require.True(t, found)
			validateRefusalChild(b, "call-spawn", "native-new")

			assert.False(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Refused step.")))
			assert.Equal(t, messages, child.Messages())
			after, found := sink.BackgroundTask("call-spawn")
			require.True(t, found)
			assert.Equal(t, row, after)
			assert.Equal(t, scenario.stored, child.LastSessionID())
		})
	}
}

func TestChildIdentityRefusalConsumesTaggedUpdatesWithoutRootFallback(t *testing.T) {
	t.Parallel()
	b, sink, child, _ := newChildRefusalBase(t, "native-old")
	validateRefusalChild(b, "call-spawn", "native-new")
	before := child.Messages()
	for _, update := range []string{
		`{"sessionUpdate":"plan","entries":[{"content":"Refused plan.","status":"pending"}],"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Refused text."},"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"refused-tool","title":"Read","status":"pending","_meta":{"parentToolCallId":"call-spawn"}}`,
	} {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", update))
	}
	b.FinishChildTurn("call-spawn")
	assert.Equal(t, before, child.Messages())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, b.TurnAssistantTextForTest().String())
}

func TestChildIdentityRefusalRequiresSuccessfulValidationBeforeRestoration(t *testing.T) {
	t.Parallel()
	b, _, child, services := newChildRefusalBase(t, "native-old")
	validateRefusalChild(b, "call-spawn", "native-new")
	services.setFailure(assert.AnError)
	validateRefusalChild(b, "call-spawn", "native-old")
	before := child.Messages()
	assert.False(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Still refused.")))
	assert.Equal(t, before, child.Messages())
	services.setFailure(nil)
	validateRefusalChild(b, "call-spawn", "native-old")
	require.True(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Restored step.")))
	assert.Len(t, child.Messages(), len(before)+1)
	assert.Contains(t, string(child.Messages()[len(before)].Content), "Restored step.")
	assert.Equal(t, 3, services.validationCount())
}

func TestChildTransientFailureKeepsTheExistingRouteAndLaterValidation(t *testing.T) {
	t.Parallel()
	b, _, child, services := newChildRefusalBase(t, "native-old")
	services.setFailure(assert.AnError)
	_, err := services.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "call-spawn", ProviderChildKey: "call-spawn"})
	require.ErrorIs(t, err, assert.AnError)
	assert.NotErrorIs(t, err, agent.ErrChildIdentityRefused)
	validateRefusalChild(b, "call-spawn", "native-old")
	require.True(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Registry step.")))
	services.setFailure(nil)
	validateRefusalChild(b, "call-spawn", "native-old")
	require.True(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Validated step.")))
	assert.Len(t, child.Messages(), 3)
}

func TestChildIdentityRefusalProtectsAttachedSessionReaders(t *testing.T) {
	t.Parallel()
	b, sink, child, _ := newChildRefusalBase(t, "native-old")
	b.AttachChildSession("child-session", "call-spawn")
	require.True(t, b.FeedChildUpdate("call-spawn", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"child-open","title":"Read","status":"pending"}`)))
	before := child.Messages()
	validateRefusalChild(b, "call-spawn", "native-new")
	services, found := b.OpenToolSink("child-session", "child-open")
	assert.False(t, found)
	assert.Nil(t, services)
	assert.False(t, b.ApplySubagentObservationForSession("child-session", &SubagentObservation{
		RowKey: "nested-refused", ChildAgentKey: "nested-refused", Status: bgtask.StatusRunning,
	}))
	b.FinishChildTurn("call-spawn")
	assert.Equal(t, before, child.Messages())
	assert.Equal(t, []string{"child-of-call-spawn"}, sink.ChildAgentIDs())
	assert.Empty(t, child.ChildAgentIDs())
}

func TestChildIdentityRefusalFinishesEarlierAcceptedOutputAtProcessEnd(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"stop", "wait"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			b, sink := newExitedACPTurnBase(t)
			child := seedRefusalChild(t, sink, "call-spawn", "native-old")
			services := &childRefusalServices{ProviderServices: agent.NewProviderServices(sink)}
			b.sink = services
			validateRefusalChild(b, "call-spawn", "native-old")
			require.True(t, b.FeedChildUpdate("call-spawn", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Accepted partial."}}`)))
			validateRefusalChild(b, "call-spawn", "native-new")
			b.ApplySubagentObservation(&SubagentObservation{
				RowKey: "call-spawn", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly,
			})
			assert.Empty(t, assembledTexts(t, child.Messages()), "a refused close must not finish earlier accepted output")
			assert.False(t, b.FeedChildUpdate("call-spawn", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Refused suffix."}}`)))
			if method == "stop" {
				b.Stop()
			} else {
				require.NoError(t, b.Wait())
			}
			assert.Equal(t, []string{"text:Accepted partial."}, assembledTexts(t, child.Messages()))
			assert.Equal(t, agent.MessageCompletionInterrupted, childTextCompletion(t, child))
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestChildIdentityRefusalRechecksAnInFlightRegistryLookup(t *testing.T) {
	t.Parallel()
	b, sink, child, _ := newChildRefusalBase(t, "native-old")
	barrier := &lookupBarrierSink{
		ProviderServices: agent.NewProviderServices(sink),
		release:          make(chan struct{}), entered: make(chan struct{}),
	}
	var release sync.Once
	defer release.Do(func() { close(barrier.release) })
	b.sink = &childRefusalServices{ProviderServices: barrier}
	result := make(chan bool, 1)
	update := childRefusalPlan(t, "Late lookup step.")
	go func() { result <- b.FeedChildUpdate("call-spawn", update) }()
	select {
	case <-barrier.entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the registry lookup did not reach the barrier")
	}
	validateRefusalChild(b, "call-spawn", "native-new")
	release.Do(func() { close(barrier.release) })
	select {
	case routed := <-result:
		assert.False(t, routed)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the registry lookup did not finish")
	}
	assert.Len(t, child.Messages(), 1)
}

func TestChildRoute_TaggedUpdatesReachTheChildTranscript(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	childID := "child-of-call-spawn"
	require.Equal(t, []string{childID}, sink.ChildAgentIDs())

	for _, update := range []string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Child thinks."},"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Child "},"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answers."},"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call-child-read","title":"Read","kind":"read","status":"pending","_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call-child-read","status":"completed","content":[{"type":"content","content":{"type":"text","text":"data"}}],"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Parent speaks."}}`,
	} {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", update))
	}

	child := sink.Child(childID)
	// The child assembles one text row for each segment in its own transcript.
	// Its tool call opens and closes a span there.
	assert.Equal(t, []string{"thought:Child thinks.", "text:Child answers."}, assembledTexts(t, child.Messages()))
	var childToolRows []agenttest.Message
	for _, message := range child.Messages() {
		if message.SpanID == "call-child-read" {
			childToolRows = append(childToolRows, message)
		}
	}
	require.Len(t, childToolRows, 2, "the child tool call writes its request and its result")
	assert.False(t, childToolRows[0].Closing)
	assert.True(t, childToolRows[1].Closing)

	// Nothing of the child reached the parent, and the parent's own text stays
	// buffered in the parent until its segment ends.
	for _, message := range sink.Messages() {
		assert.NotEqual(t, "call-child-read", message.SpanID, "a child tool call must not reach the parent transcript")
	}
	assert.Equal(t, "Parent speaks.", b.TurnAssistantTextForTest().String())
}

func TestChildRoute_AnUnknownTagStaysInTheMainTranscript(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)

	// The tag identifies a tool call that spawns no child known to this agent.
	// The registry therefore has no child for it, and the update stays where the reader can see it.
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"orphan"},"_meta":{"parentToolCallId":"call-unknown"}}`))

	assert.Equal(t, "orphan", b.TurnAssistantTextForTest().String())
	assert.Empty(t, sink.ChildAgentIDs())
}

func TestChildRoute_TheMetadataHandlerNeverReadsAChildUpdate(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	var read []string
	b.hooks.SessionMetadataHandler = func(updateType string, _ map[string]json.RawMessage, _ json.RawMessage) bool {
		read = append(read, updateType)
		return false
	}

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	// The usage of the SUBAGENT must not reach the reader of the main session.
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"parentToolCallId":"call-spawn","usage":{"inputTokens":9}}}`))

	assert.Equal(t, []string{"tool_call"}, read)
}

func TestSessionMetadataHandler_ReadsTheWholeUpdate(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	var updates []string
	b.hooks.SessionMetadataHandler = func(_ string, _ map[string]json.RawMessage, update json.RawMessage) bool {
		updates = append(updates, string(update))
		return true
	}
	const update = `{"sessionUpdate":"session_info_update","_meta":{"vendor":{"kind":"turn_end","stopReason":"end_turn"}}}`

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", update))

	require.Len(t, updates, 1)
	assert.JSONEq(t, update, updates[0], "a provider that keeps an update as a row needs all of it")
}

func TestChildRoute_TheCloseStoresWhatTheChildStillHeld(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-child-open","title":"Run","kind":"execute","status":"pending","_meta":{"parentToolCallId":"call-spawn"}}`))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Last words."},"_meta":{"parentToolCallId":"call-spawn"}}`))
	child := sink.Child("child-of-call-spawn")
	require.Empty(t, assembledTexts(t, child.Messages()), "the segment has not ended yet")

	var closedBefore []string
	sink.OnCloseBackgroundTask = func(string, bgtask.Status) {
		closedBefore = assembledTexts(t, child.Messages())
	}
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"call-spawn","status":"completed"}`))

	assert.Equal(t, []string{"text:Last words."}, closedBefore, "the text reaches the child BEFORE the close draws its divider")
	var closing *agenttest.Message
	for _, message := range child.Messages() {
		if message.SpanID == "call-child-open" && message.Closing {
			closing = &message
		}
	}
	require.NotNil(t, closing, "the tool call the child left open is stored")
	assert.Equal(t, agent.MessageCompletionError, closing.Completion, "a completed subagent left that call unfinished")

	// The closed row has no route left: a late update stays in the parent, and
	// no child conversation opens again for the row.
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"late"},"_meta":{"parentToolCallId":"call-spawn"}}`))
	assert.Equal(t, "late", b.TurnAssistantTextForTest().String(), "the late text is in the parent's buffer")
	b.children.mu.Lock()
	_, reopened := b.children.active["call-spawn"]
	b.children.mu.Unlock()
	assert.False(t, reopened, "the closed row opened a child conversation again")
	assert.NotContains(t, assembledTexts(t, child.Messages()), "text:late")
}

// A subagent tool call that never ends closes its row at the turn's end.
// The row then has no route, as after an agent-reported close.
func TestChildRoute_AnIncompleteSpawnLeavesNoRoute(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Child speaks."},"_meta":{"parentToolCallId":"call-spawn"}}`))

	b.finishAllTurnOutput(agent.MessageCompletionInterrupted)

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"late"},"_meta":{"parentToolCallId":"call-spawn"}}`))
	assert.Equal(t, "late", b.TurnAssistantTextForTest().String(), "the late text is in the parent's buffer")
	b.children.mu.Lock()
	_, reopened := b.children.active["call-spawn"]
	b.children.mu.Unlock()
	assert.False(t, reopened)
}

// A spawn that remains open closes its row at the turn's end.
// The base releases its child agent, as for an agent-reported row close.
// A root that repeatedly starts subagents therefore retains no closed child's service state.
func TestChildRoute_AnIncompleteSpawnReleasesItsChildAgent(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	probe := &registryProbeSink{ProviderServices: b.sink}
	b.sink = probe
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Child speaks."},"_meta":{"parentToolCallId":"call-spawn"}}`))
	require.Equal(t, []string{"child-of-call-spawn"}, sink.ChildAgentIDs())
	require.Empty(t, probe.releasedChildren())

	b.finishAllTurnOutput(agent.MessageCompletionInterrupted)

	row, ok := sink.BackgroundTask("call-spawn")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status, "a stopped turn stops the subagent that it left open")
	assert.Equal(t, []string{"child-of-call-spawn"}, probe.releasedChildren(), "the closed row releases its child agent once")
	messages := sink.Child("child-of-call-spawn").Messages()
	require.Len(t, messages, 1)
	_, text, completion, isText := agenttest.DecodeAssembledMessage(messages[0].Content)
	require.True(t, isText)
	assert.Equal(t, "Child speaks.", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion, "the child text ends with the stop")
}

// The registry retains a finished row and its child ID.
// A tag for that row must not open a transcript that nothing later finishes.
// Remember that result so each streamed chunk does not read the registry again.
func TestChildRoute_AFinishedRegistryRowOpensNoChild(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	// This finished row retains its child link from an earlier process.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "call-old", Kind: bgtask.KindSubagent, ChildAgentID: "child-old", Status: bgtask.StatusRunning}))
	require.NoError(t, sink.CloseBackgroundTask("call-old", bgtask.StatusSucceeded))

	for range 3 {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"},"_meta":{"parentToolCallId":"call-old"}}`))
	}

	assert.Equal(t, "xxx", b.TurnAssistantTextForTest().String())
	assert.Empty(t, sink.ChildAgentIDs(), "no child transcript opens for the finished row")
	assert.Equal(t, 1, sink.LookupBackgroundTaskCalls("call-old"), "one registry read answers every later chunk")
}

// Read an absent tagged row again on the next update.
// A provider can create it later through a path outside the base, such as Qwen Code's background-subagent store reader.
// Retaining the preceding miss would discard that child's transcript.
func TestChildRoute_AnUnknownRowIsReadAgain(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"early"},"_meta":{"parentToolCallId":"call-late"}}`))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "call-late", Kind: bgtask.KindSubagent, ChildAgentID: "child-late", Status: bgtask.StatusRunning}))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"routed"},"_meta":{"parentToolCallId":"call-late"}}`))

	assert.Equal(t, "early", b.TurnAssistantTextForTest().String(), "the update before the row stays in the parent")
	b.children.mu.Lock()
	_, routed := b.children.active["call-late"]
	b.children.mu.Unlock()
	assert.True(t, routed, "the update after the row reaches the child")
}

func TestChildSession_TheAgentsMessagesOpenAndContinueTheChildTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.hooks.ChildUserMessages = true
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "step-session", ChildAgentKey: "step-session", Title: "step", Status: bgtask.StatusRunning, Spawns: true})
	b.AttachChildSession("step-session", "step-session")

	for _, update := range []string{
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"Do the step."}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Working."}}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"A warning of the run."}}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"  "}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Done."}}`,
	} {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "step-session", update))
	}
	b.FinishChildTurn("step-session")

	child := sink.Child("child-of-step-session")
	var rows []string
	for _, message := range child.Messages() {
		if _, text, _, ok := agenttest.DecodeAssembledMessage(message.Content); ok {
			rows = append(rows, "agent:"+text)
			continue
		}
		var user map[string]string
		require.NoError(t, json.Unmarshal(message.Content, &user))
		mark := ""
		if message.MarkType == leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE {
			mark = " (marked)"
		}
		rows = append(rows, "user:"+user["content"]+mark)
	}
	assert.Equal(t, []string{
		"user:Do the step.",
		"agent:Working.",
		"user:A warning of the run. (marked)",
		"agent:Done.",
	}, rows, "the first message is the prompt, a later one keeps its place, and a blank one is dropped")
}

func TestChildSession_WithoutTheHookTheAgentsMessagesAreDropped(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "step-session", ChildAgentKey: "step-session", Status: bgtask.StatusRunning})
	b.AttachChildSession("step-session", "step-session")

	b.HandleSessionUpdateForTest(sessionUpdate(t, "step-session", `{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"History."}}`))

	assert.Empty(t, sink.Child("child-of-step-session").Messages(), "a user_message_chunk is a replay for a provider that did not ask for it")
}

func TestChildSession_ASpawnPromptKeepsItsPlaceBeforeTheAgentsFirstMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.hooks.ChildUserMessages = true
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "step-session", ChildAgentKey: "step-session", Status: bgtask.StatusRunning, Spawns: true, Prompt: "The spawn's prompt."})
	b.AttachChildSession("step-session", "step-session")

	b.HandleSessionUpdateForTest(sessionUpdate(t, "step-session", `{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"The same instruction."}}`))

	var contents []string
	for _, message := range sink.Child("child-of-step-session").Messages() {
		var user map[string]string
		if json.Unmarshal(message.Content, &user) == nil && user["content"] != "" {
			contents = append(contents, user["content"])
		}
	}
	assert.Equal(t, []string{"The spawn's prompt."}, contents, "the prompt already opened the transcript")
}

func TestChildCloseCompletions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status      bgtask.Status
		text, tools agent.MessageCompletion
	}{
		{bgtask.StatusSucceeded, agent.MessageCompletionComplete, agent.MessageCompletionError},
		{bgtask.StatusFailed, agent.MessageCompletionError, agent.MessageCompletionError},
		{bgtask.StatusStopped, agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted},
		{bgtask.StatusInterrupted, agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted},
	} {
		text, tools := childCloseCompletions(tc.status)
		assert.Equal(t, tc.text, text, "text of %v", tc.status)
		assert.Equal(t, tc.tools, tools, "tools of %v", tc.status)
	}
}

func TestChildSession_UpdatesOfAnAttachedSessionReachTheChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", ChildAgentKey: "call-spawn", Title: "helper", Status: bgtask.StatusRunning, Spawns: true})
	b.AttachChildSession("child-session", "call-spawn")

	b.HandleSessionUpdateForTest(sessionUpdate(t, "child-session", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"From the child."}}`))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "other-session", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"From nowhere."}}`))
	b.FinishChildTurn("call-spawn")

	assert.Equal(t, []string{"text:From the child."}, assembledTexts(t, sink.Child("child-of-call-spawn").Messages()))
	assert.Empty(t, b.TurnAssistantTextForTest().String(), "a session this agent does not serve renders nowhere")

	// The close drops the route with the row.
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})
	assert.Empty(t, b.childSessionRow("child-session"))
}

func TestChildSession_TheMainSessionIsNeverAChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", ChildAgentKey: "call-spawn", Status: bgtask.StatusRunning})
	// A provider that attaches the main session by mistake routes nothing:
	// the main session is dispatched before the child map is read.
	b.AttachChildSession("session-1", "call-spawn")

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"main"}}`))
	assert.Equal(t, "main", b.TurnAssistantTextForTest().String())
}

func TestFeedChildUpdate_AFedChildSerializesWithTheReader(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-bg", ChildAgentKey: "call-bg", Status: bgtask.StatusRunning})

	// A provider feeds a background child from a separate goroutine while the reader closes its row.
	// Every write holds that child's lock, so the race detector finds no unprotected state and each supplied segment appears once.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.FeedChildUpdate("call-bg", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-`+string(rune('a'+i))+`","title":"Read","kind":"read","status":"completed"}`))
		}()
	}
	wg.Wait()
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-bg", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})

	closing := 0
	for _, message := range sink.Child("child-of-call-bg").Messages() {
		if message.Closing {
			closing++
		}
	}
	assert.Equal(t, 20, closing)
}

func TestFeedChildUpdate_ARowWithNoChildIsRefused(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}

	assert.False(t, b.FeedChildUpdate("", json.RawMessage(`{"sessionUpdate":"agent_message_chunk"}`)))
	assert.False(t, b.FeedChildUpdate("call-none", json.RawMessage(`{"sessionUpdate":"agent_message_chunk"}`)))
	assert.False(t, b.FeedChildUpdate("call-none", json.RawMessage(`not json`)))
	assert.Equal(t, 0, sink.MessageCount())
}

func TestChildRoute_ARegistryReadFailureRoutesNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{LookupErr: assert.AnError}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}

	// This process did not create the row, so the base reads the registry.
	// A failed registry read must not count as a missing child that can route to the parent.
	assert.False(t, b.FeedChildUpdate("call-restarted", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`)))
}

func TestChildRoute_ARowFromBeforeARestartResolvesThroughTheRegistry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// The row and its child exist in the registry, but no observation of THIS
	// base created them: the worker restarted.
	_, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "call-old", ProviderChildKey: "call-old", Title: "old helper"})
	require.NoError(t, err)
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}

	require.True(t, b.FeedChildUpdate("call-old", json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step","status":"pending"}]}`)))
	messages := sink.Child("child-of-call-old").Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[0].Source)
}

// Stop and Wait exercise this path when stopping the process. See TestAgentTurn_StopClosesTheToolsOfBothTurns.
// This test exercises the helper independently.
func TestFinishAllChildConversations_EndsEveryChildAsAStop(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-a", ChildAgentKey: "call-a", Status: bgtask.StatusRunning})
	require.True(t, b.FeedChildUpdate("call-a", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"cut off"}}`)))

	b.finishAllChildConversations()

	messages := sink.Child("child-of-call-a").Messages()
	require.Len(t, messages, 1)
	_, text, completion, ok := agenttest.DecodeAssembledMessage(messages[0].Content)
	require.True(t, ok)
	assert.Equal(t, "cut off", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion)
}

// A child renders conversation updates only.
// These updates belong to the main session:
//   - Mode.
//   - Options.
//   - Command set.
//   - Usage.
//   - Session information.
// A tagged update of any listed type changes no parent state and writes no transcript row.
// An unrecognized update type counts as conversation content and enters the child transcript.
func TestChildRoute_ASessionStateUpdateOfAChildChangesNothing(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	b.hooks.ModeChannel = ModeChannelPermissionMode
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	parentRows := sink.MessageCount()
	child := sink.Child("child-of-call-spawn")

	for _, update := range []string{
		`{"sessionUpdate":"current_mode_update","currentModeId":"plan","_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"usage_update","used":900,"size":1000,"cost":{"amount":2,"currency":"USD"},"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"goal"}],"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","category":"mode","type":"select","currentValue":"plan","options":[{"value":"plan"}]}],"_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"session_info_update","title":"Child title","_meta":{"parentToolCallId":"call-spawn"}}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"replay"},"_meta":{"parentToolCallId":"call-spawn"}}`,
	} {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", update))
	}

	assert.Empty(t, b.PermissionModeForTest(), "the mode of a child is not the mode of the parent")
	assert.Nil(t, b.AvailableModes())
	assert.False(t, b.HasAvailableCommand("goal"))
	assert.Zero(t, sink.SessionInfoCount(), "the usage of a child is not the usage of the parent")
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Zero(t, sink.StatusActiveCount())
	assert.Zero(t, sink.GoalCapabilityPublishes())
	assert.Equal(t, parentRows, sink.MessageCount())
	assert.Zero(t, child.MessageCount())

	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"vendor_note","text":"Child note.","_meta":{"parentToolCallId":"call-spawn"}}`))

	require.Len(t, child.Messages(), 1, "an update type that the base does not know is conversation")
	assert.Contains(t, string(child.Messages()[0].Content), "Child note.")
	assert.Equal(t, parentRows, sink.MessageCount())
}

// The reader and a provider's separate goroutine can open one row's child conversation concurrently.
// Registry reads occur outside the lock, so both can read the same row.
// Keep the first opened conversation and render every update into it.
// The close then finds every tool call opened by any update.
func TestChildRoute_ConcurrentFirstUpdatesShareOneConversation(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// A row of a process that ran before a worker restart, so no observation of
	// this base knows it.
	_, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "call-old", ProviderChildKey: "call-old", Title: "old helper"})
	require.NoError(t, err)
	const feeds = 20
	// Hold each update after its registry read until every update finishes that read.
	// All updates then reach the conversation-creation step while no conversation exists.
	barrier := &lookupBarrierSink{ProviderServices: agent.NewProviderServices(sink), waiters: feeds, release: make(chan struct{})}
	b := &Base{sink: barrier, sessionID: "session-1"}

	var wg sync.WaitGroup
	for i := range feeds {
		wg.Go(func() {
			update := fmt.Sprintf(`{"sessionUpdate":"tool_call","toolCallId":"call-%d","title":"Read","kind":"read","status":"pending"}`, i)
			assert.True(t, b.FeedChildUpdate("call-old", json.RawMessage(update)))
		})
	}
	wg.Wait()
	b.finishAllChildConversations()

	closing := 0
	for _, message := range sink.Child("child-of-call-old").Messages() {
		if message.Closing {
			closing++
			assert.Equal(t, agent.MessageCompletionInterrupted, message.Completion)
		}
	}
	assert.Equal(t, feeds, closing, "the close finds each tool call that any update opened")
}

// A provider can learn the stable subagent ID late and change the row key.
// The new key retains the transcript and child-session route.
// A close through the new key ends that route.
func TestChildSession_ARenamedRowKeepsItsTranscriptAndItsSessionRoute(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", ChildAgentKey: "call-spawn", Title: "helper", Status: bgtask.StatusRunning, Spawns: true})
	b.AttachChildSession("child-session", "call-spawn")
	b.HandleSessionUpdateForTest(sessionUpdate(t, "child-session", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Before, "}}`))

	b.ApplySubagentObservation(&SubagentObservation{RowKey: "ses-child", RenameFrom: "call-spawn", Status: bgtask.StatusRunning})

	assert.Equal(t, "ses-child", b.childSessionRow("child-session"))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "child-session", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"after."}}`))
	b.FinishChildTurn("ses-child")
	assert.Equal(t, []string{"text:Before, after."}, assembledTexts(t, sink.Child("child-of-call-spawn").Messages()),
		"one conversation continues across the rename")
	assert.Equal(t, []string{"child-of-call-spawn"}, sink.ChildAgentIDs(), "the rename opens no second transcript")

	b.ApplySubagentObservation(&SubagentObservation{RowKey: "ses-child", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})

	row, ok := sink.BackgroundTask("ses-child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, b.childSessionRow("child-session"), "the close under the new key ends the route")
}

// A refused registry rename keeps the row under its preceding key.
// The transcript and session route must stay under that key also.
// Creating a child under the requested new key would split one subagent across two identities.
func TestChildSession_AFailedRenameKeepsTheRouteOfTheOldRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: &registryProbeSink{ProviderServices: agent.NewProviderServices(sink), failRename: true}, sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", ChildAgentKey: "call-spawn", Title: "helper", Status: bgtask.StatusRunning, Spawns: true})
	b.AttachChildSession("child-session", "call-spawn")

	b.ApplySubagentObservation(&SubagentObservation{RowKey: "ses-child", RenameFrom: "call-spawn", ChildAgentKey: "ses-child", Status: bgtask.StatusRunning})

	assert.Equal(t, "call-spawn", b.childSessionRow("child-session"))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "child-session", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Still routed."}}`))
	b.FinishChildTurn("call-spawn")
	assert.Equal(t, []string{"text:Still routed."}, assembledTexts(t, sink.Child("child-of-call-spawn").Messages()))
	assert.Equal(t, []string{"child-of-call-spawn"}, sink.ChildAgentIDs(), "no child opens under the key that the registry refused")
	assert.Equal(t, []string{"call-spawn"}, agenttest.RowKeys(sink))
}

// An empty session ID or row key identifies nothing and attaches no route.
// Ending a turn for a row with no conversation writes nothing.
func TestAttachChildSession_AnEmptySessionOrRowAttachesNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}

	b.AttachChildSession("", "call-spawn")
	b.AttachChildSession("child-session", "")
	b.FinishChildTurn("call-none")

	assert.Empty(t, b.childSessionRow(""))
	assert.Empty(t, b.childSessionRow("child-session"))
	assert.False(t, b.ServesSession("child-session"))
	assert.Zero(t, sink.MessageCount())
}

// OpenToolSink finds the transcript that holds a running call.
// The current session uses the main transcript, and a routed subagent session uses its child transcript.
// Return no transcript in each of these cases:
//   - The call ended.
//   - The call never opened.
//   - No row routes the session.
func TestOpenToolSink_FindsTheTranscriptOfARunningCall(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", ChildAgentKey: "call-spawn", Title: "helper", Status: bgtask.StatusRunning, Spawns: true})
	b.AttachChildSession("child-session", "call-spawn")
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"main-call","title":"Run","kind":"execute","status":"in_progress"}`))
	b.HandleSessionUpdateForTest(sessionUpdate(t, "child-session", `{"sessionUpdate":"tool_call","toolCallId":"child-call","title":"Run","kind":"execute","status":"in_progress"}`))

	main, open := b.OpenToolSink("session-1", "main-call")
	require.True(t, open)
	main.ReportProgress(agent.OutputTailProgress("main-call", "main out", false))
	child, open := b.OpenToolSink("child-session", "child-call")
	require.True(t, open)
	child.ReportProgress(agent.OutputTailProgress("child-call", "child out", false))
	assert.Len(t, sink.ProgressUpdates(), 1, "the main call reports into the main transcript")
	assert.Len(t, sink.Child("child-of-call-spawn").ProgressUpdates(), 1, "the child call reports into the child's transcript")

	for name, lookup := range map[string][2]string{
		"a call of another transcript": {"session-1", "child-call"},
		"a call that never opened":     {"child-session", "never"},
		"a session that no row routes": {"other-session", "child-call"},
		"no call":                      {"session-1", ""},
	} {
		_, open := b.OpenToolSink(lookup[0], lookup[1])
		assert.False(t, open, name)
	}
	b.HandleSessionUpdateForTest(sessionUpdate(t, "child-session", `{"sessionUpdate":"tool_call_update","toolCallId":"child-call","status":"completed"}`))
	_, open = b.OpenToolSink("child-session", "child-call")
	assert.False(t, open, "a call that ended is no longer open")
}

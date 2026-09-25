package acp

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newChildRouteBase is a bare base whose provider tags each update of a
// subagent with `_meta.parentToolCallId`, and whose spawn detector claims a
// tool call titled "spawn".
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

// decodeAssembled reads one assembled-message envelope.
func decodeAssembled(content []byte) (kind agent.AssembledMessageKind, text string, completion agent.MessageCompletion, ok bool) {
	var envelope map[string]string
	if json.Unmarshal(content, &envelope) != nil || envelope[contracts.AssembledMessageFieldType] != contracts.AssembledMessageType {
		return "", "", "", false
	}
	return agent.AssembledMessageKind(envelope[contracts.AssembledMessageFieldKind]), envelope[contracts.AssembledMessageFieldText],
		agent.MessageCompletion(envelope[contracts.AssembledMessageFieldCompletion]), true
}

// registryProbeSink wraps the services of a test agent. It records each child
// agent that the base releases, and it fails the registry writes that the test
// selects. The test sink records no release and fails no rename or close.
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

// lookupBarrierSink holds each registry read until waiters reads arrived, and
// then lets every read pass. A later read passes at once.
type lookupBarrierSink struct {
	agent.ProviderServices
	mu      sync.Mutex
	arrived int
	waiters int
	release chan struct{}
}

func (s *lookupBarrierSink) LookupBackgroundTask(rowKey string) (string, bgtask.Status, bool, error) {
	s.mu.Lock()
	s.arrived++
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
		kind, text, _, ok := decodeAssembled(message.Content)
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
	// The child's text is assembled into one row per segment, in its OWN
	// transcript, and its tool call opens and closes a span there.
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

	// The tag names a tool call that spawned nothing this agent knows, so the
	// registry has no child for it. The update stays where the reader can see it.
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

// A tool call that spawned a subagent and never ended closes its row at the end
// of the turn. The row has no route left afterwards, as after a close that the
// agent reported.
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

// A spawn that the turn left open closes its row at the turn end. The base then
// releases the child agent, as it does for a row that the agent closed, so a
// root that cycles many subagents keeps no service state of a closed child.
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
	_, text, completion, isText := decodeAssembled(messages[0].Content)
	require.True(t, isText)
	assert.Equal(t, "Child speaks.", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion, "the child text ends with the stop")
}

// The registry keeps a finished row and its child id. A tag of that row must
// not open a transcript that nothing ever finishes, and the answer is kept, so
// each streamed chunk does not read the registry again.
func TestChildRoute_AFinishedRegistryRowOpensNoChild(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	// A row of a process that ran before, which finished with its child link.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "call-old", Kind: bgtask.KindSubagent, ChildAgentID: "child-old", Status: bgtask.StatusRunning}))
	require.NoError(t, sink.CloseBackgroundTask("call-old", bgtask.StatusCompleted))

	for range 3 {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"},"_meta":{"parentToolCallId":"call-old"}}`))
	}

	assert.Equal(t, "xxx", b.TurnAssistantTextForTest().String())
	assert.Empty(t, sink.ChildAgentIDs(), "no child transcript opens for the finished row")
	assert.Equal(t, 1, sink.LookupBackgroundTaskCalls("call-old"), "one registry read answers every later chunk")
}

// A tag whose row the registry does not hold yet is read again on the next
// update. A provider can create the row later through a route that the base
// does not see (Qwen Code reads a background subagent from its store), and a
// kept miss would lose that child's transcript.
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
		if _, text, _, ok := decodeAssembled(message.Content); ok {
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
		{bgtask.StatusCompleted, agent.MessageCompletionComplete, agent.MessageCompletionError},
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
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", Status: bgtask.StatusCompleted, CloseRow: true, Mode: ModeCloseOnly})
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

	// A provider feeds a background child from a goroutine of its own while the
	// reader closes the row. Every write takes the lock of that child, so the
	// race detector sees no unguarded state and each fed segment lands once.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.FeedChildUpdate("call-bg", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-`+string(rune('a'+i))+`","title":"Read","kind":"read","status":"completed"}`))
		}()
	}
	wg.Wait()
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-bg", Status: bgtask.StatusCompleted, CloseRow: true, Mode: ModeCloseOnly})

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

	// The row is not one this process created, so the base asks the registry,
	// and a registry it cannot read is not a miss it may route on.
	assert.False(t, b.FeedChildUpdate("call-restarted", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`)))
}

func TestChildRoute_ARowFromBeforeARestartResolvesThroughTheRegistry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// The row and its child exist in the registry, but no observation of THIS
	// base created them: the worker restarted.
	_, err := sink.EnsureChildAgent("call-old", "call-old", "old helper")
	require.NoError(t, err)
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}

	require.True(t, b.FeedChildUpdate("call-old", json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step","status":"pending"}]}`)))
	messages := sink.Child("child-of-call-old").Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[0].Source)
}

// Stop and the exit of the process run this through Stop and Wait
// (TestAgentTurn_StopClosesTheToolsOfBothTurns). This pins the helper alone.
func TestFinishAllChildConversations_EndsEveryChildAsAStop(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-a", ChildAgentKey: "call-a", Status: bgtask.StatusRunning})
	require.True(t, b.FeedChildUpdate("call-a", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"cut off"}}`)))

	b.finishAllChildConversations()

	messages := sink.Child("child-of-call-a").Messages()
	require.Len(t, messages, 1)
	_, text, completion, ok := decodeAssembled(messages[0].Content)
	require.True(t, ok)
	assert.Equal(t, "cut off", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion)
}

// A child renders its conversation and nothing else. An update that states the
// mode, the options, the command set, the usage or the session information
// belongs to the main session, so a tagged one changes nothing of the parent
// and writes no row anywhere. An update type that the base does not know is
// conversation, and it lands in the child transcript.
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

// The reader and a provider that feeds a child from its own goroutine can open
// the conversation of one row at the same time. The row resolves through the
// registry outside the lock, so both read it. The first conversation wins and
// every update renders into it: the close then finds each tool call that any
// update opened.
func TestChildRoute_ConcurrentFirstUpdatesShareOneConversation(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// A row of a process that ran before a worker restart, so no observation of
	// this base knows it.
	_, err := sink.EnsureChildAgent("call-old", "call-old", "old helper")
	require.NoError(t, err)
	const feeds = 20
	// Each update waits after its registry read until every update read, so all
	// of them reach the step that opens the conversation with none open yet.
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

// A provider that learns the stable id of a subagent late re-keys its row. The
// row keeps its transcript and the route of its session under the new key, and
// the close under the new key ends the route.
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

	b.ApplySubagentObservation(&SubagentObservation{RowKey: "ses-child", Status: bgtask.StatusCompleted, CloseRow: true, Mode: ModeCloseOnly})

	row, ok := sink.BackgroundTask("ses-child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Empty(t, b.childSessionRow("child-session"), "the close under the new key ends the route")
}

// A rename that the registry refuses leaves the row under its old key, so the
// transcript and the session route stay with the old key too. A child under the
// new key would split one subagent in two.
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

// An empty session id or row key identifies nothing, so it attaches no route,
// and the end of a turn of a row with no conversation writes nothing.
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

// OpenToolSink finds the transcript that holds a running call: the main one for
// the current session, and the child's for a routed subagent session. A call
// that ended, a call that never opened and a session that no row routes find
// none.
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

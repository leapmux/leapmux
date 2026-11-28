package claude

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func holdClaudeTranscript(t *testing.T, sink agent.ProviderServices) func() {
	t.Helper()
	queue := agent.OutputQueueFor(sink)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block Claude storage", Run: func() error {
		return errors.New("Claude storage is unavailable")
	}})
	return func() {
		queue.ClearOwner(blocker)
		require.NoError(t, agent.FlushPendingOutput(sink))
	}
}

func TestClaudeRetainedAssistantDoesNotStartTheReplacementSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("s-1")
	services := agent.NewProviderServices(sink)
	a := newTestAgent(services)
	release := holdClaudeTranscript(t, services)

	a.HandleOutput(persistedAssistant("old", "old assistant answer"))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"s-2"}`))
	require.Equal(t, "s-2", services.MessageSessionID())
	release()

	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, "s-1", rows[0].AgentSessionID)
	assert.Equal(t, "old", rows[0].IdempotencyKey)
	assert.Empty(t, sink.TurnActives())
}

func TestClaudeRetainedAssistantDoesNotStartATurnAfterExit(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("s-1")
	services := agent.NewProviderServices(sink)
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent", ProcessDone: processDone}),
		sink:    services,
	}
	release := holdClaudeTranscript(t, services)

	a.HandleOutput(persistedAssistant("answer", "retained assistant answer"))
	close(processDone)
	require.NoError(t, a.Wait())
	release()

	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, "answer", rows[0].IdempotencyKey)
	assert.Empty(t, sink.TurnActives())
}

func TestClaudeRetainedAssistantKeepsRetryResolutionAfterExit(t *testing.T) {
	t.Parallel()
	sink := &claudeRetryOrderSink{}
	services := agent.NewProviderServices(sink)
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent", ProcessDone: processDone}),
		sink:    services,
	}
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
	require.Len(t, sink.PersistedNotifications(), 1)
	release := holdClaudeTranscript(t, services)
	a.HandleOutput(persistedAssistant("answer", "the model recovered"))

	close(processDone)
	require.NoError(t, a.Wait())
	release()

	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
	assert.Equal(t, []bool{true}, sink.resolvedBeforeMessage)
}

func TestClaudeRetainedRetryNoticeBelongsToItsNativeSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("s-1")
	services := agent.NewProviderServices(sink)
	a := newTestAgent(services)
	release := holdClaudeTranscript(t, services)

	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":1,"session_id":"s-1"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"s-2"}`))
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"answer","session_id":"s-2","message":{"content":[{"type":"text","text":"the new session answered"}]}}`))
	release()

	require.Len(t, sink.PersistedNotifications(), 1)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, "s-2", sink.Messages()[0].AgentSessionID)
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestClaudePendingReceiptReservesFreshInputUntilItResolves(t *testing.T) {
	t.Parallel()
	sink := &claudeReceiptLookupFailureSink{}
	sink.UpdateSessionID("s-1")
	services := agent.NewProviderServices(sink)
	a, stdin := newClaudeAgentWithStdin(services)
	captured := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	require.NoError(t, services.RetractMessagesByIdempotencyKey(captured, []string{"removed"}))

	sink.receiptErr = errors.New("receipt lookup unavailable")
	a.HandleOutput(persistedAssistant("removed", "replayed output"))
	assert.ErrorIs(t, agenttest.SendInput(a, "wait for the receipt", nil), agent.ErrAgentBusy)
	assert.Empty(t, stdin.String())
	assert.NotContains(t, sink.TurnActives(), true)

	sink.receiptErr = nil
	require.NoError(t, agent.FlushPendingOutput(services))
	require.NoError(t, agenttest.SendInput(a, "send after the receipt", nil))
	assert.Contains(t, stdin.String(), "send after the receipt")
}

func TestClaudeFallbackIgnoresUnrelatedMalformedResultFields(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("s-1")
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput(persistedAssistant("target", "refused partial"))
	fallback := strings.NewReplacer(
		`"scope":"local"`, `"scope":"session","is_error":48`,
		`"fallback_model":"m2"`, `"fallback_model":"claude-sonnet-4-6"`,
		`"content":"Switched to m2"`, `"content":"Switched to Sonnet"`,
	).Replace(string(fallbackRetracting("fallback", "target")))
	a.HandleOutput([]byte(fallback))

	assert.Equal(t, "sonnet", a.model)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Retracted)
	assert.Contains(t, string(rows[1].Content), "model_refusal_fallback")
}

func TestClaudeResultClearsCompactionBeforeItCanStore(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a := newTestAgent(services)
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	release := holdClaudeTranscript(t, services)

	a.HandleOutput([]byte(`{"type":"result","is_error":true,"result":"stopped"}`))
	assert.Empty(t, sink.Messages())
	assert.Equal(t, []any{map[string]any{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))

	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	release()
	assert.Equal(t, []any{map[string]any{}, nil, map[string]any{}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestClaudePeerResultClearsCompactionWithoutStoringADivider(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","result":"done","origin":{"kind":"peer","from":"task-1","senderTaskId":"task-1","body":"[Subagent hand-back] The report follows:\n  Findings","handback":true}}`))

	assert.Empty(t, sink.Messages())
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, []any{map[string]any{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestClaudeDelayedResultDoesNotConsumeTheSuccessorInterruptOrControl(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	services := agent.NewProviderServices(sink)
	a := newTestAgent(services)
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	release := holdClaudeTranscript(t, services)
	const failedResult = `{"type":"result","subtype":"error_during_execution","is_error":true}`
	a.HandleOutput([]byte(failedResult))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"successor","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e1"}}`))
	require.Len(t, sink.PublishedControls(), 1)
	a.noteInterruptRequested()
	release()

	first := sink.Messages()
	require.Len(t, first, 1)
	assert.Empty(t, first[0].Completion, "the earlier failed turn was not interrupted")
	assert.Empty(t, sink.CanceledControls(), "the earlier result cannot withdraw the successor's control")

	a.HandleOutput([]byte(failedResult))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
	assert.Equal(t, []string{"successor"}, sink.CanceledControls())
}

type claudeFailingChildDiscoverySink struct {
	agenttest.ControlSink
}

func (s *claudeFailingChildDiscoverySink) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	return "", errors.New("child lookup is unavailable")
}

func TestClaudeChildToolResultSettlesItsControlEvenIfDiscoveryFails(t *testing.T) {
	t.Parallel()
	sink := &claudeFailingChildDiscoverySink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"done","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"finished","input":{"command":"ls"}}}`))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"keep","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"unrelated","input":{"command":"ls"}}}`))
	require.Len(t, sink.PublishedControls(), 2)
	a.HandleOutput([]byte(`{"type":"user","uuid":"child-result","parent_tool_use_id":"spawn","message":{"content":[{"type":"tool_result","tool_use_id":"finished","content":"ok"}]}}`))

	assert.Equal(t, []string{"done"}, sink.CanceledControls())
}

func TestClaudeConsumedChildFrameDoesNotClaimTheRootsEvictionKey(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("s-1")
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput(persistedAssistant("shared", "refused partial"))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"spawn","task_type":"local_agent","prompt":"Inspect."}`))
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"shared","session_id":"s-1","parent_tool_use_id":"spawn","task_description":"Reviewer","message":{"content":[{"type":"tool_use","id":"handback","name":"SubagentHandback","input":{"message":"Findings"}}]}}`))
	child := sink.Child("child-of-spawn")
	require.Len(t, child.LeapMuxNotifications(), 1)
	require.Len(t, child.Messages(), 1, "only the opening prompt occupies a message row")
	a.HandleOutput(fallbackRetracting("fallback", "shared"))

	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Retracted, "the sole stored owner is the root")
	assert.Empty(t, child.Retractions())
}

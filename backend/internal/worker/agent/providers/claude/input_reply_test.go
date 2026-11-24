package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type claudeReplyProofSink struct {
	agenttest.Sink
	bound          map[agent.NativeInputReceipt]string
	retired        map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome
	controls       []agent.ControlRequest
	steerRowCounts []int
}

func (s *claudeReplyProofSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	record := s.Sink.CaptureSteerResolution(resolution)
	return func() error {
		if err := record(); err != nil {
			return err
		}
		s.steerRowCounts = append(s.steerRowCounts, s.MessageCount())
		return nil
	}
}

func (s *claudeReplyProofSink) BindNativeInputTurn(receipt agent.NativeInputReceipt, turnID string) error {
	s.bound[receipt] = turnID
	return nil
}

func (s *claudeReplyProofSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.retired[receipt] = outcome
	return nil
}

func (s *claudeReplyProofSink) PublishControlRequest(request agent.ControlRequest) error {
	s.controls = append(s.controls, request)
	return s.Sink.PublishControlRequest(request)
}

func startTrackedClaudeInput(t *testing.T) (*Agent, *claudeReplyProofSink, agent.NativeInputReceipt) {
	t.Helper()
	sink := &claudeReplyProofSink{
		bound:   make(map[agent.NativeInputReceipt]string),
		retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome),
	}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	cleanupClaudeReplyProcess(t, a)
	a.sessionID = "native-session"
	sink.UpdateSessionID(a.sessionID)
	receipt, err := a.SendInputWithReceipt("The release checklist", nil)
	require.NoError(t, err)
	var request UserInputMessage
	require.NoError(t, json.Unmarshal(stdin.Bytes(), &request))
	require.NotEmpty(t, request.UUID)
	require.Equal(t, agent.NativeInputReceipt{SessionID: "native-session", InputID: request.UUID}, receipt)
	return a, sink, receipt
}

func claudeInputAcknowledgement(sessionID, inputID string) []byte {
	return []byte(fmt.Sprintf(`{"type":"user","session_id":%q,"uuid":%q,"isReplay":true,"message":{"role":"user","content":"The release checklist"}}`, sessionID, inputID))
}

func TestClaudeInputReplyRequiresExactAcknowledgementAndRootResult(t *testing.T) {
	t.Parallel()
	a, sink, receipt := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement("previous-session", receipt.InputID))
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, "another-input"))
	require.Empty(t, sink.bound)

	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	assert.Equal(t, map[agent.NativeInputReceipt]string{receipt: receipt.InputID}, sink.bound)
	assert.Empty(t, sink.Messages(), "input echoes must not duplicate the accepted message")
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","message":{"role":"assistant","content":[{"type":"text","text":"The checklist passed."}]}}`))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"own-control","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-1","input":{"command":"printf ready"}}}`))
	require.Len(t, sink.controls, 1)
	assert.Equal(t, receipt.InputID, sink.controls[0].NativeTurnID)

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","origin":{"kind":"peer"},"result":"An unrelated peer result"}`))
	observed := sink.Messages()
	require.Len(t, observed, 2)
	assert.Equal(t, receipt.InputID, observed[0].NativeTurnID)
	assert.Empty(t, observed[1].NativeTurnID)
	assert.Zero(t, observed[1].InputTurnOutcome)

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"The checklist passed."}`))
	observed = sink.Messages()
	require.Len(t, observed, 3)
	assert.Equal(t, receipt.InputID, observed[2].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, observed[2].InputTurnOutcome)
	assert.Empty(t, sink.retired)
}

func TestClaudeSteerEchoKeepsItsPositionWhileOutputIsDeferred(t *testing.T) {
	t.Parallel()
	a, sink, lead := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement(lead.SessionID, lead.InputID))
	queue := agent.OutputQueueFor(a.sink)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Run: func() error { return errors.New("storage unavailable") }})
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","uuid":"before","message":{"role":"assistant","content":[{"type":"text","text":"Before the steer."}]}}`))
	request := claudeTestSteerRequest("The release checklist", nil)
	steer, err := a.SteerInputWithReceipt(request)
	require.NoError(t, err)
	a.HandleOutput(claudeInputAcknowledgement("previous-session", steer.InputID))
	a.HandleOutput(claudeInputAcknowledgement(steer.SessionID, "a different input with identical text"))
	a.HandleOutput(claudeInputAcknowledgement(steer.SessionID, steer.InputID))
	a.HandleOutput(claudeInputAcknowledgement(steer.SessionID, steer.InputID))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","uuid":"after","message":{"role":"assistant","content":[{"type":"text","text":"After the steer."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"After the steer."}`))
	assert.Empty(t, sink.SteerResolutions())
	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	steer.TurnID = lead.InputID
	assert.Equal(t, []agent.SteerResolution{{
		InputID: request.InputID, AttemptID: request.AttemptID, Receipt: steer, Outcome: agent.SteerOutcomeEchoed,
	}}, sink.SteerResolutions())
	assert.Equal(t, []int{1}, sink.steerRowCounts, "the echo follows the earlier reply and precedes the later reply")
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.Equal(t, lead.InputID, rows[1].NativeTurnID)
	assert.Equal(t, lead.InputID, rows[2].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[2].InputTurnOutcome)
}

func TestClaudeSteerNeedsAnEchoBeforeTheSessionSettles(t *testing.T) {
	t.Parallel()
	for name, boundary := range map[string]string{
		"native idle":         `{"type":"system","subtype":"session_state_changed","state":"idle"}`,
		"session replacement": `{"type":"system","subtype":"init","session_id":"next-session"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, lead := startTrackedClaudeInput(t)
			a.HandleOutput(claudeInputAcknowledgement(lead.SessionID, lead.InputID))
			request := claudeTestSteerRequest("The release checklist", nil)
			steer, err := a.SteerInputWithReceipt(request)
			require.NoError(t, err)
			a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
			assert.Empty(t, sink.SteerResolutions(), "the result for the preceding run is still in flight")
			a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"Lead done."}`))
			sink.PersistErr = assert.AnError
			a.HandleOutput([]byte(boundary))
			assert.Empty(t, sink.SteerResolutions())
			sink.PersistErr = nil
			require.NoError(t, agent.FlushPendingOutput(a.sink))
			a.HandleOutput(claudeInputAcknowledgement(steer.SessionID, steer.InputID))
			assert.Equal(t, []agent.SteerResolution{{
				InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeUncertain,
			}}, sink.SteerResolutions())
		})
	}
}

func TestClaudeInputReplyRetiresUnprovedWhenAcknowledgementNeverArrives(t *testing.T) {
	t.Parallel()
	a, sink, receipt := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, "another-input"))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"An unproved reply"}`))
	assert.Empty(t, sink.bound)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.retired[receipt])
	observed := sink.Messages()
	require.Len(t, observed, 1)
	assert.Empty(t, observed[0].NativeTurnID)
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	assert.Empty(t, sink.bound, "a delayed echo cannot revive a retired input")
}

func TestClaudeInputReplyReportsAnExplicitNativeAbort(t *testing.T) {
	t.Parallel()
	a, sink, receipt := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	a.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","session_id":"native-session","terminal_reason":"aborted_streaming","is_error":true}`))
	observed := sink.Messages()
	require.Len(t, observed, 1)
	assert.Equal(t, receipt.InputID, observed[0].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, observed[0].InputTurnOutcome)
	assert.Equal(t, agent.MessageCompletionInterrupted, observed[0].Completion)
}

func TestClaudeInputReplyRetiresOnConversationOrSessionReplacement(t *testing.T) {
	t.Parallel()
	for name, boundary := range map[string]string{
		"conversation reset":  `{"type":"conversation_reset","new_conversation_id":"next"}`,
		"session replacement": `{"type":"system","subtype":"init","session_id":"next-session"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, receipt := startTrackedClaudeInput(t)
			a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
			require.Contains(t, sink.bound, receipt)
			a.HandleOutput([]byte(boundary))
			assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.retired[receipt])
			a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"An old reply"}`))
			for _, message := range sink.Messages() {
				assert.Empty(t, message.NativeTurnID)
			}
		})
	}
}

type claudeInputWriter func([]byte) (int, error)

func (write claudeInputWriter) Write(data []byte) (int, error) { return write(data) }

func TestClaudeInputReplyKeepsAResultThatArrivesDuringTheStdinWrite(t *testing.T) {
	t.Parallel()
	const sessionID = "native-session"
	sink := &claudeReplyProofSink{
		bound:   make(map[agent.NativeInputReceipt]string),
		retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome),
	}
	sink.UpdateSessionID(sessionID)
	a := &Agent{sink: agent.NewProviderServices(sink), sessionID: sessionID}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent",
		Stdin: agenttest.NopStdin(claudeInputWriter(func(data []byte) (int, error) {
			var input UserInputMessage
			if err := json.Unmarshal(data, &input); err != nil {
				return 0, err
			}
			a.HandleOutput(claudeInputAcknowledgement(sessionID, input.UUID))
			a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","message":{"role":"assistant","content":[{"type":"text","text":"The checklist passed."}]}}`))
			a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"The checklist passed."}`))
			return len(data), nil
		})),
	})
	cleanupClaudeReplyProcess(t, a)
	receipt, err := a.SendInputWithReceipt("The release checklist", nil)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.InputID)
	assert.Equal(t, receipt.InputID, sink.bound[receipt])
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, receipt.InputID, messages[0].NativeTurnID)
	assert.Equal(t, receipt.InputID, messages[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, messages[1].InputTurnOutcome)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "the write must leave a completed input idle")
	assert.Empty(t, sink.retired)
}

func cleanupClaudeReplyProcess(t *testing.T, a *Agent) {
	t.Helper()
	t.Cleanup(func() {
		a.SimulateExitForTest()
		a.Process.Stop()
	})
}

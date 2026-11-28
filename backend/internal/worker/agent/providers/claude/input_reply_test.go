package claude

import (
	"bytes"
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
	agenttest.ControlSink
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

func startTrackedClaudeInput(t *testing.T) (*Agent, *claudeReplyProofSink, *bytes.Buffer, agent.NativeInputReceipt) {
	t.Helper()
	sink := &claudeReplyProofSink{}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	cleanupClaudeReplyProcess(t, a)
	a.sessionID = "native-session"
	sink.UpdateSessionID(a.sessionID)
	receipt, err := a.SendInput("The release checklist", nil)
	require.NoError(t, err)
	var request UserInputMessage
	require.NoError(t, json.Unmarshal(stdin.Bytes(), &request))
	stdin.Reset()
	require.NotEmpty(t, request.UUID)
	require.Equal(t, agent.NativeInputReceipt{SessionID: "native-session", InputID: request.UUID}, receipt)
	return a, sink, stdin, receipt
}

func lastClaudeStdinInputID(t *testing.T, stdin *bytes.Buffer) string {
	t.Helper()
	var request UserInputMessage
	require.NoError(t, json.Unmarshal(stdin.Bytes(), &request))
	stdin.Reset()
	require.NotEmpty(t, request.UUID)
	return request.UUID
}

func claudeInputAcknowledgement(sessionID, inputID string) []byte {
	return []byte(fmt.Sprintf(`{"type":"user","session_id":%q,"uuid":%q,"isReplay":true,"message":{"role":"user","content":"The release checklist"}}`, sessionID, inputID))
}

func TestClaudeInputReplyRequiresExactAcknowledgementAndRootResult(t *testing.T) {
	t.Parallel()
	a, sink, _, receipt := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement("previous-session", receipt.InputID))
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, "another-input"))
	require.Empty(t, sink.NativeInputTurnBindings())

	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	assert.Equal(t, map[agent.NativeInputReceipt]string{receipt: receipt.InputID}, sink.NativeInputTurnBindings())
	assert.Empty(t, sink.Messages(), "input echoes must not duplicate the accepted message")
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","message":{"role":"assistant","content":[{"type":"text","text":"The checklist passed."}]}}`))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"own-control","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-1","input":{"command":"printf ready"}}}`))
	controls := sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Equal(t, receipt.InputID, controls[0].NativeTurnID)

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
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestClaudeSteerEchoKeepsItsPositionWhileOutputIsDeferred(t *testing.T) {
	t.Parallel()
	a, sink, stdin, lead := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement(lead.SessionID, lead.InputID))
	queue := agent.OutputQueueFor(a.sink)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Run: func() error { return errors.New("storage unavailable") }})
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","uuid":"before","message":{"role":"assistant","content":[{"type":"text","text":"Before the steer."}]}}`))
	request := claudeTestSteerRequest("The release checklist", nil)
	steerReceipt, err := a.SteerInput(request)
	require.NoError(t, err)
	assert.Zero(t, steerReceipt)
	steer := agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: lastClaudeStdinInputID(t, stdin)}
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
			a, sink, stdin, lead := startTrackedClaudeInput(t)
			a.HandleOutput(claudeInputAcknowledgement(lead.SessionID, lead.InputID))
			request := claudeTestSteerRequest("The release checklist", nil)
			steerReceipt, err := a.SteerInput(request)
			require.NoError(t, err)
			assert.Zero(t, steerReceipt)
			steerInputID := lastClaudeStdinInputID(t, stdin)
			a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
			assert.Empty(t, sink.SteerResolutions(), "the result for the preceding run is still in flight")
			a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"Lead done."}`))
			sink.PersistErr = assert.AnError
			a.HandleOutput([]byte(boundary))
			assert.Empty(t, sink.SteerResolutions())
			sink.PersistErr = nil
			require.NoError(t, agent.FlushPendingOutput(a.sink))
			a.HandleOutput(claudeInputAcknowledgement(lead.SessionID, steerInputID))
			assert.Equal(t, []agent.SteerResolution{{
				InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeUncertain,
			}}, sink.SteerResolutions())
		})
	}
}

func TestClaudeInputReplyRetiresUnprovedWhenAcknowledgementNeverArrives(t *testing.T) {
	t.Parallel()
	a, sink, _, receipt := startTrackedClaudeInput(t)
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, "another-input"))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"An unproved reply"}`))
	assert.Empty(t, sink.NativeInputTurnBindings())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
	observed := sink.Messages()
	require.Len(t, observed, 1)
	assert.Empty(t, observed[0].NativeTurnID)
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	assert.Empty(t, sink.NativeInputTurnBindings(), "a delayed echo cannot revive a retired input")
}

func TestClaudeInputReplyReportsAnExplicitNativeAbort(t *testing.T) {
	t.Parallel()
	a, sink, _, receipt := startTrackedClaudeInput(t)
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
			a, sink, _, receipt := startTrackedClaudeInput(t)
			a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
			require.Contains(t, sink.NativeInputTurnBindings(), receipt)
			a.HandleOutput([]byte(boundary))
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
				receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			}, sink.NativeReplyRetirements())
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
	sink := &claudeReplyProofSink{}
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
	receipt, err := a.SendInput("The release checklist", nil)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.InputID)
	assert.Equal(t, receipt.InputID, sink.NativeInputTurnBindings()[receipt])
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, receipt.InputID, messages[0].NativeTurnID)
	assert.Equal(t, receipt.InputID, messages[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, messages[1].InputTurnOutcome)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "the write must leave a completed input idle")
	assert.Empty(t, sink.NativeReplyRetirements())
}

func cleanupClaudeReplyProcess(t *testing.T, a *Agent) {
	t.Helper()
	t.Cleanup(func() {
		a.SimulateExitForTest()
		a.Process.Stop()
	})
}

func TestClaudeSteerReturnsEmptyReceiptAndAllowsControlReplyWithoutAttemptID(t *testing.T) {
	t.Parallel()
	sink := &claudeReplyProofSink{}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	cleanupClaudeReplyProcess(t, a)
	a.sessionID = "native-session"
	a.turnActive = true
	sink.UpdateSessionID(a.sessionID)

	// Normal RowAtEcho steer must return an empty receipt so inputqueue holds it for echo.
	keyedReceipt, err := a.SteerInput(claudeTestSteerRequest("follow-up", nil))
	require.NoError(t, err)
	assert.Zero(t, keyedReceipt, "RowAtEcho steer must not return a confirmed receipt before echo")

	// Control-reply steers carry an empty AttemptID and must still succeed without tracking a steer resolution.
	controlReceipt, err := a.SteerInput(agent.SteerRequest{InputID: "control-feedback", Content: "denied reason"})
	require.NoError(t, err)
	assert.Zero(t, controlReceipt)
	assert.Contains(t, stdin.String(), "denied reason")
}

func TestClaudeInputReplyRoutesBindAndRetireThroughOutputQueueAndDetachesOnBindFailure(t *testing.T) {
	t.Parallel()
	sink := &claudeReplyProofSink{}
	a, stdin := newClaudeAgentWithStdin(agent.NewOrderedProviderServices(&agent.OutputQueue{}, agent.NewProviderServices(sink)))
	cleanupClaudeReplyProcess(t, a)
	a.sessionID = "native-session"
	sink.UpdateSessionID(a.sessionID)
	receipt, err := a.SendInput("The release checklist", nil)
	require.NoError(t, err)
	stdin.Reset()
	queue := agent.OutputQueueFor(a.sink)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Run: func() error { return errors.New("storage unavailable") }})

	// While OutputQueue is blocked, BindNativeInputTurn must be retained on the queue rather than bypassing it.
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	assert.Empty(t, sink.NativeInputTurnBindings(), "BindNativeInputTurn must wait in OutputQueue behind earlier writes")
	assert.Empty(t, sink.NativeReplyRetirements(), "a retained bind must not retire the input reply as unproved")

	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	assert.Equal(t, receipt.InputID, sink.NativeInputTurnBindings()[receipt])
}

func TestClaudeUnacknowledgedPeerResultStillEndsTurnAndFlushesHeldAPIErrorOnAck(t *testing.T) {
	t.Parallel()
	a, sink, _, receipt := startTrackedClaudeInput(t)

	// Hold an API error, then deliver the user input acknowledgement: the held API error must flush.
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"native-session","is_api_error_message":true,"message":{"role":"assistant","content":[{"type":"text","text":"rate limited"}]}}`))
	assert.Empty(t, sink.Messages(), "API error is held before next root frame")
	a.HandleOutput(claudeInputAcknowledgement(receipt.SessionID, receipt.InputID))
	require.Len(t, sink.Messages(), 1, "input acknowledgement must flush a held API error")

	// Start a second unacknowledged input and receive a peer result: it must not leave turnActive stuck.
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","result":"done"}`))
	_, err := a.SendInput("second", nil)
	require.NoError(t, err)
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","session_id":"native-session","origin":{"kind":"peer"},"result":"peer done"}`))
	active, _ := sink.LastTurnActive()
	assert.False(t, active, "unacknowledged inputReply must not suppress peer result turn-end cleanup")
}

func TestClaudePendingSteersSettleInDeterministicSendOrder(t *testing.T) {
	t.Parallel()
	sink := &claudeReplyProofSink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	cleanupClaudeReplyProcess(t, a)
	a.sessionID = "native-session"
	a.turnActive = true
	sink.UpdateSessionID(a.sessionID)

	expectedOrder := make([]string, 0, 12)
	for i := range 12 {
		id := fmt.Sprintf("steer-%02d", i)
		expectedOrder = append(expectedOrder, id)
		_, err := a.SteerInput(agent.SteerRequest{InputID: id, AttemptID: fmt.Sprintf("attempt-%02d", i), Content: id})
		require.NoError(t, err)
	}
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"next-session"}`))
	resolutions := sink.SteerResolutions()
	require.Len(t, resolutions, len(expectedOrder))
	gotOrder := make([]string, 0, len(resolutions))
	for _, r := range resolutions {
		gotOrder = append(gotOrder, r.InputID)
	}
	assert.Equal(t, expectedOrder, gotOrder)
}

func TestClaudeSteerWriteFailureDoesNotDuplicateReportOnConcurrentReset(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("native-session")
	a := &Agent{sink: agent.NewProviderServices(sink), sessionID: "native-session", turnActive: true}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent",
		Stdin: agenttest.NopStdin(claudeInputWriter(func([]byte) (int, error) {
			// Session resets while WriteStdin is in flight and fails.
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"next-session"}`))
			return 0, errors.New("broken pipe")
		})),
	})
	cleanupClaudeReplyProcess(t, a)

	_, err := a.SteerInput(claudeTestSteerRequest("racing steer", nil))
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.SteerResolutions(), "an unwritten steer whose write fails must not also emit SteerOutcomeUncertain")
}

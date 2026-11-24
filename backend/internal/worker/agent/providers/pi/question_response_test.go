package pi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piQuestionResponseFixture() (*Agent, *agenttest.ControlSink, *bytes.Buffer) {
	output := &bytes.Buffer{}
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.SetStdinForTest(agenttest.NopStdin(output))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"question","toolName":"ask_user_question","args":{"questions":[{"question":"Choose","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}`)))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"select","method":"select","title":"Choose","options":["1. A — first","2. B — second","3. Type something."]}`))
	return a, sink, output
}

func TestPiCustomQuestionAnswerBridgesNativeDialogs(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	answer := []byte(`{"type":"extension_ui_response","id":"select","value":"My custom answer"}`)
	original := append([]byte(nil), answer...)
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: answer}))
	assert.Equal(t, original, answer)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"select","value":"3. Type something."}`, output.String())
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	frames := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, frames, 2)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"input","value":"My custom answer"}`, frames[1])
	assert.Len(t, sink.PublishedControls(), 1, "the second native step must not ask the user to type the same answer again")
}

func TestPiQuestionResponsePreservesOrdinaryReplies(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		` {"type":"extension_ui_response","id":"select","value":"1. A — first","future":9007199254740993} `,
		`{"type":"extension_ui_response","id":"select","cancelled":true}`,
		`{"type":"extension_ui_response","id":"unrelated","value":"custom"}`,
		`{"type":"get_state","id":"command"}`,
	} {
		a, _, output := piQuestionResponseFixture()
		require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(reply)}))
		assert.Equal(t, reply+"\n", output.String())
	}
}

func TestPiCustomQuestionAnswerDoesNotSurviveToolCompletion(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"custom"}`)}))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_end","toolCallId":"question","toolName":"ask_user_question","result":{"content":[]}}`)))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"late","method":"input","title":"Choose\n\nType your answer:"}`))
	assert.Len(t, strings.Split(strings.TrimSpace(output.String()), "\n"), 1)
	assert.Len(t, sink.PublishedControls(), 2)
	assert.Empty(t, a.customQuestionAnswers)
}

func TestPiCustomQuestionAnswerCanRetryAfterWriteFailure(t *testing.T) {
	t.Parallel()
	a, _, output := piQuestionResponseFixture()
	a.SetStdinForTest(agenttest.FailingStdin{})
	reply := []byte(`{"type":"extension_ui_response","id":"select","value":"custom"}`)
	require.Error(t, a.SendRawInput(agent.RawInput{Data: reply}))
	a.SetStdinForTest(agenttest.NopStdin(output))
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: reply}))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:"}`))
	assert.Len(t, strings.Split(strings.TrimSpace(output.String()), "\n"), 2)
}

func TestPiFailedStopKeepsTheSelectInputQuestionExchange(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed idle dialog cancellation", true: "failed active abort"}[active], func(t *testing.T) {
			t.Parallel()
			a, sink, output := piQuestionResponseFixture()
			if active {
				a.Mu.Lock()
				a.currentTurnActive = true
				a.Mu.Unlock()
			}
			a.SetStdinForTest(agenttest.FailingStdin{})
			require.Error(t, a.Interrupt(agent.StopContext{}))
			assert.Empty(t, sink.CanceledControls())
			a.SetStdinForTest(agenttest.NopStdin(output))
			require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"My custom answer"}`)}))
			a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
			frames := strings.Split(strings.TrimSpace(output.String()), "\n")
			require.Len(t, frames, 2, "the failed stop must retain both native answer steps")
			assert.JSONEq(t, `{"type":"extension_ui_response","id":"select","value":"3. Type something."}`, frames[0])
			assert.JSONEq(t, `{"type":"extension_ui_response","id":"input","value":"My custom answer"}`, frames[1])
			assert.Len(t, sink.PublishedControls(), 1, "the user must not type the same answer again")
		})
	}
}

func TestPiFailedAbortKeepsACustomAnswerWaitingForItsInputDialog(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	a.Mu.Lock()
	a.currentTurnActive = true
	a.Mu.Unlock()
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"My custom answer"}`)}))
	a.SetStdinForTest(agenttest.FailingStdin{})
	require.Error(t, a.Interrupt(agent.StopContext{}))
	a.SetStdinForTest(agenttest.NopStdin(output))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	frames := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, frames, 2, "the retained text must answer the native input step")
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"input","value":"My custom answer"}`, frames[1])
	assert.Len(t, sink.PublishedControls(), 1)
}

func TestPiFailedQuestionAnswerKeepsTheDialogForInterrupt(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1. A — first", "custom"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			a, sink, output := piQuestionResponseFixture()
			a.SetStdinForTest(agenttest.FailingStdin{})
			reply := []byte(`{"type":"extension_ui_response","id":"select","value":"` + value + `"}`)
			require.Error(t, a.SendRawInput(agent.RawInput{Data: reply}))
			a.SetStdinForTest(agenttest.NopStdin(output))
			require.NoError(t, a.Interrupt(agent.StopContext{}))
			assert.JSONEq(t, `{"type":"extension_ui_response","id":"select","cancelled":true}`, output.String())
			assert.Equal(t, []string{"select"}, sink.CanceledControls())
		})
	}
}

type piBlockedQuestionWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w piBlockedQuestionWriter) Write([]byte) (int, error) {
	close(w.entered)
	<-w.release
	return 0, errors.New("write failed")
}
func (piBlockedQuestionWriter) Close() error { return nil }

func TestPiQuestionWriteFailureCannotRestoreClearedState(t *testing.T) {
	t.Parallel()
	a, _, _ := piQuestionResponseFixture()
	writer := piBlockedQuestionWriter{entered: make(chan struct{}), release: make(chan struct{})}
	a.SetStdinForTest(writer)
	done := make(chan error, 1)
	go func() {
		done <- a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"custom"}`)})
	}()
	<-writer.entered
	a.discardIncompletePiTools()
	close(writer.release)
	require.Error(t, <-done)
	assert.Empty(t, a.questionDialogs)
	assert.Empty(t, a.customQuestionAnswers)
}

func TestPiCustomQuestionAnswerSurvivesAFailedAutoResponse(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"My custom answer"}`)}))
	// Pi asks for the text. The automatic answer cannot reach stdin.
	a.SetStdinForTest(agenttest.FailingStdin{})
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	require.Len(t, sink.PublishedControls(), 2, "the failed answer must reach the user as a dialog")
	// The text the user typed must survive the failure. Pi asks again, and that
	// request carries the same answer rather than an empty one.
	a.SetStdinForTest(agenttest.NopStdin(output))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"retry","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	frames := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, frames, 2)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"retry","value":"My custom answer"}`, frames[1])
	assert.Len(t, sink.PublishedControls(), 2, "a retained answer must not ask the user to type it again")
}

func TestPiOldDialogCancellationPreservesAReusedRequestID(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		var response struct {
			Cancelled bool `json:"cancelled"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return 0, err
		}
		if !response.Cancelled {
			return output.Write(data)
		}
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"replacement-question","toolName":"ask_user_question","args":{"questions":[{"question":"Choose again","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}`))
		a.HandleOutput([]byte(`{"type":"extension_ui_request","id":"select","method":"select","title":"Choose again","options":["1. A — first","2. B — second","3. Type something."]}`))
		return len(data), nil
	})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, sink.CanceledControls(), "the old write must not withdraw the new card with the same ID")
	a.Mu.Lock()
	assert.Contains(t, a.openDialogs, "select")
	assert.Equal(t, "replacement-question", a.questionDialogs["select"].Key.ToolCallID)
	a.Mu.Unlock()
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"My new answer"}`)}))
	a.HandleOutput([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose again\n\nType your answer:","placeholder":""}`))
	frames := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, frames, 2)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"select","value":"3. Type something."}`, frames[0])
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"input","value":"My new answer"}`, frames[1])
	assert.Len(t, sink.PublishedControls(), 2)
}

func TestPiAgentEndWillRetryClearsQuestionStateWhileKeepingToolCallPrompts(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	// Also open a cross-turn subagent tool with a remembered spawn prompt.
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"sub-1","toolName":"Agent","args":{"description":"Inspect","prompt":"Inspect the repo."}}`)))
	// Store a custom answer for the pending question dialog.
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"select","value":"My stale custom answer"}`)}))
	// Re-publish a pending question dialog so both questionDialogs and customQuestionAnswers are populated.
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"select-2","method":"select","title":"Choose","options":["1. A — first","2. B — second","3. Type something."]}`))
	a.Mu.Lock()
	require.NotEmpty(t, a.questionDialogs)
	require.NotEmpty(t, a.customQuestionAnswers)
	a.Mu.Unlock()

	// agent_end with willRetry:true must clear stale question state while keeping toolCallPrompts.
	// Clear stdin around agent_end so background get_session_stats does not race the test buffer.
	a.SetStdinForTest(nil)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":true,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"transient"}]}`)))
	a.SetStdinForTest(agenttest.NopStdin(output))

	a.Mu.Lock()
	assert.Empty(t, a.questionDialogs, "willRetry agent_end must clear stale questionDialogs")
	assert.Empty(t, a.customQuestionAnswers, "willRetry agent_end must clear stale customQuestionAnswers")
	a.Mu.Unlock()

	// A follow-up input dialog must not automatically replay the stale custom answer from before the retry.
	writtenBefore := output.Len()
	controlsBefore := len(sink.PublishedControls())
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input-after-retry","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	assert.Equal(t, writtenBefore, output.Len(), "stale custom answer must not be replayed across retries")
	assert.Equal(t, controlsBefore+1, len(sink.PublishedControls()))

	// Cross-turn open tool prompts must still survive willRetry.
	assert.Equal(t, "Inspect the repo.", a.toolCallPrompts.Take("sub-1"))
}

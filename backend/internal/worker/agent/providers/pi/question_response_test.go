package pi

import (
	"bytes"
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
	require.NoError(t, a.SendRawInput(answer))
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
		require.NoError(t, a.SendRawInput([]byte(reply)))
		assert.Equal(t, reply+"\n", output.String())
	}
}

func TestPiCustomQuestionAnswerDoesNotSurviveToolCompletion(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"select","value":"custom"}`)))
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
	require.Error(t, a.SendRawInput(reply))
	a.SetStdinForTest(agenttest.NopStdin(output))
	require.NoError(t, a.SendRawInput(reply))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:"}`))
	assert.Len(t, strings.Split(strings.TrimSpace(output.String()), "\n"), 2)
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
		done <- a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"select","value":"custom"}`))
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
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"select","value":"My custom answer"}`)))
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

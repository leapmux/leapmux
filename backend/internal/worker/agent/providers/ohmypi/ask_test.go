package ohmypi

import (
	"encoding/json"
	"errors"
	"html"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dialog chains follow the installed omp 18.18.6 HTML form fallback.

// askCallMessage is the assistant message that calls `ask` with the given
// arguments.
func askCallMessage(toolCallID, arguments string) string {
	return `{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"` + toolCallID + `","name":"ask","arguments":` + arguments + `}],"stopReason":"toolUse"}}`
}

func askStart(toolCallID string) string {
	return `{"type":"tool_execution_start","toolCallId":"` + toolCallID + `","toolName":"ask","args":{}}`
}

func askEnd(toolCallID string) string {
	return `{"type":"tool_execution_end","toolCallId":"` + toolCallID + `","toolName":"ask","result":{"content":[{"type":"text","text":"User selected: x"}],"details":{}},"isError":false}`
}

func askAnswer(t *testing.T, requestID string, answers ...contracts.OhMyPiAskQuestionAnswer) []byte {
	t.Helper()
	encoded, err := json.Marshal(contracts.OhMyPiAskAnswer{
		Type: contracts.OhMyPiAskTypeAnswer, ID: requestID, Answers: answers,
	})
	require.NoError(t, err)
	return encoded
}

// dialogAnswers returns the value of each extension_ui_response the agent wrote,
// in order.
func (r *rig) dialogAnswers() []map[string]any {
	var out []map[string]any
	for _, command := range r.commandsOfType(contracts.OhMyPiEventExtensionUIResponse) {
		out = append(out, command.Payload)
	}
	return out
}

func (r *rig) waitForDialogAnswers(n int) []map[string]any {
	r.t.Helper()
	r.waitForCommand(contracts.OhMyPiEventExtensionUIResponse, n)
	return r.dialogAnswers()
}

const (
	singleAskArgs    = `{"i":"Choose the database","html":"<form><fieldset><legend>Which database?</legend><label><input type=radio name=db value=sqlite checked><strong>SQLite</strong> <span>Single file</span></label><label><input type=radio name=db value=postgres>PostgreSQL</label></fieldset></form>"}`
	singleAskDialog  = `{"type":"extension_ui_request","id":"158b2ba4525bfb89","method":"select","title":"Which database?","options":["SQLite Single file","PostgreSQL","Other (type your own)"]}`
	multiAskArgs     = `{"i":"Choose languages","html":"<form><fieldset><legend>Which languages?</legend><label><input type=checkbox name=langs value=go required>Go</label><label><input type=checkbox name=langs value=rust>Rust</label><label><input type=checkbox name=langs value=ts>TypeScript</label></fieldset></form>"}`
	multiAskDialog1  = `{"type":"extension_ui_request","id":"158b2ba48f5bfb8b","method":"select","title":"Which languages?","options":["Go","Rust","TypeScript","Other (type your own)"]}`
	multiAskDialog2  = `{"type":"extension_ui_request","id":"158b2ba48f5bfb8c","method":"select","title":"(1 selected) Which languages?","options":["Go","Rust","TypeScript","Other (type your own)","✔ Done selecting"]}`
	multiAskDialog3  = `{"type":"extension_ui_request","id":"158b2ba48f5bfb8d","method":"select","title":"(2 selected) Which languages?","options":["Go","Rust","TypeScript","Other (type your own)","✔ Done selecting"]}`
	twoAskArgs       = `{"i":"Choose project settings","html":"<form><fieldset><legend>Project name?</legend><label><input type=radio name=name value=alpha>alpha</label><label><input type=radio name=name value=beta>beta</label></fieldset><fieldset><legend>Color?</legend><label><input type=radio name=color value=red>red</label><label><input type=radio name=color value=blue>blue</label></fieldset></form>"}`
	twoAskDialog1    = `{"type":"extension_ui_request","id":"158b2ba4d71bfb8f","method":"select","title":"Project name?","options":["alpha","beta","Other (type your own)"]}`
	twoAskInput      = `{"type":"extension_ui_request","id":"158b2ba4d7dbfb90","method":"input","title":"Project name?"}`
	twoAskDialog2    = `{"type":"extension_ui_request","id":"158b2ba4d7dbfb91","method":"select","title":"Color?","options":["red","blue","Other (type your own)"]}`
	twoAskFirstReqID = "158b2ba4d71bfb8f"
)

func askChoiceFormArguments(t *testing.T, markup string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"i": "Choose the project settings", "html": markup})
	require.NoError(t, err)
	return string(raw)
}

func nativeAskDialog(t *testing.T, id, method, title string, options ...string) string {
	t.Helper()
	frame := map[string]any{"type": "extension_ui_request", "id": id, "method": method, "title": title}
	if method == "select" {
		frame["options"] = options
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return string(raw)
}

func TestAskPublishesOneRequestForTheWholeCall(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_3", twoAskArgs), twoAskDialog1)

	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Equal(t, twoAskFirstReqID, controls[0].RequestID, "the request takes the first dialog's id")
	var request contracts.OhMyPiAskRequest
	require.NoError(t, json.Unmarshal(controls[0].Payload, &request))
	assert.Equal(t, contracts.OhMyPiAskTypeRequest, request.Type)
	assert.Equal(t, twoAskFirstReqID, request.ID)
	require.Len(t, request.Questions, 2)
	assert.Equal(t, "Project name?", request.Questions[0].Question)
	assert.Equal(t, "color", request.Questions[1].ID)
	assert.Empty(t, r.dialogAnswers(), "omp waits on the first dialog until the reader answers")
}

func TestAskAnswersASingleSelect(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)

	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89",
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}}), agent.StopContext{}))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, "158b2ba4525bfb89", answers[0]["id"])
	assert.Equal(t, "SQLite Single file", answers[0]["value"], "the bridge answers the complete native dialog label")

	r.emit(askStart("call_1"), askEnd("call_1"))
	assert.Empty(t, r.sink.CanceledControls(), "an answered request is not withdrawn when the call ends")
}

func TestAskAnswersAMultiSelectWithDone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"0", "1"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)

	r.emit(askStart("call_2"), multiAskDialog2)
	r.waitForDialogAnswers(2)
	r.emit(multiAskDialog3)
	answers := r.waitForDialogAnswers(3)

	assert.Equal(t, []any{"Go", "Rust", "✔ Done selecting"}, []any{answers[0]["value"], answers[1]["value"], answers[2]["value"]})
	assert.Equal(t, []any{"158b2ba48f5bfb8b", "158b2ba48f5bfb8c", "158b2ba48f5bfb8d"}, []any{answers[0]["id"], answers[1]["id"], answers[2]["id"]})
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "the later dialogs of the chain never reach the reader")
}

func TestAskAnswersOtherThroughTheNativeInput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_3", twoAskArgs), twoAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID,
		contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"1"}},
		contracts.OhMyPiAskQuestionAnswer{ID: "name", Custom: "gamma (typed)"}), agent.StopContext{}))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_3"), twoAskInput)
	r.waitForDialogAnswers(2)
	r.emit(twoAskDialog2)
	answers := r.waitForDialogAnswers(3)

	assert.Equal(t, askOtherOption, answers[0]["value"])
	assert.Equal(t, "gamma (typed)", answers[1]["value"])
	assert.Equal(t, "blue", answers[2]["value"], "the answers are matched to questions by id, not by order")
}

func TestAskFinishesAMultiSelectInsideSeveralQuestionsWithNativeDone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := askChoiceFormArguments(t, `<form><fieldset><legend>Which languages?</legend><label><input type=checkbox name=langs required>Go</label><label><input type=checkbox name=langs>Rust</label></fieldset><fieldset><legend>Ship it?</legend><label><input type=radio name=ok>yes</label><label><input type=radio name=ok>no</label></fieldset></form>`)
	r.emit(askCallMessage("call_8", args), nativeAskDialog(t, "d1", "select", "Which languages?", "Go", "Rust", askOtherOption))
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "d1", contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"1"}, Custom: "and Zig"}, contracts.OhMyPiAskQuestionAnswer{ID: "ok", Selected: []string{"0"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_8"), nativeAskDialog(t, "d2", "select", "(1 selected) Which languages?", "Go", "Rust", askOtherOption, "✔ Done selecting"))
	r.waitForDialogAnswers(2)
	r.emit(nativeAskDialog(t, "d3", "input", "Which languages?"))
	r.waitForDialogAnswers(3)
	r.emit(nativeAskDialog(t, "d4", "select", "(2 selected) Which languages?", "Go", "Rust", askOtherOption, "✔ Done selecting"))
	r.waitForDialogAnswers(4)
	r.emit(nativeAskDialog(t, "d5", "select", "Ship it?", "yes", "no", askOtherOption))
	answers := r.waitForDialogAnswers(5)
	assert.Equal(t, []any{"Rust", askOtherOption, "and Zig", "✔ Done selecting", "yes"}, []any{answers[0]["value"], answers[1]["value"], answers[2]["value"], answers[3]["value"], answers[4]["value"]})
	assert.Equal(t, 1, r.sink.PublishedControlCount())
}

// Native input titles retain the whole question, including wide characters.
func TestAskAnswersTheWholeNativeInputTitle(t *testing.T) {
	t.Parallel()
	for _, question := range []string{"Which testing frameworks should the new project scaffold include?", "新しいプロジェクトの足場にどのテストフレームワークを含めるべきですか"} {
		t.Run(question, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			args := askChoiceFormArguments(t, `<form><fieldset><legend>`+html.EscapeString(question)+`</legend><label><input type=radio name=fw>vitest</label><label><input type=radio name=fw>playwright</label></fieldset></form>`)
			r.emit(askCallMessage("call_long", args), nativeAskDialog(t, "d1", "select", question, "vitest", "playwright", askOtherOption))
			require.NoError(t, r.agent.SendRawInput(askAnswer(t, "d1", contracts.OhMyPiAskQuestionAnswer{ID: "fw", Custom: "property testing"}), agent.StopContext{}))
			r.waitForDialogAnswers(1)
			r.emit(nativeAskDialog(t, "d2", "input", question))
			answers := r.waitForDialogAnswers(2)
			assert.Equal(t, []any{askOtherOption, "property testing"}, []any{answers[0]["value"], answers[1]["value"]})
			assert.Len(t, r.sink.PublishedControls(), 1)
		})
	}
}

func TestAskHandsOffAnInputOfAnotherQuestion(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_3", twoAskArgs), twoAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID, contracts.OhMyPiAskQuestionAnswer{ID: "name", Custom: "gamma"}, contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"0"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)
	unexpected := nativeAskDialog(t, "another", "input", "Another question…")
	r.emit(unexpected)
	require.Len(t, r.sink.PublishedControls(), 2)
	assert.JSONEq(t, unexpected, string(r.sink.PublishedControls()[1].Payload))
	assert.Len(t, r.dialogAnswers(), 1)
}

func TestAskAnswersASingleMultiSelectWithNativeCustomInput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b", contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"0"}, Custom: "Zig too"}), agent.StopContext{}))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_2"), multiAskDialog2)
	r.waitForDialogAnswers(2)
	r.emit(nativeAskDialog(t, "custom", "input", "Which languages?"))
	r.waitForDialogAnswers(3)
	r.emit(multiAskDialog3)
	answers := r.waitForDialogAnswers(4)
	assert.Equal(t, []any{"Go", askOtherOption, "Zig too", "✔ Done selecting"}, []any{answers[0]["value"], answers[1]["value"], answers[2]["value"], answers[3]["value"]})
}

func TestAskRefusesAnAnswerItCannotUse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		answers []contracts.OhMyPiAskQuestionAnswer
		message string
	}{
		{name: "a missing question", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"0"}}}, message: `nothing for question "color"`},
		{name: "a question stated twice", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"0"}}, {ID: "name", Selected: []string{"1"}}, {ID: "color", Selected: []string{"0"}}}, message: "twice"},
		{name: "an option the question does not offer", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"gamma"}}, {ID: "color", Selected: []string{"0"}}}, message: `no option "gamma"`},
		{name: "two options for a single-select", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"0", "1"}}, {ID: "color", Selected: []string{"0"}}}, message: "takes one option"},
		{name: "an empty answer", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name"}, {ID: "color", Selected: []string{"0"}}}, message: `"name" has no answer`},
		{name: "blank text", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Custom: "   "}, {ID: "color", Selected: []string{"0"}}}, message: `"name" has no answer`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.emit(askCallMessage("call_3", twoAskArgs), twoAskDialog1)
			err := r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID, tc.answers...), agent.StopContext{})
			assert.ErrorContains(t, err, tc.message)
			assert.Empty(t, r.dialogAnswers(), "nothing reaches omp, so the request stays open")

			// A correct answer still works afterwards.
			require.NoError(t, r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID,
				contracts.OhMyPiAskQuestionAnswer{ID: "name", Selected: []string{"0"}},
				contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"0"}}), agent.StopContext{}))
			r.waitForDialogAnswers(1)
		})
	}
}

func TestAskRefusesAnAnswerToNoOpenQuestion(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	err := r.agent.SendRawInput(askAnswer(t, "gone", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}}), agent.StopContext{})
	assert.ErrorContains(t, err, "no longer open")

	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	answer := askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}})
	require.NoError(t, r.agent.SendRawInput(answer, agent.StopContext{}))
	assert.ErrorContains(t, r.agent.SendRawInput(answer, agent.StopContext{}), "already answered")

	assert.Error(t, r.agent.SendRawInput([]byte(`{"type":"leapmux_ask_answer","answers":"not a list"}`), agent.StopContext{}), "a malformed answer fails")
}

func TestAskHandsAnUnexpectedDialogToTheReader(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"0", "1"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)

	// A later omp changed the title format: the bridge cannot tell what it asks.
	unexpected := `{"type":"extension_ui_request","id":"x2","method":"select","title":"[1] Which languages?","options":["Go","Rust","TypeScript","Done","Other (type your own)"]}`
	r.emit(askStart("call_2"), unexpected)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.JSONEq(t, unexpected, string(controls[1].Payload), "the reader answers it by hand")

	// Every later dialog of the call goes to the reader too.
	r.emit(multiAskDialog3)
	assert.Equal(t, 3, r.sink.PublishedControlCount())
	assert.Len(t, r.dialogAnswers(), 1)
}

func TestAskLeavesADialogThatDoesNotOpenTheCall(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), frameApprovalDialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.JSONEq(t, frameApprovalDialog, string(controls[0].Payload), "an approval is not the question")

	r.emit(singleAskDialog)
	controls = r.sink.PublishedControls()
	require.Len(t, controls, 2)
	var request contracts.OhMyPiAskRequest
	require.NoError(t, json.Unmarshal(controls[1].Payload, &request), "the call's own dialog still opens it")
	assert.Equal(t, contracts.OhMyPiAskTypeRequest, request.Type)
}

func TestAskCancellationEndsTheBridge(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	// The reader rejects the question: the answer is omp's own cancel.
	require.NoError(t, r.agent.SendRawInput([]byte(`{"type":"extension_ui_response","id":"158b2ba4525bfb89","cancelled":true}`), agent.StopContext{}))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, true, answers[0]["cancelled"])

	err := r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}}), agent.StopContext{})
	assert.ErrorContains(t, err, "no longer open")
	r.emit(askEnd("call_1"))
	assert.Empty(t, r.sink.CanceledControls(), "the reader closed the request already")
}

type partialDialogStdin struct{}

func (partialDialogStdin) Write([]byte) (int, error) {
	return 1, errors.New("the pipe stopped after one byte")
}
func (partialDialogStdin) Close() error { return nil }

func TestUncertainQuestionDeliveryKeepsItsClaim(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	r.agent.SetStdinForTest(partialDialogStdin{})
	answer := askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}})
	require.ErrorIs(t, r.agent.SendRawInput(answer, agent.StopContext{}), agent.ErrDeliveryUncertain)
	require.ErrorContains(t, r.agent.SendRawInput(answer, agent.StopContext{}), "already answered")
}

func TestAskWithdrawsAnUnansweredRequestWhenTheCallEnds(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog, askStart("call_1"), askEnd("call_1"))
	assert.Equal(t, []string{"158b2ba4525bfb89"}, r.sink.CanceledControls())
}

func TestAskWithdrawnByOmp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog,
		`{"type":"extension_ui_request","id":"c","method":"cancel","targetId":"158b2ba4525bfb89"}`)
	assert.Equal(t, []string{"158b2ba4525bfb89"}, r.sink.CanceledControls())
	err := r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}}), agent.StopContext{})
	assert.ErrorContains(t, err, "no longer open", "omp no longer waits for the answer")
}

func TestAskAPublishFailureCancelsTheDialog(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sink.PublicationError = errors.New("the store is gone")
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, true, answers[0]["cancelled"])
}

func TestAskIgnoresACallWithNoQuestions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", `{"html":"<form></form>"}`), singleAskDialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.JSONEq(t, singleAskDialog, string(controls[0].Payload), "with no call to follow, the dialog is published as it is")
}

func TestAskTheAgentEndForgetsTheCalls(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`, askCallMessage("call_1", singleAskArgs), frameAgentEnd, singleAskDialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.JSONEq(t, singleAskDialog, string(controls[0].Payload), "a finished run's call does not claim a later dialog")
}

// omp runs `ask` alone in its tool batch, so two calls of one message ask one
// after the other: the second call's first dialog opens a request of its own.
func TestAskFollowsTheCallsOfOneMessageInOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	message := `{"type":"message_end","message":{"role":"assistant","content":[` +
		`{"type":"toolCall","id":"call_a","name":"ask","arguments":` + singleAskArgs + `},` +
		`{"type":"toolCall","id":"call_b","name":"ask","arguments":` + twoAskArgs + `}],"stopReason":"toolUse"}}`
	r.emit(message, singleAskDialog)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89",
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)

	r.emit(askStart("call_a"), askEnd("call_a"), twoAskDialog1, askStart("call_b"))
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 2)
	var request contracts.OhMyPiAskRequest
	require.NoError(t, json.Unmarshal(controls[1].Payload, &request))
	assert.Equal(t, twoAskFirstReqID, request.ID)
	require.Len(t, request.Questions, 2, "the request holds the second call's own questions")
	assert.Equal(t, "name", request.Questions[0].ID)

	require.NoError(t, r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID,
		contracts.OhMyPiAskQuestionAnswer{ID: "name", Selected: []string{"1"}},
		contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"0"}}), agent.StopContext{}))
	answers := r.waitForDialogAnswers(2)
	assert.Equal(t, "beta", answers[1]["value"])
}

// The answer cannot reach omp. The request stays open, so the reader can answer
// again rather than read "already answered" for an answer omp never got.
func TestAskKeepsTheRequestOpenWhenTheAnswerCannotReachOmp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	answer := askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}})

	r.agent.SetStoppedForTest(true)
	assert.ErrorContains(t, r.agent.SendRawInput(answer, agent.StopContext{}), "answer the question")
	r.agent.SetStoppedForTest(false)

	require.NoError(t, r.agent.SendRawInput(answer, agent.StopContext{}))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, "SQLite Single file", answers[0]["value"])
}

// omp adds "Done" to a single multi-select question after its first toggle. A
// dialog without it cannot be finished by the plan, so the reader gets it.
func TestAskHandsOffASingleMultiSelectThatOffersNoDone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"0"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)

	noDone := `{"type":"extension_ui_request","id":"nd","method":"select","title":"(1 selected) Which languages?","options":["Go","Rust","TypeScript","Other (type your own)"]}`
	r.emit(askStart("call_2"), noDone)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.JSONEq(t, noDone, string(controls[1].Payload))
	assert.Len(t, r.dialogAnswers(), 1, "the bridge wrote no answer to it")
}

func TestAskHandsADialogAfterTheLastQuestionToTheReader(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89",
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"0"}}), agent.StopContext{}))
	r.waitForDialogAnswers(1)

	again := `{"type":"extension_ui_request","id":"x9","method":"select","title":"Which database?","options":["SQLite Single file","PostgreSQL","Other (type your own)"]}`
	r.emit(askStart("call_1"), again)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.JSONEq(t, again, string(controls[1].Payload), "every question is answered, so this dialog is not the call's")
	assert.Len(t, r.dialogAnswers(), 1)

	r.emit(askEnd("call_1"))
	assert.Empty(t, r.sink.CanceledControls(), "the call's own request was answered")
}

// omp waits on the call's first dialog until the reader answers it, so a select
// that arrives meanwhile is another dialog. It reaches the reader, and the
// call's own request still takes the answer.
func TestAskPublishesAnotherDialogThatArrivesBeforeTheAnswer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	other := `{"type":"extension_ui_request","id":"s7","method":"select","title":"Pick one","options":["a","b"]}`
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog, other)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.JSONEq(t, other, string(controls[1].Payload))

	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89",
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"1"}}), agent.StopContext{}))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, "158b2ba4525bfb89", answers[0]["id"])
	assert.Equal(t, "PostgreSQL", answers[0]["value"])
}

// A subagent's `ask` call is not the session's. Its dialog reaches the reader as
// it is.
func TestAskIgnoresASubagentsCall(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted, subagentEvent("Probe", askCallMessage("call_s", singleAskArgs)), singleAskDialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.JSONEq(t, singleAskDialog, string(controls[0].Payload))
}

func TestRememberAskCallsKeepsOnlyUsableAskCalls(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.rememberAskCalls(json.RawMessage(`[` +
		`{"type":"text","text":"Let me ask."},` +
		`{"type":"toolCall","id":"","name":"ask","arguments":` + singleAskArgs + `},` +
		`{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls"}},` +
		`{"type":"toolCall","id":"c2","name":"ask","arguments":{"html":false}},` +
		`{"type":"toolCall","id":"c3","name":"ask","arguments":` + singleAskArgs + `}]`))
	r.agent.rememberAskCalls(json.RawMessage(`"not a list"`))
	r.agent.rememberAskCalls(nil)

	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	require.Len(t, r.agent.asks.calls, 1)
	assert.Equal(t, "c3", r.agent.asks.calls[0].toolCallID)
	assert.Len(t, r.agent.asks.calls[0].questions, 1)
}

func TestAskPlanMatchesSelect(t *testing.T) {
	t.Parallel()
	plan := &askPlan{questions: []contracts.OhMyPiAskQuestion{{ID: "db", Question: "Which database?", Multi: true, AllowCustomText: true, Options: []contracts.OhMyPiAskOption{{Label: "SQLite"}, {Label: "PostgreSQL"}}}}}
	valid := dialogHeader{Method: "select", Title: "Which database?", Options: []string{"SQLite", "PostgreSQL", askOtherOption}}
	require.NoError(t, plan.matchesSelect(0, 0, valid))
	withDone := dialogHeader{Method: "select", Title: "(1 selected) Which database?", Options: []string{"SQLite", "PostgreSQL", askOtherOption, "✔ Done selecting"}}
	require.NoError(t, plan.matchesSelect(0, 1, withDone))
	for _, sample := range []struct {
		name         string
		index, count int
		head         dialogHeader
	}{
		{name: "an input", head: dialogHeader{Method: "input", Title: valid.Title, Options: valid.Options}},
		{name: "an index past the last question", index: 1, head: valid},
		{name: "a negative index", index: -1, head: valid},
		{name: "a negative count", count: -1, head: valid},
		{name: "another title", head: dialogHeader{Method: "select", Title: "Which DB?", Options: valid.Options}},
		{name: "another count", count: 1, head: valid},
		{name: "no custom option", head: dialogHeader{Method: "select", Title: valid.Title, Options: []string{"SQLite", "PostgreSQL", "Maybe"}}},
		{name: "added options", head: dialogHeader{Method: "select", Title: valid.Title, Options: []string{"SQLite", "PostgreSQL", "a", "b", askOtherOption}}},
		{name: "a missing option", head: dialogHeader{Method: "select", Title: valid.Title, Options: []string{"SQLite", askOtherOption}}},
		{name: "a renamed option", head: dialogHeader{Method: "select", Title: valid.Title, Options: []string{"MySQL", "PostgreSQL", askOtherOption}}},
		{name: "an unexpected suffix", head: dialogHeader{Method: "select", Title: valid.Title, Options: []string{"SQLite (different)", "PostgreSQL", askOtherOption}}},
		{name: "no options", head: dialogHeader{Method: "select", Title: valid.Title}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, plan.matchesSelect(sample.index, sample.count, sample.head), errAskShape)
		})
	}
}

func TestAskPlanTitle(t *testing.T) {
	t.Parallel()
	plan := &askPlan{questions: []contracts.OhMyPiAskQuestion{{ID: "a", Question: "A?", Multi: true}, {ID: "b", Question: "B?"}}}
	assert.Equal(t, "A?", plan.title(0, 0))
	assert.Equal(t, "(3 selected) A?", plan.title(0, 3))
	assert.Equal(t, "B?", plan.title(1, 0))
	assert.Equal(t, "B?", plan.title(1, 1), "a radio's preselection adds no count prefix")
}

func TestAskPlanDoesNotFlattenSelectedValuesIntoCustomText(t *testing.T) {
	t.Parallel()
	questions := []contracts.OhMyPiAskQuestion{{ID: "langs", Question: "Languages?", Multi: true, AllowCustomText: true, Options: []contracts.OhMyPiAskOption{{Value: "0", Label: "Go"}, {Value: "1", Label: "Rust"}}}}
	plan, err := newAskPlan(questions, []contracts.OhMyPiAskQuestionAnswer{{ID: "langs", Selected: []string{"0"}, Custom: " and Zig "}})
	require.NoError(t, err)
	for _, sample := range []struct {
		head  dialogHeader
		value string
	}{
		{dialogHeader{Method: "select", Title: "Languages?", Options: []string{"Go", "Rust", askOtherOption}}, "Go"},
		{dialogHeader{Method: "select", Title: "(1 selected) Languages?", Options: []string{"Go", "Rust", askOtherOption, "✔ Done selecting"}}, askOtherOption},
		{dialogHeader{Method: "input", Title: "Languages?"}, " and Zig "},
		{dialogHeader{Method: "select", Title: "(2 selected) Languages?", Options: []string{"Go", "Rust", askOtherOption, "✔ Done selecting"}}, "✔ Done selecting"},
	} {
		value, err := plan.respond(sample.head)
		require.NoError(t, err)
		assert.Equal(t, sample.value, value)
	}
	assert.Equal(t, 1, plan.questionIndex)
}

func TestAskPlanRequiresTheExactNativeCustomInputTitle(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"Another question", "Languages…", "Languages?\n\nOptions", "(1 selected) Languages?"} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			questions := []contracts.OhMyPiAskQuestion{{ID: "langs", Question: "Languages?", AllowCustomText: true, Options: []contracts.OhMyPiAskOption{{Value: "0", Label: "Go"}, {Value: "1", Label: "Rust"}}}}
			plan, err := newAskPlan(questions, []contracts.OhMyPiAskQuestionAnswer{{ID: "langs", Custom: "Zig"}})
			require.NoError(t, err)
			_, err = plan.respond(dialogHeader{Method: "select", Title: "Languages?", Options: []string{"Go", "Rust", askOtherOption}})
			require.NoError(t, err)
			_, err = plan.respond(dialogHeader{Method: "input", Title: title})
			require.ErrorIs(t, err, errAskShape)
			value, err := plan.respond(dialogHeader{Method: "input", Title: "Languages?"})
			require.NoError(t, err)
			assert.Equal(t, "Zig", value, "a refused shape does not consume the pending answer")
		})
	}
}

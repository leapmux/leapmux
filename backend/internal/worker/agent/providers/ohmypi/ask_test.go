package ohmypi

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dialog chains below are omp 18.2.11's own, from probe s2.

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
	singleAskArgs   = `{"questions":[{"id":"db","question":"Which database?","options":[{"label":"SQLite","description":"Single file"},{"label":"PostgreSQL"}],"recommended":0}]}`
	singleAskDialog = `{"type":"extension_ui_request","id":"158b2ba4525bfb89","method":"select","title":"Which database?","options":["SQLite (Recommended)","PostgreSQL","Other (type your own)"],"optionDetails":[{"description":"Single file"},{},{}]}`

	multiAskArgs    = `{"questions":[{"id":"langs","question":"Which languages?","multi":true,"options":[{"label":"Go"},{"label":"Rust"},{"label":"TypeScript"}]}]}`
	multiAskDialog1 = `{"type":"extension_ui_request","id":"158b2ba48f5bfb8b","method":"select","title":"Which languages?","options":["Go","Rust","TypeScript","Other (type your own)"]}`
	multiAskDialog2 = `{"type":"extension_ui_request","id":"158b2ba48f5bfb8c","method":"select","title":"(1 selected) Which languages?","options":["Go","Rust","TypeScript","✔ Done selecting","Other (type your own)"]}`
	multiAskDialog3 = `{"type":"extension_ui_request","id":"158b2ba48f5bfb8d","method":"select","title":"(2 selected) Which languages?","options":["Go","Rust","TypeScript","✔ Done selecting","Other (type your own)"]}`

	twoAskArgs       = `{"questions":[{"id":"name","question":"Project name?","options":[{"label":"alpha"},{"label":"beta"}]},{"id":"color","question":"Color?","options":[{"label":"red"},{"label":"blue"}]}]}`
	twoAskDialog1    = `{"type":"extension_ui_request","id":"158b2ba4d71bfb8f","method":"select","title":"Project name? (1/2)","options":["alpha","beta","Other (type your own)"]}`
	twoAskEditor     = `{"type":"extension_ui_request","id":"158b2ba4d7dbfb90","method":"editor","title":"Project name? (1/2)\n\n○ alpha\n○ beta\n◉ Other (type your own)\n\nEnter your response:","promptStyle":true}`
	twoAskDialog2    = `{"type":"extension_ui_request","id":"158b2ba4d7dbfb91","method":"select","title":"Color? (2/2)","options":["red","blue","Other (type your own)"]}`
	twoAskFirstReqID = "158b2ba4d71bfb8f"
)

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
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}})))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, "158b2ba4525bfb89", answers[0]["id"])
	assert.Equal(t, "SQLite (Recommended)", answers[0]["value"], "the bridge answers omp's own label, suffix included")

	r.emit(askStart("call_1"), askEnd("call_1"))
	assert.Empty(t, r.sink.CanceledControls(), "an answered request is not withdrawn when the call ends")
}

func TestAskAnswersAMultiSelectWithDone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"Go", "Rust"}})))
	r.waitForDialogAnswers(1)

	r.emit(askStart("call_2"), multiAskDialog2)
	r.waitForDialogAnswers(2)
	r.emit(multiAskDialog3)
	answers := r.waitForDialogAnswers(3)

	assert.Equal(t, []any{"Go", "Rust", "✔ Done selecting"}, []any{answers[0]["value"], answers[1]["value"], answers[2]["value"]})
	assert.Equal(t, []any{"158b2ba48f5bfb8b", "158b2ba48f5bfb8c", "158b2ba48f5bfb8d"}, []any{answers[0]["id"], answers[1]["id"], answers[2]["id"]})
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "the later dialogs of the chain never reach the reader")
}

func TestAskAnswersOtherThroughTheEditor(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_3", twoAskArgs), twoAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID,
		contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"blue"}},
		contracts.OhMyPiAskQuestionAnswer{ID: "name", Custom: "gamma (typed)"})))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_3"), twoAskEditor)
	r.waitForDialogAnswers(2)
	r.emit(twoAskDialog2)
	answers := r.waitForDialogAnswers(3)

	assert.Equal(t, askOtherOption, answers[0]["value"])
	assert.Equal(t, "gamma (typed)", answers[1]["value"])
	assert.Equal(t, "blue", answers[2]["value"], "the answers are matched to questions by id, not by order")
}

func TestAskFinishesAMultiSelectInsideSeveralQuestionsThroughOther(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := `{"questions":[{"id":"langs","question":"Which languages?","multi":true,"options":[{"label":"Go"},{"label":"Rust"}]},{"id":"ok","question":"Ship it?","options":[{"label":"yes"},{"label":"no"}]}]}`
	r.emit(askCallMessage("call_8", args),
		`{"type":"extension_ui_request","id":"d1","method":"select","title":"Which languages? (1/2)","options":["Go","Rust","Other (type your own)"]}`)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "d1",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"Rust"}, Custom: "and Zig"},
		contracts.OhMyPiAskQuestionAnswer{ID: "ok", Selected: []string{"yes"}})))
	r.waitForDialogAnswers(1)
	// omp offers no "Done" here: it expects its own arrow keys to move on.
	r.emit(askStart("call_8"),
		`{"type":"extension_ui_request","id":"d2","method":"select","title":"(1 selected) Which languages? (1/2)","options":["Go","Rust","Other (type your own)"]}`)
	r.waitForDialogAnswers(2)
	r.emit(`{"type":"extension_ui_request","id":"d3","method":"editor","title":"(1 selected) Which languages? (1/2)\n\n☐ Go\n☑ Rust\n◉ Other (type your own)\n\nEnter your response:","promptStyle":true}`)
	r.waitForDialogAnswers(3)
	r.emit(`{"type":"extension_ui_request","id":"d4","method":"select","title":"Ship it? (2/2)","options":["yes","no","Other (type your own)"]}`)
	answers := r.waitForDialogAnswers(4)

	values := []any{answers[0]["value"], answers[1]["value"], answers[2]["value"], answers[3]["value"]}
	assert.Equal(t, []any{"Rust", askOtherOption, "Rust; and Zig", "yes"}, values,
		"the editor carries the chosen labels, because omp shows the model only the typed text")
}

// omp shortens the question row of an editor title to the width of its
// terminal, less the panel's chrome (tools/ask.ts, clampLineToWidth). In RPC
// mode stdout is a pipe, so the width is 72 columns, and a long question
// arrives as a prefix with an ellipsis. The bridge still answers the editor
// itself: the user already answered every question in the form.
func TestAskAnswersAnEditorWhoseQuestionRowOmpShortened(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		question string
	}{
		{"a long question", "Which testing frameworks should the new project scaffold include?"},
		{"a question of wide characters", "新しいプロジェクトの足場にどのテストフレームワークを含めるべきですか"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			args := `{"questions":[{"id":"fw","question":"` + tc.question + `","multi":true,"options":[{"label":"vitest"},{"label":"playwright"}]},{"id":"ok","question":"Ship it?","options":[{"label":"yes"},{"label":"no"}]}]}`
			r.emit(askCallMessage("call_9", args),
				`{"type":"extension_ui_request","id":"d1","method":"select","title":"`+tc.question+` (1/2)","options":["vitest","playwright","Other (type your own)"]}`)
			require.NoError(t, r.agent.SendRawInput(askAnswer(t, "d1",
				contracts.OhMyPiAskQuestionAnswer{ID: "fw", Selected: []string{"vitest"}},
				contracts.OhMyPiAskQuestionAnswer{ID: "ok", Selected: []string{"yes"}})))
			r.waitForDialogAnswers(1)
			r.emit(askStart("call_9"),
				`{"type":"extension_ui_request","id":"d2","method":"select","title":"(1 selected) `+tc.question+` (1/2)","options":["vitest","playwright","Other (type your own)"]}`)
			r.waitForDialogAnswers(2)

			// The row as omp sends it: 30 characters, then the ellipsis. The CJK
			// question is 60 columns wide at that point, so both rows pass for a
			// shortened one.
			row := []rune("(1 selected) " + tc.question + " (1/2)")
			shortened := string(row[:30]) + "…"
			r.emit(`{"type":"extension_ui_request","id":"d3","method":"editor","title":"` + shortened + `\n\n☑ vitest\n☐ playwright\n◉ Other (type your own)\n\nEnter your response:","promptStyle":true}`)
			r.waitForDialogAnswers(3)
			r.emit(`{"type":"extension_ui_request","id":"d4","method":"select","title":"Ship it? (2/2)","options":["yes","no","Other (type your own)"]}`)
			answers := r.waitForDialogAnswers(4)

			assert.Equal(t, []any{"vitest", askOtherOption, "vitest", "yes"},
				[]any{answers[0]["value"], answers[1]["value"], answers[2]["value"], answers[3]["value"]})
			assert.Len(t, r.sink.PublishedControls(), 1, "the user answers the form once, and no dialog reaches the user")
		})
	}
}

// A row that ends with an ellipsis but differs from the question is another
// dialog, and the bridge hands it to the user.
func TestAskHandsOffAnEditorOfAnotherQuestion(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := `{"questions":[{"id":"langs","question":"Which languages?","multi":true,"options":[{"label":"Go"},{"label":"Rust"}]},{"id":"ok","question":"Ship it?","options":[{"label":"yes"},{"label":"no"}]}]}`
	r.emit(askCallMessage("call_8", args),
		`{"type":"extension_ui_request","id":"d1","method":"select","title":"Which languages? (1/2)","options":["Go","Rust","Other (type your own)"]}`)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "d1",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"Rust"}},
		contracts.OhMyPiAskQuestionAnswer{ID: "ok", Selected: []string{"yes"}})))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_8"),
		`{"type":"extension_ui_request","id":"d2","method":"select","title":"(1 selected) Which languages? (1/2)","options":["Go","Rust","Other (type your own)"]}`)
	r.waitForDialogAnswers(2)
	r.emit(`{"type":"extension_ui_request","id":"d3","method":"editor","title":"(1 selected) Which frameworks…\n\nEnter your response:","promptStyle":true}`)

	testutil.RequireEventually(t, func() bool { return len(r.sink.PublishedControls()) == 2 },
		"a dialog that does not continue the chain reaches the user")
	assert.Len(t, r.dialogAnswers(), 2, "the bridge wrote no answer to it")
}

func TestAskAnswersASingleMultiSelectWithTextThroughTheEditor(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"Go"}, Custom: "Zig too"})))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_2"), multiAskDialog2)
	r.waitForDialogAnswers(2)
	r.emit(`{"type":"extension_ui_request","id":"ed","method":"editor","title":"(1 selected) Which languages?\n\n☑ Go\n☐ Rust\n☐ TypeScript\n◉ Other (type your own)\n\nEnter your response:","promptStyle":true}`)
	answers := r.waitForDialogAnswers(3)
	assert.Equal(t, []any{"Go", askOtherOption, "Zig too"}, []any{answers[0]["value"], answers[1]["value"], answers[2]["value"]},
		"omp shows the model both the selection and the text of a single question")
}

func TestAskRefusesAnAnswerItCannotUse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		answers []contracts.OhMyPiAskQuestionAnswer
		message string
	}{
		{name: "a missing question", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"alpha"}}}, message: `nothing for question "color"`},
		{name: "a question stated twice", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"alpha"}}, {ID: "name", Selected: []string{"beta"}}, {ID: "color", Selected: []string{"red"}}}, message: "twice"},
		{name: "an option the question does not offer", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"gamma"}}, {ID: "color", Selected: []string{"red"}}}, message: `no option "gamma"`},
		{name: "two options for a single-select", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Selected: []string{"alpha", "beta"}}, {ID: "color", Selected: []string{"red"}}}, message: "takes one option"},
		{name: "an empty answer", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name"}, {ID: "color", Selected: []string{"red"}}}, message: `"name" has no answer`},
		{name: "blank text", answers: []contracts.OhMyPiAskQuestionAnswer{{ID: "name", Custom: "   "}, {ID: "color", Selected: []string{"red"}}}, message: `"name" has no answer`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.emit(askCallMessage("call_3", twoAskArgs), twoAskDialog1)
			err := r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID, tc.answers...))
			assert.ErrorContains(t, err, tc.message)
			assert.Empty(t, r.dialogAnswers(), "nothing reaches omp, so the request stays open")

			// A correct answer still works afterwards.
			require.NoError(t, r.agent.SendRawInput(askAnswer(t, twoAskFirstReqID,
				contracts.OhMyPiAskQuestionAnswer{ID: "name", Selected: []string{"alpha"}},
				contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"red"}})))
			r.waitForDialogAnswers(1)
		})
	}
}

func TestAskRefusesAnAnswerToNoOpenQuestion(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	err := r.agent.SendRawInput(askAnswer(t, "gone", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}}))
	assert.ErrorContains(t, err, "no longer open")

	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	answer := askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}})
	require.NoError(t, r.agent.SendRawInput(answer))
	assert.ErrorContains(t, r.agent.SendRawInput(answer), "already answered")

	assert.Error(t, r.agent.SendRawInput([]byte(`{"type":"leapmux_ask_answer","answers":"not a list"}`)), "a malformed answer fails")
}

func TestAskHandsAnUnexpectedDialogToTheReader(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"Go", "Rust"}})))
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
	// The reader rejects the question: the browser sends omp's own cancel.
	require.NoError(t, r.agent.SendRawInput([]byte(`{"type":"extension_ui_response","id":"158b2ba4525bfb89","cancelled":true}`)))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, true, answers[0]["cancelled"])

	err := r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}}))
	assert.ErrorContains(t, err, "no longer open")
	r.emit(askEnd("call_1"))
	assert.Empty(t, r.sink.CanceledControls(), "the reader closed the request already")
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
	err := r.agent.SendRawInput(askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}}))
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
	r.emit(askCallMessage("call_1", `{"questions":[]}`), singleAskDialog)
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
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}})))
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
		contracts.OhMyPiAskQuestionAnswer{ID: "name", Selected: []string{"beta"}},
		contracts.OhMyPiAskQuestionAnswer{ID: "color", Selected: []string{"red"}})))
	answers := r.waitForDialogAnswers(2)
	assert.Equal(t, "beta", answers[1]["value"])
}

// The answer cannot reach omp. The request stays open, so the reader can answer
// again rather than read "already answered" for an answer omp never got.
func TestAskKeepsTheRequestOpenWhenTheAnswerCannotReachOmp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_1", singleAskArgs), singleAskDialog)
	answer := askAnswer(t, "158b2ba4525bfb89", contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}})

	r.agent.SetStoppedForTest(true)
	assert.ErrorContains(t, r.agent.SendRawInput(answer), "answer the question")
	r.agent.SetStoppedForTest(false)

	require.NoError(t, r.agent.SendRawInput(answer))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, "SQLite (Recommended)", answers[0]["value"])
}

// omp adds "Done" to a single multi-select question after its first toggle. A
// dialog without it cannot be finished by the plan, so the reader gets it.
func TestAskHandsOffASingleMultiSelectThatOffersNoDone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	require.NoError(t, r.agent.SendRawInput(askAnswer(t, "158b2ba48f5bfb8b",
		contracts.OhMyPiAskQuestionAnswer{ID: "langs", Selected: []string{"Go"}})))
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
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"SQLite"}})))
	r.waitForDialogAnswers(1)

	again := `{"type":"extension_ui_request","id":"x9","method":"select","title":"Which database?","options":["SQLite (Recommended)","PostgreSQL","Other (type your own)"]}`
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
		contracts.OhMyPiAskQuestionAnswer{ID: "db", Selected: []string{"PostgreSQL"}})))
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
		`{"type":"toolCall","id":"c2","name":"ask","arguments":{"questions":"not a list"}},` +
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
	plan := &askPlan{questions: []contracts.OhMyPiAskQuestion{{
		ID: "db", Question: "Which database?",
		Options: []contracts.OhMyPiAskOption{{Label: "SQLite"}, {Label: "PostgreSQL"}},
	}}}
	valid := dialogHeader{Method: "select", Title: "Which database?", Options: []string{"SQLite (Recommended)", "PostgreSQL", askOtherOption}}
	require.NoError(t, plan.matchesSelect(0, 0, valid))
	withDone := dialogHeader{Method: "select", Title: "(1 selected) Which database?", Options: []string{"SQLite", "PostgreSQL", "✔ Done selecting", askOtherOption}}
	require.NoError(t, plan.matchesSelect(0, 1, withDone))

	for _, tc := range []struct {
		name    string
		index   int
		toggled int
		head    dialogHeader
	}{
		{name: "an editor", head: dialogHeader{Method: "editor", Title: "Which database?", Options: valid.Options}},
		{name: "an index past the last question", index: 1, head: valid},
		{name: "another title", head: dialogHeader{Method: "select", Title: "Which DB?", Options: valid.Options}},
		{name: "a toggle count the plan did not make", toggled: 1, head: valid},
		{name: "no Other option", head: dialogHeader{Method: "select", Title: "Which database?", Options: []string{"SQLite", "PostgreSQL", "Maybe"}}},
		{name: "three added options", head: dialogHeader{Method: "select", Title: "Which database?", Options: []string{"SQLite", "PostgreSQL", "a", "b", askOtherOption}}},
		{name: "a missing option", head: dialogHeader{Method: "select", Title: "Which database?", Options: []string{"SQLite", askOtherOption}}},
		{name: "a renamed option", head: dialogHeader{Method: "select", Title: "Which database?", Options: []string{"MySQL", "PostgreSQL", askOtherOption}}},
		{name: "no options at all", head: dialogHeader{Method: "select", Title: "Which database?"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, plan.matchesSelect(tc.index, tc.toggled, tc.head), errAskShape)
		})
	}
}

func TestAskPlanTitle(t *testing.T) {
	t.Parallel()
	one := &askPlan{questions: []contracts.OhMyPiAskQuestion{{ID: "a", Question: "Pick?"}}}
	assert.Equal(t, "Pick?", one.title(0, 0))
	assert.Equal(t, "(3 selected) Pick?", one.title(0, 3))
	two := &askPlan{questions: []contracts.OhMyPiAskQuestion{{ID: "a", Question: "A?"}, {ID: "b", Question: "B?"}}}
	assert.Equal(t, "B? (2/2)", two.title(1, 0))
	assert.Equal(t, "(1 selected) A? (1/2)", two.title(0, 1))
}

func TestJoinedAnswer(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Go, Rust; and Zig", joinedAnswer(contracts.OhMyPiAskQuestionAnswer{Selected: []string{"Go", "Rust"}, Custom: " and Zig "}))
	assert.Equal(t, "Go", joinedAnswer(contracts.OhMyPiAskQuestionAnswer{Selected: []string{"Go"}}))
	assert.Equal(t, "only text", joinedAnswer(contracts.OhMyPiAskQuestionAnswer{Custom: "only text"}))
	assert.Empty(t, joinedAnswer(contracts.OhMyPiAskQuestionAnswer{}))
}

func TestEditorContinuesQuestion(t *testing.T) {
	t.Parallel()
	const question = "(1 selected) Which languages? (1/2)"
	for _, tc := range []struct {
		name  string
		title string
		want  bool
	}{
		{"the whole row", question + "\n\n☑ Go\n\nEnter your response:", true},
		{"a shortened row", "(1 selected) Which lang…\n\n☑ Go", true},
		{"a bare ellipsis states no question", "…\n\n☑ Go", false},
		{"a row of another question", "(1 selected) Which frame…\n\n☑ Go", false},
		{"a longer row", question + " and more\n\n☑ Go", false},
		{"no blank line after the row", question + "\n☑ Go", false},
		{"no line after the row", question, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, editorContinuesQuestion(tc.title, question))
		})
	}
}

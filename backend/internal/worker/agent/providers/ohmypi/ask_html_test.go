package ohmypi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAskHTMLPublishesTheWholeForm(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := `{"i":"Choose the project settings","html":"<form><fieldset><legend>Choose a style</legend><label><input type=radio name=style value=alpha>Alpha</label><label><input type=radio name=style value=beta>Beta</label></fieldset><fieldset><legend>Choose sizes</legend><label><input type=checkbox name=sizes value=small>Small</label><label><input type=checkbox name=sizes value=large>Large</label></fieldset></form>"}`
	dialog := `{"type":"extension_ui_request","id":"html-first","method":"select","title":"Choose a style","options":["Alpha","Beta","Other (type your own)"]}`
	r.emit(askCallMessage("html-call", args), dialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	var request contracts.OhMyPiAskRequest
	require.NoError(t, json.Unmarshal(controls[0].Payload, &request))
	require.Len(t, request.Questions, 2, "the HTML call must publish both fields under its first dialog")
	assert.Equal(t, "style", request.Questions[0].ID)
	assert.Equal(t, "Choose a style", request.Questions[0].Question)
	assert.Equal(t, "Alpha", request.Questions[0].Options[0].Label)
	assert.Equal(t, "sizes", request.Questions[1].ID)
	assert.True(t, request.Questions[1].Multi)
	assert.Empty(t, r.dialogAnswers())
}

func TestAskHTMLAnswersTheNativeDialogChain(t *testing.T) {
	t.Parallel()
	questions := []contracts.OhMyPiAskQuestion{
		{ID: "style", AllowCustomText: true, Question: "Choose a style", Options: []contracts.OhMyPiAskOption{{Label: "Alpha"}, {Label: "Beta"}}},
		{ID: "sizes", AllowCustomText: true, AllowEmpty: true, Question: "Choose sizes", Multi: true, Options: []contracts.OhMyPiAskOption{{Label: "Small"}, {Label: "Large"}}},
	}
	plan, err := newAskPlan(questions, []contracts.OhMyPiAskQuestionAnswer{
		{ID: "style", Custom: "A custom style"},
		{ID: "sizes", Selected: []string{"Small"}, Custom: "Medium"},
	})
	require.NoError(t, err)
	for _, step := range []struct {
		frame string
		value string
	}{
		{`{"method":"select","title":"Choose a style","options":["Alpha","Beta","Other (type your own)"]}`, "Other (type your own)"},
		{`{"method":"input","title":"Choose a style"}`, "A custom style"},
		{`{"method":"select","title":"Choose sizes","options":["Small","Large","Other (type your own)","✔ Done selecting"]}`, "Small"},
		{`{"method":"select","title":"(1 selected) Choose sizes","options":["Small","Large","Other (type your own)","✔ Done selecting"]}`, "Other (type your own)"},
		{`{"method":"input","title":"Choose sizes"}`, "Medium"},
		{`{"method":"select","title":"(2 selected) Choose sizes","options":["Small","Large","Other (type your own)","✔ Done selecting"]}`, "✔ Done selecting"},
	} {
		var dialog dialogHeader
		require.NoError(t, json.Unmarshal([]byte(step.frame), &dialog))
		value, err := plan.respond(dialog)
		require.NoError(t, err, step.frame)
		assert.Equal(t, step.value, value)
	}
	assert.Equal(t, len(questions), plan.questionIndex)
}

func TestAskHTMLEmptyOptionalCheckboxAnswer(t *testing.T) {
	t.Parallel()
	question := contracts.OhMyPiAskQuestion{ID: "sizes", AllowCustomText: true, AllowEmpty: true, Question: "Choose sizes", Multi: true, Options: []contracts.OhMyPiAskOption{{Label: "Small"}, {Label: "Large"}}}
	plan, err := newAskPlan([]contracts.OhMyPiAskQuestion{question}, []contracts.OhMyPiAskQuestionAnswer{{ID: "sizes"}})
	require.NoError(t, err)
	var dialog dialogHeader
	require.NoError(t, json.Unmarshal([]byte(`{"method":"select","title":"Choose sizes","options":["Small","Large","Other (type your own)","✔ Done selecting"]}`), &dialog))
	value, err := plan.respond(dialog)
	require.NoError(t, err)
	assert.Equal(t, "✔ Done selecting", value)
	assert.Equal(t, 1, plan.questionIndex)
}

func TestAskHTMLKeepsNativeLabelsSeparateFromQuestionMetadata(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := `{"i":"Choose a style","html":"<form><fieldset><legend>Choose a style</legend><label><input type=radio name=style value=alpha><strong>Alpha</strong> <span>Fast</span> <pre>const x = 1;\nx</pre></label><label><input type=radio name=style value=beta>Beta</label></fieldset></form>"}`
	dialog := `{"type":"extension_ui_request","id":"rich-first","method":"select","title":"Choose a style","options":["Alpha Fast const x = 1; x","Beta","Other (type your own)"]}`
	r.emit(askCallMessage("rich-call", args), dialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	var request struct {
		Type      string `json:"type"`
		Questions []struct {
			Options []map[string]any `json:"options"`
		} `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(controls[0].Payload, &request))
	require.Equal(t, contracts.OhMyPiAskTypeRequest, request.Type)
	require.Len(t, request.Questions, 1)
	require.Len(t, request.Questions[0].Options, 2)
	assert.Equal(t, "0", request.Questions[0].Options[0]["value"])
	assert.Equal(t, "Alpha Fast const x = 1; x", request.Questions[0].Options[0]["label"], "keep the exact native protocol label")
	assert.Equal(t, "Alpha", request.Questions[0].Options[0]["displayLabel"])
	assert.Equal(t, "Fast", request.Questions[0].Options[0]["description"])
	assert.Equal(t, "```\nconst x = 1;\nx\n```", request.Questions[0].Options[0]["preview"])
	assert.NotContains(t, request.Questions[0].Options[0], "markup")
	assert.Equal(t, "1", request.Questions[0].Options[1]["value"])
	assert.Equal(t, "Beta", request.Questions[0].Options[1]["label"])
	assert.Empty(t, request.Questions[0].Options[1]["displayLabel"])
	assert.Empty(t, request.Questions[0].Options[1]["description"])
	assert.Empty(t, request.Questions[0].Options[1]["preview"])
	require.NoError(t, r.agent.SendRawInput(agent.RawInput{Data: askAnswer(t, "rich-first", contracts.OhMyPiAskQuestionAnswer{ID: "style", Selected: []string{"0"}})}))
	answers := r.waitForDialogAnswers(1)
	assert.Equal(t, "Alpha Fast const x = 1; x", answers[0]["value"], "the native response uses the complete dialog label")
}

func TestAskHTMLCanClearAPreselectedOptionalChoice(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := `{"i":"Choose languages","html":"<form><fieldset><legend>Languages</legend><label><input type=checkbox name=langs value=go checked>Go</label><label><input type=checkbox name=langs value=rust>Rust</label></fieldset></form>"}`
	dialog := `{"type":"extension_ui_request","id":"checked-first","method":"select","title":"(1 selected) Languages","options":["Go","Rust","Other (type your own)","✔ Done selecting"]}`
	r.emit(askCallMessage("checked-call", args), dialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	var request struct {
		Type      string           `json:"type"`
		Questions []map[string]any `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(controls[0].Payload, &request))
	require.Equal(t, contracts.OhMyPiAskTypeRequest, request.Type)
	require.Len(t, request.Questions, 1)
	assert.Equal(t, []any{"0"}, request.Questions[0]["initialSelections"])
	assert.Equal(t, true, request.Questions[0]["allowEmpty"])
	require.NoError(t, r.agent.SendRawInput(agent.RawInput{Data: askAnswer(t, "checked-first", contracts.OhMyPiAskQuestionAnswer{ID: "langs"})}))
	assert.Equal(t, "Go", r.waitForDialogAnswers(1)[0]["value"], "clear the native default before finishing")
	r.emit(`{"type":"extension_ui_request","id":"checked-next","method":"select","title":"Languages","options":["Go","Rust","Other (type your own)","✔ Done selecting"]}`)
	assert.Equal(t, "✔ Done selecting", r.waitForDialogAnswers(2)[1]["value"])
}

func TestAskHTMLNativeLabelRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, markup string
		labels       []string
	}{
		{"one label containing several controls", `<form><fieldset><legend>Targets</legend><label>Shared <input type=checkbox name=target value=a><input type=checkbox name=target value=b></label></fieldset></form>`, []string{"Shared", "Shared (2)"}},
		{"unlabeled control without a value", `<form><input type=checkbox name=enabled></form>`, []string{"enabled"}},
		{"empty aria label does not use title", `<form><input type=checkbox name=enabled value=yes aria-label="" title="Wrong"></form>`, []string{"yes"}},
		{"empty wrapping label uses detached label", `<form><label for=c>Detached</label><label><input id=c type=checkbox name=enabled value=yes></label></form>`, []string{"Detached"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			questions, err := readAskChoiceQuestions(tt.markup)
			require.NoError(t, err)
			require.Len(t, questions, 1)
			var labels []string
			for _, option := range questions[0].Options {
				labels = append(labels, option.Label)
			}
			assert.Equal(t, tt.labels, labels)
		})
	}
}

func TestAskHTMLChoiceBoundaries(t *testing.T) {
	t.Parallel()
	t.Run("first form excludes other controls", func(t *testing.T) {
		questions, err := readAskChoiceQuestions(`<input name=before><form><label><input type=checkbox name=inside checked>Inside</label></form><form><input name=after></form>`)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		assert.Equal(t, "inside", questions[0].ID)
		assert.Equal(t, []string{"0"}, questions[0].InitialSelections)
		assert.False(t, questions[0].AllowCustomText)
	})
	t.Run("disabled defaults retain native counts", func(t *testing.T) {
		questions, err := readAskChoiceQuestions(`<form><fieldset><legend>Languages</legend><label><input type=checkbox name=lang value=a checked disabled>Disabled</label><label><input type=checkbox name=lang value=b checked>Enabled</label></fieldset></form>`)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		assert.Equal(t, []string{"0", "1"}, questions[0].InitialSelections)
		require.Len(t, questions[0].Options, 1)
		assert.Equal(t, "1", questions[0].Options[0].Value)
		assert.Equal(t, "Enabled", questions[0].Options[0].Label)
	})
	t.Run("single select defaults to first enabled option", func(t *testing.T) {
		questions, err := readAskChoiceQuestions(`<form><label>Color<select name=color><option disabled>Red</option><option value=b>Blue</option></select></label></form>`)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		assert.Equal(t, "Color", questions[0].Question)
		assert.Equal(t, []string{"1"}, questions[0].InitialSelections)
		assert.False(t, questions[0].AllowCustomText)
		assert.False(t, questions[0].AllowEmpty)
	})
	t.Run("mixed and table forms keep native dialogs", func(t *testing.T) {
		for _, markup := range []string{
			`<form><input type=checkbox name=c><input name=text></form>`,
			`<form><table><tr><td><input type=checkbox name=c></td></tr></table></form>`,
		} {
			_, err := readAskChoiceQuestions(markup)
			require.Error(t, err)
		}
	})
	t.Run("empty form has no questions", func(t *testing.T) {
		questions, err := readAskChoiceQuestions("")
		require.NoError(t, err)
		assert.Empty(t, questions)
	})
	t.Run("deep form returns an error", func(t *testing.T) {
		_, err := readAskChoiceQuestions(strings.Repeat("<div>", maxAskFormDepth+1) + `<input type=checkbox name=c>` + strings.Repeat("</div>", maxAskFormDepth+1))
		require.ErrorContains(t, err, "depth")
	})
}

func TestAskHTMLChoicePresentationAndDisambiguation(t *testing.T) {
	t.Parallel()

	t.Run("plain repeated labels omit redundant displayLabel across suffixes", func(t *testing.T) {
		t.Parallel()
		markup := `<form><fieldset><legend>Pick</legend>` +
			`<label><input type=radio name=choice value=a>Alpha</label>` +
			`<label><input type=radio name=choice value=b>Alpha</label>` +
			`<label><input type=radio name=choice value=c>Alpha</label>` +
			`</fieldset></form>`
		questions, err := readAskChoiceQuestions(markup)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		require.Len(t, questions[0].Options, 3)
		assert.Equal(t, []contracts.OhMyPiAskOption{
			{Value: "0", Label: "Alpha"},
			{Value: "1", Label: "Alpha (2)"},
			{Value: "2", Label: "Alpha (3)"},
		}, questions[0].Options)
	})

	t.Run("rich repeated labels propagate disambiguation suffixes to displayLabel", func(t *testing.T) {
		t.Parallel()
		markup := `<form><fieldset><legend>Pick</legend>` +
			`<label><input type=radio name=choice value=a><strong>Alpha</strong> <span>Fast</span></label>` +
			`<label><input type=radio name=choice value=b><strong>Alpha</strong> <span>Fast</span></label>` +
			`<label><input type=radio name=choice value=c><strong>Alpha</strong> <span>Fast</span></label>` +
			`</fieldset></form>`
		questions, err := readAskChoiceQuestions(markup)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		require.Len(t, questions[0].Options, 3)
		assert.Equal(t, []contracts.OhMyPiAskOption{
			{Value: "0", Label: "Alpha Fast", DisplayLabel: "Alpha", Description: "Fast"},
			{Value: "1", Label: "Alpha Fast (2)", DisplayLabel: "Alpha (2)", Description: "Fast"},
			{Value: "2", Label: "Alpha Fast (3)", DisplayLabel: "Alpha (3)", Description: "Fast"},
		}, questions[0].Options)
	})

	t.Run("detached label for id extracts structured presentation", func(t *testing.T) {
		t.Parallel()
		markup := `<form><fieldset><legend>Pick</legend>` +
			`<label for=opt1><strong>Detached</strong> <span>Details</span> <pre>x()</pre></label>` +
			`<input id=opt1 type=radio name=choice value=a>` +
			`</fieldset></form>`
		questions, err := readAskChoiceQuestions(markup)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		require.Len(t, questions[0].Options, 1)
		assert.Equal(t, contracts.OhMyPiAskOption{
			Value:        "0",
			Label:        "Detached Details x()",
			DisplayLabel: "Detached",
			Description:  "Details",
			Preview:      "```\nx()\n```",
		}, questions[0].Options[0])
	})

	t.Run("control without labelNode has empty presentation metadata", func(t *testing.T) {
		t.Parallel()
		markup := `<form><fieldset><legend>Pick</legend>` +
			`<input type=radio name=choice value=a aria-label="Aria">` +
			`<input type=radio name=choice value=b>` +
			`</fieldset></form>`
		questions, err := readAskChoiceQuestions(markup)
		require.NoError(t, err)
		require.Len(t, questions, 1)
		require.Len(t, questions[0].Options, 2)
		assert.Equal(t, []contracts.OhMyPiAskOption{
			{Value: "0", Label: "Aria"},
			{Value: "1", Label: "b"},
		}, questions[0].Options)
	})
}

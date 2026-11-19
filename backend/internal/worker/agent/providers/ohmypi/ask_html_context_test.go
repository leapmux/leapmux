package ohmypi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAskHTMLMatchesNativeQuestionContexts(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/ask_form_context_conformance.json")
	require.NoError(t, err)
	type question struct {
		ID       string `json:"id"`
		Question string `json:"question"`
		Context  string `json:"context"`
	}
	var corpus struct {
		Cases []struct {
			Name      string     `json:"name"`
			HTML      string     `json:"html"`
			Questions []question `json:"questions"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Cases)
	for _, example := range corpus.Cases {
		t.Run(example.Name, func(t *testing.T) {
			t.Parallel()
			actual, err := readAskChoiceQuestions(example.HTML)
			require.NoError(t, err)
			wire, err := json.Marshal(actual)
			require.NoError(t, err)
			var questions []question
			require.NoError(t, json.Unmarshal(wire, &questions))
			assert.Equal(t, example.Questions, questions)
		})
	}
}

func TestAskHTMLMatchesOnlyItsCompleteNativeContextTitle(t *testing.T) {
	t.Parallel()
	var questions []contracts.OhMyPiAskQuestion
	require.NoError(t, json.Unmarshal([]byte(`[{"id":"target","question":"Choose target","context":"Deployment policy","options":[{"value":"a","label":"Alpha"},{"value":"b","label":"Beta"}],"allowCustomText":true,"allowEmpty":false}]`), &questions))
	head := dialogHeader{Method: contracts.OhMyPiDialogMethodSelect, Title: "Deployment policy\n\nChoose target", Options: []string{"Alpha", "Beta", "Other (type your own)"}}
	assert.True(t, opensAsk(questions, head))
	head.Title = "Another policy\n\nChoose target"
	assert.False(t, opensAsk(questions, head))
	head.Title = "Choose target"
	assert.False(t, opensAsk(questions, head))
	plan, err := newAskPlan(questions, []contracts.OhMyPiAskQuestionAnswer{{ID: "target", Custom: "Staging"}})
	require.NoError(t, err)
	head.Title = "Deployment policy\n\nChoose target"
	value, err := plan.respond(head)
	require.NoError(t, err)
	assert.Equal(t, "Other (type your own)", value)
	value, err = plan.respond(dialogHeader{Method: contracts.OhMyPiDialogMethodInput, Title: head.Title})
	require.NoError(t, err)
	assert.Equal(t, "Staging", value)
}

func TestAskHTMLContextPrecedesTheNativeSelectionCount(t *testing.T) {
	t.Parallel()
	var questions []contracts.OhMyPiAskQuestion
	require.NoError(t, json.Unmarshal([]byte(`[{"id":"target","question":"Choose target","context":"Deployment policy","multi":true,"initialSelections":["0"],"options":[{"value":"0","label":"Alpha"},{"value":"1","label":"Beta"}],"allowCustomText":true,"allowEmpty":true}]`), &questions))
	plan, err := newAskPlan(questions, []contracts.OhMyPiAskQuestionAnswer{{ID: "target", Selected: []string{"0"}, Custom: "Staging"}})
	require.NoError(t, err)
	options := []string{"Alpha", "Beta", "Other (type your own)", "✔ Done selecting"}
	_, err = plan.respond(dialogHeader{Method: contracts.OhMyPiDialogMethodSelect, Title: "(1 selected) Deployment policy\n\nChoose target", Options: options})
	require.ErrorIs(t, err, errAskShape)
	value, err := plan.respond(dialogHeader{Method: contracts.OhMyPiDialogMethodSelect, Title: "Deployment policy\n\n(1 selected) Choose target", Options: options})
	require.NoError(t, err)
	assert.Equal(t, "Other (type your own)", value)
	value, err = plan.respond(dialogHeader{Method: contracts.OhMyPiDialogMethodInput, Title: "Deployment policy\n\nChoose target"})
	require.NoError(t, err)
	assert.Equal(t, "Staging", value)
	value, err = plan.respond(dialogHeader{Method: contracts.OhMyPiDialogMethodSelect, Title: "Deployment policy\n\n(2 selected) Choose target", Options: options})
	require.NoError(t, err)
	assert.Equal(t, "✔ Done selecting", value)
}

func TestAskHTMLRefusesAmplifiedChoiceMetadata(t *testing.T) {
	t.Parallel()
	const controls = 64
	label := strings.Repeat("x", contracts.MaxMessageSize/controls)
	markup := "<form><label>" + label + strings.Repeat(`<input type=checkbox name=target value=x>`, controls) + "</label></form>"
	questions, err := readAskChoiceQuestions(markup)
	require.ErrorContains(t, err, "message limit")
	assert.Nil(t, questions, "an oversized bridge must keep the native dialog route")
}

func TestAskHTMLRefusesInputBeyondTheWorkerMessageLimit(t *testing.T) {
	t.Parallel()
	questions, err := readAskChoiceQuestions(strings.Repeat(" ", contracts.MaxMessageSize+1))
	require.ErrorContains(t, err, "message limit")
	assert.Nil(t, questions)
}

func TestAskHTMLKeepsTheNativeRouteWhenEncodedContextExceedsTheMessageLimit(t *testing.T) {
	t.Parallel()
	rig := newRig(t)
	context := strings.Repeat("<", contracts.MaxMessageSize/6+1)
	call := &askCall{toolCallID: "large-ask", questions: []contracts.OhMyPiAskQuestion{{ID: "target", Question: "Choose", Context: context, Options: []contracts.OhMyPiAskOption{{Value: "0", Label: "Alpha"}}}}}
	rig.agent.Mu.Lock()
	rig.agent.asks.calls = []*askCall{call}
	rig.agent.Mu.Unlock()
	head := dialogHeader{ID: "native-large-select", Method: contracts.OhMyPiDialogMethodSelect, Title: context + "\n\nChoose", Options: []string{"Alpha"}}
	require.False(t, rig.agent.routeAskDialog(head), "JSON escaping cannot turn a native dialog into an oversized bridge request")
	rig.agent.Mu.Lock()
	defer rig.agent.Mu.Unlock()
	assert.True(t, call.handOff)
	assert.Nil(t, rig.agent.asks.byRequest(head.ID), "the bridge must not claim a later cancellation of the native fallback")
}

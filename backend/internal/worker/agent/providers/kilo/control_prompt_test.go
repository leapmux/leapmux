package kilo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Kilo runs OpenCode's daemon, so it reads and answers its control requests
// through the family's own methods. These cases state what Kilo's plugin
// promised in the browser, against kiloProvider itself.

func TestReadControlRequest_RecognizesAQuestionAndNothingElse(t *testing.T) {
	assert.NotNil(t, agenttest.ControlPromptOf(kiloProvider{}, []byte(`{"type":"question.asked","properties":{"questions":[]}}`)).GetQuestion())
	assert.Nil(t, agenttest.ControlPromptOf(kiloProvider{}, []byte(`{"method":"requestPermission","params":{"toolCall":{"toolCallId":"tc-1"}}}`)).GetQuestion())
	assert.Nil(t, agenttest.ControlPromptOf(kiloProvider{}, []byte(`{}`)).GetQuestion())
}

func TestReadControlRequest_AShellPermissionStatesItsCommand(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiloProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"command","kind":"execute","title":"Run command",
		"rawInput":{"command":"printf \"first line\\n\"","timeout":0}}}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Run command", permission.GetTitle())
	assert.Equal(t, `printf "first line\n"`, permission.GetCommand())
	assert.JSONEq(t, `{"command":"printf \"first line\\n\"","timeout":0}`, permission.GetInputJson())
}

// Kilo's daemon answers a request with no option list with its own two ids.
func TestReadControlRequest_ARequestWithNoOptionsOffersTheDaemonPair(t *testing.T) {
	assert.Equal(t, []string{"once", "reject"}, agenttest.PromptPermissionOptionIDs(agenttest.ControlPromptOf(kiloProvider{}, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"}}}`))))
}

func TestAnswerControlRequest_SelectsTheChosenOption(t *testing.T) {
	payload := `{"method":"session/request_permission","params":{"options":[{"optionId":"once","name":"Allow once","kind":"allow_once"},{"optionId":"reject","name":"Reject","kind":"reject_once"}]}}`
	got, err := agenttest.AnswerControl(kiloProvider{}, []byte(payload), "5", agenttest.PermissionDecision("once", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"5","result":{"outcome":{"outcome":"selected","optionId":"once"}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(kiloProvider{}, []byte(payload), "request", agenttest.PermissionDecision("reject", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"request","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`, string(got.Content))
}

// The composer refuses with the agent's own reject option, and the reason
// follows as a message, whether the runtime or the daemon pair offers it.
func TestAnswerControlRequest_ARefusalSelectsARejectOptionAndTheReasonFollows(t *testing.T) {
	const options = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s",
		"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}},
		"options":[{"optionId":"allow-1","name":"Allow","kind":"allow_once"},{"optionId":"reject-1","name":"Reject","kind":"reject_once"}]}}`
	const optionLess = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s",
		"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}}}}`
	for _, tc := range []struct{ payload, reject, allow string }{
		{options, "reject-1", "allow-1"},
		{optionLess, "reject", "once"},
	} {
		got, err := agenttest.AnswerControl(kiloProvider{}, []byte(tc.payload), "jsonrpc:3", agenttest.PermissionDecision(tc.reject, "use the other file"))
		require.NoError(t, err)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"`+tc.reject+`"}}}`, string(got.Content))
		assert.Equal(t, "use the other file", got.FollowUp)

		got, err = agenttest.AnswerControl(kiloProvider{}, []byte(tc.payload), "jsonrpc:3", agenttest.PermissionDecision(tc.allow, ""))
		require.NoError(t, err)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"`+tc.allow+`"}}}`, string(got.Content))
		assert.Empty(t, got.FollowUp)
	}
}

func TestAnswerControlRequest_SendsOrderedAnswerListsForEachQuestion(t *testing.T) {
	payload := `{"type":"question.asked","properties":{"questions":[
		{"question":"Action?","options":[{"label":"Build"}]},{"question":"Env?","options":[{"label":"Dev"}]}]}}`
	got, err := agenttest.AnswerControl(kiloProvider{}, []byte(payload), "que_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build"}}, &leapmuxv1.QuestionAnswer{CustomText: "Dev"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"que_1","result":{"answers":[["Build"],["Dev"]]}}`, string(got.Content))
}

// A multiple-choice answer of two options and typed text chose all three,
// and the typed text is one more value of the answer.
func TestAnswerControlRequest_SendsTypedTextAsOneMoreValueOfAMultipleChoice(t *testing.T) {
	payload := `{"type":"question.asked","properties":{"questions":[
		{"question":"Which steps?","multiple":true,"options":[{"label":"Build"},{"label":"Test"}]}]}}`
	got, err := agenttest.AnswerControl(kiloProvider{}, []byte(payload), "que_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build", "Test"}, CustomText: "Lint"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"que_1","result":{"answers":[["Build","Test","Lint"]]}}`, string(got.Content))
}

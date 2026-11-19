package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// conformanceRequestID is the stored key of the request that the shared
// control-response suites answer, as the Worker keys a JSON-RPC request with
// the id 7.
const conformanceRequestID = "jsonrpc:7"

// conformanceAnswer is the answer that the shared control-response suites
// resolve. It addresses conformanceRequestID as the Worker's own answers do
// (agent.JSONRPCResult).
func conformanceAnswer() agent.ControlAnswer {
	return agent.ControlAnswer{Content: agent.JSONRPCResult(conformanceRequestID, map[string]any{"decision": "accept"})}
}

// AssertWithholdsTheResponseForAMalformedRequest pins what provider does with a
// response whose request is not valid JSON. The provider cannot validate the
// native id against that request, so it withholds the response, and it states
// the shared reason for a request that it cannot read. It keeps the bytes, which
// a recovery reads.
//
// No Worker path resolves this context today: the Worker's AnswerControl RPC
// refuses a request that is not a JSON object before the provider answers it,
// and the delivery refuses such a request before the provider resolves the
// answer (agent.StoredControlRequestAgrees). The suite pins the provider's own
// guard, so a later caller that skips those checks still sends nothing that
// the provider cannot match to its request.
func AssertWithholdsTheResponseForAMalformedRequest(t *testing.T, provider agent.Provider) {
	t.Helper()
	answer := conformanceAnswer()
	res := ResolveAnswer(provider, []byte(`not json`), conformanceRequestID, answer)
	assert.Equal(t, answer.Content, res.Content)
	assert.True(t, res.Withhold)
	assert.EqualError(t, res.Refusal(), agent.RefusalUnreadableRequest)
}

// AssertPreservesTheResponseWithoutARequest pins that an absent request does not
// change the response bytes that provider forwards.
//
// No Worker path resolves this context today. An agent restart can delete the
// request row after the Worker's AnswerControl RPC reads it, and the delivery
// then reads the row again and refuses the answer before the provider resolves
// it, because the request is gone. The suite pins the provider's own handling,
// so a later caller that resolves such an answer still forwards its bytes
// unchanged.
func AssertPreservesTheResponseWithoutARequest(t *testing.T, provider agent.Provider) {
	t.Helper()
	answer := conformanceAnswer()
	res := ResolveAnswer(provider, nil, conformanceRequestID, answer)
	assert.Equal(t, answer.Content, res.Content)
}

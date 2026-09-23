package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// AssertWithholdsTheResponseForAMalformedRequest pins what provider does with a
// response whose request is not valid JSON. The provider cannot validate the
// native id against that request, so it withholds the response. It keeps the
// bytes, which a recovery reads.
func AssertWithholdsTheResponseForAMalformedRequest(t *testing.T, provider agent.Provider) {
	t.Helper()
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	res := provider.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`not json`),
		ResponseContent: content,
	})
	assert.Equal(t, content, res.Content)
	assert.True(t, res.Withhold)
}

// AssertPreservesTheResponseWithoutARequest pins that an absent request does not
// change the response bytes that provider forwards.
func AssertPreservesTheResponseWithoutARequest(t *testing.T, provider agent.Provider) {
	t.Helper()
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	res := provider.ResolveControlResponse(agent.ControlResponseContext{ResponseContent: content})
	assert.Equal(t, content, res.Content)
}

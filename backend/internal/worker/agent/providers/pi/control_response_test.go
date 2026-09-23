package pi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveControlResponse_PiPreservesTheResponse(t *testing.T) {
	t.Parallel()

	confirmed := true
	response, err := json.Marshal(map[string]interface{}{"confirmed": confirmed})
	require.NoError(t, err)

	res := piProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`{"method":"confirm"}`),
		ResponseContent: response,
	})

	assert.Equal(t, response, res.Content)
}

func TestPiResolveControlResponse_PreservesButWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, piProvider{})
}

func TestPiResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, piProvider{})
}

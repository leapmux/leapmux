package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandAcceptsNoopOnlyForCompaction(t *testing.T) {
	a, _ := testAgent(t)
	peer := &museTestPeer{agent: a, reply: func(_ string, params json.RawMessage) agenttest.RPCReply {
		var payload struct {
			CommandID string `json:"commandId"`
		}
		require.NoError(t, json.Unmarshal(params, &payload))
		return agenttest.RPCReply{Result: json.RawMessage(`{"commandId":"` + payload.CommandID + `","status":"noop"}`)}
	}}
	a.SetStdinForTest(peer)
	_, err := a.command(methodSetModel, map[string]any{"sessionId": "session"}, time.Second, nil)
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	_, err = a.command(methodSessionCompact, map[string]any{"sessionId": "session"}, time.Second, nil)
	require.NoError(t, err)
}

func TestCommandParamsUsesNativeUUIDv7(t *testing.T) {
	t.Parallel()

	params, id, err := commandParams(nil)
	require.NoError(t, err)
	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version())
	assert.Equal(t, uuid.RFC4122, parsed.Variant())
	assert.Equal(t, id, params["commandId"])
}

func TestCommandParamsPreservesTheCaller(t *testing.T) {
	t.Parallel()

	input := map[string]any{"sessionId": "native-session", "input": "native input"}
	params, _, err := commandParams(input)
	require.NoError(t, err)
	assert.Equal(t, "native-session", params["sessionId"])
	assert.Equal(t, "native input", params["input"])
	assert.NotContains(t, input, "commandId")
	params["sessionId"] = "replacement"
	assert.Equal(t, "native-session", input["sessionId"])
}

type museTestPeer struct {
	agent *Agent
	reply func(string, json.RawMessage) agenttest.RPCReply
}

func (p *museTestPeer) Write(raw []byte) (int, error) {
	var request struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return 0, err
	}
	reply := p.reply(request.Method, request.Params)
	p.agent.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, reply))
	return len(raw), nil
}
func (*museTestPeer) Close() error { return nil }

package acp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartSpecReadsTheRegistration keeps the launch and option metadata in
// one Registration. A second field can differ from the registry at runtime.
func TestStartSpecReadsTheRegistration(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[StartSpec[struct{}]]()
	field, ok := typ.FieldByName("Registration")
	require.True(t, ok, "ACP startup must receive the provider Registration")
	assert.Equal(t, reflect.TypeFor[agent.Registration](), field.Type)
	for _, duplicate := range []string{"Provider", "Locator", "OptionGroups"} {
		_, ok := typ.FieldByName(duplicate)
		assert.Falsef(t, ok, "StartSpec must read %s from Registration", duplicate)
	}
}

func TestSessionParams_AdjustTheSessionRequest(t *testing.T) {
	t.Parallel()
	var adjusted []string
	adjust := func(method string, params map[string]any) {
		adjusted = append(adjusted, method)
		params["_meta"] = map[string]any{"vendorFlag": true}
		if method == "session/resume" {
			params["cwd"] = "/stored/cwd"
		}
	}

	method, params := buildACPSessionRequest("", "/work", MethodSessionNew, "session/resume", adjust)
	assert.Equal(t, MethodSessionNew, method)
	assert.JSONEq(t, `{"cwd":"/work","mcpServers":[],"_meta":{"vendorFlag":true}}`, string(params))

	method, params = buildACPSessionRequest("sess-1", "/work", MethodSessionNew, "session/resume", adjust)
	assert.Equal(t, "session/resume", method)
	assert.JSONEq(t, `{"cwd":"/stored/cwd","mcpServers":[],"sessionId":"sess-1","_meta":{"vendorFlag":true}}`, string(params))
	assert.Equal(t, []string{MethodSessionNew, "session/resume"}, adjusted)

	_, params = buildACPSessionRequest("", "/work", MethodSessionNew, "", nil)
	assert.JSONEq(t, `{"cwd":"/work","mcpServers":[]}`, string(params))
}

// A context clear opens its session through the same hook as the handshake, so
// the session/new of a clear carries the keys of the provider too.
func TestSessionParams_AdjustTheSessionNewOfAContextClear(t *testing.T) {
	t.Parallel()
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.sink = agent.NewProviderServices(&agenttest.Sink{})
	var adjusted []string
	a.hooks.SessionParams = func(method string, params map[string]any) {
		adjusted = append(adjusted, method)
		params["_meta"] = map[string]any{"vendorFlag": true}
	}

	_, err := a.ClearContext()
	require.NoError(t, err)

	assert.Equal(t, []string{MethodSessionNew}, adjusted)
	lines := requests()
	index := indexOfMethod(lines, MethodSessionNew)
	require.NotEqual(t, -1, index)
	assert.Equal(t, map[string]any{"vendorFlag": true}, lines[index].Params["_meta"])
}

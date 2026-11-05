package antigravity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeAntigravityRPC(t *testing.T) (antigravityRPC, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		decoded["method"] = r.URL.Path
		bodies = append(bodies, decoded)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return antigravityRPC{endpoint: endpoint, timeout: time.Second}, &bodies
}

func TestAntigravitySendInputBuildsStrictRequest(t *testing.T) {
	rpc, bodies := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	require.NoError(t, a.SendInput("hello", nil))
	require.Len(t, *bodies, 1)
	body := (*bodies)[0]
	assert.Equal(t, "cascade", body["cascadeId"])
	assert.Equal(t, "/exa.language_server_pb.LanguageServerService/SendUserCascadeMessage", body["method"])
	items := body["items"].([]any)
	assert.Equal(t, "hello", items[0].(map[string]any)["text"])
	assert.Contains(t, body["cascadeConfig"], "plannerConfig")
	a.Mu.Lock()
	assert.True(t, a.root.sendInFlight)
	a.Mu.Unlock()
	assert.ErrorIs(t, a.SendInput("busy", nil), agent.ErrAgentBusy)
}

func TestAntigravitySlashAndCompactInput(t *testing.T) {
	rpc, bodies := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	a.slashCommands = []string{"compact", "plan"}
	require.NoError(t, a.SendInput("/compact", nil))
	items := (*bodies)[0]["items"].([]any)
	assert.Len(t, items, 1)
	assert.Contains(t, items[0], "item")
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning})
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.NoError(t, a.CompactContext())
	items = (*bodies)[len(*bodies)-1]["items"].([]any)
	assert.Len(t, items, 1)
	assert.Contains(t, items[0], "item")
}

func TestAntigravitySteerPolicy(t *testing.T) {
	rpc, bodies := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	assert.ErrorIs(t, a.SteerInput("early", nil), agent.ErrNoActiveTurn)
	a.beginTurn(a.root)
	require.NoError(t, a.SteerInput("steer", nil))
	require.Len(t, *bodies, 1)
	assert.True(t, a.SteerSurvivesTurnEnd())
	_, echo := any(a).(agent.SteerEchoReporter)
	assert.False(t, echo)
}

func TestAntigravityInterruptCancelsRootOnly(t *testing.T) {
	rpc, bodies := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	child := newCascadeState("child", a.sink.ChildSink("child"), 0, false)
	child.parent = a.root
	a.cascades["child"] = child
	a.beginTurn(a.root)
	a.beginTurn(child)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Len(t, *bodies, 1)
	assert.Equal(t, "cascade", (*bodies)[0]["cascadeId"])
	assert.Equal(t, "/exa.language_server_pb.LanguageServerService/CancelCascadeInvocation", (*bodies)[0]["method"])
}

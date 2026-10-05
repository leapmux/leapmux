package deepseekharness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nativeCalls records the native RPC methods that a settings update called, with their arguments.
type nativeCalls struct {
	mu    sync.Mutex
	calls []nativeCall
}

type nativeCall struct {
	method string
	args   map[string]any
}

func (c *nativeCalls) methods() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	methods := make([]string, 0, len(c.calls))
	for _, call := range c.calls {
		methods = append(methods, call.method)
	}
	return methods
}

func (c *nativeCalls) first(method string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if call.method == method {
			return call.args
		}
	}
	return nil
}

// serveNativeSettings answers the native RPC methods that a settings update uses. A model
// selection echoes the selection that it received, as the native process does for a valid one.
func serveNativeSettings(t *testing.T, a *Agent) *nativeCalls {
	t.Helper()
	calls := &nativeCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args map[string]any `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		calls.mu.Lock()
		calls.calls = append(calls.calls, nativeCall{method: method, args: request.Payload.Args})
		calls.mu.Unlock()
		var value any
		switch method {
		case "session/selectModel":
			selected, _ := request.Payload.Args["request"].(map[string]any)
			value = map[string]any{"selected": map[string]any{"provider": selected["provider"], "model": selected["model"], "reasoningEffort": selected["reasoningEffort"]}}
		case "commands/execute":
			value = map[string]any{"commandId": "cmd-1", "result": map[string]any{"kind": "success", "text": "done"}}
		default:
			t.Errorf("unexpected native method %s", method)
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": value}}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
	return calls
}

// Source: the Worker service (resetEffortToAutoIfUnsupported) sends effort "auto" with every model
// switch of a provider that manages effort, and the native model catalog of the E2E profile
// declares effort "high" for the first model and "low" for the second.
func TestUpdateSettingsSwitchesTheModelWhenTheServiceSendsEffortAuto(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	a.catalog = catalog
	a.selection = modelSelection{Provider: "deepseek-official", Model: "deepseek-flash", Effort: "max"}
	calls := serveNativeSettings(t, a)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "deepseek-official/deepseek-pro", agent.OptionIDEffort: agent.EffortAuto})

	assert.Equal(t, "deepseek-official/deepseek-pro", result.ConfirmedOptions()[agent.OptionIDModel], "the model switch took effect")
	assert.Equal(t, "off", result.ConfirmedOptions()[agent.OptionIDEffort], "the new model states its own default effort")
	selection := calls.first("session/selectModel")
	require.NotNil(t, selection)
	assert.Equal(t, map[string]any{"sessionId": "native-root", "provider": "deepseek-official", "model": "deepseek-pro", "reasoningEffort": "off"}, selection["request"])
	assert.Contains(t, calls.methods(), "commands/execute")
}

func TestUpdateSettingsKeepsTheCurrentSelectionForAnEffortThatTheModelDoesNotOffer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	a.catalog = catalog
	a.selection = modelSelection{Provider: "deepseek-official", Model: "deepseek-flash", Effort: "high"}
	calls := serveNativeSettings(t, a)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "deepseek-official/deepseek-pro", agent.OptionIDEffort: "max"})

	assert.Equal(t, "deepseek-official/deepseek-flash", result.ConfirmedOptions()[agent.OptionIDModel], "a refused update keeps the confirmed selection")
	assert.Empty(t, calls.methods(), "a refused update sends no native request")
}

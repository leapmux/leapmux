package deepseekharness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
		case "$events/result":
			value = nil
		case "commands/execute":
			value = map[string]any{"commandId": "cmd-1", "result": map[string]any{"kind": "success", "text": "done"}}
		case "session/projections":
			// A refused update reads the native values back. An empty projection keeps every value.
			value = map[string]any{"values": map[string]any{}}
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

// Source: the Worker service (resetEffortToAutoIfUnsupported) sends effort "auto" with a model
// switch of a provider that manages effort, when the catalog does not show that the new model offers
// the stored effort. The native model catalog of the E2E profile declares effort "high" for the first
// model and "low" for the second.
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

func optionIDs(group *leapmuxv1.AvailableOptionGroup) []string {
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	return ids
}

func TestPermissionGroupListsAutoOnlyWhileTheNativeCatalogOffersIt(t *testing.T) {
	t.Parallel()
	base := []string{contracts.DeepseekHarnessPermissionPresetReadOnly, contracts.DeepseekHarnessPermissionPresetWorkspaceWrite, contracts.DeepseekHarnessPermissionPresetDangerFullAccess}
	assert.Equal(t, append(slices.Clone(base), contracts.DeepseekHarnessPermissionPresetAuto), optionIDs(permissionGroup("", true)))
	assert.Equal(t, base, optionIDs(permissionGroup("", false)))
	assert.Len(t, nativePermissions, 4, "the filter leaves the shared list intact")
	// The launch overlay loads the layer, so the static groups offer Auto before an agent runs.
	var static *leapmuxv1.AvailableOptionGroup
	for _, group := range Registration().OptionGroups {
		if group.GetId() == contracts.DeepseekHarnessOptionPermissions {
			static = group
		}
	}
	require.NotNil(t, static)
	assert.Contains(t, optionIDs(static), contracts.DeepseekHarnessPermissionPresetAuto)
}

func TestOptionGroupsOmitsAutoWithoutNativeAutoReview(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.autoReview = false

	var permissions *leapmuxv1.AvailableOptionGroup
	for _, group := range a.OptionGroups() {
		if group.GetId() == contracts.DeepseekHarnessOptionPermissions {
			permissions = group
		}
	}
	require.NotNil(t, permissions)
	assert.Equal(t, []string{
		contracts.DeepseekHarnessPermissionPresetReadOnly,
		contracts.DeepseekHarnessPermissionPresetWorkspaceWrite,
		contracts.DeepseekHarnessPermissionPresetDangerFullAccess,
	}, optionIDs(permissions))
}

func TestUpdateSettingsSelectsAutoWhileTheNativeCatalogOffersIt(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	a.catalog, a.selection, a.autoReview = catalog, catalog.defaultSelection, true
	calls := serveNativeSettings(t, a)

	result := a.UpdateSettings(optionmap.Map{contracts.DeepseekHarnessOptionPermissions: contracts.DeepseekHarnessPermissionPresetAuto})

	assert.Equal(t, contracts.DeepseekHarnessPermissionPresetAuto, result.ConfirmedOptions()[contracts.DeepseekHarnessOptionPermissions])
	assert.Equal(t, "/permission "+contracts.DeepseekHarnessPermissionPresetAuto, calls.first("commands/execute")["line"])
	assert.Contains(t, optionIDs(permissionGroup(a.permissions, a.autoReview)), contracts.DeepseekHarnessPermissionPresetAuto)
}

func TestUpdateSettingsRefusesAutoWithoutTheNativeCatalog(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	a.catalog, a.selection = catalog, catalog.defaultSelection
	calls := serveNativeSettings(t, a)

	result := a.UpdateSettings(optionmap.Map{contracts.DeepseekHarnessOptionPermissions: contracts.DeepseekHarnessPermissionPresetAuto})

	assert.Equal(t, contracts.DeepseekHarnessPermissionPresetWorkspaceWrite, result.ConfirmedOptions()[contracts.DeepseekHarnessOptionPermissions], "a refused preset keeps the confirmed one")
	assert.Equal(t, []string{"session/projections"}, calls.methods(), "the refusal sends no native setting")
	assert.NotContains(t, optionIDs(permissionGroup(a.permissions, a.autoReview)), contracts.DeepseekHarnessPermissionPresetAuto)
}

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newCodexAgentForRPC(t *testing.T, respond func(method string) json.RawMessage) (*CodexAgent, *testSink, func() []codexRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	sink := &testSink{}
	agent := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: processBase{
			agentID:     "test-codex",
			stdin:       writePipe,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		}},
		model:             "gpt-5.4",
		effort:            "high",
		approvalPolicy:    CodexDefaultApprovalPolicy,
		sandboxPolicy:     CodexDefaultSandboxPolicy,
		networkAccess:     CodexDefaultNetworkAccess,
		collaborationMode: CodexDefaultCollaborationMode,
		serviceTier:       CodexDefaultServiceTier,
		sink:              sink,
	}
	close(agent.stderrDone)

	var (
		mu       sync.Mutex
		requests []codexRecordedRequest
	)

	go func() {
		scanner := bufio.NewScanner(readPipe)
		for scanner.Scan() {
			var req struct {
				ID     int64                  `json:"id"`
				Method string                 `json:"method"`
				Params map[string]interface{} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			mu.Lock()
			requests = append(requests, codexRecordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			agent.deliver(req.ID, respond(req.Method))
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, sink, func() []codexRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]codexRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func TestCodexRefreshSettingsFromAgent(t *testing.T) {
	t.Parallel()

	agent, sink, requests := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.2",
					"model_reasoning_effort": "medium",
					"approval_policy": "never",
					"sandbox_mode": "danger-full-access",
					"service_tier": "fast"
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, "medium", agent.effort)
	assert.Equal(t, "never", agent.approvalPolicy)
	assert.Equal(t, "danger-full-access", agent.sandboxPolicy)
	assert.Equal(t, "fast", agent.serviceTier)

	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "config/read", recorded[0].Method)

	// Verify settings were broadcast.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "medium", refresh.Effort)
	assert.Equal(t, "never", refresh.PermissionMode)
	assert.Equal(t, "danger-full-access", refresh.Options[CodexOptionSandboxPolicy])
	assert.Equal(t, "fast", refresh.Options[CodexOptionServiceTier])
}

func TestCodexRefreshSettingsFromAgent_PreservesResumeModelOverride(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			// config/read returns the global configuration. It does not include the
			// model override that thread/resume applied to this session.
			return json.RawMessage(`{"config":{"model":"gpt-5.6-luna"},"origins":{}}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.6-sol"

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "gpt-5.6-sol", agent.model)
}

func TestCodexRefreshSettingsFromAgent_NullFields(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": null,
					"model_reasoning_effort": null,
					"approval_policy": null,
					"sandbox_mode": null,
					"network_access": null,
					"collaboration_mode": null,
					"service_tier": null
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	// Set initial values.
	agent.model = "gpt-5.4"
	agent.effort = "high"
	agent.approvalPolicy = "on-request"
	agent.sandboxPolicy = "workspace-write"
	agent.networkAccess = "enabled"
	agent.collaborationMode = "plan"
	agent.serviceTier = "default"

	agent.refreshSettingsFromAgent()

	// Null fields should not overwrite existing values.
	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, "high", agent.effort)
	assert.Equal(t, "on-request", agent.approvalPolicy)
	assert.Equal(t, "workspace-write", agent.sandboxPolicy)
	assert.Equal(t, "enabled", agent.networkAccess)
	assert.Equal(t, "plan", agent.collaborationMode)
	assert.Equal(t, "default", agent.serviceTier)
}

// TestCodexRefreshSettingsFromAgent_PreservesNetworkAndCollaboration documents that Codex's
// config/read does NOT carry network_access or collaboration_mode: network access is a boolean
// nested under sandbox_workspace_write (not a top-level key), and collaboration_mode is a
// per-turn parameter, not a config key at all (verified against the codex-rs app-server v2
// Config struct). The readback therefore omits both, the table loop keeps their prior
// (optimistically-pushed) values, and a live edit to either axis is never clobbered by
// config/read -- while a real top-level config key (sandbox_mode) IS reconciled by the same loop.
func TestCodexRefreshSettingsFromAgent_PreservesNetworkAndCollaboration(t *testing.T) {
	t.Parallel()

	agent, sink, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			// A realistic Codex response: sandbox_mode is reported (a real top-level config key),
			// but network_access and collaboration_mode are absent -- they are not config keys.
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.2",
					"sandbox_mode": "read-only"
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	// The user optimistically set network on + plan collaboration via UpdateSettings.
	agent.networkAccess = "enabled"
	agent.collaborationMode = "plan"

	agent.refreshSettingsFromAgent()

	// config/read doesn't report these axes, so the pushed values survive unchanged -- no flip.
	assert.Equal(t, "enabled", agent.networkAccess)
	assert.Equal(t, "plan", agent.collaborationMode)
	// A real top-level config key IS reconciled by the same table loop.
	assert.Equal(t, "read-only", agent.sandboxPolicy)
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "enabled", refresh.Options[CodexOptionNetworkAccess])
	assert.Equal(t, "plan", refresh.Options[CodexOptionCollaborationMode])
}

func TestCodexRefreshSettingsFromAgent_GranularApprovalPolicy(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.4",
					"approval_policy": {"granular": {"sandbox_approval": true, "rules": true}}
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.approvalPolicy = "on-request"
	agent.refreshSettingsFromAgent()

	// Granular object should not overwrite the simple string.
	assert.Equal(t, "on-request", agent.approvalPolicy)
}

func TestCodexUpdateSettingsCallsRefresh(t *testing.T) {
	t.Parallel()

	agent, sink, requests := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.2",
					"model_reasoning_effort": "low",
					"approval_policy": "never",
					"sandbox_mode": "read-only",
					"service_tier": "fast"
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:            "gpt-5.2",
		OptionIDEffort:           "low",
		OptionIDPermissionMode:   "never",
		CodexOptionSandboxPolicy: "read-only",
		CodexOptionServiceTier:   "fast",
	})
	require.True(t, updated)

	// After UpdateSettings, refreshSettingsFromAgent should have been called.
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "config/read", recorded[0].Method)

	// Values should reflect the config/read response.
	assert.Equal(t, "gpt-5.2", agent.model)
	assert.Equal(t, "low", agent.effort)
	assert.Equal(t, "never", agent.approvalPolicy)
	assert.Equal(t, "read-only", agent.sandboxPolicy)
	assert.Equal(t, "fast", agent.serviceTier)

	// Verify settings were broadcast.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.2", refresh.Model)
	assert.Equal(t, "low", refresh.Effort)
	assert.Equal(t, "never", refresh.PermissionMode)
}

// TestCodexRefreshSettingsFromAgent_AutoFallsBackToModelDefault verifies
// that when config/read returns null for model_reasoning_effort and the
// agent is in "auto" mode, the refresh falls back to the current model's
// preset default from the model catalog. This mirrors Codex's own
// inference-time behavior, where the CLI uses ModelInfo.default_reasoning_level
// when nothing is explicitly set in config.
func TestCodexRefreshSettingsFromAgent_AutoFallsBackToModelDefault(t *testing.T) {
	t.Parallel()

	agent, sink, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": null,
					"model_reasoning_effort": null
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"
	agent.model = "gpt-5.4"
	agent.availableModels = []*ModelInfo{
		{Id: "gpt-5.4", DefaultEffort: "high"},
		{Id: "gpt-5.2", DefaultEffort: "medium"},
	}

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "high", agent.effort,
		"auto should fall back to the current model's default from the catalog")

	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "high", sink.LastSettingsRefresh().Effort,
		"broadcast should carry the resolved effort so the UI updates")
}

// TestCodexRefreshSettingsFromAgent_AutoNoModelCatalogStaysAuto verifies
// the safe fallback: if the model catalog hasn't been populated yet (e.g.
// queryAvailableModels failed), the refresh leaves effort at "auto" rather
// than clobbering it with an empty string.
func TestCodexRefreshSettingsFromAgent_AutoNoModelCatalogStaysAuto(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{"config": {"model_reasoning_effort": null}, "origins": {}}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"
	agent.model = "gpt-5.4"
	agent.availableModels = nil

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "auto", agent.effort, "with no catalog, auto stays auto")
}

// TestCodexOptionGroups_OrderAndCurrentsFromTemplates verifies the live catalog
// carries each group's display order (now sourced from the registered template,
// not a hand-maintained side map) and the agent's current values, with model and
// effort leading and every provider group sorting after the model group.
func TestCodexOptionGroups_OrderAndCurrentsFromTemplates(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.availableModels = []*ModelInfo{{Id: "gpt-5.4", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{{Id: "high"}, {Id: "low"}}}}
	agent.serviceTier = CodexServiceTierFast

	groups := agent.OptionGroups()
	orderByID := map[string]int32{}
	currentByID := map[string]string{}
	for _, g := range groups {
		orderByID[g.GetId()] = g.GetOrder()
		currentByID[g.GetId()] = g.GetCurrentValue()
	}

	assert.Equal(t, OptionOrderModel, orderByID[OptionIDModel])
	assert.Equal(t, OptionOrderEffort, orderByID[OptionIDEffort])
	assert.Equal(t, OptionOrderProviderFirst, orderByID[CodexOptionServiceTier])
	assert.Equal(t, OptionOrderProviderSecond, orderByID[CodexOptionCollaborationMode])
	assert.Equal(t, OptionOrderProviderThird, orderByID[CodexOptionNetworkAccess])
	assert.Equal(t, OptionOrderProviderFourth, orderByID[CodexOptionSandboxPolicy])
	assert.Equal(t, OptionOrderPermissionMode, orderByID[OptionIDPermissionMode])

	// The agent's per-axis current values flow through.
	assert.Equal(t, "gpt-5.4", currentByID[OptionIDModel])
	assert.Equal(t, "high", currentByID[OptionIDEffort])
	assert.Equal(t, CodexServiceTierFast, currentByID[CodexOptionServiceTier])

	for id, ord := range orderByID {
		if id != OptionIDModel {
			assert.Greater(t, ord, OptionOrderModel, "group %q must sort after the model group", id)
		}
	}
}

// TestCodexUpdateSettings_AutoRequiresRestart verifies that switching
// effort to "auto" mid-session signals the caller to restart the agent
// (returns false) rather than writing "auto" into Codex's live session
// config. Codex has no way to clear reasoning_effort at runtime, so a
// fresh process is the only path back to CLI-default behavior.
func TestCodexUpdateSettings_AutoRequiresRestart(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(_ string) json.RawMessage {
		return json.RawMessage(`{}`)
	})

	require.Equal(t, "high", agent.effort, "precondition")

	updated := agent.UpdateSettings(map[string]string{OptionIDEffort: "auto"})
	require.False(t, updated, "switching to \"auto\" should request a restart")

	assert.Equal(t, "high", agent.effort, "live effort must stay untouched until restart")
	assert.Empty(t, requests(), "no config/read should be issued when restart is requested")
}

// TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto verifies that when the
// agent is already in "auto", a redundant "auto" update does not trigger
// a restart.
func TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{"config": {}, "origins": {}}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"

	updated := agent.UpdateSettings(map[string]string{OptionIDEffort: "auto"})
	require.True(t, updated, "a no-op \"auto\"→\"auto\" should not request a restart")
	assert.Equal(t, "auto", agent.effort)
}

// TestCodexThreadParams covers the request params shared by thread/start and thread/resume that
// StartCodex and ClearContext build identically via codexThreadParams. The model/cwd/approvalPolicy/
// sandbox axes are stamped verbatim; serviceTier is included ONLY when codexServiceTierValue reports
// a non-default tier (so the default/unset tier leaves Codex's normal tier untouched).
func TestCodexThreadParams(t *testing.T) {
	t.Parallel()

	// A non-default service tier is included.
	fast := codexThreadParams("gpt-5.4", "/work", CodexDefaultApprovalPolicy, CodexDefaultSandboxPolicy, CodexServiceTierFast)
	assert.Equal(t, "gpt-5.4", fast["model"])
	assert.Equal(t, "/work", fast["cwd"])
	assert.Equal(t, CodexDefaultApprovalPolicy, fast["approvalPolicy"])
	assert.Equal(t, CodexDefaultSandboxPolicy, fast["sandbox"])
	assert.Equal(t, CodexServiceTierFast, fast["serviceTier"], "a non-default tier is sent")

	// The default tier omits serviceTier so Codex keeps its normal tier.
	def := codexThreadParams("gpt-5.4", "/work", CodexDefaultApprovalPolicy, CodexDefaultSandboxPolicy, CodexDefaultServiceTier)
	_, hasDefaultTier := def["serviceTier"]
	assert.False(t, hasDefaultTier, "the default tier omits serviceTier")

	// An empty (unset) tier likewise omits it.
	empty := codexThreadParams("gpt-5.4", "/work", CodexDefaultApprovalPolicy, CodexDefaultSandboxPolicy, "")
	_, hasEmptyTier := empty["serviceTier"]
	assert.False(t, hasEmptyTier, "an empty tier omits serviceTier")
}

// The static fallback effort list is BOTH the id -> name lookup
// (codexEffortName) and the ordered menu every codexDefaultModels entry
// advertises. Only the second role constrains the order, and only the first
// one fails visibly when it is wrong -- so appending a tier satisfies the
// label lookup while placing it wrongly in every picker, which is exactly how
// "max" first landed after "none".
func TestCodexDefaultEffortsRankOrder(t *testing.T) {
	// The expected order is DERIVED from the shared effortRank table, not
	// written out again here. A hand-written list is a third copy of the
	// ordering, and a tier appended at the end can be added to the list and the
	// slice in one edit, which is the mistake this test exists to catch.
	require.NotEmpty(t, codexDefaultEfforts)
	require.Equal(t, EffortAuto, codexDefaultEfforts[0].Id, "the LeapMux auto sentinel leads the menu")

	tiers := codexDefaultEfforts[1:]
	for i, e := range tiers {
		rank, ok := effortRankOf(e.Id)
		require.True(t, ok, "effort %q is absent from effortRank, so sortEffortOptionsDescending drops it into the unranked tail", e.Id)
		if i == 0 {
			continue
		}
		prevRank, _ := effortRankOf(tiers[i-1].Id)
		assert.Greater(t, prevRank, rank,
			"codexDefaultEfforts is the menu order: %q ranks above %q, so it must come first", tiers[i-1].Id, e.Id)
	}

	// Every tier carries a label, and it is the SHARED table's label -- the live
	// catalog resolves the same way, so the two paths cannot spell one tier two
	// ways.
	for _, e := range codexDefaultEfforts {
		assert.NotEmpty(t, e.Name, "effort %q needs a display name", e.Id)
		assert.Equal(t, effortLabel(e.Id), e.Name, "effort %q must take the shared label", e.Id)
		assert.NotEqual(t, e.Id, e.Name, "effort %q must carry a display label, not its raw id", e.Id)
	}

	// The models that advertise the list inherit that order verbatim.
	want := make([]string, 0, len(codexDefaultEfforts))
	for _, e := range codexDefaultEfforts {
		want = append(want, e.Id)
	}
	for _, m := range codexDefaultModels {
		require.NotEmpty(t, m.SupportedEfforts, "model %q advertises no efforts", m.Id)
		ids := make([]string, 0, len(m.SupportedEfforts))
		for _, e := range m.SupportedEfforts {
			ids = append(ids, e.Id)
		}
		assert.Equal(t, want, ids, "model %q must offer the tiers in rank order", m.Id)
	}
}

// The live CLI (codex-cli 0.147.0) reports an "ultra" reasoning effort above
// "max" on every current model. A tier missing from codexDefaultEfforts leaves
// the static fallback offering a menu the running session does not, and
// effortRank sorts it into the unranked tail.
func TestCodexOffersUltraEffort(t *testing.T) {
	var ultra *EffortInfo
	for _, e := range codexDefaultEfforts {
		if e.Id == "ultra" {
			ultra = e
		}
	}
	require.NotNil(t, ultra, "the live CLI reports an \"ultra\" tier; the fallback catalog must offer it too")
	assert.Equal(t, "Ultra", ultra.Name, "the tier carries its shared label")
	// A tier the CLI ships before this catalog catches up still renders
	// capitalized, not as a raw lowercase id beside its siblings.
	assert.Equal(t, "Turbo", effortLabel("turbo"), "an unlisted tier is capitalized, not raw")

	ultraRank, ok := effortRankOf("ultra")
	require.True(t, ok, "effortRank must rank \"ultra\" or it sorts after every ranked value")
	maxRank, ok := effortRankOf("max")
	require.True(t, ok)
	assert.Greater(t, ultraRank, maxRank, "\"ultra\" is stronger than \"max\"")
}

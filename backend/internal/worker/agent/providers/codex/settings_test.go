package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newCodexAgentForRPC(t *testing.T, respond func(method string) agenttest.RPCReply) (*Agent, *agenttest.Sink, func() []codexRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	sink := &agenttest.Sink{}
	agent := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:     "test-codex",
			Stdin:       writePipe,
			Ctx:         ctx,
			Cancel:      cancel,
			ProcessDone: make(chan struct{}),
			StderrDone:  make(chan struct{}),
		})},
		model:             "gpt-5.4",
		effort:            "high",
		approvalPolicy:    DefaultApprovalPolicy,
		sandboxPolicy:     contracts.CodexOptionDefaultSandboxPolicy,
		networkAccess:     contracts.CodexOptionDefaultNetworkAccess,
		collaborationMode: contracts.CodexOptionDefaultCollaborationMode,
		serviceTier:       contracts.CodexOptionDefaultServiceTier,
		sink:              agent.NewProviderServices(sink),
	}
	agent.SkipStderr()

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
			agent.Deliver(req.ID, agenttest.JSONRPCResponse(req.ID, respond(req.Method)))
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

func TestCodexUpdateSettingsPublishesRequestedValues(t *testing.T) {
	t.Parallel()

	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	updated := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:                "gpt-5.2",
		agent.OptionIDEffort:               "low",
		agent.OptionIDPermissionMode:       "never",
		contracts.CodexOptionSandboxPolicy: "read-only",
		contracts.CodexOptionServiceTier:   "fast",
	})
	require.True(t, updated.AppliedLive)

	assert.Empty(t, requests(), "active settings must not read thread-agnostic global config")
	assert.Equal(t, "gpt-5.2", a.model)
	assert.Equal(t, "low", a.effort)
	assert.Equal(t, "never", a.approvalPolicy)
	assert.Equal(t, "read-only", a.sandboxPolicy)
	assert.Equal(t, "fast", a.serviceTier)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.2", refresh.Model)
	assert.Equal(t, "low", refresh.Effort)
	assert.Equal(t, "never", refresh.PermissionMode)
}

func TestCodexUpdateSettingsPreservesRequestedThreadSettings(t *testing.T) {
	t.Parallel()

	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	updated := a.UpdateSettings(map[string]string{
		agent.OptionIDEffort:               "low",
		agent.OptionIDPermissionMode:       "never",
		contracts.CodexOptionSandboxPolicy: SandboxDangerFullAccess,
		contracts.CodexOptionNetworkAccess: NetworkEnabled,
		contracts.CodexOptionServiceTier:   ServiceTierFast,
	})

	require.True(t, updated.AppliedLive)
	assert.Equal(t, "low", a.effort)
	assert.Equal(t, "never", a.approvalPolicy)
	assert.Equal(t, SandboxDangerFullAccess, a.sandboxPolicy)
	assert.Equal(t, NetworkEnabled, a.networkAccess)
	assert.Equal(t, ServiceTierFast, a.serviceTier)
	assert.Empty(t, requests())
}

func TestCodexPublishSettings_AutoFallsBackToModelDefault(t *testing.T) {
	t.Parallel()

	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	a.effort = "auto"
	a.model = "gpt-5.4"
	a.availableModels = []*agent.ModelInfo{
		{Id: "gpt-5.4", DefaultEffort: "high"},
		{Id: "gpt-5.2", DefaultEffort: "medium"},
	}

	a.publishSettings()

	assert.Equal(t, "high", a.effort,
		"auto should fall back to the current model's default from the catalog")

	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "high", sink.LastSettingsRefresh().Effort,
		"broadcast should carry the resolved effort so the UI updates")
}

func TestCodexPublishSettings_AutoNoModelCatalogStaysAuto(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	agent.effort = "auto"
	agent.model = "gpt-5.4"
	agent.availableModels = nil

	agent.publishSettings()

	assert.Equal(t, "auto", agent.effort, "with no catalog, auto stays auto")
}

// TestCodexOptionGroups_OrderAndCurrentsFromTemplates verifies the live catalog
// carries each group's display order (now sourced from the registered template,
// not a hand-maintained side map) and the agent's current values, with model and
// effort leading and every provider group sorting after the model group.
func TestCodexOptionGroups_OrderAndCurrentsFromTemplates(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.availableModels = []*agent.ModelInfo{{Id: "gpt-5.4", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{{Id: "high"}, {Id: "low"}}}}
	a.serviceTier = ServiceTierFast

	groups := a.OptionGroups()
	orderByID := map[string]int32{}
	currentByID := map[string]string{}
	for _, g := range groups {
		orderByID[g.GetId()] = g.GetOrder()
		currentByID[g.GetId()] = g.GetCurrentValue()
	}

	assert.Equal(t, agent.OptionOrderModel, orderByID[agent.OptionIDModel])
	assert.Equal(t, agent.OptionOrderEffort, orderByID[agent.OptionIDEffort])
	assert.Equal(t, agent.OptionOrderProviderFirst, orderByID[contracts.CodexOptionServiceTier])
	assert.Equal(t, agent.OptionOrderProviderSecond, orderByID[contracts.CodexOptionCollaborationMode])
	assert.Equal(t, agent.OptionOrderProviderThird, orderByID[contracts.CodexOptionNetworkAccess])
	assert.Equal(t, agent.OptionOrderProviderFourth, orderByID[contracts.CodexOptionSandboxPolicy])
	assert.Equal(t, agent.OptionOrderPermissionMode, orderByID[agent.OptionIDPermissionMode])

	// The agent's per-axis current values flow through.
	assert.Equal(t, "gpt-5.4", currentByID[agent.OptionIDModel])
	assert.Equal(t, "high", currentByID[agent.OptionIDEffort])
	assert.Equal(t, ServiceTierFast, currentByID[contracts.CodexOptionServiceTier])

	for id, ord := range orderByID {
		if id != agent.OptionIDModel {
			assert.Greater(t, ord, agent.OptionOrderModel, "group %q must sort after the model group", id)
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

	ag, _, requests := newCodexAgentForRPC(t, func(_ string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	require.Equal(t, "high", ag.effort, "precondition")

	updated := ag.UpdateSettings(map[string]string{agent.OptionIDEffort: "auto"})
	require.False(t, updated.AppliedLive, "switching to \"auto\" should request a restart")

	assert.Equal(t, "high", ag.effort, "live effort must stay untouched until restart")
	assert.Empty(t, requests(), "a rejected update must not publish an RPC")
}

// TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto verifies that when the
// agent is already in "auto", a redundant "auto" update does not trigger
// a restart.
func TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto(t *testing.T) {
	t.Parallel()

	ag, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	ag.effort = "auto"

	updated := ag.UpdateSettings(map[string]string{agent.OptionIDEffort: "auto"})
	require.True(t, updated.AppliedLive, "a no-op \"auto\"→\"auto\" should not request a restart")
	assert.Equal(t, "auto", ag.effort)
}

func TestDefaultModel_CodexUsesAccountDefaultSentinel(t *testing.T) {
	t.Setenv("LEAPMUX_CODEX_DEFAULT_MODEL", "")

	assert.Equal(t, agent.DefaultModelSentinel, Registration().DefaultModel())
}

func TestDefaultModel_CodexUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("LEAPMUX_CODEX_DEFAULT_MODEL", "gpt-5.6-terra")

	assert.Equal(t, "gpt-5.6-terra", Registration().DefaultModel())
}

// TestCodexFallbackCatalogIsPinned restates codexDefaultModels so an unintended
// edit fails loudly. It cannot detect a catalog that has drifted from the app
// server, because both sides are hand-written here -- the name says "pinned", not
// "matches the CLI", so nobody reads it as the stronger guarantee. Re-copy the
// selectable rows from `codex app-server` model/list when Codex ships a release.
func TestCodexFallbackCatalogIsPinned(t *testing.T) {
	t.Parallel()

	type fallbackModel struct {
		id            string
		displayName   string
		description   string
		isDefault     bool
		hidden        bool
		defaultEffort string
		efforts       []string
		contextWindow int64
	}
	actual := make([]fallbackModel, 0, len(codexDefaultModels))
	for _, model := range codexDefaultModels {
		var efforts []string
		for _, effort := range model.SupportedEfforts {
			efforts = append(efforts, effort.Id)
		}
		actual = append(actual, fallbackModel{
			id:            model.Id,
			displayName:   model.DisplayName,
			description:   model.Description,
			isDefault:     model.IsDefault,
			hidden:        model.Hidden,
			defaultEffort: model.DefaultEffort,
			efforts:       efforts,
			contextWindow: model.ContextWindow,
		})
	}
	assert.Equal(t, []fallbackModel{
		{id: agent.DefaultModelSentinel, displayName: "Default (recommended)", description: "Use the account's default Codex model", isDefault: true},
		{id: "gpt-5.6-sol", displayName: "GPT-5.6-Sol", description: "Reliable agentic workhorse for everyday tasks", defaultEffort: "low", efforts: []string{agent.EffortAuto, "ultra", "max", agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 1_050_000},
		{id: "gpt-5.6-terra", displayName: "GPT-5.6-Terra", description: "Balanced agentic coding model for everyday work", defaultEffort: "medium", efforts: []string{agent.EffortAuto, "ultra", "max", agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 1_050_000},
		{id: "gpt-5.6-luna", displayName: "GPT-5.6-Luna", description: "Fast and affordable agentic coding model", defaultEffort: "medium", efforts: []string{agent.EffortAuto, "max", agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 1_050_000},
		{id: "gpt-5.5", displayName: "GPT-5.5", description: "Proven previous-generation model for coding and general work", defaultEffort: "medium", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 1_050_000},
		{id: "gpt-5.4", displayName: "GPT-5.4", description: "Strong model for everyday coding", defaultEffort: "medium", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 1_050_000},
		{id: "gpt-5.4-mini", displayName: "GPT-5.4-Mini", description: "Small, fast, and cost-efficient model for simpler coding tasks", defaultEffort: "medium", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 400_000},
		{id: "gpt-5.3-codex-spark", displayName: "GPT-5.3-Codex-Spark", description: "Ultra-fast coding model", defaultEffort: "high", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 128_000},
		{id: "gpt-5.2", displayName: "GPT-5.2", description: "Optimized for professional work and long-running agents", isDefault: false, hidden: true, defaultEffort: "high", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 256_000},
		{id: "gpt-5.3-codex", displayName: "GPT-5.3 Codex", description: "Frontier Codex-optimized agentic coding model", hidden: true, defaultEffort: "high", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 400_000},
		{id: "gpt-5.2-codex", displayName: "GPT-5.2 Codex", description: "Frontier agentic coding model", hidden: true, defaultEffort: "high", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 400_000},
		{id: "gpt-5.1-codex-max", displayName: "GPT-5.1 Codex Max", description: "Codex-optimized model for deep and fast reasoning", hidden: true, defaultEffort: "high", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 400_000},
		{id: "gpt-5.1-codex-mini", displayName: "GPT-5.1 Codex Mini", description: "Optimized for Codex; cheaper, faster, but less capable", hidden: true, defaultEffort: "high", efforts: []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, contextWindow: 400_000},
	}, actual)
}

func TestCodexQueryAvailableModelsDoesNotInjectAccountDefault(t *testing.T) {
	t.Parallel()

	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{"data":[
			{"id":"gpt-5.6-sol","model":"gpt-5.6-sol","displayName":"gpt-5.6-sol","isDefault":true,"defaultReasoningEffort":"low","supportedReasoningEfforts":[
				{"reasoningEffort":"low","description":"Low"},
				{"reasoningEffort":"high","description":"High"}
			]},
			{"id":"hidden-model","model":"hidden-model","displayName":"Hidden","hidden":true,"supportedReasoningEfforts":[]}
		]}`)}
	})

	models := a.queryAvailableModels(time.Second)

	require.Len(t, models, 1)
	assert.Equal(t, "gpt-5.6-sol", models[0].Id)
	assert.Equal(t, "GPT-5.6-Sol", models[0].DisplayName)
	assert.True(t, models[0].IsDefault)
	assert.Equal(t, "low", models[0].DefaultEffort)
	assert.Equal(t, int64(1_050_000), models[0].ContextWindow)
	assert.Equal(t, []string{agent.EffortAuto, agent.EffortHigh, "low"}, agenttest.OptionIDs(models[0].SupportedEfforts))
	// The RAW query reports exactly what model/list carries, and Codex never lists
	// the sentinel. reconcileModelCatalog is what puts the row back afterwards --
	// see TestCodexReconcileModelCatalog.
	assert.Nil(t, agent.FindAvailableModel(models, agent.DefaultModelSentinel))
	sent := requests()
	require.Len(t, sent, 1)
	assert.Equal(t, "model/list", sent[0].Method)
}

// TestCodexThreadParams covers the request params shared by thread/start and thread/resume that
// Start and ClearContext build identically via codexThreadParams. The cwd/approvalPolicy/
// sandbox axes are stamped verbatim; the model is included only for a concrete id, and an
// account-default model is omitted so Codex resolves it; serviceTier is included ONLY when codexServiceTierValue reports
// a non-default tier (so the default/unset tier leaves Codex's normal tier untouched).
func TestCodexThreadParams(t *testing.T) {
	t.Parallel()

	// A non-default service tier is included.
	fast := codexThreadParams("gpt-5.4", "/work", DefaultApprovalPolicy, contracts.CodexOptionDefaultSandboxPolicy, ServiceTierFast)
	assert.Equal(t, map[string]interface{}{"model_reasoning_summary": "detailed"}, fast["config"])
	assert.Equal(t, "gpt-5.4", fast["model"])
	assert.Equal(t, "/work", fast["cwd"])
	assert.Equal(t, DefaultApprovalPolicy, fast["approvalPolicy"])
	assert.Equal(t, contracts.CodexOptionDefaultSandboxPolicy, fast["sandbox"])
	assert.Equal(t, ServiceTierFast, fast["serviceTier"], "a non-default tier is sent")

	// The default tier omits serviceTier so Codex keeps its normal tier.
	def := codexThreadParams("gpt-5.4", "/work", DefaultApprovalPolicy, contracts.CodexOptionDefaultSandboxPolicy, contracts.CodexOptionDefaultServiceTier)
	_, hasDefaultTier := def["serviceTier"]
	assert.False(t, hasDefaultTier, "the default tier omits serviceTier")

	// An empty (unset) tier likewise omits it.
	empty := codexThreadParams("gpt-5.4", "/work", DefaultApprovalPolicy, contracts.CodexOptionDefaultSandboxPolicy, "")
	_, hasEmptyTier := empty["serviceTier"]
	assert.False(t, hasEmptyTier, "an empty tier omits serviceTier")

	accountDefault := codexThreadParams(agent.DefaultModelSentinel, "/work", DefaultApprovalPolicy, contracts.CodexOptionDefaultSandboxPolicy, contracts.CodexOptionDefaultServiceTier)
	assert.NotContains(t, accountDefault, "model", "the account default lets Codex resolve the model")

	unsetModel := codexThreadParams("", "/work", DefaultApprovalPolicy, contracts.CodexOptionDefaultSandboxPolicy, contracts.CodexOptionDefaultServiceTier)
	assert.NotContains(t, unsetModel, "model", "an unset model lets Codex resolve the model")
}

// The fallback effort list supplies labels and menu order. Each model selects
// a subset, which must keep that order.
func TestCodexDefaultEffortsRankOrder(t *testing.T) {
	require.NotEmpty(t, codexDefaultEfforts)
	require.Equal(t, agent.EffortAuto, codexDefaultEfforts[0].Id, "the LeapMux auto sentinel leads the menu")

	tiers := codexDefaultEfforts[1:]
	for i, e := range tiers {
		rank, ok := providerkit.EffortRankOf(e.Id)
		require.True(t, ok, "effort %q is absent from providerkit.EffortRank, so providerkit.SortEffortsDescending drops it into the unranked tail", e.Id)
		if i == 0 {
			continue
		}
		prevRank, _ := providerkit.EffortRankOf(tiers[i-1].Id)
		assert.Greater(t, prevRank, rank,
			"codexDefaultEfforts is the menu order: %q ranks above %q, so it must come first", tiers[i-1].Id, e.Id)
	}

	// Every tier uses the shared label.
	for _, e := range codexDefaultEfforts {
		assert.NotEmpty(t, e.Name, "effort %q needs a display name", e.Id)
		assert.Equal(t, providerkit.EffortLabel(e.Id), e.Name, "effort %q must take the shared label", e.Id)
		assert.NotEqual(t, e.Id, e.Name, "effort %q must carry a display label, not its raw id", e.Id)
	}

	known := make(map[string]bool, len(codexDefaultEfforts))
	for _, effort := range codexDefaultEfforts {
		known[effort.Id] = true
	}
	for _, m := range codexDefaultModels {
		if m.Id == agent.DefaultModelSentinel {
			assert.Empty(t, m.SupportedEfforts, "the unresolved model has no effort catalog")
			continue
		}
		require.NotEmpty(t, m.SupportedEfforts, "model %q advertises no efforts", m.Id)
		require.Equal(t, agent.EffortAuto, m.SupportedEfforts[0].Id, "model %q must offer auto first", m.Id)
		for i, effort := range m.SupportedEfforts[1:] {
			require.True(t, known[effort.Id], "model %q has unknown effort %q", m.Id, effort.Id)
			if i == 0 {
				continue
			}
			previousRank, _ := providerkit.EffortRankOf(m.SupportedEfforts[i].Id)
			rank, _ := providerkit.EffortRankOf(effort.Id)
			assert.Greater(t, previousRank, rank, "model %q must list efforts in rank order", m.Id)
		}
		// Each model now carries a WINDOW of the ladder, not the whole of it, so a
		// default effort can fall outside the model's own menu. agent.EffortGroupForModel
		// marks the default per option, so an unofferable default marks nothing and
		// codexEffortRefreshFallback then writes that id into the live effort.
		assert.Contains(t, agenttest.OptionIDs(m.SupportedEfforts), m.DefaultEffort,
			"model %q must offer its own default effort %q", m.Id, m.DefaultEffort)
	}
}

// The live CLI (codex-cli 0.152.1) reports an "ultra" effort above "max" on
// GPT-5.6 Sol and Terra, and not on Luna. A tier that codexDefaultEfforts omits
// leaves the static fallback offering a menu the running session does not, and
// providerkit.EffortRank sorts the omitted tier into the unranked tail.
func TestCodexOffersUltraEffort(t *testing.T) {
	var ultra *agent.EffortInfo
	for _, e := range codexDefaultEfforts {
		if e.Id == "ultra" {
			ultra = e
		}
	}
	require.NotNil(t, ultra, "the live CLI reports an \"ultra\" tier; the fallback catalog must offer it too")
	assert.Equal(t, "Ultra", ultra.Name, "the tier carries its shared label")
	sol := agent.FindAvailableModel(codexDefaultModels, "gpt-5.6-sol")
	luna := agent.FindAvailableModel(codexDefaultModels, "gpt-5.6-luna")
	require.NotNil(t, sol)
	require.NotNil(t, luna)
	assert.Contains(t, agenttest.OptionIDs(sol.SupportedEfforts), "ultra")
	assert.NotContains(t, agenttest.OptionIDs(luna.SupportedEfforts), "ultra")
	// A tier the CLI ships before this catalog catches up still renders
	// capitalized, not as a raw lowercase id beside its siblings.
	assert.Equal(t, "Turbo", providerkit.EffortLabel("turbo"), "an unlisted tier is capitalized, not raw")

	ultraRank, ok := providerkit.EffortRankOf("ultra")
	require.True(t, ok, "providerkit.EffortRank must rank \"ultra\" or it sorts after every ranked value")
	maxRank, ok := providerkit.EffortRankOf("max")
	require.True(t, ok)
	assert.Greater(t, ultraRank, maxRank, "\"ultra\" is stronger than \"max\"")
}

// TestCodexUpdateSettings_AccountDefaultRequiresRestart pins the model twin of the
// effort-auto guard. turn/start sends the stored model string as it is, and Codex
// rejects the literal id "default" ("The 'default' model is not supported"), so a
// live switch to the account default must ask for a relaunch instead of writing
// the sentinel into a.model where the next turn would forward it.
func TestCodexUpdateSettings_AccountDefaultRequiresRestart(t *testing.T) {
	t.Parallel()

	ag, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.model = "gpt-5.6-sol"

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: agent.DefaultModelSentinel})

	assert.False(t, result.AppliedLive, "a switch to the account default needs a relaunch")
	assert.Equal(t, "gpt-5.6-sol", ag.model, "the sentinel must never reach a.model on a live agent")

	// An edit that leaves the model alone carries no model key, which reads as an
	// empty value. That is "not supplied", not "the account default", so it must
	// apply live -- the whole reason this guard tests the sentinel exactly.
	unrelated := ag.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"})
	assert.True(t, unrelated.AppliedLive, "an edit on another axis must not demand a relaunch")
	assert.Equal(t, "gpt-5.6-sol", ag.model)
	assert.Equal(t, "high", ag.effort)

	// Re-selecting the model the agent already runs is a no-op, not a relaunch.
	same := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "gpt-5.6-sol"})
	assert.True(t, same.AppliedLive, "re-selecting the current model applies live")
}

// A concrete model still applies live: the restart guard must not catch every
// model change, only the move to the account default.
func TestCodexUpdateSettings_ConcreteModelAppliesLive(t *testing.T) {
	t.Parallel()

	ag, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.model = "gpt-5.6-sol"

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "gpt-5.6-luna"})

	assert.True(t, result.AppliedLive, "a concrete model applies without a relaunch")
	assert.Equal(t, "gpt-5.6-luna", ag.model)
}

// TestCodexSendTurnStartOmitsAccountDefaultModel covers the wire shape directly:
// turn/start must carry no model key for the account default, exactly as
// codexThreadParams omits it for thread/start.
func TestCodexSendTurnStartOmitsAccountDefaultModel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		model       string
		wantOmitted bool
	}{
		{name: "account default sentinel", model: agent.DefaultModelSentinel, wantOmitted: true},
		{name: "unset model", model: "", wantOmitted: true},
		{name: "concrete model", model: "gpt-5.6-sol"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// turn/start resolves only at turn end, so the ack comes from the
			// turn/started notification. Release it when the request ARRIVES,
			// which the harness records just before it calls this responder.
			//
			// Not on a sleep: sendTurnStart writes the request on a detached
			// goroutine and returns as soon as the ack closes, so a fixed sleep
			// can release it before that goroutine ran. The assertion below then
			// reads an empty slice, which is how this test failed under a loaded
			// suite while the wire shape it checks was correct.
			var a *Agent
			var requests func() []codexRecordedRequest
			a, _, requests = newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
				if method == "turn/start" {
					a.Mu.Lock()
					ack := a.turnStartAck
					a.turnStartAck = nil
					a.Mu.Unlock()
					if ack != nil {
						close(ack)
					}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "thread-1"

			err := a.sendTurnStart("thread-1", []map[string]interface{}{{"type": "text", "text": "hi"}}, turnSettings{
				model:          test.model,
				approvalPolicy: DefaultApprovalPolicy,
			})
			require.NoError(t, err)

			require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)
			sent := requests()
			require.Len(t, sent, 1)
			assert.Equal(t, "turn/start", sent[0].Method)
			if test.wantOmitted {
				assert.NotContains(t, sent[0].Params, "model", "the account default lets Codex keep its resolved model")
			} else {
				assert.Equal(t, test.model, sent[0].Params["model"])
			}
		})
	}
}

// TestCodexEffortsDownFrom pins the ladder cut: a window runs from its top tier to
// the weakest one Codex offers, always behind the auto sentinel, and an unknown
// tier fails at once rather than silently shortening a menu.
func TestCodexEffortsDownFrom(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{agent.EffortAuto, "ultra", "max", agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, agenttest.OptionIDs(codexEffortsDownFrom("ultra")))
	assert.Equal(t, []string{agent.EffortAuto, agent.EffortXHigh, agent.EffortHigh, "medium", "low"}, agenttest.OptionIDs(codexEffortsDownFrom(agent.EffortXHigh)))
	assert.Equal(t, []string{agent.EffortAuto, "low"}, agenttest.OptionIDs(codexEffortsDownFrom("low")))

	// "minimal" is on effortLadder but not in codexEffortIDs. A filter dropped such
	// an id and returned a short menu; the cut refuses it.
	assert.Panics(t, func() { codexEffortsDownFrom("minimal") }, "a tier Codex does not offer must fail loudly")
	assert.Panics(t, func() { codexEffortsDownFrom("nonsense") }, "a typo must fail loudly")

	// The returned slice must not alias codexDefaultEfforts, or one model's menu
	// could rewrite another's.
	window := codexEffortsDownFrom("ultra")
	window[1] = &agent.EffortInfo{Id: "scribbled"}
	assert.Equal(t, "ultra", codexDefaultEfforts[1].Id, "the shared catalog must be untouched")
}

// TestCodexRetiredModelsStayResolvable pins the retirement rule: a model the
// current app server no longer lists stays in the catalog Hidden, so a session
// still pinned to it keeps its effort tiers and its context window while the
// picker stops offering it.
func TestCodexRetiredModelsStayResolvable(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"gpt-5.2", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini"} {
		model := agent.FindAvailableModel(codexDefaultModels, id)
		require.NotNil(t, model, "a retired model must stay resolvable by id: %q", id)
		assert.True(t, model.Hidden, "retired model %q must not appear in the picker", id)
		assert.NotEmpty(t, model.SupportedEfforts, "retired model %q keeps its effort tiers", id)
		assert.NotZero(t, model.ContextWindow, "retired model %q keeps its context window", id)
		assert.NotEmpty(t, model.Description, "retired model %q keeps its description", id)
	}

	// A Hidden row must never take the default badge away from the sentinel.
	assert.Equal(t, agent.DefaultModelSentinel, Registration().DefaultModel())
}

// TestCodexQueryAvailableModelsKeepsTheReportedDescription covers a model the
// static catalog does not carry -- an account-specific one, which is exactly what
// model/list exists to supply. Its description must come from the report rather
// than render blank.
func TestCodexQueryAvailableModelsKeepsTheReportedDescription(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{"data":[
			{"id":"gpt-daybreak-blue-latest","model":"gpt-daybreak-blue-latest","displayName":"Daybreak Blue","description":"Latest frontier model for defensive cybersecurity work.","defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low","description":"Low"}]},
			{"id":"gpt-5.6-sol","model":"gpt-5.6-sol","displayName":"gpt-5.6-sol","description":"the CLI wording","supportedReasoningEfforts":[{"reasoningEffort":"low","description":"Low"}]}
		]}`)}
	})

	models := agent.queryAvailableModels(time.Second)

	require.Len(t, models, 2)
	assert.Equal(t, "Daybreak Blue", models[0].DisplayName)
	assert.Equal(t, "Latest frontier model for defensive cybersecurity work.", models[0].Description,
		"a model the static catalog lacks keeps the description the CLI reported")

	// The curated wording still wins for a model the catalog does carry.
	assert.Equal(t, "Reliable agentic workhorse for everyday tasks", models[1].Description)
	assert.Equal(t, "GPT-5.6-Sol", models[1].DisplayName)
}

// TestCodexReconcileModelCatalog covers the two gaps between what model/list
// reports and what the picker must offer: the account-default sentinel, which the
// Codex CLI never lists, and a settled model the live list omits.
func TestCodexReconcileModelCatalog(t *testing.T) {
	t.Parallel()

	live := func() []*agent.ModelInfo {
		return []*agent.ModelInfo{
			{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol"},
			{Id: "gpt-5.4", DisplayName: "GPT-5.4"},
		}
	}

	t.Run("adds the account default so a tab can return to it", func(t *testing.T) {
		t.Parallel()

		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.availableModels = live()
		a.model = "gpt-5.6-sol"

		a.reconcileModelCatalog()

		require.NotNil(t, agent.FindAvailableModel(a.availableModels, agent.DefaultModelSentinel),
			"without the sentinel a user can never return to the account default")
		assert.Equal(t, agent.DefaultModelSentinel, a.availableModels[0].Id, "the sentinel leads the picker")
		assert.Empty(t, a.availableModels[0].SupportedEfforts, "the unresolved entry carries no effort menu")
	})

	t.Run("keeps a settled model the live list omits", func(t *testing.T) {
		t.Parallel()

		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.availableModels = live()
		a.model = "gpt-5.2" // retired: present in the static catalog, absent from model/list

		a.reconcileModelCatalog()

		settled := agent.FindAvailableModel(a.availableModels, "gpt-5.2")
		require.NotNil(t, settled, "an unlisted current model leaves the picker unselected")
		assert.NotZero(t, settled.ContextWindow, "the reinstated entry carries its capabilities")
		// gpt-5.2 is retired, so it sorts after every current model.
		assert.Equal(t, []string{agent.DefaultModelSentinel, "gpt-5.6-sol", "gpt-5.4", "gpt-5.2"}, modelIDsOf(a.availableModels))
	})

	t.Run("leaves a listed model and an already-present sentinel alone", func(t *testing.T) {
		t.Parallel()

		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.availableModels = append([]*agent.ModelInfo{{Id: agent.DefaultModelSentinel}}, live()...)
		a.model = "gpt-5.4"

		a.reconcileModelCatalog()

		assert.Equal(t, []string{agent.DefaultModelSentinel, "gpt-5.6-sol", "gpt-5.4"}, modelIDsOf(a.availableModels),
			"a complete catalog is left untouched")
	})

	t.Run("no-ops on an empty live list so the static fallback survives", func(t *testing.T) {
		t.Parallel()

		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.model = "gpt-5.6-sol"

		a.reconcileModelCatalog()

		assert.Empty(t, a.availableModels,
			"a singleton here would replace the full static fallback OptionGroups uses")
	})

	// An account-specific model is exactly what model/list exists to supply, and the
	// static catalog cannot know it. It must not push a catalog-known model out of
	// position when the settled model is inserted beside it.
	t.Run("ranks an account-specific model after every known one", func(t *testing.T) {
		t.Parallel()

		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.availableModels = []*agent.ModelInfo{
			{Id: "gpt-daybreak-blue-latest", DisplayName: "Daybreak Blue"},
			{Id: "gpt-5.4", DisplayName: "GPT-5.4"},
		}
		a.model = "gpt-5.6-sol" // known to the static catalog, absent from this live list

		a.reconcileModelCatalog()

		// The insert positions the SETTLED model only; it never reorders what
		// model/list reported, whose order is the CLI's own. Daybreak ranks last
		// because neither catalog knows it, so gpt-5.6-sol lands before it -- and
		// gpt-5.4 keeps the place the CLI gave it, behind Daybreak.
		assert.Equal(t, []string{agent.DefaultModelSentinel, "gpt-5.6-sol", "gpt-daybreak-blue-latest", "gpt-5.4"},
			modelIDsOf(a.availableModels))
	})

	t.Run("leaves an unknown settled model unlisted", func(t *testing.T) {
		t.Parallel()

		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.availableModels = live()
		a.model = "gpt-9-unreleased"

		a.reconcileModelCatalog()

		assert.Nil(t, agent.FindAvailableModel(a.availableModels, "gpt-9-unreleased"),
			"a model neither catalog knows carries no capabilities to surface")
	})
}

func modelIDsOf(models []*agent.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.Id)
	}
	return ids
}

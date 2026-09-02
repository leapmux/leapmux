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

func TestCodexUpdateSettingsPublishesRequestedValues(t *testing.T) {
	t.Parallel()

	agent, sink, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})

	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:            "gpt-5.2",
		OptionIDEffort:           "low",
		OptionIDPermissionMode:   "never",
		CodexOptionSandboxPolicy: "read-only",
		CodexOptionServiceTier:   "fast",
	})
	require.True(t, updated.AppliedLive)

	assert.Empty(t, requests(), "active settings must not read thread-agnostic global config")
	assert.Equal(t, "gpt-5.2", agent.model)
	assert.Equal(t, "low", agent.effort)
	assert.Equal(t, "never", agent.approvalPolicy)
	assert.Equal(t, "read-only", agent.sandboxPolicy)
	assert.Equal(t, "fast", agent.serviceTier)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.2", refresh.Model)
	assert.Equal(t, "low", refresh.Effort)
	assert.Equal(t, "never", refresh.PermissionMode)
}

func TestCodexUpdateSettingsPreservesRequestedThreadSettings(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})

	updated := agent.UpdateSettings(map[string]string{
		OptionIDEffort:           "low",
		OptionIDPermissionMode:   "never",
		CodexOptionSandboxPolicy: CodexSandboxDangerFullAccess,
		CodexOptionNetworkAccess: CodexNetworkEnabled,
		CodexOptionServiceTier:   CodexServiceTierFast,
	})

	require.True(t, updated.AppliedLive)
	assert.Equal(t, "low", agent.effort)
	assert.Equal(t, "never", agent.approvalPolicy)
	assert.Equal(t, CodexSandboxDangerFullAccess, agent.sandboxPolicy)
	assert.Equal(t, CodexNetworkEnabled, agent.networkAccess)
	assert.Equal(t, CodexServiceTierFast, agent.serviceTier)
	assert.Empty(t, requests())
}

func TestCodexPublishSettings_AutoFallsBackToModelDefault(t *testing.T) {
	t.Parallel()

	agent, sink, _ := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"
	agent.model = "gpt-5.4"
	agent.availableModels = []*ModelInfo{
		{Id: "gpt-5.4", DefaultEffort: "high"},
		{Id: "gpt-5.2", DefaultEffort: "medium"},
	}

	agent.publishSettings()

	assert.Equal(t, "high", agent.effort,
		"auto should fall back to the current model's default from the catalog")

	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "high", sink.LastSettingsRefresh().Effort,
		"broadcast should carry the resolved effort so the UI updates")
}

func TestCodexPublishSettings_AutoNoModelCatalogStaysAuto(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
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
	require.False(t, updated.AppliedLive, "switching to \"auto\" should request a restart")

	assert.Equal(t, "high", agent.effort, "live effort must stay untouched until restart")
	assert.Empty(t, requests(), "a rejected update must not publish an RPC")
}

// TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto verifies that when the
// agent is already in "auto", a redundant "auto" update does not trigger
// a restart.
func TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"

	updated := agent.UpdateSettings(map[string]string{OptionIDEffort: "auto"})
	require.True(t, updated.AppliedLive, "a no-op \"auto\"→\"auto\" should not request a restart")
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

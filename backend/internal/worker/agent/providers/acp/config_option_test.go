package acp

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shared Agent Client Protocol (ACP) config-option machinery, exercised through
// testAgent. Its peer declares a reasoning axis under a convention config-option
// id (`thinking_effort`) rather than the well-known `effort`, which is the case these
// paths exist for. The behavior belongs to Base, so every provider that embeds it
// inherits what passes here.

// The base UpdateSettings (permission-mode providers) reads only model and
// permissionMode, so an unknown extras key (a future config-option axis) is structurally
// ignored: model + mode RPCs fire, nothing else, and the write succeeds.
func TestUpdateSettings_IgnoresUnknownExtraKey(t *testing.T) {
	a, requests := newTestAgentForRPC(t)
	a.availableModes = []*leapmuxv1.AvailableOption{
		{Id: testModeAuto, Name: "Auto"},
		{Id: testModeApprove, Name: "Approve"},
	}

	ok := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:          "gpt-5.4",
		agent.OptionIDPermissionMode: testModeApprove,
		"thoughtLevel":               "high",
	})

	require.True(t, ok.AppliedLive)
	recorded := requests()
	require.Len(t, recorded, 2, "model + mode RPCs fire; the unknown extra key sends nothing")
	assert.Equal(t, MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, ConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, MethodSessionSetMode, recorded[1].Method)
}

func TestACPUpdateSettingsSendsLiveRequests(t *testing.T) {
	a, requests := newTestAgentForRPC(t)
	a.availableModes = []*leapmuxv1.AvailableOption{
		{Id: testModeAuto, Name: "Auto"},
		{Id: testModeApprove, Name: "Approve"},
	}

	updated := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:          "gpt-5.4-mini",
		agent.OptionIDPermissionMode: testModeApprove,
	})
	require.True(t, updated.AppliedLive)
	assert.Equal(t, "gpt-5.4-mini", a.model)
	assert.Equal(t, testModeApprove, a.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, ConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, "gpt-5.4-mini", recorded[0].Params["value"])
	assert.Equal(t, MethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, testModeApprove, recorded[1].Params["modeId"])
}

func TestACPCancelSessionSendsSessionCancel(t *testing.T) {
	agent, requests := newTestAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == MethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

// applyStartupPermissionMode pushes a requested mode that differs from the
// server's current, is a no-op when empty or already matching, and propagates a
// rejection as a fatal error. The current mode is read under the lock (it shares
// the field the reader goroutine writes), mirroring trySetStartupModel.

func TestApplyStartupPermissionMode_PushesWhenDiffers(t *testing.T) {
	agent, requests := newTestAgentForRPC(t)
	agent.permissionMode = testModeAuto

	require.NoError(t, agent.applyStartupPermissionMode(testModeApprove, false))

	assert.Equal(t, testModeApprove, agent.permissionMode)
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, MethodSessionSetMode, recorded[0].Method)
	assert.Equal(t, testModeApprove, recorded[0].Params["modeId"])
}

func TestApplyStartupPermissionMode_NoopWhenMatchesCurrent(t *testing.T) {
	agent, requests := newTestAgentForRPC(t)
	agent.permissionMode = testModeApprove

	require.NoError(t, agent.applyStartupPermissionMode(testModeApprove, false))

	assert.Empty(t, requests(), "no set_mode when the request already matches the server's current")
}

func TestApplyStartupPermissionMode_NoopWhenEmpty(t *testing.T) {
	agent, requests := newTestAgentForRPC(t)
	agent.permissionMode = testModeAuto

	require.NoError(t, agent.applyStartupPermissionMode("", false))

	assert.Empty(t, requests(), "an empty requested mode is a no-op")
}

func TestApplyStartupPermissionMode_RejectionIsFatal(t *testing.T) {
	agent, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetMode {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"unknown mode"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	agent.permissionMode = testModeAuto

	err := agent.applyStartupPermissionMode(testModeApprove, false)
	require.Error(t, err, "a rejected mode must surface so the caller aborts startup")
}

// A mode LeapMux chose, not the user, must not kill the session when this build does not
// offer it: the safe default degrades to whatever mode the handshake reported. Goose is
// the provider that ships one (smart_approve), and a build without that mode would
// otherwise fail EVERY new session.
func TestApplyStartupPermissionMode_SafeDefaultDegradesWhenUnavailable(t *testing.T) {
	agent, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetMode {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"unknown mode"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	agent.permissionMode = testModeAuto
	agent.availableModes = []*leapmuxv1.AvailableOption{{Id: testModeAuto}}

	require.NoError(t, agent.applyStartupPermissionMode(testModeApprove, true),
		"a safe default this session does not offer must not abort startup")
	assert.Empty(t, requests(), "the mode is never pushed, so the session keeps the one it reported")
	assert.Equal(t, testModeAuto, agent.permissionMode)

	// The same mode asked for EXPLICITLY still aborts, so a typed --permission-mode this
	// build cannot enter is reported rather than silently downgraded.
	require.Error(t, agent.applyStartupPermissionMode(testModeApprove, false))
}

// seedThinkingEffort surfaces a thinking_effort config option, as a
// handshake would, returning the agent ready for a settings write.
func seedThinkingEffort(agent *testAgent, current string) {
	agent.Mu.Lock()
	agent.applyOptionGroupsLocked([]ConfigOption{{
		ID:           testThinkingEffort,
		Category:     "thought_level",
		Name:         "Reasoning Effort",
		CurrentValue: current,
		Options: []ConfigOptionValue{
			{Value: "low", Name: "low"}, {Value: "medium", Name: "medium"}, {Value: "high", Name: "high"},
		},
	}})
	agent.Mu.Unlock()
}

// TestSetConfigOptionGuarded_PreconditionGatesWrite is the [S2] guard for the tightened
// raiseEffortOffNone race window: the optional precondition is evaluated under the WRITE's own
// b.Mu acquisition (the latest point before the send), so a write predicated on stale state can be
// skipped without holding the lock across the RPC. A false precondition must NOT send a
// session/set_config_option RPC (no-op success); a true one must.
func TestSetConfigOptionGuarded_PreconditionGatesWrite(t *testing.T) {
	ag, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"low","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.sink = agent.NewProviderServices(&agenttest.Sink{})
	seedThinkingEffort(ag, "high") // marks thinking_effort known + offered (low/medium/high)

	effortWrites := func() int {
		n := 0
		for _, r := range requests() {
			if r.Method == MethodSessionSetConfigOption && r.Params["configId"] == testThinkingEffort {
				n++
			}
		}
		return n
	}

	// A false precondition skips the write entirely.
	require.NoError(t, ag.setConfigOptionGuarded(testThinkingEffort, "low", func() bool { return false }))
	assert.Equal(t, 0, effortWrites(), "a write predicated on a false precondition is skipped")

	// A true precondition lets the write through.
	require.NoError(t, ag.setConfigOptionGuarded(testThinkingEffort, "low", func() bool { return true }))
	assert.Equal(t, 1, effortWrites(), "a write predicated on a true precondition is sent")
}

// TestACPConfigOption_MutableUpdateRoundTrips verifies a config option
// (Goose's thinking_effort) is surfaced mutable, and a settings change is written
// via session/set_config_option with the new value adopted from the response.
func TestACPConfigOption_MutableUpdateRoundTrips(t *testing.T) {
	ag, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[
				{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	ag.sink = agent.NewProviderServices(sink)
	seedThinkingEffort(ag, "medium")

	// Surfaced mutable, at its server-reported current.
	g := optionids.GroupByID(ag.OptionGroups(), testThinkingEffort)
	require.NotNil(t, g)
	assert.True(t, g.GetMutable(), "a config option is now writable")
	assert.Equal(t, "medium", g.GetCurrentValue())

	// Changing it routes through session/set_config_option and adopts the response value.
	ok := ag.UpdateSettings(map[string]string{testThinkingEffort: "high"})
	assert.True(t, ok.AppliedLive)

	var setReq *agenttest.RecordedRequest
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "UpdateSettings sent session/set_config_option")
	assert.Equal(t, testThinkingEffort, setReq.Params["configId"])
	assert.Equal(t, "high", setReq.Params["value"])

	assert.Equal(t, "high", optionids.GroupByID(ag.OptionGroups(), testThinkingEffort).GetCurrentValue(),
		"the new value is adopted from the set_config_option response")

	// A pure current-value change keeps the same option set, so it must NOT broadcast a catalog
	// refresh -- the new value rides the settings reply; broadcasting here would fire a redundant
	// statusChange on every effort/mode edit.
	assert.Equal(t, 0, sink.StatusActiveCount(),
		"a value-only change must not broadcast a catalog refresh")
}

// TestACPConfigOption_SkipsUnofferedValue is the regression guard for the stale-tier push: a
// settings write whose value the current option list does NOT offer (e.g. an effort tier
// inherited from a prior model that the new model dropped) is SKIPPED rather than force-pushed,
// so the daemon never sees a value it would reject and bounce UpdateSettings into a relaunch.
func TestACPConfigOption_SkipsUnofferedValue(t *testing.T) {
	ag, requests := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.sink = agent.NewProviderServices(&agenttest.Sink{})
	// The current model offers only low/medium/high.
	seedThinkingEffort(ag, "medium")

	// A change to "xhigh" -- a tier the current list does not offer.
	ok := ag.UpdateSettings(map[string]string{testThinkingEffort: "xhigh"})
	assert.True(t, ok.AppliedLive, "skipping an unoffered value is a no-op success, not a failure")

	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption && r.Params["configId"] == testThinkingEffort {
			t.Fatalf("an unoffered value must not be pushed to the daemon, got value=%v", r.Params["value"])
		}
	}
	assert.Equal(t, "medium", optionids.GroupByID(ag.OptionGroups(), testThinkingEffort).GetCurrentValue(),
		"the running session keeps its actual value when an unoffered write is skipped")
}

// TestACPModelChangeSurfacesReasoningEffort is the thinking_effort parity for
// TestStartOpenCode_ModelChangeSurfacesEffort: switching to a reasoning-capable model must
// surface the daemon's thinking_effort group. Goose, like OpenCode, returns the refreshed
// configOptions from session/set_config_option (configId "model") but emits no
// config_option_update notification, so the live model write must BOTH fold the response (so the
// group exists in agent state) AND broadcast a status refresh (so the new group reaches the
// settings panel, which rebuilds its catalog only from statusChange events).
func TestACPModelChangeSurfacesReasoningEffort(t *testing.T) {
	ag, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			// The new model supports reasoning, so its refreshed configOptions now carry the
			// thinking_effort axis (the prior model offered none).
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[
				{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"medium","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.model = "gpt-5.4-mini"
	sink := &agenttest.Sink{}
	ag.sink = agent.NewProviderServices(sink)

	// The prior model surfaced no thinking_effort axis.
	require.Nil(t, optionids.GroupByID(ag.OptionGroups(), testThinkingEffort),
		"the prior model surfaced no thinking_effort group")

	// Switch to the reasoning-capable model.
	require.True(t, ag.UpdateSettings(map[string]string{agent.OptionIDModel: "gpt-5.4"}).AppliedLive)

	// The model write went through session/set_config_option (configId "model"), not set_model.
	var setReq *agenttest.RecordedRequest
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the model write goes through session/set_config_option")
	assert.Equal(t, ConfigOptionIDModel, setReq.Params["configId"])
	assert.Equal(t, "gpt-5.4", setReq.Params["value"])

	// The refreshed configOptions surfaced thinking_effort at its server-reported current.
	groups := ag.OptionGroups()
	effort := optionids.GroupByID(groups, testThinkingEffort)
	require.NotNil(t, effort, "thinking_effort must surface after switching to a reasoning-capable model")
	require.Len(t, effort.GetOptions(), 3)
	assert.Equal(t, "medium", agent.CurrentOptions(groups)[testThinkingEffort])

	// The option-group set changed, so a status refresh must broadcast the new catalog -- the
	// settings panel rebuilds its option groups only from statusChange events.
	assert.Equal(t, 1, sink.StatusActiveCount(),
		"a status refresh must broadcast the new thinking_effort group to the frontend")
}

// TestACPConfigOption_EmptyResponseAdoptsWrittenValue is the regression guard for
// [E10]: a server that accepts session/set_config_option but echoes no refreshed
// configOptions (off-spec, but possible) must not leave the option at its stale prior value.
// The write succeeded, so the value we wrote is authoritative and is recorded optimistically
// -- otherwise applySettingsLive's readback would persist the stale value and revert the
// user's choice.
func TestACPConfigOption_EmptyResponseAdoptsWrittenValue(t *testing.T) {
	ag, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		// Every method (incl. set_config_option) succeeds but returns no configOptions.
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	seedThinkingEffort(ag, "medium")

	ok := ag.UpdateSettings(map[string]string{testThinkingEffort: "high"})
	assert.True(t, ok.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, ok.Settlements[testThinkingEffort].State,
		"an empty response cannot confirm a clamp or removal")
	assert.Equal(t, "high", optionids.GroupByID(ag.OptionGroups(), testThinkingEffort).GetCurrentValue(),
		"the written value is adopted even when the response carries no configOptions")
}

// TestACPConfigOption_EffortSortedStrongestFirst verifies a thought_level
// (reasoning-effort) config option is reordered strongest-first, regardless of the
// weakest-first order the server reports.
func TestACPConfigOption_EffortSortedStrongestFirst(t *testing.T) {
	agent, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.Mu.Lock()
	agent.applyOptionGroupsLocked([]ConfigOption{{
		ID: "thinking_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "medium",
		Options: []ConfigOptionValue{
			{Value: "none"}, {Value: "low"}, {Value: "medium"}, {Value: "high"}, {Value: "xhigh"},
		},
	}})
	agent.Mu.Unlock()

	g := optionids.GroupByID(agent.OptionGroups(), testThinkingEffort)
	require.NotNil(t, g)
	var ids []string
	for _, o := range g.GetOptions() {
		ids = append(ids, o.GetId())
	}
	assert.Equal(t, []string{"xhigh", "high", "medium", "low", "none"}, ids)
}

// TestSortEffortOptionsDescending_RanksUnrankedLast guards the degenerate path: a
// provider-specific variant the rank table doesn't know sorts after every ranked
// value, and -- crucially -- does NOT act as a barrier that leaves the ranked entries
// mis-ordered. With the old "incomparable" comparator [low, default, high] stayed put
// (low ahead of high); the strict-weak-ordering comparator now sorts the ranked pair.
func TestSortEffortOptionsDescending_RanksUnrankedLast(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "low"}, {Id: "default"}, {Id: "high"}}
	providerkit.SortEffortsDescending(opts)
	ids := func() []string {
		out := make([]string, len(opts))
		for i, o := range opts {
			out[i] = o.GetId()
		}
		return out
	}()
	assert.Equal(t, []string{"high", "low", "default"}, ids,
		"ranked values sort strongest-first; the unranked value sorts last")
}

// TestSortEffortOptionsDescending_StableAmongUnranked verifies multiple unranked
// values keep their relative (server-reported) order while sorting after ranked ones.
func TestSortEffortOptionsDescending_StableAmongUnranked(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "alpha"}, {Id: "high"}, {Id: "beta"}, {Id: "low"}}
	providerkit.SortEffortsDescending(opts)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.GetId()
	}
	assert.Equal(t, []string{"high", "low", "alpha", "beta"}, ids,
		"ranked strongest-first, then unranked in their original order")
}

// TestSortEffortOptionsDescending_CaseInsensitive verifies a server reporting effort ids
// in mixed case (e.g. "High"/"LOW") is still ranked rather than dumped into the unranked
// tail -- providerkit.EffortRankOf lowercases before the lookup.
func TestSortEffortOptionsDescending_CaseInsensitive(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "LOW"}, {Id: "High"}, {Id: "Medium"}}
	providerkit.SortEffortsDescending(opts)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.GetId()
	}
	assert.Equal(t, []string{"High", "Medium", "LOW"}, ids,
		"mixed-case effort ids rank by intensity, preserving their original spelling")
}

// TestSortEffortOptionsDescending_KnownSynonyms verifies the common separator/spelling
// variants share a rank with their canonical name (e.g. "very_high" == "xhigh"), so a
// mid-tier synonym is not stranded after every ranked value.
func TestSortEffortOptionsDescending_KnownSynonyms(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "moderate"}, {Id: "very_high"}, {Id: "minimal"}}
	providerkit.SortEffortsDescending(opts)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.GetId()
	}
	assert.Equal(t, []string{"very_high", "moderate", "minimal"}, ids,
		"synonyms (very_high~xhigh, moderate~medium) rank by intensity, not as unknowns")
}

// TestChooseDefaultEffort covers the value installed in place of a daemon's "none" default:
// "high" when offered, otherwise the offered level CLOSEST TO high with ties broken toward the
// stronger level. none/off and unranked provider-specific values are never chosen.
func TestChooseDefaultEffort(t *testing.T) {
	option := func(values ...string) ConfigOption {
		o := ConfigOption{Category: acpConfigOptionCategoryThoughtLevel}
		for _, v := range values {
			o.Options = append(o.Options, ConfigOptionValue{Value: v})
		}
		return o
	}
	cases := []struct {
		name   string
		option ConfigOption
		want   string
	}{
		{"high offered wins outright", option("low", "medium", "high"), "high"},
		{"no high: medium is closest", option("low", "medium"), "medium"},
		{"no high/medium: low is closest", option("minimal", "low"), "low"},
		{"none and off are never chosen", option("none", "off", "low", "medium", "high"), "high"},
		{"closest-to-high beats a farther level", option("low", "xhigh"), "xhigh"},                // low d2, xhigh d1
		{"equidistant ties toward the stronger level", option("low", "max"), "max"},               // both d2 -> higher
		{"synonyms rank like their canonical name", option("moderate", "very-high"), "very-high"}, // d1 each -> stronger
		{"only none/off yields no choice", option("none", "off"), ""},
		{"only unranked values yields no choice", option("default", "custom"), ""},
		{"empty option list yields no choice", option(), ""},
		{"case-insensitive", option("Low", "High"), "High"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chooseDefaultEffort(tc.option))
		})
	}
}

// TestACPConfigOption_DedupsDuplicateIDs verifies a (non-conforming) server that
// reports the same config-option id twice surfaces a SINGLE group, not two sharing a key
// -- two groups with the same id corrupt the frontend's id-keyed <For> reconciliation.
func TestACPConfigOption_DedupsDuplicateIDs(t *testing.T) {
	agent, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.Mu.Lock()
	agent.applyOptionGroupsLocked([]ConfigOption{
		{ID: "allow_all", Name: "Allow All", CurrentValue: "off", Options: []ConfigOptionValue{{Value: "off"}, {Value: "on"}}},
		{ID: "allow_all", Name: "Allow All (dup)", CurrentValue: "on", Options: []ConfigOptionValue{{Value: "off"}, {Value: "on"}}},
	})
	agent.Mu.Unlock()

	groups := agent.OptionGroups()
	count := 0
	for _, g := range groups {
		if g.GetId() == "allow_all" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a duplicate config-option id surfaces a single group")
	assert.Equal(t, "off", optionids.GroupByID(groups, "allow_all").GetCurrentValue(),
		"the first sighting wins")
}

// TestACPConfigOption_EmptyCurrentRecoverable verifies the empty-current recovery
// fix: an option the server advertises with an EMPTY current at handshake is not surfaced
// yet but IS recorded as known, so re-pushing its persisted preference via
// set_config_option is accepted (not rejected as "unknown config option") and surfaces it.
func TestACPConfigOption_EmptyCurrentRecoverable(t *testing.T) {
	agent, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"high","name":"high"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	// Handshake reports thinking_effort with an empty current and nothing stored.
	agent.Mu.Lock()
	agent.applyOptionGroupsLocked([]ConfigOption{{
		ID: "thinking_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}})
	agent.Mu.Unlock()

	assert.Nil(t, optionids.GroupByID(agent.OptionGroups(), testThinkingEffort),
		"an option with an empty current is not surfaced until a concrete value is known")

	// Re-pushing the persisted preference is accepted (recorded as known) and surfaces it.
	require.NoError(t, agent.setConfigOption("thinking_effort", "high"),
		"an empty-current option must be pushable so its persisted preference can be recovered")
	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), testThinkingEffort).GetCurrentValue())
}

// TestStaticSecondaryGroup_CarriesDefault verifies the static fallback secondary group
// (served before the session reports its catalog) carries a DefaultValue (default-or-first
// option), matching the live secondaryOptionGroupLocked -- so a fresh tab shows a default
// badge instead of none until the handshake lands.
func TestStaticSecondaryGroup_CarriesDefault(t *testing.T) {
	groups := StaticSecondaryGroup(ModeChannelPermissionMode, []*leapmuxv1.AvailableOption{
		{Id: "default"}, {Id: "plan"},
	})
	require.Len(t, groups, 1)
	assert.Equal(t, agent.OptionIDPermissionMode, groups[0].GetId())
	assert.Equal(t, "Mode", groups[0].GetLabel())
	assert.Equal(t, "default", groups[0].GetDefaultValue(),
		"the static fallback marks the default-or-first option as default")
}

// TestSecondaryOptionGroupLocked_DefaultIsProviderDefaultNotCurrent is the regression guard
// for the live secondary group's DefaultValue: it must mark the provider default (default-or-
// first option), NOT the user's current selection -- so the picker's default badge stays put
// instead of following the selection around, matching the static fallback
// (TestStaticSecondaryGroup_CarriesDefault).
func TestSecondaryOptionGroupLocked_DefaultIsProviderDefaultNotCurrent(t *testing.T) {
	agent, _ := newTestAgentForRPC(t)
	agent.Mu.Lock()
	agent.availableModes = []*leapmuxv1.AvailableOption{{Id: "default"}, {Id: "plan"}}
	agent.permissionMode = "plan" // current selection differs from the default-or-first option
	g := agent.secondaryOptionGroupLocked()
	agent.Mu.Unlock()

	require.NotNil(t, g)
	assert.Equal(t, "plan", g.GetCurrentValue(), "current reflects the live selection")
	assert.Equal(t, "default", g.GetDefaultValue(),
		"default marks the provider default (first option), not the current selection")
}

// TestACPConfigOption_NoopWhenUnchanged verifies UpdateSettings does not write a
// config option whose value already matches the current selection.
func TestACPConfigOption_NoopWhenUnchanged(t *testing.T) {
	agent, requests := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	seedThinkingEffort(agent, "medium")

	require.True(t, agent.UpdateSettings(map[string]string{testThinkingEffort: "medium"}).AppliedLive)
	for _, r := range requests() {
		assert.NotEqual(t, MethodSessionSetConfigOption, r.Method, "no write when the value is unchanged")
	}
}

// TestACPConfigOption_PreservesValueOnTransientEmptyCurrent verifies a select
// always has a value, so a server-reported empty current (a transient/partial
// config_option_update) keeps the prior selection rather than wiping it -- which
// mergeOptionValues would otherwise propagate as a delete.
func TestACPConfigOption_PreservesValueOnTransientEmptyCurrent(t *testing.T) {
	agent, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	seedThinkingEffort(agent, "high")

	agent.Mu.Lock()
	// The server re-reports the same option with an empty current value.
	agent.applyOptionGroupsLocked([]ConfigOption{{
		ID: "thinking_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}})
	got := agent.options.values["thinking_effort"]
	agent.Mu.Unlock()

	assert.Equal(t, "high", got, "a transient empty current must not wipe the stored selection")
	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), testThinkingEffort).GetCurrentValue(),
		"the projected group keeps the preserved current too")
}

// TestACPConfigOption_ClearContextReconcilesStoredValueNotInList is the regression guard for
// the resolveCurrent membership check: on a ClearContext refresh (preferStoredValue), a stored
// value the new session no longer offers must NOT be surfaced as the group's current -- doing
// so would render a selection absent from its own option list. The payload's authoritative
// CurrentValue wins instead, mirroring reconcileCurrentOptionID on the model/mode channels.
func TestACPConfigOption_ClearContextReconcilesStoredValueNotInList(t *testing.T) {
	agent, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.Mu.Lock()
	// A prior session stored "xhigh"; a re-push the new session rejected left it lingering.
	agent.options.values = map[string]string{testThinkingEffort: "xhigh"}
	agent.options.markKnown(ConfigOption{ID: "thinking_effort"})
	agent.options.markSurfaced("thinking_effort")
	// ClearContext refresh: the new session offers only [low, high] and reports low as current.
	agent.applyOptionGroupsKeepingStoredLocked([]ConfigOption{{
		ID: "thinking_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "low",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}})
	got := agent.options.values["thinking_effort"]
	agent.Mu.Unlock()

	assert.Equal(t, "low", got, "a stored value absent from the new option list reconciles to the payload current")
	g := optionids.GroupByID(agent.OptionGroups(), testThinkingEffort)
	require.NotNil(t, g)
	assert.Equal(t, "low", g.GetCurrentValue(), "the surfaced current is selectable from the group's own options")
	assert.True(t, HasOption(g.GetOptions(), g.GetCurrentValue()),
		"the surfaced current must be one of the group's listed options")
}

// TestACPConfigOption_StartupReappliesPersistedValue verifies a persisted
// option preference is re-pushed after a (relaunch) handshake whose server reports a
// different default, so the user's choice survives a fresh process.
func TestACPConfigOption_StartupReappliesPersistedValue(t *testing.T) {
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[
				{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"low","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	// The handshake surfaced the server default (medium); the launch options carry the
	// user's persisted preference (low).
	seedThinkingEffort(a, "medium")
	a.applyStartupOptions(agent.Options{Options: map[string]string{testThinkingEffort: "low"}})

	var setReq *agenttest.RecordedRequest
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the persisted preference is re-pushed on startup")
	assert.Equal(t, "low", setReq.Params["value"])
}

// TestACPConfigOption_StartupMapsEnvEffortOntoDeclaredID verifies the operator env-effort override
// (resolveProviderDefaults stores it under the well-known "effort" id) is re-pushed onto the
// provider's DECLARED effort config id at startup, even though the override never carries the
// daemon's own id ("thinking_effort"). This is the provider-declaration replacement for the old
// live well-known-id scan: Goose declares effortConfigID = "thinking_effort" in configure.
func TestACPConfigOption_StartupMapsEnvEffortOntoDeclaredID(t *testing.T) {
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[
				{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	// A provider sets this in configure, as Goose does. The test agent has no configure.
	a.hooks.EffortConfigID = testThinkingEffort
	// The handshake surfaced the server default (medium); the env override (high) is stored under
	// the well-known "effort" id, NOT under "thinking_effort".
	seedThinkingEffort(a, "medium")
	a.applyStartupOptions(agent.Options{Options: map[string]string{agent.OptionIDEffort: "high"}})

	var setReq *agenttest.RecordedRequest
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the env-effort override is re-pushed onto the declared effort id")
	assert.Equal(t, testThinkingEffort, setReq.Params["configId"],
		"the well-known \"effort\" override maps onto the declared \"thinking_effort\" axis")
	assert.Equal(t, "high", setReq.Params["value"])
}

// TestACPConfigOption_LiveUpdateAppliesKnownButUnsurfacedOption is the [V8] regression guard:
// a live settings edit must reach a config option that is KNOWN (advertised at handshake) but
// not yet surfaced with a value -- the same advertised-with-empty-current case applyStartupOptions
// handles. forEachOption iterating only the surfaced values would silently skip it, yet
// UpdateSettings would still return true, so the service would persist/broadcast a value the live
// session never applied until the next relaunch. The fix iterates the union of known + valued ids.
func TestACPConfigOption_LiveUpdateAppliesKnownButUnsurfacedOption(t *testing.T) {
	agent, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[
				{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"high","name":"high"}]}
			]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	// Advertised at handshake (known) but never surfaced with a value (no seedThinkingEffort).
	agent.Mu.Lock()
	agent.options.markKnown(ConfigOption{ID: "thinking_effort"})
	agent.Mu.Unlock()

	require.True(t, agent.UpdateSettings(map[string]string{testThinkingEffort: "high"}).AppliedLive,
		"a known config option is applied live")

	var setReq *agenttest.RecordedRequest
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the known-but-unsurfaced option is written via session/set_config_option, not silently skipped")
	assert.Equal(t, testThinkingEffort, setReq.Params["configId"])
	assert.Equal(t, "high", setReq.Params["value"])
}

// TestACPSessionRPCs_ConcurrentWithClearContext stresses the S3 coordination: ClearContext
// (session/new + the sessionID swap, under sessionMu.Lock via newSessionLocked) and the
// session/* RPCs (under sessionMu.RLock via WithSessionID) run concurrently. Under -race
// this catches a data race or a deadlock, and every recorded session-scoped request must
// have carried a concrete (non-empty) sessionId -- never one observed mid-swap.
func TestACPSessionRPCs_ConcurrentWithClearContext(t *testing.T) {
	var sessionSeq atomic.Int64
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		switch method {
		case MethodSessionNew:
			return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(`{"sessionId":"session-%d"}`, sessionSeq.Add(1)))}
		case MethodSessionSetConfigOption:
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"high","name":"high"}]}]}`)}
		default:
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	})
	a.sink = agent.NewProviderServices(&agenttest.Sink{}) // ClearContext broadcasts the new session id through the sink
	// Surface thinking_effort so setConfigOption's known-id gate accepts it.
	seedThinkingEffort(a, "high")

	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	for range 8 {
		run(func() {
			_, err := a.ClearContext()
			assert.NoError(t, err)
		})
		run(func() { _ = a.setConfigOption(testThinkingEffort, "low") })
		run(func() { _ = a.SetModelViaConfigOption("gpt-5") })
		run(func() { _ = a.cancelSession() })
	}
	wg.Wait()

	for _, r := range requests() {
		if sid, ok := r.Params["sessionId"].(string); ok {
			assert.NotEmpty(t, sid, "%s carried an empty sessionId", r.Method)
		}
	}
}

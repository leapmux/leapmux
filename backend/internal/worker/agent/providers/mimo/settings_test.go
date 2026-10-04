package mimo

import (
	"net/http"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionGroups(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	a.effort = "high"

	groups := a.OptionGroups()
	model := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, model)
	assert.Equal(t, "mock/alpha", model.GetCurrentValue())
	effort := optionids.GroupByID(groups, agent.OptionIDEffort)
	require.NotNil(t, effort)
	assert.Equal(t, "high", effort.GetCurrentValue())
	modes := optionids.GroupByID(groups, agent.OptionIDPermissionMode)
	require.NotNil(t, modes)
	assert.Equal(t, contracts.MiMoModeBuild, modes.GetCurrentValue())
	assert.Equal(t, []string{"build", "plan", "max"}, optionIDs(modes), "the server's primary agents are the modes")
	policy := optionids.GroupByID(groups, contracts.MiMoOptionPermissionPolicy)
	require.NotNil(t, policy)
	assert.Equal(t, contracts.MiMoPermissionPolicyAsk, policy.GetCurrentValue())

	a.catalog.modes = nil
	modes = optionids.GroupByID(a.OptionGroups(), agent.OptionIDPermissionMode)
	assert.Equal(t, []string{"build", "plan"}, optionIDs(modes), "a catalog with no agent list keeps the static seed")

	snapshot := a.SettingsSnapshot()
	assert.Equal(t, "mock/alpha", snapshot.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, contracts.MiMoPermissionPolicyAsk, snapshot.ConfirmedOptions()[contracts.MiMoOptionPermissionPolicy])
}

func TestResolveSettings(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	current := mimoSettings{model: "mock/alpha", effort: "high", mode: "build", permissionPolicy: "ask"}

	for _, tc := range []struct {
		name    string
		options optionmap.Map
		want    mimoSettings
	}{
		{name: "nothing requested keeps everything", want: current},
		{name: "a model the catalog holds",
			options: optionmap.Map{agent.OptionIDModel: "mock/beta"},
			want:    mimoSettings{model: "mock/beta", effort: "", mode: "build", permissionPolicy: "ask"}},
		{name: "a model the catalog lacks keeps the model",
			options: optionmap.Map{agent.OptionIDModel: "mock/gone"}, want: current},
		{name: "an effort the model offers",
			options: optionmap.Map{agent.OptionIDEffort: "low"},
			want:    mimoSettings{model: "mock/alpha", effort: "low", mode: "build", permissionPolicy: "ask"}},
		{name: "an effort the model lacks falls back to Auto",
			options: optionmap.Map{agent.OptionIDEffort: "extreme"},
			want:    mimoSettings{model: "mock/alpha", effort: agent.EffortAuto, mode: "build", permissionPolicy: "ask"}},
		{name: "a mode the server offers",
			options: optionmap.Map{agent.OptionIDPermissionMode: "max"},
			want:    mimoSettings{model: "mock/alpha", effort: "high", mode: "max", permissionPolicy: "ask"}},
		{name: "a subagent is no mode",
			options: optionmap.Map{agent.OptionIDPermissionMode: "general"}, want: current},
		{name: "a known policy",
			options: optionmap.Map{contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicyBypass},
			want:    mimoSettings{model: "mock/alpha", effort: "high", mode: "build", permissionPolicy: "bypass"}},
		{name: "an unknown policy keeps the policy",
			options: optionmap.Map{contracts.MiMoOptionPermissionPolicy: "yolo"}, want: current},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, resolveSettings(catalog, current, tc.options))
		})
	}
}

// The model, the effort and the mode are LeapMux's own state: MiMo takes them
// from each prompt, so a change sends no request.
func TestUpdateSettingsAppliesAtTheNextPrompt(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "mock/beta", agent.OptionIDPermissionMode: contracts.MiMoModePlan})
	assert.Equal(t, "mock/beta", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, contracts.MiMoModePlan, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	assert.Empty(t, server.allRequests())
	assert.Equal(t, "mock/beta", sink.LastSettingsRefresh().Model)
	assert.Equal(t, contracts.MiMoModePlan, sink.LastSettingsRefresh().PermissionMode)

	require.NoError(t, a.SendInput("plan it", nil))
	body := decodeBody(t, server.requestsTo("POST /session/ses_test/prompt_async")[0])
	assert.Equal(t, contracts.MiMoModePlan, body["agent"])
	assert.Equal(t, map[string]any{"providerID": "mock", "modelID": "beta"}, body["model"])
}

func TestUpdateSettingsSetsThePermissionSwitches(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		policy        string
		skipAll       string
		approveDelete string
	}{
		{policy: contracts.MiMoPermissionPolicySkip, skipAll: `{"enabled":true}`, approveDelete: `{"enabled":false}`},
		{policy: contracts.MiMoPermissionPolicyBypass, skipAll: `{"enabled":true}`, approveDelete: `{"enabled":true}`},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			result := a.UpdateSettings(optionmap.Map{contracts.MiMoOptionPermissionPolicy: tc.policy})
			assert.Equal(t, tc.policy, result.ConfirmedOptions()[contracts.MiMoOptionPermissionPolicy])
			assert.JSONEq(t, tc.skipAll, string(server.requestsTo("POST /permission/skip-all")[0].Body))
			assert.JSONEq(t, tc.approveDelete, string(server.requestsTo("POST /permission/auto-approve-delete")[0].Body))
			assert.Equal(t, tc.policy, sink.LastSettingsRefresh().Options[contracts.MiMoOptionPermissionPolicy])

			a.UpdateSettings(optionmap.Map{contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicyAsk})
			assert.JSONEq(t, `{"enabled":false}`, string(server.requestsTo("POST /permission/skip-all")[1].Body))
			assert.JSONEq(t, `{"enabled":false}`, string(server.requestsTo("POST /permission/auto-approve-delete")[1].Body))
		})
	}
}

// The first switch can take effect before the second one fails. The server
// must not run a policy that the agent does not report, so the old policy goes
// back until a restart applies the new one.
func TestUpdateSettingsRestoresThePolicyAfterAFailedSwitch(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("POST /permission/auto-approve-delete", http.StatusInternalServerError, `{}`)

	result := a.UpdateSettings(optionmap.Map{contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicyBypass})
	assert.False(t, result.AppliedLive, "a restart applies the policy at startup")
	assert.Equal(t, contracts.MiMoPermissionPolicyAsk, a.permissionPolicy)
	skipAll := server.requestsTo("POST /permission/skip-all")
	require.Len(t, skipAll, 2)
	assert.JSONEq(t, `{"enabled":true}`, string(skipAll[0].Body))
	assert.JSONEq(t, `{"enabled":false}`, string(skipAll[1].Body), "the switch that took effect is turned back")
	assert.Zero(t, sink.SettingsRefreshCount())
}

func TestApplyStartupSettings(t *testing.T) {
	t.Parallel()

	t.Run("the requested values the catalog serves", func(t *testing.T) {
		t.Parallel()
		a, server := newTestAgent(t, nil)
		a.applyStartupSettings(a.Context(), agent.Options{Options: optionmap.Map{
			agent.OptionIDModel: "mock/alpha", agent.OptionIDEffort: "low",
			agent.OptionIDPermissionMode: contracts.MiMoModePlan, contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicySkip,
		}})
		assert.Equal(t, mimoSettings{model: "mock/alpha", effort: "low", mode: "plan", permissionPolicy: "skip"}, a.settingsLocked())
		assert.JSONEq(t, `{"enabled":true}`, string(server.requestsTo("POST /permission/skip-all")[0].Body))
	})

	t.Run("nothing requested runs the catalog default on Ask", func(t *testing.T) {
		t.Parallel()
		a, server := newTestAgent(t, nil)
		a.applyStartupSettings(a.Context(), agent.Options{})
		assert.Equal(t, mimoSettings{model: "mock/beta", effort: "", mode: "build", permissionPolicy: "ask"}, a.settingsLocked())
		// Ask sets both switches too, so they hold the value LeapMux reports
		// whatever the server started with.
		skipAll := server.requestsTo("POST /permission/skip-all")
		require.Len(t, skipAll, 1)
		assert.JSONEq(t, `{"enabled":false}`, string(skipAll[0].Body))
		approveDelete := server.requestsTo("POST /permission/auto-approve-delete")
		require.Len(t, approveDelete, 1)
		assert.JSONEq(t, `{"enabled":false}`, string(approveDelete[0].Body))
	})

	t.Run("Ask that the server refuses stays on Ask", func(t *testing.T) {
		t.Parallel()
		a, server := newTestAgent(t, nil)
		server.respond("POST /permission/skip-all", http.StatusInternalServerError, `{}`)
		a.applyStartupSettings(a.Context(), agent.Options{})
		assert.Equal(t, contracts.MiMoPermissionPolicyAsk, a.permissionPolicy,
			"the launch strips the variables that seed the switches, so the server starts on Ask")
		assert.Len(t, server.requestsTo("POST /permission/skip-all"), 1, "a refused Ask is not sent a second time")
	})

	t.Run("a refused policy stays on Ask", func(t *testing.T) {
		t.Parallel()
		a, server := newTestAgent(t, nil)
		server.respond("POST /permission/auto-approve-delete", http.StatusInternalServerError, `{}`)
		a.applyStartupSettings(a.Context(), agent.Options{Options: optionmap.Map{
			contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicyBypass,
		}})
		assert.Equal(t, contracts.MiMoPermissionPolicyAsk, a.permissionPolicy)
		skipAll := server.requestsTo("POST /permission/skip-all")
		require.Len(t, skipAll, 2)
		assert.JSONEq(t, `{"enabled":false}`, string(skipAll[1].Body), "a switch that did take effect is turned back")
	})
}

func TestApplyPermissionPolicyRefusesAnUnknownPolicy(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	assert.ErrorContains(t, a.applyPermissionPolicy(a.Context(), "yolo"), "unknown permission policy")
	assert.Empty(t, server.allRequests())
}

// modeRecordingSink is a recording sink that keeps every permission-mode
// report. agenttest.Sink states only the last one, which cannot show that a
// call reported nothing new.
type modeRecordingSink struct {
	*agenttest.Sink
	mu    sync.Mutex
	modes []string
}

func (s *modeRecordingSink) UpdatePermissionMode(mode string) {
	s.mu.Lock()
	s.modes = append(s.modes, mode)
	s.mu.Unlock()
	s.Sink.UpdatePermissionMode(mode)
}

func (s *modeRecordingSink) reportedModes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.modes...)
}

func TestAdoptMode(t *testing.T) {
	t.Parallel()
	sink := &modeRecordingSink{Sink: &agenttest.Sink{}}
	a, _ := newTestAgent(t, agent.NewProviderServices(sink))
	a.mode = contracts.MiMoModePlan

	a.adoptMode(contracts.MiMoModeBuild)
	assert.Equal(t, contracts.MiMoModeBuild, a.mode)
	assert.Equal(t, []string{contracts.MiMoModeBuild}, sink.reportedModes())

	a.adoptMode(contracts.MiMoModeBuild)
	assert.Equal(t, []string{contracts.MiMoModeBuild}, sink.reportedModes(), "an unchanged mode reports nothing new")
}

// A catalog with no agent list checks a requested mode against the static
// seed, which is what the mode menu then shows.
func TestResolveSettingsWithTheStaticModes(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	catalog.modes = nil
	current := mimoSettings{model: "mock/alpha", effort: agent.EffortAuto, mode: contracts.MiMoModeBuild, permissionPolicy: contracts.MiMoPermissionPolicyAsk}

	assert.Equal(t, contracts.MiMoModePlan, resolveSettings(catalog, current, optionmap.Map{agent.OptionIDPermissionMode: contracts.MiMoModePlan}).mode)
	assert.Equal(t, contracts.MiMoModeBuild, resolveSettings(catalog, current, optionmap.Map{agent.OptionIDPermissionMode: "max"}).mode,
		"an agent that the seed lacks is not applied")
}

// A plan tool call that failed moved nothing, whatever its metadata states.
func TestPlanExitThatFailedKeepsPlanMode(t *testing.T) {
	t.Parallel()
	sink := &modeRecordingSink{Sink: &agenttest.Sink{}}
	a, _ := newTestAgent(t, agent.NewProviderServices(sink))
	a.mode = contracts.MiMoModePlan

	feed(a,
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_p", "msg_1", contracts.MiMoToolPlanExit, "call-plan", toolState{Status: contracts.MiMoToolStatusError,
			Error: "The user dismissed this question", Metadata: map[string]any{"switched": true}}),
		toolPartEvent(t, "prt_q", "msg_1", contracts.MiMoToolPlanExit, "call-plan-2", toolState{Status: contracts.MiMoToolStatusCompleted,
			Output: "The plan stays."}),
	)
	assert.Equal(t, contracts.MiMoModePlan, a.mode)
	assert.Empty(t, sink.reportedModes(), "a call that states no switch moved nothing either")
}

// A plan tool call that completed with metadata.switched moved the session to
// build by itself.
func TestPlanExitThatSwitchedAdoptsBuild(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.mode = contracts.MiMoModePlan

	feed(a,
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_p", "msg_1", contracts.MiMoToolPlanExit, "call-plan", toolState{Status: contracts.MiMoToolStatusCompleted,
			Output: "User approved switching to build agent.", Metadata: map[string]any{"switched": false}}),
	)
	assert.Equal(t, contracts.MiMoModePlan, a.mode, "a declined plan keeps plan mode")

	feed(a, toolPartEvent(t, "prt_q", "msg_1", contracts.MiMoToolPlanExit, "call-plan-2", toolState{Status: contracts.MiMoToolStatusCompleted,
		Output: "User approved switching to build agent.", Metadata: map[string]any{"switched": true}}))
	assert.Equal(t, contracts.MiMoModeBuild, a.mode)
	assert.Equal(t, contracts.MiMoModeBuild, sink.PermissionMode())
}

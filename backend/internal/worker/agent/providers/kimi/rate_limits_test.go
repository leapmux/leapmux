package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// kimiTestManagedModelID is a model that a Kimi Code account supplies.
const kimiTestManagedModelID = "kimi-code/kimi-for-coding"

// kimiTestManagedModel is the GET /models item of that model.
func kimiTestManagedModel() map[string]any {
	return map[string]any{
		"provider": kimiManagedProvider, "model": kimiTestManagedModelID, "display_name": "Kimi for Coding",
		"max_context_size": 262144, "capabilities": []string{"thinking"}, "support_efforts": []string{"low", "medium", "high"},
	}
}

// kimiTestPlanUsage is a GET /oauth/usage answer that holds every window of a
// plan, and the Kimi Code share of the monthly window.
func kimiTestPlanUsage() map[string]any {
	return map[string]any{"kind": "ok", "quota": map[string]any{
		"usages": map[string]any{
			"limit5h":    map[string]any{"usedRatio": 0.42, "resetAt": "2026-10-25T05:00:00Z"},
			"limit7d":    map[string]any{"usedRatio": 0.85, "resetAt": "2026-10-30T00:00:00.500Z"},
			"monthTotal": map[string]any{"usedRatio": 1.2},
			"monthCode":  map[string]any{"usedRatio": 0.9, "resetAt": "2026-11-01T00:00:00Z"},
		},
		"extraUsage": nil,
	}}
}

// kimiTestPlanWindows is the rate-limit state of kimiTestPlanUsage.
const kimiTestPlanWindows = `{
	"five_hour": {"rate_limit_type": "five_hour", "utilization": 0.42, "status": "allowed", "resets_at": 1792904400},
	"seven_day": {"rate_limit_type": "seven_day", "utilization": 0.85, "status": "allowed_warning", "resets_at": 1793318400},
	"one_month": {"rate_limit_type": "one_month", "utilization": 1.2, "status": "exceeded"}
}`

// newKimiPlanRig opens a session on the model of the Kimi Code account, on a
// server that reports kimiTestPlanUsage.
func newKimiPlanRig(t *testing.T) *kimiTestRig {
	t.Helper()
	fake, server := newFakeKap(t)
	fake.models = append(fake.models, kimiTestManagedModel())
	fake.usage = kimiTestPlanUsage()
	return connectKimiTestRig(t, fake, server.URL, agent.Options{Options: options(agent.OptionIDModel, kimiTestManagedModelID)})
}

// kimiRateLimitStates returns the rate-limit values of each session-info
// broadcast, as JSON.
func kimiRateLimitStates(t *testing.T, sink *agenttest.ControlSink) []string {
	t.Helper()
	var states []string
	for _, info := range sink.SessionInfos() {
		update, ok := info[contracts.SessionInfoKeyRateLimits].(map[string]any)
		if !ok {
			continue
		}
		require.Equal(t, contracts.RateLimitUpdateModeReplace, update[contracts.RateLimitUpdateFieldMode])
		values, err := json.Marshal(update[contracts.RateLimitUpdateFieldValues])
		require.NoError(t, err)
		states = append(states, string(values))
	}
	return states
}

// lastKimiRateLimitState is the last state that the agent published.
func lastKimiRateLimitState(t *testing.T, sink *agenttest.ControlSink) string {
	t.Helper()
	states := kimiRateLimitStates(t, sink)
	require.NotEmpty(t, states, "the agent published a rate-limit state")
	return states[len(states)-1]
}

// waitForKimiUsageRead waits until the read of the plan usage that a trigger
// requested is complete.
func waitForKimiUsageRead(t *testing.T, rig *kimiTestRig) {
	t.Helper()
	waitFor(t, rig.agent.usageWork.Idle, "the plan usage read finishes")
}

// The catalog marks the models of the Kimi Code account by their provider.
func TestKimiCatalogMarksTheModelsOfTheAccount(t *testing.T) {
	t.Parallel()
	catalog := buildKimiCatalog([]kimiModelItem{
		{Provider: kimiManagedProvider, Model: kimiTestManagedModelID},
		{Provider: " " + kimiManagedProvider + " ", Model: "kimi-code/padded"},
		{Provider: "moonshot-ai", Model: "kimi-k2"},
		{Model: "kimi-text"},
	}, "")
	assert.True(t, catalog.managed(kimiTestManagedModelID))
	assert.True(t, catalog.managed("kimi-code/padded"), "the provider key is read without its spaces")
	assert.False(t, catalog.managed("kimi-k2"), "a model of another provider")
	assert.False(t, catalog.managed("kimi-text"), "a model that states no provider")
	assert.False(t, catalog.managed("unknown"), "a model that the catalog does not list")
	assert.False(t, kimiCatalog{}.managed(kimiTestManagedModelID), "an empty catalog")
}

// Each window of the plan becomes one entry with its use, its status and its
// reset. The Kimi Code share of the monthly window is not a limit of its own.
func TestKimiRateLimitValues(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(kimiTestPlanUsage())
	require.NoError(t, err)
	var usage kimiManagedUsage
	require.NoError(t, json.Unmarshal(raw, &usage))
	require.NotNil(t, usage.Quota)
	values, err := json.Marshal(kimiRateLimitValues(usage.Quota.Usages))
	require.NoError(t, err)
	assert.JSONEq(t, kimiTestPlanWindows, string(values))

	assert.Empty(t, kimiRateLimitValues(kimiPlanUsages{}), "a plan without windows")
}

// A window without a usable share is left out, and a reset that is not RFC
// 3339 is left out of its window.
func TestKimiRateLimitWindow(t *testing.T) {
	t.Parallel()
	ratio := func(value float64) *float64 { return &value }
	for name, window := range map[string]*kimiQuotaWindow{
		"an absent window":      nil,
		"a window with no use":  {ResetAt: "2026-10-25T05:00:00Z"},
		"a negative used share": {UsedRatio: ratio(-0.1)},
	} {
		assert.Nil(t, kimiRateLimitWindow(contracts.RateLimitTypeFiveHour, window), name)
	}
	for _, tc := range []struct {
		used   float64
		status string
	}{
		{0, contracts.RateLimitStatusAllowed},
		{0.79, contracts.RateLimitStatusAllowed},
		{0.8, contracts.RateLimitStatusAllowedWarning},
		{1, contracts.RateLimitStatusExceeded},
		{3, contracts.RateLimitStatusExceeded},
	} {
		info := kimiRateLimitWindow(contracts.RateLimitTypeFiveHour, &kimiQuotaWindow{UsedRatio: ratio(tc.used)})
		assert.Equal(t, tc.status, info[contracts.RateLimitFieldStatus], "used %v", tc.used)
		assert.Equal(t, tc.used, info[contracts.RateLimitFieldUtilization], "used %v", tc.used)
	}
	for _, reset := range []string{"", "tomorrow", "2026-10-25 05:00:00", "1793336400"} {
		info := kimiRateLimitWindow(contracts.RateLimitTypeFiveHour, &kimiQuotaWindow{UsedRatio: ratio(0.5), ResetAt: reset})
		assert.NotContains(t, info, contracts.RateLimitFieldResetsAt, "reset %q", reset)
	}
	withOffset := kimiRateLimitWindow(contracts.RateLimitTypeFiveHour, &kimiQuotaWindow{UsedRatio: ratio(0.5), ResetAt: "2026-10-25T14:00:00+09:00"})
	assert.Equal(t, int64(1792904400), withOffset[contracts.RateLimitFieldResetsAt], "a reset with a zone offset")
}

// A session on a model of the account publishes the windows of the plan.
func TestKimiUsageRefreshPublishesThePlanOfTheAccountModel(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRig(t)
	rig.agent.refreshUsage()
	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
	assert.JSONEq(t, kimiTestPlanWindows, lastKimiRateLimitState(t, rig.sink))

	// A plan that reports no window clears the windows.
	rig.fake.setUsage(map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{}, "extraUsage": nil}})
	rig.agent.refreshUsage()
	assert.JSONEq(t, `{}`, lastKimiRateLimitState(t, rig.sink))
}

// The plan does not limit a model of another provider, so its session clears
// the windows and never reads the plan.
func TestKimiUsageRefreshClearsTheWindowsForAnotherProvider(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.setUsage(kimiTestPlanUsage())
	rig.agent.refreshUsage()
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage))
	assert.Equal(t, []string{`{}`}, kimiRateLimitStates(t, rig.sink))
}

// A read that fails publishes nothing, so the last windows stay.
func TestKimiUsageRefreshKeepsTheLastWindowsWhenTheReadFails(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]fakeKapReply{
		"no account":            {Data: map[string]any{"kind": "error", "message": `No token for "managed:kimi-code". Run /login to authenticate.`}},
		"a refused token":       {Data: map[string]any{"kind": "error", "status": 401, "message": "Authorization failed."}},
		"an answer of no kind":  {Data: map[string]any{"quota": map[string]any{"usages": map[string]any{}}}},
		"an answer of no quota": {Data: map[string]any{"kind": "ok"}},
		"a refused request":     {Code: 50001, Msg: "internal error"},
		"a failed request":      {HTTPStatus: 502, Code: 50201, Msg: "bad gateway"},
		"a malformed answer":    {Data: "not an object"},
		"a malformed window":    {Data: map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": "0.4"}}}}},
		"a dropped connection":  {Drop: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiPlanRig(t)
			rig.agent.refreshUsage()
			require.Len(t, kimiRateLimitStates(t, rig.sink), 1)
			rig.fake.reply("GET "+kimiRouteManagedUsage, reply)
			rig.agent.refreshUsage()
			// Go's transport sends a GET once more when the server closes the
			// connection without a reply, so a dropped connection is read twice.
			assert.GreaterOrEqual(t, len(rig.fake.requestsTo("GET "+kimiRouteManagedUsage)), 2, "the agent read the plan again")
			assert.Len(t, kimiRateLimitStates(t, rig.sink), 1, "the read published nothing")
		})
	}
}

// Main turn completion refreshes account usage; a child completion does not.
func TestKimiUsageRefreshFollowsNativeTurnCompletion(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRig(t)
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	before := len(rig.fake.requestsTo("GET " + kimiRouteManagedUsage))
	native.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`)
	native.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"}}}`)
	waitForKimiUsageRead(t, rig)
	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), before+1)
	assert.JSONEq(t, kimiTestPlanWindows, lastKimiRateLimitState(t, rig.sink))
	adoptNativeKimiChild(t, rig.agent, "agent-0")
	native.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	waitForKimiUsageRead(t, rig)
	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), before+1, "a child completion does not refresh the account")

}

func TestKimiUsageRefreshFollowsTheModel(t *testing.T) {
	t.Parallel()
	t.Run("a switch that the reader makes", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.models = append(fake.models, kimiTestManagedModel())
		fake.usage = kimiTestPlanUsage()
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{})

		rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: kimiTestManagedModelID})
		waitForKimiUsageRead(t, rig)
		assert.Len(t, fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
		assert.JSONEq(t, kimiTestPlanWindows, lastKimiRateLimitState(t, rig.sink))

		rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
		waitForKimiUsageRead(t, rig)
		assert.Len(t, fake.requestsTo("GET "+kimiRouteManagedUsage), 1, "a change that keeps the model reads nothing")

		rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		waitForKimiUsageRead(t, rig)
		assert.Len(t, fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
		assert.JSONEq(t, `{}`, lastKimiRateLimitState(t, rig.sink), "the model of another provider clears the plan")
	})

	t.Run("a switch that the server reports", func(t *testing.T) {
		t.Parallel()
		rig := newKimiPlanRig(t)
		rig.agent.HandleOutput([]byte(`{"type":"agent","session_id":"session_1","agent_id":"main","status":"idle","model":"kimi-text","permission_mode":"manual","plan_mode":false,"swarm_mode":false,"tower_mode":false}`))
		waitForKimiUsageRead(t, rig)
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage))
		assert.Equal(t, []string{`{}`}, kimiRateLimitStates(t, rig.sink))

		rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"agent","session_id":"session_1","agent_id":"main","status":"idle","model":%q,"permission_mode":"manual","plan_mode":false,"swarm_mode":false,"tower_mode":false}`, kimiTestManagedModelID)))
		waitForKimiUsageRead(t, rig)
		assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
		assert.JSONEq(t, kimiTestPlanWindows, lastKimiRateLimitState(t, rig.sink))

		rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"agent","session_id":"session_1","agent_id":"main","status":"idle","model":%q,"permission_mode":"manual","plan_mode":true,"swarm_mode":false,"tower_mode":false}`, kimiTestManagedModelID)))
		waitForKimiUsageRead(t, rig)
		assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1, "an update that keeps the model reads nothing")
	})
}

func TestKimiModelUpdateRefreshesUsageOnceWhenNativeStateArrivesBeforeTheReply(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	fake.models = append(fake.models, kimiTestManagedModel())
	fake.usage = kimiTestPlanUsage()
	rig := connectKimiTestRig(t, fake, server.URL, agent.Options{})
	waitForKimiUsageRead(t, rig)
	before := len(fake.requestsTo("GET " + kimiRouteManagedUsage))
	stateApplied := make(chan struct{})
	replyReleased := make(chan struct{})
	releaseReply := sync.OnceFunc(func() { close(replyReleased) })
	t.Cleanup(releaseReply)
	fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Data: map[string]any{}, BeforeReply: func() {
		profiles := fake.requestsTo("POST " + kimiSessionPath("session_1", "/profile"))
		var patch struct {
			Config map[string]any `json:"agent_config"`
		}
		if !assert.NoError(t, json.Unmarshal(profiles[len(profiles)-1].Body, &patch)) {
			return
		}
		fake.update("session_1", func(session *fakeKapSession) {
			session.Model = kimiTestManagedModelID
			if thinking, exists := patch.Config[kimiConfigThinking].(string); exists {
				if thinking == "on" {
					thinking = "high"
				}
				session.Thinking = thinking
			}
		})
		for _, state := range fake.sessionStateFrames("session_1") {
			data, err := json.Marshal(state)
			if !assert.NoError(t, err) {
				return
			}
			var frame kimiFrame
			if !assert.NoError(t, json.Unmarshal(data, &frame)) {
				return
			}
			rig.agent.stream.observeSessionState(frame)
			rig.agent.handleFrame(frame)
		}
		close(stateApplied)
		<-replyReleased
	}})
	applied := make(chan agent.SettingsApplyResult, 1)
	go func() {
		applied <- rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: kimiTestManagedModelID})
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-stateApplied:
	case <-ctx.Done():
		t.Fatal("the native model update was not applied")
	}
	waitForKimiUsageRead(t, rig)
	assert.Len(t, fake.requestsTo("GET "+kimiRouteManagedUsage), before+1)
	releaseReply()
	select {
	case result := <-applied:
		require.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDModel].State)
	case <-ctx.Done():
		t.Fatal("the model update did not finish")
	}
	waitForKimiUsageRead(t, rig)
	assert.Len(t, fake.requestsTo("GET "+kimiRouteManagedUsage), before+1)
}

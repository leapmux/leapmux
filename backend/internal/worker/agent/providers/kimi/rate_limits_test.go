package kimi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
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

// The end of a main turn reads the plan, because the turn used it. The end of
// a subagent's turn does not: the main turn's end follows.
func TestKimiUsageRefreshFollowsTheMainTurnEnd(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRig(t)
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": contracts.KimiOriginUser}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": contracts.KimiTurnEndCompleted})
	waitForKimiUsageRead(t, rig)
	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
	assert.JSONEq(t, kimiTestPlanWindows, lastKimiRateLimitState(t, rig.sink))

	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "agentId": "agent_child", "turnId": 0, "reason": contracts.KimiTurnEndCompleted})
	waitForKimiUsageRead(t, rig)
	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1, "a subagent's turn end reads nothing")
}

// A model change reads the windows of the new model: a switch to the account's
// model shows the plan, and a switch away clears it.
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
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "model": "kimi-text"})
		waitForKimiUsageRead(t, rig)
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage))
		assert.Equal(t, []string{`{}`}, kimiRateLimitStates(t, rig.sink))

		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "model": kimiTestManagedModelID})
		waitForKimiUsageRead(t, rig)
		assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
		assert.JSONEq(t, kimiTestPlanWindows, lastKimiRateLimitState(t, rig.sink))

		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "planMode": true})
		waitForKimiUsageRead(t, rig)
		assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1, "an update that keeps the model reads nothing")
	})
}

// --- the pending usage limit (kimiPendingUsageLimit) ---

// kimiFiveHourResetAt is the reset of the 5-hour window in the usage answers
// below. kimiPlanClockAt pins the clock at the real 5-hour failure, about 90
// minutes before it.
var (
	kimiPlanClockAt     = time.UnixMilli(1792675623669).UTC()
	kimiFiveHourResetAt = time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
)

// kimiUsageAnswer is a GET /oauth/usage answer with a 5-hour window at used
// and the reset of kimiFiveHourResetAt.
func kimiUsageAnswer(used float64) map[string]any {
	return map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
		"limit5h":    map[string]any{"usedRatio": used, "resetAt": kimiFiveHourResetAt.Format(time.RFC3339)},
		"limit7d":    map[string]any{"usedRatio": 0.4, "resetAt": "2026-10-27T00:00:00Z"},
		"monthTotal": map[string]any{"usedRatio": 0.2, "resetAt": "2026-11-01T00:00:00Z"},
	}}}
}

// newKimiPlanRigAt opens a session on the model of the Kimi Code account, with
// the agent's clock pinned at kimiPlanClockAt, on a server whose plan used up
// its 5-hour window.
func newKimiPlanRigAt(t *testing.T) *kimiTestRig {
	t.Helper()
	fake, server := newFakeKap(t)
	fake.models = append(fake.models, kimiTestManagedModel())
	fake.usage = kimiUsageAnswer(1)
	a, sink, opts := newConnectedKimiAgent(t, server.URL, agent.Options{Options: options(agent.OptionIDModel, kimiTestManagedModelID)})
	clock := testutil.NewQuartzMock(t)
	clock.Set(kimiPlanClockAt).MustWait(testutil.DeadlineContext(t))
	a.clock = clock
	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	return &kimiTestRig{agent: a, fake: fake, sink: sink}
}

// kimiUsageGate holds one plan-usage read inside the fake server until the test
// releases it.
type kimiUsageGate struct {
	entered chan struct{}
	release chan struct{}
}

func newKimiUsageGate() *kimiUsageGate {
	return &kimiUsageGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

// reply is a usage answer that waits at the gate.
func (g *kimiUsageGate) reply(usage any) fakeKapReply {
	return fakeKapReply{Data: usage, BeforeReply: func() {
		g.entered <- struct{}{}
		<-g.release
	}}
}

// waitEntered waits until the gated read reached the fake server.
func (g *kimiUsageGate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the plan-usage read never reached the server")
	}
}

// failFiveHour runs one main turn that ends on the real 5-hour limit.
func failFiveHour(t *testing.T, rig *kimiTestRig, turnID int) {
	t.Helper()
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": turnID, "origin": map[string]any{"kind": contracts.KimiOriginUser}})
	failed := kimiFixturePayload(t, kimiTurnEndedFiveHour)
	failed["turnId"] = turnID
	rig.feed(t, failed)
}

// kimiOpsAfterLastTurnEnd returns the retry operations after the last turn-end
// row of the timeline.
func kimiOpsAfterLastTurnEnd(t *testing.T, sink *agenttest.ControlSink) []kimiRetryOp {
	t.Helper()
	ops := kimiRetryOps(sink.Timeline())
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].op == "turn_end" {
			return ops[i+1:]
		}
	}
	t.Fatal("the timeline holds no turn end")
	return nil
}

// The plan-usage read states the reset of a usage limit that the event does not.
func TestKimiUsageLimitTakesTheResetOfTheExhaustedWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		usage fakeKapReply
		want  []kimiRetryOp
	}{
		{
			"an exhausted window schedules the usage-limit retry at its reset",
			fakeKapReply{Data: kimiUsageAnswer(1)},
			[]kimiRetryOp{{op: "schedule_usage_limit", resetAt: kimiFiveHourResetAt}, {op: "cancel_server_error"}},
		},
		{
			"overage reads as an exhausted window",
			fakeKapReply{Data: kimiUsageAnswer(1.3)},
			[]kimiRetryOp{{op: "schedule_usage_limit", resetAt: kimiFiveHourResetAt}, {op: "cancel_server_error"}},
		},
		{
			"the latest reset of the exhausted windows wins",
			fakeKapReply{Data: map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
				"limit5h":    map[string]any{"usedRatio": 1, "resetAt": kimiFiveHourResetAt.Format(time.RFC3339)},
				"limit7d":    map[string]any{"usedRatio": 1, "resetAt": "2026-10-27T00:00:00Z"},
				"monthTotal": map[string]any{"usedRatio": 0.9, "resetAt": "2026-11-01T00:00:00Z"},
			}}}},
			[]kimiRetryOp{{op: "schedule_usage_limit", resetAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}, {op: "cancel_server_error"}},
		},
		{
			"no exhausted window applies the stored verdict",
			fakeKapReply{Data: kimiUsageAnswer(0.6)},
			[]kimiRetryOp{{op: "schedule_server_error"}},
		},
		{
			"an exhausted window with no reset applies the stored verdict",
			fakeKapReply{Data: map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
				"limit5h": map[string]any{"usedRatio": 1},
			}}}},
			[]kimiRetryOp{{op: "schedule_server_error"}},
		},
		{
			"a reset in the past applies the stored verdict",
			fakeKapReply{Data: map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
				"limit5h": map[string]any{"usedRatio": 1, "resetAt": kimiPlanClockAt.Add(-time.Minute).Format(time.RFC3339)},
			}}}},
			[]kimiRetryOp{{op: "schedule_server_error"}},
		},
		{
			"a reset more than 31 days ahead applies the stored verdict",
			fakeKapReply{Data: map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
				"limit5h": map[string]any{"usedRatio": 1, "resetAt": kimiPlanClockAt.Add(32 * 24 * time.Hour).Format(time.RFC3339)},
			}}}},
			[]kimiRetryOp{{op: "schedule_server_error"}},
		},
		{
			"an answer of the error kind applies the stored verdict",
			fakeKapReply{Data: map[string]any{"kind": "error", "message": "Authorization failed."}},
			[]kimiRetryOp{{op: "schedule_server_error"}},
		},
		{
			"a failed request applies the stored verdict",
			fakeKapReply{HTTPStatus: 502, Code: 50201, Msg: "bad gateway"},
			[]kimiRetryOp{{op: "schedule_server_error"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiPlanRigAt(t)
			rig.fake.reply("GET "+kimiRouteManagedUsage, tc.usage)
			failFiveHour(t, rig, 41)
			waitForKimiUsageRead(t, rig)
			assert.Equal(t, tc.want, kimiOpsAfterLastTurnEnd(t, rig.sink))
			require.Positive(t, rig.sink.AutoScheduleCount())
			assert.JSONEq(t, kimiTurnEndedFiveHour, string(rig.sink.LastAutoSchedule().SourcePayload), "the retry carries the failed turn's event")
		})
	}
}

// Every verdict but a usage limit with no reset applies at the turn end, with
// no wait for the read.
func TestKimiOtherVerdictsOfAPlanModelApplyAtOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRigAt(t)
	gate := newKimiUsageGate()
	rig.fake.reply("GET "+kimiRouteManagedUsage, gate.reply(kimiUsageAnswer(1)))
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": contracts.KimiOriginUser}})
	rig.feed(t, kimiFixturePayload(t, kimiTurnEndedSuspended))
	gate.waitEntered(t)
	assert.Equal(t, []kimiRetryOp{{op: "cancel_usage_limit"}, {op: "cancel_server_error"}}, kimiOpsAfterLastTurnEnd(t, rig.sink),
		"the billing block applied while the read still runs")
	close(gate.release)
	waitForKimiUsageRead(t, rig)
	assert.Equal(t, []kimiRetryOp{{op: "cancel_usage_limit"}, {op: "cancel_server_error"}}, kimiOpsAfterLastTurnEnd(t, rig.sink),
		"the read adds nothing")
}

// A read with no pending usage limit makes no retry call.
func TestKimiUsageReadWithNoPendingLimitMakesNoRetryCall(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRigAt(t)
	rig.agent.refreshUsage()
	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 1)
	assert.Zero(t, rig.sink.AutoScheduleCount())
	assert.Zero(t, rig.sink.AutoCancelCount())
}

// A read that already runs when the turn fails took no record. The repeat that
// the failure requests takes it, so the verdict reads the second answer.
func TestKimiUsageLimitWaitsForTheReadAfterARunningOne(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRigAt(t)
	gate := newKimiUsageGate()
	rig.fake.reply("GET "+kimiRouteManagedUsage, gate.reply(kimiUsageAnswer(0.5)), fakeKapReply{Data: kimiUsageAnswer(1)})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 40, "origin": map[string]any{"kind": contracts.KimiOriginUser}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 40, "reason": contracts.KimiTurnEndCompleted})
	gate.waitEntered(t)

	failFiveHour(t, rig, 41)
	assert.Empty(t, kimiOpsAfterLastTurnEnd(t, rig.sink), "the verdict waits for a read")
	close(gate.release)
	waitForKimiUsageRead(t, rig)

	assert.Len(t, rig.fake.requestsTo("GET "+kimiRouteManagedUsage), 2, "the failure asked for one more read")
	assert.Equal(t, []kimiRetryOp{{op: "schedule_usage_limit", resetAt: kimiFiveHourResetAt}, {op: "cancel_server_error"}},
		kimiOpsAfterLastTurnEnd(t, rig.sink), "the read that took the record states the reset, not the read that ran before")
}

// A read that ends after the state of the failed turn changed drops the
// verdict: the turn that it belongs to no longer runs.
func TestKimiUsageLimitOfAReplacedTurnIsDropped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		replace func(t *testing.T, rig *kimiTestRig)
	}{
		{"a new main turn starts", func(t *testing.T, rig *kimiTestRig) {
			rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 42, "origin": map[string]any{"kind": contracts.KimiOriginUser}})
		}},
		{"the server moves the model", func(t *testing.T, rig *kimiTestRig) {
			rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "model": "kimi-text"})
		}},
		{"the server moves the model away and back", func(t *testing.T, rig *kimiTestRig) {
			rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "model": "kimi-text"})
			rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "model": kimiTestManagedModelID})
		}},
		{"the reader changes the model", func(t *testing.T, rig *kimiTestRig) {
			rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		}},
		{"the reader clears the context", func(t *testing.T, rig *kimiTestRig) {
			_, err := rig.agent.ClearContext()
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiPlanRigAt(t)
			gate := newKimiUsageGate()
			rig.fake.reply("GET "+kimiRouteManagedUsage, gate.reply(kimiUsageAnswer(1)), fakeKapReply{Data: kimiUsageAnswer(1)})
			failFiveHour(t, rig, 41)
			gate.waitEntered(t)

			tc.replace(t, rig)
			close(gate.release)
			waitForKimiUsageRead(t, rig)

			assert.Zero(t, rig.sink.AutoScheduleCount(), "the replaced turn schedules nothing")
			assert.Zero(t, rig.sink.AutoCancelCount(), "the replaced turn cancels nothing")
		})
	}
}

// A manual compaction can start while the plan-usage read of a failed turn
// runs. The compaction's start publishes an active turn, which retires the
// writer that the stored verdict captured, so the read applies nothing after
// it, whatever the answer. Only the compaction's own verdict reaches the queue.
func TestKimiUsageLimitThatSettlesAfterAManualCompactionStartsAppliesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		usage map[string]any
	}{
		{"an answer that states the reset", kimiUsageAnswer(1)},
		{"an answer that states no reset", kimiUsageAnswer(0.6)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiPlanRigAt(t)
			gate := newKimiUsageGate()
			rig.fake.reply("GET "+kimiRouteManagedUsage, gate.reply(tc.usage), fakeKapReply{Data: tc.usage})
			failFiveHour(t, rig, 41)
			gate.waitEntered(t)

			rig.feed(t, map[string]any{"type": contracts.KimiEventCompactionStarted, "trigger": "manual"})
			rig.feed(t, map[string]any{"type": contracts.KimiEventCompactionCompleted})
			close(gate.release)
			waitForKimiUsageRead(t, rig)

			ops := providerkittest.TurnEndAndRetryOps(rig.sink.Timeline())
			require.NotEmpty(t, ops)
			assert.Equal(t, append([]agenttest.TimelineEntry(nil), kimiCompactionEnd...), ops[len(ops)-1:])
			assert.Equal(t, []string{"turn_end", "cancel_server_error"}, providerkittest.TurnEndAndRetryOpNames(rig.sink.Timeline()))
			assert.Zero(t, rig.sink.AutoScheduleCount())
		})
	}
}

// A read that settles before the manual compaction starts applies the stored
// verdict, and the compaction's end then cancels the server-error retry that
// the verdict scheduled.
func TestKimiUsageLimitThatSettlesBeforeAManualCompactionIsCancelledByIt(t *testing.T) {
	t.Parallel()
	rig := newKimiPlanRigAt(t)
	rig.fake.reply("GET "+kimiRouteManagedUsage, fakeKapReply{Data: kimiUsageAnswer(0.6)})
	failFiveHour(t, rig, 41)
	waitForKimiUsageRead(t, rig)
	rig.feed(t, map[string]any{"type": contracts.KimiEventCompactionStarted, "trigger": "manual"})
	rig.feed(t, map[string]any{"type": contracts.KimiEventCompactionCompleted})
	want := append(providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}), kimiCompactionEnd...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(rig.sink.Timeline()))
}

// kimiExhaustedResetAt reads the latest reset of a used-up window.
func TestKimiExhaustedResetAt(t *testing.T) {
	t.Parallel()
	ratio := func(value float64) *float64 { return &value }
	early, late := "2026-10-25T05:00:00Z", "2026-10-30T00:00:00.5Z"
	for _, tc := range []struct {
		name   string
		usages kimiPlanUsages
		want   time.Time
	}{
		{"no window", kimiPlanUsages{}, time.Time{}},
		{"no used-up window", kimiPlanUsages{Limit5h: &kimiQuotaWindow{UsedRatio: ratio(0.99), ResetAt: early}}, time.Time{}},
		{"a window with no use", kimiPlanUsages{Limit5h: &kimiQuotaWindow{ResetAt: early}}, time.Time{}},
		{"a used-up window with a malformed reset", kimiPlanUsages{Limit5h: &kimiQuotaWindow{UsedRatio: ratio(1), ResetAt: "tomorrow"}}, time.Time{}},
		{"one used-up window", kimiPlanUsages{Limit5h: &kimiQuotaWindow{UsedRatio: ratio(1), ResetAt: early}}, time.Date(2026, 10, 25, 5, 0, 0, 0, time.UTC)},
		{
			"the later of two used-up windows",
			kimiPlanUsages{Limit5h: &kimiQuotaWindow{UsedRatio: ratio(1), ResetAt: early}, Limit7d: &kimiQuotaWindow{UsedRatio: ratio(2), ResetAt: late}},
			time.Date(2026, 10, 30, 0, 0, 0, 500_000_000, time.UTC),
		},
		{
			"a window below the limit does not count",
			kimiPlanUsages{Limit5h: &kimiQuotaWindow{UsedRatio: ratio(1), ResetAt: early}, MonthTotal: &kimiQuotaWindow{UsedRatio: ratio(0.5), ResetAt: late}},
			time.Date(2026, 10, 25, 5, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := kimiExhaustedResetAt(tc.usages)
			assert.True(t, tc.want.Equal(got), "got %v, want %v", got, tc.want)
		})
	}
}

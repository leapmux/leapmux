package qoder

import (
	"maps"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func qoderPercent(value float64) *float64 { return &value }

// rateLimitStates returns the rate-limit values of each session-info broadcast.
func rateLimitStates(t *testing.T, sink *agenttest.Sink) []map[string]any {
	t.Helper()
	var states []map[string]any
	for _, info := range sink.SessionInfos() {
		update, ok := info[contracts.SessionInfoKeyRateLimits].(map[string]any)
		if !ok {
			continue
		}
		require.Equal(t, contracts.RateLimitUpdateModeReplace, update[contracts.RateLimitUpdateFieldMode])
		values, ok := update[contracts.RateLimitUpdateFieldValues].(map[string]any)
		require.True(t, ok)
		states = append(states, values)
	}
	return states
}

func qoderWindow(name string, utilization float64, status string) map[string]any {
	return map[string]any{
		contracts.RateLimitFieldRateLimitType: name,
		contracts.RateLimitFieldUtilization:   utilization,
		contracts.RateLimitFieldStatus:        status,
	}
}

func TestQoderRateLimitValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		usage qoderQuotaUsage
		want  map[string]any
	}{
		{name: "no quota", usage: qoderQuotaUsage{}, want: map[string]any{}},
		{
			name: "every quota at its status",
			usage: qoderQuotaUsage{
				UserQuota:          &qoderQuota{Percentage: qoderPercent(42)},
				AddOnQuota:         &qoderQuota{Percentage: qoderPercent(80)},
				OrgResourcePackage: &qoderQuota{Percentage: qoderPercent(100)},
			},
			want: map[string]any{
				"user_quota":           qoderWindow("user_quota", 0.42, contracts.RateLimitStatusAllowed),
				"add_on_quota":         qoderWindow("add_on_quota", 0.8, contracts.RateLimitStatusAllowedWarning),
				"org_resource_package": qoderWindow("org_resource_package", 1, contracts.RateLimitStatusExceeded),
			},
		},
		{
			name:  "unused quota",
			usage: qoderQuotaUsage{UserQuota: &qoderQuota{Percentage: qoderPercent(0)}},
			want:  map[string]any{"user_quota": qoderWindow("user_quota", 0, contracts.RateLimitStatusAllowed)},
		},
		{
			name:  "just below the warning",
			usage: qoderQuotaUsage{UserQuota: &qoderQuota{Percentage: qoderPercent(79.9)}},
			want:  map[string]any{"user_quota": qoderWindow("user_quota", 0.799, contracts.RateLimitStatusAllowed)},
		},
		{
			name:  "overage keeps its value",
			usage: qoderQuotaUsage{UserQuota: &qoderQuota{Percentage: qoderPercent(130)}},
			want:  map[string]any{"user_quota": qoderWindow("user_quota", 1.3, contracts.RateLimitStatusExceeded)},
		},
		{
			name: "a quota without a usable percentage",
			usage: qoderQuotaUsage{
				UserQuota:          &qoderQuota{},
				AddOnQuota:         &qoderQuota{Percentage: qoderPercent(-1)},
				OrgResourcePackage: &qoderQuota{Percentage: qoderPercent(math.NaN())},
			},
			want: map[string]any{},
		},
		{
			name:  "an infinite percentage",
			usage: qoderQuotaUsage{UserQuota: &qoderQuota{Percentage: qoderPercent(math.Inf(1))}},
			want:  map[string]any{},
		},
		{
			name:  "an exceeded account without a quota",
			usage: qoderQuotaUsage{IsQuotaExceeded: true},
			want: map[string]any{"account": map[string]any{
				contracts.RateLimitFieldRateLimitType: "account",
				contracts.RateLimitFieldStatus:        contracts.RateLimitStatusExceeded,
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := qoderRateLimitValues(&tc.usage)
			require.Len(t, got, len(tc.want))
			for key, want := range tc.want {
				wantWindow := want.(map[string]any)
				gotWindow, ok := got[key].(map[string]any)
				require.True(t, ok, key)
				assert.Equal(t, wantWindow[contracts.RateLimitFieldRateLimitType], gotWindow[contracts.RateLimitFieldRateLimitType])
				assert.Equal(t, wantWindow[contracts.RateLimitFieldStatus], gotWindow[contracts.RateLimitFieldStatus])
				if utilization, ok := wantWindow[contracts.RateLimitFieldUtilization]; ok {
					assert.InDelta(t, utilization, gotWindow[contracts.RateLimitFieldUtilization], 1e-9)
				} else {
					assert.NotContains(t, gotWindow, contracts.RateLimitFieldUtilization)
				}
				assert.NotContains(t, gotWindow, contracts.RateLimitFieldResetsAt, "LeapMux shows no reset time for Qoder")
			}
		})
	}
}

// The reply of Qoder CLI 1.1.78 for the mock quota of the browser test, as the probe recorded it.
const qoderUsageReply42 = `{"usage":{"userId":"leapmux-e2e","userType":"personal","totalUsagePercentage":42,"isHighestTier":false,"expiresAt":1893456000000,"upgradeUrl":"","isQuotaExceeded":false,` +
	`"userQuota":{"total":1000,"used":420,"remaining":580,"percentage":42,"unit":"credits"},` +
	`"addOnQuota":{"total":200,"used":50,"remaining":150,"percentage":25,"unit":"credits","detailUrl":""},` +
	`"orgResourcePackage":{"used":30,"cap":300,"remaining":270,"percentage":10,"available":true,"unit":"credits"}},` +
	`"session":{"total_credits":0,"model_usage":{}}}`

func newUsageAgent(t *testing.T, replies map[string][]qoderScriptedReply) (*Agent, *agenttest.Sink, *qoderControlScript) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	script := &qoderControlScript{agent: a, replies: replies}
	a.SetStdinForTest(script)
	return a, sink, script
}

func TestQoderRefreshUsagePublishesTheQuotaWindows(t *testing.T) {
	t.Parallel()
	a, sink, script := newUsageAgent(t, map[string][]qoderScriptedReply{"get_usage_info": {{response: qoderUsageReply42}}})
	a.refreshUsage()
	assert.Equal(t, []string{"get_usage_info"}, script.subtypes())
	states := rateLimitStates(t, sink)
	require.Len(t, states, 1)
	assert.Equal(t, []string{"add_on_quota", "org_resource_package", "user_quota"}, sortedKeys(states[0]))
	userQuota := states[0]["user_quota"].(map[string]any)
	assert.InDelta(t, 0.42, userQuota[contracts.RateLimitFieldUtilization], 1e-9)
	assert.Equal(t, contracts.RateLimitStatusAllowed, userQuota[contracts.RateLimitFieldStatus])
	assert.NotContains(t, userQuota, contracts.RateLimitFieldResetsAt, "expiresAt is not a reset time")
}

func TestQoderRefreshUsageKeepsTheLastStateWithoutAQuota(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]qoderScriptedReply{
		"no quota service answer": {response: `{"usage":null,"session":{"total_credits":3,"model_usage":{}},"usage_error":"get_usage_info failed"}`},
		"refused request":         {refusal: "get_usage_info failed"},
		"malformed reply":         {response: `{"usage":[]}`},
		"no payload":              {response: noPayload},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newUsageAgent(t, map[string][]qoderScriptedReply{"get_usage_info": {reply}})
			a.refreshUsage()
			assert.Empty(t, rateLimitStates(t, sink))
		})
	}
}

func TestQoderRefreshUsageClearsWindowsThatTheQuotaDropped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newUsageAgent(t, map[string][]qoderScriptedReply{"get_usage_info": {
		{response: qoderUsageReply42},
		{response: `{"usage":{"isQuotaExceeded":true,"userQuota":{"percentage":100}}}`},
	}})
	a.refreshUsage()
	a.refreshUsage()
	states := rateLimitStates(t, sink)
	require.Len(t, states, 2)
	assert.Equal(t, []string{"account", "user_quota"}, sortedKeys(states[1]), "a replace update drops the windows that the quota no longer states")
}

// An agent that Start did not launch has no refresh loop, so a turn end sends
// no quota request to a process that is not there.
func TestQoderUsageRefreshNeedsALaunchedAgent(t *testing.T) {
	t.Parallel()
	a, sink, script := newUsageAgent(t, nil)
	a.requestUsageRefresh()
	a.HandleOutput([]byte(qoderSuccessResult))
	assert.Empty(t, script.subtypes())
	assert.Empty(t, rateLimitStates(t, sink))
}

func TestQoderTurnEndRequestsTheQuotaWithoutWaiting(t *testing.T) {
	t.Parallel()
	a, _, script := newUsageAgent(t, nil)
	a.usageRefresh = make(chan struct{}, 1)
	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput([]byte(qoderSuccessResult))
	assert.Len(t, a.usageRefresh, 1, "two turn ends while no loop runs join into one request")
	assert.Empty(t, script.subtypes(), "the reader sends nothing itself")
}

func TestQoderStartupUsageRefreshPublishesBeforeTheFirstTurn(t *testing.T) {
	t.Parallel()
	processDone := make(chan struct{})
	sink := &agenttest.Sink{}
	a := newOfflineAgentWithRuntime(t, sink, nil, processDone)
	script := &qoderControlScript{agent: a, replies: map[string][]qoderScriptedReply{"get_usage_info": {{response: qoderUsageReply42}}}}
	a.SetStdinForTest(script)
	a.usageRefresh = make(chan struct{}, 1)
	ended := make(chan struct{})
	go func() {
		a.refreshUsageLoop()
		close(ended)
	}()

	// Start wakes the loop once before it returns the agent for its first input.
	a.requestUsageRefresh()
	testutil.RequireEventually(t, func() bool { return len(rateLimitStates(t, sink)) == 1 }, "the startup refresh publishes the account quota")
	states := rateLimitStates(t, sink)
	assert.Equal(t, []string{"add_on_quota", "org_resource_package", "user_quota"}, sortedKeys(states[0]))
	assert.Equal(t, []string{"get_usage_info"}, script.subtypes(), "no user turn requested the quota")

	close(processDone)
	ctx := testutil.DeadlineContext(t)
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("the loop did not end with the process")
	}
}

func TestQoderUsageRefreshLoopSendsOneRequestPerWake(t *testing.T) {
	t.Parallel()
	processDone := make(chan struct{})
	sink := &agenttest.Sink{}
	a := newOfflineAgentWithRuntime(t, sink, nil, processDone)
	published := make(chan struct{}, 4)
	script := &qoderControlScript{agent: a, replies: map[string][]qoderScriptedReply{"get_usage_info": {{response: qoderUsageReply42}, {response: qoderUsageReply42}}}}
	script.beforeReply = func(string) { published <- struct{}{} }
	a.SetStdinForTest(script)
	a.usageRefresh = make(chan struct{}, 1)
	ended := make(chan struct{})
	go func() {
		a.refreshUsageLoop()
		close(ended)
	}()
	ctx := testutil.DeadlineContext(t)
	for range 2 {
		a.requestUsageRefresh()
		select {
		case <-published:
		case <-ctx.Done():
			t.Fatal("the loop sent no quota request")
		}
	}
	close(processDone)
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("the loop did not end with the process")
	}
	assert.Equal(t, []string{"get_usage_info", "get_usage_info"}, script.subtypes())
}

func sortedKeys(values map[string]any) []string {
	return slices.Sorted(maps.Keys(values))
}

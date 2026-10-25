package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

// nativeProviderUsage is the answer of Kilo 7.8.16's provider-usage route for
// one coding plan that Kilo manages (probe `kilo-usage-probe`).
const nativeProviderUsage = `{"items":[{"id":"kilo-managed:sub-1","providerID":"zai","sourceKind":"kilo_managed","providerLabel":"Z.ai","planLabel":"Lite","sourceLabel":"via Kilo","fetchState":"ready","planState":"active","routingState":"active","fetchedAt":"2026-10-24T10:12:00.739Z","managementUrl":"http://127.0.0.1:65348/subscriptions/coding-plans/sub-1","windows":[{"id":"sub-1:five_hour","resource":"subscription","unit":"percent","orientation":"remaining_percent","used":42,"remaining":58,"limit":100,"period":{"unit":"hour","value":5},"durationMs":18000000,"resetAt":"2026-10-24T13:11:59.260Z","state":"active"},{"id":"sub-1:weekly","resource":"subscription","unit":"percent","orientation":"remaining_percent","used":90,"remaining":10,"limit":100,"period":{"unit":"week","value":1},"durationMs":604800000,"resetAt":"2026-10-29T10:12:00.739Z","state":"active"}]}],"generatedAt":"2026-10-24T10:12:00.740Z"}`

func number(value float64) *float64 { return &value }

func unixOf(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)
	return parsed.Unix()
}

func TestKiloRateLimitValuesFromTheNativeReport(t *testing.T) {
	t.Parallel()
	var report kiloUsageReport
	require.NoError(t, json.Unmarshal([]byte(nativeProviderUsage), &report))
	require.NotNil(t, report.Items)
	assert.Equal(t, map[string]any{
		"kilo-managed:sub-1/sub-1:five_hour": map[string]any{
			contracts.RateLimitFieldRateLimitType: contracts.RateLimitTypeFiveHour,
			contracts.RateLimitFieldUtilization:   0.42,
			contracts.RateLimitFieldStatus:        contracts.RateLimitStatusAllowed,
			contracts.RateLimitFieldResetsAt:      unixOf(t, "2026-10-24T13:11:59.260Z"),
		},
		"kilo-managed:sub-1/sub-1:weekly": map[string]any{
			contracts.RateLimitFieldRateLimitType: contracts.RateLimitTypeSevenDay,
			contracts.RateLimitFieldUtilization:   0.9,
			contracts.RateLimitFieldStatus:        contracts.RateLimitStatusAllowedWarning,
			contracts.RateLimitFieldResetsAt:      unixOf(t, "2026-10-29T10:12:00.739Z"),
		},
	}, kiloRateLimitValues(*report.Items))
}

func TestKiloRateLimitValuesKeepEachPlanApart(t *testing.T) {
	t.Parallel()
	window := kiloUsageWindow{ID: "five_hour", Used: number(10), Limit: number(100), Period: &kiloUsagePeriod{Unit: "hour", Value: 5}}
	values := kiloRateLimitValues([]kiloUsageSnapshot{{ID: "plan-a", Windows: []kiloUsageWindow{window}}, {ID: "plan-b", Windows: []kiloUsageWindow{window}}})
	assert.Len(t, values, 2)
	assert.Contains(t, values, "plan-a/five_hour")
	assert.Contains(t, values, "plan-b/five_hour")
	assert.Empty(t, kiloRateLimitValues(nil))
}

func TestKiloWindowUtilization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		window kiloUsageWindow
		want   float64
		known  bool
	}{
		{name: "used against the limit", window: kiloUsageWindow{Used: number(30), Limit: number(120)}, want: 0.25, known: true},
		{name: "remaining against the limit", window: kiloUsageWindow{Remaining: number(90), Limit: number(120)}, want: 0.25, known: true},
		{name: "overage keeps its value", window: kiloUsageWindow{Used: number(150), Limit: number(100)}, want: 1.5, known: true},
		{name: "remaining above the limit reads as no use", window: kiloUsageWindow{Remaining: number(150), Limit: number(100)}, want: 0, known: true},
		{name: "a used percent", window: kiloUsageWindow{Orientation: kiloUsedPercent, Used: number(64)}, want: 0.64, known: true},
		{name: "a remaining percent", window: kiloUsageWindow{Orientation: kiloRemainingPercent, Remaining: number(25)}, want: 0.75, known: true},
		{name: "a zero limit falls back to the percent", window: kiloUsageWindow{Orientation: kiloUsedPercent, Used: number(5), Limit: number(0)}, want: 0.05, known: true},
		{name: "an amount without a limit", window: kiloUsageWindow{Orientation: "amount", Used: number(5)}},
		{name: "a negative use", window: kiloUsageWindow{Orientation: kiloUsedPercent, Used: number(-1)}},
		{name: "a use that is not a number", window: kiloUsageWindow{Orientation: kiloUsedPercent, Used: number(math.NaN())}},
		{name: "an infinite limit", window: kiloUsageWindow{Used: number(1), Limit: number(math.Inf(1))}},
		{name: "no numbers", window: kiloUsageWindow{Orientation: kiloRemainingPercent}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, known := kiloWindowUtilization(tc.window)
			assert.Equal(t, tc.known, known)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

func TestKiloRateLimitWindowStates(t *testing.T) {
	t.Parallel()
	assert.Equal(t, map[string]any{
		contracts.RateLimitFieldRateLimitType: "quota",
		contracts.RateLimitFieldStatus:        contracts.RateLimitStatusExceeded,
	}, kiloRateLimitWindow(kiloUsageWindow{ID: "w", State: kiloWindowExhausted}), "an exhausted window without numbers")
	assert.Equal(t, contracts.RateLimitStatusExceeded, kiloRateLimitWindow(kiloUsageWindow{ID: "w", Used: number(100), Limit: number(100)})[contracts.RateLimitFieldStatus])
	assert.Equal(t, contracts.RateLimitStatusAllowedWarning, kiloRateLimitWindow(kiloUsageWindow{ID: "w", Used: number(80), Limit: number(100)})[contracts.RateLimitFieldStatus])
	assert.Equal(t, contracts.RateLimitStatusAllowed, kiloRateLimitWindow(kiloUsageWindow{ID: "w", Used: number(79), Limit: number(100)})[contracts.RateLimitFieldStatus])
	for name, window := range map[string]kiloUsageWindow{
		"an unlimited window":       {ID: "w", State: kiloWindowUnlimited, Used: number(1), Limit: number(100)},
		"a window outside the plan": {ID: "w", State: kiloWindowNotInPlan, Used: number(1), Limit: number(100)},
		"an unknown window":         {ID: "w", State: "unknown"},
		"a window without an id":    {Used: number(1), Limit: number(100)},
	} {
		assert.Nil(t, kiloRateLimitWindow(window), name)
	}
	withoutReset := kiloRateLimitWindow(kiloUsageWindow{ID: "w", Used: number(1), Limit: number(100), ResetAt: "tomorrow"})
	assert.NotContains(t, withoutReset, contracts.RateLimitFieldResetsAt, "a reset time that is not RFC 3339 is left out")
}

func TestKiloWindowType(t *testing.T) {
	t.Parallel()
	cases := map[string]kiloUsageWindow{
		contracts.RateLimitTypeFiveHour: {Period: &kiloUsagePeriod{Unit: "hour", Value: 5}},
		contracts.RateLimitTypeSevenDay: {Period: &kiloUsagePeriod{Unit: "week", Value: 1}, Resource: kiloPlanResource},
		contracts.RateLimitTypeOneMonth: {Period: &kiloUsagePeriod{Unit: "month", Value: 1}},
		"3_month":                       {Period: &kiloUsagePeriod{Unit: "month", Value: 3}},
		"1_hour":                        {Period: &kiloUsagePeriod{Unit: "hour", Value: 1}},
		"quota":                         {},
		"general_five_hour":             {Period: &kiloUsagePeriod{Unit: "hour", Value: 5}, Resource: "general"},
		"general_quota":                 {Resource: "general", Period: &kiloUsagePeriod{Unit: "day", Value: 0}},
	}
	for want, window := range cases {
		assert.Equal(t, want, kiloWindowType(window), want)
	}
	assert.Equal(t, contracts.RateLimitTypeSevenDay, kiloWindowType(kiloUsageWindow{Period: &kiloUsagePeriod{Unit: "day", Value: 7}}))
}

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

// usageDaemon serves the provider-usage route with the answers that a test
// sets, and counts its reads.
type usageDaemon struct {
	daemon *opencodetest.FakeDaemon
	status atomic.Int32
	body   atomic.Value
	reads  atomic.Int32
}

func newUsageDaemon(t *testing.T, body string) *usageDaemon {
	t.Helper()
	d := &usageDaemon{daemon: opencodetest.NewFakeDaemon(t, opencode.EventRoute, opencode.QuestionRoot)}
	d.status.Store(http.StatusOK)
	d.body.Store(body)
	d.daemon.HandleFunc(kiloProviderUsageRoute, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		d.reads.Add(1)
		w.WriteHeader(int(d.status.Load()))
		_, _ = fmt.Fprint(w, d.body.Load().(string))
	})
	return d
}

// newUsageAgent builds the agent that Start builds, with its refresh loop, and
// connects its daemon bridge to daemon. The connection asks for the first read.
func newUsageAgent(t *testing.T, daemon *usageDaemon) (*Agent, *agenttest.Sink) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.Sink{}
	a := newAgent(true)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "kilo-usage", ProviderName: "kilo", Ctx: ctx, Clock: testutil.NewQuartzMock(t)})
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session-1")
	*a.HooksForTest() = opencode.FamilyHooks(&a.FamilyBase)
	go a.refreshUsageLoop()
	a.BeginQuestionsForTest(ctx, a.Sink(), "kilo-usage", daemon.daemon.Server.URL)
	return a, sink
}

func TestKiloReadsThePlanUsageOnceTheDaemonAnswers(t *testing.T) {
	t.Parallel()
	daemon := newUsageDaemon(t, nativeProviderUsage)
	_, sink := newUsageAgent(t, daemon)
	testutil.RequireEventually(t, func() bool { return len(rateLimitStates(t, sink)) == 1 })
	states := rateLimitStates(t, sink)
	assert.Len(t, states[0], 2)
	assert.Contains(t, states[0], "kilo-managed:sub-1/sub-1:five_hour")
}

func TestKiloReadsThePlanUsageAfterEachTurn(t *testing.T) {
	t.Parallel()
	daemon := newUsageDaemon(t, nativeProviderUsage)
	a, sink := newUsageAgent(t, daemon)
	testutil.RequireEventually(t, func() bool { return len(rateLimitStates(t, sink)) == 1 })
	daemon.body.Store(`{"items":[]}`)
	a.TurnEnded()
	testutil.RequireEventually(t, func() bool { return len(rateLimitStates(t, sink)) == 2 })
	assert.Empty(t, rateLimitStates(t, sink)[1], "a report without plans clears the windows")
}

// A read that fails publishes nothing, so the last windows stay. A later read
// that succeeds is the barrier: the loop reads in order, so a broadcast of a
// failed read would come before it.
func TestKiloKeepsTheLastPlanUsageWhenAReadFails(t *testing.T) {
	t.Parallel()
	daemon := newUsageDaemon(t, nativeProviderUsage)
	a, sink := newUsageAgent(t, daemon)
	testutil.RequireEventually(t, func() bool { return len(rateLimitStates(t, sink)) == 1 })
	for _, failure := range []struct {
		status int
		body   string
	}{
		{status: http.StatusServiceUnavailable, body: `{}`},
		{status: http.StatusOK, body: `not JSON`},
		{status: http.StatusOK, body: `{"generatedAt":"2026-10-24T10:12:00.740Z"}`},
	} {
		reads := daemon.reads.Load()
		daemon.status.Store(int32(failure.status))
		daemon.body.Store(failure.body)
		a.TurnEnded()
		testutil.RequireEventually(t, func() bool { return daemon.reads.Load() > reads })
	}
	daemon.status.Store(http.StatusOK)
	daemon.body.Store(`{"items":[{"id":"later","windows":[{"id":"w","used":5,"limit":10}]}]}`)
	a.TurnEnded()
	testutil.RequireEventually(t, func() bool { return len(rateLimitStates(t, sink)) >= 2 })
	states := rateLimitStates(t, sink)
	require.Len(t, states, 2, "no failed read published a state")
	assert.Equal(t, map[string]any{"later/w": map[string]any{
		contracts.RateLimitFieldRateLimitType: "quota",
		contracts.RateLimitFieldUtilization:   0.5,
		contracts.RateLimitFieldStatus:        contracts.RateLimitStatusAllowed,
	}}, states[1])
}

func TestKiloUsageRequestsJoinWhileOneWaits(t *testing.T) {
	t.Parallel()
	a := newAgent(true)
	a.requestUsageRefresh()
	a.requestUsageRefresh()
	assert.Len(t, a.usageRefresh, 1)
	(&Agent{}).requestUsageRefresh()
}

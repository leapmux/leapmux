package letta

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func lettaQuotaNumber(value float64) *float64 { return &value }

func answerLettaQuotaBackend(t *testing.T, fake *fakeAppServer, backend string, providers ...string) map[string]any {
	t.Helper()
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": backend})
	if backend == "local" {
		request := fake.nextCommand(t)
		require.Equal(t, "list_connect_providers", request["type"])
		records := make([]any, 0, len(providers))
		for _, name := range providers {
			records = append(records, map[string]any{"is_connected": true, "id": "connection-" + name, "provider_name": name, "provider_type": "chatgpt_oauth", "auth_type": "oauth"})
		}
		replyLettaSettings(t, fake, request, map[string]any{"target": backend, "providers": []any{map[string]any{"provider_type": "chatgpt_oauth", "connected_providers": records}}})
	}
	read := fake.nextCommand(t)
	require.Equal(t, "chatgpt_usage_read", read["type"])
	assert.Equal(t, backend, read["target"])
	return read
}

func returnLettaQuota(t *testing.T, fake *fakeAppServer, read map[string]any, used float64) {
	t.Helper()
	replyLettaSettings(t, fake, read, map[string]any{"target": read["target"], "usage": map[string]any{"providerName": read["provider_name"], "primary": map[string]any{"usedPercent": used, "windowDurationMins": 300}}})
}

func TestLettaQuotaUsesTheModelConnection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		model       string
		backend     string
		connections []string
		want        string
	}{
		{"legacy local connection", "openai-codex/gpt-5.5", "local", []string{"chatgpt-plus-pro"}, "chatgpt-plus-pro"},
		{"native model priority", "chatgpt-plus-pro/gpt-5.5", "local", []string{"chatgpt-plus-pro", "openai-codex"}, "openai-codex"},
		{"custom local connection", "work/gpt-5.5", "local", []string{"openai-codex", "work"}, "work"},
		{"API connection", "work/gpt-5.5", "api", nil, "work"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, a, sink := newLettaQuotaTestAgent(t, quartz.NewReal())
			a.settings.model = tc.model
			a.catalog.models = []lettaModel{{id: tc.model, variants: []lettaModelVariant{{providerType: "chatgpt_oauth"}}}}
			a.requestQuotaRefresh()
			read := answerLettaQuotaBackend(t, fake, tc.backend, tc.connections...)
			assert.Equal(t, tc.want, read["provider_name"])
			returnLettaQuota(t, fake, read, 20)
			values := waitLettaQuotaValues(t, sink, true)
			assert.Contains(t, values, "primary")
		})
	}
}

func TestLettaQuotaSkipsAPIKeyModels(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaQuotaTestAgent(t, quartz.NewReal())
	a.settings.model = "openai/gpt-5.5"
	a.catalog.models = []lettaModel{{id: a.settings.model, variants: []lettaModelVariant{{providerType: "openai"}}}}
	a.requestQuotaRefresh()
	assert.Empty(t, waitLettaQuotaValues(t, sink, false))
	assert.True(t, a.quotaWork.Idle())
	select {
	case command := <-fake.commands:
		t.Fatalf("an API-key model queried subscription usage: %v", command["type"])
	default:
	}
}

func TestLettaQuotaRetryRetainsTheSnapshotAndStopsWithOutput(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	fake, a, sink := newLettaQuotaTestAgent(t, clock)
	trap := clock.Trap().NewTimer("letta quota retry")
	defer trap.Close()
	a.requestQuotaRefresh()
	read := answerLettaQuotaBackend(t, fake, "api")
	returnLettaQuota(t, fake, read, 90)
	before := waitLettaQuotaValues(t, sink, true)
	a.requestQuotaRefresh()
	read = answerLettaQuotaBackend(t, fake, "api")
	replyLettaSettings(t, fake, read, map[string]any{"success": false, "target": "api", "error": map[string]any{"code": "rate_limited", "retryAfterMs": 2000}})
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "api"})
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, 2*time.Second, delay)
	latest, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, ok)
	assert.Equal(t, before, latest.(map[string]any)[contracts.RateLimitUpdateFieldValues])
	assert.Zero(t, sink.AutoScheduleCount(), "a quota-endpoint refusal cannot schedule model work")
	clock.Advance(delay).MustWait(ctx)
	read = fake.nextCommand(t)
	require.Equal(t, "chatgpt_usage_read", read["type"])
	returnLettaQuota(t, fake, read, 0)
	values := waitLettaQuotaValues(t, sink, true)
	assert.Equal(t, float64(0), values["primary"].(map[string]any)[contracts.RateLimitFieldUtilization])
	a.requestQuotaRefresh()
	read = answerLettaQuotaBackend(t, fake, "api")
	replyLettaSettings(t, fake, read, map[string]any{"success": false, "target": "api", "error": map[string]any{"code": "rate_limited", "retryAfterMs": 2000}})
	info = fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "api"})
	testutil.WaitForTimer(t, ctx, trap)
	a.finishOutputAfterExit(agent.MessageCompletionComplete)
	require.Eventually(t, a.quotaWork.Idle, 30*time.Second, time.Millisecond)
	select {
	case command := <-fake.commands:
		t.Fatalf("quota work continued after output ended: %v", command["type"])
	default:
	}
}

func TestLettaQuotaRetryDelay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value *float64
		want  time.Duration
	}{
		{nil, 30 * time.Second}, {lettaQuotaNumber(-1), 30 * time.Second},
		{lettaQuotaNumber(math.NaN()), 30 * time.Second}, {lettaQuotaNumber(0), time.Second},
		{lettaQuotaNumber(1000.1), 1001 * time.Millisecond}, {lettaQuotaNumber(2000), 2 * time.Second},
		{lettaQuotaNumber(math.MaxFloat64), time.Duration(math.MaxInt64)},
	} {
		assert.Equal(t, tc.want, lettaQuotaRetryDelay(tc.value))
	}
}

func TestLettaQuotaKeepsTheSnapshotForInvalidAndTransientReplies(t *testing.T) {
	t.Parallel()
	for name, fields := range map[string]map[string]any{
		"missing success": {"success": nil, "target": "api"},
		"missing usage":   {"target": "api"},
		"wrong backend":   {"target": "local", "usage": map[string]any{"providerName": "openai-codex"}},
		"wrong provider":  {"target": "api", "usage": map[string]any{"providerName": "another"}},
		"malformed count": {"target": "api", "usage": map[string]any{"providerName": "openai-codex", "primary": map[string]any{"usedPercent": "invalid"}}},
		"network failure": {"success": false, "target": "api", "error": map[string]any{"code": "network_error", "message": "unavailable"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, a, sink := newLettaQuotaTestAgent(t, quartz.NewReal())
			a.requestQuotaRefresh()
			read := answerLettaQuotaBackend(t, fake, "api")
			returnLettaQuota(t, fake, read, 90)
			before := waitLettaQuotaValues(t, sink, true)
			a.requestQuotaRefresh()
			read = answerLettaQuotaBackend(t, fake, "api")
			replyLettaSettings(t, fake, read, fields)
			require.Eventually(t, a.quotaWork.Idle, 30*time.Second, time.Millisecond)
			latest, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
			require.True(t, ok)
			assert.Equal(t, before, latest.(map[string]any)[contracts.RateLimitUpdateFieldValues])
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

func TestLettaQuotaClearsDisconnectedConnections(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaQuotaTestAgent(t, quartz.NewReal())
	a.requestQuotaRefresh()
	read := answerLettaQuotaBackend(t, fake, "local", "openai-codex")
	returnLettaQuota(t, fake, read, 80)
	waitLettaQuotaValues(t, sink, true)
	a.requestQuotaRefresh()
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
	connections := fake.nextCommand(t)
	require.Equal(t, "list_connect_providers", connections["type"])
	replyLettaSettings(t, fake, connections, map[string]any{"target": "local", "providers": []any{}})
	assert.Empty(t, waitLettaQuotaValues(t, sink, false))
	require.Eventually(t, a.quotaWork.Idle, 30*time.Second, time.Millisecond)
	select {
	case command := <-fake.commands:
		t.Fatalf("a disconnected account received a quota request: %v", command["type"])
	default:
	}
}

func TestLettaQuotaModelChangeRetainsTheAccountCooldown(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	fake, a, sink := newLettaQuotaTestAgent(t, clock)
	a.catalog.models = append(a.catalog.models, lettaModel{id: "openai-codex/gpt-6-sol", variants: []lettaModelVariant{{providerType: "chatgpt_oauth"}}})
	trap := clock.Trap().NewTimer("letta quota retry")
	defer trap.Close()
	a.requestQuotaRefresh()
	read := answerLettaQuotaBackend(t, fake, "api")
	replyLettaSettings(t, fake, read, map[string]any{"success": false, "target": "api", "error": map[string]any{"code": "rate_limited", "retryAfterMs": 2000}})
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "api"})
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, 2*time.Second, testutil.WaitForTimer(t, ctx, trap))
	changed := make(chan agent.SettingsApplyResult, 1)
	go func() { changed <- a.UpdateSettings(map[string]string{agent.OptionIDModel: "openai-codex/gpt-6-sol"}) }()
	model := fake.nextCommand(t)
	require.Equal(t, "update_model", model["type"])
	replyLettaSettings(t, fake, model, map[string]any{"runtime": model["runtime"], "model_handle": "openai-codex/gpt-6-sol", "model_settings": map[string]any{"provider_type": "chatgpt_oauth"}})
	assert.Equal(t, "openai-codex/gpt-6-sol", waitModeResult(t, changed).ConfirmedOptions()[agent.OptionIDModel])
	info = fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "api"})
	delay := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, 2*time.Second, delay)
	select {
	case command := <-fake.commands:
		t.Fatalf("a model change bypassed the account cooldown: %v", command["type"])
	default:
	}
	clock.Advance(delay).MustWait(ctx)
	read = fake.nextCommand(t)
	require.Equal(t, "chatgpt_usage_read", read["type"])
	returnLettaQuota(t, fake, read, 10)
	assert.NotEmpty(t, waitLettaQuotaValues(t, sink, true))
}

func TestLettaQuotaProjection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		window *lettaQuotaWindow
		label  string
		want   map[string]any
	}{
		{"absent", nil, "", nil},
		{"empty named window", &lettaQuotaWindow{Label: "model"}, "model", nil},
		{"invalid named window", &lettaQuotaWindow{UsedPercent: lettaQuotaNumber(-1), ResetsAt: lettaQuotaNumber(-1)}, "model", nil},
		{"zero reset only", &lettaQuotaWindow{ResetsAt: lettaQuotaNumber(0)}, "", map[string]any{contracts.RateLimitFieldResetsAt: int64(0)}},
		{"period only", &lettaQuotaWindow{WindowDurationMins: lettaQuotaNumber(300)}, "", map[string]any{contracts.RateLimitFieldRateLimitType: "five_hour"}},
		{"zero usage", &lettaQuotaWindow{UsedPercent: lettaQuotaNumber(0)}, "model", map[string]any{contracts.RateLimitFieldRateLimitType: "model", contracts.RateLimitFieldUtilization: float64(0), contracts.RateLimitFieldStatus: "allowed"}},
		{"exceeded", &lettaQuotaWindow{UsedPercent: lettaQuotaNumber(125)}, "model", map[string]any{contracts.RateLimitFieldRateLimitType: "model", contracts.RateLimitFieldUtilization: 1.25, contracts.RateLimitFieldStatus: "exceeded"}},
		{"nonfinite", &lettaQuotaWindow{UsedPercent: lettaQuotaNumber(math.NaN()), ResetsAt: lettaQuotaNumber(math.Inf(1))}, "model", nil},
		{"invalid reset with valid usage", &lettaQuotaWindow{UsedPercent: lettaQuotaNumber(50), ResetsAt: lettaQuotaNumber(math.MaxFloat64)}, "model", map[string]any{contracts.RateLimitFieldRateLimitType: "model", contracts.RateLimitFieldUtilization: 0.5, contracts.RateLimitFieldStatus: "allowed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, lettaQuotaWindowInfo(tc.window, tc.label))
		})
	}
	assert.Empty(t, lettaQuotaValues(&lettaQuotaSnapshot{IndividualLimit: &lettaIndividualLimit{}}))
	values := lettaQuotaValues(&lettaQuotaSnapshot{Additional: []*lettaQuotaWindow{
		{Label: "same", UsedPercent: lettaQuotaNumber(10)},
		{Label: "same", UsedPercent: lettaQuotaNumber(90)},
	}})
	assert.Len(t, values, 2, "equal labels must not collapse different native windows")
}

func TestLettaQuotaModelSwitchDoesNotWaitForTheOldAccount(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaQuotaTestAgent(t, testutil.NewQuartzMock(t))
	a.catalog.models = append(a.catalog.models, lettaModel{id: "work/gpt-5.5", variants: []lettaModelVariant{{id: "work-high", effort: "high", providerType: "chatgpt_oauth"}}})
	a.requestQuotaRefresh()
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "api"})
	oldRead := fake.nextCommand(t)
	require.Equal(t, "chatgpt_usage_read", oldRead["type"])
	assert.Equal(t, "openai-codex", oldRead["provider_name"])
	changed := make(chan agent.SettingsApplyResult, 1)
	go func() { changed <- a.UpdateSettings(map[string]string{agent.OptionIDModel: "work/gpt-5.5"}) }()
	model := fake.nextCommand(t)
	require.Equal(t, "update_model", model["type"])
	replyLettaSettings(t, fake, model, map[string]any{"runtime": model["runtime"], "model_handle": "work/gpt-5.5", "model_settings": map[string]any{"provider_type": "chatgpt_oauth", "reasoning_effort": "high"}})
	assert.Equal(t, "work/gpt-5.5", waitModeResult(t, changed).ConfirmedOptions()[agent.OptionIDModel])
	// The old usage request remains unanswered. A new account must refresh independently.
	info = fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	oldID, ok := oldRead["request_id"].(string)
	require.True(t, ok)
	assert.False(t, a.rpcReplies.IsPendingForTest(oldID))
	replyLettaSettings(t, fake, info, map[string]any{"backend": "api"})
	read := fake.nextCommand(t)
	require.Equal(t, "chatgpt_usage_read", read["type"])
	assert.Equal(t, "work", read["provider_name"])
	replyLettaSettings(t, fake, read, map[string]any{"target": "api", "usage": map[string]any{"providerName": "work", "primary": map[string]any{"usedPercent": 20, "windowDurationMins": 300}}})
	values := waitLettaQuotaValues(t, sink, true)
	late, err := json.Marshal(map[string]any{"type": "chatgpt_usage_read_response", "request_id": oldID, "success": true, "target": "api", "usage": map[string]any{"providerName": "openai-codex", "primary": map[string]any{"usedPercent": 99, "windowDurationMins": 300}}})
	require.NoError(t, err)
	a.handleFrame(late)
	latest, present := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, present)
	update, ok := latest.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, values, update[contracts.RateLimitUpdateFieldValues])
}

func TestLettaQuotaRepliesNeverBecomeTranscriptRows(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"chatgpt_usage_read_response", "app_server_info_response"} {
		for _, requestID := range []string{`"unmatched"`, `7`} {
			t.Run(typ+requestID, func(t *testing.T) {
				t.Parallel()
				_, a, sink := newLettaQuotaTestAgent(t, quartz.NewReal())
				a.handleFrame([]byte(fmt.Sprintf(`{"type":%q,"request_id":%s,"success":false,"error":{"code":"network_error"}}`, typ, requestID)))
				assert.Zero(t, sink.MessageCount())
			})
		}
	}
}

type lettaQuotaTestSink struct {
	*agenttest.Sink
	updates chan map[string]any
}

func (s *lettaQuotaTestSink) BroadcastSessionInfo(info map[string]any) {
	s.Sink.BroadcastSessionInfo(info)
	if update, ok := info[contracts.SessionInfoKeyRateLimits].(map[string]any); ok {
		s.updates <- update
	}
}

func newLettaQuotaTestAgent(t *testing.T, clock quartz.Clock) (*fakeAppServer, *Agent, *lettaQuotaTestSink) {
	t.Helper()
	sink := &lettaQuotaTestSink{Sink: &agenttest.Sink{}, updates: make(chan map[string]any, 16)}
	fake, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), clock)
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-codex/gpt-5.5"
	a.catalog.models = []lettaModel{{id: a.settings.model, variants: []lettaModelVariant{{id: "gpt-5.5-high", effort: "high", providerType: "chatgpt_oauth"}}}}
	return fake, a, sink
}

func waitLettaQuotaValues(t *testing.T, sink *lettaQuotaTestSink, nonempty bool) map[string]any {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	for {
		select {
		case update := <-sink.updates:
			require.Equal(t, contracts.RateLimitUpdateModeReplace, update[contracts.RateLimitUpdateFieldMode])
			values, ok := update[contracts.RateLimitUpdateFieldValues].(map[string]any)
			require.True(t, ok)
			if (len(values) > 0) == nonempty {
				return values
			}
		case <-ctx.Done():
			t.Fatal("the native quota snapshot did not reach session info")
			return nil
		}
	}
}

func TestLettaQuotaReadsNativeUsageAfterTurn(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaQuotaTestAgent(t, quartz.NewReal())
	a.handleFrame([]byte(`{"type":"turn_finished","turn_id":"quota-turn","stop_reason":"end_turn","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"}}`))
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
	connections := fake.nextCommand(t)
	require.Equal(t, "list_connect_providers", connections["type"])
	assert.Equal(t, "local", connections["target"])
	replyLettaSettings(t, fake, connections, map[string]any{
		"target": "local", "providers": []any{map[string]any{
			"provider_type": "chatgpt_oauth", "connected_providers": []any{map[string]any{
				"is_connected": true, "id": "connection-1", "provider_name": "openai-codex", "provider_type": "chatgpt_oauth", "auth_type": "oauth",
			}},
		}},
	})
	read := fake.nextCommand(t)
	require.Equal(t, "chatgpt_usage_read", read["type"])
	assert.Equal(t, "local", read["target"])
	assert.Equal(t, "openai-codex", read["provider_name"])
	assert.Equal(t, true, read["force_refresh"])
	assert.NotContains(t, read, "runtime")
	replyLettaSettings(t, fake, read, map[string]any{
		"target": "local",
		"usage": map[string]any{
			"providerName": "openai-codex", "limitReached": false,
			"primary":   map[string]any{"label": "primary", "usedPercent": 87.5, "windowDurationMins": 300, "resetsAt": 1893456000},
			"secondary": map[string]any{"label": "secondary", "usedPercent": 0, "windowDurationMins": 10080, "resetsAt": 1893542400},
		},
	})
	values := waitLettaQuotaValues(t, sink, true)
	assert.Equal(t, map[string]any{
		contracts.RateLimitFieldRateLimitType: "five_hour", contracts.RateLimitFieldStatus: "allowed_warning",
		contracts.RateLimitFieldUtilization: 0.875, contracts.RateLimitFieldResetsAt: int64(1893456000),
	}, values["primary"])
	assert.Equal(t, map[string]any{
		contracts.RateLimitFieldRateLimitType: "seven_day", contracts.RateLimitFieldStatus: "allowed",
		contracts.RateLimitFieldUtilization: float64(0), contracts.RateLimitFieldResetsAt: int64(1893542400),
	}, values["secondary"])
	assert.Equal(t, 1, sink.MessageCount(), "quota responses must not become transcript rows")
}

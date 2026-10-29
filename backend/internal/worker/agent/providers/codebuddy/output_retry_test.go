package codebuddy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// The research frames of CodeBuddy 2.175.0 (.tmp/retry-research/codebuddy-qoder.md),
// copied verbatim: a 529, a network reset, a stream timeout, the billing code
// 14001, and a success.
const (
	codebuddyOverloadedResult    = `{"type":"result","subtype":"error_during_execution","is_error":true,"uuid":"01a18000-7063-7850-bfa8-c8c5ff6c6d69","session_id":"2943c8e3-7422-48ef-991e-e7925ed6b77e","duration_ms":8208,"duration_api_ms":8195,"num_turns":2,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":null,"server_tool_use":null,"service_tier":null},"permission_denials":[],"errors":["529 Overloaded (01a1800050a77a2ca5f7edbb2af5c356/2943c8e3-7422-48ef-991e-e7925ed6b77e)"],"errors_info":[{"status":529,"code":0,"category":"model_service","details":"529 Overloaded (01a1800050a77a2ca5f7edbb2af5c356/2943c8e3-7422-48ef-991e-e7925ed6b77e)"}],"modelUsage":{"custom-local:fake-model":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"contextWindow":128000,"maxOutputTokens":4096}},"_requestId":"01a180004fd27163ad790f5467b86c7f","_meta":{"baggage":"codebuddy.session_id=2943c8e3-7422-48ef-991e-e7925ed6b77e,codebuddy.conversation_request_id=01a180004fd27163ad790f5467b86c7f"},"__timestamp":"2026-10-28T00:54:34.852Z"}`
	codebuddyNetworkResetResult  = `{"type":"result","subtype":"error_during_execution","is_error":true,"uuid":"01a18003-65ad-7c6e-bd28-6640349f2778","session_id":"7cbf30ab-27ae-4a76-a226-21654a08d086","duration_ms":6207,"duration_api_ms":6197,"num_turns":2,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":null,"server_tool_use":null,"service_tier":null},"permission_denials":[],"errors":["502 网络连接被中断：可能是防火墙、代理或安全软件拦截，请检查网络环境（read ECONNRESET） (target: http://127.0.0.1:64763) (01a180034db2761bb86c9e3b41f97329/7cbf30ab-27ae-4a76-a226-21654a08d086)"],"errors_info":[{"status":502,"code":3002,"category":"network","details":"502 网络连接被中断：可能是防火墙、代理或安全软件拦截，请检查网络环境（read ECONNRESET） (target: http://127.0.0.1:64763) (01a180034db2761bb86c9e3b41f97329/7cbf30ab-27ae-4a76-a226-21654a08d086)"}],"modelUsage":{"custom-local:fake-model":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"contextWindow":128000,"maxOutputTokens":4096}},"_requestId":"01a180034cdb75058eaca171af7efdbf","_meta":{"baggage":"codebuddy.session_id=7cbf30ab-27ae-4a76-a226-21654a08d086,codebuddy.conversation_request_id=01a180034cdb75058eaca171af7efdbf"},"__timestamp":"2026-10-28T00:57:48.717Z"}`
	codebuddyStreamTimeoutResult = `{"type":"result","subtype":"error_during_execution","is_error":true,"uuid":"01a18003-bb13-7da6-9445-fa167ac2eaea","session_id":"84eb5ad3-f735-47aa-ac60-dd8eb60c370a","duration_ms":3072,"duration_api_ms":3064,"num_turns":8,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":null,"server_tool_use":null,"service_tier":null},"permission_denials":[],"errors":["Stream timeout: no data received for 3000ms"],"modelUsage":{"custom-local:fake-model":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"contextWindow":128000,"maxOutputTokens":4096}},"_requestId":"01a1800373fa7644ba481906dbf0f567","_meta":{"baggage":"codebuddy.session_id=84eb5ad3-f735-47aa-ac60-dd8eb60c370a,codebuddy.conversation_request_id=01a1800373fa7644ba481906dbf0f567"},"__timestamp":"2026-10-28T00:58:10.580Z"}`
	codebuddyBillingResult       = `{"type":"result","subtype":"error_during_execution","is_error":true,"uuid":"01a18000-c73d-7f79-bfa4-f5f05844b03b","session_id":"1eb4ab66-57e3-42c2-93b4-76ad24f7032a","duration_ms":99,"duration_api_ms":89,"num_turns":2,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":null,"server_tool_use":null,"service_tier":null},"permission_denials":[],"errors":["429 fake business code 14001 (01a18000c71c76559e1a37a55dc3f523/1eb4ab66-57e3-42c2-93b4-76ad24f7032a)"],"errors_info":[{"status":429,"code":14001,"category":"quota","details":"429 fake business code 14001 (01a18000c71c76559e1a37a55dc3f523/1eb4ab66-57e3-42c2-93b4-76ad24f7032a)"}],"modelUsage":{"custom-local:fake-model":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"contextWindow":128000,"maxOutputTokens":4096}},"_requestId":"01a18000c65d7d7791c7a137537e8852","_meta":{"baggage":"codebuddy.session_id=1eb4ab66-57e3-42c2-93b4-76ad24f7032a,codebuddy.conversation_request_id=01a18000c65d7d7791c7a137537e8852"},"__timestamp":"2026-10-28T00:54:57.086Z"}`
	codebuddySuccessResult       = `{"type":"result","subtype":"success","is_error":false,"result":"pong","uuid":"01a17fff-3e27-7df9-b6cf-ece45ce37c7e","session_id":"0143dde2-6786-463c-a35a-709166b2f9f3","duration_ms":102,"duration_api_ms":101,"num_turns":2,"total_cost_usd":0,"usage":{"input_tokens":5,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":null,"server_tool_use":null,"service_tier":null},"permission_denials":[],"modelUsage":{"custom-local:fake-model":{"inputTokens":5,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"contextWindow":128000,"maxOutputTokens":4096}},"_meta":{"codebuddy.ai/contextUsed":5,"codebuddy.ai/usageByCategory":{"systemPrompt":0,"conversation":0,"tools":5,"mcp":0,"skills":0,"version":1},"baggage":"codebuddy.session_id=0143dde2-6786-463c-a35a-709166b2f9f3,codebuddy.conversation_request_id=01a17fff3d3f7dc089b0f85376d3b914"},"_requestId":"01a17fff3d3f7dc089b0f85376d3b914","__timestamp":"2026-10-28T00:53:16.459Z"}`
)

// codebuddyProbeResult returns the result frame with uuid from
// testdata/probe_results.jsonl: the result of the 401, 400 context, 429, 429
// quota, and 500 probes of CodeBuddy 2.175.0 (.tmp/probe/codebuddy/out-*).
func codebuddyProbeResult(t *testing.T, uuid string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "probe_results.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var result struct {
			UUID string `json:"uuid"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &result))
		if result.UUID == uuid {
			return line
		}
	}
	t.Fatalf("no captured result has the uuid %s", uuid)
	return ""
}

// zaiUsageLimitText is the real Z.AI 1308 text that Pi received
// (.tmp/retry-research/transcripts-kimi-pi-omp.md). Its stamp reads as UTC+8,
// so it lifts at zaiUsageLimitResetAt for a failure seen at
// zaiUsageLimitObservedAt.
const zaiUsageLimitText = `429: {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`

var (
	zaiUsageLimitObservedAt = time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC)
	zaiUsageLimitResetAt    = time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC)
)

// newCodebuddyRetryAgent builds an offline agent whose clock reads
// zaiUsageLimitObservedAt.
func newCodebuddyRetryAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(zaiUsageLimitObservedAt).MustWait(testutil.DeadlineContext(t))
	sink := &agenttest.Sink{}
	return newOfflineAgentWithRuntime(t, sink, clock, nil), sink
}

// codebuddyResultLine builds a failed result with errors and errors_info.
func codebuddyResultLine(t *testing.T, errors []string, errorsInfo any) string {
	t.Helper()
	frame := map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": "session-1", "errors": errors}
	if errorsInfo != nil {
		frame["errors_info"] = errorsInfo
	}
	line, err := json.Marshal(frame)
	require.NoError(t, err)
	return string(line)
}

// Each result reaches the real handler, and its verdict follows the turn-end
// row.
func TestCodebuddyResultsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "CodeBuddy reported a transient failure"}
	rateLimited := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "CodeBuddy reported a rate limit"}
	for _, tc := range []struct {
		name string
		line func(*testing.T) string
		want providerkit.TurnFailure
	}{
		{name: "a 529 is transient", line: constLine(codebuddyOverloadedResult), want: transient},
		{name: "a network reset is transient", line: constLine(codebuddyNetworkResetResult), want: transient},
		{name: "a stream timeout is transient", line: constLine(codebuddyStreamTimeoutResult), want: transient},
		{
			name: "the billing code 14001 cancels both retries", line: constLine(codebuddyBillingResult),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported billing code 14001"},
		},
		{name: "a success cancels the server-error retry", line: constLine(codebuddySuccessResult), want: providerkit.TurnFailure{}},
		{
			name: "code 0 with an exceeded quota is a billing block",
			line: probeLine("01a18000-c039-706c-837f-ea77045b4155"),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported an exceeded quota"},
		},
		{name: "code 0 with an ordinary rate limit is rate limited", line: probeLine("01a18000-b926-7c19-ad47-69b4bf64beee"), want: rateLimited},
		{name: "a 500 is transient", line: probeLine("01a18000-482f-7662-a50e-0eea01f9041f"), want: transient},
		{name: "a rejected key with code 0 is not retryable", line: probeLine("01a18000-ce3b-7ae0-8001-92aa1723d401"), want: providerkit.TurnFailure{}},
		{name: "a full context with code 0 is not retryable", line: probeLine("01a18000-d639-749e-bb2d-4b6c54d1806c"), want: providerkit.TurnFailure{}},
		{
			name: "a quota entry with no code is not the quota code 0",
			line: func(t *testing.T) string {
				return codebuddyResultLine(t, []string{"429 slow down"}, []any{map[string]any{"status": 429, "category": "quota", "details": "429 slow down"}})
			},
			want: providerkit.TurnFailure{},
		},
		{
			name: "a string code decodes and decides nothing alone",
			line: func(t *testing.T) string {
				return codebuddyResultLine(t, []string{"agent turn failed without an error payload"}, []any{map[string]any{"code": "cbc_empty_error", "details": "agent turn failed without an error payload"}})
			},
			want: providerkit.TurnFailure{},
		},
		{
			name: "a vendor code in a string code decides",
			line: func(t *testing.T) string {
				return codebuddyResultLine(t, []string{"429 Too Many Requests"}, []any{map[string]any{"status": 429, "code": "insufficient_quota", "category": "quota", "details": "429 Too Many Requests"}})
			},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported insufficient_quota"},
		},
		{
			name: "a null entry before the structured one is skipped",
			line: func(t *testing.T) string {
				return codebuddyResultLine(t, []string{"boom", "529 Overloaded"}, []any{nil, map[string]any{"status": 529, "code": 0, "category": "model_service", "details": "529 Overloaded"}})
			},
			want: transient,
		},
		{
			name: "a Z.AI usage limit schedules at its stamp by the agent clock",
			line: func(t *testing.T) string {
				return codebuddyResultLine(t, []string{zaiUsageLimitText}, []any{map[string]any{"status": 429, "code": 0, "category": "quota", "details": zaiUsageLimitText}})
			},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: zaiUsageLimitResetAt, Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newCodebuddyRetryAgent(t)
			a.HandleOutput([]byte(tc.line(t)))
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

func constLine(line string) func(*testing.T) string {
	return func(*testing.T) string { return line }
}

func probeLine(uuid string) func(*testing.T) string {
	return func(t *testing.T) string { return codebuddyProbeResult(t, uuid) }
}

// A result that ends a turn the user stopped never retries: a stop that took
// effect, and a failure that won the race against the stop.
func TestCodebuddyStoppedTurnDoesNotRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		line       string
		completion agent.MessageCompletion
	}{
		{
			name:       "a stop that took effect",
			line:       `{"type":"result","subtype":"success","is_error":false,"session_id":"session-1","terminal_reason":"aborted_streaming"}`,
			completion: agent.MessageCompletionInterrupted,
		},
		{
			name: "a failure that won the race against the stop",
			line: `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"session-1","errors":["529 Overloaded"],"errors_info":[{"status":529,"code":0,"category":"model_service","details":"529 Overloaded"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newCodebuddyRetryAgent(t)
			a.mu.Lock()
			a.interruptRequests = map[uint64]struct{}{1: {}}
			a.mu.Unlock()
			a.HandleOutput([]byte(tc.line))
			rows := sink.Messages()
			require.NotEmpty(t, rows)
			require.Equal(t, tc.completion, rows[len(rows)-1].Completion)
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// The rejected ExitPlanMode ends the turn with no result. Its own row ends the
// turn, and the server-error retry is cancelled once, after that row.
func TestCodebuddyRejectedPlanExitCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink := newCodebuddyRetryAgent(t)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`))
	rejection := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. They want to keep planning."}]}]}}`)
	a.HandleOutput(rejection)
	a.HandleOutput(rejection)

	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, providerkit.TurnFailure{}, nil)
	timeline := sink.Timeline()
	cancels := 0
	for i, entry := range timeline {
		if entry.Op != "cancel_server_error" {
			continue
		}
		cancels++
		assert.Equal(t, reference.Timeline()[0], entry)
		require.Positive(t, i)
		assert.Equal(t, "message", timeline[i-1].Op, "the cancellation follows the rejection row")
	}
	assert.Equal(t, 1, cancels, "a repeated rejection ends no second turn")
	assert.Len(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), 1, "no other retry operation runs")
}

func TestCodebuddyResultFailure(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "CodeBuddy reported a transient failure"}
	rateLimited := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "CodeBuddy reported a rate limit"}
	billing := func(code string) providerkit.TurnFailure {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported billing code " + code}
	}
	for _, tc := range []struct {
		name string
		line string
		stop bool
		want providerkit.TurnFailure
	}{
		{name: "a research 529 is transient", line: codebuddyOverloadedResult, want: transient},
		{name: "a research network reset is transient", line: codebuddyNetworkResetResult, want: transient},
		{name: "a research stream timeout is transient", line: codebuddyStreamTimeoutResult, want: transient},
		{name: "the research billing code 14001 is a billing block", line: codebuddyBillingResult, want: billing("14001")},
		{name: "a research success is not retryable", line: codebuddySuccessResult, want: providerkit.TurnFailure{}},
		{name: "a stopped turn is not retryable", line: codebuddyOverloadedResult, stop: true, want: providerkit.TurnFailure{}},
		{name: "billing code 14012", line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":14012,"category":"quota"}]}`, want: billing("14012")},
		{name: "billing code 14019", line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":14019,"category":"quota"}]}`, want: billing("14019")},
		{name: "an expired license is not retryable", line: `{"is_error":true,"errors":["403 x"],"errors_info":[{"status":403,"code":14015,"category":"auth"}]}`, want: providerkit.TurnFailure{}},
		{name: "a license code wins over a 5xx status", line: `{"is_error":true,"errors":["500 x"],"errors_info":[{"status":500,"code":14016,"category":"model_service"}]}`, want: providerkit.TurnFailure{}},
		{
			name: "a windowed limit is a usage limit of a week or less",
			line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":6003,"category":"quota"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "CodeBuddy reported a windowed usage limit"},
		},
		{name: "a transient quota code is rate limited", line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":6001,"category":"quota"}]}`, want: rateLimited},
		{name: "quota code 14003 is rate limited", line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":14003,"category":"quota"}]}`, want: rateLimited},
		{name: "a quota code outside a quota entry decides nothing", line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":6001,"category":"auth"}]}`, want: providerkit.TurnFailure{}},
		{name: "a request timeout is transient", line: `{"is_error":true,"errors":["408 x"],"errors_info":[{"status":408,"code":0}]}`, want: transient},
		{name: "a 599 with no category is transient", line: `{"is_error":true,"errors":["599 x"],"errors_info":[{"status":599}]}`, want: transient},
		{name: "a 575 with another category is not retryable", line: `{"is_error":true,"errors":["575 x"],"errors_info":[{"status":575,"category":"custom_model_config"}]}`, want: providerkit.TurnFailure{}},
		{name: "a 600 is not retryable", line: `{"is_error":true,"errors":["600 x"],"errors_info":[{"status":600,"category":"custom_model_config"}]}`, want: providerkit.TurnFailure{}},
		{
			name: "a string code that a vendor rule reads takes its indicative verdict",
			line: `{"is_error":true,"errors":["429 Too Many Requests"],"errors_info":[{"status":429,"code":"rate_limit_exceeded","category":"quota"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			name: "a definitive vendor verdict wins over a transient category",
			line: `{"is_error":true,"errors":["500 Insufficient balance or no resource package. Please recharge."],"errors_info":[{"status":500,"code":0,"category":"model_service"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		// Constructed from the texts of CodeBuddy 2.175.0's own exhausted-balance
		// list, which carry the quota code 0.
		{
			name: "an exhausted quota with the code 0 is a billing block",
			line: `{"is_error":true,"errors":["429 quota exhausted"],"errors_info":[{"status":429,"code":0,"category":"quota","details":"429 quota exhausted"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported an exhausted balance"},
		},
		{
			name: "no available credits is a billing block",
			line: `{"is_error":true,"errors":["429 Too Many Requests"],"errors_info":[{"status":429,"code":0,"category":"quota","details":"no available credits for this account"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported an exhausted balance"},
		},
		{
			name: "a Chinese exhausted quota is a billing block",
			line: `{"is_error":true,"errors":["429 额度已用尽"],"errors_info":[{"status":429,"code":0,"category":"quota"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported an exhausted balance"},
		},
		{
			name: "a word that only contains the text is no billing block",
			line: `{"is_error":true,"errors":["429 x"],"errors_info":[{"status":429,"code":0,"category":"quota","details":"subquota exhaustedness"}]}`,
			want: rateLimited,
		},
		{
			name: "a vendor's monthly limit wins over the quota code 0",
			line: `{"is_error":true,"errors":["429 You've reached your monthly usage limit for this billing cycle."],"errors_info":[{"status":429,"code":0,"category":"quota"}]}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{name: "an empty stream with no structured error is transient", line: `{"is_error":true,"errors":["Empty stream: the model sent nothing"]}`, want: transient},
		{name: "a stream timeout prefix inside a structured error decides nothing alone", line: `{"is_error":true,"errors":["Stream timeout: no data received for 3000ms"],"errors_info":[{"status":400}]}`, want: providerkit.TurnFailure{}},
		{name: "only null entries read as no structured error", line: `{"is_error":true,"errors":["Stream timeout: no data received for 3000ms"],"errors_info":[null]}`, want: transient},
		{name: "a failure with no errors is not retryable", line: `{"is_error":true}`, want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var result resultMessage
			require.NoError(t, json.Unmarshal([]byte(tc.line), &result))
			assert.Equal(t, tc.want, codebuddyResultFailure(result, tc.stop, zaiUsageLimitObservedAt))
		})
	}
}

// A manual /compact ends at its result, and that end never schedules a retry,
// whatever the result states. The verdict is the compaction's own, once, after
// the turn-end row.
func TestCodebuddyManualCompactionEndNeverSchedulesARetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		line    func(*testing.T) string
		stopped bool
	}{
		{name: "a success", line: constLine(codebuddySuccessResult)},
		{name: "a 529", line: constLine(codebuddyOverloadedResult)},
		{name: "a network reset", line: constLine(codebuddyNetworkResetResult)},
		{name: "a 429 rate limit", line: probeLine("01a18000-b926-7c19-ad47-69b4bf64beee")},
		{name: "the billing code 14001", line: constLine(codebuddyBillingResult)},
		{
			name: "a Z.AI usage limit with a stated reset",
			line: func(t *testing.T) string {
				return codebuddyResultLine(t, []string{zaiUsageLimitText}, []any{map[string]any{"status": 429, "code": 0, "category": "quota", "details": zaiUsageLimitText}})
			},
		},
		{
			name:    "a stop that took effect",
			line:    constLine(`{"type":"result","subtype":"success","is_error":false,"session_id":"session-1","terminal_reason":"aborted_streaming"}`),
			stopped: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newCodebuddyRetryAgent(t)
			require.NoError(t, a.SendInput("/compact keep the current task", nil))
			if tc.stopped {
				a.mu.Lock()
				a.interruptRequests = map[uint64]struct{}{1: {}}
				a.mu.Unlock()
			}
			a.HandleOutput([]byte(tc.line(t)))
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// The compaction mark belongs to the /compact turn alone. The next turn's
// result takes the classifier's verdict again.
func TestCodebuddyTurnAfterManualCompactionTakesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodebuddyRetryAgent(t)
	require.NoError(t, a.SendInput("/compact", nil))
	a.HandleOutput([]byte(codebuddyOverloadedResult))
	require.NoError(t, a.SendInput("continue", nil))
	a.HandleOutput([]byte(codebuddyOverloadedResult))
	want := append(providerkittest.TurnEndThen(providerkit.CompactionEnd()),
		providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient})...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A /compact that arrives during a running conversation turn starts no turn,
// so it never marks that turn. The turn keeps its own verdict.
func TestCodebuddyCompactDuringARunningTurnDoesNotMarkTheTurn(t *testing.T) {
	t.Parallel()
	a, sink := newCodebuddyRetryAgent(t)
	require.NoError(t, a.SendInput("explain the plan", nil))
	require.ErrorIs(t, a.SendInput("/compact", nil), agent.ErrAgentBusy)
	a.HandleOutput([]byte(codebuddyOverloadedResult))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A process exit during a manual /compact ends its turn with the compaction's
// verdict, once. An exit after the compaction's result applies no second one.
func TestCodebuddyProcessExitEndsAManualCompactionOnce(t *testing.T) {
	t.Parallel()
	for _, resultFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("the result comes first: ", resultFirst), func(t *testing.T) {
			t.Parallel()
			a, sink := newCodebuddyRetryAgent(t)
			require.NoError(t, a.SendInput("/compact", nil))
			want := []agenttest.TimelineEntry{{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: providerkit.TurnEndedOnCompaction}}
			if resultFirst {
				a.HandleOutput([]byte(codebuddyOverloadedResult))
				want = providerkittest.TurnEndThen(providerkit.CompactionEnd())
			}
			a.cleanupAfterProcessExit()
			a.cleanupAfterProcessExit()
			assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.False(t, a.PublishTurnActive().Active)
		})
	}
}

// A process exit during an ordinary turn applies no verdict, as before.
func TestCodebuddyProcessExitDuringAnOrdinaryTurnAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodebuddyRetryAgent(t)
	require.NoError(t, a.SendInput("explain the plan", nil))
	a.cleanupAfterProcessExit()
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

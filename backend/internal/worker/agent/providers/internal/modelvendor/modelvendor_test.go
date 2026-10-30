package modelvendor

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func at(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// The cases marked "real" are verbatim captures from the user's transcripts
// and from oh-my-pi issue 11014. The rest are vendor templates from the
// vendors' error references.
func TestClassifyError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		text       string
		observedAt time.Time
		want       providerkit.TurnFailure
		definitive bool
	}{
		// Rule A.
		{
			name:       "real Pi Z.AI 1308 reads the stamp as UTC+8",
			text:       `429: {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-07-14T11:06:14Z")},
			definitive: true,
		},
		{
			name:       "real OpenCode Z.AI 1310",
			text:       `{"error":{"code":"1310","message":"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-09-19 20:42:58"}}`,
			observedAt: at("2026-09-18T09:30:59Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-09-19T12:42:59Z")},
			definitive: true,
		},
		{
			name:       "real Chinese Z.AI 1308",
			text:       `429 已达到 5 小时的使用上限。您的限额将在 2026-09-06 09:04:58 重置。 (type=1308)`,
			observedAt: at("2026-09-05T22:37:09.823Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-09-06T01:04:59Z")},
			definitive: true,
		},
		{
			name:       "real Z.AI 1308 in the Anthropic-endpoint wrapper",
			text:       `429 {"type":"error","error":{"type":"rate_limit_error","code":"1308","message":"[1308][Usage limit reached for 5 hour. Your limit will reset at 2026-09-12 00:35:41][20260912001903b4955568412247d7]"},"request_id":"20260912001903b4955568412247d7"} retry-after-ms=997000`,
			observedAt: at("2026-09-11T16:19:03Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-09-11T16:35:42Z")},
			definitive: true,
		},
		{
			name:       "Z.AI 1308 in a Python dict repr",
			text:       `Error code: 429 - {'error': {'code': '1308', 'message': 'Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13'}}`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-07-14T11:06:14Z")},
			definitive: true,
		},
		{
			name:       "Z.AI 1316 is a usage limit despite its balance sentence",
			text:       `Usage limit reached for the past 5 hours. Insufficient balance for extra usage. Resets at 2026-07-14 19:06:13.`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-07-14T11:06:14Z")},
			definitive: true,
		},
		{
			name:       "Anthropic spend limit",
			text:       `You have reached your API usage limits: please contact sales. You will regain access on 2026-09-01 at 00:00 UTC.`,
			observedAt: at("2026-08-20T00:00:00Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-09-01T00:00:00Z")},
			definitive: true,
		},
		{
			name:       "real Pi Codex usage limit reads resets_at",
			text:       `Codex error: {"type":"error","error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_at":1779003886,"eligible_promo":null,"resets_in_seconds":89585},"status_code":429}`,
			observedAt: time.Unix(1779003886-89585, 0).UTC(),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Unix(1779003886, 0).UTC()},
			definitive: true,
		},
		{
			name:       "ChatGPT resets_in_seconds when resets_at is past",
			text:       `{"error":{"type":"usage_limit_reached","resets_at":1000000000,"resets_in_seconds":3600}}`,
			observedAt: at("2026-07-14T10:00:00Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at("2026-07-14T11:00:00Z")},
			definitive: true,
		},
		// Rule C.
		{
			name:       "a ChatGPT workspace limit is billing",
			text:       `{"error":{"type":"usage_limit_reached","resets_at":1779003886},"headers":{"x-codex-rate-limit-reached-type":"workspace_owner_credits_depleted"}}`,
			observedAt: time.Unix(1779000000, 0),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "real Droid insufficient quota",
			text:       `429 You exceeded your current quota, please check your plan and billing details.`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Kimi suspended account",
			text:       `Your account org-x <ak-x> is suspended due to insufficient balance, please recharge your account or check your plan and billing details`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Kimi monthly limit carries access_terminated_error",
			text:       `{"error":{"message":"You've reached your monthly usage limit for this billing cycle.","type":"access_terminated_error"}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Z.AI 1113 in English",
			text:       `{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Z.AI 1113 in Chinese",
			text:       `您的账户已欠费，请充值后重试`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Z.AI 1311 by its code field",
			text:       `{"error":{"code":1311,"message":"model access"}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "MiMo membership required",
			text:       `{"error":{"code":"membership_required","type":"permission_error","biz_code":30012,"message":"未开通会员或会员已到期，请订阅后使用"}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "DeepSeek insufficient balance",
			text:       `{"error":{"code":"insufficient_balance","message":"Insufficient balance"}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Gemini spending cap",
			text:       `You have reached your spending cap for this project.`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "a Gemini quota of zero is not retryable",
			text:       `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-2.5-pro\nPlease retry in 33.6s.","status":"RESOURCE_EXHAUSTED"}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		{
			name:       "Kimi's organization overage limit",
			text:       `You've reached the Extra Usage limit set for your account by your organization.`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
			definitive: true,
		},
		// Rule B.
		{
			name:       "an implausible Z.AI 1318 stamp keeps its 5-hour window despite the monthly spend limit",
			text:       `Usage limit reached for the past 5 hours. Extra usage is not available due to monthly spend limit. Resets at 2026-07-15 19:06:13.`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name: "real Kimi 5-hour limit",
			text: `429 You've reached your 5-hour usage limit. Your quota will reset when the current 5-hour window ends. To continue now, purchase extra usage or upgrade your plan: https://www.kimi.com/membership/subscription?tab=quota`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name: "Kimi monthly limit without a type",
			text: `You've reached your monthly usage limit for this billing cycle.`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger},
		},
		{
			name: "MiMo weekly limit 30011",
			text: `{"error":{"code":"subscription_quota_exhausted","type":"permission_error","biz_code":30011,"message":"本周用量已满，请等待额度重置或升级套餐"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name: "ChatGPT usage limit with a weekly window and no reset",
			text: `{"error":{"type":"usage_limit_reached","limit_window_minutes":10080}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name: "ChatGPT usage limit with a monthly window and no reset",
			text: `{"error":{"type":"usage_limit_reached","limit_window_minutes":43200}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated},
		},
		{
			name: "Gemini daily quota",
			text: `You have exhausted your daily quota on this model.`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name:       "a 5-hour stamp 25 hours ahead falls to rule B",
			text:       `Usage limit reached for 5 hour. Your limit will reset at 2026-07-15 19:06:13`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name:       "a 1316 stamp in the past falls to rule B, not to billing",
			text:       `Usage limit reached for the past 5 hours. Insufficient balance for extra usage. Resets at 2026-07-13 19:06:13.`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		{
			name:       "a Weekly/Monthly stamp 40 days ahead falls to rule B with a longer window",
			text:       `Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-08-23 20:42:58`,
			observedAt: at("2026-07-14T10:34:45Z"),
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger},
		},
		// Rule D.
		{
			name: "real Pi Z.AI 1302",
			text: `429: {"code":"1302","message":"Rate limit reached for requests"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "Z.AI 1302 in Chinese",
			text: `您的账户已达到速率限制，请您控制请求频率`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "a request ID that holds a billing code does not decide",
			text: `[1302][Rate limit reached for requests][2026091113110000a1113]`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "a Gemini quota of 10 is a rate limit, not a zero quota",
			text: `{"error":{"code":429,"message":"You exceeded your current quota.\n* Quota exceeded for metric: generate_content_free_tier_requests, limit: 10, model: gemini-2.5-flash\nPlease retry in 34.07s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "a JSON-escaped apostrophe reads like the plain one",
			text: `{"error":{"message":"We\u2019re receiving too many requests at the moment."}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "OpenAI organization rate limit",
			text: `Rate limit reached for gpt-5 in organization org-x on tokens per min (TPM): Limit 1, Used 1, Requested 1.`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "Moonshot RPM limit",
			text: `request reached organization max RPM: 200`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "DashScope token limit reuses the quota sentence",
			text: `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota","help":"https://help.aliyun.com/error-code#token-limit"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		{
			name: "the Gemini API reuses the quota sentence for a per-minute limit",
			text: `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 10, model: gemini-2.5-flash\nPlease retry in 34.07s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		// Rule E.
		{
			name: "real Pi Z.AI 1305",
			text: `429: {"code":"1305","message":"The service may be temporarily overloaded, please try again later"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "Z.AI 1234",
			text: `Network error, error id: 7f0a, please try again later`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "real Xiaomi token-plan 500",
			text: `{"error":{"code":"503","message":"Internal Server Error","param":"","type":"ServiceUnavailableError"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "real MiMo transport failure sent as a 400",
			text: `400 data:{"error":{"code":"400","message":"Request failed","param":"Connection prematurely closed BEFORE response","type":""}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name:       "Kimi's risk control blocks the account",
			text:       `429 You've reached your concurrent request limit.`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
			definitive: true,
		},
		{
			name:       "Kimi's account block with a typographic apostrophe in JSON",
			text:       `{"error":{"message":"You\u2019ve reached your concurrent request limit.","type":"rate_limit_reached_error"}}`,
			want:       providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
			definitive: true,
		},
		{
			name: "real Kimi overload sent as a 429",
			text: `429 The engine is currently overloaded, please try again later`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "Anthropic overload",
			text: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "Anthropic api_error in a Python dict repr",
			text: `Error code: 500 - {'type': 'error', 'error': {'type': 'api_error', 'message': 'Internal server error'}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "DeepSeek server overload",
			text: `{"error":{"message":"Server Overloaded","type":"server_error"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "Gemini INTERNAL body",
			text: `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "Gemini capacity",
			text: `No capacity available for model gemini-3-pro on the server`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "a Gemini capacity 429 is transient",
			text: `{"error":{"code":429,"message":"No capacity available for model gemini-2.5-pro on the server","status":"RESOURCE_EXHAUSTED","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "Gemini capacity reason",
			text: `{"error":{"code":503,"message":"overloaded","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		{
			name: "a Z.AI transient code with a JSON-escaped message",
			text: `{\"error\":{\"code\":\"1234\",\"message\":\"网络错误\"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			observedAt := tt.observedAt
			if observedAt.IsZero() {
				observedAt = at("2026-07-14T10:00:00Z")
			}
			verdict, ok := ClassifyError(tt.text, observedAt)
			require.True(t, ok, "the text must give a verdict")
			assert.Equal(t, tt.want.Kind, verdict.Failure.Kind)
			assert.Equal(t, tt.want.Window, verdict.Failure.Window)
			assert.True(t, tt.want.ResetAt.Equal(verdict.Failure.ResetAt), "reset: want %v, got %v", tt.want.ResetAt, verdict.Failure.ResetAt)
			assert.Equal(t, tt.definitive, verdict.Definitive)
			assert.NotEmpty(t, verdict.Failure.Cause)
		})
	}
}

func TestClassifyErrorGivesNoVerdict(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"",
		"   ",
		"this is a discussion about a rate limit",
		"429",
		"rate_limit_error",
		"concurrency",
		"1311",
		"1113 tokens were used",
		`{"max_tokens":1200,"timeout_ms":1305}`,
		"the server error page was blank",
		"500 Internal Server Error",
		"OAuth error: invalid_grant (code=1234)",
		"the upstream answered (type=1311)",
		`{"error":"Internal Server Error"}`,
		"usage_limit_reached_count",
		"Anthropic spend limit. You will regain access on 2026-01-01 at 00:00 UTC.",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			verdict, ok := ClassifyError(text, at("2026-07-14T10:00:00Z"))
			assert.False(t, ok, "verdict: %+v", verdict)
		})
	}
}

func TestClassifyErrorReadsEveryEncodingAlike(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
		`{'error': {'code': 'insufficient_quota', 'message': 'You exceeded your current quota'}}`,
		`{"error": {"code": "insufficient_quota", "message": "You exceeded your current quota"}}`,
		`{\"error\":{\"message\":\"You exceeded your current quota\"}}`,
		`You exceeded your current quota`,
	} {
		verdict, ok := ClassifyError(text, at("2026-07-14T10:00:00Z"))
		require.True(t, ok, text)
		assert.Equal(t, providerkit.TurnFailureBillingBlocked, verdict.Failure.Kind, text)
		assert.True(t, verdict.Definitive, text)
	}
	for _, text := range []string{
		`You've reached your 5-hour usage limit.`,
		"You’ve reached your 5-hour usage limit.",
		`You’ve reached your 5-hour usage limit.`,
	} {
		verdict, ok := ClassifyError(text, at("2026-07-14T10:00:00Z"))
		require.True(t, ok, text)
		assert.Equal(t, providerkit.UsageWindowWeekOrLess, verdict.Failure.Window, text)
	}
}

func TestClassifyErrorIsDeterministic(t *testing.T) {
	t.Parallel()
	text := `{"error":{"code":"usage_not_included","type":"insufficient_quota"}}`
	first, ok := ClassifyError(text, at("2026-07-14T10:00:00Z"))
	require.True(t, ok)
	for range 50 {
		again, _ := ClassifyError(text, at("2026-07-14T10:00:00Z"))
		assert.Equal(t, first, again)
	}
}

func TestClassifyErrorsPreservesSeparateEvidenceAndPrecedence(t *testing.T) {
	t.Parallel()
	observed := at("2026-10-28T09:00:00Z")
	for _, tc := range []struct {
		label               string
		texts               []string
		want                providerkit.TurnFailureKind
		definitive, matched bool
		reset               time.Time
	}{
		{label: "empty"},
		{label: "a definitive zero kind still decides", texts: []string{"You've reached your concurrent request limit.", `{"code":"insufficient_quota"}`}, want: providerkit.TurnFailureNotRetryable, definitive: true, matched: true},
		{label: "unrecognized", texts: []string{"", "bad request"}},
		{label: "later definitive beats earlier indicative", texts: []string{`{"code":"overloaded_error"}`, `{"code":"insufficient_quota"}`}, want: providerkit.TurnFailureBillingBlocked, definitive: true, matched: true},
		{label: "first indicative wins", texts: []string{`{"code":"overloaded_error"}`, `{"code":"rate_limit_exceeded"}`}, want: providerkit.TurnFailureTransient, matched: true},
		{label: "first definitive billing wins", texts: []string{`{"code":"insufficient_quota"}`, `{"code":"usage_limit_reached","resets_in_seconds":3600}`}, want: providerkit.TurnFailureBillingBlocked, definitive: true, matched: true},
		{label: "first definitive reset wins", texts: []string{`{"code":"usage_limit_reached","resets_in_seconds":3600}`, `{"code":"insufficient_quota"}`}, want: providerkit.TurnFailureUsageLimited, definitive: true, matched: true, reset: observed.Add(time.Hour)},
		{label: "separate fields cannot manufacture a reset", texts: []string{`{"code":"usage_limit_reached"}`, `{"resets_in_seconds":3600}`}, want: providerkit.TurnFailureUsageLimited, matched: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			got, matched := ClassifyErrors(tc.texts, observed)
			assert.Equal(t, tc.matched, matched)
			assert.Equal(t, tc.definitive, got.Definitive)
			assert.Equal(t, tc.want, got.Failure.Kind)
			assert.Equal(t, tc.reset, got.Failure.ResetAt)
			if matched {
				assert.NotEmpty(t, got.Failure.Cause)
			}
		})
	}
}

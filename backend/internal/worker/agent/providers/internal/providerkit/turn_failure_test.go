package providerkit

import (
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ApplyTurnFailure verdict table ---

func TestApplyTurnFailure(t *testing.T) {
	t.Parallel()
	resetAt := time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC)

	tests := []struct {
		name    string
		failure TurnFailure
		wantOps []agenttest.TimelineEntry
	}{
		{
			name:    "not retryable cancels the server-error retry",
			failure: TurnFailure{Kind: TurnFailureNotRetryable},
			wantOps: []agenttest.TimelineEntry{
				{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: TurnEndedWithoutRetryableFailure},
			},
		},
		{
			name:    "transient schedules a server-error retry",
			failure: TurnFailure{Kind: TurnFailureTransient, Cause: "503"},
			wantOps: []agenttest.TimelineEntry{
				{Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
			},
		},
		{
			name:    "rate limited schedules a server-error retry",
			failure: TurnFailure{Kind: TurnFailureRateLimited, Cause: "429"},
			wantOps: []agenttest.TimelineEntry{
				{Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
			},
		},
		{
			name:    "usage limited with reset schedules usage then cancels server-error",
			failure: TurnFailure{Kind: TurnFailureUsageLimited, ResetAt: resetAt, Cause: "Z.AI 1308"},
			wantOps: []agenttest.TimelineEntry{
				{Op: "schedule_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, ResetAt: resetAt},
				{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: "the turn ended on a usage limit that lifts at a stated time"},
			},
		},
		{
			name:    "usage limited week-or-less schedules a server-error retry",
			failure: TurnFailure{Kind: TurnFailureUsageLimited, Window: UsageWindowWeekOrLess, Cause: "5-hour limit"},
			wantOps: []agenttest.TimelineEntry{
				{Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
			},
		},
		{
			name:    "usage limited longer cancels the server-error retry",
			failure: TurnFailure{Kind: TurnFailureUsageLimited, Window: UsageWindowLonger, Cause: "monthly limit"},
			wantOps: []agenttest.TimelineEntry{
				{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: "the turn ended on a usage limit that states no reset within a week"},
			},
		},
		{
			name:    "billing blocked cancels both retries",
			failure: TurnFailure{Kind: TurnFailureBillingBlocked, Cause: "insufficient_quota"},
			wantOps: []agenttest.TimelineEntry{
				{Op: "cancel_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Reason: "insufficient_quota, which only a billing or plan change lifts"},
				{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: "insufficient_quota, which only a billing or plan change lifts"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			ApplyTurnFailure(sink, tt.failure, []byte(`{"type":"result"}`))
			assert.Equal(t, tt.wantOps, sink.Timeline())
		})
	}
}

func TestApplyTurnFailure_CopiesThePayload(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	payload := []byte(`{"error":"overloaded"}`)
	ApplyTurnFailure(sink, TurnFailure{Kind: TurnFailureTransient}, payload)
	copy(payload, `{"error":"REUSED!!!!"}`)
	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.JSONEq(t, `{"error":"overloaded"}`, string(sink.LastAutoSchedule().SourcePayload))
}

// --- WithResetHint ---

func TestWithResetHint(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	validReset := observedAt.Add(30 * time.Minute)
	pastReset := observedAt.Add(-time.Minute)
	farReset := observedAt.Add(32 * 24 * time.Hour)

	tests := []struct {
		name      string
		failure   TurnFailure
		resetAt   time.Time
		wantKind  TurnFailureKind
		wantReset time.Time
	}{
		{
			name:      "rate limited with a valid hint becomes usage limited",
			failure:   TurnFailure{Kind: TurnFailureRateLimited, Cause: "429"},
			resetAt:   validReset,
			wantKind:  TurnFailureUsageLimited,
			wantReset: validReset,
		},
		{
			name:      "usage limited without reset with a valid hint gets the reset",
			failure:   TurnFailure{Kind: TurnFailureUsageLimited, Cause: "limit"},
			resetAt:   validReset,
			wantKind:  TurnFailureUsageLimited,
			wantReset: validReset,
		},
		{
			name:     "not retryable is never replaced",
			failure:  TurnFailure{Kind: TurnFailureNotRetryable},
			resetAt:  validReset,
			wantKind: TurnFailureNotRetryable,
		},
		{
			name:     "transient is never replaced",
			failure:  TurnFailure{Kind: TurnFailureTransient},
			resetAt:  validReset,
			wantKind: TurnFailureTransient,
		},
		{
			name:     "billing blocked is never replaced",
			failure:  TurnFailure{Kind: TurnFailureBillingBlocked},
			resetAt:  validReset,
			wantKind: TurnFailureBillingBlocked,
		},
		{
			name:      "usage limited with reset is never replaced",
			failure:   TurnFailure{Kind: TurnFailureUsageLimited, ResetAt: observedAt},
			resetAt:   validReset,
			wantKind:  TurnFailureUsageLimited,
			wantReset: observedAt,
		},
		{
			name:     "a past hint is ignored",
			failure:  TurnFailure{Kind: TurnFailureRateLimited},
			resetAt:  pastReset,
			wantKind: TurnFailureRateLimited,
		},
		{
			name:     "an absent hint is ignored",
			failure:  TurnFailure{Kind: TurnFailureRateLimited},
			resetAt:  time.Time{},
			wantKind: TurnFailureRateLimited,
		},
		{
			name:     "a hint more than 31 days ahead is ignored",
			failure:  TurnFailure{Kind: TurnFailureRateLimited},
			resetAt:  farReset,
			wantKind: TurnFailureRateLimited,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := WithResetHint(tt.failure, tt.resetAt, observedAt)
			assert.Equal(t, tt.wantKind, got.Kind)
			if !tt.wantReset.IsZero() {
				assert.Equal(t, tt.wantReset, got.ResetAt)
			}
		})
	}
}

func TestWithResetHint_AppendsTheCauseSuffix(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	got := WithResetHint(TurnFailure{Kind: TurnFailureRateLimited, Cause: "429"}, observedAt.Add(time.Hour), observedAt)
	assert.Equal(t, "429 (the provider states when to retry)", got.Cause)
}

// --- UsageWindowOf ---

// A compaction's end cancels the server-error retry with its own reason, and
// keeps the usage-limit retry.
func TestApplyTurnFailure_CompactionEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	ApplyTurnFailure(sink, CompactionEnd(), []byte(`{"failed":true}`))

	assert.Equal(t, []agenttest.TimelineEntry{{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: TurnEndedOnCompaction}}, sink.Timeline())
}

// A NotRetryable verdict cancels with its cause when it states one, and with
// the generic reason otherwise.
func TestApplyTurnFailure_NotRetryableStatesItsCause(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		cause, want string
	}{
		"a stated cause":  {cause: "the Kilo gateway reported PROMOTION_MODEL_LIMIT_REACHED", want: "the Kilo gateway reported PROMOTION_MODEL_LIMIT_REACHED"},
		"no stated cause": {want: TurnEndedWithoutRetryableFailure},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			ApplyTurnFailure(sink, TurnFailure{Kind: TurnFailureNotRetryable, Cause: tc.cause}, nil)
			assert.Equal(t, tc.want, sink.LastAutoCancelReason())
		})
	}
}

func TestLongRetryWaitFailure(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		resetAt time.Time
		wantOK  bool
	}{
		{name: "a wait of the threshold stays with the agent", resetAt: observedAt.Add(LongRetryWaitThreshold)},
		{name: "a shorter wait stays with the agent", resetAt: observedAt.Add(time.Minute)},
		{name: "a wait past the threshold ends the turn", resetAt: observedAt.Add(LongRetryWaitThreshold + time.Millisecond), wantOK: true},
		{name: "a wait of 19 hours ends the turn", resetAt: observedAt.Add(19*time.Hour + 14*time.Minute + 47*time.Second), wantOK: true},
		{name: "a wait of the queue's maximum ends the turn", resetAt: observedAt.Add(maxRetryDelay), wantOK: true},
		{name: "a wait past the queue's maximum stays with the agent", resetAt: observedAt.Add(maxRetryDelay + time.Second)},
		{name: "a past instant stays with the agent", resetAt: observedAt.Add(-time.Hour)},
		{name: "no instant stays with the agent", resetAt: time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure, ok := LongRetryWaitFailure(tc.resetAt, observedAt)
			require.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				assert.Equal(t, TurnFailure{}, failure)
				return
			}
			assert.Equal(t, TurnFailureUsageLimited, failure.Kind)
			assert.Equal(t, tc.resetAt, failure.ResetAt)
			assert.NotEmpty(t, failure.Cause)
		})
	}
}

func TestUsageWindowOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want UsageWindow
	}{
		{"5-hour", UsageWindowWeekOrLess},
		{"daily", UsageWindowWeekOrLess},
		{"7 days", UsageWindowWeekOrLess},
		{"7-day", UsageWindowWeekOrLess},
		{"30-day", UsageWindowLonger},
		{"720 hours", UsageWindowLonger},
		{"1 week 1 day", UsageWindowLonger},
		{"0 hours", UsageWindowUnstated},
		{"-1 day", UsageWindowUnstated},
		{"1 hourglass", UsageWindowUnstated},
		{"9999999 hours", UsageWindowLonger},
		{"Weekly/Monthly", UsageWindowLonger},
		{"monthly", UsageWindowLonger},
		{"billing cycle", UsageWindowLonger},
		{"", UsageWindowUnstated},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, UsageWindowOf(tt.text))
		})
	}
}

// --- ParseRetryDelay ---

func TestParseRetryDelay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want time.Duration
		ok   bool
	}{
		{"5h", 5 * time.Hour, true},
		{"23h 59m", 23*time.Hour + 59*time.Minute, true},
		{"~42 min", 42 * time.Minute, true},
		{"19h14m47s", 19*time.Hour + 14*time.Minute + 47*time.Second, true},
		{"34.07s", 34070 * time.Millisecond, true},
		{"1500ms", 1500 * time.Millisecond, true},
		{"0s", 0, true},
		{"-1h", 0, false},
		{"5000000h", 0, false},
		{"garbage", 0, false},
		{"", 0, false},
		{"5h 3h", 0, false},
		{"5m 3min", 0, false},
		{"+5h", 0, false},
		{"Try again in 5h", 0, false},
		{"5 hours", 0, false},
		{"31d", 31 * 24 * time.Hour, true},
		{"31d 1s", 0, false},
		{"99999999999h", 0, false},
		{"2678400000ms", 31 * 24 * time.Hour, true},
		{"2678400001ms", 0, false},
		{"3600000ms", time.Hour, true},
		{"9999999999999ms", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseRetryDelay(tt.text)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// --- RetryAfterInstant ---

func TestRetryAfterInstant(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	// The server clock runs five minutes behind observedAt.
	dateHeader := "Tue, 14 Jul 2026 09:55:00 GMT"

	tests := []struct {
		name    string
		headers map[string]string
		wantOK  bool
		wantAdd time.Duration
	}{
		{
			name:    "seconds",
			headers: map[string]string{"retry-after": "120"},
			wantOK:  true,
			wantAdd: 2 * time.Minute,
		},
		{
			name:    "http date",
			headers: map[string]string{"retry-after": "Tue, 14 Jul 2026 10:30:00 GMT"},
			wantOK:  true,
			wantAdd: 30 * time.Minute,
		},
		{
			name:    "milliseconds win when both are present",
			headers: map[string]string{"retry-after": "120", "retry-after-ms": "5000"},
			wantOK:  true,
			wantAdd: 5 * time.Second,
		},
		{
			name:    "mixed-case header name",
			headers: map[string]string{"Retry-After": "60"},
			wantOK:  true,
			wantAdd: time.Minute,
		},
		{
			name:    "base from the date header",
			headers: map[string]string{"retry-after": "60", "date": dateHeader},
			wantOK:  true,
			wantAdd: -5*time.Minute + time.Minute,
		},
		{
			name:    "an unparsable date header leaves observedAt as the base",
			headers: map[string]string{"retry-after": "60", "date": "yesterday"},
			wantOK:  true,
			wantAdd: time.Minute,
		},
		{
			name:    "a malformed retry-after-ms falls back to retry-after",
			headers: map[string]string{"retry-after-ms": "abc", "retry-after": "120"},
			wantOK:  true,
			wantAdd: 2 * time.Minute,
		},
		{
			name:    "a malformed retry-after-ms alone states nothing",
			headers: map[string]string{"retry-after-ms": "abc"},
			wantOK:  false,
		},
		{
			name:    "negative milliseconds",
			headers: map[string]string{"retry-after-ms": "-5"},
			wantOK:  false,
		},
		{
			name:    "an http date in the past",
			headers: map[string]string{"retry-after": "Tue, 14 Jul 2026 09:00:00 GMT"},
			wantOK:  false,
		},
		{
			name:    "no date header uses observedAt",
			headers: map[string]string{"retry-after": "60"},
			wantOK:  true,
			wantAdd: time.Minute,
		},
		{
			name:    "malformed value",
			headers: map[string]string{"retry-after": "abc"},
			wantOK:  false,
		},
		{
			name:    "zero value",
			headers: map[string]string{"retry-after": "0"},
			wantOK:  false,
		},
		{
			name:    "out of bounds",
			headers: map[string]string{"retry-after": "9999999999"},
			wantOK:  false,
		},
		{
			name:    "no retry headers",
			headers: map[string]string{"content-type": "application/json"},
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := RetryAfterInstant(tt.headers, observedAt)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, observedAt.Add(tt.wantAdd), got)
			}
		})
	}
}

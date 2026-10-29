package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// No probe captured a Gemini CLI failure. Every message below is CONSTRUCTED
// from the Gemini CLI 0.65.0 bundle: the ACP prompt loop of gemini-GLQ4MVLB.js
// sends {code: <HTTP status, or 500>, message} with no data, and
// classifyGoogleError of chunk-DKZARV6U.js writes the quota messages. The five
// audit fixtures come first.
const (
	// The Code Assist capacity limit, whose server message states its reset.
	geminiQuotaResetMessage = "You have exhausted your capacity on this model. Your quota will reset after 19h14m47s."
	// A Google API error with RetryInfo, to which classifyGoogleError appends
	// the delay.
	geminiSuggestedRetryMessage = "Resource has been exhausted (e.g. check quota).\nSuggested retry after 34s."
	// The Gemini API's own delay, which classifyGoogleError reads with
	// /Please retry in ([0-9.]+(?:ms|s))/.
	geminiPleaseRetryMessage = "Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 10, model: gemini-2.5-flash\nPlease retry in 34.07s."
	// The spend cap of a project.
	geminiSpendCapMessage = "You have reached your spending cap for this project."
	// A capacity failure that states no reset.
	geminiCapacityMessage = "No capacity available for model gemini-3-pro on the server"

	// The per-minute quota, to which classifyGoogleError appends a fixed delay.
	geminiPerMinuteMessage = "Quota exceeded for quota metric 'Generate Content API requests per minute'.\nSuggested retry after 60s."
	// The daily quota, which classifyGoogleError rewrites to this sentence.
	geminiDailyMessage = "You have exhausted your daily quota on this model."
	// The ACP prompt loop's own text for a 429 that the stream threw.
	geminiThrownRateLimitMessage = "Rate limit exceeded. Try again later."
	geminiCapacityCodeMessage    = "MODEL_CAPACITY_EXHAUSTED"
	geminiNetworkMessage         = "fetch failed"
	geminiTerminatedMessage      = "terminated"
	geminiInternalBodyMessage    = `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`
	geminiUnknownStreamMessage   = "Unknown stream execution error."
	geminiBadRequestMessage      = "Request contains an invalid argument."
)

var geminiObservedAt = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func TestGeminiTurnFailureClassifiesEveryFixture(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		code    int
		message string
		want    providerkit.TurnFailure
	}{
		"a quota reset after a compound delay": {
			429, geminiQuotaResetMessage,
			providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: geminiObservedAt.Add(19*time.Hour + 14*time.Minute + 47*time.Second)},
		},
		"a suggested retry": {
			429, geminiSuggestedRetryMessage,
			providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: geminiObservedAt.Add(34 * time.Second)},
		},
		"a fractional retry delay": {
			429, geminiPleaseRetryMessage,
			providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: geminiObservedAt.Add(34070 * time.Millisecond)},
		},
		"a spend cap":                {429, geminiSpendCapMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked}},
		"an exhausted capacity":      {429, geminiCapacityMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"an exhausted capacity code": {429, geminiCapacityCodeMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a per-minute quota": {
			429, geminiPerMinuteMessage,
			providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: geminiObservedAt.Add(time.Minute)},
		},
		"a daily quota": {
			429, geminiDailyMessage,
			providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		"a thrown 429":                {429, geminiThrownRateLimitMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited}},
		"a zero delay keeps the rate": {429, "Please retry in 0s.", providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited}},
		"a delay beyond 31 days":      {429, "Your quota will reset after 800h.", providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited}},
		"a closed client":             {499, "The operation was cancelled.", providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a 503":                       {503, "The model is overloaded. Please try again later.", providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a 502":                       {502, "Bad Gateway", providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a network failure":           {500, geminiNetworkMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a terminated stream":         {500, geminiTerminatedMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"an INTERNAL body":            {500, geminiInternalBodyMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a 500 that states no cause":  {500, geminiUnknownStreamMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"a 400":                       {400, geminiBadRequestMessage, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"a 501":                       {501, "Not Implemented", providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"a JSON-RPC code":             {-32603, "Internal error", providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := &providerkit.JSONRPCResponseError{Code: tc.code, Message: tc.message}
			verdict := geminiTurnFailure(err, false, geminiObservedAt)
			assert.Equal(t, tc.want.Kind, verdict.Failure.Kind)
			assert.True(t, tc.want.ResetAt.Equal(verdict.Failure.ResetAt), "reset at %v, want %v", verdict.Failure.ResetAt, tc.want.ResetAt)
			assert.Equal(t, tc.want.Window, verdict.Failure.Window)
			payload, marshalErr := json.Marshal(map[string]any{"code": tc.code, "message": tc.message})
			require.NoError(t, marshalErr)
			assert.JSONEq(t, string(payload), string(verdict.Payload), "the payload is the error response")
		})
	}
}

// ParseRetryDelay reads a delay alone, so the classifier must extract it from
// the sentence first. Before the extraction, every stated reset fell to a
// verdict with no instant.
func TestGeminiStatedDelayExtractsTheDelayFromTheSentence(t *testing.T) {
	t.Parallel()
	_, wholeParses := providerkit.ParseRetryDelay(geminiQuotaResetMessage)
	assert.False(t, wholeParses, "the strict parser refuses a whole sentence")
	for message, want := range map[string]time.Duration{
		geminiQuotaResetMessage:     19*time.Hour + 14*time.Minute + 47*time.Second,
		geminiSuggestedRetryMessage: 34 * time.Second,
		geminiPleaseRetryMessage:    34070 * time.Millisecond,
		"Please retry in 1500ms.":   1500 * time.Millisecond,
	} {
		got, ok := geminiStatedDelay(message)
		require.True(t, ok, message)
		assert.Equal(t, want, got, message)
	}
	for _, message := range []string{"", geminiDailyMessage, "Please retry in a moment.", "Please retry in -5s."} {
		_, ok := geminiStatedDelay(message)
		assert.False(t, ok, message)
	}
}

func TestGeminiTurnFailureGivesNotRetryableWithoutAFailure(t *testing.T) {
	t.Parallel()
	quota := &providerkit.JSONRPCResponseError{Code: 429, Message: geminiQuotaResetMessage}
	for name, tc := range map[string]struct {
		err     error
		stopped bool
	}{
		"a stop":                 {quota, true},
		"a successful prompt":    {nil, false},
		"an error with no reply": {errors.New("the agent exited"), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			verdict := geminiTurnFailure(tc.err, tc.stopped, geminiObservedAt)
			assert.Equal(t, providerkit.TurnFailureNotRetryable, verdict.Failure.Kind)
		})
	}
}

// A wrapped error still reveals its JSON-RPC response.
func TestGeminiTurnFailureUnwrapsTheResponseError(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("session/prompt: %w", &providerkit.JSONRPCResponseError{Code: 503, Message: "Service Unavailable"})
	assert.Equal(t, providerkit.TurnFailureTransient, geminiTurnFailure(err, false, geminiObservedAt).Failure.Kind)
}

// geminiCapacityDelayMessage is the message of a 429 that states an exhausted
// capacity and a delay of 69287 s. Gemini CLI reads a delay of more than five
// minutes with the capacity wording as an exhausted capacity, which it waits out
// inside the turn.
const geminiCapacityDelayMessage = "You have exhausted your capacity on this model. Please retry in 69287s."

// geminiLongWaitDelay is the delay, with Gemini's own jitter, of the warning in
// geminiLongWaitWarning.
const geminiLongWaitDelay = 81331625 * time.Millisecond

// geminiLongWaitWarning is the debug log line that Gemini CLI 0.65.0 wrote
// before it waited out that capacity, captured against a local server that
// answered the 429 (.tmp/gemrepro).
const geminiLongWaitWarning = "[2026-10-29T08:17:13.654Z] [WARN] Attempt 1 failed: " + geminiCapacityDelayMessage + ". Retrying after 81331625ms..."

// geminiWarnLine is text as Gemini's debug log writes a warning: after its time
// and its level.
func geminiWarnLine(text string) string {
	return "[2026-10-28T09:00:00.000Z] [WARN] " + text
}

func TestGeminiRetryWarningReader(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		lines       []string
		wantMessage string
		wantDelay   time.Duration
		wantOK      bool
	}{
		{name: "the captured warning", lines: []string{geminiLongWaitWarning}, wantMessage: geminiCapacityDelayMessage, wantDelay: geminiLongWaitDelay, wantOK: true},
		{
			name:        "a warning whose message spans lines",
			lines:       []string{geminiWarnLine("Attempt 2 failed: Resource has been exhausted (e.g. check quota)."), "Suggested retry after 34s. Retrying after 34512ms..."},
			wantMessage: "Resource has been exhausted (e.g. check quota).\nSuggested retry after 34s",
			wantDelay:   34512 * time.Millisecond, wantOK: true,
		},
		{name: "a later warning replaces an unfinished one", lines: []string{geminiWarnLine("Attempt 1 failed: unfinished"), geminiWarnLine("Attempt 2 failed: done. Retrying after 10ms...")}, wantMessage: "done", wantDelay: 10 * time.Millisecond, wantOK: true},
		{name: "a line of another source", lines: []string{"[2026-10-28T09:00:00.000Z] [DEBUG] Hook system initialized successfully"}},
		{name: "the same text at another level", lines: []string{"[2026-10-28T09:00:00.000Z] [LOG] Attempt 1 failed: x. Retrying after 700000ms..."}},
		{name: "the warning without the debug log's prefix", lines: []string{"Attempt 1 failed: x. Retrying after 700000ms..."}},
		{name: "the backoff of a server error states no delay", lines: []string{geminiWarnLine("Attempt 1 failed with status 500. Retrying with backoff...")}},
		{name: "a delay line with no warning before it", lines: []string{"x. Retrying after 10ms..."}},
		{name: "a delay of more than 12 digits", lines: []string{geminiWarnLine("Attempt 1 failed: x. Retrying after 1234567890123ms...")}},
		{name: "an empty line", lines: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reader geminiRetryWarningReader
			var message string
			var delay time.Duration
			var ok bool
			for _, line := range tc.lines {
				message, delay, ok = reader.read(line)
			}
			require.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantMessage, message)
			assert.Equal(t, tc.wantDelay, delay)
			if ok {
				assert.Empty(t, reader.pending, "a finished warning leaves no line behind")
			}
		})
	}
}

// A warning that never states its delay holds a limited number of lines, and
// a delay line after the limit decides nothing.
func TestGeminiRetryWarningReaderDropsAWarningThatNeverEnds(t *testing.T) {
	t.Parallel()
	var reader geminiRetryWarningReader
	_, _, ok := reader.read(geminiWarnLine("Attempt 1 failed: a message"))
	require.False(t, ok)
	for range geminiRetryWarningMaxLines - 1 {
		_, _, ok = reader.read("more of the message")
		require.False(t, ok)
	}
	_, _, ok = reader.read("end. Retrying after 10ms...")
	assert.False(t, ok, "the warning passed its line limit")
	assert.Empty(t, reader.pending)
}

// newGeminiStopAgent builds the agent of newGeminiRetryAgent with a stdin that
// records the frames that a stop writes.
func newGeminiStopAgent(t *testing.T, sink *agenttest.Sink) (*Agent, *agenttest.Stdin) {
	t.Helper()
	a := newGeminiRetryAgent(t, sink)
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)
	return a, stdin
}

func geminiSessionCancels(stdin *agenttest.Stdin) int {
	return strings.Count(stdin.String(), `"method":"session/cancel"`)
}

// Gemini waits out an exhausted capacity inside the open turn for about 22
// hours here, and states the wait only in its debug log. LeapMux stops the
// turn once, and the queue retries at the instant that Gemini would.
func TestGeminiLongRetryWaitStopsTheTurnAndSchedulesTheReset(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, stdin := newGeminiStopAgent(t, sink)

	a.observeGeminiDebugLog(geminiLongWaitWarning)
	require.Equal(t, 1, geminiSessionCancels(stdin), "the long wait stops the turn")
	a.observeGeminiDebugLog(geminiLongWaitWarning)
	require.Equal(t, 1, geminiSessionCancels(stdin), "a second warning stops nothing more")
	a.FinishPromptRequestForTest(geminiReplaySessionID, json.RawMessage(`{"stopReason":"cancelled"}`), nil)

	lastRow, retries := geminiRetryTimeline(t, sink.Timeline())
	assert.Equal(t, "turn_end", lastRow)
	require.Equal(t, []string{"schedule_usage_limit", "cancel_server_error"}, geminiRetryOps(retries))
	assert.True(t, geminiObservedAt.Add(geminiLongWaitDelay).Equal(retries[0].ResetAt), "reset at %v", retries[0].ResetAt)
	assert.JSONEq(t, `{"message":`+strconv.Quote(geminiCapacityDelayMessage)+`,"retryDelayMs":81331625}`, string(sink.LastAutoSchedule().SourcePayload))
}

// A wait of 10 minutes or less stays with Gemini, and so does a warning while
// no prompt of LeapMux's runs.
func TestGeminiShortRetryWaitStopsNothing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		warning      string
		promptActive bool
	}{
		"a per-minute delay":       {warning: geminiWarnLine("Attempt 1 failed: " + geminiPleaseRetryMessage + ". Retrying after 34070ms..."), promptActive: true},
		"a delay of ten minutes":   {warning: geminiWarnLine("Attempt 1 failed: x. Retrying after 600000ms..."), promptActive: true},
		"a long wait with no turn": {warning: geminiLongWaitWarning},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, stdin := newGeminiStopAgent(t, sink)
			a.SetPromptActiveForTest(tc.promptActive)

			a.observeGeminiDebugLog(tc.warning)

			assert.Zero(t, geminiSessionCancels(stdin))
			a.retryMu.Lock()
			defer a.retryMu.Unlock()
			assert.Nil(t, a.longWait)
		})
	}
}

// A reader who stops the turn after LeapMux stopped it for a long wait gets no
// retry, as after any other stop of the reader.
func TestGeminiReaderStopAfterALongWaitSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for name, stop := range map[string]func(*Agent) error{
		"Interrupt": func(a *Agent) error { return a.Interrupt(agent.NewStopContext(nil)) },
		"a raw session/cancel": func(a *Agent) error {
			return a.SendRawInput([]byte(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"`+geminiReplaySessionID+`"}}`), agent.NewStopContext(nil))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newGeminiStopAgent(t, sink)
			a.observeGeminiDebugLog(geminiLongWaitWarning)
			require.NoError(t, stop(a))
			a.FinishPromptRequestForTest(geminiReplaySessionID, json.RawMessage(`{"stopReason":"cancelled"}`), nil)

			_, retries := geminiRetryTimeline(t, sink.Timeline())
			assert.Equal(t, []string{"cancel_server_error"}, geminiRetryOps(retries))
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// The next prompt forgets the long wait and the reader's stop of the last one,
// and so does a context clear. The stopped turn ends first, as it does in a
// session, because the base keeps its note of LeapMux's stop until then.
func TestGeminiNewPromptForgetsTheLastRetryWait(t *testing.T) {
	t.Parallel()
	for name, forget := range map[string]func(*Agent){
		"a prompt start":  func(a *Agent) { a.HooksForTest().PromptPrepared() },
		"a context clear": func(a *Agent) { a.HooksForTest().ClearProviderState() },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newGeminiStopAgent(t, sink)
			a.observeGeminiDebugLog(geminiLongWaitWarning)
			a.FinishPromptRequestForTest(geminiReplaySessionID, json.RawMessage(`{"stopReason":"cancelled"}`), nil)
			a.noteUserStop()
			firstPrompt := len(sink.Timeline())

			forget(a)
			a.SetPromptActiveForTest(true)
			a.FinishPromptRequestForTest(geminiReplaySessionID, nil, &providerkit.JSONRPCResponseError{Code: 503, Message: "The model is overloaded."})

			_, retries := geminiRetryTimeline(t, sink.Timeline()[firstPrompt:])
			assert.Equal(t, []string{"schedule_server_error"}, geminiRetryOps(retries), "the prompt's own error decides")
		})
	}
}

// newGeminiRetryAgent builds a Gemini agent with the hooks that Start
// installs, whose prompt end runs on a pinned agent clock.
func newGeminiRetryAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(geminiObservedAt)
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "gemini-1", ProviderName: "gemini", Clock: clock})
	hooks := a.configure(context.Background(), agent.Options{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}, agent.NewProviderServices(sink))
	// The start keeps the sink of the hooks as the agent's own.
	a.SetSinkForTest(hooks.Sink)
	hooks.Sink = nil
	*a.HooksForTest() = hooks
	a.SetSessionIDForTest(geminiReplaySessionID)
	a.SetPromptActiveForTest(true)
	return a
}

// geminiRetryTimeline splits the timeline at its last row. It returns that
// row's operation and every retry operation after it, and fails when a retry
// operation precedes the row.
func geminiRetryTimeline(t *testing.T, timeline []agenttest.TimelineEntry) (lastRow string, retries []agenttest.TimelineEntry) {
	t.Helper()
	for _, entry := range timeline {
		switch entry.Op {
		case "message", "turn_end", "notification", "leapmux_notification":
			require.Empty(t, retries, "a retry operation precedes the row %q", entry.Op)
			lastRow = entry.Op
		default:
			retries = append(retries, entry)
		}
	}
	return lastRow, retries
}

func geminiRetryOps(entries []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(entries))
	for _, entry := range entries {
		ops = append(ops, entry.Op)
	}
	return ops
}

func TestGeminiPromptEndAppliesOneVerdictAfterTheFailureNote(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		code    int
		message string
		want    []string
		resetAt time.Time
	}{
		"a quota reset schedules a usage-limit retry at the reset": {
			429, geminiQuotaResetMessage, []string{"schedule_usage_limit", "cancel_server_error"},
			geminiObservedAt.Add(19*time.Hour + 14*time.Minute + 47*time.Second),
		},
		"a daily quota schedules a server-error retry":        {429, geminiDailyMessage, []string{"schedule_server_error"}, time.Time{}},
		"a network failure schedules a server-error retry":    {500, geminiNetworkMessage, []string{"schedule_server_error"}, time.Time{}},
		"a spend cap cancels both retries":                    {429, geminiSpendCapMessage, []string{"cancel_usage_limit", "cancel_server_error"}, time.Time{}},
		"a 400 cancels the server-error retry":                {400, geminiBadRequestMessage, []string{"cancel_server_error"}, time.Time{}},
		"a 500 that states no cause cancels the server retry": {500, geminiUnknownStreamMessage, []string{"cancel_server_error"}, time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newGeminiRetryAgent(t, sink)
			a.FinishPromptRequestForTest(geminiReplaySessionID, nil, &providerkit.JSONRPCResponseError{Code: tc.code, Message: tc.message})

			lastRow, retries := geminiRetryTimeline(t, sink.Timeline())
			assert.Equal(t, "leapmux_notification", lastRow, "the failure note ends the failed turn")
			require.Equal(t, tc.want, geminiRetryOps(retries))
			if !tc.resetAt.IsZero() {
				assert.True(t, tc.resetAt.Equal(retries[0].ResetAt), "reset at %v", retries[0].ResetAt)
			}
			if retries[0].Op == "schedule_server_error" || retries[0].Op == "schedule_usage_limit" {
				payload, err := json.Marshal(map[string]any{"code": tc.code, "message": tc.message})
				require.NoError(t, err)
				assert.JSONEq(t, string(payload), string(sink.LastAutoSchedule().SourcePayload))
			}
		})
	}
}

func TestGeminiSuccessfulAndStoppedPromptsCancelTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]func(*Agent){
		"a successful prompt": func(a *Agent) {
			a.FinishPromptRequestForTest(geminiReplaySessionID, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
		},
		"a stop": func(a *Agent) {
			a.NoteInterruptRequestedForTest()
			a.FinishPromptRequestForTest(geminiReplaySessionID, nil, &providerkit.JSONRPCResponseError{Code: 429, Message: geminiQuotaResetMessage})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newGeminiRetryAgent(t, sink)
			end(a)
			_, retries := geminiRetryTimeline(t, sink.Timeline())
			assert.Equal(t, []string{"cancel_server_error"}, geminiRetryOps(retries))
		})
	}
}

package letta

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// The research frames of Letta Code 0.34.23 (.tmp/retry-research/commandcode-letta.md),
// copied verbatim: a 503 after Letta's own retries, a ChatGPT usage limit, and
// a success.
const (
	lettaUnavailableTurnEnd  = `{"type":"turn_finished","turn_id":"turn-0f9f6cc2-bdd9-40f1-9cad-c22b5e686bab","stop_reason":"llm_api_error","run_id":"local-run-4","error":"{\n  \"error\": {\n    \"error\": {\n      \"type\": \"llm_error\",\n      \"message\": \"503: {\\\"message\\\":\\\"Service Unavailable\\\",\\\"type\\\":\\\"server_error\\\"}\",\n      \"detail\": \"503: {\\\"message\\\":\\\"Service Unavailable\\\",\\\"type\\\":\\\"server_error\\\"}\"\n    },\n    \"run_id\": \"local-run-4\"\n  }\n}","runtime":{"agent_id":"agent-local-f53704c6-6654-48cd-ba84-3409d4968323","conversation_id":"local-conv-2"},"event_seq":53,"emitted_at":"2026-10-28T01:15:25.738Z","idempotency_key":"turn_finished:53:34ea2523-11e1-4a82-9d14-a1d8374cb97b"}`
	lettaChatGPTLimitTurnEnd = `{"type":"turn_finished","turn_id":"turn-2ca735e1-62e0-4241-ac46-ae471a5d681e","stop_reason":"error","run_id":"local-run-1","error":"{\n  \"error\": {\n    \"error\": {\n      \"type\": \"local_backend_error\",\n      \"message\": \"You have hit your ChatGPT usage limit (plus plan). Try again in ~2263 min.\",\n      \"detail\": \"You have hit your ChatGPT usage limit (plus plan). Try again in ~2263 min.\"\n    },\n    \"run_id\": \"local-run-1\"\n  }\n}","runtime":{"agent_id":"agent-local-3843f560-3eb8-4285-9c4c-ac6e33a6405d","conversation_id":"local-conv-6"},"event_seq":17,"emitted_at":"2026-10-28T01:17:12.045Z","idempotency_key":"turn_finished:17:ac39d041-d46e-442c-8ddb-337846c68e5a"}`
	lettaSuccessTurnEnd      = `{"type":"turn_finished","turn_id":"turn-dbbbf782-ffca-4afc-989b-e8b56de07e23","stop_reason":"end_turn","run_id":"local-run-1","runtime":{"agent_id":"agent-local-a9bd0a8e-0e76-400a-b297-461f704dad74","conversation_id":"local-conv-1"},"event_seq":16,"emitted_at":"2026-10-28T01:14:32.999Z","idempotency_key":"turn_finished:16:464afe7f-8c4b-4979-9bb6-da1979538439"}`
)

// lettaObservedAt is the agent clock of the retry tests: the instant the
// ChatGPT usage limit capture was emitted.
var lettaObservedAt = time.Date(2026, 10, 28, 1, 17, 12, 0, time.UTC)

// newLettaRetryAgent builds an agent with no process, whose clock reads
// lettaObservedAt.
func newLettaRetryAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(lettaObservedAt).MustWait(testutil.DeadlineContext(t))
	a, sink := newLettaChildTestAgent(t)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "letta-retry-test", ProviderName: "letta", Stdin: agenttest.NopStdin(io.Discard), Clock: clock,
	})
	return a, sink
}

// lettaProbeFrames reads every stream_delta and turn_finished frame of a
// Letta Code 0.34.23 probe run (.tmp/probe/letta/run-<name>.log).
func lettaProbeFrames(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "probe_run_"+name+".jsonl"))
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// lettaErrorMessageDelta is the root error_message delta of one failed model
// request, with its retryable flag.
func lettaErrorMessageDelta(t *testing.T, subagentID string, retryable bool) []byte {
	t.Helper()
	frame := map[string]any{"type": "stream_delta", "delta": map[string]any{"message_type": "error_message", "message": "503: Service Unavailable", "error_type": "llm_error", "retryable": retryable}}
	if subagentID != "" {
		frame["subagent_id"] = subagentID
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return raw
}

// Each probe run reaches the real handler, and the turn's verdict follows its
// turn-end row.
func TestLettaProbeRunsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}
	for _, tc := range []struct {
		name string
		run  string
		want providerkit.TurnFailure
	}{
		{name: "a 503 after Letta's own retries is transient", run: "503", want: transient},
		{
			// Letta calls every 429 retryable. The vendor reads the body as a rate
			// limit, which schedules the same server-error retry.
			name: "a plain 429 that Letta calls retryable is rate limited", run: "s429plain",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{name: "a dropped stream is transient", run: "drop", want: transient},
		{
			name: "missing credits cancel both retries", run: "s402credits",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported insufficient_quota"},
		},
		{name: "a rejected key is not retryable", run: "s401", want: providerkit.TurnFailure{}},
		{name: "a full context is not retryable", run: "s400ctx", want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newLettaRetryAgent(t)
			for _, frame := range lettaProbeFrames(t, tc.run) {
				a.HandleOutput([]byte(frame))
			}
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// The research turn ends reach the real handler.
func TestLettaResearchTurnEndsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		line string
		want providerkit.TurnFailure
	}{
		{
			name: "a 503 with no error_message delta is transient",
			line: lettaUnavailableTurnEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"},
		},
		{
			name: "a ChatGPT usage limit schedules at the clock plus its delay and the rounding margin",
			line: lettaChatGPTLimitTurnEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: lettaObservedAt.Add(2263*time.Minute + 30*time.Second), Cause: "Letta reported a usage limit (the provider states when to retry)"},
		},
		{name: "a success cancels the server-error retry", line: lettaSuccessTurnEnd, want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newLettaRetryAgent(t)
			a.HandleOutput([]byte(tc.line))
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// A root error_message delta that states retryable false keeps an
// llm_api_error turn end from retrying.
func TestLettaRootErrorThatIsNotRetryableDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	a.HandleOutput(lettaErrorMessageDelta(t, "", false))
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-1","stop_reason":"llm_api_error","error":"503: Service Unavailable"}`))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A child's error_message delta never decides the root turn.
func TestLettaChildErrorDoesNotSetTheRootFlag(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	a.HandleOutput(lettaErrorMessageDelta(t, lettaTestChildID, false))
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-1","stop_reason":"llm_api_error","error":"503: Service Unavailable"}`))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The root flag of one turn never decides the next turn: the turn end resets
// it.
func TestLettaRootErrorFlagDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	a.HandleOutput(lettaErrorMessageDelta(t, "", false))
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-1","stop_reason":"error","error":"401: Incorrect API key provided"}`))
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-2","stop_reason":"llm_api_error","error":"503: Service Unavailable"}`))
	first := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(first))
	assert.Equal(t, first, timeline[:len(first)])
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}), timeline[len(first):])
}

// A turn end that Letta repeats applies no second verdict.
func TestLettaRepeatedTurnEndAppliesOneVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	a.HandleOutput([]byte(lettaUnavailableTurnEnd))
	a.HandleOutput([]byte(lettaUnavailableTurnEnd))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The research usage read response reports the ChatGPT plan quota. It ends no
// turn, so it applies no verdict.
func TestLettaUsageReadResponseAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	a.HandleOutput([]byte(`{"type":"chatgpt_usage_read_response","request_id":"usage-1","success":true,"target":"local","usage":{"providerName":"openai-codex","fetchedAt":"2026-10-28T01:20:37.750Z","planType":"plus","limitReached":true,"rateLimitReachedType":"rate_limit_reached","primary":{"label":"primary","usedPercent":100,"windowDurationMins":300,"resetsAt":1793160000},"secondary":{"label":"secondary","usedPercent":41,"windowDurationMins":10080,"resetsAt":1793700000},"additional":[],"credits":null,"individualLimit":null,"summary":"Usage: 5h 0% left resets in 3h · 7d 59% left resets in 7d"}}`))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A cancelled turn never retries.
func TestLettaCancelledTurnDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-1","stop_reason":"cancelled","error":"503: Service Unavailable"}`))
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	require.Equal(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// newLettaInterruptAgent builds an agent on a fake App Server whose clock
// reads lettaObservedAt and whose root turn runs.
func newLettaInterruptAgent(t *testing.T) (*fakeAppServer, *Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(lettaObservedAt).MustWait(testutil.DeadlineContext(t))
	sink := &agenttest.Sink{}
	fake, a := newFakeAppServerWithClock(t, sink, clock)
	a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	return fake, a, sink
}

// A failure that Letta reports after the user stopped the turn won the race
// against the abort. The user asked to stop, so the failure must not re-send
// the stopped prompt.
func TestLettaFailureAfterAStopDoesNotRetry(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaInterruptAgent(t)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, "abort_message", fake.nextCommand(t)["type"])
	a.HandleOutput([]byte(lettaUnavailableTurnEnd))
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	require.Equal(t, agent.MessageCompletionError, rows[len(rows)-1].Completion, "the failure keeps its own completion")
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The stop of one turn never decides the next turn: the next turn start clears
// it.
func TestLettaStopDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaInterruptAgent(t)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, "abort_message", fake.nextCommand(t)["type"])
	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	require.NoError(t, a.SendInput("Continue after the cancelled turn.", nil))
	a.HandleOutput([]byte(lettaUnavailableTurnEnd))

	stopped := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(stopped))
	assert.Equal(t, stopped, timeline[:len(stopped)])
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}),
		timeline[len(stopped):], "the second turn was not stopped, so its failure retries")
}

// A stop with no turn running sends nothing and notes nothing.
func TestLettaStopWhileIdleDoesNotDecideTheNextTurn(t *testing.T) {
	t.Parallel()
	_, a, sink := newLettaInterruptAgent(t)
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	a.HandleOutput([]byte(lettaUnavailableTurnEnd))

	completed := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(completed))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}),
		timeline[len(completed):])
}

// The end of a manual compaction is a root turn end, so a flag that the
// compaction's model request left decides no later turn. The compaction applies
// its own verdict, and the next turn takes the classifier's verdict again.
func TestLettaManualCompactionEndResetsTheRootErrorFlag(t *testing.T) {
	t.Parallel()
	a, sink := newLettaRetryAgent(t)
	command := a.prepareManualCompaction(runtimeScope{AgentID: "agent-local-1", ConversationID: "local-conv-1"}, "")
	a.HandleOutput(lettaErrorMessageDelta(t, "", false))
	response, err := json.Marshal(map[string]any{"type": "execute_command_response", "request_id": command.RequestID, "success": false, "output": "Native command outcome."})
	require.NoError(t, err)
	a.HandleOutput(response)
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-1","stop_reason":"llm_api_error","error":"503: Service Unavailable"}`))
	want := append(compactionVerdictOps(), providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"})...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestLettaTurnFailure(t *testing.T) {
	t.Parallel()
	retryable, notRetryable := true, false
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}
	usageAt := func(delay time.Duration) providerkit.TurnFailure {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: lettaObservedAt.Add(delay), Cause: "Letta reported a usage limit (the provider states when to retry)"}
	}
	unstatedUsage := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Cause: "Letta reported a usage limit"}
	billing := func(reason string) providerkit.TurnFailure {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Letta reported " + reason}
	}
	turnEndError := func(t *testing.T, line string) (string, string) {
		t.Helper()
		var finished lettaTurnFinished
		require.NoError(t, json.Unmarshal([]byte(line), &finished))
		return finished.StopReason, finished.Error
	}
	for _, tc := range []struct {
		name        string
		stopReason  string
		error       string
		line        string
		retryable   *bool
		interrupted bool
		want        providerkit.TurnFailure
	}{
		{name: "the research 503 is transient", line: lettaUnavailableTurnEnd, want: transient},
		{name: "the research 503 with a retryable root error is transient", line: lettaUnavailableTurnEnd, retryable: &retryable, want: transient},
		{name: "the research 503 with a root error that is not retryable is not retryable", line: lettaUnavailableTurnEnd, retryable: &notRetryable, want: providerkit.TurnFailure{}},
		{
			// Letta classifies a model API failure by its status. The vendor's text
			// states a monthly limit, which a retry within the week does not lift.
			name: "a retryable model API failure whose text is a monthly vendor limit is a longer usage limit", stopReason: "llm_api_error", retryable: &retryable,
			error: `429: {"error":{"message":"You have reached your monthly usage limit."}}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name: "a model API failure that is not retryable stays not retryable despite a vendor rate limit", stopReason: "llm_api_error", retryable: &notRetryable,
			error: `429: {"message":"Rate limit reached for requests","type":"rate_limit_error","code":"rate_limit_exceeded"}`,
			want:  providerkit.TurnFailure{},
		},
		{name: "the research ChatGPT usage limit lifts at the clock plus its delay and margin", line: lettaChatGPTLimitTurnEnd, want: usageAt(2263*time.Minute + 30*time.Second)},
		{name: "the research success is not retryable", line: lettaSuccessTurnEnd, want: providerkit.TurnFailure{}},
		{
			name: "a ChatGPT limit that lifts now waits for the rounding margin", stopReason: "error",
			error: "You have hit your ChatGPT usage limit (plus plan). Try again in ~0 min.",
			want:  usageAt(30 * time.Second),
		},
		{
			name: "a ChatGPT limit more than 31 days ahead falls back to an unstated window", stopReason: "error",
			error: "You have hit your ChatGPT usage limit (plus plan). Try again in ~99999 min.",
			want:  unstatedUsage,
		},
		{
			name: "a Letta Cloud quota lifts at the clock plus its delay and margin", stopReason: "error",
			error: "You've hit your usage limit. Resets at 3:45 PM (2h 5m). View usage: https://app.letta.com/settings/organization/usage",
			want:  usageAt(2*time.Hour + 5*time.Minute + 60*time.Second),
		},
		{
			name: "a Letta Cloud quota in whole hours lifts at the clock plus its delay and margin", stopReason: "error",
			error: "You've hit your usage limit. Resets at 15:45 (3h). View usage: https://app.letta.com/settings/organization/usage",
			want:  usageAt(3*time.Hour + 60*time.Second),
		},
		{
			name: "a Letta Cloud quota in minutes lifts at the clock plus its delay and margin", stopReason: "error",
			error: "You've hit your usage limit. Resets at 3:45 PM (5m). View usage: https://app.letta.com/settings/organization/usage",
			want:  usageAt(5*time.Minute + 60*time.Second),
		},
		{name: "a billing reason in the reasons list is a billing block", stopReason: "error", error: `402: {"error":"Payment required","reasons":["not-enough-credits"]}`, want: billing("not-enough-credits")},
		{name: "a tier usage reason is a billing block", stopReason: "error", error: `402: {"reasons":["premium-usage-exceeded"]}`, want: billing("premium-usage-exceeded")},
		{name: "the agents limit is a billing block", stopReason: "error", error: `429: {"reasons":["agents-limit-exceeded"]}`, want: billing("agents-limit-exceeded")},
		{name: "a reason inside a longer token is not a billing block", stopReason: "error", error: `429: {"reasons":["subagents-limit-exceeded"]}`, want: providerkit.TurnFailure{}},
		{
			name: "a definitive vendor verdict wins over the retryable class", stopReason: "llm_api_error", retryable: &retryable,
			error: `402: {"message":"Insufficient credits","type":"insufficient_quota","code":"insufficient_quota"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported insufficient_quota"},
		},
		{
			name: "an indicative vendor verdict decides an error", stopReason: "error",
			error: `429: {"message":"Rate limit reached for requests","type":"rate_limit_error","code":"rate_limit_exceeded"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{name: "an error with no known text is not retryable", stopReason: "error", error: "boom", want: providerkit.TurnFailure{}},
		{name: "an empty error is not retryable", stopReason: "error", want: providerkit.TurnFailure{}},
		{name: "a cancelled turn is not retryable", stopReason: "cancelled", error: "503: Service Unavailable", want: providerkit.TurnFailure{}},
		{name: "an unknown stop reason is not retryable", stopReason: "max_steps", error: "503: Service Unavailable", want: providerkit.TurnFailure{}},
		{name: "a stopped turn's retryable model API failure is not retryable", line: lettaUnavailableTurnEnd, retryable: &retryable, interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's ChatGPT usage limit is not retryable", line: lettaChatGPTLimitTurnEnd, interrupted: true, want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stopReason, errorText := tc.stopReason, tc.error
			if tc.line != "" {
				stopReason, errorText = turnEndError(t, tc.line)
			}
			assert.Equal(t, tc.want, lettaTurnFailure(stopReason, errorText, tc.retryable, tc.interrupted, lettaObservedAt))
		})
	}
}

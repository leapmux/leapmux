package copilot

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copilotCompactionEnd is the timeline of the verdict that a manual compaction
// applies when no turn-end row precedes it.
var copilotCompactionEnd = []agenttest.TimelineEntry{{
	Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	Reason: providerkit.TurnEndedOnCompaction,
}}

// copilotQueryServerError is the data of a root session.error that the
// classifier reads as a transient 5xx failure.
var copilotQueryServerError = map[string]any{"errorType": "query", "statusCode": 500, "message": "Execution failed: 500 Internal Server Error"}

// newCopilotCompactionAgent builds an agent whose requests reach a recording
// stdin, so CompactContext sends its request and the test answers it.
func newCopilotCompactionAgent(t *testing.T) (*Agent, *agenttest.Sink, *agenttest.Stdin) {
	t.Helper()
	a, sink := newNativeCopilotForEvents(t)
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)
	return a, sink, stdin
}

// copilotCompactRequestRe finds the request ID of the session.history.compact
// request in what the agent wrote to stdin.
var copilotCompactRequestRe = regexp.MustCompile(`"id":(\d+),"method":"session\.history\.compact"`)

// answerCopilotCompaction answers the session.history.compact request with
// reply, a "result" or an "error" member. It waits until the reply ends the
// compaction. The reply decides its verdict in the same critical section that
// ends the compaction.
func answerCopilotCompaction(t *testing.T, a *Agent, stdin *agenttest.Stdin, reply string) {
	t.Helper()
	match := copilotCompactRequestRe.FindStringSubmatch(stdin.String())
	require.NotNil(t, match, "the agent sends the compaction request")
	id, err := strconv.ParseInt(match[1], 10, 64)
	require.NoError(t, err)
	require.True(t, a.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,%s}`, id, reply)))))
	require.Eventually(t, func() bool {
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return a.compactingRevision == 0
	}, 30*time.Second, time.Millisecond)
}

// The reply ends a compaction that ran no model turn of its own, and it
// cancels the server-error retry whatever the outcome.
func TestCopilotManualCompactionReplyCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		reply  string
		failed bool
	}{
		{name: "success", reply: `"result":{"success":true,"tokensRemoved":4,"messagesRemoved":1}`},
		{name: "refused", reply: `"result":{"success":false,"tokensRemoved":0,"messagesRemoved":0}`, failed: true},
		{name: "invalid result", reply: `"result":{}`, failed: true},
		{name: "server error", reply: `"error":{"code":-32603,"message":"Execution failed: 503 Service Unavailable"}`, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newCopilotCompactionAgent(t)
			require.NoError(t, a.CompactContext())
			answerCopilotCompaction(t, a, stdin, tc.reply)
			require.Eventually(t, func() bool {
				return len(providerkittest.TurnEndAndRetryOps(sink.Timeline())) > 0
			}, 30*time.Second, time.Millisecond)
			assert.Equal(t, copilotCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
			timeline := sink.Timeline()
			if tc.failed {
				require.Len(t, timeline, 2)
				assert.Equal(t, "leapmux_notification", timeline[0].Op, "the error notice precedes the verdict")
			} else {
				assert.Len(t, timeline, 1)
			}
			active, published := sink.LastTurnActive()
			require.True(t, published)
			assert.False(t, active, "the reply releases the queue")
		})
	}
}

// The runtime can end a root turn while the compaction request runs. That
// turn end applies the compaction's verdict, not the classifier's, and the
// later reply applies none.
func TestCopilotTurnEndInsideManualCompactionAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frames func(session string) []string
	}{
		{name: "a model turn that hits a server error", frames: func(session string) []string {
			return []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", copilotQueryServerError),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			}
		}},
		{name: "an idle without a turn start", frames: func(session string) []string {
			return []string{
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", copilotQueryServerError),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			}
		}},
		{name: "a turn that the reader stopped", frames: func(session string) []string {
			return []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", copilotQueryServerError),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{"aborted": true}),
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newCopilotCompactionAgent(t)
			require.NoError(t, a.CompactContext())
			handleCopilotFrames(a, tc.frames(a.currentNativeSessionID())...)
			want := providerkittest.TurnEndThen(providerkit.CompactionEnd())
			require.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			answerCopilotCompaction(t, a, stdin, `"result":{"success":false}`)
			assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the reply applies no second verdict")
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// A model turn that the runtime started for the compaction can still run when
// the reply arrives. The reply then leaves the verdict to that turn's end, so
// the verdict follows the turn-end row.
func TestCopilotModelTurnThatOutlivesTheCompactionReplyAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newCopilotCompactionAgent(t)
	session := a.currentNativeSessionID()
	require.NoError(t, a.CompactContext())
	handleCopilotFrames(a,
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", copilotQueryServerError),
	)
	answerCopilotCompaction(t, a, stdin, `"result":{"success":true,"tokensRemoved":4,"messagesRemoved":1}`)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the running turn keeps the verdict")
	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.True(t, active, "the model turn still runs")
	handleCopilotFrames(a, copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.Zero(t, sink.AutoScheduleCount())
}

// The runtime can run more than one model turn for one compaction. The
// compaction still applies one verdict, and the classifier reads none of them.
func TestCopilotManualCompactionAppliesOneVerdictForSeveralModelTurns(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newCopilotCompactionAgent(t)
	session := a.currentNativeSessionID()
	require.NoError(t, a.CompactContext())
	handleCopilotFrames(a,
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:51.000Z", map[string]any{}),
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", copilotQueryServerError),
		copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
	)
	answerCopilotCompaction(t, a, stdin, `"result":{"success":true}`)
	assert.Equal(t, []string{"turn_end", "cancel_server_error", "turn_end"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedOnCompaction, sink.LastAutoCancelReason())
	assert.Zero(t, sink.AutoScheduleCount())
}

// The compaction's mark ends with the compaction: the next root turn applies
// the classifier's verdict again, in each order of the reply and the idle.
func TestCopilotTurnAfterManualCompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// compact runs the compaction to its end, and returns the timeline
		// that the compaction leaves.
		compact func(t *testing.T, a *Agent, stdin *agenttest.Stdin, session string) []agenttest.TimelineEntry
	}{
		{name: "a compaction without a model turn", compact: func(t *testing.T, a *Agent, stdin *agenttest.Stdin, _ string) []agenttest.TimelineEntry {
			answerCopilotCompaction(t, a, stdin, `"result":{"success":true}`)
			return copilotCompactionEnd
		}},
		{name: "a model turn that ends before the reply", compact: func(t *testing.T, a *Agent, stdin *agenttest.Stdin, session string) []agenttest.TimelineEntry {
			handleCopilotFrames(a, copilotTurnStartFrame(session), copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:51.000Z", map[string]any{}))
			answerCopilotCompaction(t, a, stdin, `"result":{"success":true}`)
			return providerkittest.TurnEndThen(providerkit.CompactionEnd())
		}},
		{name: "a model turn that outlives the reply", compact: func(t *testing.T, a *Agent, stdin *agenttest.Stdin, session string) []agenttest.TimelineEntry {
			handleCopilotFrames(a, copilotTurnStartFrame(session))
			answerCopilotCompaction(t, a, stdin, `"result":{"success":true}`)
			handleCopilotFrames(a, copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:51.000Z", map[string]any{}))
			return providerkittest.TurnEndThen(providerkit.CompactionEnd())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newCopilotCompactionAgent(t)
			session := a.currentNativeSessionID()
			require.NoError(t, a.CompactContext())
			compaction := tc.compact(t, a, stdin, session)
			require.Eventually(t, func() bool {
				return len(providerkittest.TurnEndAndRetryOps(sink.Timeline())) == len(compaction)
			}, 30*time.Second, time.Millisecond)
			handleCopilotFrames(a,
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", copilotQueryServerError),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			)
			want := append(append([]agenttest.TimelineEntry(nil), compaction...), providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient})...)
			assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Equal(t, 1, sink.AutoScheduleCount(), "the next turn's server error schedules its retry")
		})
	}
}

// A reply that arrives after the process stopped, or after the session was
// replaced, applies no verdict. A normal turn applies none on those paths
// either: the worker's runtime cleanup owns the retries of a stopped agent.
func TestCopilotManualCompactionReplyAfterTheSessionEndsAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		end  func(a *Agent)
	}{
		{name: "a stopped process", end: func(a *Agent) { a.SetStoppedForTest(true) }},
		{name: "a replaced session", end: func(a *Agent) { a.forgetNativeSessionState("replacement") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newCopilotCompactionAgent(t)
			require.NoError(t, a.CompactContext())
			tc.end(a)
			match := copilotCompactRequestRe.FindStringSubmatch(stdin.String())
			require.NotNil(t, match)
			require.True(t, a.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":`+match[1]+`,"error":{"code":-32603,"message":"The process exited"}}`))))
			require.Eventually(t, func() bool {
				a.stateMu.Lock()
				defer a.stateMu.Unlock()
				return a.compactingRevision == 0 && !a.active
			}, 30*time.Second, time.Millisecond)
			assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// A compaction request that never reached the runtime starts no compaction
// turn. CompactContext returns the error to the dispatcher and applies no
// verdict.
func TestCopilotManualCompactionThatIsNotDeliveredAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	require.Error(t, a.CompactContext())
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.False(t, a.active)
	assert.Zero(t, a.compactingRevision)
}

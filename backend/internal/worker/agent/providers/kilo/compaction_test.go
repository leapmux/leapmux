package kilo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kiloCompactionCancel is the retry operation of a failed compaction, which
// writes no turn end: one cancel of the server-error retry, with the
// compaction's reason.
var kiloCompactionCancel = providerkittest.TurnEndThen(providerkit.CompactionEnd())[1:]

// startKiloPrompt ends the prompt that newKiloRetryAgent left active and
// starts a prompt of content through SendInput, as the worker's queue does.
func startKiloPrompt(t *testing.T, a *Agent, content string) {
	t.Helper()
	a.SetPromptActiveForTest(false)
	require.NoError(t, a.SendInput(content, nil))
	require.True(t, a.PromptActive())
}

// Kilo runs the family's ACP adapter, which runs the /compact command as the
// daemon's compaction. The compaction's 500 schedules no retry.
func TestKiloFailedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T00:56:47Z")
	a, sink, _, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
	startKiloPrompt(t, a, "/compact")

	a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
	assert.Equal(t, kiloCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.Zero(t, sink.AutoScheduleCount())
}

// A steer that the reader sends during a compaction joins the compaction's
// turn, as it joins any running prompt. Whichever answer comes first, the turn
// ends once with the compaction's verdict, although both requests fail with a
// 500 that the recipe retries.
func TestKiloSteerThatJoinsACompactionSharesItsVerdict(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T00:56:47Z")
	for name, steerFirst := range map[string]bool{"the prompt answers first": false, "the steer answers first": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
			startKiloPrompt(t, a, "/compact")
			require.NoError(t, a.SteerInput("Keep the test plan.", nil))
			steer := lastRequestID(t, stdin)
			if steerFirst {
				answerSteerFirst(a, nil, kiloACPError(t))
				a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
			} else {
				a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
				answerSteerLast(t, a, steer, agenttest.RPCReply{Error: json.RawMessage(kiloACPFailure)})
			}
			assert.False(t, a.PromptActive())
			assert.Equal(t, kiloCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// The prompt after a compaction takes the recipe's verdict again, so its 500
// schedules the server-error retry.
func TestKiloPromptAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T00:56:47Z")
	a, sink, _, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
	startKiloPrompt(t, a, "/compact")
	a.FinishPromptRequestForTest(kiloRetrySession, json.RawMessage(kiloEndTurn), nil)
	compactionOps := len(sink.Timeline())

	require.NoError(t, a.SendInput("Continue the task.", nil))
	a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, kiloRetryTimeline(sink)[compactionOps:])
}

func TestOpenCodeCompactionRow(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, opencode.EventRoute, opencode.QuestionRoot)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
	a.SetSessionIDForTest("session-1")
	*a.HooksForTest() = opencode.FamilyHooks(&a.FamilyBase)
	read := make(chan struct{})
	a.MemberEvents = func(string, json.RawMessage) { close(read) }
	a.BeginQuestionsForTest(ctx, a.Sink(), "agent-1", daemon.Server.URL)
	daemon.Send("data: {\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"summary\",\"role\":\"assistant\",\"summary\":true}}}\n\n")
	const end = `{"type":"session.compacted","properties":{"sessionID":"session-1"}}`
	daemon.Send("data: " + end + "\n\n")
	daemon.Send("data: {\"type\":\"session.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"session-1\"}}}\n\n")
	select {
	case <-read:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the daemon bridge did not read its events")
	}
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, end, string(sink.PersistedNotifications()[0].Content))
	assert.Equal(t, agent.CompactionBoundaryNotification(), Registration().Plugin.Classify(sink.PersistedNotifications()[0].Content))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

// A /compact text that the reader steers into a running prompt joins that
// conversation turn and marks nothing. The turn's 500 still schedules the
// server-error retry once.
func TestKiloSteeredCompactCommandMarksNothing(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T00:56:47Z")
	a, sink, stdin, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
	startKiloPrompt(t, a, "Fix the failing test.")
	require.NoError(t, a.SteerInput("/compact keep the plan", nil))
	steer := lastRequestID(t, stdin)

	a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
	answerSteerLast(t, a, steer, agenttest.RPCReply{Error: json.RawMessage(kiloACPFailure)})
	assert.Equal(t, []string{"leapmux_notification", "leapmux_notification", "schedule_server_error"}, kiloRetryTimeline(sink))
	assert.Equal(t, 1, sink.AutoScheduleCount())
}

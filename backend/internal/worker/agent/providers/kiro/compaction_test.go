package kiro

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pending manual compaction publishes its live trigger without storing a start row.
func TestKiroManualCompactionIsLiveState(t *testing.T) {
	t.Parallel()
	requestReceived := make(chan struct{})
	allowReply := make(chan struct{})
	a, sink, requests := newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == kiroCompactMethod {
			close(requestReceived)
			<-allowReply
			return agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	releaseReply := sync.OnceFunc(func() { close(allowReply) })
	t.Cleanup(releaseReply)
	require.NoError(t, a.CompactContext())
	ctx := testutil.DeadlineContext(t)
	select {
	case <-requestReceived:
	case <-ctx.Done():
		require.FailNow(t, "the peer never received the compaction request")
	}
	sent := requestsFor(requests(), kiroCompactMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, kiroTestSession, sent[0].Params["sessionId"])
	assert.True(t, a.PromptActive(), "the pending reply must retain the compaction turn")
	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "manual"},
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	for _, row := range sink.PersistedNotifications() {
		assert.NotContains(t, string(row.Content), "compacting", "no stored start row")
	}
	assert.Empty(t, sink.Notifications(), "no worker start row")

	releaseReply()
	waitForIdle(t, a)
	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "manual"}, nil,
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.Equal(t, []string{"The conversation had nothing to compact"}, statusTexts(sink))
}

// The automatic summarization is live state, and its native end stores one
// boundary and ends the live key.
func TestKiroAutomaticCompactionIsLiveThenOneEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind string
	}{
		{"completed", "summarization_completed"},
		{"failed", "summarization_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			a.HandleOutput(kiroSessionInfo(t, "summarization_started"))
			a.HandleOutput(kiroSessionInfo(t, tc.kind))
			values := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
			require.Len(t, values, 2)
			assert.Equal(t, map[string]interface{}{contracts.CompactionFieldTrigger: "auto"}, values[0])
			assert.Nil(t, values[1])
			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1, "one native end row")
			assert.Contains(t, string(rows[0].Content), tc.kind)
			assert.Empty(t, sink.Notifications(), "no worker status row")
		})
	}
}

// Each finishCompaction outcome ends the live state and keeps its own row,
// because Kiro answers a manual compaction with a bare result.
func TestKiroCompactionFinishEndsLiveStateAndKeepsItsRow(t *testing.T) {
	t.Parallel()
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method == kiroCompactMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"success":false}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
		require.NoError(t, a.CompactContext())
		waitForIdle(t, a)
		assert.Equal(t, []interface{}{
			map[string]interface{}{contracts.CompactionFieldTrigger: "manual"}, nil,
		}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
		assert.Equal(t, []string{"Kiro did not compact the context"}, statusTexts(sink))
	})
	t.Run("nothing to compact", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method == kiroCompactMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
		require.NoError(t, a.CompactContext())
		waitForIdle(t, a)
		assert.Equal(t, []interface{}{
			map[string]interface{}{contracts.CompactionFieldTrigger: "manual"}, nil,
		}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
		assert.Equal(t, []string{"The conversation had nothing to compact"}, statusTexts(sink))
	})
}

// An interrupt during a manual compaction ends the live state and keeps its
// row.
func TestKiroInterruptDuringACompactionEndsLiveState(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	work := a.BeginClientWork()
	require.NotNil(t, work)
	sessionID := a.CurrentSessionID()
	a.stateMu.Lock()
	a.compaction = compactionState{id: 7, lastID: 7, sessionID: sessionID, work: work}
	a.stateMu.Unlock()
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	values := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
	require.Len(t, values, 1)
	assert.Nil(t, values[0])
	require.NotEmpty(t, sink.Notifications(), "the interrupt keeps its own row")
}

func kiroSessionInfo(t *testing.T, kind string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"kiro": map[string]any{"kind": kind}},
			},
		},
	})
}

// kiroCompactionCancel is the retry operation of a manual compaction's end,
// which writes no turn end: one cancel of the server-error retry, with the
// compaction's reason.
var kiroCompactionCancel = providerkittest.TurnEndThen(providerkit.CompactionEnd())[1:]

// newKiroCompactionAgent builds a Kiro agent whose peer answers the compaction
// request with reply once the test calls release, and every other request
// with {}. Its transcript records the order of every row and retry operation.
func newKiroCompactionAgent(t *testing.T, reply agenttest.RPCReply) (a *Agent, sink *agenttest.Sink, release func()) {
	t.Helper()
	held := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(held) }) }
	t.Cleanup(release)
	a, _, _ = newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method != kiroCompactMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		<-held
		return reply
	})
	sink = &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	return a, sink, release
}

// Every end of a manual compaction ends the turn that it held with one cancel
// of the server-error retry, which states the compaction's reason, and
// schedules nothing: a compaction that ends well, one that Kiro refuses, and
// one whose request fails, even with a temporary model error.
func TestKiroCompactionEndCancelsWithTheCompactionReason(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]agenttest.RPCReply{
		"a compaction that ends well":    {Result: json.RawMessage(`{"success":true}`)},
		"a compaction that Kiro refuses": {Result: json.RawMessage(`{"success":false}`)},
		"a compaction that fails":        {Error: kiroResponseErrorObject(t, kiroInternalServerResponse)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, release := newKiroCompactionAgent(t, reply)
			require.NoError(t, a.CompactContext())
			require.True(t, a.PromptActive(), "the compaction holds a turn")

			release()
			testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
			assert.Equal(t, kiroCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// An interrupt ends the turn that the compaction held, with the compaction's
// reason. Kiro's late answer then changes nothing, so the turn applies one
// verdict.
func TestKiroInterruptedCompactionAppliesOneVerdict(t *testing.T) {
	t.Parallel()
	a, sink, release := newKiroCompactionAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)})
	require.NoError(t, a.CompactContext())

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.False(t, a.PromptActive())
	assert.Equal(t, kiroCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))

	release()
	_, err := a.SendRequest("test/sync", nil, a.APITimeout())
	require.NoError(t, err)
	assert.Equal(t, kiroCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the late answer applies no second verdict")
	assert.Zero(t, sink.AutoScheduleCount())
}

// A compaction request that Kiro never receives ends the turn that it opened
// with the compaction's reason.
func TestKiroCompactionThatFailsToSendCancelsWithTheCompactionReason(t *testing.T) {
	t.Parallel()
	a, sink := newKiroRetryAgent(t)
	a.SetStdinForTest(agenttest.FailingStdin{})

	require.Error(t, a.CompactContext())
	assert.False(t, a.PromptActive())
	assert.Equal(t, kiroCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The prompt after a compaction takes its own verdict, so its temporary model
// error schedules the server-error retry.
func TestKiroPromptAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink, release := newKiroCompactionAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)})
	require.NoError(t, a.CompactContext())
	release()
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
	compactionOps := len(sink.Timeline())

	failKiroPrompt(t, a, kiroProbedDisplayError(t, kiroInternalServerResponse), kiroInternalServerResponse)
	assert.Equal(t, []string{"leapmux_notification", "turn_end", "schedule_server_error"}, kiroTimelineOps(sink.Timeline()[compactionOps:]))
}

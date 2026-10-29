package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeManualCompactionUsesNativeSessionRequest(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	compactor, supported := any(a).(agent.ContextCompactor)
	require.True(t, supported, "ZCode must dispatch manual compaction through session/compact")
	answerZCodeRequest(t, a, stdin, "session/compact", `{"response":"","compact":{"state":"accepted"}}`)
	require.NoError(t, compactor.CompactContext())

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	require.Equal(t, "session/compact", requests[0].Method)
	var params struct {
		SessionID string `json:"sessionId"`
		InputID   string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	require.Equal(t, "sess-1", params.SessionID)
	require.NotEmpty(t, params.InputID)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active)
	require.ErrorIs(t, a.SendInput("later turn", nil), agent.ErrAgentBusy)
	require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)

	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, "session_compacted", `{"status":"idle"}`))
	active, published = sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
	require.Empty(t, sink.Messages(), "the state.updated reason is no row; the lifecycle events own the compaction rows")

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"operationId":"c1","status":"completed","trigger":"manual","phase":"standalone_turn",`+
			`"display":"separator","preCompactTokenCount":180000,"postCompactTokenCount":12000}`))
	require.Len(t, sink.PersistedNotifications(), 1, "the compaction lifecycle is the row")
}

func TestZCodeManualCompactionEndsTheLiveStateAtTheFinalStateReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{contracts.ZCodeStateReasonSessionCompacted, contracts.ZCodeStateReasonSessionCompactFailed, contracts.ZCodeStateReasonSessionCompactCancelled} {
		for _, manual := range []bool{false, true} {
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			if manual {
				a.compactionSessionID = "sess-1"
			}
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
				`{"operationId":"c1","status":"started","trigger":"auto"}`))
			a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, reason, `{"status":"idle"}`))
			assert.Equal(t, []any{map[string]any{"trigger": "auto"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			assert.Empty(t, a.compactionSessionID)
		}
	}
}

func TestZCodeManualCompactionRejectsInvalidReceipt(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{}`)
	require.ErrorIs(t, a.CompactContext(), agent.ErrDeliveryUncertain)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
}

func TestZCodeManualCompactionPreservesNativeFailure(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{"compact":{"state":"accepted"}}`)
	require.NoError(t, a.CompactContext())
	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
	require.Empty(t, sink.Messages(), "the state.updated reason is no row; the lifecycle events own the compaction rows")
}

func TestZCodeManualCompactionRefusalReleasesTheQueue(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	refuseZCodeRequest(t, a, stdin, MethodSessionCompact, -32603, "compaction refused")
	require.ErrorContains(t, a.CompactContext(), "compaction refused")
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
}

func TestZCodeManualCompactionInterruptEndsASilentSummary(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.compactionSessionID = "sess-1"
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"operationId":"c1","status":"started","trigger":"manual"}`))

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)
	timer.fire()
	assert.Equal(t, []any{map[string]any{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Empty(t, a.compactionSessionID)
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeInterrupted+`"}`, string(sink.PersistedNotifications()[0].Content))
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
	// A stopped compaction ends with the compaction's verdict, after the stop row.
	assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestZCodeManualCompactionIgnoresAReplacedSessionReply(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.sessionID = "sess-new"
	a.compactionSessionID = "sess-new"
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Equal(t, "sess-new", a.compactionSessionID)
	require.Empty(t, sink.Messages())
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active)
}

// zcodeCompactionEnd is the timeline of the verdict that a manual compaction
// applies when no turn-end row precedes it.
var zcodeCompactionEnd = []agenttest.TimelineEntry{{
	Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	Reason: providerkit.TurnEndedOnCompaction,
}}

// zcodeCompactionTurnFailed500 is a turn.failed that the classifier reads as a
// transient 5xx failure. Its attribution follows zcodeTurnFailed500.
func zcodeCompactionTurnFailed500(t *testing.T, seq int64) []byte {
	t.Helper()
	return zcodeEventLine(t, seq, contracts.ZCodeEventTurnFailed,
		`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Provider returned a server error.",`+
			`"attribution":{"source":"provider","reason":"server_error","statusCode":500,"retryable":true}},"turnPhase":"processing_input"}`)
}

// startZCodeManualCompaction runs CompactContext against a receipt that
// accepts the compaction, and returns the inputId of the request.
func startZCodeManualCompaction(t *testing.T, a *Agent, stdin *zcodeRecordedStdin) string {
	t.Helper()
	answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{"response":"","compact":{"state":"accepted"}}`)
	require.NoError(t, a.CompactContext())
	var params struct {
		InputID string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionCompact).Params, &params))
	return params.InputID
}

// The final state reason of a manual compaction ends it with one verdict that
// cancels the server-error retry, whatever the outcome. The verdict follows
// the release of the queue, as a turn end's verdict does.
func TestZCodeManualCompactionEndCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{contracts.ZCodeStateReasonSessionCompacted, contracts.ZCodeStateReasonSessionCompactFailed, contracts.ZCodeStateReasonSessionCompactCancelled} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.Sink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			startZCodeManualCompaction(t, a, stdin)
			a.HandleOutput(zcodeStateLine(t, ScopeSession, reason, `{"status":"idle"}`))
			assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			active, published := sink.LastTurnActive()
			require.True(t, published)
			assert.False(t, active)
			// A repeated final reason finds no pending compaction.
			a.HandleOutput(zcodeStateLine(t, ScopeSession, reason, `{"status":"idle"}`))
			assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the compaction applies one verdict")
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// ZCode runs the compaction as a turn of its own. A turn that starts while
// the compaction is pending belongs to it: the classifier reads none of its
// failures, and the compaction applies one verdict in each order of the turn's
// end and the final state reason.
func TestZCodeTurnInsideManualCompactionAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()

	t.Run("the turn ends before the state reason", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		sink := &agenttest.Sink{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
		inputID := startZCodeManualCompaction(t, a, stdin)
		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"/compact","inputId":"`+inputID+`"}`))
		a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
		assert.Equal(t, []string{"turn_end"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()), "the pending compaction keeps the verdict")
		active, _ := sink.LastTurnActive()
		assert.True(t, active, "the compaction still holds the queue")
		a.HandleOutput(zcodeStateLine(t, ScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		assert.Zero(t, sink.AutoScheduleCount())
	})

	t.Run("the state reason arrives while the turn runs", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		sink := &agenttest.Sink{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
		inputID := startZCodeManualCompaction(t, a, stdin)
		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"/compact","inputId":"`+inputID+`"}`))
		a.HandleOutput(zcodeStateLine(t, ScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
		assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the running turn keeps the verdict")
		active, _ := sink.LastTurnActive()
		assert.True(t, active, "the turn still holds the queue")
		a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		assert.Zero(t, sink.AutoScheduleCount())
	})

	t.Run("a stop ends the turn after the state reason", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		sink := &agenttest.Sink{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
		timer := &zcodeCapturedTimer{}
		a.afterFunc = timer.afterFunc
		inputID := startZCodeManualCompaction(t, a, stdin)
		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"/compact","inputId":"`+inputID+`"}`))
		a.HandleOutput(zcodeStateLine(t, ScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))
		answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
		require.NoError(t, a.Interrupt(agent.StopContext{}))
		require.Equal(t, 1, timer.armed)
		timer.fire()
		assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		active, _ := sink.LastTurnActive()
		assert.False(t, active)
	})
}

// A /compact text that reaches SendInput runs as ZCode's native compaction
// command. Its turn is a manual compaction, so its failure applies the
// compaction's verdict.
func TestZCodeTypedCompactCommandEndsWithTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"/compact", "/compact keep the plan"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.Sink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"accepted":true}`)
			require.NoError(t, a.SendInput(input, nil))
			var params struct {
				InputID string `json:"inputId"`
			}
			require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionSend).Params, &params))
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"/compact","inputId":"`+params.InputID+`"}`))
			a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// A message that is not a bare /compact command starts a conversation turn,
// and that turn's failure applies the classifier's verdict.
func TestZCodeInputThatIsNoCompactCommandKeepsItsOwnVerdict(t *testing.T) {
	t.Parallel()
	attachment := &leapmuxv1.Attachment{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("note")}
	for _, tc := range []struct {
		name        string
		input       string
		attachments []*leapmuxv1.Attachment
	}{
		{name: "an attachment", input: "/compact keep the plan", attachments: []*leapmuxv1.Attachment{attachment}},
		{name: "another command", input: "/compaction"},
		{name: "a prompt", input: "Summarize the plan."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.Sink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"accepted":true}`)
			require.NoError(t, a.SendInput(tc.input, tc.attachments))
			var params struct {
				InputID string `json:"inputId"`
			}
			require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionSend).Params, &params))
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"x","inputId":"`+params.InputID+`"}`))
			a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
			assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
		})
	}
}

// A /compact text that the queue steers into a running turn does not make
// that turn a compaction.
func TestZCodeSteeredCompactTextKeepsTheRunningTurnVerdict(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, zcodeAcceptedSteer("guide"))
	require.NoError(t, a.SteerInput("/compact keep the plan", nil))
	a.HandleOutput(zcodeCompactionTurnFailed500(t, 1))
	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
}

// The compaction's mark ends with its turn: the next turn applies the
// classifier's verdict again.
func TestZCodeTurnAfterManualCompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	inputID := startZCodeManualCompaction(t, a, stdin)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"/compact","inputId":"`+inputID+`"}`))
	a.HandleOutput(zcodeStateLine(t, ScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"response":"","resultType":"success"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventTurnStarted, `{"turnNumber":2,"input":"Continue.","inputId":"next"}`))
	a.HandleOutput(zcodeCompactionTurnFailed500(t, 4))
	want := append(providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient})...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A compaction request that ZCode refused, or whose receipt states no
// compaction, starts no compaction turn. CompactContext returns the error to
// the dispatcher and applies no verdict.
func TestZCodeManualCompactionThatDoesNotStartAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer func(t *testing.T, a *Agent, stdin *zcodeRecordedStdin)
	}{
		{name: "a refusal", answer: func(t *testing.T, a *Agent, stdin *zcodeRecordedStdin) {
			refuseZCodeRequest(t, a, stdin, MethodSessionCompact, -32603, "compaction refused")
		}},
		{name: "an invalid receipt", answer: func(t *testing.T, a *Agent, stdin *zcodeRecordedStdin) {
			answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.Sink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			tc.answer(t, a, stdin)
			require.Error(t, a.CompactContext())
			assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

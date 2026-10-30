package zcode

import (
	"encoding/json"
	"fmt"
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
// the turn-end row and precedes the release of the queue.
func TestZCodeManualCompactionEndCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{contracts.ZCodeStateReasonSessionCompacted, contracts.ZCodeStateReasonSessionCompactFailed, contracts.ZCodeStateReasonSessionCompactCancelled} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.Sink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			startZCodeManualCompaction(t, a, stdin)
			a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, reason, `{"status":"idle"}`))
			assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			active, published := sink.LastTurnActive()
			require.True(t, published)
			assert.False(t, active)
			// A repeated final reason finds no pending compaction.
			a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, reason, `{"status":"idle"}`))
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
		a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
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
		a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
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
		a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))
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
			answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":0}`)
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
			answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":0}`)
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
	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))
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

func TestZCodeManualCompactionOrderedRetryAuthority(t *testing.T) {
	for _, order := range []string{"no turn", "turn first", "state first"} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained=%t", order, retained), func(t *testing.T) {
				stdin := &zcodeRecordedStdin{}
				sink := &agenttest.ControlSink{}
				a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
				a.sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(a.sink), a.sink)
				inputID := startZCodeManualCompaction(t, a, stdin)
				if order != "no turn" {
					a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputId":"`+inputID+`"}`))
				}
				if retained {
					sink.PersistErr = assert.AnError
				}
				endState := func() {
					a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
				}
				if order == "state first" {
					endState()
				}
				if order != "no turn" {
					a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
				}
				if order != "state first" {
					endState()
				}
				sink.PersistErr = nil
				require.NoError(t, agent.FlushPendingOutput(a.sink))
				want := []string{"cancel_server_error", "idle"}
				if order != "no turn" {
					want = []string{"turn_end", "cancel_server_error", "idle"}
				}
				assert.Equal(t, want, providerkittest.TurnEndRetryAndIdleOpNames(sink.Operations()))
				assert.Zero(t, sink.AutoScheduleCount())
				active, _ := sink.LastTurnActive()
				assert.False(t, active)
			})
		}
	}
}

func TestZCodePendingCompactionDoesNotClaimAnotherInput(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	startZCodeManualCompaction(t, a, stdin)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputId":"unrelated"}`))
	a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
}

func TestZCodeUncertainTypedInputRetainsCompactionIdentity(t *testing.T) {
	for _, input := range []string{"/compact", "ordinary input"} {
		t.Run(input, func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.Sink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSend, `{}`)
			require.ErrorIs(t, a.SendInput(input, nil), agent.ErrDeliveryUncertain)
			var params struct {
				InputID string `json:"inputId"`
			}
			require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionSend).Params, &params))
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputId":"`+params.InputID+`"}`))
			a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
			want := []string{"turn_end", "schedule_server_error"}
			if input == "/compact" {
				want = []string{"turn_end", "cancel_server_error"}
			}
			assert.Equal(t, want, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
			assert.Empty(t, a.inputAdmissions)
		})
	}
}

func TestZCodeManualCompactionRetainsDelayedStartIdentity(t *testing.T) {
	for _, receipt := range []string{`{"compact":{"state":"accepted"}}`, `{}`} {
		for _, order := range []string{"state before start", "turn before state", "state before end"} {
			t.Run(receipt+order, func(t *testing.T) {
				stdin := &zcodeRecordedStdin{}
				sink := &agenttest.ControlSink{}
				a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
				answerZCodeRequest(t, a, stdin, MethodSessionCompact, receipt)
				err := a.CompactContext()
				if receipt == `{}` {
					require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
				} else {
					require.NoError(t, err)
				}
				var params struct {
					InputID string `json:"inputId"`
				}
				require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionCompact).Params, &params))
				endState := func() {
					a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
				}
				if order == "state before start" {
					endState()
				}
				a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputId":"`+params.InputID+`"}`))
				if order == "state before end" {
					endState()
				}
				a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
				if order == "turn before state" {
					assert.True(t, a.PublishTurnActive().Active)
					assert.Equal(t, []string{"turn_end"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
					endState()
				}
				var cancels int
				for _, entry := range sink.Operations() {
					if entry.Op == "cancel_server_error" {
						cancels++
					}
				}
				assert.Equal(t, 1, cancels)
				assert.Zero(t, sink.AutoScheduleCount())
				assert.False(t, a.PublishTurnActive().Active)
				assert.Empty(t, a.inputAdmissions)
			})
		}
	}
}

func TestZCodeCompactionIgnoresStaleFinalState(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.stateRevision = 10
	startZCodeManualCompaction(t, a, stdin)
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":7,"reason":"session_compacted","patch":{"status":"idle"}}`))
	assert.True(t, a.PublishTurnActive().Active)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestZCodeCompactionFinalStateWaitsForAdmission(t *testing.T) {
	for _, receipt := range []string{"refused", "accepted", "already_running"} {
		t.Run(receipt, func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			done := make(chan error, 1)
			go func() { done <- a.CompactContext() }()
			request := waitZCodeRequest(t, stdin, MethodSessionCompact)
			a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":1,"reason":"session_compacted","patch":{"status":"idle"}}`))
			assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			if receipt == "refused" {
				a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, request), -32603, "refused"))
				require.Error(t, zcodeTestReceive(t, done))
				assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			} else {
				a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"compact":{"state":"`+receipt+`"}}`)))
				require.NoError(t, zcodeTestReceive(t, done))
				assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			}
		})
	}
}

func TestZCodeCompactionReceiptReconcilesEqualAdmissionRevision(t *testing.T) {
	for _, receipt := range []string{"accepted", "already_running"} {
		t.Run(receipt, func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			a.stateRevision = 10
			done := make(chan error, 1)
			go func() { done <- a.CompactContext() }()
			request := waitZCodeRequest(t, stdin, MethodSessionCompact)
			a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":10,"reason":"session_compacted","patch":{"status":"idle"}}`))
			assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"compact":{"state":"`+receipt+`"}}`)))
			require.NoError(t, zcodeTestReceive(t, done))
			if receipt == "accepted" {
				assert.True(t, a.PublishTurnActive().Active)
				assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
				a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":11,"reason":"session_compacted","patch":{"status":"idle"}}`))
			}
			assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.False(t, a.PublishTurnActive().Active)
		})
	}
}

func TestZCodeCompactionRetainedFinalSurvivesNewerSettings(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.stateRevision = 10
	done := make(chan error, 1)
	go func() { done <- a.CompactContext() }()
	request := waitZCodeRequest(t, stdin, MethodSessionCompact)
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":10,"reason":"session_compacted","patch":{"status":"idle"}}`))
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":11,"reason":"settings_updated","patch":{}}`))
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"compact":{"state":"already_running"}}`)))
	require.NoError(t, zcodeTestReceive(t, done))
	assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.False(t, a.PublishTurnActive().Active)
}

type zcodeCompactionAdmissionSink struct {
	*agenttest.ControlSink
	onActive func()
}

func (sink *zcodeCompactionAdmissionSink) SetTurnState(state agent.TurnState, seq uint64) {
	if state.Active && sink.onActive != nil {
		sink.onActive()
	}
	sink.ControlSink.SetTurnState(state, seq)
}

func TestZCodeCompactionAdmissionSerializesActivityPublication(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &zcodeCompactionAdmissionSink{ControlSink: &agenttest.ControlSink{}}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	serialized := false
	sink.onActive = func() {
		if a.dispatchMu.TryLock() {
			a.dispatchMu.Unlock()
		} else {
			serialized = true
		}
	}
	startZCodeManualCompaction(t, a, stdin)
	assert.True(t, serialized, "a completion claim must not interleave with admission publication")
}

func TestZCodeBackgroundStartPreservesForegroundCompactionAdmission(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{}`)
	require.ErrorIs(t, a.SendInput("/compact", nil), agent.ErrDeliveryUncertain)
	var params struct {
		InputID string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionSend).Params, &params))
	admission := a.inputAdmissions[params.InputID]
	require.NotNil(t, admission)
	a.HandleOutput(zcodeTurnEventLine(t, 1, "background", contracts.ZCodeEventTurnStarted, `{"inputId":"`+params.InputID+`","inputSource":"goal-continuation"}`))
	assert.Same(t, admission, a.inputAdmissions[params.InputID])
	assert.Nil(t, admission.owner, "the background turn cannot consume foreground admission")
	a.HandleOutput(zcodeTurnEventLine(t, 2, "background", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "foreground", contracts.ZCodeEventTurnStarted, `{"inputId":"`+params.InputID+`"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "foreground", contracts.ZCodeEventTurnFailed, `{"error":{"code":"model_request_failed","attribution":{"source":"provider","reason":"server_error","retryable":true,"statusCode":500}}}`))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.Zero(t, sink.AutoScheduleCount())
}

func TestZCodeManualCompactionPreservesStartBeforeUncertainReceipt(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	done := make(chan error, 1)
	go func() { done <- a.CompactContext() }()
	request := waitZCodeRequest(t, stdin, MethodSessionCompact)
	var params struct {
		InputID string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(request.Params, &params))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputId":"`+params.InputID+`"}`))
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{}`)))
	require.ErrorIs(t, zcodeTestReceive(t, done), agent.ErrDeliveryUncertain)
	assert.True(t, a.PublishTurnActive().Active)
	a.HandleOutput(zcodeCompactionTurnFailed500(t, 2))
	assert.Equal(t, []string{"turn_end"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
	assert.True(t, a.PublishTurnActive().Active)
	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.False(t, a.PublishTurnActive().Active)
}

func TestZCodeSessionReplacementRetiresUnresolvedCompactionAdmissions(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			method := MethodSessionSend
			if manual {
				method = MethodSessionCompact
			}
			answerZCodeRequest(t, a, stdin, method, `{}`)
			var err error
			if manual {
				err = a.CompactContext()
			} else {
				err = a.SendInput("/compact", nil)
			}
			require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			require.NotEmpty(t, a.inputAdmissions)
			var replacement zcodeStateSnapshot
			replacement.Session.SessionID = "replacement"
			a.adoptCompleteZCodeSessionSnapshot(replacement, true)
			assert.Empty(t, a.inputAdmissions)
			assert.Nil(t, a.compactionRetry)
			assert.Empty(t, a.compactionSessionID)
			assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

func TestZCodeUnresolvedManualCompactionRejectsAReplacement(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(fmt.Sprintf("ordinary=%t", ordinary), func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{}`)
			require.ErrorIs(t, a.CompactContext(), agent.ErrDeliveryUncertain)
			unresolved := a.compactionRetry
			require.NotNil(t, unresolved)
			var params struct {
				InputID string `json:"inputId"`
			}
			require.NoError(t, json.Unmarshal(waitZCodeRequest(t, stdin, MethodSessionCompact).Params, &params))
			seq := int64(1)
			if ordinary {
				a.HandleOutput(zcodeTurnEventLine(t, seq, "ordinary", contracts.ZCodeEventTurnStarted, `{"inputId":"ordinary-input"}`))
				a.HandleOutput(zcodeTurnEventLine(t, seq+1, "ordinary", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
				seq += 2
			}
			requests := 0
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method != MethodSessionCompact {
					return nil
				}
				requests++
				return json.RawMessage(`{"compact":{"state":"accepted"}}`)
			})
			err := a.CompactContext()
			require.ErrorContains(t, err, "previous compaction")
			assert.NotErrorIs(t, err, agent.ErrAgentBusy, "unresolved ownership must not report active work to the queue")
			assert.Zero(t, requests)
			assert.Same(t, unresolved, a.compactionRetry)
			before := providerkittest.TurnEndAndRetryOps(sink.Timeline())
			a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))
			assert.Equal(t, before, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			a.HandleOutput(zcodeTurnEventLine(t, seq, "old-manual", contracts.ZCodeEventTurnStarted, `{"inputId":"`+params.InputID+`"}`))
			a.HandleOutput(zcodeTurnEventLine(t, seq+1, "old-manual", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
			assert.Nil(t, a.compactionRetry)
			require.NoError(t, a.CompactContext())
			assert.Equal(t, 1, requests)
		})
	}
}

func TestZCodeLocallyStoppedCompactionWaitsForNativeFinalState(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	startZCodeManualCompaction(t, a, stdin)
	a.Mu.Lock()
	a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, "", a.compactionGeneration)
	generation := a.stopWindow.generation
	a.Mu.Unlock()
	a.endStoppedZCodeTurn(generation)
	assert.False(t, a.PublishTurnActive().Active)
	assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	require.NotNil(t, a.compactionRetry)
	requests := 0
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method != MethodSessionCompact {
			return nil
		}
		requests++
		return json.RawMessage(`{"compact":{"state":"accepted"}}`)
	})
	require.ErrorContains(t, a.CompactContext(), "previous compaction")
	assert.Zero(t, requests)
	a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompactCancelled, `{"status":"idle"}`))
	assert.Nil(t, a.compactionRetry)
	assert.Equal(t, zcodeCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	require.NoError(t, a.CompactContext())
	assert.Equal(t, 1, requests)
}

func TestZCodeHistoricalCompactionStartPreservesANewerForeground(t *testing.T) {
	for _, localStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("local-stop=%t", localStop), func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			inputID := startZCodeManualCompaction(t, a, stdin)
			if localStop {
				a.Mu.Lock()
				a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, "", a.compactionGeneration)
				generation := a.stopWindow.generation
				a.Mu.Unlock()
				a.endStoppedZCodeTurn(generation)
			} else {
				a.HandleOutput(zcodeStateLine(t, contracts.ZCodeScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))
			}
			a.createProvisionalZCodeTurn("sess-1", "new-input", 8)
			a.Mu.Lock()
			current := a.foregroundZCodeTurnLocked()
			a.Mu.Unlock()
			require.NotNil(t, current)
			before := providerkittest.TurnEndAndRetryOps(sink.Timeline())
			a.HandleOutput(zcodeTurnEventLine(t, 1, "old-manual", contracts.ZCodeEventTurnStarted, `{"inputId":"`+inputID+`"}`))
			a.Mu.Lock()
			assert.Same(t, current, a.foregroundZCodeTurnLocked())
			historical := a.turns["old-manual"]
			historicalSettled := historical != nil && historical.settled
			a.Mu.Unlock()
			require.NotNil(t, historical)
			assert.True(t, historicalSettled)
			a.HandleOutput(zcodeTurnEventLine(t, 2, "old-manual", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
			assert.Equal(t, before, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			a.Mu.Lock()
			assert.Same(t, current, a.foregroundZCodeTurnLocked())
			a.Mu.Unlock()
			assert.True(t, a.PublishTurnActive().Active)
			ends := zcodeNativeTurnEndRows(t, sink.Messages())
			require.Len(t, ends, 1)
			assert.False(t, ends[0].TurnEnd, "the native divider belongs to historical output")
			if localStop {
				a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","revision":9,"reason":"session_compact_cancelled","patch":{"status":"idle"}}`))
				assert.Nil(t, a.compactionRetry)
			}
		})
	}
}

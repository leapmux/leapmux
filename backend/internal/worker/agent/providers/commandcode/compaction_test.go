package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeCompactionEndpoint(t *testing.T, a *Agent, outcome string) *providerkit.HTTPEndpoint {
	t.Helper()
	return compactionEndpoint(t, nativeCompactionEndpointHandler(t, a, outcome, 1))
}

// nativeCompactionEndpointHandler answers /compact as the native host does: a
// start, the outcome when one is given, and doneEvents done events.
func nativeCompactionEndpointHandler(t *testing.T, a *Agent, outcome string, doneEvents int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/compact", r.URL.Path)
		a.HandleOutput([]byte(`{"type":"event","event":{"type":"compaction_start","trigger":"manual"}}`))
		if outcome != "" {
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"event","event":{"type":"compaction_outcome","trigger":"manual","outcome":%q,"tokensBefore":1000,"tokensAfter":1000}}`, outcome)))
		}
		for range doneEvents {
			a.HandleOutput([]byte(`{"type":"event","event":{"type":"compaction_done","trigger":"manual","tokensSaved":0}}`))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"completed":true}`))
	}
}

func compactionEndpoint(t *testing.T, handler http.HandlerFunc) *providerkit.HTTPEndpoint {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return endpoint
}

func TestFailedNativeManualCompactionDoesNotReportSuccess(t *testing.T) {
	a, sink := testAgent(t)
	a.bridge = nativeCompactionEndpoint(t, a, "failed")
	require.Error(t, a.CompactContext())
	active, known := sink.LastTurnActive()
	require.True(t, known)
	assert.False(t, active)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestNativeManualCompactionRequiresItsExactOutcome(t *testing.T) {
	for _, outcome := range []string{"summarized", "failed", "too-small", "", "unknown-native-outcome"} {
		t.Run(outcome, func(t *testing.T) {
			a, _ := testAgent(t)
			a.bridge = nativeCompactionEndpoint(t, a, outcome)
			err := a.CompactContext()
			if outcome == "summarized" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.False(t, a.PublishTurnActive().Active)
			assert.Nil(t, a.manualCompaction)
		})
	}
}

func TestNativeManualCompactionBoundaryAcceptsEveryOutcome(t *testing.T) {
	p := commandcodeProvider{}
	assert.Empty(t, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_done","trigger":"manual","tokensSaved":0}}`)).Kind())
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_outcome","trigger":"manual","outcome":"summarized","tokensBefore":1000,"tokensAfter":1000}}`)).Kind())
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_outcome","trigger":"manual","outcome":"failed"}}`)).Kind())
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_outcome","outcome":"too-small"}}`)).Kind())
	assert.Empty(t, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_done","tokensSaved":101}}`)).Kind())
}

func TestCompactionStartIsLiveStateAndStoresNoRow(t *testing.T) {
	for _, trigger := range []string{"", "manual"} {
		t.Run(trigger, func(t *testing.T) {
			a, sink := testAgent(t)
			feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": trigger})
			want := trigger
			if want == "" {
				want = "auto"
			}
			assert.Equal(t, []interface{}{map[string]interface{}{contracts.CompactionFieldTrigger: want}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestTooSmallCompactionStoresTheOutcomeAndEndsLiveState(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start"})
	end := feedEvent(t, a, map[string]any{"type": "compaction_outcome", "outcome": "too-small"})
	assert.Equal(t, []interface{}{map[string]interface{}{contracts.CompactionFieldTrigger: "auto"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, string(end), string(sink.PersistedNotifications()[0].Content))
}

func TestAutomaticCompactionReleasesTheTurn(t *testing.T) {
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		assert.Equal(t, methodTurnStart, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_2"}`)}
	})
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "compaction_start"})
	feedEvent(t, a, map[string]any{"type": "compaction_outcome", "outcome": "too-small"})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	assert.False(t, a.PublishTurnActive().Active)
	require.NoError(t, a.SendInput("next turn", nil))
}

func TestCompactionDoneStoresNoRow(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
	feedEvent(t, a, map[string]any{"type": "compaction_done", "trigger": "manual", "tokensSaved": 0})
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.LeapMuxNotifications())
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
}

func TestTurnEndDuringACompactionWritesCompactionEndedAborted(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "compaction_start"})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
	assert.Equal(t, contracts.CompactionEndOutcomeAborted, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
	assert.False(t, a.PublishTurnActive().Active)
}

func TestProcessExitDuringACompactionWritesCompactionEndedAborted(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start"})
	a.finishStream()
	a.finishStream()
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.CompactionEndOutcomeAborted, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
}

func TestBridgeWaitTimeoutAfterTheStartWritesCompactionEndedFailed(t *testing.T) {
	a := agentWithPeer(t, func(string, json.RawMessage) agenttest.RPCReply { return agenttest.RPCReply{} })
	sink := &agenttest.Sink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	a.SetAPITimeoutForTest(time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	a.bridge = compactionEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
		clock.Advance(time.Minute).MustWait(ctx)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"completed":true}`))
		assert.NoError(t, err)
	})
	trap := clock.Trap().NewTimer(providerkit.AwaitSignalTimerTag, compactionWaitLabel)
	t.Cleanup(trap.Close)
	result := make(chan error, 1)
	go func() { result <- a.CompactContext() }()
	wait := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, 4*time.Minute, wait, "the HTTP request and boundary share one deadline")
	clock.Advance(wait).MustWait(ctx)
	require.ErrorIs(t, <-result, context.DeadlineExceeded)
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.CompactionEndOutcomeFailed, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
	assert.False(t, a.PublishTurnActive().Active)
	assert.Nil(t, a.manualCompaction)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestBridgeFailureOnlyEndsAPublishedCompaction(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			a, sink := testAgent(t)
			a.bridge = compactionEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
				if started {
					feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, err := w.Write([]byte(`{"error":"bridge failed"}`))
				assert.NoError(t, err)
			})
			require.Error(t, a.CompactContext())
			assert.False(t, a.PublishTurnActive().Active)
			assert.Empty(t, sink.PersistedNotifications())
			if started {
				require.Len(t, sink.LeapMuxNotifications(), 1)
				assert.Equal(t, contracts.CompactionEndOutcomeFailed, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
				assert.NotEmpty(t, sink.LeapMuxNotifications()[0][contracts.NotificationFieldError])
			} else {
				assert.Empty(t, sink.LeapMuxNotifications())
				assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			}
		})
	}
}

func TestCompactionNativeEndDuringStopKeepsItsOutcome(t *testing.T) {
	a, sink := testAgent(t)
	a.SetStdinForTest(&compactionExitStdin{onClose: func() {
		feedEvent(t, a, map[string]any{"type": "compaction_outcome", "trigger": "manual", "outcome": "summarized"})
		feedEvent(t, a, map[string]any{"type": "compaction_done", "trigger": "manual"})
		a.finishStream()
	}})
	a.bridge = compactionEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
		_, err := w.Write([]byte(`{"completed":`))
		assert.NoError(t, err)
	})
	require.Error(t, a.CompactContext())
	assert.True(t, a.IsStopped())
	assert.False(t, a.PublishTurnActive().Active)
	assert.Empty(t, sink.LeapMuxNotifications(), "the native end takes precedence over the failed HTTP reply")
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "compaction_outcome")
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "summarized")
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the native end and the reader end apply one verdict")
}

func TestClassifyNoticeKeepsEachNotice(t *testing.T) {
	assert.False(t, (commandcodeProvider{}).Classify([]byte(`{"type":"event","event":{"type":"notice","message":"Keep this notice"}}`)).Consolidatable())
}

func TestCompactionShutdownLetsTheReaderDetermineTheOutcome(t *testing.T) {
	for _, nativeEnd := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeEnd), func(t *testing.T) {
			a, sink := testAgent(t)
			parent, cancelParent := context.WithCancel(t.Context())
			processDone := make(chan struct{})
			a.JSONRPCProcess = providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID: "compaction-shutdown", Ctx: parent, Cancel: cancelParent, Stdin: &agenttest.Stdin{}, ProcessDone: processDone,
			})}
			t.Cleanup(func() { close(processDone); cancelParent() })
			clock := testutil.NewQuartzMock(t)
			a.clock = clock
			a.bridge = compactionEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
				feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"completed":true}`))
				assert.NoError(t, err)
			})
			trap := clock.Trap().NewTimer(providerkit.AwaitSignalTimerTag, compactionWaitLabel)
			t.Cleanup(trap.Close)
			result := make(chan error, 1)
			go func() { result <- a.CompactContext() }()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			t.Cleanup(cancel)
			testutil.WaitForTimer(t, ctx, trap)
			cancelParent()
			require.ErrorIs(t, <-result, context.Canceled)
			assert.Empty(t, sink.LeapMuxNotifications(), "shutdown does not invent a failure before stdout ends")
			if nativeEnd {
				feedEvent(t, a, map[string]any{"type": "compaction_outcome", "trigger": "manual", "outcome": "summarized"})
				feedEvent(t, a, map[string]any{"type": "compaction_done", "trigger": "manual"})
			}
			a.finishStream()
			if nativeEnd {
				assert.Empty(t, sink.LeapMuxNotifications())
				require.Len(t, sink.PersistedNotifications(), 1)
			} else {
				require.Len(t, sink.LeapMuxNotifications(), 1)
				assert.Equal(t, contracts.CompactionEndOutcomeAborted, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
			}
			assert.False(t, a.PublishTurnActive().Active)
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

func TestNativeCompactionProcessExitPublishesIdle(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
	a.finishStream()
	assert.False(t, a.PublishTurnActive().Active)
	active, known := sink.LastTurnActive()
	require.True(t, known)
	assert.False(t, active)
}

// compactionExitStdin closes the controlled reader when the process stops.
type compactionExitStdin struct {
	agenttest.Stdin
	onClose func()
}

func (s *compactionExitStdin) Close() error {
	s.onClose()
	return nil
}

func TestCompactionReplyFailureStopsOnlyUnresolvedNativeWork(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			a, sink := testAgent(t)
			a.SetStdinForTest(&compactionExitStdin{onClose: a.finishStream})
			a.bridge = compactionEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
				feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
				if completed {
					feedEvent(t, a, map[string]any{"type": "compaction_outcome", "trigger": "manual", "outcome": "summarized"})
					feedEvent(t, a, map[string]any{"type": "compaction_done", "trigger": "manual"})
				}
				_, err := w.Write([]byte(`{"completed":`))
				assert.NoError(t, err)
			})
			require.Error(t, a.CompactContext())
			assert.Equal(t, !completed, a.IsStopped())
			assert.False(t, a.PublishTurnActive().Active)
			if completed {
				assert.Empty(t, sink.LeapMuxNotifications())
			} else {
				require.Len(t, sink.LeapMuxNotifications(), 1)
				assert.Equal(t, contracts.CompactionEndOutcomeFailed, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
				assert.Error(t, a.SendInput("do not overlap the uncertain compaction", nil))
			}
			value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			assert.True(t, exists)
			assert.Nil(t, value)
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

func TestCompactionRequestTimeoutStopsUnresolvedNativeWork(t *testing.T) {
	a, sink := testAgent(t)
	a.SetStdinForTest(&compactionExitStdin{onClose: a.finishStream})
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	started := make(chan struct{})
	a.bridge = compactionEndpoint(t, func(_ http.ResponseWriter, r *http.Request) {
		feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
		close(started)
		<-r.Context().Done()
	})
	trap := clock.Trap().AfterFunc(compactionWaitLabel, "request")
	t.Cleanup(trap.Close)
	result := make(chan error, 1)
	go func() { result <- a.CompactContext() }()
	ctx := testutil.DeadlineContext(t)
	duration := testutil.WaitForTimer(t, ctx, trap)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("The controlled compaction request did not arrive.")
	}
	clock.Advance(duration).MustWait(ctx)
	select {
	case err := <-result:
		require.Error(t, err)
	case <-ctx.Done():
		t.Fatal("The compaction request did not stop at its deadline.")
	}
	assert.True(t, a.IsStopped())
	assert.False(t, a.PublishTurnActive().Active)
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.CompactionEndOutcomeFailed, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// compactionVerdictOps is the retry operation that the end of one manual
// compaction applies: CompactionEnd's cancellation of the server-error retry.
func compactionVerdictOps() []agenttest.TimelineEntry {
	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, providerkit.CompactionEnd(), nil)
	return reference.Timeline()
}

// A manual compaction applies CompactionEnd once when it ends, whatever its
// outcome: a native end, a bridge failure, a missing confirmation, or the end
// of the reader. A replayed native end applies no second verdict.
func TestManualCompactionEndAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	manualStart := map[string]any{"type": "compaction_start", "trigger": "manual"}
	manualOutcome := func(outcome string) map[string]any {
		return map[string]any{"type": "compaction_outcome", "trigger": "manual", "outcome": outcome}
	}
	manualDone := map[string]any{"type": "compaction_done", "trigger": "manual"}
	for _, tc := range []struct {
		name    string
		handler func(*testing.T, *Agent) http.HandlerFunc
		success bool
	}{
		{name: "a summarized compaction", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return nativeCompactionEndpointHandler(t, a, "summarized", 1)
		}, success: true},
		{name: "a failed native compaction", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return nativeCompactionEndpointHandler(t, a, "failed", 1)
		}},
		{name: "a replayed native end", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return nativeCompactionEndpointHandler(t, a, "summarized", 2)
		}, success: true},
		{name: "a bridge failure before the start", handler: func(*testing.T, *Agent) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }
		}},
		{name: "a bridge failure after the start", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				feedEvent(t, a, manualStart)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}},
		{name: "a host that does not confirm the compaction", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				feedEvent(t, a, manualStart)
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"completed":false}`))
				assert.NoError(t, err)
			}
		}},
		{name: "a reader that ends after the native outcome", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				feedEvent(t, a, manualStart)
				feedEvent(t, a, manualOutcome("summarized"))
				a.finishStream()
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"completed":true}`))
				assert.NoError(t, err)
			}
		}},
		{name: "a reader that ends before a late native end", handler: func(t *testing.T, a *Agent) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				feedEvent(t, a, manualStart)
				a.finishStream()
				// The reader already ended the attempt, so a late end changes nothing.
				feedEvent(t, a, manualOutcome("summarized"))
				feedEvent(t, a, manualDone)
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"completed":true}`))
				assert.NoError(t, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			a.bridge = compactionEndpoint(t, tc.handler(t, a))
			err := a.CompactContext()
			if tc.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Nil(t, a.manualCompaction)
			active, known := sink.LastTurnActive()
			require.True(t, known)
			assert.False(t, active, "the end of the compaction publishes idle")
		})
	}
}

// An automatic compaction, outside LeapMux's CompactContext, ends no turn of
// its own, so it applies no verdict.
func TestAutomaticCompactionEndAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start"})
	feedEvent(t, a, map[string]any{"type": "compaction_outcome", "outcome": "summarized"})
	feedEvent(t, a, map[string]any{"type": "compaction_done", "tokensSaved": 101})
	a.finishStream()
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The compaction's verdict belongs to the compaction alone. The next turn
// takes the classifier's verdict again.
func TestTurnAfterManualCompactionTakesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCommandCodeRetryAgent(t)
	a.HandleOutput([]byte(commandcodeSuccessEnd))
	before := len(providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	a.bridge = nativeCompactionEndpoint(t, a, "summarized")
	require.NoError(t, a.CompactContext())
	a.startTurn("turn_2")
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":16,"turnId":"turn_2","stopReason":"run_error","finalText":"","turnCount":1,"error":{"name":"TransportError","message":"POST /alpha/generate → 529 overloaded_error: Overloaded","status":529}}}`))
	want := append(compactionVerdictOps(), providerkittest.TurnEndThen(commandcodeOverloadedFailure)...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline())[before:])
}

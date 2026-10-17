package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexLifecycleTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCodexRetryCannotSnapshotAnUnsettledSteer(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/steer" {
			close(entered)
			<-release
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	steered := make(chan error, 1)
	go func() { steered <- a.SteerInput("Keep the original files.", nil) }()
	<-entered
	_, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.ErrorIs(t, err, agent.ErrAgentBusy, "the retry cannot copy input while an earlier delivery remains unresolved")
	finish()
	require.NoError(t, <-steered)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	require.NoError(t, retry.Start())
	for _, request := range requests() {
		if request.Method == "turn/start" {
			assert.Equal(t, []interface{}{
				map[string]interface{}{"type": "text", "text": "Hello."},
				map[string]interface{}{"type": "text", "text": "\n"},
				map[string]interface{}{"type": "text", "text": "Keep the original files."},
			}, request.Params["input"])
		}
	}
}

func TestCodexUncertainSteerWithdrawsItsRetryOffer(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"message":"an unreadable error without a code"}`)}
	})
	armRetryableTurn(t, a, sink)
	require.ErrorIs(t, a.SteerInput("An instruction that might arrive.", nil), agent.ErrDeliveryUncertain)
	offer, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
	require.True(t, exists)
	assert.Nil(t, offer, "a retry cannot reconstruct an input whose delivery is uncertain")
	_, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	assert.ErrorIs(t, err, agent.ErrTurnRetryUnavailable)
}

func TestCodexRetryUsesPermissionsThatTheUserChangedAfterAcceptance(t *testing.T) {
	t.Parallel()
	for _, beforeReplace := range []bool{true, false} {
		t.Run(fmt.Sprintf("before replacement %t", beforeReplace), func(t *testing.T) {
			t.Parallel()
			var a *Agent
			a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, nil))
			a.UpdateSettings(contracts.CodexBypassOptions())
			armRetryableTurn(t, a, sink)
			retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			require.NoError(t, err)
			if !beforeReplace {
				require.NoError(t, retry.Replace())
			}
			a.UpdateSettings(map[string]string{
				agent.OptionIDPermissionMode:           DefaultPermissionMode,
				contracts.CodexOptionPermissionProfile: contracts.CodexOptionDefaultPermissionProfile,
			})
			if beforeReplace {
				require.NoError(t, retry.Replace())
			}
			require.NoError(t, retry.Start())
			for _, request := range requests() {
				if request.Method == "turn/start" {
					assert.Equal(t, "on-request", request.Params["approvalPolicy"], request.Method)
					assert.Equal(t, contracts.CodexOptionDefaultPermissionProfile, request.Params["permissions"], request.Method)
				}
			}
		})
	}
}

func TestCodexUncertainRetryStartKeepsLaterInputOut(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := codexLifecycleTestContext(t)
	trap := clock.Trap().NewTimer(codexTurnStartAckTimerTag)
	t.Cleanup(trap.Close)
	var a *Agent
	a, sink, requests := newCodexAgentForRPCWithClock(t, clock, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, method == "turn/start"
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	done := make(chan error, 1)
	go func() { done <- retry.Start() }()
	require.Equal(t, turnStartAckTimeout, testutil.WaitForTimer(t, ctx, trap))
	clock.Advance(turnStartAckTimeout).MustWait(ctx)
	require.ErrorIs(t, <-done, agent.ErrDeliveryUncertain)
	active, _ := sink.LastTurnActive()
	require.True(t, active, "delivery uncertainty must not release the queue")
	before := len(requests())
	require.ErrorIs(t, a.SendInput("A later message.", nil), agent.ErrAgentBusy)
	assert.Len(t, requests(), before)
	feedCodexLines(a, retryTurnStarted)
	model, effort := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.5", model)
	assert.Equal(t, "low", effort)
	a.Mu.Lock()
	assert.Equal(t, []map[string]interface{}{{"type": "text", "text": "Hello."}}, a.mainTurn.input)
	a.Mu.Unlock()
	feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-2","status":"completed"}}}`)
	active, _ = sink.LastTurnActive()
	assert.False(t, active)
}

type codexSessionTransitionSink struct {
	*agenttest.Sink
	onState func()
}

func (s *codexSessionTransitionSink) SetTurnState(state agent.TurnState, sequence uint64) {
	s.Sink.SetTurnState(state, sequence)
	if callback := s.onState; callback != nil {
		s.onState = nil
		callback()
	}
}

func TestCodexThreadPublicationUsesTheReplacementSession(t *testing.T) {
	t.Parallel()
	a, base, _ := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		if method == "thread/start" {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-thread"},"model":"gpt-5.4"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &codexSessionTransitionSink{Sink: base}
	a.sink = agent.NewProviderServices(sink)
	a.threadID = "old-thread"
	sink.UpdateSessionID("old-thread")
	sink.onState = func() {
		feedCodexLines(a, `{"method":"warning","params":{"threadId":"fresh-thread","message":"A new session warning"}}`)
	}
	_, err := a.ClearContext()
	require.NoError(t, err)
	notices := sink.PersistedNotifications()
	require.Len(t, notices, 1)
	assert.Equal(t, "fresh-thread", notices[0].AgentSessionID)
}

func TestCodexMainTurnKeepsItsIdentityAcrossRepeatedStarts(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	armRetryableTurn(t, a, sink)
	feedCodexLines(a, `{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"answer","delta":"partial"}}`)
	a.Mu.Lock()
	ended := a.mainTurn.ended
	a.Mu.Unlock()
	feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)
	a.Mu.Lock()
	assert.Equal(t, ended, a.mainTurn.ended)
	assert.True(t, a.mainTurn.agentMessageStarted)
	a.Mu.Unlock()
	feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`)
	select {
	case <-ended:
	default:
		t.Error("a replacement turn must end the previous turn's wait")
	}
}

func TestCodexAnEarlierRetryCannotChangeALaterRetry(t *testing.T) {
	t.Parallel()
	for _, finishFirstBeforeAck := range []bool{true, false} {
		t.Run(fmt.Sprintf("first finishes before second acknowledgment %t", finishFirstBeforeAck), func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			ctx := codexLifecycleTestContext(t)
			trap := clock.Trap().NewTimer(codexTurnStartAckTimerTag)
			t.Cleanup(trap.Close)
			firstAccepted, secondWritten := make(chan struct{}), make(chan struct{})
			var a *Agent
			starts := 0
			a, sink, _ := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
				switch request.Method {
				case "turn/interrupt":
					feedCodexLines(a, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":%q,"status":"interrupted"}}}`, request.Params["threadId"], request.Params["turnId"]))
				case "thread/revert":
					return agenttest.RPCReply{Result: json.RawMessage(retryRevertReply)}
				case "turn/start":
					starts++
					if starts == 1 {
						feedCodexLines(a, retryTurnStarted)
						close(firstAccepted)
					} else {
						close(secondWritten)
					}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}, clock)
			armRetryableTurn(t, a, sink)
			first, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			require.NoError(t, err)
			require.NoError(t, first.Replace())
			firstDone := make(chan error, 1)
			go func() { firstDone <- first.Start() }()
			firstTimer := trap.MustWait(ctx)
			<-firstAccepted
			feedCodexLines(a, `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-2","showBufferingUi":true,"fasterModel":"gpt-5.6"}}`)
			second, err := a.BeginTurnRetry("turn-2", "gpt-5.6")
			require.NoError(t, err)
			require.NoError(t, second.Replace())
			secondDone := make(chan error, 1)
			go func() { secondDone <- second.Start() }()
			secondTimer := trap.MustWait(ctx)
			<-secondWritten
			secondTimer.MustRelease(ctx)
			if finishFirstBeforeAck {
				firstTimer.MustRelease(ctx)
				require.NoError(t, <-firstDone)
			}
			feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-3"}}}`)
			require.NoError(t, <-secondDone)
			if !finishFirstBeforeAck {
				firstTimer.MustRelease(ctx)
				require.NoError(t, <-firstDone)
			}
			feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-3","status":"completed"}}}`)
			active, _ := sink.LastTurnActive()
			assert.False(t, active, "the earlier retry must not leave the later retry's hold active")
			model, _ := a.modelAndEffortForTest()
			assert.Equal(t, "gpt-5.6", model, "the earlier retry must not restore its model")
		})
	}
}

type codexRetryNoticeBlockSink struct {
	*agenttest.Sink
	entered chan struct{}
	release chan struct{}
}

func (s *codexRetryNoticeBlockSink) PersistLeapMuxNotification(data map[string]interface{}) {
	s.Sink.PersistLeapMuxNotification(data)
	if data[contracts.NotificationFieldType] == contracts.NotificationTypeTurnRetried {
		close(s.entered)
		<-s.release
	}
}

func (s *codexRetryNoticeBlockSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
		close(s.entered)
		<-s.release
	}
	return s.Sink.PersistNotification(source, content)
}

func TestCodexRetryNeverSendsInputToAThreadThatAClearRetired(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, base, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		switch method {
		case "thread/start":
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-thread"},"model":"gpt-5.4"}`)}, true
		case "turn/start":
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"the thread is retired"}`)}, true
		default:
			return agenttest.RPCReply{}, false
		}
	}))
	sink := &codexRetryNoticeBlockSink{Sink: base, entered: make(chan struct{}), release: make(chan struct{})}
	finish := sync.OnceFunc(func() { close(sink.release) })
	defer finish()
	a.sink = agent.NewProviderServices(sink)
	armRetryableTurn(t, a, base)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	done := make(chan error, 1)
	go func() { done <- retry.Start() }()
	<-sink.entered
	_, err = a.ClearContext()
	require.NoError(t, err)
	finish()
	require.ErrorIs(t, <-done, errCodexTurnRetryCancelled)
	for _, request := range requests() {
		assert.NotEqual(t, "turn/start", request.Method, "the cancelled retry must dispatch no input")
	}
	assert.Equal(t, "fresh-thread", a.threadID)
	var retryNotices []agenttest.Message
	for _, notice := range sink.PersistedNotifications() {
		if notice.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
			retryNotices = append(retryNotices, notice)
		}
	}
	require.Len(t, retryNotices, 1)
	assert.Equal(t, "main-thread", retryNotices[0].AgentSessionID)
	assert.True(t, retryNotices[0].TranscriptOnly)
}

func TestCodexRetryFailureCannotEnterAReplacementSession(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var a *Agent
	a, sink, _ := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		switch method {
		case "turn/start":
			close(started)
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		case "thread/start":
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-thread"},"model":"gpt-5.4"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	r := retry.(*codexTurnRetry)
	r.noticeMu.Lock()
	unlock := sync.OnceFunc(r.noticeMu.Unlock)
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- retry.Start() }()
	<-started
	a.Mu.Lock()
	pending := a.pendingStart
	a.Mu.Unlock()
	require.NotNil(t, pending)
	replied := make(chan struct{})
	go func() {
		a.handleCodexTurnStartResponse(pending, &providerkit.JSONRPCResponseError{Code: -32602, Message: "The request is invalid."})
		close(replied)
	}()
	<-pending.done
	_, err = a.ClearContext()
	require.NoError(t, err)
	unlock()
	require.Error(t, <-done)
	<-replied
	assert.Empty(t, agenttest.AgentErrorTexts(sink.LeapMuxNotifications()))
	for _, notice := range sink.PersistedNotifications() {
		assert.NotEqual(t, "fresh-thread", notice.AgentSessionID)
	}
}

func TestCodexClearReturnsBeforeRetiredThreadCleanup(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a, _, requests := newCodexAgentForRPCWithClock(t, clock, func(method string) agenttest.RPCReply {
		if method == "thread/start" {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-thread"},"model":"gpt-5.4"}`)}
		}
		if method == "thread/unsubscribe" {
			close(entered)
			<-release
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "old-thread"
	done := make(chan error, 1)
	go func() {
		_, err := a.ClearContext()
		done <- err
	}()
	<-entered
	ctx := codexLifecycleTestContext(t)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("a completed clear must not wait for the old thread's cleanup reply")
	}
	assert.Equal(t, "old-thread", requests()[1].Params["threadId"])
}

type codexRetryUncertaintySink struct {
	*agenttest.Sink
	message  string
	reported chan struct{}
}

func (s *codexRetryUncertaintySink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	stored, err := s.Sink.PersistNotification(source, content)
	if strings.Contains(string(content.Original), s.message) {
		close(s.reported)
	}
	return stored, err
}

func TestCodexLateRetryRefusalReleasesOnlyItsPendingRequest(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := codexLifecycleTestContext(t)
	trap := clock.Trap().NewTimer(codexTurnStartAckTimerTag)
	t.Cleanup(trap.Close)
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	var a *Agent
	a, sink, _ := newCodexAgentForRPCWithClock(t, clock, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/start" {
			close(entered)
			<-release
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"The retry is unavailable."}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	notices := &codexRetryUncertaintySink{Sink: sink, message: "The retry can still start", reported: make(chan struct{})}
	a.sink = agent.NewProviderServices(notices)
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	done := make(chan error, 1)
	go func() { done <- retry.Start() }()
	<-entered
	require.Equal(t, turnStartAckTimeout, testutil.WaitForTimer(t, ctx, trap))
	a.Mu.Lock()
	pending := a.pendingStart
	a.Mu.Unlock()
	clock.Advance(turnStartAckTimeout).MustWait(ctx)
	// Start waits for unsubscribe after its timeout. The notice proves that the wait ended.
	<-notices.reported
	require.NotNil(t, pending)
	finish()
	require.Error(t, <-done)
	select {
	case <-pending.done:
	case <-ctx.Done():
		t.Fatal("the late refusal did not settle the request")
	}
	a.Mu.Lock()
	assert.Nil(t, a.pendingStart)
	assert.Nil(t, a.retry)
	assert.Empty(t, pending.input)
	a.Mu.Unlock()
}

func TestCodexRetryAcknowledgmentSettlesEachSettingIndependently(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		edit   map[string]string
		model  string
		effort string
		tier   string
	}{
		{"model and effort", map[string]string{agent.OptionIDModel: "gpt-5.6", agent.OptionIDEffort: "medium"}, "gpt-5.6", "medium", ""},
		{"model only", map[string]string{agent.OptionIDModel: "gpt-5.6"}, "gpt-5.6", "low", ""},
		{"effort only", map[string]string{agent.OptionIDEffort: "medium"}, "gpt-5.5", "medium", ""},
		{"service tier only", map[string]string{contracts.CodexOptionServiceTier: ServiceTierFast}, "gpt-5.5", "low", ServiceTierFast},
		{"unchanged snapshot", nil, "gpt-5.5", "low", ""},
	} {
		for _, nativeEcho := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/native echo %t", test.name, nativeEcho), func(t *testing.T) {
				t.Parallel()
				entered := make(chan struct{})
				var a *Agent
				starts := 0
				a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
					if method == "turn/start" {
						starts++
						if starts == 1 {
							close(entered)
						} else {
							feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-3"}}}`)
						}
						return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
					}
					return agenttest.RPCReply{}, false
				}))
				armRetryableTurn(t, a, sink)
				retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
				require.NoError(t, err)
				require.NoError(t, retry.Replace())
				done := make(chan error, 1)
				go func() { done <- retry.Start() }()
				<-entered
				a.Mu.Lock()
				options := a.codexAxisValuesLocked()
				a.Mu.Unlock()
				for key, value := range test.edit {
					options[key] = value
				}
				a.UpdateSettings(options)
				if nativeEcho {
					feedCodexLines(a, `{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"model":"gpt-5.5","effort":"low"}}}`)
				}
				feedCodexLines(a, retryTurnStarted)
				require.NoError(t, <-done)
				model, effort := a.modelAndEffortForTest()
				assert.Equal(t, test.model, model)
				assert.Equal(t, test.effort, effort)
				feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-2","status":"completed"}}}`)
				require.NoError(t, a.SendInput("Use the settled settings.", nil))
				sent := requests()
				last := sent[len(sent)-1]
				require.Equal(t, "turn/start", last.Method)
				assert.Equal(t, test.model, last.Params["model"])
				assert.Equal(t, test.effort, last.Params["effort"])
				if test.tier == "" {
					assert.Nil(t, last.Params["serviceTier"])
				} else {
					assert.Equal(t, test.tier, last.Params["serviceTier"])
				}
			})
		}
	}
}

func TestCodexRetryAcknowledgmentFollowsThePermissionUpdateOutcome(t *testing.T) {
	t.Parallel()
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{})
			var a *Agent
			starts := 0
			native := codexThreadSettingsEcho{model: "gpt-5.5", effort: "low", collaborationMode: CollaborationDefault}
			a, sink, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
				switch request.Method {
				case "turn/interrupt":
					feedCodexLines(a, retryTurnCompleted)
				case "thread/revert":
					return agenttest.RPCReply{Result: json.RawMessage(retryRevertReply)}
				case "turn/start":
					starts++
					if starts == 1 {
						close(entered)
					} else {
						feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-3"}}}`)
					}
				case "thread/settings/update":
					if !accepted {
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"The permission update is invalid."}`)}
					}
					native.apply(t, a, request.Params)
					return agenttest.RPCReply{Result: json.RawMessage(`{"status":"applied"}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			armRetryableTurn(t, a, sink)
			retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			require.NoError(t, err)
			require.NoError(t, retry.Replace())
			done := make(chan error, 1)
			go func() { done <- retry.Start() }()
			<-entered
			a.Mu.Lock()
			options := a.codexAxisValuesLocked()
			a.Mu.Unlock()
			options[agent.OptionIDPermissionMode] = contracts.CodexPermissionModeApproveForMe
			a.UpdateSettings(options)
			feedCodexLines(a, retryTurnStarted)
			require.NoError(t, <-done)
			model, effort := a.modelAndEffortForTest()
			expectedModel, expectedEffort := "gpt-5.5", "low"
			if accepted {
				expectedModel, expectedEffort = "gpt-5.4", "high"
			}
			assert.Equal(t, expectedModel, model)
			assert.Equal(t, expectedEffort, effort)
			feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-2","status":"completed"}}}`)
			require.NoError(t, a.SendInput("Use the accepted settings.", nil))
			sent := requests()
			last := sent[len(sent)-1]
			assert.Equal(t, expectedModel, last.Params["model"])
			assert.Equal(t, expectedEffort, last.Params["effort"])
			reviewer := "user"
			if accepted {
				reviewer = "auto_review"
			}
			assert.Equal(t, reviewer, last.Params["approvalsReviewer"])
		})
	}
}

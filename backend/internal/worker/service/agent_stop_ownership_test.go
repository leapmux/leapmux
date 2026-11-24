package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

type heldStopCall struct {
	input []byte
	reply chan error
	stop  agent.StopContext
}

type heldStopAgent struct {
	agenttest.IdleAgent
	calls chan heldStopCall
	ctx   context.Context
}

func (a *heldStopAgent) holdStop(input []byte, stop agent.StopContext) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	call := heldStopCall{input: append([]byte(nil), input...), reply: make(chan error, 1), stop: stop}
	select {
	case a.calls <- call:
	case <-a.ctx.Done():
		return a.ctx.Err()
	}
	select {
	case result := <-call.reply:
		return result
	case <-a.ctx.Done():
		return a.ctx.Err()
	}
}

func (a *heldStopAgent) Interrupt(stop agent.StopContext) error { return a.holdStop(nil, stop) }

func (a *heldStopAgent) SendRawInput(input agent.RawInput) error {
	return a.holdStop(input.Data, input.Stop)
}

type heldChildStopAgent struct{ *heldStopAgent }

func (a heldChildStopAgent) InterruptChild(childKey string, stop agent.StopContext) error {
	return a.holdStop([]byte(childKey), stop)
}

func stopOwnershipFixture(t *testing.T) (*Service, testRPCDispatcher, agent.ProviderServices, *heldStopAgent) {
	t.Helper()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	provider := newHeldStopAgent(t.Context(), 2)
	svc.Agents.PutAgentForTest("agent-1", provider)
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.Output.NoteAgentProcessStarted("agent-1")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	return svc, dispatcher, sink, provider
}

func receiveHeldStop(t *testing.T, provider *heldStopAgent) heldStopCall {
	t.Helper()
	select {
	case call := <-provider.calls:
		return call
	case <-time.After(30 * time.Second):
		t.Fatal("the service did not reach the provider stop")
		return heldStopCall{}
	}
}

func finishHeldStop(t *testing.T, call heldStopCall, done <-chan struct{}, result error) {
	t.Helper()
	call.reply <- result
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the service did not finish the provider stop")
	}
}

func dispatchHeldStop(t *testing.T, dispatcher testRPCDispatcher, raw bool) (*testResponseWriter, <-chan struct{}) {
	t.Helper()
	writer := newTestWriter()
	done := make(chan struct{})
	joinHeldStopAtCleanup(t, done)
	go func() {
		defer close(done)
		if raw {
			dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceSendAgentRawMessageProcedure, &leapmuxv1.SendAgentRawMessageRequest{
				AgentId: "agent-1", Content: `{"type":"control_request","request_id":"stop-1","request":{"subtype":"interrupt"}}`,
			}, writer)
			return
		}
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, &leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"}, writer)
	}()
	return writer, done
}

func publishStopControl(t *testing.T, sink agent.ProviderServices, requestID string) {
	t.Helper()
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID,
		Payload:   []byte(fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"pwd"}}}`, requestID)),
	}))
}

func TestInterruptAgentKeepsTheReplacementTurnControl(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, sameID := range []bool{false, true} {
			t.Run(map[bool]string{false: "rpc", true: "raw"}[raw]+"/"+map[bool]string{false: "different_id", true: "same_id"}[sameID], func(t *testing.T) {
				t.Parallel()
				svc, dispatcher, sink, provider := stopOwnershipFixture(t)
				publishStopControl(t, sink, "old-control")
				writer, done := dispatchHeldStop(t, dispatcher, raw)
				call := receiveHeldStop(t, provider)
				if raw {
					assert.Equal(t, `{"type":"control_request","request_id":"stop-1","request":{"subtype":"interrupt"}}`, string(call.input))
				}
				sink.SetTurnState(agent.TurnState{}, 2)
				sink.SetTurnState(agent.TurnState{Active: true}, 3)
				newID := "replacement-control"
				if sameID {
					newID = "old-control"
				}
				publishStopControl(t, sink, newID)
				finishHeldStop(t, call, done, nil)
				require.Empty(t, writer.rejections())
				requests, err := svc.Output.ListControlRequests(context.Background(), "agent-1")
				require.NoError(t, err)
				require.Len(t, requests, 1, "the old stop must withdraw only the old turn's control")
				assert.Equal(t, newID, requests[0].RequestID)
				assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
					svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
			})
		}
	}
}

func TestInterruptAgentFailureKeepsAnotherPendingStop(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _, provider := stopOwnershipFixture(t)
	firstWriter, firstDone := dispatchHeldStop(t, dispatcher, false)
	first := receiveHeldStop(t, provider)
	secondWriter, secondDone := dispatchHeldStop(t, dispatcher, false)
	second := receiveHeldStop(t, provider)
	finishHeldStop(t, first, firstDone, errors.New("the first stop did not reach the provider"))
	require.NotEmpty(t, firstWriter.rejections())
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State,
		"the second stop still owns its optimistic activity mark")
	finishHeldStop(t, second, secondDone, nil)
	require.Empty(t, secondWriter.rejections())
}

func TestRawStopReportsTheDeliveryFailure(t *testing.T) {
	t.Parallel()
	svc, dispatcher, sink, provider := stopOwnershipFixture(t)
	publishStopControl(t, sink, "waiting-control")
	writer, done := dispatchHeldStop(t, dispatcher, true)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, errors.New("the native stop was refused"))
	require.NotEmpty(t, writer.rejections(), "a rejected raw stop must not return RPC success")
	requests, err := svc.Output.ListControlRequests(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "waiting-control", requests[0].RequestID)
}

func TestControlPublicationRenewsTheClaimForANewTurn(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	publishStopControl(t, sink, "reused-control")
	first := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", "reused-control")
	publishStopControl(t, sink, "reused-control")
	repeated := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", "reused-control")
	assert.Equal(t, first.ClaimToken, repeated.ClaimToken, "one runtime owner keeps an identical pending claim")
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	publishStopControl(t, sink, "reused-control")
	replacement := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", "reused-control")
	assert.NotEqual(t, first.ClaimToken, replacement.ClaimToken, "a replacement turn owns a new request instance")
}

func TestInterruptAgentFailureKeepsAnotherAcceptedStop(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _, provider := stopOwnershipFixture(t)
	_, firstDone := dispatchHeldStop(t, dispatcher, false)
	first := receiveHeldStop(t, provider)
	writer, secondDone := dispatchHeldStop(t, dispatcher, false)
	second := receiveHeldStop(t, provider)
	finishHeldStop(t, second, secondDone, nil)
	require.Empty(t, writer.rejections())
	finishHeldStop(t, first, firstDone, errors.New("the first stop did not reach the provider"))
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	first.stop.ReportIgnored()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	second.stop.ReportIgnored()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
}

func TestIgnoredInterruptSuccessRetainsTheStillLiveControl(t *testing.T) {
	t.Parallel()
	svc, dispatcher, sink, provider := stopOwnershipFixture(t)
	publishStopControl(t, sink, "still-live-control")
	before := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", "still-live-control")
	writer, done := dispatchHeldStop(t, dispatcher, false)
	call := receiveHeldStop(t, provider)
	call.stop.ReportIgnored()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	finishHeldStop(t, call, done, nil)
	require.Empty(t, writer.rejections())
	after := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", "still-live-control")
	assert.Equal(t, before.ClaimToken, after.ClaimToken)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
}

func TestInterruptAgentResultKeepsTheReplacementProcessControl(t *testing.T) {
	for _, result := range []string{"success", "failure", "ignored"} {
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, sink, provider := stopOwnershipFixture(t)
			publishStopControl(t, sink, "old-control")
			writer, done := dispatchHeldStop(t, dispatcher, false)
			call := receiveHeldStop(t, provider)
			svc.Output.ClearPendingControlRequests("agent-1")
			replacement := newHeldStopAgent(t.Context(), 1)
			svc.Agents.PutAgentForTest("agent-1", replacement)
			newSink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			svc.Output.NoteAgentProcessStarted("agent-1")
			newSink.SetTurnState(agent.TurnState{Active: true}, 1)
			publishStopControl(t, newSink, "replacement-control")
			if result == "ignored" {
				call.stop.ReportIgnored()
			}
			var stopErr error
			if result == "failure" {
				stopErr = errors.New("the old process refused its stop")
			}
			finishHeldStop(t, call, done, stopErr)
			if stopErr == nil {
				assert.Empty(t, writer.rejections())
			} else {
				assert.NotEmpty(t, writer.rejections())
			}
			request := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", "replacement-control")
			assert.NotEmpty(t, request.ClaimToken)
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
		})
	}
}

func TestInterruptChildKeepsTheReplacementTurnControl(t *testing.T) {
	t.Parallel()
	svc, dispatcher, childID, rootID := setupChildAgentTest(t)
	provider := newHeldStopAgent(t.Context(), 1)
	svc.Agents.PutAgentForTest(rootID, heldChildStopAgent{provider})
	rootSink := svc.Output.sinkForAgent(rootID)
	require.NotNil(t, rootSink)
	svc.Output.NoteAgentProcessStarted(rootID)
	childSink := rootSink.ChildSink(childID)
	childSink.SetTurnState(agent.TurnState{Active: true}, 1)
	publishStopControl(t, childSink, "old-child-control")
	writer := newTestWriter()
	done := make(chan struct{})
	joinHeldStopAtCleanup(t, done)
	go func() {
		defer close(done)
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, &leapmuxv1.InterruptAgentRequest{AgentId: childID}, writer)
	}()
	call := receiveHeldStop(t, provider)
	assert.Equal(t, "row-key-1", string(call.input))
	childSink.SetTurnState(agent.TurnState{}, 2)
	childSink.SetTurnState(agent.TurnState{Active: true}, 3)
	publishStopControl(t, childSink, "new-child-control")
	finishHeldStop(t, call, done, nil)
	require.Empty(t, writer.rejections())
	requests, err := svc.Output.ListControlRequests(t.Context(), childID)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "new-child-control", requests[0].RequestID)
}

func TestEscalationDoesNotShareAnOldTurnResultWithANewTurn(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	seedOpenAgent(t, svc, "agent-1", true)
	row := requireAgentRow(t, svc, "agent-1")
	svc.Agents.PutAgentForTest("agent-1", newHeldStopAgent(t.Context(), 1))
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.Output.NoteAgentProcessStarted("agent-1")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	target, err := svc.Agents.CaptureStopTarget("agent-1")
	require.NoError(t, err)
	var launches atomic.Int32
	launchFailure := errors.New("the replacement launch failed")
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		launches.Add(1)
		return nil, launchFailure
	}
	unlock := svc.Agents.LockAgent("agent-1")
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	old := svc.Output.NoteAgentStopRequested("agent-1", "agent-1")
	oldDone := make(chan error, 1)
	go func() { oldDone <- svc.forceStopAgentTurn(row, target, old) }()
	require.Eventually(t, func() bool { return svc.Agents.LifecycleLockCallersForTest("agent-1") == 2 }, 30*time.Second, 5*time.Millisecond)
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	current := svc.Output.NoteAgentStopRequested("agent-1", "agent-1")
	currentDone := make(chan error, 1)
	go func() { currentDone <- svc.forceStopAgentTurn(row, target, current) }()
	require.Eventually(t, func() bool { return svc.Agents.LifecycleLockCallersForTest("agent-1") == 3 }, 30*time.Second, 5*time.Millisecond,
		"the replacement turn must own a separate escalation result")
	waiting, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.False(t, waiting.Paused, "waiting escalations cannot claim a restart guard before turn revalidation")
	unlock()
	released = true
	require.NoError(t, <-oldDone, "an obsolete escalation changes no replacement turn")
	require.ErrorIs(t, <-currentDone, launchFailure)
	assert.Equal(t, int32(1), launches.Load())
}

func TestInterruptAgentPreservesProviderFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		failure error
		code    codes.Code
		message string
	}{
		{name: "provider failure", failure: errors.New("the native operation refused interruption"), code: codes.Internal, message: "the native operation refused interruption"},
		{name: "native restriction", failure: &agent.InterruptUnavailableError{Message: "the native operation cannot be interrupted"}, code: codes.FailedPrecondition, message: "the native operation cannot be interrupted"},
		{name: "deadline", failure: context.DeadlineExceeded, code: codes.DeadlineExceeded, message: "deadline exceeded"},
		{name: "cancelled", failure: context.Canceled, code: codes.Canceled, message: "context canceled"},
		{name: "missing agent", failure: agent.ErrAgentNotFound, code: codes.NotFound, message: "agent not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _, provider := stopOwnershipFixture(t)
			writer, done := dispatchHeldStop(t, dispatcher, false)
			call := receiveHeldStop(t, provider)
			finishHeldStop(t, call, done, fmt.Errorf("provider stop: %w", tc.failure))
			failures := writer.rejections()
			require.Len(t, failures, 1)
			assert.Equal(t, int32(tc.code), failures[0].code)
			assert.Contains(t, failures[0].message, tc.message)
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
		})
	}
}

func newHeldStopAgent(ctx context.Context, capacity int) *heldStopAgent {
	return &heldStopAgent{ctx: ctx, calls: make(chan heldStopCall, capacity)}
}

func TestHeldStopCancellationReleasesBlockedCalls(t *testing.T) {
	for _, phase := range []string{"publish", "reply"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				provider := newHeldStopAgent(ctx, 2)
				if phase == "publish" {
					for range cap(provider.calls) {
						provider.calls <- heldStopCall{reply: make(chan error, 1)}
					}
				}
				result := make(chan error, 1)
				finished := make(chan struct{})
				var held heldStopCall
				defer func() {
					cancel()
					if held.reply != nil {
						select {
						case held.reply <- context.Canceled:
						default:
						}
					}
					for {
						select {
						case <-finished:
							return
						case call := <-provider.calls:
							if call.reply != nil {
								select {
								case call.reply <- context.Canceled:
								default:
								}
							}
						}
					}
				}()
				go func() { defer close(finished); result <- provider.Interrupt(agent.StopContext{}) }()
				if phase == "reply" {
					held = receiveHeldStop(t, provider)
				}
				synctest.Wait()
				cancel()
				synctest.Wait()
				assert.ErrorIs(t, provider.ctx.Err(), context.Canceled)
				select {
				case err := <-result:
					assert.ErrorIs(t, err, context.Canceled)
				default:
					t.Error("cancellation left a fake stop blocked")
				}
			})
		})
	}
}

// Testing cancels each provider's context before running these cleanup joins.
func joinHeldStopAtCleanup(t *testing.T, done <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the held stop request did not finish during cleanup")
		}
	})
}

func TestIdleInterruptClearsDanglingRootSpan(t *testing.T) {
	t.Parallel()
	svc, dispatcher, sink, provider := stopOwnershipFixture(t)
	sink.SetTurnState(agent.TurnState{Active: false}, 2)
	sink.OpenSpan("dangling-1", "")
	sink.ReportProgress(agent.OutputTailProgress("dangling-1", "running", false))
	require.True(t, svc.Output.hasOpenRootSpans("agent-1"))
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)

	writer, done := dispatchHeldStop(t, dispatcher, false)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, nil)
	svc.Output.WaitActivityRefreshes()

	require.Empty(t, writer.rejections())
	assert.False(t, svc.Output.hasOpenRootSpans("agent-1"))
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "agent-1").currentSpanTracker().ActiveSpans())
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "agent-1").progress.tailSnapshots())
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
}

func TestIdleInterruptRotatesScopeSoLaterPromptAndSpanWork(t *testing.T) {
	t.Parallel()
	svc, dispatcher, sink, provider := stopOwnershipFixture(t)
	sink.SetTurnState(agent.TurnState{Active: false}, 2)
	sink.OpenSpan("dangling-1", "")

	writer, done := dispatchHeldStop(t, dispatcher, false)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, nil)
	svc.Output.WaitActivityRefreshes()
	require.Empty(t, writer.rejections())

	// After idle interrupt rotated st.scope, a new root span marks WORKING and a
	// blocking control request is published instead of suppressed by controlsStopped.
	sink.OpenSpan("later-span", "")
	svc.Output.WaitActivityRefreshes()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)

	publishStopControl(t, sink, "later-control")
	requests, err := svc.Output.ListControlRequests(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "later-control", requests[0].RequestID)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
}

func TestIdleInterruptPreservesSpansOnPreserveOpenSpansOrIgnoredStop(t *testing.T) {
	t.Parallel()

	t.Run("preserve_open_spans", func(t *testing.T) {
		t.Parallel()
		svc, dispatcher, sink, provider := stopOwnershipFixture(t)
		sink.SetTurnState(agent.TurnState{Active: false}, 2)
		sink.OpenSpan("spared-span", "")

		writer, done := dispatchHeldStop(t, dispatcher, false)
		call := receiveHeldStop(t, provider)
		call.stop.PreserveOpenSpans()
		finishHeldStop(t, call, done, nil)
		svc.Output.WaitActivityRefreshes()

		require.Empty(t, writer.rejections())
		assert.True(t, svc.Output.hasOpenRootSpans("agent-1"), "PreserveOpenSpans must spare open spans")
		assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State,
			"rotating st.scope must restore WORKING for spared spans")
		publishStopControl(t, sink, "after-preserved-stop")
		requests, err := svc.Output.ListControlRequests(t.Context(), "agent-1")
		require.NoError(t, err)
		require.Len(t, requests, 1, "rotated scope must allow later blocking prompts")
	})

	t.Run("ignored_stop", func(t *testing.T) {
		t.Parallel()
		svc, dispatcher, sink, provider := stopOwnershipFixture(t)
		sink.SetTurnState(agent.TurnState{Active: false}, 2)
		sink.OpenSpan("ignored-span", "")

		writer, done := dispatchHeldStop(t, dispatcher, false)
		call := receiveHeldStop(t, provider)
		call.stop.ReportIgnored()
		finishHeldStop(t, call, done, nil)
		svc.Output.WaitActivityRefreshes()

		require.Empty(t, writer.rejections())
		assert.True(t, svc.Output.hasOpenRootSpans("agent-1"), "ignored stop must leave open spans untouched")
		assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	})
}

func TestInterruptAgentSkipsIdleSpanResetWhileTurnActiveOrOnInterruptError(t *testing.T) {
	t.Parallel()

	t.Run("turn_active", func(t *testing.T) {
		t.Parallel()
		svc, dispatcher, sink, provider := stopOwnershipFixture(t)
		sink.OpenSpan("active-turn-span", "")

		writer, done := dispatchHeldStop(t, dispatcher, false)
		call := receiveHeldStop(t, provider)
		finishHeldStop(t, call, done, nil)
		svc.Output.WaitActivityRefreshes()

		require.Empty(t, writer.rejections())
		assert.True(t, svc.Output.hasOpenRootSpans("agent-1"),
			"SettleIdleRootStop must not wipe spans while turnActive is true")
	})

	t.Run("interrupt_error", func(t *testing.T) {
		t.Parallel()
		svc, dispatcher, sink, provider := stopOwnershipFixture(t)
		sink.SetTurnState(agent.TurnState{Active: false}, 2)
		sink.OpenSpan("error-span", "")

		writer, done := dispatchHeldStop(t, dispatcher, false)
		call := receiveHeldStop(t, provider)
		finishHeldStop(t, call, done, errors.New("interrupt refused"))
		svc.Output.WaitActivityRefreshes()

		require.NotEmpty(t, writer.rejections())
		assert.True(t, svc.Output.hasOpenRootSpans("agent-1"),
			"failed Interrupt must not reset spans")
		assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	})
}

func TestSettleIdleRootStopResetsSpansBeforeRotatingScope(t *testing.T) {
	t.Parallel()

	ctx := testutil.DeadlineContext(t)
	svc, _, sink, _ := stopOwnershipFixture(t)
	sink.SetTurnState(agent.TurnState{Active: false}, 2)
	sink.OpenSpan("dangling-race", "")
	svc.Output.WaitActivityRefreshes()

	target, err := svc.Agents.CaptureStopTarget("agent-1")
	require.NoError(t, err)
	stop := svc.Output.NoteAgentStopRequested("agent-1", "agent-1")
	stop.Delivered(target)
	svc.Output.WaitActivityRefreshes()
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)

	st := svc.Output.activityFor("agent-1", "agent-1")

	entered := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	stop.beforeIdleReset = func() {
		close(entered)
		<-release
	}

	settled := make(chan struct{})
	go func() {
		defer close(settled)
		stop.SettleIdleRootStop()
	}()

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("SettleIdleRootStop did not reach the reset boundary")
	}

	// SettleIdleRootStop must still hold st.mu across span reset and scope rotation.
	lockFree := st.mu.TryLock()
	if lockFree {
		st.mu.Unlock()
	}

	snapshotEntered := make(chan struct{})
	snapshotEnteredOnce := sync.OnceFunc(func() { close(snapshotEntered) })
	svc.Output.processRunning = func(string) bool {
		snapshotEnteredOnce()
		return true
	}

	snapCh := make(chan AgentActivity, 1)
	go func() {
		snapCh <- svc.Output.AgentActivitySnapshot("agent-1", "agent-1")
	}()

	select {
	case <-snapshotEntered:
	case <-ctx.Done():
		t.Fatal("AgentActivitySnapshot did not start before release")
	}

	releaseOnce()

	var snapshot AgentActivity
	select {
	case snapshot = <-snapCh:
	case <-ctx.Done():
		t.Fatal("AgentActivitySnapshot did not finish after release")
	}
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("SettleIdleRootStop did not finish after release")
	}
	svc.Output.WaitActivityRefreshes()

	assert.False(t, lockFree, "SettleIdleRootStop must hold activity mutex across span reset and scope rotation")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, snapshot.State,
		"concurrent AgentActivitySnapshot must never observe WORKING between scope rotation and span reset")
	assert.False(t, svc.Output.hasOpenRootSpans("agent-1"))
}

func TestIdleInterruptClearsStaleTailAndPermissionWithoutActiveSpans(t *testing.T) {
	t.Parallel()
	svc, dispatcher, sink, provider := stopOwnershipFixture(t)
	sink.SetTurnState(agent.TurnState{Active: false}, 2)
	sink.ReportProgress(agent.OutputTailProgress("nospan-tool", "stale tail output", false))
	sink.BroadcastSessionInfo(permissionCheckInfo("sess-1", "nospan-tool", true))

	rootSink := requireRootOutputSink(t, svc.Output, "agent-1")
	require.False(t, svc.Output.hasOpenRootSpans("agent-1"))
	require.NotEmpty(t, rootSink.progress.tailSnapshots())
	require.NotEmpty(t, rootSink.permissionCheckSnapshots())

	writer, done := dispatchHeldStop(t, dispatcher, false)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, nil)
	svc.Output.WaitActivityRefreshes()

	require.Empty(t, writer.rejections())
	assert.Empty(t, rootSink.progress.tailSnapshots(), "idle interrupt must clear stale tails even with no active spans")
	assert.Empty(t, rootSink.permissionCheckSnapshots(), "idle interrupt must clear stale permission checks even with no active spans")
}

func TestSettleIdleRootStopSkipsReplacementSink(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	sink.SetTurnState(agent.TurnState{Active: false}, 2)
	sink.OpenSpan("old-dangling", "")
	svc.Output.WaitActivityRefreshes()

	target, err := svc.Agents.CaptureStopTarget("agent-1")
	require.NoError(t, err)
	stop := svc.Output.NoteAgentStopRequested("agent-1", "agent-1")
	stop.Delivered(target)

	// A replacement process swaps rootSinks before NoteAgentProcessStarted adopts its turn publisher.
	replacement := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	replacement.OpenSpan("replacement-span", "")
	replacement.ReportProgress(agent.OutputTailProgress("replacement-span", "replacement output", false))
	replacement.BroadcastSessionInfo(permissionCheckInfo("sess-2", "replacement-span", true))

	stop.SettleIdleRootStop()
	svc.Output.WaitActivityRefreshes()

	replacementSink := requireRootOutputSink(t, svc.Output, "agent-1")
	assert.True(t, replacementSink.currentSpanTracker().HasSpan("replacement-span"),
		"old stop settlement must not wipe a replacement sink's open spans")
	assert.NotEmpty(t, replacementSink.progress.tailSnapshots(),
		"old stop settlement must not wipe a replacement sink's progress tails")
	assert.NotEmpty(t, replacementSink.permissionCheckSnapshots(),
		"old stop settlement must not wipe a replacement sink's permission checks")
}

func TestIdleRawInterruptClearsDanglingRootSpanAndRotatesScope(t *testing.T) {
	t.Parallel()
	svc, dispatcher, sink, provider := stopOwnershipFixture(t)
	sink.SetTurnState(agent.TurnState{Active: false}, 2)
	sink.OpenSpan("raw-dangling", "")
	sink.ReportProgress(agent.OutputTailProgress("raw-dangling", "running", false))
	svc.Output.WaitActivityRefreshes()
	require.True(t, svc.Output.hasOpenRootSpans("agent-1"))

	writer, done := dispatchHeldStop(t, dispatcher, true)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, nil)
	svc.Output.WaitActivityRefreshes()

	require.Empty(t, writer.rejections())
	assert.False(t, svc.Output.hasOpenRootSpans("agent-1"), "idle raw interrupt must reset open root spans")
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "agent-1").progress.tailSnapshots())
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)

	// Rotated st.scope allows later spans and blocking control prompts.
	sink.OpenSpan("after-raw-interrupt", "")
	svc.Output.WaitActivityRefreshes()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	publishStopControl(t, sink, "after-raw-control")
	requests, err := svc.Output.ListControlRequests(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "after-raw-control", requests[0].RequestID)
}

func dispatchHeldChildStop(t *testing.T, dispatcher testRPCDispatcher, childID string) (*testResponseWriter, <-chan struct{}) {
	t.Helper()
	writer := newTestWriter()
	done := make(chan struct{})
	joinHeldStopAtCleanup(t, done)
	go func() {
		defer close(done)
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, &leapmuxv1.InterruptAgentRequest{AgentId: childID}, writer)
	}()
	return writer, done
}

func TestIdleMultiTurnChildInterruptRotatesScopeAndHandlesSpans(t *testing.T) {
	t.Parallel()

	t.Run("resets_spans_and_progress_and_admits_later_control", func(t *testing.T) {
		t.Parallel()
		svc, dispatcher, childID, rootID := setupChildAgentTest(t)
		provider := newHeldStopAgent(t.Context(), 1)
		svc.Agents.PutAgentForTest(rootID, heldChildStopAgent{provider})
		rootSink := svc.Output.sinkForAgent(rootID)
		require.NotNil(t, rootSink)
		svc.Output.NoteAgentProcessStarted(rootID)

		childSink := rootSink.ChildSink(childID)
		childSink.SetTurnState(agent.TurnState{Active: true}, 1)
		childSink.SetTurnState(agent.TurnState{Active: false}, 2)
		childSink.OpenSpan("child-dangling", "")
		childSink.ReportProgress(agent.OutputTailProgress("child-dangling", "running", false))
		childSink.BroadcastSessionInfo(permissionCheckInfo("child-sess", "child-dangling", true))

		childPhysical := svc.Output.sinkForAgent(childID)
		require.NotNil(t, childPhysical)
		require.True(t, childPhysical.currentSpanTracker().HasSpan("child-dangling"))
		require.NotEmpty(t, childPhysical.progress.tailSnapshots())
		require.NotEmpty(t, childPhysical.permissionCheckSnapshots())

		writer, done := dispatchHeldChildStop(t, dispatcher, childID)
		call := receiveHeldStop(t, provider)
		finishHeldStop(t, call, done, nil)
		require.Empty(t, writer.rejections())

		assert.Empty(t, childPhysical.currentSpanTracker().ActiveSpans(),
			"idle multi-turn child interrupt must reset open child spans")
		assert.Empty(t, childPhysical.progress.tailSnapshots(),
			"idle multi-turn child interrupt must reset child progress tails")
		assert.Empty(t, childPhysical.permissionCheckSnapshots(),
			"idle multi-turn child interrupt must clear child permission checks")

		publishStopControl(t, childSink, "later-child-control")
		requests, err := svc.Output.ListControlRequests(t.Context(), childID)
		require.NoError(t, err)
		require.Len(t, requests, 1, "rotated child scope must admit later idle blocking control prompts")
		assert.Equal(t, "later-child-control", requests[0].RequestID)
	})

	t.Run("preserve_open_spans_keeps_child_spans_and_rotates_scope", func(t *testing.T) {
		t.Parallel()
		svc, dispatcher, childID, rootID := setupChildAgentTest(t)
		provider := newHeldStopAgent(t.Context(), 1)
		svc.Agents.PutAgentForTest(rootID, heldChildStopAgent{provider})
		rootSink := svc.Output.sinkForAgent(rootID)
		require.NotNil(t, rootSink)
		svc.Output.NoteAgentProcessStarted(rootID)

		childSink := rootSink.ChildSink(childID)
		childSink.SetTurnState(agent.TurnState{Active: true}, 1)
		childSink.SetTurnState(agent.TurnState{Active: false}, 2)
		childSink.OpenSpan("child-spared", "")
		childSink.ReportProgress(agent.OutputTailProgress("child-spared", "still running", false))

		childPhysical := svc.Output.sinkForAgent(childID)
		require.NotNil(t, childPhysical)

		writer, done := dispatchHeldChildStop(t, dispatcher, childID)
		call := receiveHeldStop(t, provider)
		call.stop.PreserveOpenSpans()
		finishHeldStop(t, call, done, nil)
		require.Empty(t, writer.rejections())

		assert.True(t, childPhysical.currentSpanTracker().HasSpan("child-spared"),
			"PreserveOpenSpans must spare open child spans")
		assert.NotEmpty(t, childPhysical.progress.tailSnapshots(),
			"PreserveOpenSpans must spare child progress tails")

		publishStopControl(t, childSink, "after-preserved-child-stop")
		requests, err := svc.Output.ListControlRequests(t.Context(), childID)
		require.NoError(t, err)
		require.Len(t, requests, 1, "rotated child scope must admit later idle blocking control prompts")
		assert.Equal(t, "after-preserved-child-stop", requests[0].RequestID)
	})

	t.Run("skips_replacement_child_sink", func(t *testing.T) {
		t.Parallel()
		svc, _, childID, rootID := setupChildAgentTest(t)
		provider := newHeldStopAgent(t.Context(), 1)
		svc.Agents.PutAgentForTest(rootID, heldChildStopAgent{provider})
		rootSink := svc.Output.sinkForAgent(rootID)
		require.NotNil(t, rootSink)
		svc.Output.NoteAgentProcessStarted(rootID)

		childSink := rootSink.ChildSink(childID)
		childSink.SetTurnState(agent.TurnState{Active: true}, 1)
		childSink.SetTurnState(agent.TurnState{Active: false}, 2)
		childSink.OpenSpan("old-child-dangling", "")

		target, err := svc.Agents.CaptureStopTarget(rootID)
		require.NoError(t, err)
		stop := svc.Output.NoteAgentStopRequested(childID, rootID)
		stop.Delivered(target)

		replacementRoot := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		replacementChild := replacementRoot.ChildSink(childID)
		replacementChild.OpenSpan("replacement-child-span", "")
		replacementChild.ReportProgress(agent.OutputTailProgress("replacement-child-span", "replacement output", false))
		replacementChild.BroadcastSessionInfo(permissionCheckInfo("child-sess-2", "replacement-child-span", true))

		stop.SettleIdleStop()

		replacementPhysical := svc.Output.sinkForAgent(childID)
		require.NotNil(t, replacementPhysical)
		assert.True(t, replacementPhysical.currentSpanTracker().HasSpan("replacement-child-span"),
			"old child stop settlement must not wipe a replacement child sink's open spans")
		assert.NotEmpty(t, replacementPhysical.progress.tailSnapshots(),
			"old child stop settlement must not wipe a replacement child sink's progress tails")
		assert.NotEmpty(t, replacementPhysical.permissionCheckSnapshots(),
			"old child stop settlement must not wipe a replacement child sink's permission checks")
	})
}

func TestRegistryOnlyChildInterruptKeepsControlsStoppedAndSpans(t *testing.T) {
	t.Parallel()
	svc, dispatcher, childID, rootID := setupChildAgentTest(t)
	provider := newHeldStopAgent(t.Context(), 1)
	svc.Agents.PutAgentForTest(rootID, heldChildStopAgent{provider})
	rootSink := svc.Output.sinkForAgent(rootID)
	require.NotNil(t, rootSink)
	svc.Output.NoteAgentProcessStarted(rootID)

	// Registry-only child: never reports SetTurnState, so st.turnSeq remains 0.
	childSink := rootSink.ChildSink(childID)
	childSink.OpenSpan("registry-child-span", "")
	childSink.ReportProgress(agent.OutputTailProgress("registry-child-span", "running", false))

	childPhysical := svc.Output.sinkForAgent(childID)
	require.NotNil(t, childPhysical)

	writer, done := dispatchHeldChildStop(t, dispatcher, childID)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, nil)
	require.Empty(t, writer.rejections())

	assert.True(t, childPhysical.currentSpanTracker().HasSpan("registry-child-span"),
		"registry-only child interrupt must not reset open child spans")
	assert.NotEmpty(t, childPhysical.progress.tailSnapshots(),
		"registry-only child interrupt must not reset child progress")

	st := svc.Output.activityFor(childID, rootID)
	st.mu.Lock()
	stopped := st.scope != nil && st.scope.controlsStopped
	turnSeq := st.turnSeq
	st.mu.Unlock()
	assert.Zero(t, turnSeq)
	assert.True(t, stopped, "registry-only child must keep controlsStopped true after InterruptChild")

	publishStopControl(t, childSink, "suppressed-registry-child-control")
	requests, err := svc.Output.ListControlRequests(t.Context(), childID)
	require.NoError(t, err)
	assert.Empty(t, requests, "registry-only child must keep suppressing blocking controls on the stopped scope")
}

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type heldStopCall struct {
	input []byte
	reply chan error
	stop  agent.StopContext
}

type heldStopAgent struct {
	agenttest.IdleAgent
	calls chan heldStopCall
}

func (a *heldStopAgent) holdStop(input []byte, stop agent.StopContext) error {
	call := heldStopCall{input: append([]byte(nil), input...), reply: make(chan error, 1), stop: stop}
	a.calls <- call
	return <-call.reply
}

func (a *heldStopAgent) Interrupt(stop agent.StopContext) error { return a.holdStop(nil, stop) }

func (a *heldStopAgent) SendRawInput(input []byte, stop agent.StopContext) error {
	return a.holdStop(input, stop)
}

type heldChildStopAgent struct{ *heldStopAgent }

func (a heldChildStopAgent) InterruptChild(childKey string, stop agent.StopContext) error {
	return a.holdStop([]byte(childKey), stop)
}

func stopOwnershipFixture(t *testing.T) (*Service, *channel.Dispatcher, agent.ProviderServices, *heldStopAgent) {
	t.Helper()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	provider := &heldStopAgent{calls: make(chan heldStopCall, 2)}
	svc.Agents.PutAgentForTest("agent-1", provider)
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
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

func dispatchHeldStop(dispatcher *channel.Dispatcher, raw bool) (*testResponseWriter, <-chan struct{}) {
	writer := newTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if raw {
			dispatch(dispatcher, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
				AgentId: "agent-1", Content: `{"type":"control_request","request_id":"stop-1","request":{"subtype":"interrupt"}}`,
			}, writer)
			return
		}
		dispatch(dispatcher, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"}, writer)
	}()
	return writer, done
}

func publishStopControl(t *testing.T, sink agent.ProviderServices, requestID string) {
	t.Helper()
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID,
		Payload:   []byte(`{"request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"pwd"}}}`),
	}))
}

func TestInterruptAgentKeepsTheReplacementTurnControl(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, sameID := range []bool{false, true} {
			t.Run(map[bool]string{false: "rpc", true: "raw"}[raw]+"/"+map[bool]string{false: "different_id", true: "same_id"}[sameID], func(t *testing.T) {
				t.Parallel()
				svc, dispatcher, sink, provider := stopOwnershipFixture(t)
				publishStopControl(t, sink, "old-control")
				writer, done := dispatchHeldStop(dispatcher, raw)
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
				requests, err := svc.Queries.ListControlRequestsByAgentID(context.Background(), "agent-1")
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
	firstWriter, firstDone := dispatchHeldStop(dispatcher, false)
	first := receiveHeldStop(t, provider)
	secondWriter, secondDone := dispatchHeldStop(dispatcher, false)
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
	writer, done := dispatchHeldStop(dispatcher, true)
	call := receiveHeldStop(t, provider)
	finishHeldStop(t, call, done, errors.New("the native stop was refused"))
	require.NotEmpty(t, writer.rejections(), "a rejected raw stop must not return RPC success")
	requests, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "waiting-control", requests[0].RequestID)
}

func TestControlPublicationRenewsTheClaimForANewTurn(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	publishStopControl(t, sink, "reused-control")
	first, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "reused-control"})
	require.NoError(t, err)
	publishStopControl(t, sink, "reused-control")
	repeated, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "reused-control"})
	require.NoError(t, err)
	assert.Equal(t, first.ClaimToken, repeated.ClaimToken, "one runtime owner keeps an identical pending claim")
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	publishStopControl(t, sink, "reused-control")
	replacement, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "reused-control"})
	require.NoError(t, err)
	assert.NotEqual(t, first.ClaimToken, replacement.ClaimToken, "a replacement turn owns a new request instance")
}

func TestInterruptAgentFailureKeepsAnotherAcceptedStop(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _, provider := stopOwnershipFixture(t)
	_, firstDone := dispatchHeldStop(dispatcher, false)
	first := receiveHeldStop(t, provider)
	writer, secondDone := dispatchHeldStop(dispatcher, false)
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
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "still-live-control"})
	require.NoError(t, err)
	writer, done := dispatchHeldStop(dispatcher, false)
	call := receiveHeldStop(t, provider)
	call.stop.ReportIgnored()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
	finishHeldStop(t, call, done, nil)
	require.Empty(t, writer.rejections())
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "still-live-control"})
	require.NoError(t, err, "an ignored stop retains the native control that still needs an answer")
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
			writer, done := dispatchHeldStop(dispatcher, false)
			call := receiveHeldStop(t, provider)
			svc.Output.ClearPendingControlRequests("agent-1")
			replacement := &heldStopAgent{calls: make(chan heldStopCall, 1)}
			svc.Agents.PutAgentForTest("agent-1", replacement)
			newSink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
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
			request, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "replacement-control"})
			require.NoError(t, err)
			assert.NotEmpty(t, request.ClaimToken)
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)
		})
	}
}

func TestInterruptChildKeepsTheReplacementTurnControl(t *testing.T) {
	t.Parallel()
	svc, dispatcher, childID, rootID := setupChildAgentTest(t)
	provider := &heldStopAgent{calls: make(chan heldStopCall, 1)}
	svc.Agents.PutAgentForTest(rootID, heldChildStopAgent{provider})
	rootSink := svc.Output.sinkForAgent(rootID)
	require.NotNil(t, rootSink)
	svc.Output.NoteAgentProcessStarted(rootID)
	childSink := rootSink.ChildSink(childID)
	childSink.SetTurnState(agent.TurnState{Active: true}, 1)
	publishStopControl(t, childSink, "old-child-control")
	writer := newTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch(dispatcher, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: childID}, writer)
	}()
	call := receiveHeldStop(t, provider)
	assert.Equal(t, "row-key-1", string(call.input))
	childSink.SetTurnState(agent.TurnState{}, 2)
	childSink.SetTurnState(agent.TurnState{Active: true}, 3)
	publishStopControl(t, childSink, "new-child-control")
	finishHeldStop(t, call, done, nil)
	require.Empty(t, writer.rejections())
	requests, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), childID)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "new-child-control", requests[0].RequestID)
}

func TestEscalationDoesNotShareAnOldTurnResultWithANewTurn(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	seedOpenAgent(t, svc, "agent-1", true)
	row := requireAgentRow(t, svc, "agent-1")
	svc.Agents.PutAgentForTest("agent-1", &heldStopAgent{calls: make(chan heldStopCall, 1)})
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
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
	unlock()
	released = true
	require.NoError(t, <-oldDone, "an obsolete escalation changes no replacement turn")
	require.ErrorIs(t, <-currentDone, launchFailure)
	assert.Equal(t, int32(1), launches.Load())
}

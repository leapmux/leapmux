package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestParseWaitUntil(t *testing.T) {
	for _, token := range []string{"settled", "idle", "working", "waiting_for_user"} {
		got, err := parseWaitUntil(token)
		require.NoError(t, err, token)
		assert.Equal(t, waitUntil(token), got)
	}
	_, err := parseWaitUntil("waiting")
	e := cli.FromError(err)
	assert.Equal(t, cli.CodeUsage, e.Code)
	assert.Equal(t, []string{"accepted values: settled, idle, working, waiting_for_user"}, e.Hints)
	_, err = parseWaitUntil("")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestWaitUntilHolds(t *testing.T) {
	unspecified := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
	for _, tc := range []struct {
		until waitUntil
		state leapmuxv1.AgentActivityState
		want  bool
	}{
		{waitUntilSettled, idle, true},
		{waitUntilSettled, waiting, true},
		{waitUntilSettled, working, false},
		{waitUntilSettled, unspecified, false},
		{"idle", idle, true},
		{"idle", waiting, false},
		{"working", working, true},
		{"working", idle, false},
		{"waiting_for_user", waiting, true},
		{"waiting_for_user", idle, false},
		{"idle", unspecified, false},
	} {
		assert.Equal(t, tc.want, tc.until.holds(agentStateToken(tc.state)), "%s holds for %s", tc.until, tc.state)
	}
}

func TestAgentWaitVerdict(t *testing.T) {
	reached := agentWaitRow{State: "idle", Reached: true}
	working := agentWaitRow{State: "working"}
	closed := agentWaitRow{State: agentStateClosed}
	closedAfterReaching := agentWaitRow{State: agentStateClosed, Reached: true}
	for name, tc := range map[string]struct {
		rows     []agentWaitRow
		anyAgent bool
		want     agentWaitOutcome
		done     bool
	}{
		"every agent reached":             {rows: []agentWaitRow{reached, reached}, want: agentWaitHeld, done: true},
		"one agent still works":           {rows: []agentWaitRow{reached, working}},
		"one agent closed before":         {rows: []agentWaitRow{reached, closed}, want: agentWaitImpossible, done: true},
		"an agent that closed after it":   {rows: []agentWaitRow{reached, closedAfterReaching}, want: agentWaitHeld, done: true},
		"any: one agent reached":          {rows: []agentWaitRow{working, reached}, anyAgent: true, want: agentWaitHeld, done: true},
		"any: none reached yet":           {rows: []agentWaitRow{working, closed}, anyAgent: true},
		"any: every agent closed before":  {rows: []agentWaitRow{closed, closed}, anyAgent: true, want: agentWaitImpossible, done: true},
		"one agent, nothing observed yet": {rows: []agentWaitRow{{State: agentStateUnknown}}},
	} {
		t.Run(name, func(t *testing.T) {
			got, done := agentWaitVerdict(tc.rows, tc.anyAgent)
			assert.Equal(t, tc.done, done)
			assert.Equal(t, tc.want, got)
		})
	}
}

// feedEvents is a buffered channel of events that a test fills before or
// while awaitAgents reads it.
func feedEvents(events ...activityEvent) chan activityEvent {
	ch := make(chan activityEvent, 32)
	for _, ev := range events {
		ch <- ev
	}
	return ch
}

func level(agentID string, state leapmuxv1.AgentActivityState) activityEvent {
	return activityEvent{kind: activityLevel, agentID: agentID, state: state}
}

func change(agentID string, state leapmuxv1.AgentActivityState) activityEvent {
	return activityEvent{kind: activityChange, agentID: agentID, state: state}
}

// noQueuedInput is a queueWaitProbe for agents whose queues are empty.
func noQueuedInput(context.Context, string, bool) (bool, error) { return false, nil }

func TestAwaitAgents_AnAgentInTheStateEndsTheWaitAtOnce(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(level("a-1", idle)),
		noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, agentWaitResult{Agents: []agentWaitRow{{ID: "a-1", State: "idle", Reached: true}}}, result)
}

func TestAwaitAgents_WaitsForEveryAgent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	events := feedEvents(
		level("a-1", working),
		level("a-2", idle),
		activityEvent{kind: activityTurnEnd, agentID: "a-1"},
		change("other", idle),
		change("a-1", waiting),
	)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), events,
		noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1", "a-2"}, until: waitConditions{waitUntilSettled}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{
		{ID: "a-1", State: "waiting_for_user", Reached: true},
		{ID: "a-2", State: "idle", Reached: true},
	}, result.Agents)
	assert.Empty(t, events, "the wait read no event past the one that met the condition")
}

// An agent reaches the state when any event reports it, so a later change
// away from the state does not undo it.
func TestAwaitAgents_KeepsAStateThatAnAgentReachedOnce(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(
		level("a-1", idle),
		level("a-2", working),
		change("a-1", working),
		change("a-2", idle),
	), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1", "a-2"}, until: waitConditions{"idle"}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{
		{ID: "a-1", State: "working", Reached: true},
		{ID: "a-2", State: "idle", Reached: true},
	}, result.Agents)
}

func TestAwaitAgents_AnyEndsWithTheFirstAgent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(
		level("a-1", working),
		level("a-2", working),
		change("a-2", waiting),
	), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1", "a-2"}, until: waitConditions{"waiting_for_user"}, anyAgent: true})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{
		{ID: "a-1", State: "working"},
		{ID: "a-2", State: "waiting_for_user", Reached: true},
	}, result.Agents)
}

func TestAwaitAgents_EndsWhenAnAgentClosesBeforeTheState(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(
		level("a-1", working),
		level("a-2", idle),
		activityEvent{kind: activityClosed, agentID: "a-1"},
	), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1", "a-2"}, until: waitConditions{"idle"}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitImpossible, outcome)
	assert.Equal(t, []agentWaitRow{
		{ID: "a-1", State: agentStateClosed},
		{ID: "a-2", State: "idle", Reached: true},
	}, result.Agents)
}

// A closed agent stays closed: a level that a read took before the close
// cannot revive it. Revived, a-1 would reach the state and end the wait as
// held.
func TestAwaitAgents_ALaterLevelDoesNotReviveAClosedAgent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(
		activityEvent{kind: activityClosed, agentID: "a-1"},
		level("a-1", idle),
		activityEvent{kind: activityClosed, agentID: "a-2"},
	), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1", "a-2"}, until: waitConditions{"idle"}, anyAgent: true})
	require.NoError(t, err)
	assert.Equal(t, agentWaitImpossible, outcome)
	assert.Equal(t, []agentWaitRow{
		{ID: "a-1", State: agentStateClosed},
		{ID: "a-2", State: agentStateClosed},
	}, result.Agents)
}

// A Worker of another version can state no state. The row then says
// "unknown", never an empty string, and the wait goes on.
func TestAwaitAgents_PrintsAnUnstatedStateAsUnknown(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(
		level("a-1", leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED),
		activityEvent{kind: activityClosed, agentID: "a-1"},
	), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitImpossible, outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: agentStateClosed}}, result.Agents)

	ctx, cancel := context.WithCancel(ctx)
	events := feedEvents(level("a-1", leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED))
	done := make(chan agentWaitResult, 1)
	go func() {
		result, _, _ := awaitAgents(ctx, testutil.NewQuartzMock(t), events, noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled}})
		done <- result
	}()
	require.Eventually(t, func() bool { return len(events) == 0 }, 10*time.Second, time.Millisecond)
	cancel()
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: agentStateUnknown}}, receive(t, testutil.DeadlineContext(t), done).Agents)
}

func TestAwaitAgents_DeadlineCancelsAnInFlightQueueRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	readStarted := make(chan context.Context, 1)
	releaseRead := make(chan struct{})
	queued := func(ctx context.Context, _ string, _ bool) (bool, error) {
		readStarted <- ctx
		select {
		case <-ctx.Done():
			return false, context.Cause(ctx)
		case <-releaseRead:
			return false, nil
		}
	}
	type ended struct {
		result  agentWaitResult
		outcome agentWaitOutcome
		err     error
	}
	done := make(chan ended, 1)
	go func() {
		result, outcome, err := awaitAgents(ctx, clock, feedEvents(level("a-1", idle)), queued,
			agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}, timeout: time.Minute})
		done <- ended{result: result, outcome: outcome, err: err}
	}()
	readCtx := receive(t, ctx, readStarted)
	clock.Advance(time.Minute).MustWait(ctx)
	assert.ErrorIs(t, readCtx.Err(), context.Canceled)
	close(releaseRead)
	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, agentWaitTimedOut, got.outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "idle"}}, got.result.Agents)
}

func TestAwaitAgents_EndsWhenTheTimeoutPasses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().AfterFunc(agentWaitTimerTag)
		defer newTimer.Close()
		events := feedEvents(level("a-1", working))
		type ended struct {
			result  agentWaitResult
			outcome agentWaitOutcome
			err     error
		}
		done := make(chan ended, 1)
		go func() {
			result, outcome, err := awaitAgents(ctx, clock, events, noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled}, timeout: 5 * time.Minute})
			done <- ended{result, outcome, err}
		}()
		assert.Equal(t, 5*time.Minute, testutil.WaitForTimer(t, ctx, newTimer))
		// The wait must process the initial activity before its deadline.
		synctest.Wait()
		clock.Advance(5 * time.Minute).MustWait(ctx)

		got := receive(t, ctx, done)
		require.NoError(t, got.err)
		assert.Equal(t, agentWaitTimedOut, got.outcome)
		assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "working"}}, got.result.Agents)
	})
}

func TestAwaitAgents_ArmsNoTimerWithoutATimeout(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	_, _, err := awaitAgents(ctx, clock, feedEvents(level("a-1", idle)), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}})
	require.NoError(t, err)
	_, armed := clock.Peek()
	assert.False(t, armed)
}

func TestAwaitAgents_ReturnsTheFailureOfTheFeed(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	refused := errors.New("the worker refused")
	_, _, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(activityEvent{kind: activityFailed, err: refused}),
		noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}})
	assert.ErrorIs(t, err, refused)
}

func TestAwaitAgents_EndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(testutil.DeadlineContext(t))
	cancel(cli.ErrInterrupted)
	_, _, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(), noQueuedInput, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}})
	assert.ErrorIs(t, err, cli.ErrInterrupted)
}

// A starting agent reports idle while its first message waits in the queue,
// so an idle agent with a queued message has not settled. Its turn sends a
// new change, and the queue is asked again then.
func TestAwaitAgents_AQueuedMessageKeepsAnIdleAgentFromSettling(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var asked []string
	queued := func(_ context.Context, agentID string, _ bool) (bool, error) {
		asked = append(asked, agentID)
		return len(asked) == 1, nil
	}
	events := feedEvents(level("a-1", idle), change("a-1", working), change("a-1", idle))
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), events, queued, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "idle", Reached: true}}, result.Agents)
	assert.Equal(t, []string{"a-1", "a-1"}, asked, "the queue is asked only when the state holds")
}

func TestAwaitAgents_RechecksTheQueueWithoutAnActivityChange(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var reads []string
	queued := func(_ context.Context, agentID string, _ bool) (bool, error) {
		reads = append(reads, agentID)
		return len(reads) == 1, nil
	}
	events := feedEvents(
		level("a-1", idle),
		activityEvent{kind: activityInputChanged, agentID: "outside"},
		activityEvent{kind: activityInputChanged, agentID: "a-1"},
		activityEvent{kind: activityClosed, agentID: "a-1"},
	)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), events, queued, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "idle", Reached: true}}, result.Agents)
	assert.Equal(t, []string{"a-1", "a-1"}, reads)
	assert.Len(t, events, 1, "the wait ends before the close")
}

func TestAwaitAgents_AsksTheQueueOnlyForIdleAndSettled(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	refuse := func(context.Context, string, bool) (bool, error) {
		t.Error("a wait for working or waiting_for_user asked the queue")
		return true, nil
	}
	for until, state := range map[waitUntil]leapmuxv1.AgentActivityState{"working": working, "waiting_for_user": waiting} {
		_, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(level("a-1", state)), refuse, agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{until}})
		require.NoError(t, err)
		assert.Equal(t, agentWaitHeld, outcome, until)
	}
	assert.True(t, waitUntil("idle").needsEmptyQueue("idle"))
	assert.True(t, waitUntilSettled.needsEmptyQueue("idle"))
	assert.False(t, waitUntilSettled.needsEmptyQueue("waiting_for_user"))
}

// SWEEP-1: an agent in waiting_for_user is blocked on a prompt and cannot drain
// its input queue until the user answers, so --until settled must complete
// without asking or blocking on queued messages.
func TestAwaitAgents_SettledWaitingForUserDoesNotBlockOnQueuedMessages(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(level("a-1", waiting)),
		func(context.Context, string, bool) (bool, error) {
			t.Error("settled on waiting_for_user must not ask the queue")
			return true, nil
		},
		agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "waiting_for_user", Reached: true}}, result.Agents)
}

// TRAPS-11: a completed queue probe result (either a non-cancellation error or
// a settled answer) must not be discarded if the timeout expires right as the
// probe finishes.
func TestAwaitAgents_CompletedQueueProbeWinsOverTimeoutRace(t *testing.T) {
	t.Run("non-cancellation probe error is returned even if timeout also expired", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		refused := errors.New("worker permission denied")
		probe := func(_ context.Context, _ string, _ bool) (bool, error) {
			clock.Advance(time.Minute).MustWait(ctx)
			return false, refused
		}
		_, _, err := awaitAgents(ctx, clock, feedEvents(level("a-1", idle)), probe,
			agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}, timeout: time.Minute})
		assert.ErrorIs(t, err, refused)
	})

	t.Run("settled probe result completes wait even if timeout also expired", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		probe := func(_ context.Context, _ string, _ bool) (bool, error) {
			clock.Advance(time.Minute).MustWait(ctx)
			return false, nil
		}
		result, outcome, err := awaitAgents(ctx, clock, feedEvents(level("a-1", idle)), probe,
			agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}, timeout: time.Minute})
		require.NoError(t, err)
		assert.Equal(t, agentWaitHeld, outcome)
		assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "idle", Reached: true}}, result.Agents)
	})
}

func TestAwaitAgents_InvokesOnActivityCallback(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var seen []activityEvent
	events := feedEvents(
		level("unwatched", working),
		level("a-1", working),
		change("a-1", idle),
	)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), events, noQueuedInput, agentWaitSpec{
		agentIDs: []string{"a-1"},
		until:    waitConditions{"idle"},
		onActivity: func(ev activityEvent) {
			seen = append(seen, ev)
		},
	})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "idle", Reached: true}}, result.Agents)
	assert.Equal(t, []activityEvent{level("a-1", working), change("a-1", idle)}, seen)
}

func TestAwaitAgents_AlternativesAcrossAgents(t *testing.T) {
	for _, anyAgent := range []bool{false, true} {
		ctx := testutil.DeadlineContext(t)
		result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t),
			feedEvents(level("a-1", working), level("a-2", waiting)), noQueuedInput,
			agentWaitSpec{agentIDs: []string{"a-1", "a-2"}, until: waitConditions{"working", "waiting_for_user"}, anyAgent: anyAgent})
		require.NoError(t, err)
		assert.Equal(t, agentWaitHeld, outcome)
		assert.Equal(t, agentWaitRow{ID: "a-1", State: "working", Reached: true}, result.Agents[0])
		if anyAgent {
			assert.Equal(t, agentWaitRow{ID: "a-2", State: agentStateUnknown}, result.Agents[1])
		} else {
			assert.Equal(t, agentWaitRow{ID: "a-2", State: "waiting_for_user", Reached: true}, result.Agents[1])
		}
	}
}

func TestAwaitAgents_DirectAlternativeNeedsNoQueueRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	result, outcome, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(level("a-1", waiting)),
		func(context.Context, string, bool) (bool, error) { return false, errors.New("unexpected queue read") },
		agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{waitUntilSettled, "waiting_for_user"}})
	require.NoError(t, err)
	assert.Equal(t, agentWaitHeld, outcome)
	assert.Equal(t, []agentWaitRow{{ID: "a-1", State: "waiting_for_user", Reached: true}}, result.Agents)
}

func TestAwaitAgents_ReturnsTheFailureOfAQueueRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	refused := errors.New("the worker refused")
	_, _, err := awaitAgents(ctx, testutil.NewQuartzMock(t), feedEvents(level("a-1", idle)),
		func(context.Context, string, bool) (bool, error) { return false, refused },
		agentWaitSpec{agentIDs: []string{"a-1"}, until: waitConditions{"idle"}})
	assert.ErrorIs(t, err, refused)
}

func TestQueuedForDispatch(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	item := func(state leapmuxv1.AgentInputState, scheduledAt string) *leapmuxv1.QueuedAgentInput {
		return &leapmuxv1.QueuedAgentInput{Id: "in-1", State: state, ScheduledAt: scheduledAt}
	}
	for name, tc := range map[string]struct {
		snapshot *leapmuxv1.AgentInputQueueSnapshot
		want     bool
	}{
		"no queue":                 {nil, false},
		"an empty queue":           {&leapmuxv1.AgentInputQueueSnapshot{}, false},
		"a queued message":         {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, "")}}, true},
		"a dispatching message":    {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, "")}}, true},
		"a steer awaiting echo":    {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, "")}}, true},
		"a failed message":         {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, "")}}, false},
		"an uncertain delivery":    {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, "")}}, false},
		"a message for later":      {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, "2026-11-19T13:00:00Z")}}, false},
		"a message that is due":    {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, "2026-11-19T12:00:00Z")}}, true},
		"a time that cannot parse": {&leapmuxv1.AgentInputQueueSnapshot{Items: []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, "noon")}}, true},
		"a paused queue": {&leapmuxv1.AgentInputQueueSnapshot{
			PauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL,
			Items:       []*leapmuxv1.QueuedAgentInput{item(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, "")},
		}, true},
	} {
		assert.Equal(t, tc.want, queuedForDispatch(tc.snapshot, now), name)
	}
}

func TestQueueWatch_SaysOnceWhyAPausedQueueKeepsTheWaitGoing(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	paused := &leapmuxv1.AgentInputQueueSnapshot{
		PauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL,
		Items:       []*leapmuxv1.QueuedAgentInput{{Id: "in-1", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}},
	}
	snapshots := map[string]*leapmuxv1.AgentInputQueueSnapshot{"a-1": paused, "a-2": {PauseReason: paused.GetPauseReason()}}
	var notices []string
	q := &queueWatch{
		read: func(_ context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error) {
			return snapshots[agentID], nil
		},
		notice: func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) },
		now:    time.Now,
	}
	for range 2 {
		busy, err := q.blocksWait(ctx, "a-1", true)
		require.NoError(t, err)
		assert.True(t, busy)
	}
	busy, err := q.blocksWait(ctx, "a-2", true)
	require.NoError(t, err)
	assert.False(t, busy, "a paused queue with nothing in it holds nothing back")
	assert.Equal(t, []string{"the input queue of agent a-1 is paused (manual); the wait goes on until the agent reads its messages"}, notices)

	refused := errors.New("the worker refused")
	q.read = func(context.Context, string) (*leapmuxv1.AgentInputQueueSnapshot, error) { return nil, refused }
	_, err = q.blocksWait(ctx, "a-1", true)
	assert.ErrorIs(t, err, refused)
}

func TestGroupAgentsByWorker(t *testing.T) {
	groups, agentIDs := groupAgentsByWorker([]tabTarget{
		{ID: "a-1", WorkerID: "w-1"},
		{ID: "b-1", WorkerID: "w-2"},
		{ID: "a-2", WorkerID: "w-1"},
		{ID: "a-1", WorkerID: "w-1"},
	})
	assert.Equal(t, []string{"a-1", "b-1", "a-2"}, agentIDs, "the result follows the arguments, and a repeated agent counts once")
	assert.Equal(t, []workerAgents{
		{workerID: "w-1", agentIDs: []string{"a-1", "a-2"}},
		{workerID: "w-2", agentIDs: []string{"b-1"}},
	}, groups, "one watch for each Worker")

	groups, agentIDs = groupAgentsByWorker(nil)
	assert.Empty(t, groups)
	assert.Empty(t, agentIDs)
}

func TestRefuseSelfWait(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "agent-self")
	assert.NoError(t, refuseSelfWait(tabTarget{ID: "agent-other"}))
	for name, target := range map[string]tabTarget{
		"self":            {ID: "agent-self", Self: true},
		"own id, spelled": {ID: "agent-self"},
	} {
		err := refuseSelfWait(target)
		e := cli.FromError(err)
		require.NotNil(t, e, name)
		assert.Equal(t, cli.CodeUsage, e.Code, name)
		assert.Contains(t, e.Message, "agent-self", name)
	}
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "")
	assert.NoError(t, refuseSelfWait(tabTarget{ID: "agent-self"}), "outside a tab no agent is the caller")
}

func TestAgentWaitEnded(t *testing.T) {
	result := agentWaitResult{Agents: []agentWaitRow{
		{ID: "a-1", State: "working"},
		{ID: "a-2", State: agentStateClosed},
		{ID: "a-3", State: "idle", Reached: true},
	}}
	for name, tc := range map[string]struct {
		outcome agentWaitOutcome
		notice  string
	}{
		"timeout":    {agentWaitTimedOut, "the wait ended after 1m30s, before agents a-1, a-2 reached the state idle\n"},
		"impossible": {agentWaitImpossible, "the wait ended, because agent a-2 closed before reaching the state idle\n"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			p := &cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatJSON}
			err := agentWaitEnded(p, result, tc.outcome, agentWaitSpec{until: waitConditions{"idle"}, timeout: 90 * time.Second})
			var status *cli.ExitStatus
			require.ErrorAs(t, err, &status)
			assert.Equal(t, cli.ExitWaitEnded, status.Code)
			assert.JSONEq(t, `{"data":{"agents":[
				{"id":"a-1","state":"working","reached":false},
				{"id":"a-2","state":"closed","reached":false},
				{"id":"a-3","state":"idle","reached":true}]}}`, out.String(), "the result prints whether the condition held or not")
			assert.Equal(t, tc.notice, errOut.String())
		})
	}

	t.Run("impossible with multiple closed agents and multiple conditions", func(t *testing.T) {
		multi := agentWaitResult{Agents: []agentWaitRow{
			{ID: "a-1", State: "working"},
			{ID: "a-2", State: agentStateClosed},
			{ID: "a-3", State: "idle", Reached: true},
			{ID: "a-4", State: agentStateClosed},
		}}
		var out, errOut bytes.Buffer
		p := &cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatJSON}
		err := agentWaitEnded(p, multi, agentWaitImpossible, agentWaitSpec{until: waitConditions{"idle", "waiting_for_user"}})
		var status *cli.ExitStatus
		require.ErrorAs(t, err, &status)
		assert.Equal(t, cli.ExitWaitEnded, status.Code)
		assert.Equal(t, "the wait ended, because agents a-2, a-4 closed before reaching any of the states idle, waiting_for_user\n", errOut.String())
	})
}

type queuePoolDispatcher struct {
	mu      sync.Mutex
	errCode codes.Code
	errMsg  string
	items   []*leapmuxv1.QueuedAgentInput
}

func (d *queuePoolDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	d.mu.Lock()
	code, msg, items := d.errCode, d.errMsg, d.items
	d.mu.Unlock()
	if code != codes.OK {
		_ = rw.SendError(int32(code), msg)
		return
	}
	writeProtoResponse(rw, &leapmuxv1.ListAgentInputQueueResponse{
		Snapshot: &leapmuxv1.AgentInputQueueSnapshot{Items: items},
	})
}

func TestWorkerQueuePool_ReusesChannelReconnectsOnDropAndClosesCleanly(t *testing.T) {
	id := fullTestID("ag-pool")
	disp := &queuePoolDispatcher{
		items: []*leapmuxv1.QueuedAgentInput{{Id: "in-1", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}},
	}
	harness := startHubChannelHarness(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	client, err := control.NewClient(harness.url)
	require.NoError(t, err)

	pool := newWorkerQueuePool(client)
	defer pool.close()
	ctx := testutil.DeadlineContext(t)

	// Empty workerID fails locally before opening any channel.
	_, err = pool.readQueue(ctx, "", id)
	var coded *codedRPCError
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, "invalid_request", coded.Code)
	assert.Zero(t, harness.channelOpens())

	// Repeated probes against the same worker share one E2EE channel.
	for range 3 {
		snap, readErr := pool.readQueue(ctx, "worker-A", id)
		require.NoError(t, readErr)
		require.Len(t, snap.GetItems(), 1)
	}
	assert.Equal(t, 1, harness.channelOpens(), "multiple queue probes must reuse one E2EE channel")

	// In-band RPC errors (not rpc_failed) must not drop or reopen the cached channel.
	disp.mu.Lock()
	disp.errCode = codes.PermissionDenied
	disp.errMsg = "forbidden"
	disp.mu.Unlock()
	_, err = pool.readQueue(ctx, "worker-A", id)
	require.Error(t, err)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
	assert.Equal(t, 1, harness.channelOpens(), "in-band RPC error must not reopen the channel")

	disp.mu.Lock()
	disp.errCode = codes.OK
	disp.errMsg = ""
	disp.mu.Unlock()

	// Dropping the relay triggers rpc_failed and a single transparent reconnect.
	harness.mu.Lock()
	for _, ws := range harness.relays {
		_ = ws.CloseNow()
	}
	harness.mu.Unlock()
	snap, err := pool.readQueue(ctx, "worker-A", id)
	require.NoError(t, err)
	require.Len(t, snap.GetItems(), 1)
	assert.Equal(t, 2, harness.channelOpens(), "transport drop must trigger one reconnect")

	// Closing the pool is idempotent and prevents further channel opens.
	pool.close()
	pool.close()
	_, err = pool.readQueue(ctx, "worker-A", id)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, harness.channelOpens(), "closed pool must not open new channels")
}

type countingAgentsWorker struct {
	mu        sync.Mutex
	agents    []*leapmuxv1.AgentInfo
	listCalls int
}

func (w *countingAgentsWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		w.mu.Lock()
		w.listCalls++
		agents := w.agents
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: agents})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestResolveWaitTargets_MemoizesDiscoveryAcrossMultipleTargets(t *testing.T) {
	idA := fullTestID("aaaa")
	idB := fullTestID("bbbb")
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		workspaces: []*leapmuxv1.Workspace{
			{Id: "ws-1", CreatedBy: "u-spawn", Title: "Main"},
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: idA}, TileId: "tile-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: idB}, TileId: "tile-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	w := &countingAgentsWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: idA, Title: "Alpha Agent"},
			{Id: idB, Title: "Beta Agent"},
		},
	}
	startSpawnIPC(t, hub, w)

	root := NewCommand()
	waitCmd, _, err := root.Find([]string{"agent", "wait"})
	require.NoError(t, err)
	require.NoError(t, waitCmd.Flags().Set("workspace", "title:Main"))

	inv := &cli.Invocation{Context: testutil.DeadlineContext(t), Command: waitCmd}
	s, err := openStreamSession(inv)
	require.NoError(t, err)
	defer s.Close()

	targets, err := resolveWaitTargets(s, []string{"title:Alpha Agent", "title:Beta Agent"})
	require.NoError(t, err)
	require.Len(t, targets, 2)
	assert.Equal(t, idA, targets[0].ID)
	assert.Equal(t, idB, targets[1].ID)

	listTabsCount, listWorkspacesCount := 0, 0
	for _, proc := range hub.called() {
		switch proc {
		case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
			listTabsCount++
		case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
			listWorkspacesCount++
		}
	}
	assert.Equal(t, 1, listWorkspacesCount, "workspace scope should resolve once across targets")
	assert.Equal(t, 1, listTabsCount, "ListTabs should run at most once across targets")
	w.mu.Lock()
	listCalls := w.listCalls
	w.mu.Unlock()
	assert.Equal(t, 1, listCalls, "TabTitles (ListAgents) should run at most once across targets")
}

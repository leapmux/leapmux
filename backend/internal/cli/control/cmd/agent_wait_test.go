package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
	assert.True(t, waitUntil("idle").needsEmptyQueue())
	assert.True(t, waitUntilSettled.needsEmptyQueue())
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
}

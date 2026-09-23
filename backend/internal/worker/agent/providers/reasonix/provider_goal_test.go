package reasonix

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonixGoal_StatusUpdateReportsTheGoal(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))

	handled := a.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"","status":{"goal":{"status":"running",` +
			`"objective":"land the refactor","runtime":{"turnsUsed":6,"tokensUsed":9000,` +
			`"lastReason":"tests still red"}}}}`),
	})

	require.True(t, handled, "the reasonix namespace must be claimed, not left unknown")
	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "land the refactor", got.Objective)
	assert.Equal(t, agent.GoalStatusActive, got.Status)
	assert.Equal(t, "tests still red", got.StatusDetail)
	require.NotNil(t, got.Iterations)
	assert.EqualValues(t, 6, *got.Iterations)
	require.NotNil(t, got.TokensUsed)
	assert.EqualValues(t, 9000, *got.TokensUsed)
}

// A stop cause says more than the status word: goal_stuck and budget_spend are
// both reported as `stopped`.
func TestReasonixGoal_StopCauseWinsOverTheStatusWord(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))

	a.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"status":{"goal":{"status":"stopped","objective":"x",` +
			`"runtime":{"stopCause":"budget_spend","lastReason":"out of budget"}}}}`),
	})

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusBlocked, got.Status)
	assert.Equal(t, "budget_spend", got.StatusDetail)
}

func TestReasonixGoal_NoneClears(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))

	a.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"status":{"goal":{"status":"none"}}}`),
	})

	assert.Equal(t, 1, sink.GoalClears())
}

// ClearContext mints a NEW ACP sessionId. A status notification still in flight
// for the OLD session must not be applied, or a goal the user just cleared
// comes back.
func TestReasonixGoal_IgnoresAnotherSession(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := &Agent{}
	ag.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.SetSessionIDForTest("session-new")

	ag.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"session-old","status":{"goal":` +
			`{"status":"running","objective":"stale objective"}}}`),
	})

	assert.Empty(t, sink.Goals(), "a notification for a replaced session is dropped")
}

// Reasonix exposes goal controls only after the handshake advertises their modes.
func TestReasonixGoal_RequiresAdvertisedModes(t *testing.T) {
	t.Parallel()

	var a any = &Agent{}
	_, ok := a.(agent.GoalWriter)
	assert.True(t, ok, "Reasonix has verified Set and Clear operations")
	_, ok = a.(agent.GoalCapable)
	assert.True(t, ok)
	assert.Empty(t, a.(agent.GoalCapable).SupportedGoalActions())
}

// Reasonix's real wire vocabulary. `cancelled` and `failed` come from a
// per-turn override rather than the goal enum, so they are easy to miss; both
// mean "not progressing, needs the user".
func TestReasonixGoal_StatusMapping(t *testing.T) {
	t.Parallel()

	for wire, want := range map[string]agent.GoalStatus{
		"running":  agent.GoalStatusActive,
		"complete": agent.GoalStatusDone,
		"blocked":  agent.GoalStatusBlocked,
		// Set when the user cancels a turn, and when a turn returns an error.
		"cancelled": agent.GoalStatusBlocked,
		"failed":    agent.GoalStatusBlocked,
		// A word this build does not know must not offer Pause either.
		"somethingNew": agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, reasonixGoalStatus(wire), "status %q", wire)
	}
}

// The notification is also observed with the status fields HOISTED to the top
// level. The struct declares both shapes because declaring one silently
// reported no goal, and every other Reasonix test here sends the nested shape --
// so without this the hoisted field could be deleted with a green suite.
func TestReasonixGoal_ReadsTheHoistedShape(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := &Agent{}
	ag.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	ag.SetSinkForTest(agent.NewProviderServices(sink))

	handled := ag.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"","goal":{"status":"running",` +
			`"objective":"hoisted objective"}}`),
	})

	require.True(t, handled)
	got, ok := sink.LastGoal()
	require.True(t, ok, "the hoisted shape must report a goal, not silence")
	assert.Equal(t, "hoisted objective", got.Objective)
	assert.Equal(t, agent.GoalStatusActive, got.Status)
}

// When BOTH shapes arrive, the nested one wins. Nothing else pins that
// precedence, so inverting it would keep the suite green.
func TestReasonixGoal_NestedStatusWinsOverTheHoistedCopy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))

	a.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"",` +
			`"goal":{"status":"running","objective":"hoisted"},` +
			`"status":{"goal":{"status":"running","objective":"nested"}}}`),
	})

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "nested", got.Objective)
}

// workDurationMs is MILLISECONDS and TimeUsedSeconds is seconds. A direct
// assignment would print a duration a thousand times too large.
func TestReasonixGoal_ConvertsWorkDurationToSeconds(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))

	a.handleExtraMethod(&providerkit.ParsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"","status":{"goal":{"status":"running",` +
			`"objective":"land it","runtime":{"workDurationMs":90000}}}}`),
	})

	got, ok := sink.LastGoal()
	require.True(t, ok)
	require.NotNil(t, got.TimeUsedSeconds)
	assert.EqualValues(t, 90, *got.TimeUsedSeconds)
}

// A status update that reaches the reader goroutine while a session/new round
// trip holds sessionMu must not block. It once did: IsCurrentSession took
// sessionMu.RLock on the goroutine that has to deliver that round trip's own
// response, so the clear and the whole output stream stalled until the API
// timeout.
func TestReasonixGoal_StatusUpdateDoesNotBlockOnTheSessionLock(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))

	a.SessionMuForTest().Lock()
	defer a.SessionMuForTest().Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.handleExtraMethod(&providerkit.ParsedLine{
			Method: reasonixMethodStatusUpdate,
			Params: json.RawMessage(`{"sessionId":"","status":{"goal":{"status":"running","objective":"x"}}}`),
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the status update blocked on sessionMu, which the reader goroutine must never wait for")
	}
}

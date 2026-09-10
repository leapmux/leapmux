//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newClaudeGoalAgent gives a Claude agent whose stdin is a real pipe. The read
// end drains so a long command cannot fill the pipe buffer.
func newClaudeGoalAgent(t *testing.T, sink ProviderServices) *ClaudeCodeAgent {
	t.Helper()
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = writePipe.Close()
		_ = readPipe.Close()
	})
	go func() { _, _ = io.Copy(io.Discard, readPipe) }()
	agent := newTestAgent(sink)
	agent.stdin = writePipe
	return agent
}

// goalStub implements the Agent provider surface (via stubProvider) plus
// GoalWriter, so Manager.UpdateGoal can reach it.
//
// `supported` is settable per test, because the whole point of the Manager's
// check is that a provider's capability list and its implemented methods are
// one answer: a stub whose list always held every action could not exercise
// the refusal.
type goalStub struct {
	stubProvider
	supported []GoalAction
	setCalls  []string
	clears    int
	pauses    int
	resumes   int
	err       error
}

func (g *goalStub) SupportedGoalActions() []GoalAction { return g.supported }

// A side-band writer: every action completes here, so the outcome is empty and
// the Manager has nothing to hand back to the caller.
func (g *goalStub) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	switch action {
	case GoalActionSet:
		g.setCalls = append(g.setCalls, objective)
	case GoalActionClear:
		g.clears++
	case GoalActionPause:
		g.pauses++
	case GoalActionResume:
		g.resumes++
	default:
		return GoalOutcome{}, ErrGoalControlUnsupported
	}
	return GoalOutcome{}, g.err
}

// Compile-time drift guards, the same pair manager_child_test.go keeps for
// ChildSteerer.
var (
	_ GoalWriter = (*goalStub)(nil)
	_ Agent      = (*goalStub)(nil)
)

func allGoalActions() []GoalAction {
	return []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume}
}

func TestManagerGoal_UpdateGoalOnAnAgentThatIsNotRunning(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)

	_, err := m.UpdateGoal("nope", GoalActionPause, "")

	assert.ErrorIs(t, err, ErrAgentNotFound)
}

// A provider that reports a goal without being able to change one implements no
// GoalWriter at all, and the Manager must refuse rather than panic on the
// type assertion.
func TestManagerGoal_UpdateGoalOnAProviderWithNoController(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = &stubProvider{}
	m.mu.Unlock()

	_, err := m.UpdateGoal("root", GoalActionPause, "")

	assert.ErrorIs(t, err, ErrGoalControlUnsupported)
}

// The backstop for a stale browser. The capability list and the dispatch must
// come from the SAME agent instance, so an action the running process does not
// list is refused BEFORE the provider's method runs.
func TestManagerGoal_RefusesAnActionTheAgentDoesNotList(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	st := &goalStub{supported: []GoalAction{GoalActionSet, GoalActionClear}}
	m.mu.Lock()
	m.agents["root"] = st
	m.mu.Unlock()

	_, err := m.UpdateGoal("root", GoalActionPause, "")

	assert.ErrorIs(t, err, ErrGoalControlUnsupported)
	assert.Zero(t, st.pauses, "the provider's method must not run for an unlisted action")
}

func TestManagerGoal_DispatchesEachActionToItsOwnMethod(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	st := &goalStub{supported: allGoalActions()}
	m.mu.Lock()
	m.agents["root"] = st
	m.mu.Unlock()

	for _, call := range []struct {
		action    GoalAction
		objective string
	}{
		{GoalActionSet, "ship it"},
		{GoalActionPause, ""},
		{GoalActionResume, ""},
		{GoalActionClear, ""},
	} {
		command, err := m.UpdateGoal("root", call.action, call.objective)
		require.NoError(t, err)
		assert.Empty(t, command, "a side-band controller returns no queued command")
	}

	assert.Equal(t, []string{"ship it"}, st.setCalls, "only SET carries the objective")
	assert.Equal(t, 1, st.pauses)
	assert.Equal(t, 1, st.resumes)
	assert.Equal(t, 1, st.clears)
}

// The provider's own failure reaches the caller unchanged, so the service can
// tell "this agent cannot do that" from "the provider refused it".
func TestManagerGoal_ReturnsTheProvidersError(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	st := &goalStub{supported: allGoalActions(), err: assert.AnError}
	m.mu.Lock()
	m.agents["root"] = st
	m.mu.Unlock()

	_, err := m.UpdateGoal("root", GoalActionPause, "")

	assert.ErrorIs(t, err, assert.AnError)
	assert.NotErrorIs(t, err, ErrGoalControlUnsupported,
		"a provider failure is not a capability refusal")
}

// The browser disables every control it does not find here, so "nothing" is the
// safe answer for an agent that is not running and for a provider that reports
// a goal without being able to change one.
func TestManagerGoal_SupportedGoalActionsAnswersNothingWhenItCannotKnow(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["plain"] = &stubProvider{}
	m.mu.Unlock()

	assert.Empty(t, m.SupportedGoalActions("not-running"))
	assert.Empty(t, m.SupportedGoalActions("plain"))
}

// The capability is read from the LIVE process, never a per-provider table:
// goal support is version-dependent, so a table would offer a control that does
// nothing against an older CLI.
func TestManagerGoal_SupportedGoalActionsReadsTheRunningAgent(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	st := &goalStub{supported: []GoalAction{GoalActionSet, GoalActionClear}}
	m.mu.Lock()
	m.agents["root"] = st
	m.mu.Unlock()

	assert.Equal(t, []GoalAction{GoalActionSet, GoalActionClear}, m.SupportedGoalActions("root"))

	// The same agent, answering differently once its process learns more --
	// which is exactly what Claude Code does when its init frame arrives.
	st.supported = allGoalActions()
	assert.Equal(t, allGoalActions(), m.SupportedGoalActions("root"))
}

// A goal command that enters through the normal input queue must update the
// local goal row after the provider accepts it. The provider cannot observe
// delivery before Manager.SendInput returns successfully.
func TestManagerGoal_SendInputObservesADeliveredClaudeGoalCommand(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	provider := newClaudeGoalAgent(t, sink)
	provider.HandleOutput([]byte(
		`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = provider
	m.mu.Unlock()

	require.NoError(t, m.SendInput("root", "/goal ship the release", nil))

	goal, ok := sink.LastGoal()
	require.True(t, ok, "delivery must update the local goal row")
	assert.Equal(t, "ship the release", goal.Objective)
}

func TestManagerGoal_SendInputDoesNotObserveARefusedCommand(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	provider := newClaudeGoalAgent(t, sink)
	provider.HandleOutput([]byte(
		`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	provider.mu.Lock()
	provider.stopped = true
	provider.mu.Unlock()
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = provider
	m.mu.Unlock()

	assert.Error(t, m.SendInput("root", "/goal ship the release", nil))
	assert.Empty(t, sink.Goals())
}

func TestManagerGoal_UpdateGoalReturnsTextRouteCommand(t *testing.T) {
	t.Parallel()
	provider := newClaudeGoalAgent(t, &testSink{})
	provider.HandleOutput([]byte(
		`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = provider
	m.mu.Unlock()

	command, err := m.UpdateGoal("root", GoalActionSet, "  ship\n the release ")

	require.NoError(t, err)
	assert.Equal(t, "/goal ship the release", command)
}

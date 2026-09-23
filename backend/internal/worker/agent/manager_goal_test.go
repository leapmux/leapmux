//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent_test

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goalStub implements the Agent provider surface (via stubProvider) plus
// GoalWriter, so Manager.UpdateGoal can reach it.
//
// `supported` is settable per test, because the whole point of the Manager's
// check is that a provider's capability list and its implemented methods are
// one answer: a stub whose list always held every action could not exercise
// the refusal.
type goalStub struct {
	stubProvider
	supported []agent.GoalAction
	setCalls  []string
	clears    int
	pauses    int
	resumes   int
	err       error
}

func (g *goalStub) SupportedGoalActions() []agent.GoalAction { return g.supported }

// A side-band writer: every action completes here, so the outcome is empty and
// the Manager has nothing to hand back to the caller.
func (g *goalStub) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	switch action {
	case agent.GoalActionSet:
		g.setCalls = append(g.setCalls, objective)
	case agent.GoalActionClear:
		g.clears++
	case agent.GoalActionPause:
		g.pauses++
	case agent.GoalActionResume:
		g.resumes++
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	return agent.GoalOutcome{}, g.err
}

// Compile-time drift guards, the same pair manager_child_test.go keeps for
// ChildSteerer.
var (
	_ agent.GoalWriter = (*goalStub)(nil)
	_ agent.Agent      = (*goalStub)(nil)
)

func allGoalActions() []agent.GoalAction {
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

func TestManagerGoal_UpdateGoalOnAnAgentThatIsNotRunning(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)

	_, err := m.UpdateGoal("nope", agent.GoalActionPause, "")

	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

// A provider that reports a goal without being able to change one implements no
// GoalWriter at all, and the Manager must refuse rather than panic on the
// type assertion.
func TestManagerGoal_UpdateGoalOnAProviderWithNoController(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("root", &stubProvider{})

	_, err := m.UpdateGoal("root", agent.GoalActionPause, "")

	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
}

// The backstop for a stale browser. The capability list and the dispatch must
// come from the SAME agent instance, so an action the running process does not
// list is refused BEFORE the provider's method runs.
func TestManagerGoal_RefusesAnActionTheAgentDoesNotList(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &goalStub{supported: []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}}
	m.PutAgentForTest("root", st)

	_, err := m.UpdateGoal("root", agent.GoalActionPause, "")

	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	assert.Zero(t, st.pauses, "the provider's method must not run for an unlisted action")
}

func TestManagerGoal_DispatchesEachActionToItsOwnMethod(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &goalStub{supported: allGoalActions()}
	m.PutAgentForTest("root", st)

	for _, call := range []struct {
		action    agent.GoalAction
		objective string
	}{
		{agent.GoalActionSet, "ship it"},
		{agent.GoalActionPause, ""},
		{agent.GoalActionResume, ""},
		{agent.GoalActionClear, ""},
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
	m := agent.NewManager(testRegistry, nil)
	st := &goalStub{supported: allGoalActions(), err: assert.AnError}
	m.PutAgentForTest("root", st)

	_, err := m.UpdateGoal("root", agent.GoalActionPause, "")

	assert.ErrorIs(t, err, assert.AnError)
	assert.NotErrorIs(t, err, agent.ErrGoalControlUnsupported,
		"a provider failure is not a capability refusal")
}

// The browser disables every control it does not find here, so "nothing" is the
// safe answer for an agent that is not running and for a provider that reports
// a goal without being able to change one.
func TestManagerGoal_SupportedGoalActionsAnswersNothingWhenItCannotKnow(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("plain", &stubProvider{})

	assert.Empty(t, m.SupportedGoalActions("not-running"))
	assert.Empty(t, m.SupportedGoalActions("plain"))
}

// The capability is read from the LIVE process, never a per-provider table:
// goal support is version-dependent, so a table would offer a control that does
// nothing against an older CLI.
func TestManagerGoal_SupportedGoalActionsReadsTheRunningAgent(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &goalStub{supported: []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}}
	m.PutAgentForTest("root", st)

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, m.SupportedGoalActions("root"))

	// The same agent, answering differently once its process learns more --
	// which is exactly what Claude Code does when its init frame arrives.
	st.supported = allGoalActions()
	assert.Equal(t, allGoalActions(), m.SupportedGoalActions("root"))
}

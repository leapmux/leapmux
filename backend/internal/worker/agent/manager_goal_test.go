//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goalStub implements the Agent provider surface (via stubProvider) plus
// GoalController, so Manager.UpdateGoal can reach it.
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

func (g *goalStub) SetGoal(objective string) error {
	g.setCalls = append(g.setCalls, objective)
	return g.err
}
func (g *goalStub) ClearGoal() error  { g.clears++; return g.err }
func (g *goalStub) PauseGoal() error  { g.pauses++; return g.err }
func (g *goalStub) ResumeGoal() error { g.resumes++; return g.err }

// Compile-time drift guards, the same pair manager_child_test.go keeps for
// ChildSteerer.
var (
	_ GoalController = (*goalStub)(nil)
	_ Agent          = (*goalStub)(nil)
)

func allGoalActions() []GoalAction {
	return []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume}
}

func TestManagerGoal_UpdateGoalOnAnAgentThatIsNotRunning(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)

	err := m.UpdateGoal("nope", GoalActionPause, "")

	assert.ErrorIs(t, err, ErrAgentNotFound)
}

// A provider that reports a goal without being able to change one implements no
// GoalController at all, and the Manager must refuse rather than panic on the
// type assertion.
func TestManagerGoal_UpdateGoalOnAProviderWithNoController(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = &stubProvider{}
	m.mu.Unlock()

	err := m.UpdateGoal("root", GoalActionPause, "")

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

	err := m.UpdateGoal("root", GoalActionPause, "")

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

	require.NoError(t, m.UpdateGoal("root", GoalActionSet, "ship it"))
	require.NoError(t, m.UpdateGoal("root", GoalActionPause, ""))
	require.NoError(t, m.UpdateGoal("root", GoalActionResume, ""))
	require.NoError(t, m.UpdateGoal("root", GoalActionClear, ""))

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

	err := m.UpdateGoal("root", GoalActionPause, "")

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

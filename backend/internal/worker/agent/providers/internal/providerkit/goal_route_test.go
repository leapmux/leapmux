package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestFoldGoalObjective_CollapsesWhitespace(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ship the release", foldGoalObjective("  ship\n\tthe  release  "))
}

func TestParseGoalCommandText_ClassifiesTheCompleteCommand(t *testing.T) {
	t.Parallel()
	clearArgs := []string{"clear", "off"}
	for _, test := range []struct {
		name      string
		text      string
		intent    goalTextIntent
		objective string
	}{
		{name: "other text", text: "please /goal ship", intent: goalTextNotCommand},
		{name: "bare query", text: "/goal", intent: goalTextBareQuery},
		{name: "empty argument", text: "/goal   ", intent: goalTextBareQuery},
		{name: "clear", text: "/goal CLEAR", intent: goalTextClear},
		{name: "set clear prefix", text: "/goal clear the queue", intent: goalTextSet, objective: "clear the queue"},
		{name: "set folded", text: "/goal ship\tthe release", intent: goalTextSet, objective: "ship the release"},
		{name: "literal space delimiter", text: "/goal\tship", intent: goalTextNotCommand},
		// A provider reads the remainder of the LINE, so a second line belongs
		// to no command and must not enter the objective. Storing it would
		// state a goal longer than the one the provider installed, and no text
		// route reports the goal back to correct the difference.
		{
			name: "second line excluded", text: "/goal keep the build green\nand update the changelog",
			intent: goalTextSet, objective: "keep the build green",
		},
		// The same rule for a clear: the first line is the whole command.
		{name: "clear with a second line", text: "/goal off\nthen rest", intent: goalTextClear},
		// A leading blank line still leaves the command on the first line that
		// TrimSpace exposes.
		{name: "leading blank line", text: "\n\n/goal ship it", intent: goalTextSet, objective: "ship it"},
		// The command must not match a line further down the message: a user
		// who quotes a command in a paragraph is not issuing it.
		{name: "command on a later line", text: "look at this:\n/goal ship it", intent: goalTextNotCommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			intent, objective := GoalTextRoute{Command: "/goal", ClearArgs: clearArgs}.parse(test.text)
			assert.Equal(t, test.intent, intent)
			assert.Equal(t, test.objective, objective)
		})
	}
}

func TestGoalTextRouteWithoutClearArgumentsRejectsClear(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal"}
	outcome, err := route.Perform(agent.GoalActionClear, "")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	assert.Empty(t, outcome.QueuedInput)
}

type goalServicesRecorder struct {
	update agent.GoalUpdate
}

func (s *goalServicesRecorder) UpsertGoal(update agent.GoalUpdate) { s.update = update }

func (*goalServicesRecorder) UpdateGoalStatus(agent.GoalStatus, agent.GoalStatus) {}

func (*goalServicesRecorder) ClearGoal(bool) {}

func (*goalServicesRecorder) PublishGoalCapabilities() {}

func TestGoalTextRouteAcceptsOnlyGoalServices(t *testing.T) {
	t.Parallel()

	services := &goalServicesRecorder{}
	route := GoalTextRoute{Provider: "test", Command: "/goal", ClearArgs: []string{"clear"}}
	route.Observe(services, agent.GoalDeliverySend, "/goal Keep state coherent")
	assert.Equal(t, "Keep state coherent", services.update.Objective)
}

package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// A query word asks for the goal. An objective that equals one reaches the
// provider as the query, and no goal is set, so Set refuses it.
func TestGoalTextRouteRefusesAnObjectiveThatQueries(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal", ClearArgs: []string{"clear"}, QueryArgs: []string{"status"}}
	for _, objective := range []string{"status", "STATUS", "  status  "} {
		_, err := route.CommandText(agent.GoalActionSet, objective)
		assert.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, objective)
	}
	command, err := route.CommandText(agent.GoalActionSet, "status report for the release")
	assert.NoError(t, err)
	assert.Equal(t, "/goal status report for the release", command)
}

// The observer reads a delivered query as a query: the goal does not change.
func TestGoalTextRouteObservesAQueryAsNoChange(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal", QueryArgs: []string{"status"}}
	intent, objective := route.parse("/goal Status")
	assert.Equal(t, goalTextBareQuery, intent)
	assert.Empty(t, objective)

	services := &goalServicesRecorder{}
	route.Observe(services, agent.GoalDeliverySend, "/goal status")
	assert.Empty(t, services.update.Objective)
}

// A set verb states every objective, so an objective that starts with a verb or
// equals one still reaches the provider as an objective.
func TestGoalTextRouteWithASetVerbStatesEveryObjective(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal", SetVerb: "set", ClearArgs: []string{"clear"}, PauseArgs: []string{"pause"}}
	for objective, want := range map[string]string{
		"clear":               "/goal set clear",
		"pause the deploy":    "/goal set pause the deploy",
		"set up the CI":       "/goal set set up the CI",
		"  ship\nthe release": "/goal set ship the release",
	} {
		command, err := route.CommandText(agent.GoalActionSet, objective)
		assert.NoError(t, err, objective)
		assert.Equal(t, want, command)
	}
	_, err := route.CommandText(agent.GoalActionSet, "  ")
	assert.Error(t, err, "an empty objective is still refused")

	clear, err := route.CommandText(agent.GoalActionClear, "")
	assert.NoError(t, err)
	assert.Equal(t, "/goal clear", clear, "the other verbs keep their own form")
}

// The observer reads the set verb back, so what it stores is the objective the
// provider installed.
func TestGoalTextRouteParsesTheSetVerb(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal", SetVerb: "set", ClearArgs: []string{"clear"}}
	for text, want := range map[string]string{
		"/goal set clear":        "clear",
		"/goal SET ship it":      "ship it",
		"/goal ship it":          "ship it",
		"/goal settle the build": "settle the build",
	} {
		intent, objective := route.parse(text)
		assert.Equal(t, goalTextSet, intent, text)
		assert.Equal(t, want, objective, text)
	}
	intent, _ := route.parse("/goal clear")
	assert.Equal(t, goalTextClear, intent)
}

// Qwen Code's route states the set verb and takes the query words of its own
// report. With a set verb, an objective that equals a query word still reaches
// the provider as an objective, and the observer reads the bare query word as
// a query.
func TestGoalTextRouteWithASetVerbAndQueryWords(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal", SetVerb: "set", ClearArgs: []string{"clear"}, QueryArgs: []string{"status"}}

	command, err := route.CommandText(agent.GoalActionSet, "status")
	require.NoError(t, err, "the set verb makes every objective safe")
	assert.Equal(t, "/goal set status", command)

	intent, objective := route.parse(command)
	assert.Equal(t, goalTextSet, intent)
	assert.Equal(t, "status", objective)

	intent, objective = route.parse("/goal STATUS")
	assert.Equal(t, goalTextBareQuery, intent)
	assert.Empty(t, objective)
}

// The provider reads the first word as a verb, so the set verb with no
// objective after it installs nothing. The observer must not store the verb as
// the objective.
func TestGoalTextRouteReadsABareSetVerbAsNoChange(t *testing.T) {
	t.Parallel()
	route := GoalTextRoute{Provider: "test", Command: "/goal", SetVerb: "set", ClearArgs: []string{"clear"}}
	for _, text := range []string{"/goal set", "/goal SET", "/goal set   ", "/goal  set\nthe rest"} {
		intent, objective := route.parse(text)
		assert.Equal(t, goalTextBareQuery, intent, text)
		assert.Empty(t, objective, text)

		services := &goalServicesRecorder{}
		route.Observe(services, agent.GoalDeliverySend, text)
		assert.Empty(t, services.update.Objective, text)
	}

	// A route with no set verb takes "set" as a one-word objective, as before.
	plain := GoalTextRoute{Provider: "test", Command: "/goal", ClearArgs: []string{"clear"}}
	intent, objective := plain.parse("/goal set")
	assert.Equal(t, goalTextSet, intent)
	assert.Equal(t, "set", objective)
}

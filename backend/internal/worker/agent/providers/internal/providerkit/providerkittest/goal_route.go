package providerkittest

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertRefusesAnObjectiveThatClears pins that route refuses a one-word objective
// that equals one of its clear words. That objective would reach the provider as
// a clear. Without the refusal the RPC reports success and the card then shows
// no goal at all.
func AssertRefusesAnObjectiveThatClears(t *testing.T, route providerkit.GoalTextRoute) {
	t.Helper()
	for _, clearArg := range route.ClearArgs {
		// Upper case too: parseGoalCommandText folds case, so the provider reads
		// `OFF` as a clear exactly as it reads `off`.
		for _, objective := range []string{clearArg, strings.ToUpper(clearArg), " " + clearArg + " "} {
			_, err := route.CommandText(agent.GoalActionSet, objective)
			assert.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, objective)
		}
	}
	// A clear word that only STARTS the objective is a real objective.
	command, err := route.CommandText(agent.GoalActionSet, route.ClearArgs[0]+" the queue")
	require.NoError(t, err)
	assert.Equal(t, route.Command+" "+route.ClearArgs[0]+" the queue", command)
}

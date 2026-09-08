package agent

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goalAgentTypes is one zero value of every provider's agent type.
//
// A zero value is enough, because a type assertion asks about the TYPE. The
// test below is the whole reason this list exists: it is what stops the
// contract and the implementations from drifting apart.
var goalAgentTypes = map[leapmuxv1.AgentProvider]any{
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:    &ClaudeCodeAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:          &CodexAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:         &CursorCLIAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT: &CopilotCLIAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:           &KiloAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:       &OpenCodeAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:          &GooseCLIAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:             &PiAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX:       &ReasonixAgent{},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE:          &zcodeAgent{},
}

// The contract and the Go implementations must answer the same question.
//
// `supportsSessionGoal` in contracts/providers.json is what the BROWSER reads
// to decide whether the Goals & To-dos section offers a goal card at all. Go
// decides the same thing by which agent implements a goal interface. Nothing
// else compares the two, so a provider that gained or lost the feature could
// be goal-capable on one side only: the browser would draw a card no process
// can fill, or hide a feature the worker supports.
//
// Reasonix is the one provider that REPORTS a goal without being able to change
// it, so it is GoalCapable and not a GoalWriter. Both count as "has a session
// goal" for the contract, because the card shows a goal it cannot change.
func TestProviderSessionGoalContractMatchesGoalWriters(t *testing.T) {
	t.Parallel()

	require.Len(t, goalAgentTypes, len(contracts.AllProviders),
		"every provider needs an agent type here, or the check below skips it")

	for _, provider := range contracts.AllProviders {
		agent, ok := goalAgentTypes[provider]
		require.True(t, ok, "no agent type for %s", provider)

		_, capable := agent.(GoalCapable)
		want, declared := contracts.ProviderSupportsSessionGoal[provider]
		require.True(t, declared, "%s has no supportsSessionGoal entry", provider)
		assert.Equal(t, want, capable,
			"contracts/providers.json and the Go agent disagree for %s: "+
				"the contract says supportsSessionGoal=%v, and the agent %s GoalCapable",
			provider, want, map[bool]string{true: "implements", false: "does not implement"}[capable])
	}
}

// A provider the contract marks as goal-capable must either perform an action
// or honestly report an empty action list. Reasonix is the second case, and it
// is deliberate -- see reasonix_goal.go.
func TestProviderSessionGoalCapableProvidersAnswerAnActionList(t *testing.T) {
	t.Parallel()

	writers := 0
	for provider, agent := range goalAgentTypes {
		if !contracts.ProviderSupportsSessionGoal[provider] {
			_, capable := agent.(GoalCapable)
			assert.False(t, capable, "%s is not in the contract but answers a goal action list", provider)
			continue
		}
		if _, ok := agent.(GoalWriter); ok {
			writers++
		}
	}
	// Five write, and Reasonix is read-only. A change to this number is a real
	// change to what the browser can offer, so it is stated rather than derived.
	assert.Equal(t, 5, writers, "Claude Code, Codex, Copilot, Goose and ZCode write a goal")
}

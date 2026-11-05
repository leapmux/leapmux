package antigravity

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

var antigravityLocator = launch.Binaries("agy")

func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ANTIGRAVITY_CLI,
		Plugin:   antigravityProvider{}, Start: Start, Locator: antigravityLocator,
		DefaultModels: []*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the model that Antigravity selects")},
		OptionGroups:  []*leapmuxv1.AvailableOptionGroup{antigravityModeGroup("")},
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.AntigravityModeDefault},
			Fallback:   contracts.AntigravityModeDefault,
		},
		AdditionalOptionIDs: []string{agent.OptionIDEffort}, ManagesEffort: true, FixedPermissionModes: true,
		AgentDir:    ptrconv.Ptr(agentdir.Spec{Prefix: "antigravity"}),
		EnvModelKey: "LEAPMUX_ANTIGRAVITY_DEFAULT_MODEL", EnvEffortKey: "LEAPMUX_ANTIGRAVITY_DEFAULT_EFFORT",
	}
}

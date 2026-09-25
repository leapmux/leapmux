package opencode

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	PrimaryAgentBuild     = "build"
	PrimaryAgentPlan      = "plan"
	HiddenCompaction      = "compaction"
	openCodeHiddenTitle   = "title"
	openCodeHiddenSummary = "summary"
)

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: providerkit.TitleCaseID(PrimaryAgentBuild, "")},
		{Id: PrimaryAgentPlan, Name: providerkit.TitleCaseID(PrimaryAgentPlan, "")},
	}
}

// FamilyRegistrationSpec supplies the values that differ between OpenCode and Kilo.
type FamilyRegistrationSpec struct {
	Provider     leapmuxv1.AgentProvider
	Plugin       agent.Provider
	Start        agent.StartFunc
	Locator      launch.Locator
	OptionGroups []*leapmuxv1.AvailableOptionGroup
	EnvModelKey  string
	EnvEffortKey string
}

// FamilyRegistration builds one registration for an OpenCode family provider.
func FamilyRegistration(spec FamilyRegistrationSpec) agent.Registration {
	return agent.Registration{
		Provider: spec.Provider,
		Plugin:   spec.Plugin,
		Start:    spec.Start,
		Locator:  spec.Locator,
		// Models are discovered dynamically from newSession.
		DefaultModels:       nil,
		OptionGroups:        spec.OptionGroups,
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		EnvModelKey:         spec.EnvModelKey,
		EnvEffortKey:        spec.EnvEffortKey,
	}
}

// opencodeStaticOptionGroups holds OpenCode's static primary-agent group. The
// factory registration and Start both read this one value.
var opencodeStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPrimaryAgent, fallbackOpenCodePrimaryAgents())

// opencodeLocator finds the OpenCode CLI on the user's PATH.
var opencodeLocator = launch.Binaries("opencode")

// Registration states everything the worker knows about OpenCode before
// any of its agents runs.
func Registration() agent.Registration {
	return FamilyRegistration(FamilyRegistrationSpec{
		Provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Plugin:       opencodeProvider{},
		Start:        Start,
		Locator:      opencodeLocator,
		OptionGroups: opencodeStaticOptionGroups,
		EnvModelKey:  "LEAPMUX_OPENCODE_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_OPENCODE_DEFAULT_EFFORT",
	})
}

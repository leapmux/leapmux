package kilo

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

const PrimaryAgentCode = "code"

func fallbackKiloPrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentCode, Name: providerkit.TitleCaseID(PrimaryAgentCode, "")},
		{Id: opencode.PrimaryAgentPlan, Name: providerkit.TitleCaseID(opencode.PrimaryAgentPlan, "")},
	}
}

// kiloStaticOptionGroups holds Kilo's static primary-agent group. Registration
// gives this group to both the registry and the ACP start.
var kiloStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPrimaryAgent, fallbackKiloPrimaryAgents())

// kiloLocator finds the Kilo CLI on the user's PATH.
var kiloLocator = launch.Binaries("kilo")

// Registration states everything the worker knows about Kilo before any of
// its agents runs. Kilo shares OpenCode's registration shape.
func Registration() agent.Registration {
	return opencode.FamilyRegistration(opencode.FamilyRegistrationSpec{
		Provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		Plugin:       kiloProvider{},
		Start:        Start,
		Locator:      kiloLocator,
		OptionGroups: kiloStaticOptionGroups,
		EnvModelKey:  "LEAPMUX_KILO_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_KILO_DEFAULT_EFFORT",
	})
}

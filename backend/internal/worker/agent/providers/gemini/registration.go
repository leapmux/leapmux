package gemini

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var geminiStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, geminiModes())

// Registration supplies Gemini CLI's launch metadata and static mode catalog.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		Plugin:       geminiProvider{},
		Start:        Start,
		Locator:      launch.Binaries("gemini"),
		OptionGroups: geminiStaticOptionGroups,
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.GeminiModeDefault},
			Fallback:   contracts.GeminiModeDefault,
		},
		EnvModelKey: "LEAPMUX_GEMINI_DEFAULT_MODEL",
	}
}

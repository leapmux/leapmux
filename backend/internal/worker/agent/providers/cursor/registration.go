package cursor

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const (
	ModeAgent = "agent"
	ModePlan  = "plan"
	ModeAsk   = "ask"

	cursorCLIModelAuto     = "auto"
	cursorCLIModelAutoWire = "default"
)

func fallbackCursorCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: ModeAgent, Name: "Agent"},
		{Id: ModePlan, Name: "Plan"},
		{Id: ModeAsk, Name: "Ask"},
	}
}

var cursorCLIAvailableModels = []*agent.ModelInfo{
	{Id: cursorCLIModelAuto, DisplayName: "Auto", Description: "Automatically selects the best available Cursor model", IsDefault: true},
}

var bypassPermissionsGroup = agent.SelectGroup(contracts.CursorOptionBypassPermissions, "Bypass Permissions", agent.OptionOrderProviderFirst, "false", []agent.OptionDef{
	{Id: "false", Name: "Off", Description: "Use Cursor's configured permission rules", Default: true},
	{Id: "true", Name: "On", Description: "Restart in native Run Everything mode. Turning it off requires a new session. Explicit denials and team rules still apply."},
})

// cursorStaticOptionGroups holds Cursor's native mode and launch-policy groups. The
// factory registration and Start both read this one value, so the
// static fallback and a running agent's fallback modes cannot drift.
var cursorStaticOptionGroups = append(acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, fallbackCursorCLIModes()), bypassPermissionsGroup)

// cursorLocator finds the Cursor CLI on the user's PATH.
var cursorLocator = launch.Binaries("cursor-agent")

// Registration states everything the worker knows about Cursor before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:         leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		Plugin:           cursorProvider{},
		Start:            Start,
		Locator:          cursorLocator,
		DefaultModels:    cursorCLIAvailableModels,
		OptionGroups:     cursorStaticOptionGroups,
		NormalizeModelID: normalizeCursorModelID,
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: ModeAgent,
		},
		EnvModelKey: "LEAPMUX_CURSOR_DEFAULT_MODEL",
	}
}

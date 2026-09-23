package cursor

import (
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
	cursorCLIModelAutoWire = "default[]"
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

// cursorStaticOptionGroups holds Cursor's static permission-mode group. The
// factory registration and Start both read this one value, so the
// static fallback and a running agent's fallback modes cannot drift.
var cursorStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, fallbackCursorCLIModes())

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

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

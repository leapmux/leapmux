package claude

import (
	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// claudeStaticOptionGroups holds Claude Code's option groups that do not depend
// on the model catalog: the permission mode. The factory registration and the
// running agent both read this one value.
var claudeStaticOptionGroups = []*leapmuxv1.AvailableOptionGroup{{
	Id:           agent.OptionIDPermissionMode,
	Label:        "Permission Mode",
	DefaultValue: contracts.ClaudeModeDefault,
	Mutable:      true,
	Order:        agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{
			Id:          contracts.ClaudeModeDefault,
			Name:        "Default",
			Description: "Standard behavior, prompts for dangerous operations.",
		},
		{
			Id:          contracts.ClaudeModePlan,
			Name:        "Plan Mode",
			Description: "Planning mode, no actual tool execution.",
		},
		{
			Id:          contracts.ClaudeModeAcceptEdits,
			Name:        "Accept Edits",
			Description: "Auto-accept file edit operations.",
		},
		{
			Id:          contracts.ClaudeModeBypassPermissions,
			Name:        "Bypass Permissions",
			Description: "Bypass all permission checks (requires allowDangerouslySkipPermissions).",
		},
		{
			Id:          contracts.ClaudeModeDontAsk,
			Name:        "Don't Ask",
			Description: "Don't prompt for permissions, deny if not pre-approved.",
		},
		{
			Id:          contracts.ClaudeModeAuto,
			Name:        "Auto Mode",
			Description: "Uses an AI classifier to auto-approve safe tool calls and falls back to prompting for risky ones.",
		},
	},
}}

// claudeLocator finds the Claude Code CLI on the user's PATH.
var claudeLocator = launch.Binaries("claude")

// Registration states everything the worker knows about Claude Code before
// any of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Plugin:        claudeProvider{},
		Start:         Start,
		Locator:       claudeLocator,
		DefaultModels: claudeCodeAvailableModels,
		OptionGroups:  claudeStaticOptionGroups,
		// Each Claude model carries its effort AND extended-thinking groups, so the
		// frontend rebuilds both on a model switch (the static fallback needs this
		// too, hence the registration rather than only Claude.OptionGroups).
		ModelSubGroups:   claudeModelSubGroups,
		NormalizeModelID: normalizeClaudeCodeModel,
		// model + permissionMode (static group) + effort (built from the model catalog).
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		PermissionDefaults: agent.PermissionDefaults{
			// A new session asks for Auto Mode; a CLI that cannot enter it degrades to
			// Default at startup, which is also where a resumed session with no stored mode
			// lands.
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeAuto},
			Fallback:   contracts.ClaudeModeDefault,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_CLAUDE_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_CLAUDE_DEFAULT_EFFORT",
	}
}

package qwen

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// qwenStaticOptionGroups holds Qwen's static approval-mode group. The
// registration and the start both read this one value, so the static fallback
// and a running agent's fallback modes cannot drift.
var qwenStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, qwenModes())

// qwenLocator finds the Qwen Code CLI on the user's PATH.
var qwenLocator = launch.Binaries("qwen")

// Registration states everything the worker knows about Qwen Code before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
		Plugin:   qwenProvider{},
		Start:    Start,
		Locator:  qwenLocator,
		// Qwen lists its models from the user's settings and environment, so the
		// session is the only source of the catalog.
		DefaultModels: nil,
		OptionGroups:  qwenStaticOptionGroups,
		// The reasoning effort is a server-driven config option whose menu belongs
		// to the model, so it is known before the session reports it.
		AdditionalOptionIDs: []string{contracts.QwenConfigReasoningEffort},
		PermissionDefaults: agent.PermissionDefaults{
			// Both halves are Default. Qwen's own default is `auto`, whose classifier
			// approves actions with no prompt, so neither a new nor a resumed
			// session may inherit it.
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.QwenModeDefault},
			Fallback:   contracts.QwenModeDefault,
		},
		EnvModelKey:  "LEAPMUX_QWEN_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_QWEN_DEFAULT_EFFORT",
	}
}

package goose

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Goose exposes these server-driven option IDs before the daemon starts.
const (
	ConfigThinkingEffort = "thinking_effort"
	ConfigProvider       = "provider"
)

// fallbackGooseCLIModes lists Goose's modes in Goose's own order, then applies the same
// preferred-first rule the live catalog applies. Ordering here rather than hand-writing
// the result keeps the static fallback and every rebuilt list in agreement.
func fallbackGooseCLIModes() []*leapmuxv1.AvailableOption {
	modes := []*leapmuxv1.AvailableOption{
		{Id: contracts.GooseModeAuto, Name: "Auto"},
		{Id: contracts.GooseModeApprove, Name: "Approve"},
		{Id: contracts.GooseModeSmartApprove, Name: "Smart Approve"},
		{Id: contracts.GooseModeChat, Name: "Chat"},
	}
	acp.OrderModesPreferredFirst(modes, contracts.GooseModeSmartApprove)
	return modes
}

// gooseStaticOptionGroups holds Goose's static permission-mode group. The
// factory registration and Start both read this one value.
var gooseStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, fallbackGooseCLIModes())

// gooseLocator finds the Goose CLI on the user's PATH.
var gooseLocator = launch.Binaries("goose")

// Registration states everything the worker knows about Goose before any of
// its agents runs: a permission-mode ACP provider whose reasoning axis is a
// server-driven config option rather than the well-known "effort" id.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		Plugin:   gooseProvider{},
		Start:    Start,
		Locator:  gooseLocator,
		// Models are discovered dynamically from session/new.
		DefaultModels: nil,
		// model + permissionMode (static group) + Goose's server-driven config options.
		OptionGroups:        gooseStaticOptionGroups,
		AdditionalOptionIDs: []string{ConfigThinkingEffort, ConfigProvider},
		PermissionDefaults: agent.PermissionDefaults{
			// Both halves are Smart Approve. The fallback must NOT be Goose's own `auto`,
			// which is the mode its bypass shortcut selects: a resumed session with no stored
			// mode would then open with every permission prompt disabled.
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.GooseDefaultMode},
			Fallback:   contracts.GooseDefaultMode,
		},
		EnvModelKey: "LEAPMUX_GOOSE_DEFAULT_MODEL",
		// No well-known effort axis: reasoning is the server-driven thinking_effort
		// config option, so no default-effort variable maps onto it.
		EnvEffortKey: "",
	}
}

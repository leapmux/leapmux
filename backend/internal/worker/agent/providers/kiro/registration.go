package kiro

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// kiroBinary is the program that serves Kiro's Agent Client Protocol.
//
// It is `kiro-cli-chat` and not the `kiro-cli` launcher. The launcher starts
// `$HOME/.local/bin/kiro-cli-chat` as a child, so it fails under any HOME but
// the user's own, and a SIGTERM stops the launcher alone: its child and the
// engine below it keep running. Every Kiro install puts `kiro-cli-chat` in
// `~/.local/bin` beside the launcher, because the launcher runs it from there.
const kiroBinary = "kiro-cli-chat"

// kiroStaticOptionGroups holds Kiro's static mode group and the policy group.
// The registration and the start both read this one value, so the static
// fallback and a running agent's fallback modes cannot drift.
var kiroStaticOptionGroups = append(
	acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, kiroModes()),
	policyPresetGroup(""),
)

// kiroLocator finds the Kiro CLI on the user's PATH.
var kiroLocator = launch.Binaries(kiroBinary)

// Registration states everything the worker knows about Kiro before any of its
// agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
		Plugin:   kiroProvider{},
		Start:    Start,
		Locator:  kiroLocator,
		// Kiro lists its models from the account's catalog, so the session is
		// the only source of the catalog.
		DefaultModels: nil,
		OptionGroups:  kiroStaticOptionGroups,
		// The server-driven config options that the session surfaces as option
		// groups: the effort and the thinking switch belong to the model, and
		// autopilot and content collection belong to the session.
		AdditionalOptionIDs: []string{
			contracts.KiroConfigEffortLevel,
			kiroConfigThinking,
			kiroConfigAutopilot,
			kiroConfigContentCollection,
		},
		// The policy starts at the safe value for every agent. A stored value
		// that this build does not know falls back to the same value at the
		// start.
		ProviderOptionDefaults: map[string]string{contracts.KiroOptionPolicyPreset: kiroPolicyAsk},
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.KiroModeDefault},
			Fallback:   contracts.KiroModeDefault,
		},
		EnvModelKey:  "LEAPMUX_KIRO_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_KIRO_DEFAULT_EFFORT",
	}
}

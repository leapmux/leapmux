package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const KiloPrimaryAgentCode = "code"

// KiloAgent manages a single Kilo ACP process.
type KiloAgent struct {
	acpBase
}

// StartKilo starts a Kilo ACP agent process and performs the handshake.
func StartKilo(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[KiloAgent]{
		providerName:   "kilo",
		binaryName:     "kilo",
		baseArgs:       []string{"acp"},
		rcMarkerEnvKey: "KILO_CLIENT",
		sessionConfig:  acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume},
		newAgent:       func() *KiloAgent { return &KiloAgent{} },
		base:           func(a *KiloAgent) *acpBase { return &a.acpBase },
		configure: func(a *KiloAgent) {
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			a.reapplySettings = a.reapplyModelAndPrimaryAgent
			a.refreshFromSession = a.refreshModelAndPrimaryAgentFromSession
		},
		afterHandshake: func(a *KiloAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPrimaryAgentStartup(handshake, opts, fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode, a.setModel)
		},
	})
}

func fallbackKiloPrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: titleCaseID(KiloPrimaryAgentCode, ""), IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
	}
}

func (a *KiloAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.primaryAgentOptionGroups(fallbackKiloPrimaryAgents())
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartKilo(ctx, opts, sink)
		},
		nil, // models discovered dynamically from newSession
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     OptionGroupKeyPrimaryAgent,
			Label:   "Primary Agent",
			Options: fallbackKiloPrimaryAgents(),
		}},
		"LEAPMUX_KILO_DEFAULT_MODEL",
		"LEAPMUX_KILO_DEFAULT_EFFORT",
		"kilo",
	)
}

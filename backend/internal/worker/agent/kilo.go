package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const KiloPrimaryAgentCode = "code"

// KiloAgent manages a single Kilo ACP process.
type KiloAgent struct {
	openCodeFamilyBase
}

// StartKilo starts a Kilo ACP agent process and performs the handshake.
func StartKilo(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[KiloAgent]{
		provider:       leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		providerName:   "kilo",
		binaryName:     "kilo",
		baseArgs:       openCodeACPArgs(),
		rcMarkerEnvKey: "KILO_CLIENT",
		// Kilo forks OpenCode and requires its own spelling of the flag for the same
		// tool; see openCodeQuestionToolEnv. Its allowed client list adds `vscode`, and
		// its own `kilo acp` handler assigns `KILO_CLIENT="acp"` the same way, so the
		// flag is the only route here too.
		pinnedEnv:     []string{kiloQuestionToolEnv + "=1"},
		sessionConfig: acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume},
		newAgent:      func() *KiloAgent { return &KiloAgent{} },
		base:          func(a *KiloAgent) *acpBase { return &a.acpBase },
		configure: func(a *KiloAgent) {
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			a.questions.configure(sink)
			// Kilo uses the same task protocol as OpenCode. Its prompt and final
			// result form the child transcript.
			a.subagentFromToolCall = openCodeSubagentFromToolCall
			a.subagentFromToolCallUpdate = openCodeSubagentFromToolCallUpdate
		},
		afterHandshake: func(a *KiloAgent, handshake *acpSessionResult, opts Options) error {
			// The launched process is the shell, which a POSIX shell replaces with the
			// daemon and PowerShell does not. Discovery walks below it for that case.
			a.questions.begin(a.ctx, a.agentID, a.cmd.Process.Pid)
			return a.applyPrimaryAgentStartup(handshake, opts, KiloPrimaryAgentCode)
		},
	})
}

func fallbackKiloPrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: titleCaseID(KiloPrimaryAgentCode, "")},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
	}
}

func init() {
	registerOpenCodeFamilyProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		StartKilo,
		fallbackKiloPrimaryAgents(),
		"LEAPMUX_KILO_DEFAULT_MODEL", "LEAPMUX_KILO_DEFAULT_EFFORT", "kilo",
	)
}

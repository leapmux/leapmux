package agent

import (
	"context"
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
)

const KiloPrimaryAgentCode = "code"

// KiloAgent manages a single Kilo ACP process.
type KiloAgent struct {
	acpBase
}

// StartKilo starts a Kilo ACP agent process and performs the handshake.
func StartKilo(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		BinaryName:   "kilo",
		StripEnvKeys: []string{"KILO_CLIENT"},
		BaseArgs:     []string{"acp"},
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "KILO_CLIENT")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "KILO_CLIENT=1")
	}
	cmd.Env = FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &KiloAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "kilo", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
			sink:        sink,
			model:       opts.Model,
		},
	}
	a.modeChannel = modeChannelPrimaryAgent
	a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPrimaryAgent
	a.refreshFromSession = a.refreshModelAndPrimaryAgentFromSession

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := acpStandardInitParams()
	if err != nil {
		return nil, err
	}
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams,
		acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume})
	if err != nil {
		return nil, err
	}

	if err := a.applyPrimaryAgentStartup(handshake, opts, fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode, a.setModel); err != nil {
		return nil, err
	}

	return a, nil
}

func fallbackKiloPrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: titleCaseID(KiloPrimaryAgentCode, ""), IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
	}
}

func (a *KiloAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
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

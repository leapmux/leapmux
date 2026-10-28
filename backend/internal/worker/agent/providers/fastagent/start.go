package fastagent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// Start starts a fast-agent ACP process and performs the handshake.
//
// The launch takes `--no-update-check acp --model <model> -x --subagents`: the
// root option that turns off the PyPI release check, the ACP server entry, the
// model string the session runs, the local shell runtime that exposes coding
// tools, and the subagent tools. The option must come before `acp`, which swallows
// an unknown option that follows it. Unless --no-permissions is selected,
// Fast Agent applies its native tool permission rules and prompts to host tools.
// ACP exposes no model setter. Native commands can switch the running model.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	bypass, err := opts.Options.Bool(contracts.FastagentOptionBypassPermissions)
	if err != nil {
		return nil, err
	}
	nativeCommands, err := opts.Options.Bool(OptionNativeCommands)
	if err != nil {
		return nil, err
	}
	if nativeCommands && !bypass {
		return nil, errors.New("fastagent Native Commands requires Bypass Permissions because native commands do not request approval")
	}
	registration := Registration()
	model := opts.Model()
	if model == "" {
		model = registration.DefaultModel()
	}
	args := []string{"--no-update-check", "acp", "--model", model, "-x", "--subagents"}
	if bypass {
		args = append(args, "--no-permissions")
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: registration,
		ProviderName: "fastagent",
		BaseArgs:     args,
		NewAgent: func() *Agent {
			return &Agent{bypassPermissions: bypass, nativeCommands: nativeCommands, workingDir: opts.WorkingDir, home: fastagentHome(agent.StoredSessionQuery{WorkingDir: opts.WorkingDir, HomeDir: opts.HomeDir}), clock: quartz.NewReal()}
		},
		Base: func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			return a.hooks(model, sink)
		},
		// The session's `modes` channel carries the one configured agent mode;
		// applyHandshakeMode stores it on the permission-mode axis, and the
		// requested model is pushed last and best-effort (fast-agent fixes its
		// model at session creation and rejects a live write).
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			if err := a.initializeRootArchive(opts.ResumeSessionID != ""); err != nil {
				return err
			}
			if err := a.ApplyPermissionModeStartup(handshake, opts, contracts.FastagentDefaultMode, opts.Model()); err != nil {
				return err
			}
			return nil
		},
	})
}

func (a *Agent) hooks(model string, sink agent.ProviderServices) acp.Hooks {
	// Fast Agent raises no steer method. Its mode list arrives in the session
	// response, and its usage status arrives on the outer notification metadata.
	return acp.Hooks{
		LocalOptionGroups: a.localOptionGroups, InitialModel: model,
		ModeChannel:                acp.ModeChannelUnmapped,
		SubagentFromToolCall:       a.subagentFromToolCall,
		SubagentFromToolCallUpdate: a.subagentFromToolCallUpdate,
		ChildUpdateRoute:           a.childUpdateRoute,
		ChildUserMessages:          true,
		ClearProviderState:         a.clearProviderState,
		BeforeWaitCleanup:          a.finishChildArchiveReads,
		AfterWaitCleanup:           a.finishArchiveReads,
		DisableHostTerminal:        a.nativeCommands,
		SessionUpdateHandler:       a.observePromptUpdate,
		PreparePrompt:              a.prepareAudioPrompt,
		OpenRootTranscript:         a.openRootTranscript,
		PromptEnded:                a.retryChildArchives,
		SessionNotificationMetadata: func(_ string, metadata map[string]json.RawMessage) {
			broadcastFastagentStatusLine(sink, metadata)
		},
	}
}

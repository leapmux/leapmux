package gemini

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// Start opens Gemini CLI's native ACP session and recovers omitted tool records.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	args := []string{"--acp", "--approval-mode", "default"}
	if model := opts.Model(); model != "" {
		args = append(args, "--model", model)
	}
	debugLog, err := newGeminiDebugLog()
	if err != nil {
		return nil, err
	}
	started, err := acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "gemini",
		BaseArgs:     args,
		// Gemini's relauncher adds a process and swallows stop signals.
		// The Worker must own the process that serves ACP. Gemini states its
		// own retry waits only in its debug log (geminiDebugLog).
		PinnedEnv: []string{"GEMINI_CLI_NO_RELAUNCH=1", debugLog.env()},
		NewAgent:  func() *Agent { return &Agent{debugLog: debugLog} },
		Base:      func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, services agent.ProviderServices) acp.Hooks {
			return a.configure(ctx, opts, services)
		},
		AfterHandshake: func(a *Agent, result *acp.SessionResult, opts agent.Options) error {
			a.debugLog.follow(a.Clock(), a.observeGeminiDebugLog)
			return a.ApplyPermissionModeStartup(result, opts, contracts.GeminiModeDefault, opts.Model())
		},
	})
	if err != nil {
		// A start that fails before the base installs its hooks runs no wait
		// cleanup, so the log closes here too. A second close does nothing.
		debugLog.close()
		return nil, err
	}
	return started, nil
}

// configure returns the hooks of one agent, the ones that Start installs.
func (a *Agent) configure(ctx context.Context, opts agent.Options, services agent.ProviderServices) acp.Hooks {
	query := agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir}
	controls := &geminiControlServices{ProviderServices: services, query: query, currentSession: a.CurrentSessionID}
	transcript := newGeminiToolTranscript(ctx, &geminiOutputServices{
		ProviderServices: controls,
		query:            query,
		currentSession:   a.CurrentSessionID,
		onSession:        func(sessionID string) { a.startChildTranscript(services, query, sessionID) },
	}, query, a.observeNativeMode)
	controls.transcript = transcript
	return acp.Hooks{
		WithBypassPermission:   func(claim func()) { a.WithOptionValue(agent.OptionIDPermissionMode, contracts.GeminiModeYolo, claim) },
		PermissionAllowsBypass: permissionAllowsBypass,
		// Gemini resumes the model after a cancelled permission answer.
		// Stop the prompt before the answer releases its permission wait.
		CancelBeforeControlWithdrawal: true,
		Sink:                          transcript,
		InitialModel:                  opts.Model(),
		ModelSetter:                   a.setNativeModel,
		ModeChannel:                   acp.ModeChannelPermissionMode,
		ModeSetter:                    a.setNativePermissionMode,
		SessionUpdateHandler:          a.handleSessionUpdate,
		ClearProviderState: func() {
			a.stopChildTranscript(agent.MessageCompletionInterrupted)
			a.resetNativeModes()
			transcript.Reset()
			a.clearGeminiRetryState()
		},
		// The JSON-RPC error of a failed prompt decides the retry verdict of
		// the turn, unless LeapMux stopped the turn for a long retry wait
		// that Gemini states in its debug log. A new prompt forgets both
		// records.
		PromptPrepared: a.clearGeminiRetryState,
		PromptEnded:    a.finishGeminiPrompt,
		BeforeWaitCleanup: func() {
			// The follower stops before anything else ends, so no stop for a
			// long wait reaches the exited process.
			a.closeDebugLog()
			completion := agent.MessageCompletionError
			if a.IntentionalStopRequested() {
				completion = agent.MessageCompletionInterrupted
			}
			a.stopChildTranscript(completion)
		},
		// Gemini CLI 0.65.0 cannot create a file through the host
		// filesystem. Its ACP filesystem service maps a failed
		// fs/read_text_file to ENOENT only when the rejection is an Error
		// whose message says so, but its ACP SDK rejects with the plain
		// JSON-RPC error object. write_file then takes every missing file
		// for one that exists and cannot be read. Without the capability,
		// Gemini reads and writes the working tree itself.
		DisableHostFileSystem: true,
	}
}

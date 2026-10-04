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
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "gemini",
		BaseArgs:     args,
		// Gemini's relauncher adds a process and swallows stop signals.
		// The Worker must own the process that serves ACP.
		PinnedEnv: []string{"GEMINI_CLI_NO_RELAUNCH=1"},
		NewAgent:  func() *Agent { return &Agent{} },
		Base:      func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, services agent.ProviderServices) acp.Hooks {
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
				Sink:                 transcript,
				InitialModel:         opts.Model(),
				ModelSetter:          a.setNativeModel,
				ModeChannel:          acp.ModeChannelPermissionMode,
				ModeSetter:           a.setNativePermissionMode,
				SessionUpdateHandler: a.handleSessionUpdate,
				ClearProviderState: func() {
					a.stopChildTranscript(agent.MessageCompletionInterrupted)
					a.resetNativeModes()
					transcript.Reset()
				},
				PromptEnded: func(_ error, stopped bool) {
					completion := agent.MessageCompletionComplete
					if stopped {
						completion = agent.MessageCompletionInterrupted
					}
					a.finishNativeChildren(completion)
				},
				BeforeWaitCleanup: func() {
					completion := agent.MessageCompletionError
					if a.IntentionalStopRequested() {
						completion = agent.MessageCompletionInterrupted
					}
					a.stopChildTranscript(completion)
				},
			}
		},
		AfterHandshake: func(a *Agent, result *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(result, opts, contracts.GeminiModeDefault, opts.Model())
		},
	})
}

package ohmypi

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// terminalIdentityEnvKeys are the variables omp keys its `--continue` breadcrumb
// on (`terminal-sessions/<terminal id>`, from `tui/src/ttyid.ts`). A worker that
// runs in a terminal passes its own, and omp would then record a LeapMux session
// as the last session of that terminal: the user's next `omp --continue` there
// would reopen it. The worker resumes by explicit handle and needs none of them.
var terminalIdentityEnvKeys = []string{
	"ZELLIJ_PANE_ID", "TMUX_PANE", "CMUX_SURFACE_ID", "KITTY_WINDOW_ID",
	"WEZTERM_PANE", "TERM_SESSION_ID", "WT_SESSION",
}

// approvalModes lists omp's three tool approval modes, which approvalModeGroup
// offers.
var approvalModes = []string{
	contracts.OhMyPiApprovalModeAlwaysAsk,
	contracts.OhMyPiApprovalModeWrite,
	contracts.OhMyPiApprovalModeYolo,
}

// launchApprovalMode is the approval mode an agent launches with: the one its
// options state, else the fallback. omp runs its configured mode for a value it
// does not know -- Yolo unless the user changed it -- so no unknown value reaches
// the flag.
func launchApprovalMode(opts agent.Options) string {
	if mode := opts.PermissionMode(); slices.Contains(approvalModes, mode) {
		return mode
	}
	return Registration().PermissionDefaults.Fallback
}

// launchEffort is the thinking level an agent launches with, or agent.EffortAuto
// for omp's configured level.
func launchEffort(opts agent.Options) string {
	if effort := opts.Effort(); effort != "" {
		return effort
	}
	return agent.EffortAuto
}

// launchArgs builds omp's arguments.
//
//   - `--mode rpc-ui` is RPC with a UI context for tools. Only that mode gives the
//     model the `ask` tool.
//   - `--cwd` is ALWAYS passed. Without it, omp moves itself to a temporary
//     directory when it starts in the home directory.
//   - `--model` and `--thinking` select what the options state, and are left out
//     for omp's configured default.
//   - `--approval-mode` is always passed: the approval mode takes effect at launch
//     only, and LeapMux states it for every session.
//   - `--resume` reopens a stored session; see resumeArgs.
func launchArgs(opts agent.Options, resume []string) []string {
	args := []string{"--mode", "rpc-ui", "--cwd", opts.WorkingDir}
	if model := opts.Model(); model != "" && model != agent.DefaultModelSentinel {
		args = append(args, "--model", model)
	}
	if effort := launchEffort(opts); effort != agent.EffortAuto {
		args = append(args, "--thinking", effort)
	}
	args = append(args, "--approval-mode", launchApprovalMode(opts))
	return append(args, resume...)
}

var _ agent.StartFunc = Start

// Start starts an `omp --mode rpc-ui` process and runs the startup handshake.
//
// A resume that omp cannot honor fails the WHOLE start, and that is deliberate:
// `--resume` is on the launch line, so omp settles the session before it enters
// RPC mode, and it exits when no session matches the handle or when the session's
// recorded directory is gone. providerkit.ResumeFailedError tells the user to send
// `/clear` then.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}
	resume, err := resumeArgs(opts.ResumeSessionID, opts.HomeDir)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: terminalIdentityEnvKeys,
		BaseArgs:     launchArgs(opts, resume),
		WorkingDir:   opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		Process:       providerkit.NewProcess(opts, "omp", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:          agent.NewModelProgressResetSink(sink),
		workingDir:    opts.WorkingDir,
		ready:         make(chan readyFrame, 1),
		model:         opts.Model(),
		thinkingLevel: launchEffort(opts),
		approvalMode:  launchApprovalMode(opts),
	}
	a.startupSnapshot.Store(true)

	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.DrainStderr(stderrPipe)
	go a.ReadOutput(agent.NewStdoutScanner(stdout), a.interceptFrame, a.handleFrame)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	if err := a.handshake(opts.EffectiveStartupTimeout()); err != nil {
		cleanup()
		return nil, err
	}

	a.Mu.Lock()
	handle := a.sessionHandleLocked()
	a.Mu.Unlock()
	a.sink.UpdateSessionID(handle)
	a.sink.BroadcastStatusActive(handle)
	a.startupSnapshot.Store(false)
	a.refreshSessionStatsAsync()
	return a, nil
}

// handshake waits for the ready frame, negotiates the protocol, subscribes to the
// subagent events, and reads the session and the model catalog.
func (a *Agent) handshake(timeout time.Duration) error {
	ready, err := a.awaitReady(timeout)
	if err != nil {
		return a.FormatStartupError("ready", err)
	}

	// Protocol version 1 shrinks a frame above 1 MiB by eliding strings, which
	// loses data. Version 2 splits it into rpc_chunk frames.
	if ready.supports(rpcProtocolVersion) {
		if _, err := a.sendCommand(CommandNegotiateProtocol, map[string]any{"protocolVersion": rpcProtocolVersion}, timeout); err != nil {
			slog.Warn("omp protocol negotiation failed; large frames arrive shortened", "agent_id", a.AgentID(), "error", err)
		}
	} else {
		slog.Warn("omp offers no split frames; large frames arrive shortened", "agent_id", a.AgentID(), "versions", ready.SupportedProtocolVersions)
	}

	// Without the subscription omp sends no subagent frame at all: a subagent
	// then has no registry row and no transcript, and its result reaches the
	// parent as text alone.
	if _, err := a.sendCommand(CommandSetSubagentSubscription, map[string]any{"level": subagentSubscriptionEvents}, timeout); err != nil {
		slog.Warn("omp subagent subscription failed; subagents get no transcript", "agent_id", a.AgentID(), "error", err)
	}

	state, err := a.sendCommand(CommandGetState, nil, timeout)
	if err != nil {
		return a.FormatStartupError(CommandGetState, err)
	}
	a.applyState(state)

	// Best effort: the catalog fills the model picker, and the running model
	// reads back without it. omp waits for its model discovery before it answers.
	if models, err := a.sendCommand(CommandGetAvailableModels, nil, timeout); err != nil {
		slog.Warn("omp get_available_models failed", "agent_id", a.AgentID(), "error", err)
	} else {
		a.applyAvailableModels(models)
	}
	return nil
}

// awaitReady waits for omp's ready frame, the first frame of RPC mode. omp exits
// before it when the start fails -- an unknown model, a session that is not found
// -- and the exit is the answer then.
func (a *Agent) awaitReady(timeout time.Duration) (readyFrame, error) {
	timer := a.Clock().NewTimer(timeout, ompReadyTimerTag)
	defer timer.Stop(ompReadyTimerTag)
	select {
	case ready := <-a.ready:
		return ready, nil
	case <-a.ProcessDone():
		return readyFrame{}, a.ProcessExitError()
	case <-a.Context().Done():
		return readyFrame{}, a.Context().Err()
	case <-timer.C:
		return readyFrame{}, fmt.Errorf("omp sent no ready frame within %s", timeout)
	}
}

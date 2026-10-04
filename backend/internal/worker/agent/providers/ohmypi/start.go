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

// terminalIdentityEnvKeys identify the shell terminal for omp's continue record.
// Native tui/src/ttyid.ts stores that record under terminal-sessions/<terminal id>.
// Inherited values would make the user's next omp --continue reopen a LeapMux session.
// The Worker uses explicit resume handles, so its agent needs none of these values.
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

// launchApprovalMode selects the configured supported mode or LeapMux's fallback.
// omp uses its own configured mode for unknown values, which defaults to Yolo.
// Replace unknown options with LeapMux's fallback before the native flag receives a value.
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

// launchArgs builds omp's native arguments.
//
//   - --mode rpc-ui supplies the UI context that enables the ask tool.
//   - --cwd prevents omp from moving to a temporary directory when it starts in HOME.
//   - --model and --thinking select explicit options. Omit them for native defaults.
//   - --approval-mode always selects the session's mode because omp applies it at launch only.
//   - --resume reopens a stored session. resumeArgs supplies its validated handle.
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
// A native resume failure ends startup because omp resolves --resume before it enters RPC mode.
// omp exits when the handle matches no session or the recorded directory is absent.
// providerkit.ResumeFailedError tells the user to send /clear in that case.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	registration := Registration()
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, registration)
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

	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	a := &Agent{
		Process:       providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "omp", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel),
		sink:          agent.NewModelProgressResetSink(sink),
		workingDir:    opts.WorkingDir,
		ready:         make(chan readyFrame, 1),
		model:         opts.Model(),
		thinkingLevel: launchEffort(opts),
		approvalMode:  launchApprovalMode(opts),
	}
	a.startupSnapshot.Store(true)

	if err := a.StartCmd(); err != nil {
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

// handshake performs the native startup steps in order:
//   - Wait for the ready frame and negotiate the protocol.
//   - Subscribe to subagent events.
//   - Read the session and model catalog.
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

	// A catalog failure leaves the current model readable but the picker unpopulated.
	// omp waits for native model discovery before it returns the catalog.
	if models, err := a.sendCommand(CommandGetAvailableModels, nil, timeout); err != nil {
		slog.Warn("omp get_available_models failed", "agent_id", a.AgentID(), "error", err)
	} else {
		a.applyAvailableModels(models)
	}
	return nil
}

// awaitReady waits for the first RPC frame or the process exit that reports a startup failure.
// An unknown model or an absent resumed session can cause that exit.
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

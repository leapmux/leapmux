package pi

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piResumeArgs builds the `--session` argument that reopens a prior Pi session,
// or nothing when there is no handle to reopen.
//
// Resume happens at LAUNCH and not through a switch_session RPC after startup,
// for two reasons. `--session` reaches Pi's own resolver, which takes either
// shape of handle -- a session file path, or a bare session ID that it matches
// against the sessions of this working directory -- while the RPC takes a path
// and nothing else. And the RPC does not fail on a path that identifies no file: Pi
// starts an EMPTY session at that path and answers success, so a handle that
// identified no session became a new file in the working directory and looked like a
// resume.
//
// The value this reads is NOT the one OpenAgent validated.
// agentOutputSink.UpdateSessionID writes whatever Pi reports into the
// `agent_session_id` column, and resolveResumeSessionID hands that column back
// here on every restart. So the rule runs again at the argv sink, which is the
// same split claudeResumeArgs documents. A handle that fails the rule fails the
// start; see providerkit.ResumeFailedError.
//
// It sends the handle that ResolveResumeHandle RETURNS, never the stored one.
// The path rule normalizes as it checks -- it drops control characters, trims
// edge whitespace, expands `~` and cleans the path -- and Pi opens a session
// file without requiring that it exists, so sending the stored string started
// an EMPTY session at a filename nobody typed whenever the two differed.
//
// One case reaches Pi and this cannot answer it: a bare session ID that matches
// no session of THIS working directory, but does match one somewhere else. Pi
// then asks on stdin whether to fork it, nothing answers in RPC mode, and the
// startup handshake fails on the get_state timeout. That failure is visible,
// unlike the empty session the RPC path created.
func piResumeArgs(resumeSessionID, homeDir string) ([]string, error) {
	if resumeSessionID == "" {
		return nil, nil
	}
	resolved, err := (piProvider{}).ResolveResumeHandle(resumeSessionID, homeDir)
	if err != nil {
		return nil, providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored Pi session handle is not valid: %w", err))
	}
	return []string{"--session", resolved}, nil
}

var _ agent.StartFunc = Start

// Start starts a `pi --mode rpc` process and performs the startup handshake.
//
// A resume Pi cannot honour fails the WHOLE start, and that is deliberate.
// `--session` is on the launch line rather than in a post-startup RPC, so Pi
// settles the session before it enters RPC mode: it exits 1 when no session
// matches the handle, and exits 1 in RPC mode when the session file identifies a
// working directory that no longer exists — which happens here whenever a git
// worktree is removed. The switch_session step this replaced warned and
// continued on a fresh session instead; the launch flag cannot, because the
// process is already gone by the time the handshake times out.
//
// The failure is visible, which the RPC path's was not: switch_session answered
// SUCCESS for a path that named no file, so Pi wrote a new empty session there
// and the user saw a resumed tab with no history. Nothing clears
// `agent_session_id` after a failed start, so a stored handle that Pi refuses
// keeps failing until `/clear` replaces it -- which is what providerkit.ResumeFailedError
// tells the user to send.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}
	resumeArgs, err := piResumeArgs(opts.ResumeSessionID, opts.HomeDir)
	if err != nil {
		cancel()
		return nil, err
	}
	// Pi has no --working-dir flag (it uses the process cwd). Wrap
	// already sets cmd.Dir to opts.WorkingDir, so the agent picks up the right
	// directory implicitly.
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     launchSpec,
		BaseArgs:   append([]string{"--mode", "rpc"}, resumeArgs...),
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		Process:       providerkit.NewProcess(opts, "pi", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		model:         opts.Model(),
		thinkingLevel: opts.Effort(),
		provider:      cmp.Or(opts.Options[OptionProvider], DefaultProvider),
		workingDir:    opts.WorkingDir,
		sink:          sink,
	}
	a.sink = agent.NewModelProgressResetSink(newPiToolTranscript(ctx, a.sink))

	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	scanner := agent.NewStdoutScanner(stdout)
	go a.ReadOutput(scanner, a.handlePiResponse, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.EffectiveStartupTimeout()

	// 1. get_state — confirms the process is alive and yields the session
	//    handle plus the in-process model/thinking values that act as the
	//    starting point for any opts overrides. A resume already happened:
	//    `--session` selected the session before Pi entered RPC mode, so this
	//    reports the resumed session's file and needs no follow-up.
	stateRaw, err := a.sendPiCommand(CommandGetState, nil, timeout)
	if err != nil {
		cleanup()
		return nil, a.FormatStartupError(CommandGetState, err)
	}
	a.applyStateResponse(stateRaw)
	commandsKnown := a.refreshPiCommands(timeout)
	a.schedulePiGoalRefresh(true)

	// 2. get_available_models — best-effort; failure logs and continues.
	modelsRaw, err := a.sendPiCommand(CommandGetAvailableModels, nil, timeout)
	if err != nil {
		slog.Warn("pi get_available_models failed", "agent_id", a.AgentID(), "error", err)
	} else {
		a.applyAvailableModels(modelsRaw)
	}

	// 3. set_model if the requested model differs from current.
	if model := opts.Model(); model != "" && model != a.model {
		if err := a.applyModel(model, a.providerForModel(model), timeout); err != nil {
			slog.Warn("pi set_model on startup failed", "agent_id", a.AgentID(), "model", model, "error", err)
		}
	}

	// 4. set_thinking_level if the requested effort is concrete.
	if effort := opts.Effort(); effort != "" && effort != agent.EffortAuto && effort != a.thinkingLevel {
		if err := a.applyThinkingLevel(effort, timeout); err != nil {
			slog.Warn("pi set_thinking_level on startup failed", "agent_id", a.AgentID(), "level", effort, "error", err)
		}
	}

	a.Mu.Lock()
	sessionHandle := a.sessionHandleLocked()
	a.Mu.Unlock()
	sink.UpdateSessionID(sessionHandle)
	sink.BroadcastStatusActive(sessionHandle)
	// Best-effort: hydrate cost/context for resumed Pi sessions immediately
	// on a goroutine so startup readiness is not gated on a usage RPC.
	// Failures are non-fatal; message_end / agent_end keep updating usage.
	go func() {
		// One failed catalog read at startup would otherwise switch goal control and
		// the extension-command dispatch off for the life of the process, because
		// nothing else asks again until the session changes.
		if !commandsKnown {
			a.refreshPiGoalControl()
		}
		_, _ = a.refreshPiSessionStats(piSessionStatsTimeout(timeout))
	}()

	return a, nil
}

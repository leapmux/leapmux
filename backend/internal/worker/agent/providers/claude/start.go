package claude

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

// claudeSessionArgs returns the `--resume` argv pair for a stored session ID,
// or nil when the worker must not pass one.
//
// This is the ONE place any provider puts a session ID into argv: Codex, Pi
// and the ACP providers each carry theirs inside a JSON request. So the token
// rule is applied HERE and not at the sink that stores the value.
// agentOutputSink.UpdateSessionID writes whatever the agent process reports,
// and what a provider reports is its own shape -- Pi's is a session FILE PATH,
// which the token rule refuses by design and which no argv of ours ever holds.
// Applying the rule at the store would refuse a legitimate Pi handle. Applying
// it at the argv sink refuses nothing real, because a Claude session ID is a
// UUID. This is the same split the repository states for every provider
// decision: the hazard belongs to Claude's launch, so the guard lives with it.
//
// A stored value that fails the rule is corrupt or hostile, and OpenAgent's
// own check does not cover this path: the value the agent process reports goes
// straight to the `agent_session_id` column, and resolveResumeSessionID reads
// it back. `--resume` takes an OPTIONAL value, so a token that starts with a
// hyphen is not read as that value at all -- it parses as a flag of its own,
// and `--dangerously-skip-permissions` is one argv element away. Quoting keeps
// the SHELL from reading the token as syntax and does nothing about the CLI
// reading it as syntax.
//
// The start fails instead. It never drops the flag and launches on a fresh
// session; see providerkit.ResumeFailedError.
func claudeSessionArgs(resumeSessionID string) ([]string, error) {
	if resumeSessionID == "" {
		sessionID, err := uuid.NewRandom()
		if err != nil {
			return nil, fmt.Errorf("create Claude session ID: %w", err)
		}
		return []string{"--session-id", sessionID.String()}, nil
	}
	if err := validate.ValidateSessionID(resumeSessionID); err != nil {
		return nil, providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored session ID is not a valid token: %w", err))
	}
	return []string{"--resume", resumeSessionID}, nil
}

// claudeSessionStateEnv makes Claude Code publish its own turn state on the
// output stream, as `system`/`session_state_changed` frames carrying
// running / requires_action / idle.
//
// The CLI keeps the frames behind this variable, and the variable alone: it
// emits nothing without it, whatever the output format. The variable exists for
// clients whose "is it working" answer is a scan of the last message, which the
// trailing idle frame pins at "running" -- the exact heuristic this Worker
// replaced with the flag every provider publishes, so the frame is what this
// Worker wants and the reason to withhold it does not apply here.
//
// A CLI too old to know the variable ignores it and emits nothing. The output
// heuristic in armTurnFromOutput still covers that build, which is why both
// exist.
const claudeSessionStateEnv = "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1"

// claudeAgentEnv builds the environment for one Claude Code launch, from the
// environment the worker inherited.
//
// It is a function, not a run of appends at the call site, so a test can state
// what the launch carries without a process. It takes the RAW inherited
// environment for the same reason: the strip and the assignment for one variable
// are one decision, and a call site that holds half of it puts that half out of
// reach of a test.
//
// The pins REPLACE an inherited value; they do not layer over it. A worker that
// a Claude Code session launched inherits each of these markers with the
// PARENT's value. `exec.Cmd` resolves a duplicate last-wins, so a layered pin
// still reaches the CLI with the right value, and leaves an environment that
// states two values for one variable. See envutil.PinEnv.
//
// CLAUDECODE cannot go through PinEnv. Its strip is unconditional and its
// assignment is not.
func claudeAgentEnv(environ []string, loginShell bool) []string {
	env := envutil.PinEnv(envutil.FilterEnv(environ, "CLAUDECODE"),
		"CLAUDE_CODE_ENTRYPOINT=cli", claudeSessionStateEnv)
	if loginShell {
		// Set CLAUDECODE=1 so the user's shell rc files can detect they are
		// being sourced inside Claude Code and skip conflicting aliases.
		// The inner command unsets it before invoking claude.
		env = append(env, "CLAUDECODE=1")
	}
	return env
}

var _ agent.StartFunc = Start

// Start spawns a new Claude Code process and begins reading its output.
// The sink receives parsed output events via the Agent.HandleOutput method.
//
// Claude Code with --input-format stream-json does not produce any output
// (including the init message) until it receives input on stdin. Therefore,
// Start returns immediately without waiting for output. The session ID is
// extracted later from the init message when the first user message triggers
// output from Claude.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	agent.TraceStartupPhase(opts.AgentID, "claude_begin")
	ctx, cancel := context.WithCancel(ctx)

	// Check Claude Code settings files for third-party LLM provider env vars.
	// If detected, we omit --model/--effort entirely (simple command).
	// If not detected, we use a conditional shell command that checks env
	// vars at runtime (the user may have them in their shell profile).
	thirdPartyFromSettings := detectThirdPartyProvider(opts.HomeDir, opts.WorkingDir)

	baseArgs := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--permission-prompt-tool", "stdio",
		"--setting-sources", "user,project,local",
		// Emit summarized thinking text in thinking blocks; without this,
		// thinking blocks arrive with an empty `thinking` field.
		"--thinking-display", "summarized",
		// Forward subagent (Task tool) assistant text + thinking + the child's
		// own tool_use/tool_result envelopes onto the stream, each carrying the
		// spawning tool's parent_tool_use_id. Lets the worker route a subagent's
		// output into its OWN transcript (a virtual child agent) rather than
		// rendering it inline in the parent. Verified live against 2.1.220.
		"--forward-subagent-text",
	}

	sessionArgs, err := claudeSessionArgs(opts.ResumeSessionID)
	if err != nil {
		cancel()
		return nil, err
	}
	baseArgs = append(baseArgs, sessionArgs...)

	// opts.Model() is the raw stored/operator-default value, which may be a legacy
	// or fully-qualified id (a persisted "opus", a "claude-opus-4-8" from
	// LEAPMUX_CLAUDE_DEFAULT_MODEL). Canonicalize it up front so both the --model
	// arg we forward and the initial a.model live in the same alias space the
	// catalog and post-init refresh use -- otherwise launch would forward a bare
	// "opus"/fully-qualified id and a.model would read it raw until the first
	// get_settings refresh corrected it.
	launchModel := normalizeClaudeCodeModel(opts.Model())

	var modelEffortArgs []string
	if !thirdPartyFromSettings {
		// The dynamic catalog isn't known until the initialize response
		// arrives (below), so launch-time effort resolution uses the static
		// catalog. Fable and the other shipped models live there; a model
		// known only to a newer CLI downgrades ultracode→xhigh here and is
		// re-enabled post-init by buildStartupFlagSettings.
		modelEffortArgs = newEffortResolver(claudeCodeAvailableModels).buildModelEffortArgs(launchModel, opts.Effort())
	}

	// Always probe for a shell-profile third-party provider unless settings
	// already flagged one: a default-model launch sends no --model/--effort
	// (empty modelEffortArgs) but must still detect a provider configured in the
	// user's rc files so OptionGroups() can hide the model/effort UI.
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}
	var modelEffortGate *launch.EnvGatedArgs
	if !thirdPartyFromSettings {
		modelEffortGate = claudeModelEffortGate(modelEffortArgs)
	}
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: []string{"CLAUDECODE"},
		BaseArgs:     baseArgs,
		EnvGated:     modelEffortGate,
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = claudeAgentEnv(cmd.Environ(), opts.LoginShell)
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)

	// providerkit.SetupProcessPipes configures SIGTERM cancel, WaitDelay, and opens
	// stdin/stdout/stderr pipes.
	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		Process:                providerkit.NewProcess(opts, "claude", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		model:                  launchModel,
		sessionID:              sessionArgs[1],
		effort:                 opts.Effort(),
		workingDir:             opts.WorkingDir,
		homeDir:                opts.HomeDir,
		sink:                   agent.NewModelProgressResetSink(sink),
		thirdPartyFromSettings: thirdPartyFromSettings,
		pendingControl:         make(map[string]chan<- claudeCodeControlResult),
		alwaysThinking:         AlwaysThinkingOn,
	}

	agent.TraceStartupPhase(opts.AgentID, "before_exec_start")
	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	agent.TraceStartupPhase(opts.AgentID, "after_exec_start")

	// Drain stderr in a background goroutine.
	a.DrainStderr(stderrPipe)

	// Read stdout in a background goroutine. Output will only arrive after
	// the first message is sent to stdin (Claude Code behavior with
	// --input-format stream-json).
	scanner := agent.NewStdoutScanner(stdout)
	go a.readOutputLoop(scanner)

	// cleanup terminates the agent process and waits for it to exit.
	// This ensures no orphaned process or goroutine is left behind.
	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	// Run the control-protocol startup handshake (initialize -> extract settings ->
	// permission mode -> apply persisted flag settings -> refresh). On a hard failure
	// tear down the just-spawned process so no orphan process or goroutine survives.
	if err := a.runStartupHandshake(ctx, opts); err != nil {
		cleanup()
		return nil, err
	}
	a.sink.UpdateSessionID(a.sessionID)

	return a, nil
}

// runStartupHandshake drives the control-protocol exchange that must complete before
// Start hands back a usable agent: it sends initialize, captures the model
// catalog and the other settings the response carries, applies the permission mode and
// any persisted flag settings, then refreshes the stored settings from the CLI. It
// returns a formatted startup error on a hard failure (initialize / set_permission_mode);
// the caller tears the process down. Every field write here runs on the Start
// goroutine before the agent is registered with the manager -- the same lock-free
// pre-registration window buildStartupFlagSettings documents -- so a bare field write
// needs no a.Mu. The permission-mode writes still go through their own locked helpers
// (settlePermissionMode, setAutoModeAvailable), because the READER goroutine already
// runs: HandleOutput delivers the control responses this handshake waits for, and a
// deferred one reaches claudeCodeHandleControlResponse, which writes the same fields.
func (a *Agent) runStartupHandshake(ctx context.Context, opts agent.Options) error {
	timeout := opts.EffectiveStartupTimeout()

	// Send "initialize" as the first control request, matching the Agent SDK
	// protocol. This triggers Claude Code to emit the init system message
	// (which contains the session_id) and establishes the control protocol.
	agent.TraceStartupPhase(opts.AgentID, "before_initialize")
	initResp, err := a.sendControlAndWait(ctx, `{"subtype":"initialize"}`, timeout)
	if err != nil {
		return a.FormatStartupError("initialize", err)
	}
	agent.TraceStartupPhase(opts.AgentID, "after_initialize")

	// Extract settings from the initialize response.
	if initResp.OutputStyle != "" {
		a.outputStyle = initResp.OutputStyle
	}
	a.availableOutputStyles = initResp.AvailableOutputStyles
	// Discover the model catalog from the initialize response. An empty result
	// (old CLI or parse failure) leaves a.availableModels nil and OptionGroups()'s
	// model projection falls back to the static catalog. For a third-party provider it
	// surfaces no model group regardless (the UI hides model/effort), but effortResolver() still
	// resolves over whatever the response carried (with the static fallback) so
	// effort/window resolution has a usable catalog.
	a.availableModels = convertClaudeModels(initResp.Models, initResp.UnavailableModels)
	if initResp.FastModeState == FastModeOn || initResp.FastModeState == "cooldown" {
		a.fastMode = FastModeOn
	} else {
		a.fastMode = FastModeOff
	}

	agent.TraceStartupPhase(opts.AgentID, "before_permission_mode")
	// applyStartupPermissionMode records the acknowledged mode as the confirmed one, so
	// this discards the response rather than writing the field a second time.
	if _, err := a.applyStartupPermissionMode(ctx, cmp.Or(opts.PermissionMode(), contracts.ClaudeModeDefault), timeout); err != nil {
		return a.FormatStartupError("set_permission_mode", err)
	}
	agent.TraceStartupPhase(opts.AgentID, "after_permission_mode")

	// Apply persisted options that differ from initialized defaults.
	if flagSettings := a.buildStartupFlagSettings(opts.Options); len(flagSettings) > 0 {
		if err := a.sendApplyFlagSettings(ctx, flagSettings, timeout); err != nil {
			slog.Warn("apply_flag_settings at startup failed", "agent_id", a.AgentID(), "error", err)
		}
	}
	// Refresh from the CLI once startup completes so the persisted effort
	// reflects the value the CLI actually picked (e.g. when we launched
	// with --effort omitted because LeapMux resolved the effort option to
	// "auto"). Run even if apply_flag_settings failed so the DB mirrors
	// the CLI's actual state rather than what we tried to set.
	observedSettings := a.refreshSettingsFromAgent(timeout)
	for _, id := range claudeFlagOptionIDs {
		if _, observed := observedSettings[id]; !observed {
			a.markSettingsUnresolved(id)
		}
	}
	a.ensureSettledModelListed()
	return nil
}

// buildStartupFlagSettings builds an apply_flag_settings payload for the option
// settings that differ from the initialized defaults.
//
// Reads a.effort/a.model without holding a.Mu: this runs only from
// Start, before the agent is registered with the manager and thus
// before any concurrent UpdateSettings/refreshSettingsFromAgent can touch those
// fields, so the lock-free read is safe. Do not call it post-registration.
func (a *Agent) buildStartupFlagSettings(options map[string]string) map[string]interface{} {
	fs := map[string]interface{}{}
	maps.Copy(fs, a.reconcileStartupEffortFlags())
	if v := options[OptionOutputStyle]; v != "" && v != a.outputStyle {
		fs[OptionOutputStyle] = v
	}
	if v := options[OptionFastMode]; v != "" && v != a.fastMode {
		fs[OptionFastMode] = flagSettingOnOff(v)
	}
	if v := options[OptionAlwaysThinking]; v != "" && v != a.alwaysThinking {
		fs[OptionAlwaysThinking] = flagSettingThinking(v)
	}
	return fs
}

// reconcileStartupEffortFlags returns the apply_flag_settings needed to bring a
// freshly launched session's effort into agreement with the dynamic catalog, or nil
// for a third-party session (which has no model/effort UI -- see hidesModelEffortUI).
// The capability reconciliation itself lives on effortResolver (reconcileStartupFlags);
// this method applies the agent-level gate and hands the launch-time a.model/a.effort
// to the resolver it reconciles against.
func (a *Agent) reconcileStartupEffortFlags() map[string]interface{} {
	// A third-party session presents no model/effort UI and (when detected from
	// settings) launches with no --model/--effort; pushing effort/ultracode flags
	// would apply settings its user can neither see nor control, so leave it at the
	// CLI's own resolution. It reads the same predicate AvailableModels uses, so the
	// "hidden UI" and "no effort push" decisions can't drift.
	if a.hidesModelEffortUI() {
		return nil
	}
	return a.effortResolver().reconcileStartupFlags(a.model, a.effort)
}

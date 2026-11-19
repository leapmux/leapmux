package qoder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

var _ agent.StartFunc = Start

// qoderAgentEnv pins SDK startup settings and removes inherited session identity.
func qoderAgentEnv(environ []string) []string {
	env := envutil.FilterEnv(environ, qoderStrippedEnvKeys...)
	return envutil.PinEnv(env, qoderLaunchEnvPins...)
}

// qoderSessionArgs returns the argv pair that pins or resumes the session.
func qoderSessionArgs(resumeSessionID string) ([]string, error) {
	if resumeSessionID == "" {
		sessionID, err := uuid.NewRandom()
		if err != nil {
			return nil, fmt.Errorf("create Qoder session ID: %w", err)
		}
		return []string{"--session-id", sessionID.String()}, nil
	}
	if err := validate.ValidateSessionID(resumeSessionID); err != nil {
		return nil, providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored session ID is not a valid token: %w", err))
	}
	return []string{"--resume", resumeSessionID}, nil
}

// Start spawns one Qoder process and begins reading its output.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (_ agent.Agent, retErr error) {
	ctx, cancel := context.WithCancel(ctx)

	registration := Registration()
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}

	sessionArgs, err := qoderSessionArgs(opts.ResumeSessionID)
	if err != nil {
		cancel()
		return nil, err
	}

	effort := opts.Effort()
	thinking := qoderThinkingValue(opts.Get(OptionThinking))
	outputStyle := opts.Get(OptionOutputStyle)
	directory, settingsPath, err := prepareSDKSettings(ctx, opts.AgentDirs)
	if err != nil {
		cancel()
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			retErr = errors.Join(retErr, directory.Close())
		}
	}()
	workerArgs := []string{
		"--settings", settingsPath,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		// The SDK host exposes can_use_tool only for a session with this flag.
		"--permission-prompt-tool", "stdio",
		"--include-partial-messages",
		"--replay-user-messages",
		"--permission-mode", qoderPermissionFlag(opts.PermissionMode()),
	}
	if model := opts.Model(); model != "" {
		workerArgs = append(workerArgs, "-m", model)
	}
	workerArgs = append(workerArgs, qoderReasoningArgs(effort, thinking)...)
	workerArgs = append(workerArgs, qoderOutputStyleArgs(outputStyle)...)
	workerArgs = append(workerArgs, sessionArgs...)

	prepared, err := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: qoderStrippedEnvKeys,
		BaseArgs:     []string{"--sdk", "--config-dir", qoderConfigRoot(opts), "-w", opts.WorkingDir},
		WorkingDir:   opts.WorkingDir,
		SetEnv:       qoderLaunchEnvPins,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	cmd := prepared.Cmd

	cmd.Env = qoderAgentEnv(cmd.Environ())
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)
	credentials, err := qoderCredentialsFromEnv(cmd.Env)
	if err != nil {
		cancel()
		return nil, errors.Join(err, prepared.Close())
	}

	pipes, err := providerkit.SetupLaunchPipes(prepared, cancel)
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	a := &Agent{
		Process:        providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "qoder", ShutdownGrace: registration.ShutdownGrace}, pipes, ctx, cancel),
		sink:           agent.NewModelProgressResetSink(sink),
		opts:           opts,
		model:          opts.Model(),
		permissionMode: permissionModeWire(opts.PermissionMode()),
		effort:         effort,
		thinking:       thinking,
		outputStyle:    outputStyle,
		usageRefresh:   make(chan struct{}, 1),
		credentials:    credentials,
		directory:      directory,
	}

	if err := a.StartCmd(); err != nil {
		cancel()
		return nil, err
	}
	transferred = true
	a.DrainStderr(stderrPipe)
	scanner := a.NewOutputScanner(stdout)
	go a.readOutputLoop(scanner)

	if err := a.startDaemon(workerArgs); err != nil {
		a.Process.Stop()
		_ = a.Wait()
		return nil, a.FinalizeStartupCause(err)
	}
	// The account quota arrives in the background, so it delays no startup.
	go a.refreshUsageLoop()
	a.requestUsageRefresh()

	if a.sessionID != "" {
		a.sink.UpdateSessionID(a.sessionID)
	}
	return a, nil
}

// startStream prepares the stream-json channel before Start returns, so a
// returned agent accepts input and a failed one fails startup with the reason
// instead of cancelling the first turn.
//
// The channel answers nothing until initialize succeeds. The MCP registration
// follows, so input forms work from the first turn. Without that registration
// the agent still works, and only its MCP servers cannot ask for a form. Its
// failure therefore ends startup only when the process ended.
func (a *Agent) startStream() error {
	if err := a.initializeStream(); err != nil {
		return fmt.Errorf("initialize Qoder stream: %w", err)
	}
	if err := a.registerMcpServersForForms(); err != nil {
		if a.processEnded() {
			return fmt.Errorf("register Qoder MCP servers for input forms: %w", err)
		}
		slog.Warn("qoder could not register its MCP servers for input forms", "agent_id", a.AgentID(), "error", err)
	}
	return nil
}

// processEnded reports whether the process exited or its context ended.
func (a *Agent) processEnded() bool {
	select {
	case <-a.ProcessDone():
		return true
	case <-a.Context().Done():
		return true
	default:
		return false
	}
}

// qoderConfigRoot selects the native profile under the launch's home directory.
// Qoder stores settings.json, .auth/, projects/ and logs/ under --config-dir.
func qoderConfigRoot(opts agent.Options) string {
	if opts.HomeDir != "" {
		return opts.HomeDir + "/.qoder"
	}
	return ".qoder"
}

// qoderReasoningArgs returns the effort and thinking flags of one launch.
//
// Every launch states `--reasoning-effort`. On `--resume`, Qoder CLI 1.1.78
// restores the reasoning settings that the session stored, unless the launch
// states an effort: without one, the new process keeps the effort and the
// thinking state of the previous process, and Qoder ignores `--thinking` alone.
// LeapMux's Auto therefore sends Qoder's own word `auto`, which states no
// level, so the model's default applies again.
//
// Qoder turns thinking back on for any explicit effort other than `none`, so
// thinking Off sends `--thinking disabled` with the effort `none`. The selected
// effort stays in the options and applies again when thinking returns to Auto
// or On. On sends `--thinking adaptive`, because `--thinking enabled` also
// needs a fixed token budget. Auto sends no `--thinking` flag, so Qoder
// decides.
func qoderReasoningArgs(effort, thinking string) []string {
	level := effort
	if level == "" || level == agent.EffortAuto {
		level = contracts.QoderEffortLevelAuto
	}
	switch qoderThinkingValue(thinking) {
	case qoderThinkingOff:
		return []string{"--thinking", "disabled", "--reasoning-effort", contracts.QoderEffortLevelNone}
	case qoderThinkingOn:
		return []string{"--thinking", "adaptive", "--reasoning-effort", level}
	default:
		return []string{"--reasoning-effort", level}
	}
}

// qoderOutputStyleArgs returns the `--output-style` pair of one launch.
//
// Qoder stores no style in the session, so a launch without the flag, also on
// `--resume`, runs the style of Qoder's own settings. That is LeapMux's Auto.
// Qoder runs `default` for a name that it does not know, and its initialize
// reply then reports `default`.
func qoderOutputStyleArgs(style string) []string {
	if strings.TrimSpace(style) == "" || style == qoderOutputStyleAuto {
		return nil
	}
	return []string{"--output-style", style}
}

// qoderPermissionFlag maps the wire mode onto the snake_case flag vocabulary.
func qoderPermissionFlag(mode string) string {
	switch mode {
	case contracts.QoderModeAcceptEdits:
		return "accept_edits"
	case contracts.QoderModeDontAsk:
		return "dont_ask"
	case contracts.QoderModePlan:
		return "plan"
	case contracts.QoderModeAuto:
		return "auto"
	case contracts.QoderModeBypassPermissions:
		// Qoder runs this mode as its internal `yolo`. A disableYoloMode
		// setting, or an untrusted folder while Qoder's folder trust is on,
		// starts the session in Default, and the init frame then reports that
		// mode. Qoder refuses a live change to this mode in the same cases.
		return "bypass_permissions"
	default:
		return "default"
	}
}

// permissionModeWire normalizes a stored mode onto the wire vocabulary.
func permissionModeWire(mode string) string {
	if mode == "" {
		return contracts.QoderModeDefault
	}
	return mode
}

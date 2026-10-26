package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

var _ agent.StartFunc = Start

// codebuddyDisableAutoUpdaterEnv turns off the updater of CodeBuddy Code, which
// installs a newer release over the running one. The `-p` mode that the worker
// starts never runs it, but a daemon does (CODEBUDDY_SESSION_KIND=daemon), and the
// variable also stops the `brew list` and `dnf list` probes of the install method.
// Any truthy value counts. The shell wrapper states it after the user's profile.
const codebuddyDisableAutoUpdaterEnv = "DISABLE_AUTOUPDATER=1"

// codebuddyAgentEnv builds the environment for one CodeBuddy launch.
//
// The pins REPLACE an inherited value. The important one is the identity scrub:
// CodeBuddy writes CLAUDE_SESSION_ID into every child alongside
// CODEBUDDY_SESSION_ID, so a LeapMux worker that runs a Claude Code child would
// inherit a CodeBuddy session id under the Claude name.
func codebuddyAgentEnv(environ []string) []string {
	env := envutil.PinEnv(envutil.FilterEnv(environ,
		"CLAUDE_SESSION_ID",
		"CODEBUDDY_SESSION_ID",
		"CODEBUDDY_CONVERSATION_MESSAGE_ID",
		"CODEBUDDY_CONVERSATION_REQUEST_ID",
		"CODEBUDDY_TOOL_CALL_ID",
		"CODEBUDDY_ROOT_REQUEST_ID",
		"CODEBUDDY_PROJECT_DIR",
		"CLAUDE_PROJECT_DIR",
	),
		"DISABLE_TELEMETRY=1",
		"DISABLE_GALILEO=1",
		"CODEBUDDY_DISABLE_TRACE_COLLECTOR=1",
	)
	return env
}

// codebuddySessionArgs returns the argv pair that pins or resumes the session.
func codebuddySessionArgs(resumeSessionID string) ([]string, string, error) {
	if resumeSessionID == "" {
		sessionID, err := uuid.NewRandom()
		if err != nil {
			return nil, "", fmt.Errorf("create CodeBuddy session ID: %w", err)
		}
		return []string{"--session-id", sessionID.String()}, sessionID.String(), nil
	}
	if err := validate.ValidateSessionID(resumeSessionID); err != nil {
		return nil, "", providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored session ID is not a valid token: %w", err))
	}
	return []string{"--resume", resumeSessionID}, resumeSessionID, nil
}

// Start spawns one CodeBuddy process and begins reading its output.
//
// Start declares host capabilities and reads the model catalog before publishing
// the session. A resumed process first replays its history, which the Worker
// consumes because it already owns the transcript.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	thinking := codebuddyThinkingValue(opts.Get(OptionThinking))
	thinkingOverride, err := codebuddyThinkingOverride(thinking)
	if err != nil {
		cancel()
		return nil, err
	}

	registration := Registration()
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}

	sessionArgs, sessionID, err := codebuddySessionArgs(opts.ResumeSessionID)
	if err != nil {
		cancel()
		return nil, err
	}

	effort := opts.Effort()
	baseArgs := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--setting-sources", "user",
		"--permission-mode", permissionModeFlag(opts.PermissionMode()),
	}
	if effort != "" {
		baseArgs = append(baseArgs, "--effort", effort)
	}
	if model := opts.Model(); model != "" {
		baseArgs = append(baseArgs, "--model", model)
	}
	settings := codebuddyNativeSettings{OutputStyle: opts.Get(OptionOutputStyle), AlwaysThinkingEnabled: thinkingOverride}
	directory, settingsArgs, err := writeStartupSettings(ctx, opts.AgentDirs, settings)
	if err != nil {
		cancel()
		return nil, err
	}
	baseArgs = append(baseArgs, settingsArgs...)
	baseArgs = append(baseArgs, sessionArgs...)

	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: []string{"CLAUDE_SESSION_ID", "CODEBUDDY_SESSION_ID"},
		SetEnv:       []string{codebuddyDisableAutoUpdaterEnv},
		BaseArgs:     baseArgs,
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = codebuddyAgentEnv(cmd.Environ())
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)

	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		removeAgentDir(opts.AgentID, directory)
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()
	var resumeState *codebuddyResumeState
	if opts.ResumeSessionID != "" {
		resumeState = &codebuddyResumeState{done: make(chan error, 1)}
	}

	a := &Agent{
		Process:        providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "codebuddy", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel),
		sink:           agent.NewModelProgressResetSink(sink),
		opts:           opts,
		sessionID:      sessionID,
		resumeState:    resumeState,
		model:          opts.Model(),
		effort:         effort,
		thinkingChoice: thinking,
		permissionMode: permissionModeWire(opts.PermissionMode()),
		pendingControl: make(map[string]chan<- codebuddyControlResult),
		directory:      directory,
	}
	if err := a.StartCmd(); err != nil {
		cancel()
		removeAgentDir(opts.AgentID, directory)
		return nil, err
	}
	// A failed startup returns no agent, so nothing waits for the process.
	// fail stops it and removes the directory itself.
	fail := func(phase string, err error) (agent.Agent, error) {
		startupErr := a.FormatStartupError(phase, err)
		a.Stop()
		removeAgentDir(opts.AgentID, directory)
		return nil, startupErr
	}
	a.DrainStderr(stderrPipe)
	scanner := agent.NewStdoutScanner(stdout)
	go a.readOutputLoop(scanner)
	initialization, err := a.initializeCapabilities(opts.EffectiveStartupTimeout())
	if err != nil {
		return fail("initialization", err)
	}
	if initialization.HasOutputStyleCatalog {
		a.mu.Lock()
		a.outputStyles = normalizeCodebuddyOutputStyles(initialization.OutputStyles)
		a.outputStyle = codebuddyOutputStyleName(a.outputStyles, initialization.OutputStyle)
		a.mu.Unlock()
	}
	catalogErr := a.loadModelCatalog()
	select {
	case <-a.ProcessDone():
		if catalogErr == nil {
			catalogErr = a.ProcessExitError()
		}
		return fail("model catalog", catalogErr)
	default:
	}
	if catalogErr != nil {
		slog.Warn("codebuddy: model catalog unavailable", "agent_id", a.AgentID(), "error", catalogErr)
	}
	if resumeState != nil {
		resumeErr, err := providerkit.AwaitReply(&a.Process, ctx, resumeState.done, "CodeBuddy history replay", opts.EffectiveStartupTimeout())
		if err == nil {
			err = resumeErr
		}
		if err != nil {
			return fail("session restore", err)
		}
	}

	a.sink.UpdateSessionID(sessionID)
	return a, nil
}

// startupSettingsFileName is the file in the agent's private directory that
// holds the settings of one process.
const startupSettingsFileName = "startup-settings.json"

// writeStartupSettings writes the settings that override CodeBuddy's own for
// one process into a new private directory of the agent. It returns the
// directory and the arguments that point the process at the file. When
// settings overrides nothing, it returns no directory and no arguments.
//
// The settings travel in a file, never as `--settings <json>`. The shell passes
// each argument to CodeBuddy as a native argument, and Windows PowerShell 5.1,
// and PowerShell 7 in its Legacy argument passing, pass each embedded double
// quote unescaped. CodeBuddy's command-line parser then removes the quotes of
// inline JSON. A Windows path cannot hold a double quote, and PowerShell
// quotes a path with a space itself.
func writeStartupSettings(ctx context.Context, dirs *agentdir.Dirs, settings codebuddyNativeSettings) (*agentdir.Dir, []string, error) {
	if settings.OutputStyle == "" && settings.AlwaysThinkingEnabled == nil {
		return nil, nil, nil
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, fmt.Errorf("encode CodeBuddy startup settings: %w", err)
	}
	dir, err := dirs.New(ctx, agentDirSpec())
	if err != nil {
		return nil, nil, fmt.Errorf("prepare the CodeBuddy agent directory: %w", err)
	}
	path := filepath.Join(dir.Path(), startupSettingsFileName)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("write CodeBuddy startup settings: %w", err), dir.Close())
	}
	return dir, []string{"--settings", path}, nil
}

// permissionModeFlag maps LeapMux's wire mode onto the flag vocabulary.
func permissionModeFlag(mode string) string {
	return mode
}

// permissionModeWire normalizes a stored mode onto the wire vocabulary.
func permissionModeWire(mode string) string {
	if mode == "" {
		return contracts.CodebuddyModeDefault
	}
	return mode
}

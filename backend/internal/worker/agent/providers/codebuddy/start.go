package codebuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	if settings.OutputStyle != "" || settings.AlwaysThinkingEnabled != nil {
		encodedSettings, err := json.Marshal(settings)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("encode CodeBuddy startup settings: %w", err)
		}
		baseArgs = append(baseArgs, "--settings", string(encodedSettings))
	}
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
	}
	if err := a.StartCmd(); err != nil {
		cancel()
		return nil, err
	}
	a.DrainStderr(stderrPipe)
	scanner := agent.NewStdoutScanner(stdout)
	go a.readOutputLoop(scanner)
	initialization, err := a.initializeCapabilities(opts.EffectiveStartupTimeout())
	if err != nil {
		startupErr := a.FormatStartupError("initialization", err)
		a.Stop()
		return nil, startupErr
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
		startupErr := a.FormatStartupError("model catalog", catalogErr)
		a.Stop()
		return nil, startupErr
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
			startupErr := a.FormatStartupError("session restore", err)
			a.Stop()
			return nil, startupErr
		}
	}

	a.sink.UpdateSessionID(sessionID)
	return a, nil
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

package codebuddy

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

var _ agent.StartFunc = Start

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
		"DISABLE_AUTOUPDATER=1",
		"CODEBUDDY_DISABLE_TRACE_COLLECTOR=1",
		"CODEBUDDY_DISABLE_WORKFLOWS=1",
	)
	return env
}

// codebuddySessionArgs returns the argv pair that pins or resumes the session.
func codebuddySessionArgs(resumeSessionID string) ([]string, error) {
	if resumeSessionID == "" {
		sessionID, err := uuid.NewRandom()
		if err != nil {
			return nil, fmt.Errorf("create CodeBuddy session ID: %w", err)
		}
		return []string{"--session-id", sessionID.String()}, nil
	}
	if err := validate.ValidateSessionID(resumeSessionID); err != nil {
		return nil, providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored session ID is not a valid token: %w", err))
	}
	return []string{"--resume", resumeSessionID}, nil
}

// Start spawns one CodeBuddy process and begins reading its output.
//
// CodeBuddy emits nothing until it receives input on stdin, so Start returns
// without waiting for the init message. The session ID is pinned at launch.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}

	sessionArgs, err := codebuddySessionArgs(opts.ResumeSessionID)
	if err != nil {
		cancel()
		return nil, err
	}

	effort := opts.Effort()
	baseArgs := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--setting-sources", "user",
		"--permission-mode", permissionModeFlag(opts.PermissionMode()),
	}
	if effort != "" {
		baseArgs = append(baseArgs, "--effort", effort)
	}
	if model := opts.Model(); model != "" {
		baseArgs = append(baseArgs, "--model", model)
	}
	baseArgs = append(baseArgs, sessionArgs...)

	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: []string{"CLAUDE_SESSION_ID", "CODEBUDDY_SESSION_ID"},
		BaseArgs:     baseArgs,
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = codebuddyAgentEnv(cmd.Environ())
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		return nil, err
	}

	a := &Agent{
		Process:        providerkit.NewProcess(opts, "codebuddy", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:           agent.NewModelProgressResetSink(sink),
		opts:           opts,
		model:          opts.Model(),
		effort:         effort,
		permissionMode: permissionModeWire(opts.PermissionMode()),
		pendingControl: make(map[string]chan<- codebuddyControlResult),
	}
	if opts.ResumeSessionID != "" {
		a.sessionID = opts.ResumeSessionID
	}

	if err := a.StartCmd(cmd, cancel); err != nil {
		cancel()
		return nil, err
	}
	a.DrainStderr(stderrPipe)
	scanner := agent.NewStdoutScanner(stdout)
	go a.readOutputLoop(scanner)

	if a.sessionID != "" {
		a.sink.UpdateSessionID(a.sessionID)
	}
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

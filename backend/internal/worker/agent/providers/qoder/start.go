package qoder

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

// qoderAgentEnv builds the environment for one Qoder launch.
//
// Qoder's own tool launcher scrubs QODER_CLI_*, GEMINI_CLI_*, CLAUDE_* and
// ANTHROPIC_* prefixes from children; this scrub does the same in reverse so a
// nested LeapMux never inherits a parent Qoder session.
//
// `QODER_AGENT_SDK_ENTRYPOINT` is the exception: it marks the process as an SDK
// host, which is an identity variable in production but is also the switch that
// selects the `QODER_SDK_AUTH_PAYLOAD_FILE` credential seam. The scrub therefore
// removes the INHERITED marker and restores it only when the launch itself names
// a payload file, so a nested LeapMux starts clean and a test that installs a
// mocked credential keeps the seam it needs.
func qoderAgentEnv(environ []string) []string {
	env := envutil.FilterEnv(environ,
		"QODER_SESSION_ID",
		"QODER_PID",
		"QODER_SESSION_TYPE",
		"QODER_REMOTE_CHILD",
		"QODER_AGENT_SDK_ENTRYPOINT",
		"QODER_CLI",
		"QODERCN_CLI",
		"QODER_PERSONAL_ACCESS_TOKEN",
	)
	pins := []string{
		"QODER_SITE=GLOBAL",
		"QODER_NO_RC=1",
		"QODER_FORCE_FILE_STORAGE=1",
		"QODER_HTTPDNS=0",
	}
	if len(envutil.ValuesFor(env, "QODER_SDK_AUTH_PAYLOAD_FILE")) > 0 {
		pins = append(pins, "QODER_AGENT_SDK_ENTRYPOINT=1")
	}
	return envutil.PinEnv(env, pins...)
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
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
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
	baseArgs := []string{
		"--config-dir", qoderConfigRoot(opts),
		"-w", opts.WorkingDir,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		// Askable tool calls must reach the worker as can_use_tool
		// control_requests. Without this hidden flag Qoder keeps the
		// permission channel closed and fails every ask closed ("Allow
		// writing to ..." becomes the tool error), so a Default-mode agent
		// could never raise a banner.
		"--permission-prompt-tool", "stdio",
		"--include-partial-messages",
		"--replay-user-messages",
		"--permission-mode", qoderPermissionFlag(opts.PermissionMode()),
	}
	if model := opts.Model(); model != "" {
		baseArgs = append(baseArgs, "-m", model)
	}
	baseArgs = append(baseArgs, qoderEffortArgs(effort)...)
	baseArgs = append(baseArgs, sessionArgs...)

	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: []string{"QODER_SESSION_ID", "QODER_CLI", "QODERCN_CLI"},
		BaseArgs:     baseArgs,
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = qoderAgentEnv(cmd.Environ())
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		return nil, err
	}

	a := &Agent{
		Process:        providerkit.NewProcess(opts, "qoder", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:           agent.NewModelProgressResetSink(sink),
		opts:           opts,
		model:          opts.Model(),
		permissionMode: permissionModeWire(opts.PermissionMode()),
		effort:         effort,
		pendingControl: make(map[string]chan<- qoderControlResult),
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

	// The stream-json channel answers nothing until initialize succeeds. Run
	// the handshake here, so a returned agent accepts input and a failed one
	// fails startup with the reason instead of cancelling the first turn.
	if err := a.initializeStream(); err != nil {
		cancel()
		return nil, fmt.Errorf("initialize Qoder stream: %w", err)
	}

	if a.sessionID != "" {
		a.sink.UpdateSessionID(a.sessionID)
	}
	return a, nil
}

// qoderConfigRoot states the per-agent config directory. Qoder's --config-dir
// is mandatory for isolation: it becomes the user-level config root holding
// settings.json, .auth/, projects/ and logs/.
func qoderConfigRoot(opts agent.Options) string {
	if opts.HomeDir != "" {
		return opts.HomeDir + "/.qoder"
	}
	return ".qoder"
}

// qoderEffortArgs returns the `--reasoning-effort` pair for one level.
//
// Auto and the empty value send no flag at all, so the CLI keeps whatever
// default the model resolves. Every other level is one of the words Qoder's own
// effort validator admits.
func qoderEffortArgs(effort string) []string {
	if effort == "" || effort == agent.EffortAuto {
		return nil
	}
	return []string{"--reasoning-effort", effort}
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

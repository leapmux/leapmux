package droid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start launches `droid exec --input-format stream-jsonrpc` and opens the
// agent's session.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		return nil, err
	}
	return startProcess(ctx, opts, sink, spec)
}

// startProcess runs the CLI and completes the stream-jsonrpc handshake.
func startProcess(ctx context.Context, opts agent.Options, sink agent.ProviderServices, spec launch.Spec) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	args := append([]string{}, droidBaseArgs...)
	settingsPath, cleanupSettings, err := writeRuntimeSettings(opts)
	if err != nil {
		cancel()
		return nil, err
	}
	if settingsPath != "" {
		args = append(args, "--settings", settingsPath)
	}
	// E2E and a bypass preset run without permission prompts. It cannot combine
	// with `--auto`.
	// `default` states no --auto flag at all: Droid's own autonomy default is
	// `normal`, which asks before a tool that changes something. Passing
	// `--auto low` would auto-approve low-risk tools and no banner is raised.
	if opts.PermissionMode() == "auto-high" {
		args = append(args, "--skip-permissions-unsafe")
	} else if mode := opts.PermissionMode(); mode != "" && mode != "default" {
		args = append(args, "--auto", droidAutonomyFlag(mode))
	}
	if opts.Model() != "" {
		args = append(args, "--model", opts.Model())
	}
	if opts.Effort() != "" {
		args = append(args, "--reasoning-effort", opts.Effort())
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--session-id", opts.ResumeSessionID)
	}
	if opts.WorkingDir != "" {
		args = append(args, "--cwd", opts.WorkingDir)
	}

	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   args,
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		cleanupSettings()
		return nil, err
	}

	a := &Agent{
		Process:    providerkit.NewProcess(opts, "droid", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:       agent.NewModelProgressResetSink(sink),
		workingDir: opts.WorkingDir,
		clock:      quartz.NewReal(),
	}
	if err := a.StartCmd(cmd, cancel); err != nil {
		cancel()
		cleanupSettings()
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
		cleanupSettings()
	}

	go a.ReadLines(agent.NewStdoutScanner(stdout), a.handleFrame)

	if err := a.handshake(opts); err != nil {
		cleanup()
		return nil, a.FormatStartupError("initialize_session", err)
	}
	return a, nil
}

// handshake sends droid.initialize_session and records the session id.
func (a *Agent) handshake(opts agent.Options) error {
	machineID, err := droidMachineID()
	if err != nil {
		return err
	}
	params := initializeParams{
		Cwd:             opts.WorkingDir,
		MachineID:       machineID,
		Model:           opts.Model(),
		ReasoningEffort: opts.Effort(),
		AutonomyMode:    droidAutonomyForMode(opts.PermissionMode()),
	}
	if opts.ResumeSessionID != "" {
		params.SessionID = opts.ResumeSessionID
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	env := newDroidEnvelope(droidTypeRequest)
	env.ID = droidInitRequestID
	env.Method = droidMethodInitializeSession
	env.Params = raw
	line, err := env.Marshal()
	if err != nil {
		return err
	}
	if err := a.WriteStdin(append(line, '\n')); err != nil {
		return err
	}
	// The response arrives on the reader goroutine and is folded into state by
	// handleFrame; the driver does not block on it beyond delivery.
	a.sink.UpdateSessionID(opts.ResumeSessionID)
	return nil
}

// droidAutonomyFlag maps a LeapMux permission mode onto a `--auto` flag value.
func droidAutonomyFlag(mode string) string {
	switch mode {
	case "auto-low":
		return "low"
	case "auto-medium":
		return "medium"
	case "auto-high":
		return "high"
	default:
		return "low"
	}
}

// droidMachineID returns a stable machine identifier. The CLI requires it on
// initialize_session.
func droidMachineID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// writeRuntimeSettings writes the BYOK settings file the CLI merges for this
// process only. It returns the path and a cleanup function.
func writeRuntimeSettings(opts agent.Options) (string, func(), error) {
	model := strings.TrimSpace(opts.Model())
	if model == "" {
		return "", func() {}, nil
	}
	home := opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	dir := filepath.Join(home, ".factory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", func() {}, err
	}
	file, err := os.CreateTemp(dir, "leapmux-runtime-settings-*.json")
	if err != nil {
		return "", func() {}, err
	}
	settings := map[string]any{
		"sessionDefaultSettings": map[string]any{
			"model":           model,
			"reasoningEffort": opts.Effort(),
			"autonomyMode":    droidAutonomyForMode(opts.PermissionMode()),
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", func() {}, err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", func() {}, err
	}
	_ = file.Close()
	path := file.Name()
	return path, func() { _ = os.Remove(path) }, nil
}

// FormatStartupError wraps a startup failure with the phase name.
func (a *Agent) FormatStartupError(phase string, err error) error {
	return a.Process.FormatStartupError(phase, err)
}

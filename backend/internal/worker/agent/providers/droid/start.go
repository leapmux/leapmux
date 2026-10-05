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

// droidDisableAutoUpdateEnv turns off the update that Droid starts from its TUI,
// its daemon and `droid update`: a download that replaces its own executable.
// `droid exec` never reaches the updater, but a `droid` that the agent's own tool
// starts inherits the variable. Only `0` and `false` turn it off; any other value
// takes the default of the build, which is off in the npm build and may be on in
// another.
const droidDisableAutoUpdateEnv = "FACTORY_DROID_AUTO_UPDATE_ENABLED=0"

// Start launches `droid exec --input-format stream-jsonrpc` and opens the
// agent's session.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	registration := Registration()
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		return nil, err
	}
	return startProcess(ctx, opts, sink, droidLaunch{spec: spec, shutdownGrace: registration.ShutdownGrace})
}

// startProcess runs the CLI and completes the stream-jsonrpc handshake.
func startProcess(ctx context.Context, opts agent.Options, sink agent.ProviderServices, launchConfig droidLaunch) (agent.Agent, error) {
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
	if opts.WorkingDir != "" {
		args = append(args, "--cwd", opts.WorkingDir)
	}

	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     launchConfig.spec,
		BaseArgs:   args,
		SetEnv:     []string{droidDisableAutoUpdateEnv},
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		cleanupSettings()
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	a := &Agent{
		Process:         providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "droid", ShutdownGrace: launchConfig.shutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel),
		sink:            agent.NewModelProgressResetSink(sink),
		workingDir:      opts.WorkingDir,
		homeDir:         opts.HomeDir,
		clock:           quartz.NewReal(),
		launchOpts:      opts,
		launch:          launchConfig,
		cleanupSettings: cleanupSettings,
	}
	if err := a.StartCmd(); err != nil {
		cancel()
		cleanupSettings()
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	go a.ReadLines(agent.NewStdoutScanner(stdout), a.handleFrame)

	if err := a.handshake(opts); err != nil {
		cleanup()
		return nil, a.FormatStartupError("initialize_session", err)
	}
	return a, nil
}

// handshake initializes a new session or loads a saved session, then waits for
// the native reply before a caller can send input.
func (a *Agent) handshake(opts agent.Options) error {
	resumeID := strings.TrimSpace(opts.ResumeSessionID)
	method := droidMethodLoadSession
	var params any = loadSessionParams{SessionID: resumeID}
	if resumeID == "" {
		machineID, err := droidMachineID()
		if err != nil {
			return err
		}
		method = droidMethodInitializeSession
		params = initializeParams{
			Cwd:             opts.WorkingDir,
			MachineID:       machineID,
			Model:           opts.Model(),
			ReasoningEffort: opts.Effort(),
			AutonomyMode:    droidAutonomyForMode(opts.PermissionMode()),
		}
	}
	a.Mu.Lock()
	a.startupReply = make(chan error, 1)
	if resumeID != "" {
		a.sessionID = resumeID
	}
	reply := a.startupReply
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		a.startupReply = nil
		a.Mu.Unlock()
	}()
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	env := newDroidEnvelope(droidTypeRequest)
	env.ID = droidInitRequestID
	env.Method = method
	env.Params = raw
	line, err := env.Marshal()
	if err != nil {
		return err
	}
	if err := a.WriteStdin(append(line, '\n')); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.Context(), opts.EffectiveStartupTimeout())
	defer cancel()
	select {
	case err := <-reply:
		return err
	case <-a.ProcessDone():
		select {
		case err := <-reply:
			return err
		default:
		}
		return errAgentStopped
	case <-ctx.Done():
		return ctx.Err()
	}
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

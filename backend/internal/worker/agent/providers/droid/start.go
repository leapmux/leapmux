package droid

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// The session API controls autonomy at startup and during live changes.
	// --auto is ignored by stream-jsonrpc, while --skip-permissions-unsafe
	// overrides later autonomy changes for the lifetime of the process.
	if opts.Model() != "" {
		args = append(args, "--model", opts.Model())
	}
	if opts.Effort() != "" {
		args = append(args, "--reasoning-effort", opts.Effort())
	}
	if initialCwd := droidInitialCwd(opts); initialCwd != "" {
		args = append(args, "--cwd", initialCwd)
	}

	prepared, err := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     launchConfig.spec,
		BaseArgs:   args,
		SetEnv:     []string{droidDisableAutoUpdateEnv},
		WorkingDir: opts.WorkingDir,
	})
	if err != nil {
		cancel()
		cleanupSettings()
		return nil, err
	}
	cmd := prepared.Cmd

	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	pipes, err := providerkit.SetupLaunchPipes(prepared, cancel)
	if err != nil {
		cancel()
		cleanupSettings()
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	a := &Agent{
		Process:         providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "droid", ShutdownGrace: launchConfig.shutdownGrace}, pipes, ctx, cancel),
		sink:            agent.NewModelProgressResetSink(sink),
		workingDir:      opts.WorkingDir,
		homeDir:         opts.HomeDir,
		clock:           quartz.NewReal(),
		launchOpts:      opts,
		launch:          launchConfig,
		onTurnEnd:       launchConfig.onTurnEnd,
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

	go a.ReadLines(a.NewOutputScanner(stdout), a.handleFrame)

	if err := a.handshake(opts); err != nil {
		cleanup()
		phase := "initialize_session"
		if strings.TrimSpace(opts.Session.ForkID()) != "" {
			phase = "fork_session"
		}
		return nil, a.FormatStartupError(phase, err)
	}
	return a, nil
}

// droidInitialCwd returns the --cwd passed when starting Droid. A cross-directory
// fork starts in ForkSourceWorkingDir so load_session finds the cwd-keyed source
// session before switching to WorkingDir via droid.change_working_directory.
func droidInitialCwd(opts agent.Options) string {
	if strings.TrimSpace(opts.Session.ForkID()) != "" && strings.TrimSpace(opts.Session.ForkSourceWorkingDir()) != "" {
		return opts.Session.ForkSourceWorkingDir()
	}
	return opts.WorkingDir
}

// handshake initializes a new session, loads a saved session, or forks an
// existing session, then waits for the native reply before a caller can send
// input.
func (a *Agent) handshake(opts agent.Options) error {
	if err := opts.ValidateSessionSource(); err != nil {
		return err
	}
	resumeID := strings.TrimSpace(opts.Session.ResumeID())
	forkID := strings.TrimSpace(opts.Session.ForkID())
	var (
		method string
		params any
	)
	switch {
	case forkID != "":
		// Suppress replayed notifications across all three fork-handshake steps
		// (source load, fork, and forked-session load); dispatchFrame clears the
		// flag on the final leapmux-init reply before live notifications arrive.
		a.Mu.Lock()
		a.suppressForkReplay = true
		a.Mu.Unlock()
		defer func() {
			a.Mu.Lock()
			a.suppressForkReplay = false
			a.Mu.Unlock()
		}()
		forkedID, err := a.forkSessionDuringHandshake(opts, forkID)
		if err != nil {
			return err
		}
		resumeID = forkedID
		method = droidMethodLoadSession
		params = loadSessionParams{SessionID: forkedID}
	case resumeID != "":
		method = droidMethodLoadSession
		params = loadSessionParams{SessionID: resumeID}
	default:
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
	var initErr error
	select {
	case initErr = <-reply:
	case <-a.ProcessDone():
		select {
		case initErr = <-reply:
		default:
			initErr = errAgentStopped
		}
	case <-ctx.Done():
		initErr = ctx.Err()
	}
	if initErr != nil {
		return initErr
	}
	forkSourceDir := opts.Session.ForkSourceWorkingDir()
	if forkID != "" &&
		strings.TrimSpace(forkSourceDir) != "" &&
		opts.WorkingDir != "" &&
		forkSourceDir != opts.WorkingDir {
		if _, err := a.callStartupRPC(
			droidMethodChangeWorkingDirectory,
			changeWorkingDirectoryParams{WorkingDirectory: opts.WorkingDir},
			opts.EffectiveStartupTimeout(),
		); err != nil {
			return fmt.Errorf("change working directory to %q after fork: %w", opts.WorkingDir, err)
		}
	}
	return nil
}

// forkSessionDuringHandshake loads the source session into Droid (required
// because droid.fork_session operates on the active session) and invokes
// droid.fork_session, returning the newly created session ID without emitting
// the source session ID to the Worker.
func (a *Agent) forkSessionDuringHandshake(opts agent.Options, sourceSessionID string) (string, error) {
	timeout := opts.EffectiveStartupTimeout()
	if _, err := a.callStartupRPC(droidMethodLoadSession, loadSessionParams{SessionID: sourceSessionID}, timeout); err != nil {
		return "", fmt.Errorf("load source session %q before fork: %w", sourceSessionID, err)
	}
	forkResp, err := a.callStartupRPC(droidMethodForkSession, struct{}{}, timeout)
	if err != nil {
		return "", err
	}
	var result struct {
		NewSessionID string `json:"newSessionId"`
		SessionID    string `json:"sessionId"`
		Session      struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(forkResp.Result, &result); err != nil {
		return "", fmt.Errorf("read droid fork_session result: %w", err)
	}
	newID := cmp.Or(
		strings.TrimSpace(result.NewSessionID),
		strings.TrimSpace(result.SessionID),
		strings.TrimSpace(result.Session.ID),
	)
	if newID == "" {
		return "", errors.New("droid fork_session returned no new session id")
	}
	if newID == sourceSessionID {
		return "", fmt.Errorf("droid fork_session returned the source session ID %q", sourceSessionID)
	}
	return newID, nil
}

// callStartupRPC sends one JSON-RPC request during startup and awaits its reply.
func (a *Agent) callStartupRPC(method string, params any, timeout time.Duration) (droidEnvelope, error) {
	id := a.nextRequestID()
	reply := a.registerReply(id)
	defer a.unregisterReply(id)
	if err := a.writeRequest(id, method, params); err != nil {
		return droidEnvelope{}, err
	}
	resp, err := providerkit.AwaitReply(&a.Process, a.Context(), reply, method, timeout)
	if err != nil {
		return droidEnvelope{}, err
	}
	if resp.Error != nil {
		return droidEnvelope{}, resp.Error
	}
	return resp, nil
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

package commandcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start launches the native RPC host and confirms its session before input can arrive.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	registration := Registration()
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		return nil, err
	}
	directory, secret, err := createRuntimeDirectory()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	args, err := launchArgs(opts, directory)
	if err != nil {
		cancel()
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	pinnedEnv := []string{
		"TMPDIR=" + directory, "TMP=" + directory, "TEMP=" + directory,
		"COMMANDCODE_SCRATCHPAD_BASE=" + directory, "COMMANDCODE_DISABLE_CRON=1", "COMMANDCODE_DISABLE_DURABLE_CRON=1", bridgeSecretEnv + "=" + secret,
	}
	cmd, delimiter, prefix := launch.Wrap(ctx, launch.WrapSpec{Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: spec, BaseArgs: args, WorkingDir: opts.WorkingDir, SetEnv: pinnedEnv})
	cmd.Env = envutil.PinEnv(providerkit.FinalizeAgentEnv(cmd.Environ(), opts), pinnedEnv...)
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	stdout, stderr := pipes.Stdout(), pipes.Stderr()
	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "Command Code", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: delimiter, PreambleMetaPrefix: prefix}, pipes, ctx, cancel)},
		runtimeDir:     directory, bridgeSecret: secret, outputDone: make(chan struct{}),
		workingDir: opts.WorkingDir, homeDir: opts.HomeDir, clock: quartz.NewReal(),
		permissionMode: opts.PermissionMode(), tools: make(map[string]openTool), children: make(map[string]*childState),
	}
	a.sink = agent.NewModelProgressResetSink(sink)
	if err := a.StartCmd(); err != nil {
		a.cleanupRuntime()
		return nil, err
	}
	a.DrainStderr(stderr)
	go func() {
		defer close(a.outputDone)
		a.ReadOutputLoop(agent.NewStdoutScanner(stdout), a.handleOutput)
		a.finishStream()
	}()
	reply, err := a.request(methodInitialize, nil, opts.EffectiveStartupTimeout())
	if err == nil {
		err = a.applyState(reply)
	}
	if err == nil {
		err = a.confirmLaunchEffort(opts.Effort(), opts.EffectiveStartupTimeout())
	}
	if err != nil {
		a.Stop()
		_ = a.Wait()
		return nil, a.FormatStartupError(methodInitialize, err)
	}
	home := opts.HomeDir
	if home == "" {
		home = a.PreambleMetaValue("HOME")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	a.Mu.Lock()
	model := a.model
	localOnly := a.localOnly
	a.Mu.Unlock()
	models := loadModels(home, model, localOnly)
	a.Mu.Lock()
	a.homeDir, a.models = home, models
	id := a.sessionID
	bridge := a.bridge
	a.Mu.Unlock()
	if bridge == nil {
		a.Stop()
		_ = a.Wait()
		return nil, fmt.Errorf("the native Command Code compaction bridge did not start")
	}
	a.sink.UpdateSessionID(id)
	a.sink.BroadcastStatusActive(id)
	a.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
	return a, nil
}

func launchArgs(opts agent.Options, directory string) ([]string, error) {
	args := []string{"--experimental", "--rpc", "--no-auto-update", "--skip-onboarding", "--mod", filepath.Join(directory, "bridge.mjs")}
	if opts.ResumeSessionID != "" {
		id, err := (commandcodeProvider{}).ResolveResumeHandle(opts.ResumeSessionID, opts.HomeDir)
		if err != nil {
			return nil, providerkit.ResumeFailedError(opts.ResumeSessionID, err)
		}
		args = append(args, "--resume", id)
	}
	if model := opts.Model(); model != "" && model != agent.DefaultModelSentinel {
		args = append(args, "--model", model)
	}
	if effort := opts.Effort(); effort != "" && effort != agent.EffortAuto {
		args = append(args, "--effort", effort)
	}
	switch opts.PermissionMode() {
	case "", contracts.CommandCodePermissionModeDefault:
		args = append(args, "--permission-mode", "standard")
	case contracts.CommandCodePermissionModePlan:
		args = append(args, "--plan")
	case contracts.CommandCodePermissionModeBypass:
		args = append(args, "--yolo")
	default:
		return nil, fmt.Errorf("the Command Code permission mode %q is invalid", opts.PermissionMode())
	}
	return args, nil
}

func (a *Agent) applyState(raw json.RawMessage) error {
	var state stateResponse
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("decode the Command Code session: %w", err)
	}
	if state.ProtocolVersion != protocolVersion || state.Session.ID == "" || state.Session.Model == "" {
		return fmt.Errorf("the Command Code host returned an invalid session or protocol version")
	}
	if mode := state.Session.PermissionMode; mode != contracts.CommandCodePermissionModeDefault && mode != contracts.CommandCodePermissionModePlan && mode != contracts.CommandCodePermissionModeBypass {
		return fmt.Errorf("the Command Code host reported unsupported permission mode %q", mode)
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.sessionID != "" && state.Session.ID != a.sessionID {
		return fmt.Errorf("the Command Code settings reply replaced session %q with %q", a.sessionID, state.Session.ID)
	}
	a.sessionID, a.model, a.permissionMode = state.Session.ID, state.Session.Model, state.Session.PermissionMode
	a.effort = ""
	if state.Session.Effort != nil {
		a.effort = *state.Session.Effort
	}
	return nil
}

func (a *Agent) Stop() {
	a.Process.Stop()
	if a.outputDone != nil {
		<-a.outputDone
	}
	a.cleanupRuntime()
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	if a.outputDone != nil {
		<-a.outputDone
	}
	a.cleanupRuntime()
	return err
}

// confirmLaunchEffort makes the host hold the effort that the launch requested.
//
// The host applies `--effort` as a read-modify-write of its user configuration, and the first
// state that it reports after a relaunch can still hold the previous effort, or none. Command
// Code 1.74.1 reported effort `high` for a relaunch with `--effort low`. The state of the host is
// the answer that LeapMux surfaces, so the user would see a tier that they did not choose, and
// the next turn would run on it. When the state differs, this asks for the effort through
// `session/set_effort` and reads the state again.
//
// Effort `auto` and an empty effort request no tier, so nothing is sent. A refusal keeps the state
// that the host reported: the model can offer no such tier, and a refusal must not fail a startup
// that the host completed.
func (a *Agent) confirmLaunchEffort(want string, timeout time.Duration) error {
	if want == "" || want == agent.EffortAuto {
		return nil
	}
	a.Mu.Lock()
	held := a.effort
	a.Mu.Unlock()
	if held == want {
		return nil
	}
	if _, err := a.request(methodSetEffort, map[string]string{"effort": want}, timeout); err != nil {
		slog.Warn("confirm the launch effort of Command Code", "effort", want, "held", held, "error", err)
		return nil
	}
	raw, err := a.request(methodSessionState, nil, timeout)
	if err != nil {
		return fmt.Errorf("read the Command Code state after the launch effort: %w", err)
	}
	return a.applyState(raw)
}

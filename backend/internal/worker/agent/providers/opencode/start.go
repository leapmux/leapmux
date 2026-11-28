package opencode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"time"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/pathutil"
)

const (
	openCodeDisableAutoUpdateEnv = "OPENCODE_DISABLE_AUTOUPDATE"
	openCodeMovePollTag          = "opencode-session-move-poll"
	openCodeMoveInitialBackoff   = 50 * time.Millisecond
	openCodeMoveMaxBackoff       = 500 * time.Millisecond
)

var openCodeListenPattern = regexp.MustCompile(`^\s*\{\s*"url"\s*:\s*"([^"\s]+)"\s*\}\s*$`)
var openCodeServeArgs = []string{"serve", "--stdio", "--hostname", "127.0.0.1", "--port", "0"}
var openCodeServerEnvKeys = []string{"OPENCODE_PASSWORD", "OPENCODE_SERVER_PASSWORD", "OPENCODE_CLIENT", "OPENCODE_PTY_HANDOFF"}
var _ agent.StartFunc = Start

// Start opens the native stream before creating or restoring its root session.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (_ agent.Agent, retErr error) {
	ctx, cancel := context.WithCancel(ctx)
	registration := Registration()
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}
	automatic, err := autoApproveFromOptions(opts.Options)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("auto-approve: %w", err)
	}
	secret, err := providerkit.NewServerSecret()
	if err != nil {
		cancel()
		return nil, err
	}
	pins := []string{"OPENCODE_PASSWORD=" + secret, "OPENCODE_CLIENT=leapmux", openCodeDisableAutoUpdateEnv + "=1"}
	prepared, err := launch.Wrap(ctx, launch.WrapSpec{
		Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: spec, WorkingDir: opts.WorkingDir,
		BaseArgs: openCodeServeArgs, StripEnvKeys: openCodeServerEnvKeys, SetEnv: pins,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	cmd := prepared.Cmd
	cmd.Env = envutil.PinEnv(providerkit.FinalizeAgentEnv(cmd.Environ(), opts), pins...)
	pipes, err := providerkit.SetupLaunchPipes(prepared, cancel)
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, stderr := pipes.Stdout(), pipes.Stderr()
	a := &Agent{
		sink: agent.NewModelProgressResetSink(sink), rpc: openCodeRPC{timeout: opts.EffectiveAPITimeout()},
		runs: make(map[string]*openCodeRun), nativeCancel: cancel,
	}
	a.Process = providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "opencode", ShutdownGrace: registration.ShutdownGrace}, pipes, ctx, cancel)
	if err := a.StartCmd(); err != nil {
		cancel()
		return nil, err
	}
	defer func() {
		if retErr != nil {
			a.Stop()
			waitErr := a.Wait()
			retErr = errors.Join(a.FinalizeStartupCause(retErr), waitErr)
		}
	}()
	a.DrainStderr(stderr)
	listener := providerkit.NewListenWaiter(openCodeListenPattern)
	go a.ReadLines(a.NewOutputScanner(stdout), func(line []byte) {
		if !listener.Observe(line) {
			slog.Debug("OpenCode emitted a non-readiness stdout line", "agent_id", a.AgentID(), "bytes", len(line))
		}
	})
	address, err := listener.Wait(ctx, a.Clock(), a.ProcessDone(), opts.EffectiveStartupTimeout())
	if err != nil {
		return nil, a.FormatStartupError("listen", err)
	}
	a.rpc.endpoint, err = providerkit.NewHTTPEndpoint(address, providerkit.BasicAuth("opencode", secret))
	if err != nil {
		return nil, a.FormatStartupError("listen", err)
	}
	a.controls, err = newOpenCodeControls(ctx, a.rpc, a.requestRecovery)
	if err != nil {
		return nil, err
	}
	a.controls.setAutoApprove(automatic)
	stream, err := startOpenCodeStream(ctx, openCodeStreamConfig{
		rpc: a.rpc, clock: a.Clock(), agentID: a.AgentID(), processDone: a.ProcessDone(),
		dispatch: a.acceptNativeEvent, restoreSessions: a.restoreStream,
	})
	if err != nil {
		return nil, err
	}
	a.Mu.Lock()
	a.stream = stream
	a.Mu.Unlock()
	a.streamWatchDone = make(chan struct{})
	go a.watchStream()
	if err := a.stream.awaitConnected(ctx, opts.EffectiveStartupTimeout()); err != nil {
		return nil, a.FormatStartupError("event stream", err)
	}
	requestedModel := ""
	if !opts.NewSessionDefaultOptionIDs[agent.OptionIDModel] {
		requestedModel = opts.Model()
	}
	catalog, err := awaitOpenCodeCatalogWithModel(ctx, a.rpc, a.Clock(), opts.EffectiveStartupTimeout(), requestedModel)
	if err != nil {
		return nil, a.FormatStartupError("model catalog", err)
	}
	a.Mu.Lock()
	a.catalog = catalog
	a.Mu.Unlock()
	info, err := a.openRootSession(ctx, opts)
	if err != nil {
		return nil, err
	}
	a.dispatch.Lock()
	run, err := a.newRun(info, a.sink)
	if err == nil {
		a.sink.AdoptNativeSession(info.ID)
		a.Mu.Lock()
		a.root = run
		a.runs[info.ID] = run
		a.Mu.Unlock()
		if opts.Session.IsResume() || opts.Session.IsFork() {
			current := run.SettingsSnapshot().SurfacedOptions
			changes := make(optionmap.Map)
			for _, key := range []string{agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPrimaryAgent} {
				if requested := opts.Get(key); requested != "" && requested != current[key] {
					changes[key] = requested
				}
			}
			if len(changes) > 0 && !run.UpdateSettings(changes).AppliedLive {
				err = errors.New("OpenCode could not apply the requested settings to the session")
			}
		}
		// On fork startup, seed pre-fork baseline message IDs and sync session
		// metadata/usage/controls without re-persisting pre-fork transcript rows
		// that the Worker already cloned into SQLite.
		if err == nil {
			if opts.Session.IsFork() {
				err = a.restoreForkBaselineLocked(ctx, run)
			} else {
				err = a.restoreRunLocked(ctx, run)
			}
		}
	}
	a.dispatch.Unlock()
	if err != nil {
		return nil, a.FormatStartupError("session restore", err)
	}
	a.sink.ClearGoal(true)
	a.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
	a.sink.BroadcastStatusActive(info.ID)
	return a, nil
}

func openCodeSameDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return pathutil.SamePath(pathutil.Canonicalize(a), pathutil.Canonicalize(b))
}

func (a *Agent) moveForkedSession(ctx context.Context, sessionID, targetDir string, timeout time.Duration) (openCodeSessionInfo, error) {
	if err := a.rpc.moveSession(ctx, sessionID, targetDir); err != nil {
		return openCodeSessionInfo{}, err
	}
	clock := a.Clock()
	deadline := clock.Now().Add(timeout)
	delay := openCodeMoveInitialBackoff
	for {
		info, err := a.rpc.session(ctx, sessionID)
		if err != nil {
			return openCodeSessionInfo{}, err
		}
		if openCodeSameDirectory(info.Location.Directory, targetDir) {
			active, err := a.rpc.activeSessions(ctx)
			if err != nil {
				return openCodeSessionInfo{}, err
			}
			if !active[sessionID] {
				return info, nil
			}
		}
		remaining := deadline.Sub(clock.Now())
		if remaining <= 0 {
			return openCodeSessionInfo{}, fmt.Errorf("timed out waiting for OpenCode session %q to move to %q (current directory: %q)", sessionID, targetDir, info.Location.Directory)
		}
		timer := clock.NewTimer(min(delay, remaining), openCodeMovePollTag)
		select {
		case <-ctx.Done():
			timer.Stop()
			return openCodeSessionInfo{}, ctx.Err()
		case <-a.ProcessDone():
			timer.Stop()
			return openCodeSessionInfo{}, errors.New("OpenCode exited while moving the forked session")
		case <-timer.C:
		}
		delay = min(delay*2, openCodeMoveMaxBackoff)
	}
}

func (a *Agent) openRootSession(ctx context.Context, opts agent.Options) (openCodeSessionInfo, error) {
	if err := opts.ValidateSessionSource(); err != nil {
		return openCodeSessionInfo{}, err
	}
	if forkID := opts.Session.ForkID(); forkID != "" {
		info, err := a.rpc.forkSession(ctx, forkID)
		if err != nil {
			return info, fmt.Errorf("fork OpenCode session %q: %w", forkID, err)
		}
		if opts.WorkingDir != "" && !openCodeSameDirectory(info.Location.Directory, opts.WorkingDir) {
			targetDir := filepath.Clean(opts.WorkingDir)
			info, err = a.moveForkedSession(ctx, info.ID, targetDir, opts.EffectiveStartupTimeout())
			if err != nil {
				return info, fmt.Errorf("move forked OpenCode session %q to %q: %w", info.ID, targetDir, err)
			}
		}
		return info, nil
	}
	if resumeID := opts.Session.ResumeID(); resumeID != "" {
		info, err := a.rpc.session(ctx, resumeID)
		if err != nil {
			return info, providerkit.ResumeFailedError(resumeID, err)
		}
		if info.ParentID != "" {
			return info, providerkit.ResumeFailedError(resumeID, errors.New("a native subagent must be opened through its owning conversation"))
		}
		return info, nil
	}
	request := openCodeSessionCreate{Location: &openCodeLocation{Directory: opts.WorkingDir}}
	primary := opts.Get(agent.OptionIDPrimaryAgent)
	if opts.NewSessionDefaultOptionIDs[agent.OptionIDPrimaryAgent] {
		primary = ""
	}
	if primary != "" {
		if !a.catalog.offersAgent(primary) {
			return openCodeSessionInfo{}, fmt.Errorf("OpenCode does not offer agent %q", primary)
		}
		request.Agent = primary
	}
	if model := opts.Model(); model != "" {
		ref, found := a.catalog.modelRef(model)
		if !found && !opts.NewSessionDefaultOptionIDs[agent.OptionIDModel] {
			return openCodeSessionInfo{}, fmt.Errorf("OpenCode does not offer model %q", model)
		}
		if found {
			request.Model = &ref
		}
	}
	if effort := opts.Effort(); effort != "" && effort != agent.EffortAuto {
		ref, err := a.catalog.effectiveModel(request.Model, primary)
		if err != nil {
			return openCodeSessionInfo{}, err
		}
		if !a.catalog.offersEffortOption(openCodeModelKey(ref), effort) {
			if !opts.NewSessionDefaultOptionIDs[agent.OptionIDEffort] {
				return openCodeSessionInfo{}, fmt.Errorf("OpenCode does not offer effort %q for model %q", effort, openCodeModelKey(ref))
			}
		} else {
			variant, valid := openCodeVariantFromOption(effort)
			if !valid {
				return openCodeSessionInfo{}, fmt.Errorf("the OpenCode effort %q has no native variant", effort)
			}
			ref.Variant = variant
			request.Model = &ref
		}
	}
	return a.rpc.createSession(ctx, request)
}

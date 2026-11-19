package opencode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const openCodeDisableAutoUpdateEnv = "OPENCODE_DISABLE_AUTOUPDATE"

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
	cmd, delimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: spec, WorkingDir: opts.WorkingDir,
		BaseArgs: openCodeServeArgs, StripEnvKeys: openCodeServerEnvKeys, SetEnv: pins,
	})
	cmd.Env = envutil.PinEnv(providerkit.FinalizeAgentEnv(cmd.Environ(), opts), pins...)
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, stderr := pipes.Stdout(), pipes.Stderr()
	a := &Agent{
		sink: agent.NewModelProgressResetSink(sink), rpc: openCodeRPC{timeout: opts.EffectiveAPITimeout()},
		runs: make(map[string]*openCodeRun), nativeCancel: cancel,
	}
	a.Process = providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "opencode", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: delimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel)
	if err := a.StartCmd(); err != nil {
		cancel()
		return nil, err
	}
	defer func() {
		if retErr != nil {
			a.Stop()
			retErr = errors.Join(retErr, a.Wait())
		}
	}()
	a.DrainStderr(stderr)
	listener := providerkit.NewListenWaiter(openCodeListenPattern)
	go a.ReadLines(agent.NewStdoutScanner(stdout), func(line []byte) {
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
	catalog, err := awaitOpenCodeCatalog(ctx, a.rpc, a.Clock(), opts.EffectiveStartupTimeout())
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
		if opts.ResumeSessionID != "" {
			current := run.SettingsSnapshot().SurfacedOptions
			changes := make(optionmap.Map)
			for _, key := range []string{agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPrimaryAgent} {
				if requested := opts.Get(key); requested != "" && requested != current[key] {
					changes[key] = requested
				}
			}
			if len(changes) > 0 && !run.UpdateSettings(changes).AppliedLive {
				err = errors.New("OpenCode could not apply the requested settings to the resumed session")
			}
		}
		if err == nil {
			err = a.restoreRunLocked(ctx, run)
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

func (a *Agent) openRootSession(ctx context.Context, opts agent.Options) (openCodeSessionInfo, error) {
	if opts.ResumeSessionID != "" {
		info, err := a.rpc.session(ctx, opts.ResumeSessionID)
		if err != nil {
			return info, providerkit.ResumeFailedError(opts.ResumeSessionID, err)
		}
		if info.ParentID != "" {
			return info, providerkit.ResumeFailedError(opts.ResumeSessionID, errors.New("a native subagent must be opened through its owning conversation"))
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

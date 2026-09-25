package mimo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// mimoServeArgs start MiMo's server on a loopback port the server picks.
var mimoServeArgs = []string{"serve", "--hostname", "127.0.0.1", "--port", "0"}

// errCredentialRefused reports that the server refused the credential the
// worker gave it. The worker pins the credential in the process environment, and
// only a login shell profile that exports the same variables can replace it
// after that.
var errCredentialRefused = errors.New("MiMo refused LeapMux's server credential; a shell profile that exports " +
	envServerPassword + " or " + envServerUsername + " replaces the credential LeapMux sets, so remove the export")

// Start starts `mimo serve` and opens one session on it.
//
// The order matters. The event stream opens before the session exists, so no
// event of the session can be lost; the catalog loads before the settings
// resolve, because a requested model is checked against it.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}
	secret, err := providerkit.NewServerSecret()
	if err != nil {
		cancel()
		return nil, err
	}

	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       spec,
		StripEnvKeys: mimoStripEnvKeys,
		BaseArgs:     mimoServeArgs,
		WorkingDir:   opts.WorkingDir,
	})
	cmd.Env = mimoLaunchEnv(cmd.Environ(), secret, opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	a := newAgentState(agent.NewModelProgressResetSink(sink), mimoRPC{timeout: opts.EffectiveAPITimeout()}, opts.WorkingDir, quartz.NewReal())
	a.Process = providerkit.NewProcess(opts, mimoBinaryName, cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)
	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	listening := providerkit.NewListenWaiter(mimoListenPattern)
	go a.ReadLines(agent.NewStdoutScanner(stdout), func(line []byte) {
		if !listening.Observe(line) {
			slog.Debug("mimo stdout", "agent_id", a.AgentID(), "line", string(line))
		}
	})

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	timeout := opts.EffectiveStartupTimeout()
	address, err := listening.Wait(ctx, a.ProcessDone(), timeout)
	if err != nil {
		cleanup()
		return nil, a.FormatStartupError("listen", err)
	}
	endpoint, err := providerkit.NewHTTPEndpoint(address, providerkit.BasicAuth(serverUser, secret))
	if err != nil {
		cleanup()
		return nil, a.FormatStartupError("listen", err)
	}
	a.rpc.endpoint = endpoint.WithHeader(directoryHeader, opts.WorkingDir)

	if err := a.checkCredential(ctx); err != nil {
		cleanup()
		return nil, a.FormatStartupError("connect", err)
	}
	if err := a.openEventStream(ctx, timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError("event stream", err)
	}
	if err := a.loadCatalog(ctx); err != nil {
		cleanup()
		return nil, a.FormatStartupError("model catalog", err)
	}
	session, err := a.openSession(ctx, opts.ResumeSessionID)
	if err != nil {
		cleanup()
		if opts.ResumeSessionID != "" {
			return nil, err
		}
		return nil, a.FormatStartupError("session", err)
	}
	a.Mu.Lock()
	a.sessionID = session.ID
	a.Mu.Unlock()
	if opts.ResumeSessionID != "" {
		a.restoreResumedSession(ctx, session.ID)
	}
	a.applyStartupSettings(ctx, opts)

	// MiMo holds a goal in memory only, so the new process holds none, whatever
	// the agent row stored for an earlier process. The clear restates that
	// absence and writes no transcript row.
	a.sink.ClearGoal(true)
	a.sink.UpdateSessionID(session.ID)
	a.sink.BroadcastStatusActive(session.ID)
	return a, nil
}

// checkCredential asks the server for its health with the worker's credential.
// A refusal here is the one failure whose cause the worker can name, and every
// later request would fail with it.
func (a *Agent) checkCredential(ctx context.Context) error {
	health, err := a.rpc.health(ctx)
	if providerkit.IsHTTPStatus(err, http.StatusUnauthorized) {
		return errCredentialRefused
	}
	if err != nil {
		return err
	}
	slog.Debug("mimo server is up", "agent_id", a.AgentID(), "version", health.Version)
	return nil
}

// openEventStream starts the stream goroutine and waits for its first
// connection.
func (a *Agent) openEventStream(ctx context.Context, timeout time.Duration) error {
	streamCtx, streamCancel := context.WithCancel(ctx)
	a.streamCancel = streamCancel
	go a.runEventStream(streamCtx)
	timer := a.clock.NewTimer(timeout, mimoStreamConnectTimerTag)
	defer timer.Stop(mimoStreamConnectTimerTag)
	select {
	case <-a.connected:
		return nil
	case <-a.ProcessDone():
		return providerkit.ErrServerExited
	case <-timer.C:
		return fmt.Errorf("the event stream did not connect within %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loadCatalog reads the models and the agents the server offers. The models
// are required: a session with no model cannot run a turn. The agent list is
// not, and a server that cannot state it keeps the static modes.
func (a *Agent) loadCatalog(ctx context.Context) error {
	providers, err := a.rpc.configProviders(ctx)
	if err != nil {
		return err
	}
	config, err := a.rpc.config(ctx)
	if err != nil {
		slog.Warn("mimo read configuration", "agent_id", a.AgentID(), "error", err)
	}
	agents, err := a.rpc.agents(ctx)
	if err != nil {
		slog.Warn("mimo read agents", "agent_id", a.AgentID(), "error", err)
	}
	catalog := buildMiMoCatalog(providers, config, agents)
	a.Mu.Lock()
	a.catalog = catalog
	a.Mu.Unlock()
	return nil
}

// applyStartupSettings resolves the launch options against the catalog and
// applies the permission policy.
//
// A requested value the catalog cannot serve falls back to the catalog's own
// default, and the settings snapshot then reports the value that runs.
//
// The worker sets both permission switches for every policy, Ask included. MiMo
// seeds its auto-approve-delete switch from two variables of its environment,
// and the launch strips both (mimoStripEnvKeys), so the server starts with both
// switches off, which is Ask. The requests for Ask still make the two switches
// hold what LeapMux reports if a MiMo release seeds them from another source. A
// policy that the server refuses falls back to Ask, which is the narrower one.
func (a *Agent) applyStartupSettings(ctx context.Context, opts agent.Options) {
	a.Mu.Lock()
	catalog := a.catalog
	a.Mu.Unlock()
	requested := opts.Options
	current := mimoSettings{
		model:            catalog.defaultModel,
		effort:           agent.EffortAuto,
		mode:             contracts.MiMoDefaultMode,
		permissionPolicy: contracts.MiMoPermissionPolicyAsk,
	}
	next := resolveSettings(catalog, current, requested)
	if err := a.applyPermissionPolicy(ctx, next.permissionPolicy); err != nil {
		slog.Warn("mimo apply the permission policy at startup; staying on Ask", "agent_id", a.AgentID(), "policy", next.permissionPolicy, "error", err)
		if next.permissionPolicy != contracts.MiMoPermissionPolicyAsk {
			next.permissionPolicy = contracts.MiMoPermissionPolicyAsk
			// A switch that did take effect must not outlive the refusal of the other.
			if resetErr := a.applyPermissionPolicy(ctx, contracts.MiMoPermissionPolicyAsk); resetErr != nil {
				slog.Warn("mimo reset the permission switches", "agent_id", a.AgentID(), "error", resetErr)
			}
		}
	}
	a.Mu.Lock()
	a.model, a.effort, a.mode, a.permissionPolicy = next.model, next.effort, next.mode, next.permissionPolicy
	a.Mu.Unlock()
}

package cline

import (
	"context"
	"fmt"
	"os"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

var _ agent.StartFunc = Start

// Start starts a private Cline hub daemon, connects to it, and opens the
// agent's session on it.
//
// The order matters. Start reads the provider settings before the daemon
// starts, because the session states the provider and the model. The client
// registers and the stream opens before the session exists, and the client
// scopes the stream to the session the moment it exists, before the first
// message.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return start(ctx, opts, sink, startDeps{
		getenv: os.Getenv,
		clock:  quartz.NewReal(),
		newDir: newClineAgentDir,
	})
}

// startDeps are what a start reads from its surroundings. Tests replace them.
type startDeps struct {
	getenv func(string) string
	clock  quartz.Clock
	newDir func(context.Context, *agentdir.Dirs) (*agentdir.Dir, error)
}

// start is Start with its surroundings stated.
func start(ctx context.Context, opts agent.Options, sink agent.ProviderServices, deps startDeps) (agent.Agent, error) {
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		return nil, err
	}
	home := opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	getenv, home := settingsEnvironment(ctx, opts.Shell, opts.LoginShell, deps.getenv, home)
	selection, err := readProviderSelection(providerSettingsPath(getenv, home))
	if err != nil {
		return nil, err
	}

	dir, err := deps.newDir(ctx, opts.AgentDirs)
	if err != nil {
		return nil, fmt.Errorf("prepare the Cline agent directory: %w", err)
	}
	started, err := startDaemonProcess(ctx, opts, spec, dir.Path(), deps.clock)
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	endpoint, path, err := hubEndpoint(started.record)
	if err != nil {
		stopDaemonAt(started.record)
		started.process.Stop()
		_ = started.process.Wait()
		_ = dir.Close()
		return nil, err
	}

	lifeCtx, cancel := context.WithCancel(context.Background())
	workspaceRoot := gitutil.GetToplevel(ctx, opts.WorkingDir)
	if workspaceRoot == "" {
		workspaceRoot = opts.WorkingDir
	}
	a := &Agent{
		Process:       started.process,
		sink:          agent.NewModelProgressResetSink(sink),
		opts:          opts,
		dir:           dir,
		workspaceRoot: workspaceRoot,
		record:        started.record,
		endpoint:      endpoint,
		clientID:      "leapmux-" + opts.AgentID,
		selection:     selection,
		clock:         deps.clock,
		sendWait:      defaultSendWait,
		ctx:           lifeCtx,
		cancel:        cancel,
		stopped:       make(chan struct{}),
		out:           newOutputState(),
	}
	a.settings = a.launchSettings(opts)
	a.hub = newHubClient(endpoint, path, started.record.AuthToken, a.clientID, opts.AgentID, a.registration(), a.handleEvent, deps.clock)
	a.subscribe = a.hub.subscribe

	fail := func(phase string, err error) (agent.Agent, error) {
		a.Stop()
		_ = a.Wait()
		return nil, a.FormatStartupError(phase, err)
	}
	timeout := opts.EffectiveStartupTimeout()
	if err := a.hub.start(lifeCtx, timeout); err != nil {
		return fail("connect", err)
	}
	// The daemon is ready now: its own connection opened. A daemon that also
	// opens one with no token lets any local process drive the agent.
	checkCtx, checkCancel := context.WithTimeout(lifeCtx, timeout)
	err = refuseTokenlessHub(checkCtx, started.record)
	checkCancel()
	if err != nil {
		// The reason states the risk itself; the daemon's output adds nothing.
		a.Stop()
		_ = a.Wait()
		return nil, err
	}
	sessionCtx, sessionCancel := context.WithTimeout(ctx, timeout)
	defer sessionCancel()
	sessionID, err := a.openSession(sessionCtx, a.settings, opts.ResumeSessionID)
	if err != nil {
		a.Stop()
		_ = a.Wait()
		if opts.ResumeSessionID != "" {
			return nil, err
		}
		return nil, a.FormatStartupError("session", err)
	}
	a.sink.UpdateSessionID(sessionID)
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	a.sink.BroadcastStatusActive(sessionID)
	return a, nil
}

// registration is the payload of client.register.
func (a *Agent) registration() map[string]any {
	return map[string]any{
		"clientId":        a.clientID,
		"clientType":      clientType,
		"displayName":     clientDisplayName,
		"transport":       clientTransport,
		"protocolVersion": hubProtocolVersion,
		"capabilities":    []map[string]any{{"name": capabilityApprovalRespond}},
		"workspaceContext": map[string]any{
			"workspaceRoot": a.workspaceRoot,
			"cwd":           a.opts.WorkingDir,
		},
	}
}

// launchSettings resolves the launch options against the session's catalog.
// The account default -- and a model the catalog does not offer -- runs the
// model that the user's Cline settings select, or the provider's first model
// when they select none. An effort the model does not offer runs Auto, and a
// mode that is not one of the axis's runs the registration's fallback.
func (a *Agent) launchSettings(opts agent.Options) clineSettings {
	settings := clineSettings{
		model:          a.configuredModel(),
		effort:         agent.EffortAuto,
		permissionMode: Registration().PermissionDefaults.Fallback,
	}
	if model := opts.Model(); !agent.UsesAccountDefaultModel(model) && a.modelInfo(model) != nil {
		settings.model = model
	}
	if effort := opts.Effort(); effort != "" && a.modelTakesEffort(settings.model, effort) {
		settings.effort = effort
	}
	if mode := opts.PermissionMode(); validPermissionMode(mode) {
		settings.permissionMode = mode
	}
	return settings
}

// defaultPermissionMode is the mode of a new session, and of a session that
// stored none: Act, which asks before every tool that changes something.
const defaultPermissionMode = contracts.ClinePermissionModeAct

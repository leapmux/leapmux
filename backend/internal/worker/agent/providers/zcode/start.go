package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start starts a ZCode app-server and performs the startup handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Resolve the launch FIRST. A machine with no ZCode at all fails both this and the
	// catalog load below, and "ZCode is not installed on this machine" is the honest one:
	// the other tells the user to sign in with an application they do not have.
	registration := Registration()
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}

	// The app-server has no working-directory flag: it takes the workspace path in
	// every request. Wrap still sets cmd.Dir, which is what the
	// tools it runs inherit.
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   []string{"app-server", "--stdio"},
		WorkingDir: opts.WorkingDir,
	})
	// Wrap already prepended spec.PrefixArgs and seeded
	// spec.Env onto cmd, so this is the same finalization every provider runs.
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	// Read credentials before the process starts, using its finalized environment
	// and shell profile. Current desktops can sign in without config.json.
	accountEnvironment, err := resolveZCodeAccountEnvironment(ctx, opts, cmd.Environ())
	if err != nil {
		cancel()
		return nil, err
	}
	opts.HomeDir = accountEnvironment.home
	credentials := accountEnvironment.credentials
	catalog, err := loadZCodeCatalog(opts.HomeDir)
	legacyConfigurationError := err
	if err != nil && !errors.Is(err, errZCodeConfigMissing) && !errors.Is(err, errZCodeConfigUnusable) {
		cancel()
		return nil, err
	}
	storeLocation, err := zcodeToolStorePaths(ctx, agent.StoredSessionQuery{
		HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir, EnvEntries: cmd.Environ(),
		Shell: opts.Shell, LoginShell: opts.LoginShell,
	}, newZCodeStorageQuery(&spec))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("resolve the ZCode native store: %w", err)
	}

	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	a := &Agent{
		Process:                   providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "zcode", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel),
		sink:                      sink,
		workingDir:                opts.WorkingDir,
		workspace:                 zcodeWorkspaceFor(opts.WorkingDir),
		homeDir:                   accountEnvironment.home,
		catalog:                   catalog,
		legacyConfigurationError:  legacyConfigurationError,
		registryRevision:          "leapmux-" + opts.AgentID,
		builtinProviderConfigPath: accountEnvironment.builtinProviderConfigPath,
		credentials:               credentials,
		identity: zcodeClientIdentity{
			appVersion: zcodeAppVersion(zcodeInstalledScript(spec, fileExists)),
			deviceMid:  loadZCodeDeviceMid(opts.HomeDir),
		},
		startPlanHTTP:   zcodeStartPlanHTTPClient,
		projectTokens:   zcodeProjectTokens,
		mode:            contracts.ZCodeDefaultMode,
		toolInvocations: map[string]*zcodeToolInvocation{},
		pendingControls: map[string]*pendingControl{},
	}
	a.sink = zcodeProgressSink{ProviderServices: agent.NewModelProgressResetSink(a.sink)}
	a.toolTranscript = newZCodeToolTranscript(ctx, a.sink, func() zcodeToolStoreLocation {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		location := storeLocation
		location.sessionID = a.sessionID
		return location
	}, &zcodeToolTranscriptPolicy{canFinalizeRoot: a.canFinalizeZCodeTools, canFinalizeChild: a.canFinalizeZCodeChildTools})
	a.sink = a.toolTranscript
	a.model = catalog.launchModel(opts.Model())
	a.thoughtLevel = opts.Effort()
	// No goroutine reads the agent yet. The lock is the contract of the two helpers.
	a.Mu.Lock()
	if mode := opts.PermissionMode(); mode != "" {
		a.pinPermissionModeLocked(mode)
	}
	// Capture the launch request BEFORE the session exists. Opening one folds the
	// app-server's own settings over these fields, so this is the last point at which
	// what the USER asked for is still readable. applyStartupSettings takes it back.
	launchRequest := zcodeSettingsRequest{Model: a.model, ThoughtLevel: a.thoughtLevel, Mode: a.permissionModeLocked()}
	a.Mu.Unlock()

	if err := a.StartCmd(); err != nil {
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	scanner := agent.NewStdoutScanner(stdout)
	go a.ReadOutput(scanner, a.interceptResponse, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	timeout := opts.EffectiveStartupTimeout()

	if err := a.pushProviderRegistry(timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError("provider configuration", err)
	}

	if err := a.openSession(opts.ResumeSessionID, timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError("session open", err)
	}

	// Model, thought level and mode are applied AFTER the session exists, and each
	// setter reports the value the app-server settled on rather than the requested
	// one. A failure here is not fatal: the session runs on the app-server's own
	// choice, which the snapshot already recorded, and the user can change it.
	a.applyStartupSettings(launchRequest, timeout)

	if err := a.subscribe(timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError(MethodSessionSubscribe, err)
	}

	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	// a.sink, never the raw constructor parameter: a.sink is the thinking-reset wrapper
	// installed above, and a call that holds the pre-wrap reference bypasses whatever the
	// wrapper overrides.
	a.sink.BroadcastStatusActive(sessionID)

	return a, nil
}

// zcodeLaunchEnvValue reads the final value of one launch variable. The final
// duplicate is the value that an exec environment applies.
func zcodeLaunchEnvValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		entry := env[i]
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}

// pushProviderRegistry hands the app-server the model providers it may use.
func (a *Agent) pushProviderRegistry(timeout time.Duration) error {
	// The account snapshot's inputs need nothing from the app-server, so LeapMux
	// gathers them while the app-server boots and answers the legacy request
	// below. A legacy build never reads them, and stop ends a Start Plan check
	// that nothing waits for.
	account := a.prepareAccountSnapshot(timeout)
	defer account.stop()
	params := a.catalog.registryPayload(a.workspace, a.registryRevision, time.Now().UnixMilli())
	raw, err := a.sendZCodeRequest(MethodUpdateProviderRegistry, params, timeout)
	if err != nil {
		if !zcodeIsMethodNotFound(err) {
			if code, ok := zcodeErrorCode(err); ok && code == -32602 && a.legacyConfigurationError != nil {
				return a.legacyConfigurationError
			}
			return err
		}
		return a.pushAccountProviderConfig(account, timeout)
	}
	if a.legacyConfigurationError != nil {
		return a.legacyConfigurationError
	}
	var resp struct {
		Status        string `json:"status"`
		ProviderCount int    `json:"providerCount"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		// The registry was accepted (no error object) but the acknowledgement did not
		// parse. That is a diagnostic loss, not a startup failure.
		slog.Warn("zcode provider registry response unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}
	if resp.Status == "failed" {
		return fmt.Errorf("the app-server refused the provider registry")
	}
	slog.Debug("zcode provider registry applied", "agent_id", a.AgentID(), "status", resp.Status, "providers", resp.ProviderCount)
	return nil
}

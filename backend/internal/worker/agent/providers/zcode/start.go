package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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

	// ZCode's credentials are read BEFORE the process starts. Without a provider
	// registry every turn fails with a message that identifies the app-server rather
	// than the missing configuration, so the real cause is reported here instead.
	// The current desktop no longer writes config.json, so a sign-in in the
	// credential store serves without it.
	credentials := newZCodeCredentialStore(opts.HomeDir, os.Getenv)
	catalog, err := loadZCodeCatalog(opts.HomeDir)
	if err != nil {
		if err = zcodeConfigurationError(err, opts.HomeDir, credentials); err != nil {
			cancel()
			return nil, err
		}
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
		catalog:                   catalog,
		registryRevision:          "leapmux-" + opts.AgentID,
		builtinProviderConfigPath: zcodeLaunchEnvValue(cmd.Env, zcodeBuiltinProviderConfigEnv),
		credentials:               credentials,
		identity: zcodeClientIdentity{
			appVersion: zcodeAppVersion(zcodeInstalledScript(spec, fileExists)),
			deviceMid:  loadZCodeDeviceMid(opts.HomeDir),
		},
		startPlanHTTP:   zcodeStartPlanHTTPClient,
		projectTokens:   zcodeProjectTokens,
		mode:            contracts.ZCodeDefaultMode,
		toolCalls:       map[string]*zcodeToolCall{},
		pendingControls: map[string]*pendingControl{},
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)
	a.sink = newZCodeToolTranscript(ctx, a.sink, func() zcodeToolStoreLocation {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		location := storeLocation
		location.sessionID = a.sessionID
		return location
	})
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
	a.sink.UpdateSessionID(sessionID)
	a.sink.BroadcastStatusActive(sessionID)

	return a, nil
}

// zcodeConfigurationError decides whether a config.json that is missing or lists no
// usable provider stops the start. It does not when the credential store holds a
// sign-in, because the account plans then serve without config.json. It returns
// nil when the start continues. A store that LeapMux cannot read is reported as
// such, never as a store without a sign-in.
func zcodeConfigurationError(err error, homeDir string, credentials zcodeCredentialStore) error {
	missing, unusable := errors.Is(err, errZCodeConfigMissing), errors.Is(err, errZCodeConfigUnusable)
	if !missing && !unusable {
		return err
	}
	signedIn, storeErr := zcodeStoreHoldsSignIn(credentials)
	switch {
	case signedIn:
		return nil
	case missing && storeErr != nil:
		return fmt.Errorf("ZCode is not configured: %s does not exist, and LeapMux could not read a sign-in from the ZCode credential store: %w",
			zcodeConfigPath(homeDir), storeErr)
	case missing:
		return fmt.Errorf("ZCode is not configured: %s does not exist and the ZCode credential store holds no sign-in (sign in with the ZCode application once)",
			zcodeConfigPath(homeDir))
	case storeErr != nil:
		return fmt.Errorf("%w, and LeapMux could not read a sign-in from the ZCode credential store: %w", err, storeErr)
	default:
		return err
	}
}

// zcodeStoreHoldsSignIn reports whether the credential store holds a sign-in of
// either family. The error reports what kept LeapMux from reading one.
func zcodeStoreHoldsSignIn(credentials zcodeCredentialStore) (bool, error) {
	stored, err := credentials.read()
	if err != nil {
		return false, err
	}
	var errs []error
	for _, family := range []string{zcodeFamilyZAI, zcodeFamilyBigModel} {
		_, signedIn, err := stored.session(family)
		if signedIn {
			return true, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return false, errors.Join(errs...)
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
			return err
		}
		return a.pushAccountProviderConfig(account, timeout)
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

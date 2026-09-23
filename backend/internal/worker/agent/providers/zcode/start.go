package zcode

import (
	"context"
	"encoding/json"
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
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}

	// ZCode's credentials are read BEFORE the process starts. Without a provider
	// registry every turn fails with a message that identifies the app-server rather
	// than the missing configuration, so the real cause is reported here instead.
	catalog, err := loadZCodeCatalog(opts.HomeDir)
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

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		Process:                   providerkit.NewProcess(opts, "zcode", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:                      sink,
		workingDir:                opts.WorkingDir,
		workspace:                 zcodeWorkspaceFor(opts.WorkingDir),
		catalog:                   catalog,
		registryRevision:          "leapmux-" + opts.AgentID,
		builtinProviderConfigPath: zcodeLaunchEnvValue(cmd.Env, zcodeBuiltinProviderConfigEnv),
		mode:                      contracts.ZCodeDefaultMode,
		toolCalls:                 map[string]*zcodeToolCall{},
		pendingControls:           map[string]json.RawMessage{},
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)
	storeLocation := zcodeToolStorePaths(agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir})
	a.sink = newZCodeToolTranscript(ctx, a.sink, func() zcodeToolStoreLocation {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		location := storeLocation
		location.sessionID = a.sessionID
		return location
	})
	// A requested model may be spelled bare ("GLM-5.3"); the catalog resolves it to
	// the composite id the option groups carry. An unresolvable request leaves the
	// model empty, and the app-server then picks the registry default -- which the
	// settings snapshot reports back, so the user still sees the truth.
	if model, ok := catalog.resolveModelID(opts.Model()); ok {
		a.model = model
	}
	a.thoughtLevel = opts.Effort()
	if mode := opts.PermissionMode(); mode != "" {
		a.mode = mode
	}
	// Capture the launch request BEFORE the session exists. Opening one folds the
	// app-server's own settings over these fields, so this is the last point at which
	// what the USER asked for is still readable. applyStartupSettings takes it back.
	launchRequest := zcodeSettingsRequest{Model: a.model, ThoughtLevel: a.thoughtLevel, Mode: a.mode}

	if err := a.StartCmd(cmd, cancel); err != nil {
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
	params := a.catalog.registryPayload(a.workspace, a.registryRevision, time.Now().UnixMilli())
	raw, err := a.sendZCodeRequest(MethodUpdateProviderRegistry, params, timeout)
	if err != nil {
		if !zcodeIsMethodNotFound(err) {
			return err
		}
		return a.pushAccountProviderConfig(timeout)
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

// pushAccountProviderConfig applies the host-owned account snapshot that replaced
// workspace/updateProviderRegistry in ZCode 0.16.9.
func (a *Agent) pushAccountProviderConfig(timeout time.Duration) error {
	params, err := a.catalog.accountProviderPayload(a.builtinProviderConfigPath, a.registryRevision)
	if err != nil {
		return err
	}
	raw, err := a.sendZCodeRequest(MethodUpdateAccountConfig, params, timeout)
	if err != nil {
		return err
	}
	var response struct {
		ReceivedRevision string `json:"receivedRevision"`
		ProviderCount    int    `json:"providerCount"`
		Status           string `json:"status"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode ZCode account provider acknowledgement: %w", err)
	}
	if response.ReceivedRevision != params.Revision {
		return fmt.Errorf("ZCode account provider acknowledgement returned revision %q, expected %q",
			response.ReceivedRevision, params.Revision)
	}
	if response.Status != "received" && response.Status != "unchanged" {
		return fmt.Errorf("ZCode account provider acknowledgement returned status %q", response.Status)
	}
	a.Mu.Lock()
	a.accountProviderConfig = true
	a.Mu.Unlock()
	slog.Debug("zcode account provider configuration applied", "agent_id", a.AgentID(),
		"status", response.Status, "providers", response.ProviderCount)
	return nil
}

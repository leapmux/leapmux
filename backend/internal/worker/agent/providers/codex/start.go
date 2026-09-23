package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/version"
)

var _ agent.StartFunc = Start

// Start starts a Codex agent process and performs the JSON-RPC handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: []string{"CODEX_CI"},
		// Codex leaves Multi-Agent V2 and memories off by default. Enable both
		// stable features for every app-server process, independent of user config.
		BaseArgs:   codexBaseArgs(),
		WorkingDir: opts.WorkingDir,
	})

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "CODEX_CI", "CODEX_THREAD_ID")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "CODEX_CI=1")
	}
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcess(opts, "codex", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
		model:          opts.Model(),
		effort:         opts.Effort(),
		workingDir:     opts.WorkingDir,
		sink:           sink,
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)

	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}

	// Drain stderr in background.
	a.DrainStderr(stderrPipe)

	// Read stdout JSONL in background.
	scanner := agent.NewStdoutScanner(stdout)
	go a.ReadOutputLoop(scanner, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.EffectiveStartupTimeout()

	// 1. Send "initialize" request.
	initParams, err := json.Marshal(map[string]interface{}{
		"clientInfo": map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities": map[string]interface{}{
			"experimentalApi":           true,
			"optOutNotificationMethods": []string{"turn/diff/updated"},
		},
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	if _, err := a.SendRequest("initialize", json.RawMessage(initParams), timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError("initialize", err)
	}

	// 2. Send "initialized" notification.
	if err := a.SendNotification("initialized", nil); err != nil {
		cleanup()
		return nil, a.FormatStartupError("initialized notification", err)
	}

	// 3. Use the permission mode directly as the Codex approval policy.
	// The DB stores provider-native values (e.g. "never", "on-request", "untrusted" for Codex).
	a.approvalPolicy = cmp.Or(opts.PermissionMode(), DefaultApprovalPolicy)
	a.sandboxPolicy = cmp.Or(opts.Options[contracts.CodexOptionSandboxPolicy], contracts.CodexOptionDefaultSandboxPolicy)
	a.networkAccess = cmp.Or(opts.Options[contracts.CodexOptionNetworkAccess], contracts.CodexOptionDefaultNetworkAccess)
	a.collaborationMode = cmp.Or(opts.Options[contracts.CodexOptionCollaborationMode], contracts.CodexOptionDefaultCollaborationMode)
	a.serviceTier = cmp.Or(opts.Options[contracts.CodexOptionServiceTier], contracts.CodexOptionDefaultServiceTier)

	// 4. Send "thread/start" or "thread/resume" request.
	threadParams := codexThreadParams(opts.Model(), opts.WorkingDir, a.approvalPolicy, a.sandboxPolicy, a.serviceTier)

	// The method is the label that FormatStartupError prefixes the failure with.
	// startOrResumeThread makes the same choice from the same field, so the two
	// cannot disagree about what ran.
	threadMethod := "thread/start"
	if opts.ResumeSessionID != "" {
		threadMethod = "thread/resume"
	}

	// Publish the resume target BEFORE the request goes out. thread/resume
	// pushes unsolicited notifications for the thread -- among them the session
	// goal snapshot -- and the read loop is already running and serialized ahead
	// of the response. Assigning threadID only after startOrResumeThread
	// returned meant every one of those arrived while a.threadID was still "",
	// so isMainThreadID rejected them and the resumed goal was dropped.
	if opts.ResumeSessionID != "" {
		a.Mu.Lock()
		a.threadID = opts.ResumeSessionID
		a.Mu.Unlock()
		// Mark the handshake, so the unsolicited reports it triggers are read as
		// restatements rather than as events the user just caused.
		a.resumingThread.Store(true)
	}

	thread, err := a.startOrResumeThread(threadParams, opts.ResumeSessionID, timeout)
	if err != nil {
		// Clear on BOTH exits, and not with a defer. A defer here is
		// function-scoped, not block-scoped, so the flag would stay set through
		// the model query and the settings publication that follow. Every goal
		// report Codex made in that window would count as a restatement, and a
		// real transition would never reach the transcript.
		a.resumingThread.Store(false)
		cleanup()
		return nil, a.FormatStartupError(threadMethod, err)
	}
	// Under the lock: the read loop has been running since before the handshake
	// and reads threadID through isMainThreadID on every routed notification.
	a.Mu.Lock()
	a.applyThreadResult(thread)
	a.threadID = thread.ID
	a.Mu.Unlock()
	a.resumingThread.Store(false)
	sink.UpdateSessionID(thread.ID)
	sink.BroadcastStatusActive(thread.ID)

	// 5. Query available models (best-effort; don't fail startup if this fails).
	a.availableModels = a.queryAvailableModels(timeout)
	a.reconcileModelCatalog()

	// 6. Publish the active thread settings. The lifecycle response owns the
	// settings that thread/start accepts. Turn-only settings keep their requested
	// values, and an automatic effort resolves from the model catalog.
	a.publishSettings()

	return a, nil
}

func codexBaseArgs() []string {
	return []string{
		"--enable", codexMultiAgentV2Feature,
		"--enable", codexMemoriesFeature,
		"app-server",
	}
}

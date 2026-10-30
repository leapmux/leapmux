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

// The worker enables these Codex CLI features at launch.
const (
	codexMemoriesFeature          = "memories"
	codexMultiAgentV2Feature      = "multi_agent_v2"
	codexCodeModeInterruptFeature = "code_mode_interrupt"
)

// Codex methods that no contract table lists because only Go reads them. The
// opt-out list in codexOptedOutNotificationMethods uses them.
const (
	codexMethodTurnDiffUpdated               = "turn/diff/updated"
	codexMethodAccountGatewayOAuthChanged    = "account/gatewayOAuth/changed"
	codexMethodThreadReverted                = "thread/reverted"
	codexMethodThreadQueueChanged            = "thread/queue/changed"
	codexMethodThreadEnvironmentConnected    = "thread/environment/connected"
	codexMethodThreadEnvironmentDisconnected = "thread/environment/disconnected"
	codexMethodRawResponseCompleted          = "rawResponse/completed"
	codexMethodItemFileChangePatchUpdated    = "item/fileChange/patchUpdated"
	codexMethodModelRerouted                 = "model/rerouted"
)

// codexOptedOutNotificationMethods lists the Codex notifications that LeapMux
// never wants. `initialize` sends the list, so Codex writes none of these
// frames. Methods that Codex sends only after a request LeapMux never makes are
// absent: they cannot arrive.
var codexOptedOutNotificationMethods = []string{
	// The correlated revert response confirms the operation. LeapMux retains its own transcript.
	codexMethodThreadReverted,
	// One streaming diff per tool call. The tool card already shows the change.
	codexMethodTurnDiffUpdated,
	// Opaque moderation metadata. The Codex TUI ignores it.
	contracts.CodexMethodTurnModerationMetadata,
	// Account state that LeapMux does not display.
	contracts.CodexMethodAccountUpdated,
	// Account state that LeapMux does not display.
	codexMethodAccountGatewayOAuthChanged,
	// LeapMux reads the thread/start response.
	contracts.CodexMethodThreadStarted,
	// turn/started and turn/completed own the turn state.
	contracts.CodexMethodThreadStatusChanged,
	// Read cursors for multi-client apps.
	contracts.CodexMethodThreadReadStateChanged,
	// Another Codex client's queue.
	codexMethodThreadQueueChanged,
	// A remote environment. LeapMux runs local processes.
	codexMethodThreadEnvironmentConnected,
	// A remote environment. LeapMux runs local processes.
	codexMethodThreadEnvironmentDisconnected,
	// Skill discovery state has no transcript row and no session state.
	contracts.CodexMethodSkillsChanged,
	// Remote-control transport state does not describe the LeapMux session.
	contracts.CodexMethodRemoteControlStatusChanged,
	// A hook start has no outcome and no content.
	contracts.CodexMethodHookStarted,
	// The typed turn and token-usage notifications carry this state.
	codexMethodRawResponseCompleted,
	// An off-by-default streaming feature that would write one raw row per update.
	codexMethodItemFileChangePatchUpdated,
	// The paired warning explains the reroute, and the model display follows
	// thread/settings/updated.
	codexMethodModelRerouted,
}

var _ agent.StartFunc = Start

// codexInitializeParams builds the `initialize` request: the client identity
// and the capabilities the worker opts into or out of.
func codexInitializeParams() map[string]interface{} {
	return map[string]interface{}{
		"clientInfo": map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities": map[string]interface{}{
			"experimentalApi":           true,
			"optOutNotificationMethods": codexOptedOutNotificationMethods,
		},
	}
}

// Start starts a Codex agent process and performs the JSON-RPC handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	registration := Registration()
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launchSpec,
		StripEnvKeys: []string{"CODEX_CI"},
		// Enable the features that LeapMux requires for each app-server process.
		// Code mode interruption must cancel nested tools as well as the model turn.
		BaseArgs:    codexBaseArgs(),
		EnvMetadata: codexNativeEnvMetadata(),
		WorkingDir:  opts.WorkingDir,
	})

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "CODEX_CI", "CODEX_THREAD_ID")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "CODEX_CI=1")
	}
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Env, opts)

	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	// Initialize permission settings before the reader starts. The reader uses
	// them to decide standing approvals. Preamble-derived timezone state is set
	// under Mu after initialization, before the thread handshake.
	a := &Agent{
		JSONRPCProcess:    providerkit.JSONRPCProcess{Process: providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "codex", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel)},
		model:             opts.Model(),
		effort:            opts.Effort(),
		workingDir:        opts.WorkingDir,
		sink:              sink,
		permissionMode:    cmp.Or(opts.PermissionMode(), DefaultPermissionMode),
		permissionProfile: cmp.Or(opts.Options[contracts.CodexOptionPermissionProfile], contracts.CodexOptionDefaultPermissionProfile),
		collaborationMode: cmp.Or(opts.Options[contracts.CodexOptionCollaborationMode], contracts.CodexOptionDefaultCollaborationMode),
		serviceTier:       cmp.Or(opts.Options[contracts.CodexOptionServiceTier], contracts.CodexOptionDefaultServiceTier),
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)

	if err := a.StartCmd(); err != nil {
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
	initParams, err := json.Marshal(codexInitializeParams())
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	if _, err := a.SendRequest("initialize", json.RawMessage(initParams), timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError("initialize", err)
	}

	// The initialize response proves that the reader consumed the launch preamble.
	// Load its native timezone before thread resume can deliver conversation output.
	a.loadRetryTextLocation()

	// 2. Send "initialized" notification.
	if err := a.SendNotification("initialized", nil); err != nil {
		cleanup()
		return nil, a.FormatStartupError("initialized notification", err)
	}
	// Model discovery needs no thread. Its result can arrive during permission discovery or the thread handshake.
	modelCatalog := make(chan []*agent.ModelInfo, 1)
	go func() { modelCatalog <- a.queryAvailableModels(timeout) }()

	// 3. Learn which permission profiles and modes Codex allows, and settle the stored
	// values against them before thread/start sends them.
	permissions := a.discoverPermissions(opts.WorkingDir, timeout)

	// 4. Send "thread/start" or "thread/resume" request.
	threadParams := codexThreadParams(opts.Model(), opts.WorkingDir, permissions, a.serviceTier)

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
		// Stop ends the pending model request before this join.
		<-modelCatalog
		return nil, a.FormatStartupError(threadMethod, err)
	}
	// Under the lock: the read loop has been running since before the handshake
	// and reads threadID through isMainThreadID on every routed notification.
	a.Mu.Lock()
	a.applyThreadResult(thread)
	// The response states the settings that the thread holds, so Codex acknowledged
	// them.
	a.permissionsAcknowledged = true
	a.threadID = thread.ID
	a.Mu.Unlock()
	a.resumingThread.Store(false)
	sink.UpdateSessionID(thread.ID)
	sink.BroadcastStatusActive(thread.ID)

	// 5. Adopt the complete model catalog after the thread supplies its current model.
	models := <-modelCatalog
	a.Mu.Lock()
	a.availableModels = models
	a.reconcileModelCatalog()
	a.Mu.Unlock()

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
		"--enable", codexCodeModeInterruptFeature,
		"app-server",
	}
}

package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/version"
)

// The approval policy a fresh Codex agent runs on. The ids and the defaults of the
// four other axes live in contracts/codex-protocol.json, because the browser seeds a
// new agent from the same values. This one stays here: LeapMux owns the approval
// axis for every provider, and no browser code reads Codex's default for it.
const DefaultApprovalPolicy = "on-request"

// The Codex CLI features the worker turns on at launch.
const (
	codexMemoriesFeature     = "memories"
	codexMultiAgentV2Feature = "multi_agent_v2"
)

// Codex sandbox policy values.
const (
	SandboxDangerFullAccess = "danger-full-access"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxReadOnly         = "read-only"
)

// Codex network access values.
const (
	NetworkRestricted = "restricted"
	NetworkEnabled    = "enabled"
)

// Codex collaboration mode values.
const (
	CollaborationDefault = "default"
	CollaborationPlan    = "plan"
)

// Codex service tier values.
const (
	ServiceTierFast = "fast"
)

// How long a `turn/start` waits for its `turn/started` ack before it reports
// the delivery unconfirmed.
//
// This timeout limits the DELIVERY, not the turn. Codex emits `turn/started` as soon as it
// accepts the request, so this only has to cover the process being busy, not
// any model work. It is well inside the client's own RPC deadline, so an
// unacknowledged send surfaces as a stated failure rather than as the client
// giving up on a message the worker did deliver.
const turnStartAckTimeout = 10 * time.Second

// Agent manages a single Codex app-server process.
type Agent struct {
	providerkit.JSONRPCProcess // shared process lifecycle + JSON-RPC plumbing
	outputMu                   sync.Mutex

	model      string
	effort     string
	workingDir string
	sink       agent.ProviderServices
	// resumingThread is true only while a thread/resume handshake is in flight.
	//
	// It is what tells a goal RESTATEMENT from a goal EVENT. Codex marks the
	// resume snapshot with a null turnId, but it uses null for every report made
	// outside a turn -- including the acknowledgement of a thread/goal/set this
	// worker issued -- so the null alone reads a user's own action as history.
	// Atomic because the read loop consults it from its own goroutine while the
	// handshake sets it.
	resumingThread atomic.Bool

	// Codex-specific state.
	threadID string // from thread/start response
	turnID   string // currently active turn ID
	// The `turn/started` notification closes this channel after Codex accepts a
	// `turn/start`. Response timing differs across Codex versions, but this
	// notification always identifies the accepted turn.
	turnStartAck chan struct{}
	// A contextCompaction item can confirm a compact request before its JSON-RPC
	// response arrives. Non-nil only while CompactContext waits for acceptance.
	compactionStartAck chan struct{}

	approvalPolicy    string // Codex approval policy (stored as-is from DB)
	sandboxPolicy     string // Codex sandbox policy (e.g. "workspace-write")
	networkAccess     string // Codex network access ("restricted" or "enabled")
	collaborationMode string // Codex collaboration mode ("default" or "plan")
	serviceTier       string // Codex service tier ("default" or "fast")
	turnSawPlan       bool   // whether the current turn produced a plan item
	turnPlanText      string // final text of the current turn's plan item
	turnAssistantText string // final assistant message text for the current turn
	// planPromptRequestID is the plan-approval card that is still open.
	//
	// ONE plan is stored for each agent, and UpdatePlan REPLACES it, so a card from
	// an earlier turn approves a plan it never showed. The card is keyed by turn, so
	// two of them can coexist -- and the composer renders the OLDEST, which makes the
	// newer one invisible. Guarded by a.Mu.
	planPromptRequestID string
	// reasoningStreamKind records, per reasoning itemId, which reasoning sub-stream
	// ("summary" or "raw") was seen first, so the token counter counts
	// only one of them. Codex can emit both summaryTextDelta and textDelta for the
	// SAME reasoning item (they are the same generation surfaced two ways), which
	// would otherwise double-count. Locking onto whichever arrives first keeps the
	// counter moving for models that stream only one of the two.
	reasoningStreamKind   map[string]string
	reasoningRetainedKind map[string]string
	reasoningSummaryIndex map[string]int
	reasoningSummarySeen  map[string]bool
	reasoningSummaryBreak map[string]bool
	generationBuffer      providerkit.GenerationBuffer
	incompleteTools       map[string]*codexIncompleteTool
	incompleteToolOrder   uint64
	availableModels       []*agent.ModelInfo
	// collabChildren is the durable in-process route from each Codex child
	// thread to its LeapMux transcript. The route survives completed runs, so
	// a follow-up turn reuses the same transcript. ClearContext removes it,
	// because the new root thread owns a different child tree.
	collabChildren map[string]*codexChildState
	// retiredCodexThreads prevents notifications from a replaced context from
	// creating routes or transcript rows in the new context.
	retiredCodexThreads map[string]struct{}
	// collabChildItems records the child thread that owns a tool item. Output
	// deltas carry only an item ID, so this index restores the transcript route.
	// Guarded by Mu.
	collabChildItems map[string]string
	// codexSpawnPrompts holds Multi-Agent V2 spawn arguments until the matching
	// subAgentActivity supplies the child thread ID.
	codexSpawnPrompts map[string]string
	// interruptCalls coalesces concurrent interrupts for one Codex turn. A
	// successful call stays cached until the turn ends, so a late retry cannot
	// send another request for an already interrupted turn. Guarded by Mu.
	interruptCalls map[codexInterruptKey]*codexInterruptCall
}

type codexInterruptKey struct {
	threadID string
	turnID   string
}

type codexInterruptCall struct {
	done chan struct{}
	err  error
}

// Start starts a Codex agent process and performs the JSON-RPC handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, codexLocator)
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

// startOrResumeThread sends thread/start, or thread/resume when the launch
// carries a stored thread ID. It returns the thread ID and effective model
// reported by Codex. A resume that does not hold fails the whole start; see
// providerkit.ResumeFailedError.
func (a *Agent) startOrResumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (codexThreadResult, error) {
	if resumeSessionID != "" {
		threadParams["threadId"] = resumeSessionID
		// LeapMux reads transcript history from its own database.
		threadParams["excludeTurns"] = true
		return a.resumeThread(threadParams, resumeSessionID, timeout)
	}
	thread, err := a.startThread(threadParams, timeout)
	if err != nil {
		return codexThreadResult{}, err
	}
	if thread.ID == "" {
		return codexThreadResult{}, fmt.Errorf("codex thread/start: response did not contain a thread ID")
	}
	return thread, nil
}

type codexThreadResult struct {
	ID       string
	settings map[string]*string
}

type codexLifecyclePolicy uint8

const (
	codexLifecycleIgnored codexLifecyclePolicy = iota
	codexLifecycleAuthoritative
	codexLifecycleWhenAutomatic
)

func (a *Agent) applyThreadResult(result codexThreadResult) {
	for _, axis := range codexAxes {
		value, present := result.settings[axis.id]
		if !present || axis.lifecyclePolicy == codexLifecycleIgnored {
			continue
		}
		if axis.lifecyclePolicy == codexLifecycleWhenAutomatic {
			current := axis.get(a)
			if current != "" && current != agent.EffortAuto {
				continue
			}
		}
		if value == nil {
			if axis.lifecycleDefault != "" {
				axis.set(a, axis.lifecycleDefault)
			}
			continue
		}
		if *value != "" {
			axis.set(a, *value)
		}
	}
}

type codexThreadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model          string          `json:"model"`
	Effort         json.RawMessage `json:"reasoningEffort"`
	ServiceTier    json.RawMessage `json:"serviceTier"`
	ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
	Sandbox        json.RawMessage `json:"sandbox"`
}

// resumeThread sends `thread/resume` and returns the thread ID and effective
// model that Codex reported. An RPC error, a response that does not parse, and
// a response that carries no required field all fail: none of the three
// reopens the conversation.
func (a *Agent) resumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (codexThreadResult, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return codexThreadResult{}, providerkit.ResumeFailedError(resumeSessionID, fmt.Errorf("marshal thread/resume params: %w", err))
	}
	resp, err := a.SendRequest("thread/resume", paramsJSON, timeout)
	if err != nil {
		return codexThreadResult{}, providerkit.ResumeFailedError(resumeSessionID, err)
	}
	threadResult, err := parseCodexThreadResponse("thread/resume", resp)
	if err != nil {
		return codexThreadResult{}, providerkit.ResumeFailedError(resumeSessionID, err)
	}
	return threadResult, nil
}

// startThread sends `thread/start` and returns the new thread's effective model.
func (a *Agent) startThread(threadParams map[string]interface{}, timeout time.Duration) (codexThreadResult, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return codexThreadResult{}, fmt.Errorf("marshal thread/start params: %w", err)
	}
	resp, err := a.SendRequest("thread/start", paramsJSON, timeout)
	if err != nil {
		return codexThreadResult{}, err
	}
	return parseCodexThreadResponse("thread/start", resp)
}

func parseCodexThreadResponse(method string, raw json.RawMessage) (codexThreadResult, error) {
	var response codexThreadResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return codexThreadResult{}, fmt.Errorf("codex %s: failed to parse response %q: %w", method, string(raw), err)
	}
	if response.Thread.ID == "" {
		return codexThreadResult{}, fmt.Errorf("codex %s: response %q carried no thread ID", method, string(raw))
	}
	if response.Model == "" {
		return codexThreadResult{}, fmt.Errorf("codex %s: response %q carried no effective model", method, string(raw))
	}
	return newCodexThreadResult(response)
}

func newCodexThreadResult(response codexThreadResponse) (codexThreadResult, error) {
	model := response.Model
	result := codexThreadResult{
		ID:       response.Thread.ID,
		settings: map[string]*string{agent.OptionIDModel: &model},
	}
	for _, target := range []struct {
		raw   json.RawMessage
		id    string
		label string
	}{
		{response.Effort, agent.OptionIDEffort, "reasoningEffort"},
		{response.ServiceTier, contracts.CodexOptionServiceTier, "serviceTier"},
		{response.ApprovalPolicy, agent.OptionIDPermissionMode, "approvalPolicy"},
	} {
		if len(target.raw) == 0 {
			continue
		}
		if string(target.raw) == "null" {
			result.settings[target.id] = nil
			continue
		}
		var value string
		if err := json.Unmarshal(target.raw, &value); err != nil {
			// Codex can return granular approval policy as an object. LeapMux stores
			// the simple policy string, so preserve the prior value for that form.
			if target.label == "approvalPolicy" {
				continue
			}
			return codexThreadResult{}, fmt.Errorf("codex thread response field %s was not a string: %w", target.label, err)
		}
		result.settings[target.id] = &value
	}
	sandboxSettings, err := decodeCodexThreadSandbox(response.Sandbox)
	if err != nil {
		return codexThreadResult{}, err
	}
	for id, value := range sandboxSettings {
		result.settings[id] = value
	}
	return result, nil
}

// decodeCodexThreadSandbox accepts the legacy string and the current tagged
// object. The current object also reports the effective network access.
func decodeCodexThreadSandbox(raw json.RawMessage) (map[string]*string, error) {
	settings := make(map[string]*string)
	if len(raw) == 0 {
		return settings, nil
	}
	if string(raw) == "null" {
		settings[contracts.CodexOptionSandboxPolicy] = nil
		return settings, nil
	}

	var policy string
	if json.Unmarshal(raw, &policy) == nil {
		settings[contracts.CodexOptionSandboxPolicy] = &policy
		return settings, nil
	}

	var sandbox struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &sandbox); err != nil {
		return nil, fmt.Errorf("codex thread response field sandbox was invalid: %w", err)
	}

	switch sandbox.Type {
	case "dangerFullAccess":
		policy = SandboxDangerFullAccess
		network := NetworkEnabled
		settings[contracts.CodexOptionNetworkAccess] = &network
	case "workspaceWrite":
		policy = SandboxWorkspaceWrite
	case "readOnly":
		policy = SandboxReadOnly
	case "externalSandbox":
		// LeapMux has no external-sandbox option. Preserve the requested values.
		return settings, nil
	default:
		// Preserve both stored values for a future Codex sandbox variant.
		return settings, nil
	}
	settings[contracts.CodexOptionSandboxPolicy] = &policy
	return settings, nil
}

// Interrupt aborts the active Codex turn by sending a `turn/interrupt`
// JSON-RPC request with the current threadId and turnId. Codex responds to
// this request only after it emits TurnAborted, so this method waits for the
// response that confirms the turn stopped. A notification is ignored by
// Codex because the app server handles this method only as a request.
//
// Returns nil when there's nothing to interrupt (no active turn) so callers
// can invoke this safely without tracking turn lifecycle.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	threadID := a.threadID
	turnID := a.turnID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	// The answers go FIRST, as Base.Interrupt sends them: a request the runtime
	// still waits on outlives the turn that raised it. Codex is the one publisher
	// that registers a real cancel answer -- the MCP elicitation at
	// output.go's MCPElicitationMethodCodex case -- because Codex retires its
	// own approval requests but defines no outcome for an elicitation the client
	// withdraws. Without this drain that answer can never be delivered: the
	// elicitation stays blocked inside the CLI for the rest of the session, well
	// past the point the user believes the turn ended.
	a.AnswerOutstandingControlRequests(a.sink)
	if threadID == "" || turnID == "" {
		// No active turn — nothing to interrupt. Treat as benign so
		// scripts can call Interrupt unconditionally without first
		// probing turn state.
		return nil
	}
	if err := a.interruptCodexTurn(threadID, turnID); err != nil {
		return err
	}
	// The ANSWERLESS requests go last, and only once the interrupt is accepted.
	// Codex's four approval methods register a nil cancel answer, because Codex
	// retires its own approval requests -- so withdrawing one sends the CLI nothing
	// and only deletes the reader's card. Retiring them before `turn/interrupt` meant
	// that a turn/interrupt which timed out or was refused left the CLI still blocked
	// on an approval with the card already gone: the reader had no control that could
	// answer it, no later SendRawInput could route one, and the thinking indicator
	// never stopped.
	a.WithdrawAllControlRequests(a.sink)
	return nil
}

// Stop retains unfinished model output before it stops the process.
func (a *Agent) Stop() {
	a.Process.Stop()
	a.outputMu.Lock()
	a.flushAllCodexGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteCodexTools("", true, agent.MessageCompletionInterrupted)
	a.outputMu.Unlock()
	a.sink.ReportProgress(agent.ResetProgress())
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	completion := a.ProcessExitCompletion()
	a.outputMu.Lock()
	a.flushAllCodexGeneration(completion)
	a.persistIncompleteCodexTools("", true, completion)
	a.outputMu.Unlock()
	a.sink.ReportProgress(agent.ResetProgress())
	return err
}

func (a *Agent) interruptCodexTurn(threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return nil
	}
	key := codexInterruptKey{threadID: threadID, turnID: turnID}
	a.Mu.Lock()
	if a.interruptCalls == nil {
		a.interruptCalls = make(map[codexInterruptKey]*codexInterruptCall)
	}
	if existing := a.interruptCalls[key]; existing != nil {
		done := existing.done
		a.Mu.Unlock()
		<-done
		return existing.err
	}
	call := &codexInterruptCall{done: make(chan struct{})}
	a.interruptCalls[key] = call
	a.Mu.Unlock()

	params, err := json.Marshal(map[string]string{"threadId": threadID, "turnId": turnID})
	if err == nil {
		_, err = a.SendRequest("turn/interrupt", params, a.APITimeout())
	}
	if err != nil {
		err = fmt.Errorf("turn/interrupt: %w", err)
	}

	a.Mu.Lock()
	call.err = err
	close(call.done)
	if err != nil {
		delete(a.interruptCalls, key)
	}
	a.Mu.Unlock()
	return err
}

func (a *Agent) clearInterruptCallsForThread(threadID string) {
	a.Mu.Lock()
	for key := range a.interruptCalls {
		if key.threadID == threadID {
			delete(a.interruptCalls, key)
		}
	}
	a.Mu.Unlock()
}

// ClearContext sends a new thread/start on the running Codex process,
// replacing the current thread with a fresh one.
func (a *Agent) ClearContext() (string, error) {
	a.Mu.Lock()
	oldThreadID := a.threadID
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	serviceTier := a.serviceTier
	model := a.model
	workingDir := a.workingDir
	a.Mu.Unlock()

	threadParams := codexThreadParams(model, workingDir, approvalPolicy, sandboxPolicy, serviceTier)
	// Codex uses this source to distinguish a deliberate context clear from an
	// unrelated new conversation. Both sources start with empty model history.
	threadParams["sessionStartSource"] = "clear"

	thread, err := a.startThread(threadParams, a.APITimeout())
	if err != nil {
		return "", err
	}
	if thread.ID == "" {
		return "", fmt.Errorf("the new Codex thread has no ID")
	}
	a.outputMu.Lock()
	a.flushAllCodexGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteCodexTools("", true, agent.MessageCompletionInterrupted)

	a.Mu.Lock()
	if a.retiredCodexThreads == nil {
		a.retiredCodexThreads = make(map[string]struct{})
	}
	if oldThreadID != "" {
		a.retiredCodexThreads[oldThreadID] = struct{}{}
	}
	for childThreadID := range a.collabChildren {
		a.retiredCodexThreads[childThreadID] = struct{}{}
	}
	a.applyThreadResult(thread)
	a.threadID = thread.ID
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.turnAssistantText = ""
	clear(a.reasoningStreamKind)
	clear(a.reasoningRetainedKind)
	clear(a.reasoningSummaryIndex)
	clear(a.reasoningSummarySeen)
	clear(a.reasoningSummaryBreak)
	// Clear the child routes: a new root thread owns a different child tree. A
	// completed run keeps its route only while its root thread lives.
	clear(a.collabChildren)
	clear(a.collabChildItems)
	clear(a.codexSpawnPrompts)
	clear(a.incompleteTools)
	a.incompleteToolOrder = 0
	a.Mu.Unlock()
	a.generationBuffer.Reset()
	a.outputMu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())

	// A goal belongs to a THREAD, and this call replaced the thread. Codex
	// sends thread/goal/cleared only after a real removal and on a resume, so a
	// fresh thread/start reports nothing at all -- the stored goal would stay,
	// and the card would offer Pause and Clear for a thread that has no goal.
	a.sink.ClearGoal(false)

	a.sink.UpdateSessionID(thread.ID)
	return thread.ID, nil
}

// PublishTurnActive republishes the Worker-visible turn state from turnID, the
// single source. Call it after EVERY critical section that writes turnID.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and the sink deduplicates, so a redundant call costs
// nothing. A MISSING call is the only way the two can drift -- which is why
// this is a re-read and not an argument.
//
// Never called with a.Mu held: the sink broadcasts, and a broadcast can block
// on a slow transport.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.turnID != ""
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	// Each child route tracks its own turn. The background-task registry supplies
	// that state to the child tab. This method reports the main thread only.
	return providerkit.PublishSteerableTurnActiveTo(a.sink, active, seq)
}

// SendInput starts a new turn with the current settings. It refuses an active
// turn because only SteerInput can add input to that turn.
//
// SendInput sends the text and nothing else. It reads no command out of the
// text. The queue classifies "/compact" and "/summarize" before dispatch and
// calls CompactContext instead, so CompactContext is the single entry point to
// a native compaction.
//
// Do not add a second command check here. It disagrees with the one in the
// queue, which classifies only a plain user message: a control response whose
// text is "/compact" keeps its kind, reaches this method, and a check here
// starts a compaction in place of the answer that the agent waits for.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	// Read shared state under lock, then release before the blocking RPC.
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.threadID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	turnID := a.turnID
	model := a.model
	effort := a.effort
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	networkAccess := a.networkAccess
	collaborationMode := a.collaborationMode
	serviceTier := a.serviceTier
	a.Mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	input := buildCodexInputBlocks(content, agent.ClassifyAttachments(attachments))

	// Normal queue dispatch never changes the active turn. Steering is an
	// explicit queue operation through SteerInput.
	if turnID != "" {
		// Manager.SendInput republishes the flag for this refusal. See
		// Agent.PublishTurnActive for why the rule lives there.
		return fmt.Errorf("%w: %s", agent.ErrAgentBusy, turnID)
	}

	return a.sendTurnStart(threadID, input, turnSettings{
		model:             model,
		effort:            effort,
		approvalPolicy:    approvalPolicy,
		sandboxPolicy:     sandboxPolicy,
		networkAccess:     networkAccess,
		collaborationMode: collaborationMode,
		serviceTier:       serviceTier,
	})
}

func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	if threadID == "" {
		a.Mu.Unlock()
		return fmt.Errorf("codex agent has no active thread")
	}
	if a.compactionStartAck != nil {
		a.Mu.Unlock()
		return fmt.Errorf("context compaction request is already pending")
	}
	ack := make(chan struct{})
	a.compactionStartAck = ack
	a.Mu.Unlock()
	clearAck := func() {
		a.Mu.Lock()
		if a.compactionStartAck == ack {
			a.compactionStartAck = nil
		}
		a.Mu.Unlock()
	}
	params, err := json.Marshal(map[string]interface{}{"threadId": threadID})
	if err != nil {
		clearAck()
		return fmt.Errorf("marshal thread/compact/start params: %w", err)
	}
	response := make(chan error, 1)
	go func() {
		_, requestErr := a.SendRequest("thread/compact/start", params, 0)
		response <- requestErr
	}()
	timer := time.NewTimer(turnStartAckTimeout)
	defer timer.Stop()
	select {
	case <-ack:
		return nil
	case requestErr := <-response:
		clearAck()
		if requestErr != nil {
			return classifyCodexCompactionRequestError(requestErr)
		}
		return nil
	case <-timer.C:
		clearAck()
		return classifyCodexCompactionRequestError(fmt.Errorf("no response or contextCompaction start within %s", turnStartAckTimeout))
	}
}

func classifyCodexCompactionRequestError(err error) error {
	return providerkit.ClassifyJSONRPCDeliveryError("thread/compact/start", err)
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SupportsSteering always reports true. The Codex app-server accepts turn/steer
// for any active turn, so the capability needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	threadID, turnID := a.threadID, a.turnID
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" || turnID == "" {
		return agent.ErrNoActiveTurn
	}
	if err := a.sendTurnSteer(threadID, turnID, buildCodexInputBlocks(content, agent.ClassifyAttachments(attachments))); err != nil {
		return err
	}
	a.Mu.Lock()
	stillActive := a.turnID == turnID
	a.Mu.Unlock()
	if !stillActive {
		return agent.ErrNoActiveTurn
	}
	return nil
}

// buildCodexInputBlocks converts text + classified attachments into Codex's
// input format. Images use data URI format; text attachments are inlined.
func buildCodexInputBlocks(content string, classified []agent.ClassifiedAttachment) []map[string]interface{} {
	var input []map[string]interface{}
	if content != "" {
		input = append(input, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		switch attachment.Kind {
		case agent.AttachmentKindText:
			input = append(input, map[string]interface{}{
				"type": "text",
				"text": providerkit.BuildInlineTextAttachmentBlock(attachment),
			})
		case agent.AttachmentKindImage:
			input = append(input, map[string]interface{}{
				"type": "image",
				"url":  providerkit.EncodeDataURI(attachment.MIMEType, attachment.Data),
			})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			// Codex's input items have no representation for either, so the
			// attachment is omitted from the turn rather than sent as junk text.
		}
	}
	return input
}

// turnSettings groups the per-turn settings snapshotted from agent state.
type turnSettings struct {
	model             string
	effort            string
	approvalPolicy    string
	sandboxPolicy     string
	networkAccess     string
	collaborationMode string
	serviceTier       string
}

// sendTurnStart sends a turn/start request with all current settings.
func (a *Agent) sendTurnStart(
	threadID string,
	input []map[string]interface{},
	s turnSettings,
) error {
	params := map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}
	if !agent.UsesAccountDefaultModel(s.model) {
		params["model"] = s.model
	}
	if e, ok := codexEffortValue(s.effort); ok {
		params["effort"] = e
	}
	if s.approvalPolicy != "" {
		params["approvalPolicy"] = s.approvalPolicy
	}
	if sp := codexSandboxPolicyObject(s.sandboxPolicy, s.networkAccess); sp != nil {
		params["sandboxPolicy"] = sp
	}
	if cm := codexCollaborationModeObject(s.collaborationMode, s.model, s.effort); cm != nil {
		params["collaborationMode"] = cm
	}
	if st := codexServiceTierValue(s.serviceTier); st != nil {
		params["serviceTier"] = *st
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}

	// SendInput returns when Codex accepts the message. `turn/started` supplies
	// that acceptance independent of the response timing in the Codex version.
	// Register the channel before the request to prevent a missed notification.
	ack := make(chan struct{})
	a.Mu.Lock()
	a.turnStartAck = ack
	a.Mu.Unlock()
	clearAck := func() {
		a.Mu.Lock()
		if a.turnStartAck == ack {
			a.turnStartAck = nil
		}
		a.Mu.Unlock()
	}

	// turn/started owns the delivery deadline. Keep the request correlated until
	// Codex responds or the process exits, but do not hold the caller or a lock.
	requestErr := make(chan error, 1)
	go func() {
		if _, err := a.SendRequest("turn/start", paramsJSON, 0); err != nil {
			slog.Error("codex turn/start failed", "agent_id", a.AgentID(), "error", err)
			requestErr <- err
		}
	}()

	timer := time.NewTimer(turnStartAckTimeout)
	defer timer.Stop()
	select {
	case <-ack:
		// `turn/started` also set turnID for explicit steering.
		return nil
	case err := <-requestErr:
		clearAck()
		return classifyCodexTurnStartRequestError("turn/start", err)
	case <-a.ProcessDone():
		clearAck()
		return classifyCodexTurnStartRequestError("turn/start", a.ProcessExitError())
	case <-timer.C:
		// The request can still start a turn. Report uncertain delivery and stop
		// waiting for its acceptance notification.
		clearAck()
		return fmt.Errorf("%w: turn/start received no turn/started within %s", agent.ErrDeliveryUncertain, turnStartAckTimeout)
	}
}

func classifyCodexTurnStartRequestError(operation string, err error) error {
	return providerkit.ClassifyJSONRPCDeliveryError(operation, err)
}

// sendTurnSteer steers the active turn with additional user input.
func (a *Agent) sendTurnSteer(threadID, turnID string, input []map[string]interface{}) error {
	params := map[string]interface{}{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          input,
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/steer params: %w", err)
	}

	// Codex versions differ in response timing. Keep the request correlated
	// until Codex responds or the process exits.
	_, err = a.SendRequest("turn/steer", paramsJSON, 0)
	if err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agent.ErrNoActiveTurn
		}
		return providerkit.ClassifyJSONRPCDeliveryError("turn/steer", err)
	}
	return nil
}

// codexSandboxPolicyObject converts a simple sandbox policy string
// (e.g. "danger-full-access") to the tagged object format expected by
// turn/start's sandboxPolicy field (e.g. {"type": "dangerFullAccess"}).
// networkAccess is included as a boolean for workspaceWrite/readOnly or
// as a string ("restricted"/"enabled") for dangerFullAccess.
// Returns nil if the policy is empty or unrecognized.
func codexSandboxPolicyObject(policy, networkAccess string) map[string]interface{} {
	var obj map[string]interface{}
	switch policy {
	case SandboxDangerFullAccess:
		obj = map[string]interface{}{"type": "dangerFullAccess"}
	case SandboxWorkspaceWrite:
		obj = map[string]interface{}{"type": "workspaceWrite"}
	case SandboxReadOnly:
		obj = map[string]interface{}{"type": "readOnly"}
	default:
		return nil
	}
	obj["networkAccess"] = networkAccess == NetworkEnabled
	return obj
}

// codexCollaborationModeObject converts a simple collaboration mode string to
// the object format expected by turn/start's collaborationMode field.
// We send developer_instructions: null so Codex applies its built-in mode
// instructions, matching the native Codex TUI/Desktop behavior.
func codexCollaborationModeObject(mode, model, effort string) map[string]interface{} {
	if mode == "" {
		return nil
	}
	switch mode {
	case CollaborationDefault, CollaborationPlan:
	default:
		return nil
	}
	// EffortAuto (and empty) send null so the CLI applies whatever default its
	// version supports; a concrete tier is passed through.
	reasoningEffort := interface{}(nil)
	if e, ok := codexEffortValue(effort); ok {
		reasoningEffort = e
	}
	// settings.model is a required non-empty string: Codex answers an omitted field
	// with "missing field 'model'", a null with "invalid type: null, expected a
	// string", and an empty string with the same unknown-model error as any other
	// unknown id. So this field cannot carry the account-default rule that
	// codexThreadParams applies. The caller must supply a concrete model, which
	// UpdateSettings enforces by requiring a relaunch to reach the account default.
	return map[string]interface{}{
		"mode": mode,
		"settings": map[string]interface{}{
			"model":                  model,
			"reasoning_effort":       reasoningEffort,
			"developer_instructions": nil,
		},
	}
}

// codexEffortValue normalizes a stored effort for the Codex wire. EffortAuto (and
// empty) mean "let Codex pick its own default effort", so they map to ("", false)
// -- the caller omits the field / sends null; a concrete tier maps to (tier, true).
// Single source of the auto-means-omit rule for both turn/start's top-level effort
// and the nested collaborationMode reasoning_effort.
func codexEffortValue(effort string) (string, bool) {
	if effort == "" || effort == agent.EffortAuto {
		return "", false
	}
	return effort, true
}

// codexThreadParams builds the params shared by thread/start and thread/resume.
// Start and ClearContext both call it, so the launch path and the
// clear-context path construct the thread the same way and a new thread field is
// added once, here.
//
// It omits an account-default model so Codex can resolve it. It includes a concrete
// model so a resumed thread keeps its effective or user-selected model. It never
// sets threadId: startOrResumeThread adds that for the resume case.
func codexThreadParams(model, cwd, approvalPolicy, sandboxPolicy, serviceTier string) map[string]interface{} {
	params := map[string]interface{}{
		"cwd":            cwd,
		"approvalPolicy": approvalPolicy,
		"sandbox":        sandboxPolicy,
		// Request detailed summaries so app-server emits reasoning summary items.
		"config": map[string]interface{}{
			"model_reasoning_summary": "detailed",
		},
	}
	if !agent.UsesAccountDefaultModel(model) {
		params["model"] = model
	}
	if st := codexServiceTierValue(serviceTier); st != nil {
		params["serviceTier"] = *st
	}
	return params
}

// codexServiceTierValue converts a stored service tier to the turn/thread
// wire value. A nil return omits the field and keeps Codex's normal tier.
func codexServiceTierValue(tier string) *string {
	// Only the explicit "fast" tier is sent on the wire; "", the default tier, and any unknown
	// value all omit the field (nil) and keep Codex's normal tier.
	if tier == ServiceTierFast {
		return &tier
	}
	return nil
}

// codexAxis describes one Codex configuration axis. Lifecycle responses,
// OptionGroups, live updates, and provider defaults use this table.
type codexAxis struct {
	id  string
	get func(*Agent) string // reads the live value from agent state; call under a.Mu
	// set writes a (non-empty) requested value into agent state; call under a.Mu. Having
	// it on the table means "add a Codex axis = one table row" holds for the live-update
	// writes too, so a new axis can't be silently dropped from UpdateSettings while still
	// appearing in the picker via get.
	set func(*Agent, string)
	// refreshFallback derives a value that Codex computes implicitly. Call under a.Mu.
	refreshFallback func(*Agent)
	// defaultValue is the Codex-specific default resolveProviderDefaults stamps for an
	// provider option axis (sandbox/network/collaboration/service-tier). Empty for model, effort,
	// and approval, which are defaulted by the shared model/effort/permission logic.
	defaultValue string
	// lifecyclePolicy states whether thread/start and thread/resume own this axis.
	// Effort is authoritative only while its requested value is automatic.
	lifecyclePolicy  codexLifecyclePolicy
	lifecycleDefault string
}

var codexAxes = []codexAxis{
	{id: agent.OptionIDModel, get: func(a *Agent) string { return a.model }, set: func(a *Agent, v string) { a.model = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: agent.OptionIDEffort, get: func(a *Agent) string { return a.effort }, set: func(a *Agent, v string) { a.effort = v }, refreshFallback: codexEffortRefreshFallback, lifecyclePolicy: codexLifecycleWhenAutomatic, lifecycleDefault: agent.EffortAuto},
	{id: agent.OptionIDPermissionMode, get: func(a *Agent) string { return a.approvalPolicy }, set: func(a *Agent, v string) { a.approvalPolicy = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: contracts.CodexOptionSandboxPolicy, get: func(a *Agent) string { return a.sandboxPolicy }, set: func(a *Agent, v string) { a.sandboxPolicy = v }, defaultValue: contracts.CodexOptionDefaultSandboxPolicy, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: contracts.CodexOptionNetworkAccess, get: func(a *Agent) string { return a.networkAccess }, set: func(a *Agent, v string) { a.networkAccess = v }, defaultValue: contracts.CodexOptionDefaultNetworkAccess, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: contracts.CodexOptionCollaborationMode, get: func(a *Agent) string { return a.collaborationMode }, set: func(a *Agent, v string) { a.collaborationMode = v }, defaultValue: contracts.CodexOptionDefaultCollaborationMode},
	{id: contracts.CodexOptionServiceTier, get: func(a *Agent) string { return a.serviceTier }, set: func(a *Agent, v string) { a.serviceTier = v }, defaultValue: contracts.CodexOptionDefaultServiceTier, lifecyclePolicy: codexLifecycleAuthoritative, lifecycleDefault: contracts.CodexOptionDefaultServiceTier},
}

// codexEffortRefreshFallback mirrors the model preset's implicit effort default.
// It applies only while the requested value is automatic. Caller holds a.Mu.
func codexEffortRefreshFallback(a *Agent) {
	if a.effort != agent.EffortAuto {
		return
	}
	if m := agent.FindAvailableModel(a.availableModels, a.model); m != nil && m.DefaultEffort != "" {
		a.effort = m.DefaultEffort
	}
}

// codexAxisValuesLocked snapshots every axis's live value into an id->value map. Caller
// holds a.Mu.
func (a *Agent) codexAxisValuesLocked() map[string]string {
	vals := make(map[string]string, len(codexAxes))
	for _, ax := range codexAxes {
		vals[ax.id] = ax.get(a)
	}
	return vals
}

// codexOptionDefaults returns the Codex provider-option defaults (id->default), registered
// as Registration.ProviderOptionDefaults so resolveProviderDefaults stamps them
// uniformly without re-listing each axis or branching on the provider.
func codexOptionDefaults() map[string]string {
	out := make(map[string]string)
	for _, ax := range codexAxes {
		if ax.defaultValue != "" {
			out[ax.id] = ax.defaultValue
		}
	}
	return out
}

// OptionGroups returns the model and effort groups plus the static Codex
// option groups (service tier, collaboration mode, approval policy, sandbox,
// network), each overlaid with the agent's confirmed current value.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	vals := a.codexAxisValuesLocked()
	models := a.availableModels
	a.Mu.Unlock()

	groups := providerkit.ModelAndEffortGroups(models, vals[agent.OptionIDModel], vals[agent.OptionIDEffort], agent.EffortGroupLabel, nil)

	// Current values are sourced per-axis from the snapshot; the display order is
	// carried on each registered template (so a newly-registered group can't lose its
	// order or sort ahead of the model group), and providerkit.LiveGroup defaults an unsupplied
	// current to the template's default. The model/effort entries in vals are unused
	// here -- they are rendered by providerkit.ModelAndEffortGroups above.
	for _, sg := range codexStaticOptionGroups {
		groups = append(groups, providerkit.LiveGroup(sg, vals[sg.GetId()]))
	}
	return groups
}

// UpdateSettings stores new settings so the next turn/start picks them up.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	curEffort := a.effort
	curModel := a.model
	// Switching to EffortAuto can't be done live: Codex's session config
	// remembers the last reasoning_effort across turns, so simply
	// omitting the field on the next turn keeps the prior effort
	// applied. A restart is the only way to hand control back to the
	// CLI's own default.
	if agent.IsEffortAutoTransition(options[agent.OptionIDEffort], curEffort) {
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}
	// Switching to the account default can't be done live either, and for the same
	// shape of reason as the effort sentinel above. thread/start resolves an omitted
	// model, but turn/start sends the stored string as it is, and Codex rejects the
	// literal id "default" ("The 'default' model is not supported"). A relaunch runs
	// codexThreadParams again, which omits the model and lets Codex resolve it.
	// Test the sentinel EXACTLY, not UsesAccountDefaultModel: in this map an empty
	// value means "not supplied" (see the axis loop below), so the wider test would
	// demand a restart on every edit that leaves the model alone.
	if m := options[agent.OptionIDModel]; m == agent.DefaultModelSentinel && m != curModel {
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}
	// Table-driven so every axis applies the same "non-empty value overwrites" rule and
	// a newly-added axis can't be forgotten here. The effort-auto guard above stays out
	// of the loop -- it vetoes the whole update, which a per-axis setter can't express.
	//
	// Skipping an empty value does NOT violate the optionmap empty-deletes wire contract: that
	// contract is honored UPSTREAM, at the persistence/merge boundary (mergeOptions drops a cleared
	// key, resolveProviderDefaults refills the axis default), so every map that reaches UpdateSettings
	// is already a fully-resolved snapshot with no empties to clear -- the edit path also rejects an
	// empty value before it gets here (acceptExposedOptions). An empty here is therefore a phantom
	// "unset", and keeping the prior value is the correct response, not a missed clear.
	for _, ax := range codexAxes {
		if v := options[ax.id]; v != "" {
			ax.set(a, v)
		}
	}
	a.Mu.Unlock()

	a.publishSettings()
	return a.SettingsSnapshot()
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// publishSettings broadcasts the active thread and turn settings. config/read
// reports global layers and cannot confirm active overrides.
func (a *Agent) publishSettings() {
	a.Mu.Lock()
	for _, ax := range codexAxes {
		if ax.refreshFallback != nil {
			ax.refreshFallback(a)
		}
	}
	vals := a.codexAxisValuesLocked()
	a.Mu.Unlock()

	slog.Info("codex agent settings published",
		"agent_id", a.AgentID(),
		"model", vals[agent.OptionIDModel],
		"effort", vals[agent.OptionIDEffort],
		"approvalPolicy", vals[agent.OptionIDPermissionMode],
		"sandboxPolicy", vals[contracts.CodexOptionSandboxPolicy],
		"networkAccess", vals[contracts.CodexOptionNetworkAccess],
		"collaborationMode", vals[contracts.CodexOptionCollaborationMode],
		"serviceTier", vals[contracts.CodexOptionServiceTier],
	)

	a.sink.PersistSettingsRefresh(vals)
}

// queryAvailableModels sends a model/list request and converts the response.
func (a *Agent) queryAvailableModels(timeout time.Duration) []*agent.ModelInfo {
	resp, err := a.SendRequest("model/list", json.RawMessage(`{}`), timeout)
	if err != nil {
		slog.Warn("codex model/list failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}

	var result struct {
		Data []struct {
			ID                        string `json:"id"`
			Model                     string `json:"model"`
			DisplayName               string `json:"displayName"`
			IsDefault                 bool   `json:"isDefault"`
			Hidden                    bool   `json:"hidden"`
			Description               string `json:"description"`
			DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
				Description     string `json:"description"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("codex model/list unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}

	// Build a lookup from default models so we can fill in missing metadata.
	defaultsByID := make(map[string]*agent.ModelInfo, len(codexDefaultModels))
	for _, d := range codexDefaultModels {
		defaultsByID[d.Id] = d
	}

	var models []*agent.ModelInfo
	for _, m := range result.Data {
		if m.Hidden {
			continue
		}
		id := m.Model
		if id == "" {
			id = m.ID
		}
		// Reverse effort order so highest appears first, and split
		// the server description into a short label + tooltip. Prepend
		// the LeapMux-side "auto" sentinel so users can pick it from
		// the UI even though the CLI never reports it.
		raw := m.SupportedReasoningEfforts
		efforts := make([]*agent.EffortInfo, 0, len(raw)+1)
		efforts = append(efforts, codexAutoEffort())
		for i := len(raw) - 1; i >= 0; i-- {
			e := raw[i]
			efforts = append(efforts, &agent.EffortInfo{
				Id:          e.ReasoningEffort,
				Name:        providerkit.EffortLabel(e.ReasoningEffort),
				Description: e.Description,
			})
		}

		// Prefer our curated metadata over the API's, which often
		// returns the raw model ID (e.g. "gpt-5.4" instead of "GPT-5.4").
		var displayName string
		var description string
		var contextWindow int64
		if d, ok := defaultsByID[id]; ok {
			displayName = d.DisplayName
			description = d.Description
			contextWindow = d.ContextWindow
		}
		if description == "" {
			description = m.Description
		}
		if displayName == "" {
			displayName = m.DisplayName
		}
		if displayName == "" {
			displayName = codexModelDisplayName(id)
		}

		models = append(models, &agent.ModelInfo{
			Id:               id,
			DisplayName:      displayName,
			Description:      description,
			IsDefault:        m.IsDefault,
			DefaultEffort:    m.DefaultReasoningEffort,
			SupportedEfforts: efforts,
			ContextWindow:    contextWindow,
		})
	}
	return models
}

// reconcileModelCatalog repairs the two gaps between what model/list reports and
// what the picker must offer. It runs once at startup, after applyThreadResult has
// settled a.model, and it is the Codex twin of Claude's ensureSettledModelListed.
//
// Gap one: model/list never reports the account-default sentinel, unlike the
// Claude CLI, which lists it itself. Without the row a user who picks a concrete
// model can never return to "let my account decide" for the life of the tab, and
// the option would appear before the first launch and then vanish. The sentinel
// leads the list, matching the static catalog's order.
//
// Gap two: the settled model can be one model/list omits -- a model the account
// retired between the thread resuming and this query, for instance. An unlisted
// current model leaves the picker with no selected row and no effort menu, so the
// static catalog's entry is inserted at its canonical slot.
//
// No-op on an empty live list: queryAvailableModels failed or the CLI reported
// nothing, and OptionGroups then falls back to the static catalog, which already
// carries both the sentinel and every shipped model. Appending here would replace
// that full fallback with a singleton.
func (a *Agent) reconcileModelCatalog() {
	if len(a.availableModels) == 0 {
		return
	}
	if agent.FindAvailableModel(a.availableModels, agent.DefaultModelSentinel) == nil {
		if sentinel := agent.FindAvailableModel(codexDefaultModels, agent.DefaultModelSentinel); sentinel != nil {
			a.availableModels = slices.Insert(a.availableModels, 0, sentinel)
		}
	}
	if agent.UsesAccountDefaultModel(a.model) || agent.FindAvailableModel(a.availableModels, a.model) != nil {
		return
	}
	entry := agent.FindAvailableModel(codexDefaultModels, a.model)
	if entry == nil {
		return
	}
	// Drop the settled model at its slot in the static catalog's order rather than
	// at the end, so a retired model does not sort below a newer one it outranks.
	// The inserted pointer is the shared static entry, which every consumer reads
	// and none mutates -- agent.ModelOptionGroup projects it into fresh protos.
	rank := codexCanonicalModelRank(a.model)
	insertAt := len(a.availableModels)
	for i, m := range a.availableModels {
		if codexCanonicalModelRank(m.GetId()) > rank {
			insertAt = i
			break
		}
	}
	a.availableModels = slices.Insert(a.availableModels, insertAt, entry)
}

// codexCanonicalModelRank returns modelID's index in codexDefaultModels, whose
// order is the canonical picker order (the sentinel first, then newest to oldest,
// then the retired models). A model the static catalog omits ranks last, so it
// sorts after every catalog-known model.
func codexCanonicalModelRank(modelID string) int {
	if i := slices.IndexFunc(codexDefaultModels, func(m *agent.ModelInfo) bool {
		return m.GetId() == modelID
	}); i >= 0 {
		return i
	}
	return len(codexDefaultModels)
}

// codexDefaultEfforts contains all effort levels in the Codex fallback catalog.
// The order matches the menu. Each model selects a supported window of it below.
//
// Every tier the live CLI reports must appear here, so the static fallback offers
// the same menu the running session does. codexEffortsDownFrom fails at startup on
// a tier this list omits, so a forgotten tier cannot shrink a menu in silence.
var codexDefaultEfforts = buildCodexDefaultEfforts()

// codexEffortIDs states membership. effortLadder supplies the order.
// Codex offers no `ultracode` rung and no separate `off` level.
var codexEffortIDs = map[string]bool{
	"ultra": true, "max": true, agent.EffortXHigh: true, agent.EffortHigh: true,
	"medium": true, "low": true,
}

func buildCodexDefaultEfforts() []*agent.EffortInfo {
	efforts := []*agent.EffortInfo{codexAutoEffort()}
	for _, id := range providerkit.EffortLadderIDs() {
		if codexEffortIDs[id] {
			efforts = append(efforts, providerkit.EffortTier(id))
		}
	}
	return efforts
}

// codexEffortsDownFrom returns auto followed by every tier from top down to the
// weakest one Codex offers. Each model states only its strongest tier, so a menu
// cannot skip a rung or fall out of ladder order: the window comes from
// codexDefaultEfforts, which effortLadder already orders.
//
// It panics on a tier codexDefaultEfforts omits. Every argument is a literal in
// this file and codexDefaultEfforts is derived at build time, so no runtime input
// reaches it -- the panic fires on the first `go test` of this package, never on a
// running worker. A silent filter instead shortened the menu with no diagnostic.
func codexEffortsDownFrom(top string) []*agent.EffortInfo {
	tiers := codexDefaultEfforts[1:]
	for i, tier := range tiers {
		if tier.Id == top {
			// The literal has capacity 1, so append allocates a new array and the
			// returned slice never aliases codexDefaultEfforts.
			return append([]*agent.EffortInfo{codexAutoEffort()}, tiers[i:]...)
		}
	}
	panic("codex: effort tier " + top + " is not in codexDefaultEfforts")
}

// codexAutoEffort is the LeapMux-side "auto" sentinel. The CLI never reports it,
// so both the live catalog and the static fallback prepend this one value rather
// than spelling the label and the description out twice.
func codexAutoEffort() *agent.EffortInfo {
	return &agent.EffortInfo{
		Id:          agent.EffortAuto,
		Name:        providerkit.EffortLabel(agent.EffortAuto),
		Description: "Let Codex decide the appropriate effort",
	}
}

var (
	codexEffortsFromUltra = codexEffortsDownFrom("ultra")
	codexEffortsFromMax   = codexEffortsDownFrom("max")
	codexEffortsFromXHigh = codexEffortsDownFrom(agent.EffortXHigh)
)

// codexDefaultModels is the static fallback model catalog. The selectable rows
// mirror what Codex 0.152.1 reports from model/list, in its order. model/list adds
// the account-specific models, such as Daybreak, at runtime.
//
// A model the current app server no longer lists stays here Hidden rather than
// leaving the file. queryAvailableModels reads this list for the Description and
// the ContextWindow that model/list never reports, and modelDependentGroups reads
// it for a stopped agent, so a session still pinned to a retired model keeps its
// effort tiers and its context meter. The picker skips a Hidden row; a lookup by
// id still finds it.
var codexDefaultModels = []*agent.ModelInfo{
	agent.AccountDefaultModelEntry("Use the account's default Codex model"),
	{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", Description: "Reliable agentic workhorse for everyday tasks", DefaultEffort: "low", SupportedEfforts: codexEffortsFromUltra, ContextWindow: 1_050_000},
	{Id: "gpt-5.6-terra", DisplayName: "GPT-5.6-Terra", Description: "Balanced agentic coding model for everyday work", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromUltra, ContextWindow: 1_050_000},
	{Id: "gpt-5.6-luna", DisplayName: "GPT-5.6-Luna", Description: "Fast and affordable agentic coding model", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromMax, ContextWindow: 1_050_000},
	{Id: "gpt-5.5", DisplayName: "GPT-5.5", Description: "Proven previous-generation model for coding and general work", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 1_050_000},
	{Id: "gpt-5.4", DisplayName: "GPT-5.4", Description: "Strong model for everyday coding", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 1_050_000},
	{Id: "gpt-5.4-mini", DisplayName: "GPT-5.4-Mini", Description: "Small, fast, and cost-efficient model for simpler coding tasks", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000},
	{Id: "gpt-5.3-codex-spark", DisplayName: "GPT-5.3-Codex-Spark", Description: "Ultra-fast coding model", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 128_000},
	// Retired below: Codex 0.152.1 lists none of these, so they carry their last
	// known metadata and stay out of the picker.
	{Id: "gpt-5.2", DisplayName: "GPT-5.2", Description: "Optimized for professional work and long-running agents", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 256_000, Hidden: true},
	{Id: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", Description: "Frontier Codex-optimized agentic coding model", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
	{Id: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", Description: "Frontier agentic coding model", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
	{Id: "gpt-5.1-codex-max", DisplayName: "GPT-5.1 Codex Max", Description: "Codex-optimized model for deep and fast reasoning", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
	{Id: "gpt-5.1-codex-mini", DisplayName: "GPT-5.1 Codex Mini", Description: "Optimized for Codex; cheaper, faster, but less capable", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
}

// codexBinaryCandidates lists the executable names to probe for Codex, in
// preference order. The second entry is the full Rust host triple produced
// by `cargo install` on Windows when a shorter `codex` shim is absent.
var codexBinaryCandidates = []string{"codex", "codex-x86_64-pc-windows-msvc"}

// codexStaticOptionGroups holds Codex's option groups that do not depend on the
// model catalog: fast mode, workflow, approval policy, sandbox policy and network
// access. The factory registration and Agent.OptionGroups both read this one
// value, so the static fallback and a running agent offer the same template.
var codexStaticOptionGroups = []*leapmuxv1.AvailableOptionGroup{
	{
		Id:           contracts.CodexOptionServiceTier,
		Label:        "Fast Mode",
		DefaultValue: contracts.CodexOptionDefaultServiceTier,
		Mutable:      true,
		Order:        agent.OptionOrderProviderFirst,
		Options: []*leapmuxv1.AvailableOption{
			{Id: ServiceTierFast, Name: "On", Description: "Use Codex fast mode for future turns"},
			{Id: contracts.CodexOptionDefaultServiceTier, Name: "Off", Description: "Use the normal/default service tier"},
		},
	},
	{
		Id:           contracts.CodexOptionCollaborationMode,
		Label:        "Workflow",
		DefaultValue: contracts.CodexOptionDefaultCollaborationMode,
		Mutable:      true,
		Order:        agent.OptionOrderProviderSecond,
		Options: []*leapmuxv1.AvailableOption{
			{Id: CollaborationDefault, Name: "Default"},
			{Id: CollaborationPlan, Name: "Plan Mode"},
		},
	},
	{
		Id:           agent.OptionIDPermissionMode,
		Label:        "Approval Policy",
		DefaultValue: DefaultApprovalPolicy,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options: []*leapmuxv1.AvailableOption{
			{Id: "never", Name: "Full Auto"},
			{Id: DefaultApprovalPolicy, Name: "Suggest & Approve"},
			{Id: "untrusted", Name: "Auto-edit"},
		},
	},
	{
		Id:           contracts.CodexOptionSandboxPolicy,
		Label:        "Sandbox Policy",
		DefaultValue: contracts.CodexOptionDefaultSandboxPolicy,
		Mutable:      true,
		Order:        agent.OptionOrderProviderFourth,
		Options: []*leapmuxv1.AvailableOption{
			{Id: SandboxDangerFullAccess, Name: "Full Access", Description: "No filesystem restrictions"},
			{Id: SandboxWorkspaceWrite, Name: "Workspace Write", Description: "Write only within the working directory"},
			{Id: SandboxReadOnly, Name: "Read Only", Description: "No write access to the filesystem"},
		},
	},
	{
		Id:           contracts.CodexOptionNetworkAccess,
		Label:        "Network Access",
		DefaultValue: contracts.CodexOptionDefaultNetworkAccess,
		Mutable:      true,
		Order:        agent.OptionOrderProviderThird,
		Options: []*leapmuxv1.AvailableOption{
			{Id: NetworkRestricted, Name: "Restricted", Description: "No network access from the sandbox"},
			{Id: NetworkEnabled, Name: "Enabled", Description: "Allow network access from the sandbox"},
		},
	},
}

// codexLocator finds the Codex CLI on the user's PATH, the preferred name first.
var codexLocator = launch.Binaries(codexBinaryCandidates...)

// Registration states everything the worker knows about Codex before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Plugin:        codexProvider{},
		Start:         Start,
		Locator:       codexLocator,
		DefaultModels: codexDefaultModels,
		OptionGroups:  codexStaticOptionGroups,
		// model + the provider options above (static groups) + effort. The sandbox/network/
		// collaboration/service-tier axes are already static OptionGroups, so only effort
		// (built from the model catalog) needs declaring here.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// Seed the sandbox/network/collaboration/service-tier defaults into a fresh agent's
		// launch options; resolveProviderDefaults applies these for every provider uniformly.
		ProviderOptionDefaults: codexOptionDefaults(),
		// Codex has no new-session safe default: Suggest & Approve already asks.
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: DefaultApprovalPolicy,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_CODEX_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_CODEX_DEFAULT_EFFORT",
	}
}

// codexModelDisplayName generates a human-readable display name from a Codex
// model ID (e.g. "gpt-4.1-mini" → "GPT-4.1 Mini", "o4-mini" → "o4-mini"). It is
// the last fallback in queryAvailableModels: codexDefaultModels wins, then the
// name model/list reports, so this runs only for a model that neither supplies.
// Do not draw an example from codexDefaultModels -- the catalog spells the
// current models the way the CLI does ("GPT-5.4-Mini"), which this differs from.
func codexModelDisplayName(id string) string {
	prefix := ""
	rest := id
	if strings.HasPrefix(id, "gpt-") {
		prefix = "GPT-"
		rest = id[4:]
	}
	// Split remaining by hyphens, capitalize suffix parts.
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) == 1 {
		return prefix + parts[0]
	}
	// Version part stays as-is, suffix parts get title-cased.
	suffixParts := strings.Split(parts[1], "-")
	for i, p := range suffixParts {
		if len(p) > 0 {
			suffixParts[i] = providerkit.CapitalizeFirst(p)
		}
	}
	return prefix + parts[0] + " " + strings.Join(suffixParts, " ")
}

func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	handleCodexOutput(a, line)
}

// HandleOutput processes a single JSONL notification from Codex.
func (a *Agent) HandleOutput(content []byte) {
	handleCodexOutput(a, providerkit.ParseLine(content))
}

// childTurnSeq issues the ordering token for a collab CHILD's turn flag. Both
// edges arrive on the one reader goroutine, so the token only has to be
// monotonic -- the counter the main thread shares supplies that, and the Worker
// tracks the last token for each agent id separately.
func (a *Agent) childTurnSeq() uint64 {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.NextTurnSeq()
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

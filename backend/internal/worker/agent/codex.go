package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/util/version"
)

// Codex default option values.
const (
	CodexDefaultApprovalPolicy    = "on-request"
	CodexDefaultSandboxPolicy     = "workspace-write"
	CodexDefaultNetworkAccess     = "restricted"
	CodexDefaultCollaborationMode = "default"
	CodexDefaultServiceTier       = "default"
)

const (
	CodexOptionSandboxPolicy     = "sandbox_policy"
	CodexOptionNetworkAccess     = "network_access"
	CodexOptionCollaborationMode = "collaboration_mode"
	CodexOptionServiceTier       = "service_tier"
)

// Codex sandbox policy values.
const (
	CodexSandboxDangerFullAccess = "danger-full-access"
	CodexSandboxWorkspaceWrite   = "workspace-write"
	CodexSandboxReadOnly         = "read-only"
)

// Codex network access values.
const (
	CodexNetworkRestricted = "restricted"
	CodexNetworkEnabled    = "enabled"
)

// Codex collaboration mode values.
const (
	CodexCollaborationDefault = "default"
	CodexCollaborationPlan    = "plan"
)

// Codex service tier values.
const (
	CodexServiceTierFast = "fast"
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

// StringOrDefault returns value if non-empty, otherwise fallback.
func StringOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// CodexAgent manages a single Codex app-server process.
type CodexAgent struct {
	jsonrpcBase // shared process lifecycle + JSON-RPC plumbing

	model      string
	effort     string
	workingDir string
	sink       OutputSink

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
	streamingPlan     bool   // whether we've sent streamingType session info for the current plan stream
	// thinkingTokens is the per-phase generated-token estimate driving the
	// thinking-indicator counter; see thinkingTokenEstimator and thinkingResetSink.
	thinkingTokens thinkingTokenEstimator
	// reasoningStreamKind records, per reasoning itemId, which reasoning sub-stream
	// ("summary" or "raw") was seen first, so the thinking-token estimate counts
	// only one of them. Codex can emit both summaryTextDelta and textDelta for the
	// SAME reasoning item (they are the same generation surfaced two ways), which
	// would otherwise double-count. Locking onto whichever arrives first keeps the
	// counter moving for models that stream only one of the two.
	reasoningStreamKind map[string]string
	availableModels     []*ModelInfo
	collabThreadSpans   map[string]string // child thread ID -> owning spawnAgent span ID (child index)
	// collabChildItems records the child agent id that owns a streamed item
	// (commandExecution/fileChange itemID -> childID). Populated when
	// persistItemStartedChild routes the item/started to a child; consulted by
	// the output-delta handlers (which carry only an itemID, no threadID) so a
	// subagent's command output streams to its own transcript, not the parent's.
	// Guarded by mu.
	collabChildItems map[string]string
	// collabChildTitles records the spawn prompt's first line per child thread
	// (the registry + child tab title). Guarded by mu.
	collabChildTitles map[string]string
	// collabChildPrompts records the FULL spawn prompt per child thread (the
	// title above keeps only its first line). Spent when the child transcript is
	// created, so the subagent tab opens on the instruction it was given.
	collabChildPrompts pendingPrompts
	// childTurnIDs records the active turn id per child thread (for steering).
	// Guarded by mu. Cleared on child turn/completed.
	childTurnIDs map[string]string
	// childTurnStartAcks records the turn/started notification that accepts a
	// queued child input. Guarded by mu.
	childTurnStartAcks map[string]chan struct{}
	// interruptCalls coalesces concurrent interrupts for one Codex turn. A
	// successful call stays cached until the turn ends, so a late retry cannot
	// send another request for an already interrupted turn. Guarded by mu.
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

// StartCodex starts a Codex agent process and performs the JSON-RPC handshake.
func StartCodex(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	launch, err := resolveProviderLaunch(ctx, opts.Shell, opts.LoginShell, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		Launch:       launch,
		StripEnvKeys: []string{"CODEX_CI"},
		BaseArgs:     []string{"app-server"},
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "CODEX_CI", "CODEX_THREAD_ID")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "CODEX_CI=1")
	}
	cmd.Env = FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "codex", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
		model:       opts.Model(),
		effort:      opts.Effort(),
		workingDir:  opts.WorkingDir,
		sink:        sink,
	}
	// Reset the thinking-token estimate centrally at every frontend-clear boundary.
	a.sink = newThinkingResetSink(a.sink, &a.thinkingTokens)

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	// Drain stderr in background.
	a.drainStderr(stderrPipe)

	// Read stdout JSONL in background.
	scanner := newStdoutScanner(stdout)
	go a.readOutputLoop(scanner, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

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
	if _, err := a.sendRequest("initialize", json.RawMessage(initParams), timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	// 2. Send "initialized" notification.
	if err := a.sendNotification("initialized", nil); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialized notification", err)
	}

	// 3. Use the permission mode directly as the Codex approval policy.
	// The DB stores provider-native values (e.g. "never", "on-request", "untrusted" for Codex).
	a.approvalPolicy = StringOrDefault(opts.PermissionMode(), CodexDefaultApprovalPolicy)
	a.sandboxPolicy = StringOrDefault(opts.Options[CodexOptionSandboxPolicy], CodexDefaultSandboxPolicy)
	a.networkAccess = StringOrDefault(opts.Options[CodexOptionNetworkAccess], CodexDefaultNetworkAccess)
	a.collaborationMode = StringOrDefault(opts.Options[CodexOptionCollaborationMode], CodexDefaultCollaborationMode)
	a.serviceTier = StringOrDefault(opts.Options[CodexOptionServiceTier], CodexDefaultServiceTier)

	// 4. Send "thread/start" or "thread/resume" request.
	threadParams := codexThreadParams(opts.Model(), opts.WorkingDir, a.approvalPolicy, a.sandboxPolicy, a.serviceTier)

	// The method is the label that formatStartupError prefixes the failure with.
	// startOrResumeThread makes the same choice from the same field, so the two
	// cannot disagree about what ran.
	threadMethod := "thread/start"
	if opts.ResumeSessionID != "" {
		threadMethod = "thread/resume"
	}

	thread, err := a.startOrResumeThread(threadParams, opts.ResumeSessionID, timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError(threadMethod, err)
	}
	a.applyThreadResult(thread)
	a.threadID = thread.ID
	sink.UpdateSessionID(a.threadID)
	sink.BroadcastStatusActive(a.threadID)

	// 5. Query available models (best-effort; don't fail startup if this fails).
	a.availableModels = a.queryAvailableModels(timeout)
	a.reconcileModelCatalog()

	// 6. Publish the active thread settings. The lifecycle response owns the
	// settings that thread/start accepts. Turn-only settings keep their requested
	// values, and an automatic effort resolves from the model catalog.
	a.publishSettings()

	return a, nil
}

// startOrResumeThread sends thread/start, or thread/resume when the launch
// carries a stored thread ID. It returns the thread ID and effective model
// reported by Codex. A resume that does not hold fails the whole start; see
// resumeFailedError.
func (a *CodexAgent) startOrResumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (codexThreadResult, error) {
	if resumeSessionID != "" {
		threadParams["threadId"] = resumeSessionID
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

func (a *CodexAgent) applyThreadResult(result codexThreadResult) {
	for _, axis := range codexAxes {
		value, present := result.settings[axis.id]
		if !present || axis.lifecyclePolicy == codexLifecycleIgnored {
			continue
		}
		if axis.lifecyclePolicy == codexLifecycleWhenAutomatic {
			current := axis.get(a)
			if current != "" && current != EffortAuto {
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
func (a *CodexAgent) resumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (codexThreadResult, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return codexThreadResult{}, resumeFailedError(resumeSessionID, fmt.Errorf("marshal thread/resume params: %w", err))
	}
	resp, err := a.sendRequest("thread/resume", paramsJSON, timeout)
	if err != nil {
		return codexThreadResult{}, resumeFailedError(resumeSessionID, err)
	}
	threadResult, err := parseCodexThreadResponse("thread/resume", resp)
	if err != nil {
		return codexThreadResult{}, resumeFailedError(resumeSessionID, err)
	}
	return threadResult, nil
}

// startThread sends `thread/start` and returns the new thread's effective model.
func (a *CodexAgent) startThread(threadParams map[string]interface{}, timeout time.Duration) (codexThreadResult, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return codexThreadResult{}, fmt.Errorf("marshal thread/start params: %w", err)
	}
	resp, err := a.sendRequest("thread/start", paramsJSON, timeout)
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
		settings: map[string]*string{OptionIDModel: &model},
	}
	for _, target := range []struct {
		raw   json.RawMessage
		id    string
		label string
	}{
		{response.Effort, OptionIDEffort, "reasoningEffort"},
		{response.ServiceTier, CodexOptionServiceTier, "serviceTier"},
		{response.ApprovalPolicy, OptionIDPermissionMode, "approvalPolicy"},
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
		settings[CodexOptionSandboxPolicy] = nil
		return settings, nil
	}

	var policy string
	if json.Unmarshal(raw, &policy) == nil {
		settings[CodexOptionSandboxPolicy] = &policy
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
		policy = CodexSandboxDangerFullAccess
		network := CodexNetworkEnabled
		settings[CodexOptionNetworkAccess] = &network
	case "workspaceWrite":
		policy = CodexSandboxWorkspaceWrite
	case "readOnly":
		policy = CodexSandboxReadOnly
	case "externalSandbox":
		// LeapMux has no external-sandbox option. Preserve the requested values.
		return settings, nil
	default:
		// Preserve both stored values for a future Codex sandbox variant.
		return settings, nil
	}
	settings[CodexOptionSandboxPolicy] = &policy
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
func (a *CodexAgent) Interrupt() error {
	a.mu.Lock()
	stopped := a.stopped
	threadID := a.threadID
	turnID := a.turnID
	a.mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" || turnID == "" {
		// No active turn — nothing to interrupt. Treat as benign so
		// scripts can call Interrupt unconditionally without first
		// probing turn state.
		return nil
	}
	return a.interruptCodexTurn(threadID, turnID)
}

func (a *CodexAgent) interruptCodexTurn(threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return nil
	}
	key := codexInterruptKey{threadID: threadID, turnID: turnID}
	a.mu.Lock()
	if a.interruptCalls == nil {
		a.interruptCalls = make(map[codexInterruptKey]*codexInterruptCall)
	}
	if existing := a.interruptCalls[key]; existing != nil {
		done := existing.done
		a.mu.Unlock()
		<-done
		return existing.err
	}
	call := &codexInterruptCall{done: make(chan struct{})}
	a.interruptCalls[key] = call
	a.mu.Unlock()

	params, err := json.Marshal(map[string]string{"threadId": threadID, "turnId": turnID})
	if err == nil {
		_, err = a.sendRequest("turn/interrupt", params, a.APITimeout())
	}
	if err != nil {
		err = fmt.Errorf("turn/interrupt: %w", err)
	}

	a.mu.Lock()
	call.err = err
	close(call.done)
	if err != nil {
		delete(a.interruptCalls, key)
	}
	a.mu.Unlock()
	return err
}

func (a *CodexAgent) clearInterruptCallsForThread(threadID string) {
	a.mu.Lock()
	for key := range a.interruptCalls {
		if key.threadID == threadID {
			delete(a.interruptCalls, key)
		}
	}
	a.mu.Unlock()
}

// ClearContext sends a new thread/start on the running Codex process,
// replacing the current thread with a fresh one.
func (a *CodexAgent) ClearContext() (string, bool) {
	a.mu.Lock()
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	serviceTier := a.serviceTier
	model := a.model
	workingDir := a.workingDir
	a.mu.Unlock()

	threadParams := codexThreadParams(model, workingDir, approvalPolicy, sandboxPolicy, serviceTier)
	// Codex uses this source to distinguish a deliberate context clear from an
	// unrelated new conversation. Both sources start with empty model history.
	threadParams["sessionStartSource"] = "clear"

	thread, err := a.startThread(threadParams, a.APITimeout())
	if err != nil || thread.ID == "" {
		slog.Error("codex ClearContext: thread/start failed", "agent_id", a.agentID, "error", err)
		return "", false
	}

	a.mu.Lock()
	a.applyThreadResult(thread)
	a.threadID = thread.ID
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.turnAssistantText = ""
	a.streamingPlan = false
	clear(a.reasoningStreamKind)
	// Clear the collab child index: a new thread means prior child threads are
	// gone. Entries are otherwise only removed on a final collab status.
	clear(a.collabThreadSpans)
	clear(a.collabChildItems)
	clear(a.collabChildTitles)
	a.collabChildPrompts.clear()
	clear(a.childTurnIDs)
	clear(a.childTurnStartAcks)
	a.mu.Unlock()
	// The thread was replaced; drop any in-flight thinking-token estimate so it
	// doesn't leak into the new context (mirrors acpBase.ClearContext). The next
	// turn/started also resets, but resetting here keeps every provider's context
	// clear consistent rather than relying on that follow-up.
	a.thinkingTokens.reset()

	a.sink.UpdateSessionID(thread.ID)
	return thread.ID, true
}

// SendInput starts a new turn with the current settings. It refuses an active
// turn because only SteerInput can add input to that turn.
func (a *CodexAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	if command := strings.TrimSpace(content); (command == "/compact" || command == "/summarize") && len(attachments) == 0 {
		return a.CompactContext()
	}
	// Read shared state under lock, then release before the blocking RPC.
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
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
	a.mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	input := buildCodexInputBlocks(content, classifyAttachments(attachments))

	// Normal queue dispatch never changes the active turn. Steering is an
	// explicit queue operation through SteerInput.
	if turnID != "" {
		return fmt.Errorf("%w: %s", ErrNoActiveTurn, turnID)
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

func (a *CodexAgent) CompactContext() error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	if threadID == "" {
		a.mu.Unlock()
		return fmt.Errorf("codex agent has no active thread")
	}
	if a.compactionStartAck != nil {
		a.mu.Unlock()
		return fmt.Errorf("context compaction request is already pending")
	}
	ack := make(chan struct{})
	a.compactionStartAck = ack
	a.mu.Unlock()
	clearAck := func() {
		a.mu.Lock()
		if a.compactionStartAck == ack {
			a.compactionStartAck = nil
		}
		a.mu.Unlock()
	}
	params, err := json.Marshal(map[string]interface{}{"threadId": threadID})
	if err != nil {
		clearAck()
		return fmt.Errorf("marshal thread/compact/start params: %w", err)
	}
	response := make(chan error, 1)
	go func() {
		_, requestErr := a.sendRequest("thread/compact/start", params, 0)
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
	return classifyJSONRPCDeliveryError("thread/compact/start", err)
}

func (a *CodexAgent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	threadID, turnID := a.threadID, a.turnID
	stopped := a.stopped
	a.mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" || turnID == "" {
		return ErrNoActiveTurn
	}
	if err := a.sendTurnSteer(threadID, turnID, buildCodexInputBlocks(content, classifyAttachments(attachments))); err != nil {
		return err
	}
	a.mu.Lock()
	stillActive := a.turnID == turnID
	a.mu.Unlock()
	if !stillActive {
		return ErrNoActiveTurn
	}
	return nil
}

func (a *CodexAgent) InputReady() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.stopped && a.threadID != "" && a.turnID == ""
}

// buildCodexInputBlocks converts text + classified attachments into Codex's
// input format. Images use data URI format; text attachments are inlined.
func buildCodexInputBlocks(content string, classified []classifiedAttachment) []map[string]interface{} {
	var input []map[string]interface{}
	if content != "" {
		input = append(input, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		switch attachment.kind {
		case attachmentKindText:
			input = append(input, map[string]interface{}{
				"type": "text",
				"text": buildInlineTextAttachmentBlock(attachment),
			})
		case attachmentKindImage:
			input = append(input, map[string]interface{}{
				"type": "image",
				"url":  encodeDataURI(attachment.mimeType, attachment.data),
			})
		case attachmentKindPDF, attachmentKindBinary:
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
func (a *CodexAgent) sendTurnStart(
	threadID string,
	input []map[string]interface{},
	s turnSettings,
) error {
	params := map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}
	if !UsesAccountDefaultModel(s.model) {
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
	a.mu.Lock()
	a.turnStartAck = ack
	a.mu.Unlock()
	clearAck := func() {
		a.mu.Lock()
		if a.turnStartAck == ack {
			a.turnStartAck = nil
		}
		a.mu.Unlock()
	}

	// turn/started owns the delivery deadline. Keep the request correlated until
	// Codex responds or the process exits, but do not hold the caller or a lock.
	requestErr := make(chan error, 1)
	go func() {
		if _, err := a.sendRequest("turn/start", paramsJSON, 0); err != nil {
			slog.Error("codex turn/start failed", "agent_id", a.agentID, "error", err)
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
	case <-a.processDone:
		clearAck()
		return classifyCodexTurnStartRequestError("turn/start", a.processExitError())
	case <-timer.C:
		// The request can still start a turn. Report uncertain delivery and stop
		// waiting for its acceptance notification.
		clearAck()
		return fmt.Errorf("%w: turn/start received no turn/started within %s", ErrDeliveryUncertain, turnStartAckTimeout)
	}
}

func classifyCodexTurnStartRequestError(operation string, err error) error {
	return classifyJSONRPCDeliveryError(operation, err)
}

// sendTurnSteer steers the active turn with additional user input.
func (a *CodexAgent) sendTurnSteer(threadID, turnID string, input []map[string]interface{}) error {
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
	_, err = a.sendRequest("turn/steer", paramsJSON, 0)
	if err != nil {
		if hasJSONRPCErrorCode(err, -32600, -32602) {
			return ErrNoActiveTurn
		}
		return classifyJSONRPCDeliveryError("turn/steer", err)
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
	case CodexSandboxDangerFullAccess:
		obj = map[string]interface{}{"type": "dangerFullAccess"}
	case CodexSandboxWorkspaceWrite:
		obj = map[string]interface{}{"type": "workspaceWrite"}
	case CodexSandboxReadOnly:
		obj = map[string]interface{}{"type": "readOnly"}
	default:
		return nil
	}
	obj["networkAccess"] = networkAccess == CodexNetworkEnabled
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
	case CodexCollaborationDefault, CodexCollaborationPlan:
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
	if effort == "" || effort == EffortAuto {
		return "", false
	}
	return effort, true
}

// codexThreadParams builds the params shared by thread/start and thread/resume.
// StartCodex and ClearContext both call it, so the launch path and the
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
	}
	if !UsesAccountDefaultModel(model) {
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
	if tier == CodexServiceTierFast {
		return &tier
	}
	return nil
}

// codexAxis describes one Codex configuration axis. Lifecycle responses,
// OptionGroups, live updates, and provider defaults use this table.
type codexAxis struct {
	id  string
	get func(*CodexAgent) string // reads the live value from agent state; call under a.mu
	// set writes a (non-empty) requested value into agent state; call under a.mu. Having
	// it on the table means "add a Codex axis = one table row" holds for the live-update
	// writes too, so a new axis can't be silently dropped from UpdateSettings while still
	// appearing in the picker via get.
	set func(*CodexAgent, string)
	// refreshFallback derives a value that Codex computes implicitly. Call under a.mu.
	refreshFallback func(*CodexAgent)
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
	{id: OptionIDModel, get: func(a *CodexAgent) string { return a.model }, set: func(a *CodexAgent, v string) { a.model = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: OptionIDEffort, get: func(a *CodexAgent) string { return a.effort }, set: func(a *CodexAgent, v string) { a.effort = v }, refreshFallback: codexEffortRefreshFallback, lifecyclePolicy: codexLifecycleWhenAutomatic, lifecycleDefault: EffortAuto},
	{id: OptionIDPermissionMode, get: func(a *CodexAgent) string { return a.approvalPolicy }, set: func(a *CodexAgent, v string) { a.approvalPolicy = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: CodexOptionSandboxPolicy, get: func(a *CodexAgent) string { return a.sandboxPolicy }, set: func(a *CodexAgent, v string) { a.sandboxPolicy = v }, defaultValue: CodexDefaultSandboxPolicy, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: CodexOptionNetworkAccess, get: func(a *CodexAgent) string { return a.networkAccess }, set: func(a *CodexAgent, v string) { a.networkAccess = v }, defaultValue: CodexDefaultNetworkAccess, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: CodexOptionCollaborationMode, get: func(a *CodexAgent) string { return a.collaborationMode }, set: func(a *CodexAgent, v string) { a.collaborationMode = v }, defaultValue: CodexDefaultCollaborationMode},
	{id: CodexOptionServiceTier, get: func(a *CodexAgent) string { return a.serviceTier }, set: func(a *CodexAgent, v string) { a.serviceTier = v }, defaultValue: CodexDefaultServiceTier, lifecyclePolicy: codexLifecycleAuthoritative, lifecycleDefault: CodexDefaultServiceTier},
}

// codexEffortRefreshFallback mirrors the model preset's implicit effort default.
// It applies only while the requested value is automatic. Caller holds a.mu.
func codexEffortRefreshFallback(a *CodexAgent) {
	if a.effort != EffortAuto {
		return
	}
	if m := FindAvailableModel(a.availableModels, a.model); m != nil && m.DefaultEffort != "" {
		a.effort = m.DefaultEffort
	}
}

// codexAxisValuesLocked snapshots every axis's live value into an id->value map. Caller
// holds a.mu.
func (a *CodexAgent) codexAxisValuesLocked() map[string]string {
	vals := make(map[string]string, len(codexAxes))
	for _, ax := range codexAxes {
		vals[ax.id] = ax.get(a)
	}
	return vals
}

// codexOptionDefaults returns the Codex provider-option defaults (id->default), registered
// into the factory entry (setProviderOptionDefaults) so resolveProviderDefaults stamps them
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
func (a *CodexAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	vals := a.codexAxisValuesLocked()
	models := a.availableModels
	a.mu.Unlock()

	groups := modelAndEffortGroups(models, vals[OptionIDModel], vals[OptionIDEffort], EffortGroupLabel, nil)

	// Current values are sourced per-axis from the snapshot; the display order is
	// carried on each registered template (so a newly-registered group can't lose its
	// order or sort ahead of the model group), and liveGroup defaults an unsupplied
	// current to the template's default. The model/effort entries in vals are unused
	// here -- they are rendered by modelAndEffortGroups above.
	for _, sg := range AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX) {
		groups = append(groups, liveGroup(sg, vals[sg.GetId()]))
	}
	return groups
}

// UpdateSettings stores new settings so the next turn/start picks them up.
func (a *CodexAgent) UpdateSettings(options optionmap.Map) SettingsApplyResult {
	a.mu.Lock()
	curEffort := a.effort
	curModel := a.model
	// Switching to EffortAuto can't be done live: Codex's session config
	// remembers the last reasoning_effort across turns, so simply
	// omitting the field on the next turn keeps the prior effort
	// applied. A restart is the only way to hand control back to the
	// CLI's own default.
	if IsEffortAutoTransition(options[OptionIDEffort], curEffort) {
		a.mu.Unlock()
		return restartRequiredSettings(options)
	}
	// Switching to the account default can't be done live either, and for the same
	// shape of reason as the effort sentinel above. thread/start resolves an omitted
	// model, but turn/start sends the stored string as it is, and Codex rejects the
	// literal id "default" ("The 'default' model is not supported"). A relaunch runs
	// codexThreadParams again, which omits the model and lets Codex resolve it.
	// Test the sentinel EXACTLY, not UsesAccountDefaultModel: in this map an empty
	// value means "not supplied" (see the axis loop below), so the wider test would
	// demand a restart on every edit that leaves the model alone.
	if m := options[OptionIDModel]; m == DefaultModelSentinel && m != curModel {
		a.mu.Unlock()
		return restartRequiredSettings(options)
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
	a.mu.Unlock()

	a.publishSettings()
	return a.SettingsSnapshot()
}

func (a *CodexAgent) SettingsSnapshot() SettingsApplyResult {
	return confirmedSettings(CurrentOptions(a.OptionGroups()))
}

// publishSettings broadcasts the active thread and turn settings. config/read
// reports global layers and cannot confirm active overrides.
func (a *CodexAgent) publishSettings() {
	a.mu.Lock()
	for _, ax := range codexAxes {
		if ax.refreshFallback != nil {
			ax.refreshFallback(a)
		}
	}
	vals := a.codexAxisValuesLocked()
	a.mu.Unlock()

	slog.Info("codex agent settings published",
		"agent_id", a.agentID,
		"model", vals[OptionIDModel],
		"effort", vals[OptionIDEffort],
		"approvalPolicy", vals[OptionIDPermissionMode],
		"sandboxPolicy", vals[CodexOptionSandboxPolicy],
		"networkAccess", vals[CodexOptionNetworkAccess],
		"collaborationMode", vals[CodexOptionCollaborationMode],
		"serviceTier", vals[CodexOptionServiceTier],
	)

	a.sink.PersistSettingsRefresh(vals)
}

// queryAvailableModels sends a model/list request and converts the response.
func (a *CodexAgent) queryAvailableModels(timeout time.Duration) []*ModelInfo {
	resp, err := a.sendRequest("model/list", json.RawMessage(`{}`), timeout)
	if err != nil {
		slog.Warn("codex model/list failed", "agent_id", a.agentID, "error", err)
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
		slog.Warn("codex model/list unmarshal failed", "agent_id", a.agentID, "error", err)
		return nil
	}

	// Build a lookup from default models so we can fill in missing metadata.
	var defaults []*ModelInfo
	if reg, ok := agentFactoryRegistry[leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX]; ok {
		defaults = reg.defaultModels
	}
	defaultsByID := make(map[string]*ModelInfo, len(defaults))
	for _, d := range defaults {
		defaultsByID[d.Id] = d
	}

	var models []*ModelInfo
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
		efforts := make([]*EffortInfo, 0, len(raw)+1)
		efforts = append(efforts, codexAutoEffort())
		for i := len(raw) - 1; i >= 0; i-- {
			e := raw[i]
			efforts = append(efforts, &EffortInfo{
				Id:          e.ReasoningEffort,
				Name:        effortLabel(e.ReasoningEffort),
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

		models = append(models, &ModelInfo{
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
func (a *CodexAgent) reconcileModelCatalog() {
	if len(a.availableModels) == 0 {
		return
	}
	if FindAvailableModel(a.availableModels, DefaultModelSentinel) == nil {
		if sentinel := FindAvailableModel(codexDefaultModels, DefaultModelSentinel); sentinel != nil {
			a.availableModels = slices.Insert(a.availableModels, 0, sentinel)
		}
	}
	if UsesAccountDefaultModel(a.model) || FindAvailableModel(a.availableModels, a.model) != nil {
		return
	}
	entry := FindAvailableModel(codexDefaultModels, a.model)
	if entry == nil {
		return
	}
	// Drop the settled model at its slot in the static catalog's order rather than
	// at the end, so a retired model does not sort below a newer one it outranks.
	// The inserted pointer is the shared static entry, which every consumer reads
	// and none mutates -- modelOptionGroup projects it into fresh protos.
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
	if i := slices.IndexFunc(codexDefaultModels, func(m *ModelInfo) bool {
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
	"ultra": true, "max": true, EffortXHigh: true, EffortHigh: true,
	"medium": true, "low": true,
}

func buildCodexDefaultEfforts() []*EffortInfo {
	efforts := []*EffortInfo{codexAutoEffort()}
	for _, id := range effortLadderIDs() {
		if codexEffortIDs[id] {
			efforts = append(efforts, effortTier(id))
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
func codexEffortsDownFrom(top string) []*EffortInfo {
	tiers := codexDefaultEfforts[1:]
	for i, tier := range tiers {
		if tier.Id == top {
			// The literal has capacity 1, so append allocates a new array and the
			// returned slice never aliases codexDefaultEfforts.
			return append([]*EffortInfo{codexAutoEffort()}, tiers[i:]...)
		}
	}
	panic("codex: effort tier " + top + " is not in codexDefaultEfforts")
}

// codexAutoEffort is the LeapMux-side "auto" sentinel. The CLI never reports it,
// so both the live catalog and the static fallback prepend this one value rather
// than spelling the label and the description out twice.
func codexAutoEffort() *EffortInfo {
	return &EffortInfo{
		Id:          EffortAuto,
		Name:        effortLabel(EffortAuto),
		Description: "Let Codex decide the appropriate effort",
	}
}

var (
	codexEffortsFromUltra = codexEffortsDownFrom("ultra")
	codexEffortsFromMax   = codexEffortsDownFrom("max")
	codexEffortsFromXHigh = codexEffortsDownFrom(EffortXHigh)
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
var codexDefaultModels = []*ModelInfo{
	accountDefaultModelEntry("Use the account's default Codex model"),
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

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		StartCodex,
		codexDefaultModels,
		[]*leapmuxv1.AvailableOptionGroup{
			{
				Id:           CodexOptionServiceTier,
				Label:        "Fast Mode",
				DefaultValue: CodexDefaultServiceTier,
				Mutable:      true,
				Order:        OptionOrderProviderFirst,
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexServiceTierFast, Name: "On", Description: "Use Codex fast mode for future turns"},
					{Id: CodexDefaultServiceTier, Name: "Off", Description: "Use the normal/default service tier"},
				},
			},
			{
				Id:           CodexOptionCollaborationMode,
				Label:        "Workflow",
				DefaultValue: CodexDefaultCollaborationMode,
				Mutable:      true,
				Order:        OptionOrderProviderSecond,
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexCollaborationDefault, Name: "Default"},
					{Id: CodexCollaborationPlan, Name: "Plan Mode"},
				},
			},
			{
				Id:           OptionIDPermissionMode,
				Label:        "Approval Policy",
				DefaultValue: CodexDefaultApprovalPolicy,
				Mutable:      true,
				Order:        OptionOrderPermissionMode,
				Options: []*leapmuxv1.AvailableOption{
					{Id: "never", Name: "Full Auto"},
					{Id: CodexDefaultApprovalPolicy, Name: "Suggest & Approve"},
					{Id: "untrusted", Name: "Auto-edit"},
				},
			},
			{
				Id:           CodexOptionSandboxPolicy,
				Label:        "Sandbox Policy",
				DefaultValue: CodexDefaultSandboxPolicy,
				Mutable:      true,
				Order:        OptionOrderProviderFourth,
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexSandboxDangerFullAccess, Name: "Full Access", Description: "No filesystem restrictions"},
					{Id: CodexSandboxWorkspaceWrite, Name: "Workspace Write", Description: "Write only within the working directory"},
					{Id: CodexSandboxReadOnly, Name: "Read Only", Description: "No write access to the filesystem"},
				},
			},
			{
				Id:           CodexOptionNetworkAccess,
				Label:        "Network Access",
				DefaultValue: CodexDefaultNetworkAccess,
				Mutable:      true,
				Order:        OptionOrderProviderThird,
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexNetworkRestricted, Name: "Restricted", Description: "No network access from the sandbox"},
					{Id: CodexNetworkEnabled, Name: "Enabled", Description: "Allow network access from the sandbox"},
				},
			},
		},
		"LEAPMUX_CODEX_DEFAULT_MODEL",
		"LEAPMUX_CODEX_DEFAULT_EFFORT",
		codexBinaryCandidates...,
	)
	// model + the provider options above (static groups) + effort. The sandbox/network/
	// collaboration/service-tier axes are already static optionGroups, so only effort
	// (built from the model catalog) needs declaring here.
	setAdditionalOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, OptionIDEffort)
	// Seed the sandbox/network/collaboration/service-tier defaults into a fresh agent's
	// launch options; resolveProviderDefaults applies these for every provider uniformly.
	setProviderOptionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, codexOptionDefaults())
	// Codex has no new-session safe default: Suggest & Approve already asks.
	setPermissionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, PermissionDefaults{
		Fallback: CodexDefaultApprovalPolicy,
	})
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
			suffixParts[i] = capitalizeFirst(p)
		}
	}
	return prefix + parts[0] + " " + strings.Join(suffixParts, " ")
}

func (a *CodexAgent) handleOutput(line *parsedLine) {
	handleCodexOutput(a, line)
}

// HandleOutput processes a single JSONL notification from Codex.
func (a *CodexAgent) HandleOutput(content []byte) {
	handleCodexOutput(a, parseLine(content))
}

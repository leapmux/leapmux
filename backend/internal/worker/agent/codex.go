package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
// Bounds the DELIVERY, not the turn. Codex emits `turn/started` as soon as it
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
	// Closed by the `turn/started` notification, which is Codex's ACK that it
	// accepted a `turn/start`. Non-nil only while a start is waiting for that
	// ack. The turn/start RESPONSE cannot serve as the ack: Codex answers it
	// when the TURN ENDS, minutes or hours later.
	turnStartAck      chan struct{}
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

	threadID, err := a.startOrResumeThread(threadParams, opts.ResumeSessionID, timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError(threadMethod, err)
	}
	a.threadID = threadID
	sink.UpdateSessionID(a.threadID)
	sink.BroadcastStatusActive(a.threadID)

	// 5. Query available models (best-effort; don't fail startup if this fails).
	a.availableModels = a.queryAvailableModels(timeout)

	// 6. Refresh from the CLI so the persisted effort reflects the value
	// Codex actually applied — especially important when LeapMux resolved
	// the effort option to "auto" and sent thread/start without the field.
	// The readback broadcasts through the sink, which writes back to the
	// agents table.
	a.refreshSettingsFromAgent()

	return a, nil
}

// startOrResumeThread sends thread/start, or thread/resume when the launch
// carries a stored thread ID. A resume that does not hold fails the whole
// start; see resumeFailedError.
func (a *CodexAgent) startOrResumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (string, error) {
	if resumeSessionID != "" {
		threadParams["threadId"] = resumeSessionID
		return a.resumeThread(threadParams, resumeSessionID, timeout)
	}
	threadID, err := a.startThread(threadParams, timeout)
	if err != nil {
		return "", err
	}
	if threadID == "" {
		return "", fmt.Errorf("codex thread/start: response did not contain a thread ID")
	}
	return threadID, nil
}

// resumeThread sends `thread/resume` and returns the thread ID that Codex
// reopened. An RPC error, a response that does not parse, and a response that
// carries no thread ID all fail: none of the three reopens the conversation.
func (a *CodexAgent) resumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (string, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return "", resumeFailedError(resumeSessionID, fmt.Errorf("marshal thread/resume params: %w", err))
	}
	resp, err := a.sendRequest("thread/resume", paramsJSON, timeout)
	if err == nil {
		// An RPC error arrives as a delivered body, not as a transport error, so
		// the agent's own message is here and nowhere else. Without this test it
		// falls through to the "carried no thread ID" case below, which reports
		// the symptom and hides the reason.
		err = jsonRPCResultError(resp)
	}
	if err != nil {
		return "", resumeFailedError(resumeSessionID, err)
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", resumeFailedError(resumeSessionID,
			fmt.Errorf("failed to parse response %q: %w", string(resp), err))
	}
	if result.Thread.ID == "" {
		return "", resumeFailedError(resumeSessionID,
			fmt.Errorf("response %q carried no thread ID", string(resp)))
	}
	return result.Thread.ID, nil
}

// startThread sends `thread/start` and returns the new thread id.
func (a *CodexAgent) startThread(threadParams map[string]interface{}, timeout time.Duration) (string, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return "", fmt.Errorf("marshal thread/start params: %w", err)
	}
	resp, err := a.sendRequest("thread/start", paramsJSON, timeout)
	if err != nil {
		return "", err
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("codex thread/start: failed to parse response: %w", err)
	}
	return result.Thread.ID, nil
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
	params, err := json.Marshal(map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	})
	if err != nil {
		return fmt.Errorf("marshal turn/interrupt params: %w", err)
	}
	resp, err := a.sendRequest("turn/interrupt", json.RawMessage(params), 0)
	if err != nil {
		return fmt.Errorf("turn/interrupt: %w", err)
	}
	if err := jsonRPCResultError(resp); err != nil {
		return fmt.Errorf("turn/interrupt: %w", err)
	}
	return nil
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

	threadID, err := a.startThread(threadParams, a.APITimeout())
	if err != nil || threadID == "" {
		slog.Error("codex ClearContext: thread/start failed", "agent_id", a.agentID, "error", err)
		return "", false
	}

	a.mu.Lock()
	a.threadID = threadID
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
	a.mu.Unlock()
	// The thread was replaced; drop any in-flight thinking-token estimate so it
	// doesn't leak into the new context (mirrors acpBase.ClearContext). The next
	// turn/started also resets, but resetting here keeps every provider's context
	// clear consistent rather than relying on that follow-up.
	a.thinkingTokens.reset()

	a.sink.UpdateSessionID(threadID)
	return threadID, true
}

// SendInput writes a user message to the agent. If a turn is already in
// progress it uses turn/steer; otherwise it starts a new turn via turn/start
// with the current model, effort, approval policy and sandbox policy.
func (a *CodexAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
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

	// If a turn is active, steer it instead of starting a new one.
	if turnID != "" {
		return a.sendTurnSteer(threadID, turnID, input)
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
	if s.model != "" {
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

	// SendInput must return when the message is DELIVERED, never when the turn
	// ends -- see the Agent interface. Codex answers `turn/start` only at turn
	// end ("minutes-to-hours" is its own description), so waiting on that
	// response would hold the caller for the whole turn: the worker would
	// withhold the RPC ack and the user-message broadcast until then, and the
	// browser, whose deadline is far shorter, would label a message it had
	// already delivered "Failed to deliver".
	//
	// The ack is the `turn/started` notification instead. Register for it BEFORE
	// the request goes out, or a fast agent can answer before we are listening.
	ack := make(chan struct{})
	a.mu.Lock()
	a.turnStartAck = ack
	a.mu.Unlock()

	// No timeout on the request itself: it resolves at turn end, and a wall-clock
	// cap would kill long-but-legitimate work. Nothing waits on it, so a turn
	// that runs for hours costs one parked goroutine and no held lock.
	go func() {
		if _, err := a.sendRequest("turn/start", paramsJSON, 0); err != nil {
			slog.Error("codex turn/start failed", "agent_id", a.agentID, "error", err)
			a.sink.PersistLeapMuxNotification(map[string]any{
				"type":  NotificationTypeAgentError,
				"error": err.Error(),
			})
		}
	}()

	select {
	case <-ack:
		// Codex accepted the turn. `turn/started` also set turnID, which is what
		// makes the next send steer this turn rather than start another.
		return nil
	case <-a.processDone:
		return fmt.Errorf("turn/start: agent exited before it accepted the turn")
	case <-time.After(turnStartAckTimeout):
		// Delivery is UNCONFIRMED, not failed: the request is on its way and the
		// turn may still start. Report it so the user sees an unacknowledged
		// send rather than a silent one, and stop waiting either way.
		a.mu.Lock()
		if a.turnStartAck == ack {
			a.turnStartAck = nil
		}
		a.mu.Unlock()
		return fmt.Errorf("turn/start: no turn/started within %s", turnStartAckTimeout)
	}
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

	// No timeout: turn/steer rides the active turn's lifetime; Codex only
	// responds when the steered turn completes (which can be long-running).
	_, err = a.sendRequest("turn/steer", paramsJSON, 0)
	if err != nil {
		return fmt.Errorf("turn/steer: %w", err)
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

// codexThreadParams builds the request params shared by thread/start and thread/resume,
// so the launch (StartCodex) and clear-context (ClearContext) paths construct the thread
// the same way and a new thread field is added once. The serviceTier field is included
// only when codexServiceTierValue reports a non-default tier. StartCodex appends threadId
// itself for the resume case.
func codexThreadParams(model, cwd, approvalPolicy, sandboxPolicy, serviceTier string) map[string]interface{} {
	params := map[string]interface{}{
		"model":          model,
		"cwd":            cwd,
		"approvalPolicy": approvalPolicy,
		"sandbox":        sandboxPolicy,
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

// codexAxis describes one Codex configuration axis. The settings-refresh map, the
// OptionGroups current-value map, and the provider defaults all drive off this single
// table, so adding a Codex axis is one row instead of three coordinated edits that can
// silently drift (a missed one drops the axis from persistence or the picker).
type codexAxis struct {
	id string
	// configKey is the axis's key under the "config" object in a config/read response,
	// so refreshSettingsFromAgent can reconcile every axis from one table loop.
	configKey string
	get       func(*CodexAgent) string // reads the live value from agent state; call under a.mu
	// set writes a (non-empty) requested value into agent state; call under a.mu. Having
	// it on the table means "add a Codex axis = one table row" holds for the live-update
	// writes too, so a new axis can't be silently dropped from UpdateSettings while still
	// appearing in the picker via get.
	set func(*CodexAgent, string)
	// refreshFallback, when set, runs during refreshSettingsFromAgent ONLY if config/read
	// did not report this axis (its config value was null/absent), to derive a value the
	// CLI computes implicitly. Call under a.mu. Only effort has one (Codex omits
	// model_reasoning_effort when unset, falling back to the model preset's default at
	// inference time); every other axis simply keeps its prior value on an unreported field.
	refreshFallback func(*CodexAgent)
	// defaultValue is the Codex-specific default resolveProviderDefaults stamps for an
	// provider option axis (sandbox/network/collaboration/service-tier). Empty for model, effort,
	// and approval, which are defaulted by the shared model/effort/permission logic.
	defaultValue string
}

var codexAxes = []codexAxis{
	{id: OptionIDModel, configKey: "model", get: func(a *CodexAgent) string { return a.model }, set: func(a *CodexAgent, v string) { a.model = v }},
	{id: OptionIDEffort, configKey: "model_reasoning_effort", get: func(a *CodexAgent) string { return a.effort }, set: func(a *CodexAgent, v string) { a.effort = v }, refreshFallback: codexEffortRefreshFallback},
	{id: OptionIDPermissionMode, configKey: "approval_policy", get: func(a *CodexAgent) string { return a.approvalPolicy }, set: func(a *CodexAgent, v string) { a.approvalPolicy = v }},
	{id: CodexOptionSandboxPolicy, configKey: "sandbox_mode", get: func(a *CodexAgent) string { return a.sandboxPolicy }, set: func(a *CodexAgent, v string) { a.sandboxPolicy = v }, defaultValue: CodexDefaultSandboxPolicy},
	{id: CodexOptionNetworkAccess, configKey: "network_access", get: func(a *CodexAgent) string { return a.networkAccess }, set: func(a *CodexAgent, v string) { a.networkAccess = v }, defaultValue: CodexDefaultNetworkAccess},
	{id: CodexOptionCollaborationMode, configKey: "collaboration_mode", get: func(a *CodexAgent) string { return a.collaborationMode }, set: func(a *CodexAgent, v string) { a.collaborationMode = v }, defaultValue: CodexDefaultCollaborationMode},
	{id: CodexOptionServiceTier, configKey: "service_tier", get: func(a *CodexAgent) string { return a.serviceTier }, set: func(a *CodexAgent, v string) { a.serviceTier = v }, defaultValue: CodexDefaultServiceTier},
}

// codexEffortRefreshFallback mirrors the CLI's implicit effort default when config/read
// omits model_reasoning_effort: Codex only populates it when the user has explicitly set
// it (config.toml, per-session override, ...); when unset, the CLI falls back to the
// model preset's default_reasoning_level at inference time, which config/read does not
// reflect. Applied only when the current effort is still EffortAuto, so a concrete prior
// selection is never overwritten. Caller holds a.mu (model is reconciled before effort,
// so a.model already reflects this refresh).
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

// UpdateSettings stores new settings so the next turn/start picks them up,
// then refreshes from the Codex server to confirm the effective state.
func (a *CodexAgent) UpdateSettings(options optionmap.Map) bool {
	a.mu.Lock()
	curEffort := a.effort
	// Switching to EffortAuto can't be done live: Codex's session config
	// remembers the last reasoning_effort across turns, so simply
	// omitting the field on the next turn keeps the prior effort
	// applied. A restart is the only way to hand control back to the
	// CLI's own default.
	if IsEffortAutoTransition(options[OptionIDEffort], curEffort) {
		a.mu.Unlock()
		return false
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

	a.refreshSettingsFromAgent()
	return true
}

// refreshSettingsFromAgent sends a config/read RPC to the Codex server and
// updates internal state with the effective config values.
func (a *CodexAgent) refreshSettingsFromAgent() {
	resp, err := a.sendRequest("config/read", json.RawMessage(`{"includeLayers":false}`), a.APITimeout())
	if err != nil {
		slog.Warn("codex config/read failed", "agent_id", a.agentID, "error", err)
		return
	}

	var result struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("codex config/read unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	a.mu.Lock()
	// Reconcile every axis from one table loop (the same table that drives the picker and
	// the live-update writes, so an axis can't be reconciled here but dropped elsewhere).
	// Each config field may be null or a string; jsonString returns "" for null/absent/
	// non-string (e.g. approval_policy reported as a {"granular":...} object), so an
	// unreported axis keeps its prior value -- except effort, whose refreshFallback then
	// mirrors the CLI's implicit model-preset default. Model is reconciled before effort
	// (table order), so effort's fallback sees this refresh's model.
	for _, ax := range codexAxes {
		if s := jsonString(result.Config[ax.configKey]); s != "" {
			ax.set(a, s)
		} else if ax.refreshFallback != nil {
			ax.refreshFallback(a)
		}
	}
	vals := a.codexAxisValuesLocked()
	a.mu.Unlock()

	slog.Info("codex agent settings refreshed",
		"agent_id", a.agentID,
		"model", vals[OptionIDModel],
		"effort", vals[OptionIDEffort],
		"approvalPolicy", vals[OptionIDPermissionMode],
		"sandboxPolicy", vals[CodexOptionSandboxPolicy],
		"networkAccess", vals[CodexOptionNetworkAccess],
		"collaborationMode", vals[CodexOptionCollaborationMode],
		"serviceTier", vals[CodexOptionServiceTier],
	)

	// Codex reports every axis it manages at a concrete value, so all upsert.
	a.sink.PersistSettingsRefresh(vals)
}

// jsonString attempts to unmarshal a JSON value as a plain string.
// Returns "" if the value is null, not a string, or empty.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
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

// Default Codex efforts (auto first, then strongest → weakest) used as
// static fallback. "auto" is a LeapMux-side sentinel: the CLI never reports
// or accepts it, but selecting it causes LeapMux to omit reasoning_effort
// so Codex applies its own default.
//
// The slice order IS the menu order: optionGroupsForAgent maps it into the
// option group unchanged, and the frontend renders the options in order. It is
// DERIVED from effortLadder rather than written out, so a tier cannot be put in
// the wrong place: this file states only WHICH tiers Codex offers, and the
// ladder states what comes before what.
//
// Every tier the live CLI reports should appear here, so the static fallback
// offers the same menu the running session does. A tier that is absent is no
// longer a labeling hazard: both paths resolve their label through effortLabel,
// so an unlisted id renders capitalized like its siblings rather than raw.
var codexDefaultEfforts = buildCodexDefaultEfforts()

// codexEffortIDs is WHICH levels Codex offers. The ORDER comes from
// effortLadder, so this states membership only: Codex has no `ultracode` rung
// and no separate `off`, and listing it here in ladder order would be a second
// statement of the ordering that a level moved in the ladder could fall behind.
var codexEffortIDs = map[string]bool{
	"ultra": true, "max": true, EffortXHigh: true, EffortHigh: true,
	"medium": true, "low": true, "minimal": true, "none": true,
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

var codexDefaultModels = []*ModelInfo{
	{Id: "gpt-5.4", DisplayName: "GPT-5.4", Description: "Latest frontier agentic coding model", IsDefault: true, DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 1_050_000},
	{Id: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", Description: "Smaller frontier agentic coding model", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", Description: "Frontier Codex-optimized agentic coding model", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", Description: "Frontier agentic coding model", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.2", DisplayName: "GPT-5.2", Description: "Optimized for professional work and long-running agents", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 256_000},
	{Id: "gpt-5.1-codex-max", DisplayName: "GPT-5.1 Codex Max", Description: "Codex-optimized model for deep and fast reasoning", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.1-codex-mini", DisplayName: "GPT-5.1 Codex Mini", Description: "Optimized for Codex; cheaper, faster, but less capable", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
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
}

// codexModelDisplayName generates a human-readable display name from a Codex
// model ID (e.g. "gpt-5.4-mini" → "GPT-5.4 Mini", "o4-mini" → "o4-mini").
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

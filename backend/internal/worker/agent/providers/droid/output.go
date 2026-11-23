package droid

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// isDroidRawInterrupt reports whether a raw frame is LeapMux's own interrupt
// marker for Droid. The browser interrupts through the InterruptAgent call; the
// frame is for a caller of SendAgentRawMessage.
func isDroidRawInterrupt(content []byte) bool {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &head); err != nil {
		return false
	}
	return head.Type == "interrupt"
}

// HandleOutput feeds one stdout line into the dispatcher.
func (a *Agent) HandleOutput(content []byte) {
	a.handleFrame(content)
}

// droidBgProcessOp is one deferred registry mutation produced by
// onBackgroundProcessesChanged so sink calls run outside dispatchMu.
type droidBgProcessOp struct {
	upsert      *bgtask.Upsert
	rowKey      string
	closeStatus bgtask.Status
}

// droidDispatchAction carries post-dispatch work that must run after
// dispatchFrame releases dispatchMu so lock order holds across tail and sink calls.
type droidDispatchAction struct {
	startTailChildID    string
	resumeTailChildID   string
	resumeArchivePos    *droidArchivePosition
	resumeArchiveProbed bool
	probeResumeSpawnKey string
	probeResumeChildID  string
	settledChildID      string
	settledStatus       bgtask.Status
	bgProcessOps        []droidBgProcessOp
}

// handleFrame dispatches one stream-jsonrpc message. It runs on the process's
// reader goroutine and on HandleOutput.
func (a *Agent) handleFrame(line []byte) {
	if a.IsDiscardingOutput() {
		return
	}
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return
	}
	var env droidEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		slog.Debug("droid: unparseable line", "agent_id", a.AgentID(), "error", err)
		return
	}

	action := a.dispatchFrame(line, &env)
	switch {
	case action.startTailChildID != "":
		a.startChildTail(action.startTailChildID)
	case action.resumeTailChildID != "":
		a.resumeChildTailFromEOF(action.resumeTailChildID, action.resumeArchivePos, action.resumeArchiveProbed)
	case action.probeResumeChildID != "":
		a.probeResumeSpawnArchive(action.probeResumeSpawnKey, action.probeResumeChildID)
	case action.settledChildID != "":
		a.settleChildSession(action.settledChildID, action.settledStatus)
	case len(action.bgProcessOps) > 0:
		a.applyBackgroundProcessOps(action.bgProcessOps)
	}
}

// dispatchFrame keeps native event state in order. A child archive starts after
// this method releases dispatchMu because its reader can dispatch more events.
func (a *Agent) dispatchFrame(line []byte, env *droidEnvelope) droidDispatchAction {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	switch env.Type {
	case droidTypeNotification:
		if env.Method == droidMethodSessionNotif {
			return a.handleNotification(env)
		}
	case droidTypeRequest:
		a.handleServerRequest(line, env)
	case droidTypeResponse:
		// The startup response settles initialize_session or load_session.
		if env.ID == droidInitRequestID {
			if env.Error != nil {
				a.settleStartup(env.Error)
			} else {
				a.settleStartup(a.adoptSessionID(env))
			}
			return droidDispatchAction{}
		}
		if a.handleCompactionResponse(*env) {
			return droidDispatchAction{}
		}
		if a.settleReply(*env) {
			return droidDispatchAction{}
		}
		if env.Error != nil {
			slog.Debug("droid: request failed", "agent_id", a.AgentID(), "error", env.Error)
		}
	default:
		slog.Debug("droid: unknown envelope type", "agent_id", a.AgentID(), "type", env.Type)
	}
	return droidDispatchAction{}
}

// droidNotification is the params of droid.session_notification.
type droidNotification struct {
	SessionID    string          `json:"sessionId"`
	Notification json.RawMessage `json:"notification"`
}

// droidNotifHead is the `type` discriminator of a notification payload.
type droidNotifHead struct {
	Type string `json:"type"`
}

// handleNotification dispatches one session notification. The worker persists
// the notification payload verbatim so the browser plugin reads the same shape.
func (a *Agent) handleNotification(env *droidEnvelope) droidDispatchAction {
	var params droidNotification
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad notification params", "agent_id", a.AgentID(), "error", err)
		return droidDispatchAction{}
	}
	payload := params.Notification
	if len(payload) == 0 {
		// Some notifications carry the payload at the top level of params.
		payload = env.Params
	}
	var head droidNotifHead
	if err := json.Unmarshal(payload, &head); err != nil {
		slog.Debug("droid: bad notification payload", "agent_id", a.AgentID(), "error", err)
		return droidDispatchAction{}
	}
	if head.Type == contracts.DroidNotificationChildSessionAvailable {
		return a.onChildSessionAvailable(params.SessionID, payload)
	}
	target, ok := a.outputTargetFor(params.SessionID)
	if !ok {
		slog.Debug("droid: notification from an unknown session", "agent_id", a.AgentID(), "session_id", params.SessionID)
		return droidDispatchAction{}
	}
	a.flushPendingOutput(target)
	defer a.releaseEndedOutput(target)

	switch head.Type {
	case contracts.DroidNotificationWorkingStateChanged:
		a.onWorkingStateChanged(payload, target)
	case contracts.DroidNotificationCreateMessage:
		a.onCreateMessage(payload, target)
	case contracts.DroidNotificationAssistantTextDelta:
		a.onModelTextDelta(payload, agent.AssembledMessageKindText, target)
	case contracts.DroidNotificationAssistantTextComplete:
		a.onModelTextComplete(payload, agent.AssembledMessageKindText, target)
	case contracts.DroidNotificationThinkingTextDelta:
		a.onModelTextDelta(payload, agent.AssembledMessageKindReasoning, target)
	case contracts.DroidNotificationThinkingTextComplete:
		a.onModelTextComplete(payload, agent.AssembledMessageKindReasoning, target)
	case contracts.DroidToolNotificationToolCall:
		if spawnKey, resumeChildID := a.onToolCall(payload, target); resumeChildID != "" {
			return droidDispatchAction{
				probeResumeSpawnKey: spawnKey,
				probeResumeChildID:  resumeChildID,
			}
		}
	case contracts.DroidToolNotificationToolResult:
		a.onToolResult(payload, target)
	case contracts.DroidNotificationAgentTurnCompleted:
		a.onAgentTurnCompleted(payload, target)
	case contracts.DroidNotificationSettingsUpdated:
		if target.childSessionID == "" {
			a.onSettingsUpdated(payload)
		}
	case droidNotificationChildSessionSettled:
		var settled struct {
			ChildSessionID string `json:"childSessionId"`
			Status         string `json:"status"`
		}
		if json.Unmarshal(payload, &settled) == nil && settled.ChildSessionID != "" {
			return droidDispatchAction{
				settledChildID: settled.ChildSessionID,
				settledStatus:  droidSettledChildStatus(settled.Status),
			}
		}
	case droidNotificationBackgroundProcessesChanged:
		if target.childSessionID == "" {
			return droidDispatchAction{bgProcessOps: a.onBackgroundProcessesChanged(payload)}
		}
	case contracts.DroidNotificationSessionTitleUpdated, contracts.DroidNotificationSessionTokenUsageChanged,
		contracts.DroidNotificationToolExecutionHeartbeat, contracts.DroidNotificationPermissionResolved,
		droidNotificationQueuedMessagesChanged, droidNotificationScriptOracleChanged:
	// These native state updates supply no transcript content.
	case contracts.DroidNotificationHookExecutionStarted, contracts.DroidNotificationStructuredOutput:
	// LeapMux asks for no structured output. A hook reports its result separately.
	case contracts.DroidNotificationToolExecutionPhaseChanged:
		a.flushPendingToolCall(target, providerkit.StringField(payload, "toolUseId"))
	case contracts.DroidNotificationMcpStatusChanged:
		a.onMcpStatusChanged(payload, target)
	case contracts.DroidNotificationHookExecutionCompleted:
		a.onHookCompleted(payload, target)
	case contracts.DroidNotificationToolProgressUpdate:
		a.onToolProgressUpdate(payload, target)
	case contracts.DroidNotificationLlmRetry:
		a.persistNotification(payload, target)
		if target.childSessionID == "" {
			a.retryResolution.NoteRetry()
		}
	case contracts.DroidNotificationAssistantMessageRetracted:
		a.onAssistantMessageRetracted(payload, target)
	case contracts.DroidNotificationQueuedMessagesDiscarded:
		a.onQueuedMessagesDiscarded(payload, target)
	case contracts.DroidNotificationSessionCompacted:
		a.persistNotification(payload, target)
		endDroidCompaction(target)
		if target.childSessionID == "" {
			a.finishCompactionFromNotification()
			a.refreshContextUsage()
		}
	case contracts.DroidNotificationError:
		a.onError(payload, target)
	default:
		// An unknown notification type must move nothing. Persist it so the
		// browser can still draw it, and leave the turn flag alone.
		a.persistNotification(payload, target)
	}
	return droidDispatchAction{}
}

// persistNotification stores a notification row verbatim.
func (a *Agent) persistNotification(payload []byte, target droidOutputTarget) {
	a.enqueueOutput(target, agent.MessageContent{Original: payload}, agent.SpanInfo{}, droidOutputNotification)
}

// persistRow stores one transcript row verbatim.
func (a *Agent) persistRow(payload []byte, span agent.SpanInfo, target droidOutputTarget) {
	a.enqueueOutput(target, agent.MessageContent{Original: payload}, span, droidOutputMessage)
}

// onChildSessionAvailable registers a native child under its parent session.
func (a *Agent) onChildSessionAvailable(parentSessionID string, payload []byte) droidDispatchAction {
	childSessionID, resumed, archivePos, archiveProbed := a.registerChildSession(parentSessionID, payload)
	if childSessionID == "" {
		return droidDispatchAction{}
	}
	if resumed {
		return droidDispatchAction{
			resumeTailChildID:   childSessionID,
			resumeArchivePos:    archivePos,
			resumeArchiveProbed: archiveProbed,
		}
	}
	return droidDispatchAction{startTailChildID: childSessionID}
}

// droidSettledChildStatus maps a child_session_settled status word onto a final
// registry status.
func droidSettledChildStatus(status string) bgtask.Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "completed", "succeeded", "success":
		return bgtask.StatusSucceeded
	case "cancelled", "canceled", "interrupted":
		return bgtask.StatusInterrupted
	case "stopped", "killed":
		return bgtask.StatusStopped
	case "failed", "error":
		return bgtask.StatusFailed
	default:
		return bgtask.StatusEndedWithUnknownOutcome
	}
}

// settleChildSession drains any active child archive tail outside dispatchMu
// and closes the child registry row if the archive did not already settle it.
func (a *Agent) settleChildSession(childSessionID string, status bgtask.Status) {
	a.tailMu.Lock()
	tail := a.childTails[childSessionID]
	a.tailMu.Unlock()
	if tail != nil {
		tail.poll()
	}
	childID, currentStatus, exists, err := a.sink.LookupBackgroundTask(childSessionID)
	if err != nil || !exists || currentStatus.IsFinished() {
		return
	}
	a.dispatchMu.Lock()
	if a.childAgents == nil {
		a.childAgents = make(map[string]string)
	}
	if a.childAgents[childSessionID] == "" && childID != "" {
		a.childAgents[childSessionID] = childID
	}
	a.closeChildSession(childSessionID, status)
	a.dispatchMu.Unlock()
	if tail != nil {
		tail.finish()
	}
}

type droidBackgroundProcess struct {
	ID          string `json:"id"`
	ProcessID   string `json:"processId"`
	PID         int    `json:"pid"`
	Command     string `json:"command"`
	Description string `json:"description"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exitCode"`
}

func (p droidBackgroundProcess) key() string {
	if id := strings.TrimSpace(p.ID); id != "" {
		return id
	}
	if id := strings.TrimSpace(p.ProcessID); id != "" {
		return id
	}
	if p.PID > 0 {
		return strconv.Itoa(p.PID)
	}
	return ""
}

func droidBackgroundProcessStatus(proc droidBackgroundProcess) bgtask.Status {
	switch strings.ToLower(strings.TrimSpace(proc.Status)) {
	case "", "running", "started", "starting", "active":
		return bgtask.StatusRunning
	case "pending", "queued":
		return bgtask.StatusPending
	case "completed", "succeeded", "exited", "done":
		if proc.ExitCode != nil && *proc.ExitCode != 0 {
			return bgtask.StatusFailed
		}
		return bgtask.StatusSucceeded
	case "failed", "error":
		return bgtask.StatusFailed
	case "killed", "stopped", "cancelled", "canceled", "terminated":
		return bgtask.StatusStopped
	default:
		return bgtask.StatusRunning
	}
}

// onBackgroundProcessesChanged reconciles Droid's reported active background
// processes against tracked shell rows under dispatchMu and returns deferred
// registry operations to execute after dispatchMu is released.
func (a *Agent) onBackgroundProcessesChanged(payload []byte) []droidBgProcessOp {
	var n struct {
		Processes           *[]droidBackgroundProcess `json:"processes"`
		BackgroundProcesses *[]droidBackgroundProcess `json:"backgroundProcesses"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		slog.Debug("droid: read background processes", "agent_id", a.AgentID(), "error", err)
		return nil
	}
	var list []droidBackgroundProcess
	switch {
	case n.Processes != nil:
		list = *n.Processes
	case n.BackgroundProcesses != nil:
		list = *n.BackgroundProcesses
	default:
		return nil
	}
	if a.backgroundProcesses == nil {
		a.backgroundProcesses = make(map[string]struct{})
	}
	var ops []droidBgProcessOp
	active := make(map[string]struct{}, len(list))
	handled := make(map[string]struct{}, len(list))
	for _, proc := range list {
		id := proc.key()
		if id == "" {
			continue
		}
		rowKey := droidBackgroundProcessRowPrefix + id
		status := droidBackgroundProcessStatus(proc)
		if status.IsFinished() {
			delete(active, rowKey)
			handled[rowKey] = struct{}{}
			if _, tracked := a.backgroundProcesses[rowKey]; tracked {
				delete(a.backgroundProcesses, rowKey)
				ops = append(ops, droidBgProcessOp{rowKey: rowKey, closeStatus: status})
			}
			continue
		}
		title, isCommand := strings.TrimSpace(proc.Description), false
		if title == "" {
			title = strings.TrimSpace(proc.Label)
		}
		if title == "" && strings.TrimSpace(proc.Command) != "" {
			title, isCommand = strings.TrimSpace(proc.Command), true
		}
		if title == "" {
			title = "Background process"
		}
		upsert := bgtask.Upsert{
			RowKey:         rowKey,
			Kind:           bgtask.KindShell,
			ParentAgentID:  a.AgentID(),
			Title:          title,
			TitleIsCommand: isCommand,
			Status:         status,
		}
		ops = append(ops, droidBgProcessOp{upsert: &upsert})
		active[rowKey] = struct{}{}
		handled[rowKey] = struct{}{}
		a.backgroundProcesses[rowKey] = struct{}{}
	}
	var evicted []string
	for rowKey := range a.backgroundProcesses {
		if _, stillActive := active[rowKey]; !stillActive {
			if _, alreadyHandled := handled[rowKey]; !alreadyHandled {
				evicted = append(evicted, rowKey)
			}
		}
	}
	slices.Sort(evicted)
	for _, rowKey := range evicted {
		delete(a.backgroundProcesses, rowKey)
		ops = append(ops, droidBgProcessOp{rowKey: rowKey, closeStatus: bgtask.StatusEndedWithUnknownOutcome})
	}
	return ops
}

// applyBackgroundProcessOps executes deferred shell-row updates outside dispatchMu.
func (a *Agent) applyBackgroundProcessOps(ops []droidBgProcessOp) {
	for _, op := range ops {
		if op.upsert != nil {
			providerkit.LogRegistryRefusal("droid", "upsert background process", a.sink.UpsertBackgroundTask(*op.upsert))
			continue
		}
		providerkit.LogRegistryRefusal("droid", "close background process", a.sink.CloseBackgroundTask(op.rowKey, op.closeStatus))
	}
}

// onMcpStatusChanged stores a changed nonempty set of server failures.
// The root key stays empty across startup, because the first MCP report precedes the native session identity.
func (a *Agent) onMcpStatusChanged(payload []byte, target droidOutputTarget) {
	var notification struct {
		Servers []struct {
			Name         string `json:"name"`
			Status       string `json:"status"`
			RequiresAuth bool   `json:"requiresAuth"`
			Error        string `json:"error"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(payload, &notification); err != nil {
		slog.Debug("read the Droid MCP status", "agent_id", a.AgentID(), "error", err)
		a.persistNotification(payload, target)
		return
	}
	failures := make(map[string]bool)
	for _, server := range notification.Servers {
		if server.Status != "failed" && !server.RequiresAuth {
			continue
		}
		// A requiresAuth update can repeat the failure that status already reported.
		raw, err := json.Marshal([2]string{server.Name, server.Error})
		if err != nil {
			slog.Error("encode the Droid MCP failure", "agent_id", a.AgentID(), "error", err)
			return
		}
		failures[string(raw)] = true
	}
	if len(failures) == 0 {
		return
	}
	ordered := make([]string, 0, len(failures))
	for failure := range failures {
		ordered = append(ordered, failure)
	}
	sort.Strings(ordered)
	fingerprint := strings.Join(ordered, "\n")
	if a.mcpFailures[target.childSessionID] == fingerprint {
		return
	}
	a.persistNotification(payload, target)
	if a.mcpFailures == nil {
		a.mcpFailures = make(map[string]string)
	}
	a.mcpFailures[target.childSessionID] = fingerprint
}

func (a *Agent) onHookCompleted(payload []byte, target droidOutputTarget) {
	var result struct {
		Status    string `json:"hookStatus"`
		Prevented bool   `json:"hookPreventedAction"`
		Hidden    bool   `json:"hiddenFromUserViews"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		slog.Debug("read the Droid hook result", "agent_id", a.AgentID(), "error", err)
		a.persistNotification(payload, target)
		return
	}
	if !result.Hidden && (result.Status == "error" || result.Prevented) {
		a.persistNotification(payload, target)
	}
}

// mainSessionID returns the session id of the main conversation.
func (a *Agent) mainSessionID() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.sessionID
}

// droidWorkingState is the payload of droid_working_state_changed.
type droidWorkingState struct {
	Type     string `json:"type"`
	NewState string `json:"newState"`
}

// onWorkingStateChanged publishes compaction state and changes the turn flag only for known states.
func (a *Agent) onWorkingStateChanged(payload []byte, target droidOutputTarget) {
	var n droidWorkingState
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	if n.NewState == contracts.DroidWorkingStateCompactingConversation {
		if !target.state.compactionLive {
			providerkit.PublishCompaction(target.sink, "")
			target.state.compactionLive = true
		}
	} else if n.NewState != "" && target.state.compactionLive {
		endDroidCompaction(target)
	}
	childSessionID := target.childSessionID
	switch n.NewState {
	case contracts.DroidWorkingStateThinking, contracts.DroidWorkingStateStreamingAssistantMessage,
		contracts.DroidWorkingStateExecutingTool, contracts.DroidWorkingStateWaitingForToolConfirmation,
		contracts.DroidWorkingStateCompactingConversation:
		if childSessionID != "" {
			a.armChildTurn(childSessionID)
		} else {
			a.startTurn()
		}
	case contracts.DroidWorkingStateIdle:
		if childSessionID != "" {
			a.disarmChildTurn(childSessionID)
		} else {
			a.endTurn()
		}
	}
}

// onCreateMessage records a new message and opens a span for it.
func (a *Agent) onCreateMessage(payload []byte, target droidOutputTarget) {
	var n struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	var msg struct {
		ID         string          `json:"id"`
		Role       string          `json:"role"`
		Visibility string          `json:"visibility"`
		Content    json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(n.Message, &msg); err != nil {
		return
	}
	if msg.Visibility == "llm_only" {
		return
	}
	if msg.Role == "user" {
		if target.childSessionID == "" {
			a.onSteerEcho(msg.ID, target)
		}
		// The queue stores reader input. Child registration stores the parent's prompt.
		return
	}
	if msg.Role == "system" && msg.Visibility == "user_only" && target.state.turnErrorSeen {
		target.state.turnErrorSeen = false
		return
	}
	if msg.Role == "assistant" && !a.commitAssistantMessage(target, msg.ID, msg.Content) {
		return
	}
	target.state.ended = false
	spanID := ""
	if msg.ID != "" {
		spanID = "droid-msg-" + msg.ID
	}
	content := agent.MessageContent{Original: payload}
	if msg.Role == "assistant" {
		content.IdempotencyKey = droidMessageWriteKey(msg.ID, -1)
	}
	// The native envelope retains the message metadata beside its assembled blocks.
	a.enqueueOutput(target, content, agent.SpanInfo{SpanID: spanID}, droidOutputMessage)
}

// onSteerEcho uses Droid's client-supplied message ID, so equal text cannot confirm another input.
func (a *Agent) onSteerEcho(messageID string, target droidOutputTarget) {
	if !a.steers.EchoKey(messageID) {
		return
	}
	// A queued steer can start after the previous turn ends. Keep the queue occupied before the echo removes its item.
	a.startTurn()
	a.persistRemainingGeneration(target, agent.MessageCompletionComplete)
	a.flushPendingToolCalls(target)
	a.steers.Settle(target.sink)
}

func (a *Agent) onQueuedMessagesDiscarded(payload []byte, target droidOutputTarget) {
	if target.childSessionID == "" {
		requestID := providerkit.StringField(payload, "requestId")
		dropped := false
		if requestID != "" {
			dropped = a.steers.DropID(requestID)
		} else {
			dropped = a.steers.DropAll() > 0
		}
		if dropped {
			a.steers.Settle(target.sink)
			return
		}
	}
	a.persistNotification(payload, target)
}

// droidTextDelta is the payload of assistant_text_delta.
type droidTextDelta struct {
	Type       string `json:"type"`
	MessageID  string `json:"messageId"`
	BlockIndex int    `json:"blockIndex"`
	TextDelta  string `json:"textDelta"`
}

// onModelTextDelta accumulates one text or thinking block.
func (a *Agent) onModelTextDelta(payload []byte, kind agent.AssembledMessageKind, target droidOutputTarget) {
	var n droidTextDelta
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	if n.MessageID == "" || n.TextDelta == "" || n.BlockIndex < 0 {
		return
	}
	message := droidMessageState(target.state, n.MessageID)
	scope := modelTextScope(n.MessageID, n.BlockIndex, kind)
	if message.finalized || message.scopes[scope] == droidModelScopeCompleted {
		return
	}
	a.resolveModelRetry(target)
	target.state.ended = false
	if message.scopes == nil {
		message.scopes = make(map[string]droidModelScopeState)
	}
	message.scopes[scope] = droidModelScopeStreaming
	target.state.generation.Append(scope, kind, n.TextDelta, providerkit.JoinVerbatim)
	target.sink.ReportProgress(agent.ModelTextProgress(scope, n.TextDelta))
}

// resolveModelRetry accepts output only from this process's main conversation.
// A child archive cannot resolve the model request of the parent.
func (a *Agent) resolveModelRetry(target droidOutputTarget) {
	if target.childSessionID == "" {
		a.retryResolution.Resolve(target.sink)
	}
}

// onAssistantMessageRetracted discards the uncommitted output of the failed attempt.
func (a *Agent) onAssistantMessageRetracted(payload []byte, target droidOutputTarget) {
	messageID := providerkit.StringField(payload, "messageId")
	if messageID == "" {
		return
	}
	message := target.state.modelMessages[messageID]
	if message != nil && message.finalized {
		return
	}
	providerkit.DiscardGenerationScopes(&target.state.generation, target.sink, droidMessageScopes(target.state, message))
	delete(target.state.modelMessages, messageID)
	sessionID := target.childSessionID
	if sessionID == "" {
		sessionID = a.mainSessionID()
	}
	for id, tool := range target.state.tools {
		if tool.pendingRow != nil {
			target.sink.CloseSpan(tool.spanID)
			delete(target.state.tools, id)
			delete(a.childSpawns, droidSpawnKey(sessionID, id))
		}
	}
}

// modelTextScope distinguishes every text and thinking block of one native message.
func modelTextScope(messageID string, blockIndex int, kind agent.AssembledMessageKind) string {
	return "droid-msg-" + messageID + ":" + string(kind) + ":" + strconv.Itoa(blockIndex)
}

func droidMessageState(state *droidOutputState, messageID string) *droidModelMessage {
	if state.modelMessages == nil {
		state.modelMessages = make(map[string]*droidModelMessage)
	}
	message := state.modelMessages[messageID]
	if message == nil {
		message = &droidModelMessage{}
		state.modelMessages[messageID] = message
	}
	return message
}

func droidMessageScopes(state *droidOutputState, message *droidModelMessage) []string {
	if message == nil {
		return nil
	}
	var scopes []string
	for _, scope := range state.generation.ScopeIDs() {
		if _, ok := message.scopes[scope]; ok {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// onModelTextComplete ends live progress. Only create_message commits the assistant message.
func (a *Agent) onModelTextComplete(payload []byte, kind agent.AssembledMessageKind, target droidOutputTarget) {
	var n struct {
		MessageID  string `json:"messageId"`
		BlockIndex int    `json:"blockIndex"`
	}
	if json.Unmarshal(payload, &n) != nil || n.MessageID == "" || n.BlockIndex < 0 {
		return
	}
	message := droidMessageState(target.state, n.MessageID)
	if message.finalized {
		return
	}
	scope := modelTextScope(n.MessageID, n.BlockIndex, kind)
	state, exists := message.scopes[scope]
	if exists && state == droidModelScopeCompleted {
		return
	}
	if message.scopes == nil {
		message.scopes = make(map[string]droidModelScopeState)
	}
	// An empty completed block also needs a boundary, so a late delta cannot reopen it.
	message.scopes[scope] = droidModelScopeCompleted
	if exists {
		target.sink.ReportProgress(agent.CompleteModelProgress(scope))
	}
}

// droidMessageWriteKey separates the native metadata row (-1) from its content blocks.
func droidMessageWriteKey(messageID string, index int) string {
	return fmt.Sprintf("droid-message:%q:%d", messageID, index)
}

type droidCommittedBlock struct {
	index  int
	raw    []byte
	toolID string
}

// committedDroidBlocks validates every supported block before the agent changes any live state.
func committedDroidBlocks(content json.RawMessage) ([]droidCommittedBlock, error) {
	var blocks []struct {
		Type     string          `json:"type"`
		ID       string          `json:"id"`
		Text     json.RawMessage `json:"text"`
		Thinking json.RawMessage `json:"thinking"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}
	if blocks == nil {
		return nil, errors.New("the assistant message has no content array")
	}
	var result []droidCommittedBlock
	for index, block := range blocks {
		var kind agent.AssembledMessageKind
		var value json.RawMessage
		switch block.Type {
		case "text":
			kind, value = agent.AssembledMessageKindText, block.Text
		case "thinking":
			kind, value = agent.AssembledMessageKindReasoning, block.Thinking
		case "tool_use":
			if block.ID == "" {
				return nil, errors.New("the assistant tool block has no identity")
			}
			result = append(result, droidCommittedBlock{index: index, toolID: block.ID})
			continue
		default:
			continue
		}
		text, ok := providerkit.RawString(value)
		if !ok {
			return nil, fmt.Errorf("the assistant %s block has no string content", block.Type)
		}
		if text == "" {
			continue
		}
		raw, err := agent.MarshalAssembledMessage(kind, text, agent.MessageCompletionComplete)
		if err != nil {
			return nil, err
		}
		result = append(result, droidCommittedBlock{index: index, raw: raw})
	}
	return result, nil
}

// commitAssistantMessage freezes the native final content and preserves its block order.
// A block-complete event can precede a retry. It cannot supply this commit boundary.
func (a *Agent) commitAssistantMessage(target droidOutputTarget, messageID string, content json.RawMessage) bool {
	if messageID == "" {
		return false
	}
	message := droidMessageState(target.state, messageID)
	if message.finalized {
		return false
	}
	blocks, err := committedDroidBlocks(content)
	if err != nil {
		slog.Debug("read the committed Droid message", "agent_id", a.AgentID(), "error", err)
		return false
	}
	a.resolveModelRetry(target)
	// The final native content replaces drafts. Complete their progress without counting a retry discard.
	for _, scope := range droidMessageScopes(target.state, message) {
		target.state.generation.Discard(scope)
		target.sink.ReportProgress(agent.CompleteModelProgress(scope))
	}
	message.finalized, message.scopes = true, nil
	for _, block := range blocks {
		if block.toolID != "" {
			a.flushPendingToolCall(target, block.toolID)
			continue
		}
		a.enqueueOutput(target, agent.MessageContent{Original: block.raw, IdempotencyKey: droidMessageWriteKey(messageID, block.index)}, agent.SpanInfo{}, droidOutputMessage)
	}
	return true
}

// droidToolUse is the tool_use record of a tool_call notification.
type droidToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// onToolCall opens a tool span and returns any Task({resume}) spawn awaiting an archive probe.
func (a *Agent) onToolCall(payload []byte, target droidOutputTarget) (string, string) {
	var n struct {
		Type    string       `json:"type"`
		ToolUse droidToolUse `json:"toolUse"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return "", ""
	}
	if n.ToolUse.ID == "" {
		a.persistRow(payload, agent.SpanInfo{}, target)
		return "", ""
	}
	a.resolveModelRetry(target)
	target.state.ended = false
	if target.state.tools == nil {
		target.state.tools = make(map[string]*droidTool)
	}
	tool := target.state.tools[n.ToolUse.ID]
	if tool == nil {
		target.state.nextToolOrder++
		tool = &droidTool{id: n.ToolUse.ID, spanID: "droid-tool-" + n.ToolUse.ID, order: target.state.nextToolOrder, pendingRow: slices.Clone(payload)}
		target.state.tools[n.ToolUse.ID] = tool
		target.state.closedSpans.Reopen(tool.spanID)
		target.sink.OpenSpan(tool.spanID, "")
	} else if tool.pendingRow != nil {
		tool.pendingRow = slices.Clone(payload)
	}
	tool.name, tool.input = n.ToolUse.Name, n.ToolUse.Input
	target.sink.SetSpanType(tool.spanID, tool.name)
	sessionID := target.childSessionID
	if sessionID == "" {
		sessionID = a.mainSessionID()
	}
	return a.rememberTaskSpawn(sessionID, n.ToolUse)
}

// flushPendingToolCall freezes the latest input at its first native boundary.
// enqueueOutput retains a failed write, so later input updates cannot replace that snapshot.
func (a *Agent) flushPendingToolCall(target droidOutputTarget, toolUseID string) {
	tool := target.state.tools[toolUseID]
	if tool == nil || tool.pendingRow == nil {
		return
	}
	a.persistRow(tool.pendingRow, agent.SpanInfo{SpanID: tool.spanID, SpanType: tool.name}, target)
	tool.pendingRow = nil
}

func (a *Agent) flushPendingToolCalls(target droidOutputTarget) {
	var pending []*droidTool
	for _, tool := range target.state.tools {
		if tool.pendingRow != nil {
			pending = append(pending, tool)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].order < pending[j].order })
	for _, tool := range pending {
		a.flushPendingToolCall(target, tool.id)
	}
}

func (a *Agent) onToolProgressUpdate(payload []byte, target droidOutputTarget) {
	var notification struct {
		ToolUseID string `json:"toolUseId"`
		Update    struct {
			Text       string  `json:"text"`
			FullOutput *string `json:"fullOutput"`
		} `json:"update"`
	}
	if err := json.Unmarshal(payload, &notification); err != nil {
		slog.Debug("read the Droid tool progress", "agent_id", a.AgentID(), "error", err)
		return
	}
	if notification.ToolUseID == "" {
		return
	}
	scope := "droid-tool-" + notification.ToolUseID
	if target.state.closedSpans.WasClosed(scope) {
		return
	}
	tail := notification.Update.Text
	if notification.Update.FullOutput != nil {
		tail = *notification.Update.FullOutput
	}
	// Droid forwards a preview but omits its native truncation flag. A short
	// preview can have lost earlier lines, so neither length proves completeness.
	target.sink.ReportProgress(agent.OutputTailProgress(scope, tail, false))
	if notification.Update.FullOutput != nil && tail != "" {
		target.sink.ReportProgress(agent.OutputTotalProgress(scope, int64(len(tail)), true))
	}
}

// droidToolResult is the payload of tool_result.
type droidToolResult struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"toolUseId"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"isError"`
}

// onToolResult closes a tool span.
func (a *Agent) onToolResult(payload []byte, target droidOutputTarget) {
	var n droidToolResult
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	spanID := "droid-tool-" + n.ToolUseID
	if n.ToolUseID != "" && target.state.closedSpans.WasClosed(spanID) {
		return
	}
	a.flushPendingToolCall(target, n.ToolUseID)
	tool := target.state.tools[n.ToolUseID]
	delete(target.state.tools, n.ToolUseID)
	if tool != nil {
		target.state.turnToolUse++
		spanID = tool.spanID
	}
	if n.IsError {
		sessionID := target.childSessionID
		if sessionID == "" {
			sessionID = a.mainSessionID()
		}
		delete(a.childSpawns, droidSpawnKey(sessionID, n.ToolUseID))
	}
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, Closing: true}, target)
	if n.ToolUseID != "" {
		target.sink.ReportProgress(agent.CompleteOutputProgress(spanID))
	}
	target.sink.CloseSpan(spanID)
}

// closeOpenTools flushes any pending opening rows first, closes all open tools
// in deterministic (order, id) order with completion, and optionally resets
// spans and progress on target.sink.
func (a *Agent) closeOpenTools(target droidOutputTarget, completion agent.MessageCompletion, resetSpans bool) {
	discarding := a.IsDiscardingOutput()
	if !discarding {
		a.flushPendingToolCalls(target)
	}
	if len(target.state.tools) > 0 {
		open := make([]*droidTool, 0, len(target.state.tools))
		for _, tool := range target.state.tools {
			open = append(open, tool)
		}
		sort.Slice(open, func(i, j int) bool {
			if open[i].order != open[j].order {
				return open[i].order < open[j].order
			}
			return open[i].id < open[j].id
		})
		for _, tool := range open {
			target.state.closedSpans.MarkClosed(tool.spanID)
			if !discarding {
				closingPayload, err := json.Marshal(map[string]any{
					"type":      contracts.DroidToolNotificationToolResult,
					"toolUseId": tool.id,
					"isError":   completion != agent.MessageCompletionComplete,
				})
				if err == nil {
					a.enqueueOutput(target, agent.MessageContent{
						Original:   closingPayload,
						Completion: completion,
					}, agent.SpanInfo{
						SpanID:   tool.spanID,
						SpanType: tool.name,
						Closing:  true,
					}, droidOutputMessage)
				}
			}
			target.sink.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
			target.sink.CloseSpan(tool.spanID)
		}
		clear(target.state.tools)
	}
	if resetSpans {
		providerkit.ResetSpansAndProgress(target.sink)
	}
}

// onAgentTurnCompleted ends the turn. PersistTurnEnd runs before the clear, so
// the turn's tool count reaches the activity latch first.
func (a *Agent) onAgentTurnCompleted(payload []byte, target droidOutputTarget) {
	target.state.turnErrorSeen = false
	if target.childSessionID == "" {
		a.retryResolution.Reset()
	}
	var n struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	completion, status := droidTurnCompletion(n.Reason)
	a.persistRemainingGeneration(target, completion)
	a.flushPendingToolCalls(target)
	if completion == agent.MessageCompletionInterrupted || target.childSessionID != "" {
		a.closeOpenTools(target, completion, false)
	}
	a.persistTurnEnd(payload, target)
	if target.childSessionID != "" {
		a.closeChildSession(target.childSessionID, status)
	} else {
		if completion == agent.MessageCompletionInterrupted {
			providerkit.ResetSpansAndProgress(target.sink)
		}
		a.endTurn()
		a.refreshContextUsage()
	}
}

// droidTurnCompletion follows the native child-result classification, including early process exits.
func droidTurnCompletion(reason string) (agent.MessageCompletion, bgtask.Status) {
	switch reason {
	case "", contracts.DroidTurnEndReasonCompleted, contracts.DroidTurnEndReasonSpecHandoff:
		return agent.MessageCompletionComplete, bgtask.StatusSucceeded
	case contracts.DroidTurnEndReasonCancelled, contracts.DroidTurnEndReasonProcessExit:
		return agent.MessageCompletionInterrupted, bgtask.StatusInterrupted
	case contracts.DroidTurnEndReasonError, contracts.DroidTurnEndReasonPermissionRejected,
		contracts.DroidTurnEndReasonStructuredOutputMissing, contracts.DroidTurnEndReasonStructuredOutputInvalid,
		contracts.DroidTurnEndReasonStructuredOutputSchemaInvalid, contracts.DroidTurnEndReasonModelUsageExhausted,
		contracts.DroidTurnEndReasonModelAuthenticationFailed, contracts.DroidTurnEndReasonModelRequestRejected,
		contracts.DroidTurnEndReasonModelProviderUnreachable, contracts.DroidTurnEndReasonModelProviderUnavailable,
		contracts.DroidTurnEndReasonModelRateLimited, contracts.DroidTurnEndReasonPromptRejected,
		contracts.DroidTurnEndReasonCompletionPersistenceFailed, contracts.DroidTurnEndReasonNoApproverAvailable:
		return agent.MessageCompletionError, bgtask.StatusFailed
	default:
		// An unknown native outcome cannot establish success.
		return agent.MessageCompletionError, bgtask.StatusFailed
	}
}

// persistRemainingGeneration saves blocks when Droid omits their completion events.
// The queue retains each failed write with its original completion and publication owner.
func (a *Agent) persistRemainingGeneration(target droidOutputTarget, completion agent.MessageCompletion) {
	for _, message := range target.state.modelMessages {
		if len(message.scopes) > 0 {
			message.finalized, message.scopes = true, nil
		}
	}
	err := target.state.generation.PersistAll(completion, func(raw []byte) error {
		a.persistRow(raw, agent.SpanInfo{}, target)
		return nil
	})
	if err != nil {
		slog.Error("droid: persist unfinished text failed", "agent_id", a.AgentID(), "error", err)
	}
}

// persistTurnEnd records one native outcome without deciding child lifecycle.
func (a *Agent) persistTurnEnd(payload []byte, target droidOutputTarget) {
	uses := target.state.turnToolUse
	target.state.turnToolUse = 0

	content := agent.WithToolUseCount(agent.MessageContent{Original: payload}, uses)
	a.enqueueOutput(target, content, agent.SpanInfo{}, droidOutputTurnEnd)
}

// onError surfaces a CLI error as a notification row and ends the turn when one
// runs.
func (a *Agent) onError(payload []byte, target droidOutputTarget) {
	target.state.turnErrorSeen = true
	if target.childSessionID == "" {
		a.retryResolution.Reset()
	}
	a.persistRemainingGeneration(target, agent.MessageCompletionError)
	a.flushPendingToolCalls(target)
	a.persistNotification(payload, target)
	if target.childSessionID != "" {
		a.closeChildSession(target.childSessionID, bgtask.StatusFailed)
		return
	}
	a.Mu.Lock()
	active := a.turnActive
	a.Mu.Unlock()
	if active {
		a.endTurn()
	}
}

// settleStartup sends the result of the one native startup request to Start.
func (a *Agent) settleStartup(err error) {
	a.Mu.Lock()
	reply := a.startupReply
	a.Mu.Unlock()
	if reply != nil {
		select {
		case reply <- err:
		default:
		}
	}
}

// adoptSessionID records the new or loaded session identity and settings.
func (a *Agent) adoptSessionID(env *droidEnvelope) error {
	var result struct {
		SessionID       string              `json:"sessionId"`
		Settings        droidNativeSettings `json:"settings"`
		AvailableModels []droidCatalogModel `json:"availableModels"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return fmt.Errorf("read droid startup result: %w", err)
	}
	models := make([]droidModel, 0, len(result.AvailableModels))
	for _, m := range result.AvailableModels {
		models = append(models, droidModelFromNative(m))
	}
	a.Mu.Lock()
	if a.sessionID != "" && result.SessionID != "" && a.sessionID != result.SessionID {
		expected := a.sessionID
		a.Mu.Unlock()
		return fmt.Errorf("droid startup returned session %q instead of %q", result.SessionID, expected)
	}
	if a.sessionID == "" {
		a.sessionID = result.SessionID
	}
	sessionID := a.sessionID
	if sessionID == "" {
		a.Mu.Unlock()
		return errors.New("droid startup returned no session id")
	}
	if result.Settings.ModelID != "" {
		a.settings.model = result.Settings.ModelID
	}
	if result.Settings.ReasoningEffort != "" {
		a.settings.reasoningEffort = result.Settings.ReasoningEffort
	}
	if result.Settings.InteractionMode != "" {
		a.settings.interactionMode = result.Settings.InteractionMode
	}
	if result.Settings.AutonomyLevel != "" {
		a.settings.autonomyLevel = result.Settings.AutonomyLevel
	}
	if mode := droidModeFromSettings(result.Settings); mode != "" {
		a.settings.permissionMode = mode
	}
	if len(models) > 0 {
		a.catalog.models = models
	}
	a.Mu.Unlock()
	a.sink.UpdateSessionID(sessionID)
	return nil
}

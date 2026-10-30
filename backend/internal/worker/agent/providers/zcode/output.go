package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// handleZCodeOutput dispatches one line the response interceptor did not consume.
//
// Session events and the top-level state.updated carry conversation. A conversation
// frame arrives while a hook trust or a workflow run needs its subscription
// (workspace_trust.go, workflow.go). Everything else the app-server emits is its own
// telemetry.
func handleZCodeOutput(a *Agent, line *providerkit.ParsedLine) {
	switch line.Method {
	case NotifySessionEvent:
		event, ok := parseZCodeEvent(line.Params)
		if !ok {
			slog.Warn("zcode session/event carried no event type", "agent_id", a.AgentID(), "len", len(line.Raw))
			return
		}
		a.dispatchZCodeEventOrdered(event)
	case contracts.ZCodeMethodStateUpdated:
		a.handleZCodeStateUpdated(line.Params)
	case NotifyConversationFrame:
		a.handleZCodeConversationFrame(line.Params)
	case NotifyProviderRuntimeHeadersCancelled:
		a.handleZCodeRuntimeHeadersCancelled(line.Params)
	case "":
		// An id with no method and no pending request: a reply nobody waited for. It
		// happens when a request timed out and its answer arrived afterwards.
		slog.Debug("zcode orphan response", "agent_id", a.AgentID(), "len", len(line.Raw))
	default:
		// The app-server also emits process/resourceSample, process/mcpTelemetry,
		// plugins/operationProgress and the computer-use stream. None of them is
		// conversation, and persisting them would fill the transcript with telemetry.
		slog.Debug("zcode unhandled notification", "agent_id", a.AgentID(), "method", line.Method)
	}
}

// dispatchZCodeEventLocked routes one session event. The caller holds
// dispatchMu (dispatchZCodeEventOrdered and
// replayZCodeEvents take it).
//
// The switch lists each known event and explains each intentional drop.
// The default branch persists an unknown event of the current session.
//
// dispatchMu serializes the WHOLE body across the read loop and replay,
// because `a.Mu` guards each field read on its own and not the
// open-then-close SEQUENCE of one tool call: a replayed `scheduled` overtaken by its own
// live `result` closes a span before it opens, and the card then stays running for good.
//
// Take it here and never around `subscribe` itself. The read loop delivers the subscribe
// reply, so holding it across that RPC would deadlock the two against each other. No
// handler reachable from here issues a synchronous request; the one that issues any runs
// on its own goroutine.
//
// A turn-scoped event (a turn-end event, model streaming, tool.updated) is resolved to its
// turn owner BEFORE the caller advances the sequence watermark, so a filtered event
// records no usage, consumes no stop state and flushes no buffer.
func (a *Agent) dispatchZCodeEventLocked(event zcodeEventEnvelope) {
	if event.SessionID != "" {
		a.Mu.Lock()
		currentSessionID := a.sessionID
		a.Mu.Unlock()
		if currentSessionID != "" && event.SessionID != currentSessionID {
			return
		}
	}
	var owner *zcodeTurnState
	var toolCallID string
	switch event.Type {
	case contracts.ZCodeEventTurnCompleted, contracts.ZCodeEventTurnFailed,
		contracts.ZCodeEventModelStreaming, contracts.ZCodeEventToolUpdated, contracts.ZCodeEventStreamRecoveryUpdated:
		toolCallID = zcodeEventToolCallID(event)
		a.Mu.Lock()
		owner = a.resolveZCodeTurnLocked(event, toolCallID)
		if owner != nil {
			owner.lastEventSeq = max(owner.lastEventSeq, event.Seq)
		}
		a.Mu.Unlock()
		if owner == nil {
			// An unknown turn id, or an id-less event with zero or several
			// unsettled owners: the event identifies no owner, so it settles nothing
			// and touches nothing.
			slog.Debug("zcode turn-scoped event has no owner", "agent_id", a.AgentID(), "type", event.Type, "turn_id", event.TurnID)
			return
		}
	}

	// Only a new event of the current session extends the stop wait, and only
	// one that reports the work the window watches: see stopEventExtendsLocked.
	a.refreshStoppedZCodeTurn(event)
	switch event.Type {
	case contracts.ZCodeEventTurnStarted:
		a.handleZCodeTurnStarted(event)
	case contracts.ZCodeEventTurnCompleted:
		a.handleZCodeTurnCompleted(event, owner)
	case contracts.ZCodeEventTurnFailed:
		a.handleZCodeTurnFailed(event, owner)
	case contracts.ZCodeEventModelStreaming:
		a.handleZCodeModelStreaming(event, owner)
	case contracts.ZCodeEventToolUpdated:
		a.handleZCodeToolUpdated(event, owner)
	case contracts.ZCodeEventSessionUpdated:
		a.handleZCodeSessionUpdated(event)
	case contracts.ZCodeEventPermissionResolved:
		a.handleZCodePermissionResolved(event)
	case contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained:
		// A steer of LeapMux's resolves through the ledger that writes its row
		// at the drain. The runtime's OWN steers (a background task
		// notification, a subagent message) keep their notification row.
		if !a.observeZCodeSteerEvent(event) {
			a.persistZCodeNotification(event)
		}
	case contracts.ZCodeEventTurnSteerDeliveryChanged:
		// A steer of LeapMux's that moved to the ordinary queue stays pending.
		// Any other input keeps the row of an event no reader knows.
		if !a.observeZCodeSteerEvent(event) {
			a.persistUnknownZCodeEvent(event)
		}
	case contracts.ZCodeEventSessionClosed:
		a.persistZCodeNotification(event)

	case contracts.ZCodeEventSessionCreated, contracts.ZCodeEventSessionResumed:
		// The create/resume RPC already returned the same state, and applyStateSnapshot
		// consumed it. The event is the second copy.

	case contracts.ZCodeEventSessionTitleUpdated:
		// LeapMux sets the title from the user's first message and its rename action.
		// A native session title must not replace that title.

	case contracts.ZCodeEventMessageUpserted, contracts.ZCodeEventMessageRemoved,
		contracts.ZCodeEventPartStarted, contracts.ZCodeEventPartDelta,
		contracts.ZCodeEventPartUpserted, contracts.ZCodeEventPartRemoved:
		// The row/part projection repeats model.streaming data and the completed
		// model response. Consuming both would duplicate every message.

	case contracts.ZCodeEventPermissionRequested:
		// The actionable copy is the interaction/requestPermission REQUEST, which
		// carries the reply id and the option list. This event is its announcement, and
		// persisting it would show the prompt twice.

	case contracts.ZCodeEventUserInputRequested:
		// Same reason: interaction/requestUserInput is the actionable copy.

	case contracts.ZCodeEventUserInputResolved:
		// Nothing to persist -- the answer row already records what the user chose.
		// The request id is dropped, which re-arms the re-announcement guard.
		a.handleZCodeUserInputResolved(event)

	case contracts.ZCodeEventCheckpointCreated, contracts.ZCodeEventRewindTriggered:
		// Checkpoint and rewind belong to the desktop application's undo model, which
		// LeapMux does not expose. Its own history is the transcript.

	case contracts.ZCodeEventStreamRecoveryUpdated:
		a.handleZCodeStreamRecovery(event)

	default:
		slog.Debug("zcode unknown event type", "agent_id", a.AgentID(), "type", event.Type)
		a.persistUnknownZCodeEvent(event)
	}
}

// persistUnknownZCodeEvent preserves an unknown session event for inspection.
func (a *Agent) persistUnknownZCodeEvent(event zcodeEventEnvelope) {
	raw := event.persistBytes()
	if raw == nil {
		return
	}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
		slog.Error("zcode failed to persist an unknown event", "agent_id", a.AgentID(), "type", event.Type, "error", err)
	}
}

// persistBytes preserves a native envelope. Synthetic events use the same JSON shape.
func (e zcodeEventEnvelope) persistBytes() []byte {
	if len(e.raw) > 0 {
		return e.raw
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		slog.Warn("zcode marshal event for persist failed", "type", e.Type, "error", err)
		return nil
	}
	return encoded
}

// withPayload creates a synthetic event without changing the original envelope.
func (e zcodeEventEnvelope) withPayload(payload json.RawMessage) zcodeEventEnvelope {
	e.Payload = payload
	if len(e.raw) > 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(e.raw, &fields) == nil && fields != nil {
			fields["payload"] = payload
			encoded, err := json.Marshal(fields)
			if err != nil {
				// persistBytes reports the invalid replacement through its normal error path.
				e.raw = nil
			} else {
				e.raw = encoded
			}
		} else {
			e.raw = nil
		}
	}
	return e
}

// persistZCodeNotification records a lifecycle event and reports whether persistence succeeded.
func (a *Agent) persistZCodeNotification(event zcodeEventEnvelope) bool {
	return a.persistZCodeNotificationTo(a.sink, event)
}

func (a *Agent) persistZCodeNotificationTo(sink agent.ProviderServices, event zcodeEventEnvelope) bool {
	content := event.persistBytes()
	if content == nil {
		return false
	}
	if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content}); err != nil {
		slog.Error("zcode persist notification", "agent_id", a.AgentID(), "type", event.Type, "error", err)
		return false
	}
	return true
}

// persistZCodeBackgroundTurnEnd keeps each outcome inside its notification entry.
// A later notification can change the aggregate row without replacing this outcome.
func (a *Agent) persistZCodeBackgroundTurnEnd(owner *zcodeTurnState, original []byte, completion agent.MessageCompletion) {
	if len(original) == 0 {
		return
	}
	_, ordinal := agent.MessageMetadata(agent.MessageContent{Completion: completion})
	payload := contracts.ZCodeBackgroundTurnEnd{Original: string(original), Completion: int(ordinal)}
	record, err := json.Marshal(map[string]any{
		contracts.ZCodeSupplementType:    contracts.ZCodeRecordBackgroundTurnEnd,
		contracts.ZCodeSupplementPayload: payload,
	})
	if err != nil {
		slog.Error("zcode encode background turn end", "agent_id", a.AgentID(), "error", err)
		return
	}
	if _, err := a.zcodeProgressSinkForTurn(owner).PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: record}); err != nil {
		slog.Error("zcode persist background turn end", "agent_id", a.AgentID(), "error", err)
	}
}

// --- turn lifecycle ---

// PublishTurnActive republishes the Worker-visible prompt and compaction state.
// Call it after every critical section that changes either state.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and a missing call is the only way the two can drift.
// Never called with a.Mu held: the sink broadcasts, and a broadcast can block on
// a slow transport.
//
// A prompt turn is steerable through the command inbox unless the app-server
// refused it (steeringUnsupported); a compaction never is -- its summary query
// is not the user's conversation. A BACKGROUND turn counts as active here,
// deliberately. It persists no divider and closes none of the user's spans, but
// the agent IS processing, and turnActive is already what Interrupt and Stop
// read to decide the session is live.
//
// A publish that leaves the agent inactive is also the one choke point every
// turn end crosses, so it takes back each steer the turn left queued. ZCode
// keeps such a steer queued until the next input; see steering.go.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{
		Active:    a.turnActive || a.compactionSessionID != "",
		Steerable: a.turnActive && !a.steeringUnsupported,
	}
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	state = providerkit.PublishTurnStateTo(a.sink, state, seq)
	if !state.Active {
		if a.hasPendingZCodeSteers() {
			go a.withdrawZCodeSteersAfterTurnEnd()
		}
		a.askZCodeHookTrustWhenIdle()
	}
	return state
}

// zcodeTurnStarted is the turn.started payload.
type zcodeTurnStarted struct {
	TurnNumber            int64  `json:"turnNumber"`
	Input                 string `json:"input"`
	InputID               string `json:"inputId"`
	InputSource           string `json:"inputSource"`
	MessageID             string `json:"messageId"`
	ForegroundExecutionID string `json:"foregroundExecutionId"`
}

// zcodeEventToolCallID reads the tool-call id a turn-scoped payload carries. It
// is empty for a payload that identifies no single call, including a batch summary.
func zcodeEventToolCallID(event zcodeEventEnvelope) string {
	var payload struct {
		ToolCallID string `json:"toolCallId"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return ""
	}
	return payload.ToolCallID
}

// resolveZCodeTurnLocked finds the owner of one turn-scoped event. The caller
// holds a.Mu and drops the event when the answer is nil.
//
// The envelope's turn id identifies the owner when it carries one -- an unknown id
// has no owner and the event is ignored. A payload that identifies one tool call
// falls back to the turn recorded on that call, so a late result still reaches
// the turn that opened it. An event that identifies neither turn nor call uses the
// sole unsettled owner when exactly one exists, and has no owner at all when
// zero or several do: routing it would be a guess.
func (a *Agent) resolveZCodeTurnLocked(event zcodeEventEnvelope, toolCallID string) *zcodeTurnState {
	if event.TurnID != "" {
		return a.turns[event.TurnID]
	}
	if toolCallID != "" {
		var matched *zcodeTurnState
		for _, turn := range a.turns {
			if a.findZCodeInvocationLocked(turn, toolCallID, "") != nil {
				if matched != nil {
					return nil
				}
				matched = turn
			}
		}
		if matched != nil {
			return matched
		}
	}
	var sole *zcodeTurnState
	for _, turn := range a.turns {
		if turn.settled {
			continue
		}
		if sole != nil {
			return nil
		}
		sole = turn
	}
	return sole
}

// refreshTurnActiveLocked recomputes the turn-active flag from the owner
// records: any owner with settled == false keeps the agent busy. The caller
// holds a.Mu.
func (a *Agent) refreshTurnActiveLocked() {
	a.turnActive = false
	for _, turn := range a.turns {
		if !turn.settled {
			a.turnActive = true
			return
		}
	}
}

// dropZCodeTurnLocked removes a settled owner whose last tool call has closed.
// A settled owner stays in turns while any of its calls remains open, so a late
// result still routes to it. The caller holds a.Mu.
func (a *Agent) dropZCodeTurnLocked(owner *zcodeTurnState) {
	if owner == nil || !owner.settled || !owner.finalized || owner.settlementWriting {
		return
	}
	for id := range owner.toolInvocationIDs {
		tc := a.toolInvocations[id]
		if tc != nil && !tc.final && len(tc.lastFrame) > 0 {
			return
		}
	}
	a.children.mu.Lock()
	for id := range owner.toolInvocationIDs {
		if child := a.children.spawns[id]; child != nil && child.childAgentID != "" {
			a.children.mu.Unlock()
			return
		}
	}
	a.children.mu.Unlock()
	if a.turns[owner.id] == owner {
		delete(a.turns, owner.id)
	}
	for id := range owner.toolInvocationIDs {
		delete(a.toolInvocations, id)
	}
	clear(owner.toolInvocationIDs)
	clear(owner.invocationsByNativeID)
	clear(owner.contentByScope)
	owner.acceptedResponses = nil
	owner.currentAssistantMessageID = ""
}

// clearZCodeTurnsLocked drops every owner, at a session replacement or a
// teardown. The caller holds a.Mu.
func (a *Agent) clearZCodeTurnsLocked() {
	for _, owner := range a.turns {
		a.stopZCodeTurnWatcherLocked(owner)
	}
	clear(a.turns)
	a.foregroundTurnID = ""
	a.refreshTurnActiveLocked()
}

// createZCodeTurnLocked installs one new owner with its recovery lifetime.
// The caller supplies only state established by this admission.
func (a *Agent) createZCodeTurnLocked(initial zcodeTurnState) *zcodeTurnState {
	if a.turns == nil {
		a.turns = make(map[string]*zcodeTurnState)
	}
	a.turnGeneration++
	initial.generation = a.turnGeneration
	if initial.id == "" {
		initial.id = fmt.Sprintf("#turn-%d", initial.generation)
	}
	initial.contentByScope = make(map[string]*zcodeScopeContent)
	initial.toolInvocationIDs = make(map[string]struct{})
	initial.invocationsByNativeID = make(map[string]map[string]*zcodeToolInvocation)
	owner := &initial
	if !owner.background {
		a.replaceForegroundZCodeTurnLocked(owner)
	}
	a.turns[owner.id] = owner
	a.refreshTurnActiveLocked()
	a.startZCodeTurnWatcherLocked(owner)
	return owner
}

func (a *Agent) replaceForegroundZCodeTurnLocked(next *zcodeTurnState) {
	if previous := a.turns[a.foregroundTurnID]; previous != nil && !previous.settled && previous.pendingNative == nil {
		a.stopZCodeTurnWatcherLocked(previous)
		for scopeID := range previous.contentByScope {
			a.generationBuffer.Discard(scopeID)
		}
		delete(a.turns, previous.id)
	}
	a.cancelStoppedZCodeTurnLocked()
	a.stopCancelledRequest = false
	a.TurnToolUses = 0
	a.foregroundTurnID = next.id
}

// adoptZCodeTurnIDLocked preserves ownership while replacing a provisional key.
func (a *Agent) adoptZCodeTurnIDLocked(owner *zcodeTurnState, nativeID string) bool {
	if nativeID == "" || owner == nil || a.turns[owner.id] != owner {
		return false
	}
	if existing := a.turns[nativeID]; existing != nil && existing != owner {
		return false
	}
	oldID := owner.id
	delete(a.turns, oldID)
	owner.id = nativeID
	a.turns[nativeID] = owner
	if a.foregroundTurnID == oldID {
		a.foregroundTurnID = nativeID
	}
	if a.stopWindow.turnID == oldID && a.stopWindow.turnGeneration == owner.generation {
		a.stopWindow.turnID = nativeID
	}
	return true
}

// foregroundZCodeTurnLocked returns the unsettled user turn, which is the turn
// a stop targets. The caller holds a.Mu.
func (a *Agent) foregroundZCodeTurnLocked() *zcodeTurnState {
	turn := a.turns[a.foregroundTurnID]
	if turn == nil || turn.settled {
		return nil
	}
	return turn
}

// handleZCodeTurnStarted arms the turn: one owner record per started turn.
//
// A turn whose inputSource is set was started by the RUNTIME, not by the user: a
// background task reporting back, a subagent's reply being folded in, a todo
// reminder. Such a turn opens its own owner record beside the user's, so its
// completion does not end the user's turn and its transcript rows still land.
// A user start replaces only the previous user foreground owner.
func (a *Agent) handleZCodeTurnStarted(event zcodeEventEnvelope) {
	var payload zcodeTurnStarted
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			slog.Warn("zcode turn.started unmarshal failed", "agent_id", a.AgentID(), "error", err)
		}
	}
	background := payload.InputSource != ""
	canonicalRequired := event.archive || event.replayUncertain

	a.Mu.Lock()
	// A provisional foreground owner (one sendInputForSession created before
	// the native start) adopts the native turn ID and execution ID while
	// preserving its generation and input identity. Adoption requires a
	// matching inputId: a start that identifies no input cannot be identified as
	// the provisional turn's own.
	if !background && payload.InputID != "" {
		if prev := a.foregroundZCodeTurnLocked(); prev != nil && strings.HasPrefix(prev.id, "#turn-") && prev.inputID == payload.InputID {
			if event.TurnID != "" && !a.adoptZCodeTurnIDLocked(prev, event.TurnID) {
				a.Mu.Unlock()
				return
			}
			prev.canonicalRequired = prev.canonicalRequired || canonicalRequired
			prev.foregroundExecutionID = payload.ForegroundExecutionID
			if payload.MessageID != "" {
				prev.anchorMessageID = payload.MessageID
			}
			turnID := prev.id
			a.Mu.Unlock()
			a.PublishTurnActive()
			a.observeZCodeSteerTurnStart(payload.InputID)
			a.resetZCodeTurnProgress(prev)
			slog.Debug("zcode provisional turn adopted", "agent_id", a.AgentID(), "turn_id", turnID)
			return
		}
	}
	turn := a.createZCodeTurnLocked(zcodeTurnState{
		id:                    event.TurnID,
		background:            background,
		foregroundExecutionID: payload.ForegroundExecutionID,
		acceptedAt:            a.Clock().Now(),
		admissionRevision:     a.stateRevision,
		anchorMessageID:       payload.MessageID,
		inputID:               payload.InputID,
		canonicalRequired:     canonicalRequired,
	})
	if admission := a.inputAdmissions[payload.InputID]; admission != nil && admission.sessionID == a.sessionID && admission.epoch == a.sessionEpoch {
		admission.owner = turn
	}
	a.refreshTurnActiveLocked()
	a.Mu.Unlock()
	a.PublishTurnActive()
	// A steer that ZCode moved to the ordinary queue runs as this turn's prompt.
	a.observeZCodeSteerTurnStart(payload.InputID)

	if !background {
		a.resetZCodeTurnProgress(turn)
	}
	slog.Debug("zcode turn started", "agent_id", a.AgentID(), "turn", payload.TurnNumber, "input_source", payload.InputSource)
}

// zcodeTurnCompleted is the turn.completed payload.
type zcodeTurnCompleted struct {
	Response      string      `json:"response"`
	TokenCount    int64       `json:"tokenCount"`
	Usage         *zcodeUsage `json:"usage"`
	ToolCallCount int32       `json:"toolCallCount"`
	Duration      float64     `json:"duration"`
	ResultType    string      `json:"resultType"`
}

func (a *Agent) handleZCodeTurnCompleted(event zcodeEventEnvelope, owner *zcodeTurnState) {
	var payload zcodeTurnCompleted
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			slog.Warn("zcode turn.completed unmarshal failed", "agent_id", a.AgentID(), "error", err)
		}
	}
	if payload.Usage != nil {
		a.recordZCodeUsageLocked(*payload.Usage)
	}
	cancelled := payload.ResultType == contracts.ZCodeResultCancelled
	a.Mu.Lock()
	foreground := !owner.background && a.foregroundTurnID == owner.id && !owner.settled
	a.Mu.Unlock()
	if foreground {
		// The stop state belongs to the user's turn. A background turn-end event must
		// not consume it.
		cancelled = a.takeStopCancelledRequest() || cancelled
	}
	a.finishZCodeTurn(event, owner, zcodeTurnEnd{toolCallCount: payload.ToolCallCount, cancelled: cancelled})
}

func (a *Agent) takeStopCancelledRequest() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	cancelled := a.stopCancelledRequest
	a.stopCancelledRequest = false
	return cancelled
}

// zcodeTurnFailed is the turn.failed payload. `error.retryable` is the app-server's
// own verdict on whether the failure is transient, which is exactly the question the
// automatic retry decides on -- so no message-text matching is needed.
type zcodeTurnFailed struct {
	Error struct {
		Type      string `json:"type"`
		Message   string `json:"message"`
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		Retryable *bool  `json:"retryable"`
	} `json:"error"`
	TurnPhase string `json:"turnPhase"`
}

func (a *Agent) handleZCodeTurnFailed(event zcodeEventEnvelope, owner *zcodeTurnState) {
	var payload zcodeTurnFailed
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			slog.Warn("zcode turn.failed unmarshal failed", "agent_id", a.AgentID(), "error", err)
		}
	}
	completion := agent.MessageCompletionError
	failureKind := strings.ToLower(payload.Error.Type + " " + payload.Error.Code)
	if strings.Contains(failureKind, "cancel") || strings.Contains(failureKind, "interrupt") {
		completion = agent.MessageCompletionInterrupted
	}
	a.finishZCodeTurn(event, owner, zcodeTurnEnd{
		failedCompletion: completion,
		retryable:        zcodeFailureIsRetryable(payload),
	})
}

// zcodeFailureIsRetryable reports whether a failed turn should be retried.
//
// The app-server's own `retryable` flag decides when it states one. A missing flag
// falls back to "not retryable", with one exception: a provider that carries no
// usable credential is NEVER retryable, because a retry re-reads the same empty
// configuration and produces the same failure forever.
func zcodeFailureIsRetryable(payload zcodeTurnFailed) bool {
	if payload.Error.Code == CauseProviderNotConfigured ||
		payload.Error.Type == CauseProviderNotConfigured {
		return false
	}
	return payload.Error.Retryable != nil && *payload.Error.Retryable
}

// zcodeTurnEnd is what one turn end states beside its event.
type zcodeTurnEnd struct {
	// toolCallCount is ZCode's own count of the turn's calls, or 0 when the event
	// states none. A stopped turn states 0 for a call that the stop cut.
	toolCallCount int32
	// cancelled states that a stop ended the turn. ZCode reports a stopped turn as
	// turn.completed with resultType "cancelled".
	cancelled bool
	// failedCompletion is the outcome a turn.failed closes its scopes and tools
	// with: error, or interrupted when the failure reports a cancellation. A
	// turn.completed leaves it unset and derives its own outcomes from cancelled.
	failedCompletion agent.MessageCompletion
	// retryable is the app-server's own verdict on whether a turn.failed is
	// transient, which the automatic retry decides on. A turn.completed leaves it
	// false: its end cancels any pending server-error retry.
	retryable bool
}

// zcodeTurnDivider freezes native output and presentation metadata at receipt.
type zcodeTurnDivider struct {
	kind             string
	content          agent.MessageContent
	toolUses         int
	nativeCountKnown bool
}

// finishZCodeTurn closes out ONE turn on a native turn-end event. It runs the
// shared settleZCodeTurn transition and supplies its native-divider branch: the
// tool count and cancellation classification the divider states, the background
// notification, and the retry bookkeeping. It settles only the owner the
// turn-end event identifies; every other turn keeps running.
//
// A BACKGROUND turn takes none of the user's bookkeeping. Its completion says
// nothing about the user's turn, and closing the spans there would tear down the
// cards of tool calls the user's own turn is still running. What it does flush
// is its own generation scopes and its own incomplete tools, and its outcome is
// recorded as a notification rather than as the user's turn end.

func (a *Agent) finishZCodeTurn(event zcodeEventEnvelope, owner *zcodeTurnState, end zcodeTurnEnd) {
	a.Mu.Lock()
	finalized := owner.finalized
	a.Mu.Unlock()
	if finalized {
		return
	}
	raw := event.persistBytes()
	if end.toolCallCount > 0 {
		a.Mu.Lock()
		owner.toolUses = int(end.toolCallCount)
		if a.foregroundTurnID == owner.id {
			a.TurnToolUses = owner.toolUses
		}
		a.Mu.Unlock()
	}
	textCompletion, callCompletion := agent.MessageCompletionComplete, agent.MessageCompletionError
	switch {
	case event.Type != contracts.ZCodeEventTurnCompleted:
		textCompletion, callCompletion = end.failedCompletion, end.failedCompletion
	case end.cancelled:
		textCompletion, callCompletion = agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted
	case owner.background:
		callCompletion = agent.MessageCompletionComplete
	}
	source := a.sink.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	sink := a.zcodeProgressSinkForTurn(owner)
	sink.source = source
	content := zcodeTurnContent(raw, a.usageSnapshot())
	if end.cancelled {
		content.Completion = agent.MessageCompletionInterrupted
	}
	content = sink.CaptureMessage(content, agent.SpanInfo{})
	a.Mu.Lock()
	divider := zcodeTurnDivider{kind: event.Type, content: content, toolUses: owner.toolUses, nativeCountKnown: end.toolCallCount > 0}
	canonicalRequired := owner.canonicalRequired
	a.Mu.Unlock()
	persistEnd := func(closed int, publish bool) { a.persistZCodeTurnDivider(owner, divider, closed, publish) }
	if owner.background {
		persistEnd = func(int, bool) { a.persistZCodeBackgroundTurnEnd(owner, raw, textCompletion) }
	}
	settle := zcodeTurnSettlement{textCompletion: textCompletion, callCompletion: callCompletion, persistEnd: persistEnd}
	if raw == nil {
		settle = zcodeTurnSettlement{}
	}
	retryWriter := a.sink.CaptureAutoRetry()
	settle.afterForeground = func() { providerkit.ScheduleOrCancelServerErrorAutoRetry(retryWriter, end.retryable, raw) }
	if raw != nil && canonicalRequired {
		a.queueZCodeNativeSettlement(owner, settle, source)
		return
	}
	a.settleZCodeTurn(owner, settle)
}

// persistZCodeTurnDivider emits activity-ending effects only for an isolated
// current foreground. Historical output still retains its native divider row.
func (a *Agent) persistZCodeTurnDivider(owner *zcodeTurnState, divider zcodeTurnDivider, closed int, publish bool) {
	toolUses := divider.toolUses
	if !divider.nativeCountKnown {
		toolUses += closed
	}
	content := agent.WithToolUseCount(divider.content, toolUses)
	sink := a.zcodeProgressSinkForTurn(owner)
	var err error
	if publish {
		err = sink.PersistTurnEnd(content, agent.SpanInfo{})
	} else {
		err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	}
	if err != nil {
		slog.Error("zcode persist turn end", "agent_id", a.AgentID(), "type", divider.kind, "error", err)
	}
}

// --- streaming ---

// zcodeModelStreaming is the model.streaming payload.
type zcodeModelStreaming struct {
	AssistantMessageID string          `json:"assistantMessageId"`
	Delta              string          `json:"delta"`
	Done               bool            `json:"done"`
	Input              json.RawMessage `json:"input"`
	Kind               string          `json:"kind"`
	ToolCallID         string          `json:"toolCallId"`
	ToolName           string          `json:"toolName"`
}

// handleZCodeModelStreaming counts model deltas and caches streamed tool input.
//
// The app-server filters this event before it leaves: only text_delta and
// reasoning_delta (with a non-empty delta) and the four tool_input kinds are ever
// sent. The start/finish/error and the *_start / *_end phase markers exist in its
// enumeration and never arrive, which is why there is no phase handling here.
//
// Each appended generation delta records its content on the resolved owner.
// Every flush reaches only that owner's scopes.
func (a *Agent) handleZCodeModelStreaming(event zcodeEventEnvelope, owner *zcodeTurnState) {
	var payload zcodeModelStreaming
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode model.streaming unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	// The installed legacy live transport supplies a message ID on every
	// generation delta. V4 history hydration has another shape and does not
	// enter this handler. An incomplete live delta must not share an empty scope.
	if (payload.Kind == StreamTextDelta || payload.Kind == StreamReasoningDelta) && payload.AssistantMessageID == "" {
		return
	}
	batchable := zcodeBatchableDelta(payload.Kind)
	// Archive batching is independent of the live accumulator and has no offsets.
	// Its delta and message identity cannot describe the next live fragment.
	if event.archive && batchable {
		return
	}
	if payload.AssistantMessageID != "" {
		a.Mu.Lock()
		owner.currentAssistantMessageID = payload.AssistantMessageID
		a.Mu.Unlock()
	}
	a.Mu.Lock()
	canonicalRequired := owner.canonicalRequired
	a.Mu.Unlock()
	if canonicalRequired && batchable {
		return
	}
	switch payload.Kind {
	case StreamTextDelta:
		if payload.Delta == "" {
			return
		}
		// Keep reasoning open until the response ends so a retry can discard it.
		textScope := zcodeTextScope(payload.AssistantMessageID)
		a.generationBuffer.Append(textScope, agent.AssembledMessageKindText, payload.Delta, providerkit.JoinVerbatim)
		a.recordZCodeGenerationContent(owner, textScope, payload.Delta)
		a.sink.ReportProgress(agent.ModelTextProgress(textScope, payload.Delta))
	case StreamReasoningDelta:
		if payload.Delta == "" {
			return
		}
		reasoningScope := zcodeReasoningScope(payload.AssistantMessageID)
		a.generationBuffer.Append(reasoningScope, agent.AssembledMessageKindReasoning, payload.Delta, providerkit.JoinVerbatim)
		a.recordZCodeGenerationContent(owner, reasoningScope, payload.Delta)
		a.sink.ReportProgress(agent.ModelTextProgress(reasoningScope, payload.Delta))

	case StreamToolInputStart:
		// The completed model response supplies assistant text after this event.
		// Persist only reasoning, which that response omits -- and only this
		// turn's reasoning, never another owner's.
		a.flushZCodeGenerationKindForTurn(owner, agent.AssembledMessageKindReasoning, agent.MessageCompletionComplete)
		if payload.ToolCallID == "" {
			return
		}
		a.Mu.Lock()
		tc := a.resolveZCodeInvocationLocked(owner, payload.ToolCallID, payload.AssistantMessageID, true)
		if tc == nil || tc.final {
			a.Mu.Unlock()
			return
		}
		if payload.ToolName != "" {
			tc.name = payload.ToolName
		}
		// A restart of the same id drops a partial input, so a retried tool call does
		// not inherit the abandoned attempt's fragment.
		tc.input = nil
		a.Mu.Unlock()

	case StreamToolInputDelta:
		// The input arrives as JSON TEXT split across deltas, so the fragments are
		// concatenated and parsed only once the call is complete.
		if payload.ToolCallID == "" || payload.Delta == "" {
			return
		}
		a.Mu.Lock()
		tc := a.resolveZCodeInvocationLocked(owner, payload.ToolCallID, payload.AssistantMessageID, true)
		if tc == nil || tc.final {
			a.Mu.Unlock()
			return
		}
		tc.input = append(tc.input, payload.Delta...)
		a.Mu.Unlock()

	case StreamToolInputEnd:
		// Nothing to do: the accumulated fragments stay cached until the tool.updated
		// that opens the call consumes them.

	case StreamToolCall:
		// The complete, PARSED input. It supersedes the concatenated fragments, which
		// can be truncated when the model's stream was cut.
		if payload.ToolCallID == "" {
			return
		}
		a.Mu.Lock()
		tc := a.resolveZCodeInvocationLocked(owner, payload.ToolCallID, payload.AssistantMessageID, true)
		if tc == nil || tc.final {
			a.Mu.Unlock()
			return
		}
		if payload.ToolName != "" {
			tc.name = payload.ToolName
		}
		if len(payload.Input) > 0 {
			tc.input = append(json.RawMessage(nil), payload.Input...)
		}
		a.Mu.Unlock()

	default:
		slog.Debug("zcode unknown model.streaming kind", "agent_id", a.AgentID(), "kind", payload.Kind)
	}
}

// zcodeBatchableDelta identifies the native kinds whose replay and live batches overlap.
func zcodeBatchableDelta(kind string) bool {
	return kind == StreamTextDelta || kind == StreamReasoningDelta || kind == StreamToolInputDelta
}

// recordZCodeGenerationContent retains one delivered fragment on its owner, so
// the content survives buffer persistence for a later reconciliation.
func (a *Agent) recordZCodeGenerationContent(owner *zcodeTurnState, scopeID, delta string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if owner.contentByScope == nil {
		owner.contentByScope = make(map[string]*zcodeScopeContent)
	}
	content := owner.contentByScope[scopeID]
	if content == nil {
		content = &zcodeScopeContent{}
		owner.contentByScope[scopeID] = content
	}
	content.observed = append(content.observed, delta...)
}

// --- tool lifecycle ---

// zcodeToolUpdated is the tool.updated payload, across every kind.
//
// The subagent fields (Source / AgentID / AgentType / ChildSessionID) mark an update
// that belongs to a SUBAGENT's own conversation rather than to this one.
type zcodeToolUpdated struct {
	AssistantMessageID string `json:"assistantMessageId"`
	invocation         *zcodeToolInvocation

	Kind           string `json:"kind"`
	ToolCallID     string `json:"toolCallId"`
	ToolName       string `json:"toolName"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	AgentID        string `json:"agentId"`
	AgentType      string `json:"agentType"`
	ChildSessionID string `json:"childSessionId"`
	// ParentToolCallID identifies the native Agent call that started the subagent.
	// The resolver uses the parent invocation or native child identity to find
	// the internal spawn record. After adoption, the native child session
	// identifies the durable child transcript.
	ParentToolCallID string `json:"parentToolCallId"`

	// scheduled
	Input        json.RawMessage `json:"input"`
	InputOmitted bool            `json:"inputOmitted"`
	InputRef     string          `json:"inputRef"`

	// progress. OutputBytes is the combined native total. The per-stream totals
	// supply the fallback when a provider omits it.
	OutputBytes int64  `json:"outputBytes"`
	StdoutBytes int64  `json:"stdoutBytes"`
	StderrBytes int64  `json:"stderrBytes"`
	StdoutTail  string `json:"stdoutTail"`
	StderrTail  string `json:"stderrTail"`

	// result
	Result json.RawMessage `json:"result"`

	// error
	Error json.RawMessage `json:"error"`

	// batch
	ToolCallIDs  []string `json:"toolCallIds"`
	SuccessCount int      `json:"successCount"`
	ErrorCount   int      `json:"errorCount"`
}

func (a *Agent) handleZCodeToolUpdated(event zcodeEventEnvelope, owner *zcodeTurnState) {
	var payload zcodeToolUpdated
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode tool.updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	// A `raw` frame is the app server's own escape hatch: it carries an opaque
	// payload rather than the fields a row reads. It must not become the call's
	// last frame -- a turn that ends while the call runs stores that frame as the
	// row, and the reader would get an unrecognized card where the call's real
	// state belongs.
	if payload.Kind == contracts.ZCodeToolKindRaw {
		slog.Debug("zcode raw tool.updated frame", "agent_id", a.AgentID(), "tool_call_id", payload.ToolCallID)
		return
	}

	if payload.ToolCallID != "" {
		a.Mu.Lock()
		payload.invocation = a.resolveZCodeInvocationLocked(owner, payload.ToolCallID, payload.AssistantMessageID, true)
		if payload.invocation != nil && payload.invocation.final {
			a.Mu.Unlock()
			return
		}
		if payload.invocation != nil {
			payload.invocation.lastFrame = event.persistBytes()
			invocation := payload.invocation
			if invocation.native.ToolCallID == "" || payload.Kind == contracts.ZCodeToolKindScheduled {
				invocation.native = payload
				invocation.native.invocation = nil
			}
		}
		a.Mu.Unlock()
		if payload.invocation == nil {
			return
		}
	}

	switch payload.Kind {
	case contracts.ZCodeToolKindScheduled:
		// A subagent's tool calls go to its child transcript; see subagent.go.
		// If that transcript cannot be resolved or created, keep the update
		// in the parent transcript.
		if zcodeToolFromSubagent(payload) && a.openZCodeSubagentToolCall(event, payload, owner) {
			return
		}
		a.openZCodeToolCall(event, payload, owner)
	case contracts.ZCodeToolKindStarted:
		a.recordZCodeToolStarted(payload)
	case contracts.ZCodeToolKindProgress:
		a.streamZCodeToolProgress(payload)
	case contracts.ZCodeToolKindResult, contracts.ZCodeToolKindError:
		a.closeZCodeToolCall(event, payload, owner)
	case contracts.ZCodeToolKindBatch:
		a.applyZCodeToolBatch(event, payload, owner)
	default:
		slog.Debug("zcode unknown tool.updated kind", "agent_id", a.AgentID(), "kind", payload.Kind)
	}
}

// openZCodeToolCall persists the tool call's opening row into this agent's own
// transcript. A subagent's call goes to its child transcript instead -- see
// subagent.go.
func (a *Agent) openZCodeToolCall(event zcodeEventEnvelope, payload zcodeToolUpdated, owner *zcodeTurnState) {
	a.openZCodeToolCallInto(a.sink, event, payload, owner)
}

// recordZCodeToolStarted notes that a scheduled call began running.
func (a *Agent) recordZCodeToolStarted(payload zcodeToolUpdated) {
	if payload.spanID() == "" {
		return
	}
	// The progress counters are measured from here, so a re-started call does not read
	// its predecessor's byte totals as already-broadcast. Both streams are keyed
	// separately, so both are dropped.
	a.ClearCumulativeOutput(payload.spanID())
	a.zcodeSinkForToolCall(payload.spanID()).ReportProgress(agent.ResetOutputProgress(payload.spanID()))
	// This used to broadcast a `zcode_running_tool` session-info key that nothing
	// read. The shared channel for "which tool is running" is now
	// contracts.SessionInfoKeyRunningTool, which the tool card renders as a live
	// badge. ZCode does not feed it yet: the badge shows an elapsed time or a
	// retry state, and the app-server reports neither for a tool call, so an entry
	// from here would render nothing. Broadcast one from this site (span_id =
	// payload.spanID()) once ZCode reports either. The clear needs no code --
	// the frontend drops a span's entry when its result row lands, and
	// closeZCodeToolCall already persists that row with Closing: true.
	//
	// The other route to a ZCode badge needs nothing from the app-server: the
	// browser can count from the tool_use row's own timestamp, which would give
	// every provider a badge at once.
	// https://github.com/leapmux/leapmux/issues/439
}

// streamZCodeToolProgress reports the native combined byte total when present.
func (a *Agent) streamZCodeToolProgress(payload zcodeToolUpdated) {
	if payload.spanID() == "" {
		return
	}
	a.Mu.Lock()
	tc := payload.invocation
	if tc != nil {
		tc.progress = payload
	}
	a.Mu.Unlock()
	sink := a.zcodeSinkForToolCall(payload.spanID())
	// The two tails are SEPARATE streams, each cut at a byte count rather than at a
	// line end, so they join with a line break between them. Concatenating them
	// would glue a mid-line stdout tail to the first stderr line and show one line
	// that neither stream wrote.
	if tail := zcodeJoinOutputTails(payload.StdoutTail, payload.StderrTail); tail != "" {
		// Whether the app server DROPPED bytes, which its own counts answer. A tail
		// is usually a window, but a short command's tail is its WHOLE output, and
		// asserting the flag drew "earlier output was dropped" under a complete
		// three-byte result. Compared per stream, because the join inserts a line
		// break the byte counts do not carry.
		//
		// A frame that states no count at all is the third case: the size of what it
		// dropped is unknown, so the tail stays a window -- the same reading that
		// makes the byte total below a minimum.
		var truncated bool
		switch {
		case payload.StdoutBytes > 0 || payload.StderrBytes > 0:
			truncated = payload.StdoutBytes > int64(len(payload.StdoutTail)) || payload.StderrBytes > int64(len(payload.StderrTail))
		case payload.OutputBytes > 0:
			truncated = payload.OutputBytes > int64(len(payload.StdoutTail)+len(payload.StderrTail))
		default:
			truncated = true
		}
		sink.ReportProgress(agent.OutputTailProgress(payload.spanID(), tail, truncated))
	}
	total := payload.OutputBytes
	if total <= 0 {
		total = agent.SaturatingAdd(payload.StdoutBytes, payload.StderrBytes)
	}
	if total <= 0 {
		combined := payload.StdoutTail + payload.StderrTail
		if combined == "" {
			return
		}
		observed := a.ObserveCumulativeOutput(payload.spanID(), combined, true)
		sink.ReportProgress(agent.OutputTotalProgress(payload.spanID(), observed.Total, observed.Minimum))
		return
	}
	sink.ReportProgress(agent.OutputExactTotalProgress(payload.spanID(), total))
}

// zcodeRecoveredClose ends a tool call whose own result the agent never sent.
//
// Its presence is what redirects the row to the call's own last frame, so a nil
// outcome note cannot quietly store the event that triggered the close instead.
type zcodeRecoveredClose struct {
	// outcome is the tool-outcome metadata, which states what LeapMux concluded.
	outcome []byte
	// status is what the subagent registry row becomes. The payload cannot supply it,
	// because there is no result to read it from.
	status bgtask.Status
}

// closeZCodeToolCall persists the tool call's final row into the transcript that
// holds its opening row -- this agent's own, or a subagent's child transcript.
// The owner is the turn the call belongs to, and what its close counts against.
func (a *Agent) closeZCodeToolCall(event zcodeEventEnvelope, payload zcodeToolUpdated, owner *zcodeTurnState) {
	a.closeZCodeToolCallInto(a.zcodeSinkForToolCall(payload.spanID()), event, payload, nil, owner)
}

// persistIncompleteZCodeTools closes each announced call that never reported its end,
// with completion, and returns how many it closed. It is the teardown variant: a
// turn end closes only its own turn's calls, through
// persistIncompleteZCodeToolsForTurn.
func (a *Agent) persistIncompleteZCodeTools(completion agent.MessageCompletion) int {
	return a.persistIncompleteZCodeToolsFor(nil, completion, "")
}

// persistIncompleteZCodeToolsForTurn closes the announced calls of ONE turn that
// never reported their end, with completion, and returns how many it closed. A
// turn end counts them as calls of the turn (see persistZCodeTurnDivider).
func (a *Agent) persistIncompleteZCodeToolsForTurn(owner *zcodeTurnState, completion agent.MessageCompletion) int {
	return a.persistIncompleteZCodeToolsFor(owner, completion, "")
}

// persistIncompleteZCodeToolsFor closes the announced calls of owner -- every
// call when owner is nil -- that never reported their end.
func (a *Agent) persistIncompleteZCodeToolsForChild(spawnID string, completion agent.MessageCompletion) int {
	return a.persistIncompleteZCodeToolsFor(nil, completion, spawnID)
}

type zcodePendingToolClose struct {
	invocation *zcodeToolInvocation
	payload    zcodeToolUpdated
	frame      []byte
	metadata   []byte
	owner      *zcodeTurnState
}

func (a *Agent) persistIncompleteZCodeToolsFor(owner *zcodeTurnState, completion agent.MessageCompletion, childSpawnID string) int {
	a.Mu.Lock()
	var pending []zcodePendingToolClose
	for spanID, tool := range a.toolInvocations {
		if tool == nil || tool.final || tool.name == "" || len(tool.lastFrame) == 0 {
			continue
		}
		callOwner := tool.owner
		if owner != nil && callOwner != owner {
			continue
		}
		a.children.mu.Lock()
		spawnID := a.children.toolSpawns[spanID]
		spawn := a.children.spawns[spawnID]
		liveBackgroundChild := spawn != nil && spawn.background && spawn.childAgentID != ""
		a.children.mu.Unlock()
		if childSpawnID != "" && spawnID != childSpawnID {
			continue
		}
		if owner != nil && liveBackgroundChild {
			continue
		}
		payload := tool.native
		payload.ToolCallID, payload.ToolName, payload.invocation = tool.nativeCallID, tool.name, tool
		pending = append(pending, zcodePendingToolClose{invocation: tool, payload: payload, frame: tool.lastFrame, metadata: tool.metadata(nil), owner: callOwner})
		tool.final = true
		tool.name = ""
		tool.input = nil
		tool.lastFrame = nil
		tool.progress = zcodeToolUpdated{}
		tool.native.Input, tool.native.Result, tool.native.Error = nil, nil, nil
	}
	a.Mu.Unlock()
	if a.IsDiscardingOutput() {
		return len(pending)
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].invocation.order < pending[right].invocation.order })
	for _, close := range pending {
		spanID := close.invocation.spanID
		sink := a.zcodeSinkForToolCall(spanID)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: close.frame, Metadata: close.metadata, Completion: completion}, agent.SpanInfo{SpanID: spanID, SpanType: close.payload.ToolName, Closing: true}); err != nil {
			slog.Error("persist incomplete zcode tool", "agent_id", a.AgentID(), "tool_call_id", close.payload.ToolCallID, "error", err)
		}
		sink.CloseSpan(spanID)
		a.ClearCumulativeOutput(spanID)
		sink.ReportProgress(agent.CompleteOutputProgress(spanID))
		a.applyZCodeSubagentEnd(close.payload, &zcodeRecoveredClose{status: agent.IncompleteTaskStatus(completion)})
		a.closeZCodeSubagentChild(close.payload)
		a.children.forgetTool(spanID)
		a.children.forgetTitle(spanID)
		a.toolCallPrompts.Take(spanID)
		if close.owner != nil {
			a.Mu.Lock()
			a.dropZCodeTurnLocked(close.owner)
			a.Mu.Unlock()
		}
	}
	return len(pending)
}

// applyZCodeToolBatch backfills the calls a batch summary lists.
//
// A batch arrives AFTER the per-call results and only summarizes them, so it must
// close only an id that was opened and never reached a final state -- a call whose
// own result was lost. Closing one that already finished would double its row.
func (a *Agent) applyZCodeToolBatch(event zcodeEventEnvelope, payload zcodeToolUpdated, owner *zcodeTurnState) {
	for _, id := range payload.ToolCallIDs {
		if id == "" {
			continue
		}
		a.Mu.Lock()
		tc := a.findZCodeInvocationLocked(owner, id, "")
		// A retained frame is what marks a call the agent ANNOUNCED, which is the call
		// that opened a span. A call known only from a model-stream fragment has no
		// frame, no opening row and no span, so closing it would close a span that
		// nothing opened -- and it would leave no frame of the agent's own to store.
		final, toolName, announced := false, "", false
		if tc != nil {
			final, toolName, announced = tc.final, tc.name, len(tc.lastFrame) > 0
		}
		a.Mu.Unlock()
		if final || !announced {
			continue
		}
		recovered := tc.native
		recovered.Kind, recovered.ToolName, recovered.invocation = contracts.ZCodeToolKindResult, toolName, tc
		a.closeZCodeToolCallInto(a.zcodeSinkForToolCall(tc.spanID), event, recovered, &zcodeRecoveredClose{
			outcome: zcodeBatchOutcome(payload),
			status:  zcodeBatchTaskStatus(payload),
		}, owner)
	}
}

// zcodeBatchOutcome states what the batch summary lets LeapMux conclude about ONE of
// the calls it lists.
//
// The summary reports aggregate counts, so it cannot say WHICH call failed. A batch
// with no error means every call in it succeeded; a batch with one means this call's
// outcome is unknown. Neither answer is the agent's own, so it travels as LeapMux
// metadata and never as a result the agent did not send.
func zcodeBatchOutcome(batch zcodeToolUpdated) []byte {
	result := contracts.ToolOutcomeOutcomeSucceeded
	if batch.ErrorCount > 0 {
		result = contracts.ToolOutcomeOutcomeUnknown
	}
	encoded, err := json.Marshal(map[string]any{
		contracts.MessageMetadataFieldToolOutcome: map[string]string{
			contracts.ToolOutcomeFieldSource:  contracts.ToolOutcomeSourceBatchSummary,
			contracts.ToolOutcomeFieldOutcome: result,
		},
	})
	if err != nil {
		// Every value is a constant string, so this cannot fail. An empty note still
		// closes the span, which is what keeps the card from staying open for good.
		slog.Error("zcode marshal batch outcome", "error", err)
		return nil
	}
	return encoded
}

// zcodeBatchTaskStatus is what a subagent row becomes when only a batch summary
// reports its call. A batch that counted an error cannot say which call failed, so
// the row stops rather than claiming either outcome.
func zcodeBatchTaskStatus(batch zcodeToolUpdated) bgtask.Status {
	if batch.ErrorCount > 0 {
		return bgtask.StatusStopped
	}
	return bgtask.StatusSucceeded
}

// --- session.updated ---

// zcodeSessionUpdateKind identifies the one shape that a `session.updated` payload
// carries.
type zcodeSessionUpdateKind int

// The kinds, in the discrimination order of `kind()`.
const (
	// zcodeSessionUpdateDrop is a telemetry shape LeapMux does not read.
	zcodeSessionUpdateDrop zcodeSessionUpdateKind = iota
	zcodeSessionUpdateNetworkStatus
	zcodeSessionUpdateBackgroundTask
	zcodeSessionUpdateSubagent
	zcodeSessionUpdateModelResponse
	zcodeSessionUpdateGoalVerification
	zcodeSessionUpdateGoalChange
	zcodeSessionUpdateModeChange
	zcodeSessionUpdateCompaction
	zcodeSessionUpdateMicrocompact
	zcodeSessionUpdateModelSelected
	zcodeSessionUpdateHookRun
	zcodeSessionUpdateHookAdmission
	zcodeSessionUpdateSteerDiscarded
)

// zcodeSessionUpdatedPayload is one `session.updated` payload.
//
// `session.updated` is the app-server's catch-all: every internal event it does not
// map explicitly becomes one, with a free-form payload. Every field is therefore
// kept raw: a payload of one shape that gives a key another JSON type must not
// fail the decode for every shape. The handlers decode what they need from the
// raw bytes.
type zcodeSessionUpdatedPayload struct {
	// A network status. The `type` word starts `model_`.
	Type      json.RawMessage `json:"type"`
	RequestID json.RawMessage `json:"requestId"`
	Meta      json.RawMessage `json:"_meta"`

	// A background task's lifecycle.
	TaskID json.RawMessage `json:"taskId"`

	// A subagent lifecycle or message event. The public protocol maps all
	// three internal event types to session.updated, so their payload fields
	// are the discriminator.
	AgentID          json.RawMessage `json:"agentId"`
	ParentToolCallID json.RawMessage `json:"parentToolCallId"`

	// A finished model request: the assistant's text plus what it cost. A network
	// status carries `querySource` also. usageAndContextWindow decodes the cost.
	Content       json.RawMessage `json:"content"`
	StopReason    json.RawMessage `json:"stopReason"`
	QuerySource   json.RawMessage `json:"querySource"`
	Usage         json.RawMessage `json:"usage"`
	ContextWindow json.RawMessage `json:"contextWindow"`

	// A goal check lifecycle and a goal change.
	VerificationID json.RawMessage `json:"verificationId"`
	TargetID       json.RawMessage `json:"targetId"`
	Action         json.RawMessage `json:"action"`
	Source         json.RawMessage `json:"source"`
	Target         json.RawMessage `json:"target"`

	// A SessionModeChanged event: the native mode and the plan flag.
	Mode        json.RawMessage `json:"mode"`
	PlanEnabled json.RawMessage `json:"planEnabled"`

	// A compaction lifecycle and a microcompact boundary.
	OperationID json.RawMessage `json:"operationId"`
	Trigger     json.RawMessage `json:"trigger"`
	Status      json.RawMessage `json:"status"`
	Strategy    json.RawMessage `json:"strategy"`

	// A model switch the registry made.
	ModelSelection json.RawMessage `json:"modelSelection"`
	Origin         json.RawMessage `json:"origin"`

	// A hook run.
	HookRunID        json.RawMessage `json:"hookRunId"`
	HookInvocationID json.RawMessage `json:"hookInvocationId"`
	Descriptor       json.RawMessage `json:"descriptor"`
	Outcome          json.RawMessage `json:"outcome"`

	// A workspace hook admission.
	PendingCount      json.RawMessage `json:"pendingCount"`
	BundleDigest      json.RawMessage `json:"bundleDigest"`
	WorkspaceIdentity json.RawMessage `json:"workspaceIdentity"`

	// A discarded queued input.
	PendingInputIDs json.RawMessage `json:"pendingInputIds"`
	Reason          json.RawMessage `json:"reason"`
	TargetTurnID    json.RawMessage `json:"targetTurnId"`
}

// kind tells the shapes apart by which fields they carry, in order of
// specificity -- never on `status`, `toolCallId`, `requestId`, `source`, `mode` or
// `turnId` alone. The browser's `zcodeSessionUpdateKind` applies the same order,
// and the two read the network status prefix and the microcompact strategy from
// one contract.
func (p zcodeSessionUpdatedPayload) kind() zcodeSessionUpdateKind {
	if status, ok := providerkit.RawString(p.Type); ok && strings.HasPrefix(status, contracts.ZCodeNetworkStatusPrefixModel) && providerkit.RawPresent(p.RequestID) {
		return zcodeSessionUpdateNetworkStatus
	}
	if taskID, ok := providerkit.RawString(p.TaskID); ok && taskID != "" {
		return zcodeSessionUpdateBackgroundTask
	}
	if agentID, ok := providerkit.RawString(p.AgentID); ok && agentID != "" {
		if parentCallID, ok := providerkit.RawString(p.ParentToolCallID); ok && parentCallID != "" {
			return zcodeSessionUpdateSubagent
		}
	}
	// Callers decode the complete JSON payload before classification.
	// RawMessage therefore starts at the value, and a quote identifies a string.
	// Decode its text only when the selected handler needs the main reply.
	if len(p.Content) > 0 && p.Content[0] == '"' {
		if stopReason, ok := providerkit.RawString(p.StopReason); ok && stopReason != "" {
			return zcodeSessionUpdateModelResponse
		}
	}
	if providerkit.RawPresent(p.VerificationID) && providerkit.RawPresent(p.TargetID) {
		return zcodeSessionUpdateGoalVerification
	}
	// The `target` KEY marks a goal change, and it is present as an object or as
	// null: `cleared` states its removal through the null.
	if providerkit.RawPresent(p.Action) && providerkit.RawPresent(p.Source) && len(p.Target) > 0 {
		return zcodeSessionUpdateGoalChange
	}
	if _, _, ok := parseZCodeModeChange(p.Mode, p.PlanEnabled); ok {
		return zcodeSessionUpdateModeChange
	}
	if providerkit.RawPresent(p.OperationID) && providerkit.RawPresent(p.Trigger) && providerkit.RawPresent(p.Status) {
		return zcodeSessionUpdateCompaction
	}
	if strategy, ok := providerkit.RawString(p.Strategy); ok && strategy == contracts.ZCodeMicrocompactStrategyLocalToolResultClear {
		return zcodeSessionUpdateMicrocompact
	}
	if providerkit.RawPresent(p.ModelSelection) {
		return zcodeSessionUpdateModelSelected
	}
	if providerkit.RawPresent(p.HookRunID) && providerkit.RawPresent(p.HookInvocationID) {
		return zcodeSessionUpdateHookRun
	}
	if providerkit.RawPresent(p.PendingCount) && providerkit.RawPresent(p.BundleDigest) {
		return zcodeSessionUpdateHookAdmission
	}
	if providerkit.RawPresent(p.PendingInputIDs) && providerkit.RawPresent(p.Reason) && providerkit.RawPresent(p.TargetTurnID) {
		return zcodeSessionUpdateSteerDiscarded
	}
	return zcodeSessionUpdateDrop
}

// usageAndContextWindow decodes what a model response cost. A value of the wrong
// JSON type, or an absent one, states nothing: usage is nil, and contextWindow is
// zero. It never drops the reply that carries it.
func (p zcodeSessionUpdatedPayload) usageAndContextWindow() (usage *zcodeUsage, contextWindow int64) {
	if providerkit.RawPresent(p.Usage) {
		var decoded zcodeUsage
		if json.Unmarshal(p.Usage, &decoded) == nil {
			usage = &decoded
		}
	}
	if json.Unmarshal(p.ContextWindow, &contextWindow) != nil {
		contextWindow = 0
	}
	return usage, contextWindow
}

// networkStatusHasAPIRetry reports whether a network status carries the normalized
// retry record `mapModelNetworkStatusPayload` attaches under `_meta.zcode.apiRetry`.
func (p zcodeSessionUpdatedPayload) networkStatusHasAPIRetry() bool {
	var meta struct {
		ZCode struct {
			APIRetry json.RawMessage `json:"apiRetry"`
		} `json:"zcode"`
	}
	if json.Unmarshal(p.Meta, &meta) != nil {
		return false
	}
	return providerkit.RawPresent(meta.ZCode.APIRetry)
}

// hookRunIsVisibleFailure reports whether a hook run reached an outcome the user
// must see on a descriptor the app-server marks client-visible.
//
// A failure, a block and a timeout each stop the hook short of its work. A
// `cancelled` hook stopped because its turn stopped, so it is not a failure that
// the user must read.
func (p zcodeSessionUpdatedPayload) hookRunIsVisibleFailure() bool {
	outcome, _ := providerkit.RawString(p.Outcome)
	switch outcome {
	case contracts.ZCodeHookRunOutcomeFailed, contracts.ZCodeHookRunOutcomeBlocked, contracts.ZCodeHookRunOutcomeTimedOut:
	default:
		return false
	}
	var descriptor struct {
		ClientVisible bool `json:"clientVisible"`
	}
	if json.Unmarshal(p.Descriptor, &descriptor) != nil {
		return false
	}
	return descriptor.ClientVisible
}

// zcodeSteerDiscardReasonPromoted is the `reason` of a steering queue discard that
// moved the input into a turn of its own. The input runs, so the discard is not
// a loss: ZCode's own client skips it, and the browser never reads the word.
const zcodeSteerDiscardReasonPromoted = "promoted"

// handleZCodeSessionUpdated splits the one overloaded event.
//
// The shapes are told apart by which fields they carry -- see
// zcodeSessionUpdatedPayload.kind. The zcodeSessionUpdateDrop case drops the
// shapes that LeapMux does not read:
//
//   - `compact_boundary`.
//   - The model anomaly warning.
//   - The model request.
//   - The follow-up, queue and steer bookkeeping.
//   - Feedback.
//   - Network requests.
//   - The hook review.
//   - Auto-resolution.
//   - The session fork.
//
// They carry no conversation, and persisting them would fill the transcript with
// telemetry.
func (a *Agent) handleZCodeSessionUpdated(event zcodeEventEnvelope) {
	if len(event.Payload) == 0 {
		return
	}
	var payload zcodeSessionUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode session.updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	switch payload.kind() {
	case zcodeSessionUpdateNetworkStatus:
		// Only the retry notice is worth a row. The other network statuses hold
		// headers and base URLs. A side query's retry follows the rule of its
		// reply: the model call is the app-server's own, so it is not the user's.
		status, _ := providerkit.RawString(payload.Type)
		if !zcodeIsMainQuery(payload.QuerySource) {
			return
		}
		switch status {
		case contracts.ZCodeNetworkStatusRetryScheduled:
			if payload.networkStatusHasAPIRetry() && a.persistZCodeNotification(event) {
				a.retryResolution.NoteRetry()
			}
		case zcodeNetworkStatusRequestCompleted:
			a.retryResolution.Resolve(a.sink)
		}
	case zcodeSessionUpdateBackgroundTask:
		a.handleZCodeBackgroundTask(event)
	case zcodeSessionUpdateSubagent:
		a.handleZCodeSubagentLifecycle(event)
	case zcodeSessionUpdateModelResponse:
		stopReason, _ := providerkit.RawString(payload.StopReason)
		if !zcodeIsMainReply(payload.QuerySource, stopReason) {
			// A side query is a generation the app-server runs for itself. It is
			// not the turn's reply, so it closes no stream, records no usage and
			// persists nothing.
			return
		}
		// The flush and discard below reach only the owner's scopes: a response
		// of one turn must not close another turn's stream.
		a.Mu.Lock()
		owner := a.resolveZCodeTurnLocked(event, "")
		if owner != nil {
			owner.lastEventSeq = max(owner.lastEventSeq, event.Seq)
		}
		a.Mu.Unlock()
		if owner == nil {
			slog.Debug("zcode model response has no owner", "agent_id", a.AgentID(), "turn_id", event.TurnID)
			return
		}
		usage, contextWindow := payload.usageAndContextWindow()
		if contextWindow > 0 {
			a.Mu.Lock()
			a.contextWindow = contextWindow
			a.Mu.Unlock()
		}
		if usage != nil {
			a.recordZCodeUsageLocked(*usage)
		}
		content, _ := providerkit.RawString(payload.Content)
		a.persistZCodeAssistantMessage(event, owner, content)
	case zcodeSessionUpdateGoalVerification:
		a.handleZCodeGoalVerification(event, payload)
	case zcodeSessionUpdateGoalChange:
		a.handleZCodeGoalChanged(payload)
	case zcodeSessionUpdateModeChange:
		mode, planEnabled, _ := parseZCodeModeChange(payload.Mode, payload.PlanEnabled)
		if exitMode := a.handleZCodeModeChangedLocked(mode, planEnabled, parseZCodeModeSource(payload.Source)); exitMode != "" {
			a.sendDeferredZCodeExitMode(exitMode)
		}
	case zcodeSessionUpdateCompaction:
		// A start is live state. Only the native end becomes a stored row.
		status, _ := providerkit.RawString(payload.Status)
		switch status {
		case contracts.ZCodeCompactionStatusStarted, contracts.ZCodeCompactionStatusRetrying:
			trigger, _ := providerkit.RawString(payload.Trigger)
			a.Mu.Lock()
			wasLive := a.compactionLive
			a.compactionLive = true
			a.Mu.Unlock()
			if !wasLive {
				providerkit.PublishCompaction(a.sink, trigger)
			}
			return
		case contracts.ZCodeCompactionStatusCompleted, contracts.ZCodeCompactionStatusSkipped,
			contracts.ZCodeCompactionStatusFailed, contracts.ZCodeCompactionStatusInterrupted:
			a.endZCodeCompaction()
		}
		a.persistZCodeNotification(event)
	case zcodeSessionUpdateMicrocompact:
		a.endZCodeCompaction()
		a.persistZCodeNotification(event)
	case zcodeSessionUpdateModelSelected:
		// Adopt the registry fallback immediately. The session read confirms other selections.
		if origin, _ := providerkit.RawString(payload.Origin); origin == contracts.ZCodeModelSelectionOriginRegistryFallback {
			a.adoptZCodeRegistryFallbackLocked(payload)
			a.persistZCodeNotification(event)
		}
	case zcodeSessionUpdateHookRun:
		if payload.hookRunIsVisibleFailure() {
			a.persistZCodeNotification(event)
		}
	case zcodeSessionUpdateHookAdmission:
		// Hooks that wait for trust raise the workspace-trust question, which
		// states them. See workspace_trust.go.
		a.observeZCodeHookAdmission(event.SessionID, payload)
	case zcodeSessionUpdateSteerDiscarded:
		if a.observeZCodeSteerDiscard(payload) {
			// One of LeapMux's own steers: the ledger reported the outcome, and
			// the queue's row or requeue is the visible result.
			return
		}
		if reason, _ := providerkit.RawString(payload.Reason); reason == zcodeSteerDiscardReasonPromoted {
			return
		}
		a.persistZCodeNotification(event)
	case zcodeSessionUpdateDrop:
		// Telemetry: the per-request model and iteration counters, the provider
		// request record and the shapes that the list above gives.
	}
}

const zcodeNetworkStatusRequestCompleted = "model_request_completed"

func (a *Agent) endZCodeCompaction() {
	a.Mu.Lock()
	wasLive := a.compactionLive
	a.compactionLive = false
	a.Mu.Unlock()
	if wasLive {
		providerkit.EndCompaction(a.sink)
	}
}

func (a *Agent) adoptZCodeRegistryFallbackLocked(payload zcodeSessionUpdatedPayload) {
	var model zcodeModelRef
	if json.Unmarshal(payload.ModelSelection, &model) != nil || model.ModelID == "" {
		return
	}
	window, validWindow := providerkit.RawInt64(payload.ContextWindow)
	a.Mu.Lock()
	a.setZCodeModelLocked(zcodeModelID(model.ProviderID, model.ModelID), true)
	if validWindow && window > 0 {
		a.contextWindow = window
	}
	a.observationSequence++
	a.Mu.Unlock()
	a.captureZCodeSessionObservationLocked()
}

// zcodeIsMainReply reports whether a model response is the RUNNING TURN's reply.
//
// `session.updated` carries every finished generation the app-server makes, and
// most of them are side queries it runs for itself: the completion verifier, the
// goal summary title, the compaction summary, a git commit message. Only the main
// turn's text belongs in the transcript, and only its usage belongs on the context
// meter.
//
// The payload states its role through `querySource` when it carries one: only
// `main_turn` is the reply. A payload with no `querySource` falls back to the
// `stopReason`: `tool_internal` marks a side query. A `querySource` that is not a
// string is not the reply either. The browser replays the same rule; the shared
// corpus is testdata/zcode_model_response_conformance.json.
func zcodeIsMainReply(querySource json.RawMessage, stopReason string) bool {
	if len(querySource) == 0 {
		return stopReason != contracts.ZCodeStopReasonToolInternal
	}
	return zcodeIsMainQuery(querySource)
}

// zcodeIsMainQuery reports whether a `querySource` identifies the model call of
// the running turn. An absent key states no side query. A present key must be
// the string `main_turn`: any other word, and any other JSON type, is a side
// query. A network status has no `stopReason`, so this is its whole rule.
func zcodeIsMainQuery(querySource json.RawMessage) bool {
	if len(querySource) == 0 {
		return true
	}
	var source string
	if json.Unmarshal(querySource, &source) != nil {
		return false
	}
	return source == contracts.ZCodeQuerySourceMainTurn
}

// parseZCodeModeChange reads a SessionModeChanged payload. ok is true only for a
// non-empty string mode beside a boolean plan flag: no other session.updated payload
// carries that pair, and a mode without the flag cannot say whether plan mode is on.
func parseZCodeModeChange(rawMode, rawPlanEnabled json.RawMessage) (mode string, planEnabled bool, ok bool) {
	if len(rawMode) == 0 || len(rawPlanEnabled) == 0 {
		return "", false, false
	}
	if json.Unmarshal(rawMode, &mode) != nil || mode == "" {
		return "", false, false
	}
	// A JSON null decodes into a bool with no error, so it is refused by its bytes.
	if string(rawPlanEnabled) == "null" || json.Unmarshal(rawPlanEnabled, &planEnabled) != nil {
		return "", false, false
	}
	return mode, planEnabled, true
}

// zcodeModeSourceTool is the `source` of a SessionModeChanged event that a plan tool
// caused: EnterPlanMode or ExitPlanMode. A session/setMode request states `command`.
// Source: the app-server bundle of ZCode 3.14.4 (`createRuntimeSessionModePort`).
const zcodeModeSourceTool = "tool"

// parseZCodeModeSource reads the `source` of a SessionModeChanged payload. It is empty
// for a payload that states none or states a value that is not a string.
func parseZCodeModeSource(raw json.RawMessage) string {
	var source string
	if len(raw) == 0 || json.Unmarshal(raw, &source) != nil {
		return ""
	}
	return source
}

func (a *Agent) flushZCodeGeneration(completion agent.MessageCompletion) {
	if err := providerkit.FlushGeneration(&a.generationBuffer, a.sink, completion, a.IsDiscardingOutput()); err != nil {
		slog.Error("zcode failed to persist generation", "agent_id", a.AgentID(), "error", err)
	}
}

// flushZCodeGenerationForTurn persists every buffered scope of ONE turn. A turn
// end never flushes another turn's scopes: they stay buffered for their own
// owner's end.
func (a *Agent) flushZCodeGenerationForTurn(owner *zcodeTurnState, completion agent.MessageCompletion) {
	a.persistZCodeGenerationScopes(owner, a.zcodeTurnBufferedScopes(owner, nil), completion)
}

// flushZCodeGenerationKindForTurn persists the buffered scopes of ONE turn of
// one kind.
func (a *Agent) flushZCodeGenerationKindForTurn(owner *zcodeTurnState, kind agent.AssembledMessageKind, completion agent.MessageCompletion) {
	a.persistZCodeGenerationScopes(owner, a.zcodeTurnBufferedScopes(owner, &kind), completion)
}

// persistZCodeGenerationScopes records queue acceptance even when storage retains a retry.
func (a *Agent) persistZCodeGenerationScopes(owner *zcodeTurnState, scopes []string, completion agent.MessageCompletion) {
	type submission struct {
		scope   string
		content *zcodeScopeContent
		size    int
	}
	submissions := make([]submission, 0, len(scopes))
	a.Mu.Lock()
	for _, scope := range scopes {
		if content := owner.contentByScope[scope]; content != nil {
			submissions = append(submissions, submission{scope, content, len(content.observed)})
		}
	}
	a.Mu.Unlock()
	discarding := a.IsDiscardingOutput()
	err := providerkit.PersistGenerationScopes(&a.generationBuffer, a.zcodeProgressSinkForTurn(owner), scopes, completion, discarding)
	if !discarding && agent.OutputRetained(err) {
		a.Mu.Lock()
		for _, submitted := range submissions {
			if owner.contentByScope[submitted.scope] == submitted.content && len(submitted.content.observed) >= submitted.size {
				submitted.content.submitted = max(submitted.content.submitted, submitted.size)
			}
		}
		a.Mu.Unlock()
	}
	if err != nil {
		slog.Error("zcode failed to persist generation", "agent_id", a.AgentID(), "error", err)
	}
}

// zcodeTurnBufferedScopes lists the scopes of owner that still hold buffered
// content, of one kind when kind is set.
func (a *Agent) zcodeTurnBufferedScopes(owner *zcodeTurnState, kind *agent.AssembledMessageKind) []string {
	a.Mu.Lock()
	owned := make([]string, 0, len(owner.contentByScope))
	for scopeID := range owner.contentByScope {
		owned = append(owned, scopeID)
	}
	a.Mu.Unlock()
	scopes := make([]string, 0, len(owned))
	for _, scopeID := range owned {
		scopeKind, _, ok := a.generationBuffer.ScopeSnapshot(scopeID)
		if !ok || (kind != nil && scopeKind != *kind) {
			continue
		}
		scopes = append(scopes, scopeID)
	}
	return scopes
}

func zcodeReasoningScope(assistantMessageID string) string {
	return "zcode:reasoning:" + assistantMessageID
}

func zcodeTextScope(assistantMessageID string) string {
	return "zcode:text:" + assistantMessageID
}

// persistZCodeAssistantMessage preserves the complete native response. A prefix
// already accepted as assembled output remains represented by that earlier row.
func (a *Agent) persistZCodeAssistantMessage(event zcodeEventEnvelope, owner *zcodeTurnState, text string) {
	a.Mu.Lock()
	messageID := owner.currentAssistantMessageID
	prefix := ""
	if content := owner.contentByScope[zcodeTextScope(messageID)]; messageID != "" && content != nil {
		prefix = string(content.observed[:content.submitted])
	}
	a.Mu.Unlock()
	if !strings.HasPrefix(text, prefix) {
		slog.Error("zcode model response conflicts with accepted output", "agent_id", a.AgentID(), "message_id", messageID)
		return
	}
	a.flushZCodeGenerationKindForTurn(owner, agent.AssembledMessageKindReasoning, agent.MessageCompletionComplete)
	if strings.TrimSpace(text) != "" {
		content := agent.MessageContent{Original: event.persistBytes()}
		if len(content.Original) == 0 {
			return
		}
		if prefix != "" {
			supplement, err := json.Marshal(map[string]any{contracts.ZCodeSupplementResponseText: map[string]string{contracts.ZCodeResponseTextAcceptedPrefix: prefix}})
			if err != nil {
				slog.Error("zcode encode response coverage", "agent_id", a.AgentID(), "error", err)
				return
			}
			content.Supplemental = supplement
		}
		err := a.zcodeProgressSinkForTurn(owner).PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
		if !agent.OutputRetained(err) {
			slog.Error("zcode persist assistant message", "agent_id", a.AgentID(), "error", err)
			return
		}
	}
	a.Mu.Lock()
	if messageID != "" {
		if owner.contentByScope == nil {
			owner.contentByScope = make(map[string]*zcodeScopeContent)
		}
		owner.contentByScope[zcodeTextScope(messageID)] = &zcodeScopeContent{observed: []byte(text), submitted: len(text)}
	}
	owner.acceptedResponses = append(owner.acceptedResponses, zcodeAcceptedModelResponse{messageID: messageID, text: text})
	owner.currentAssistantMessageID = ""
	a.Mu.Unlock()
	if messageID != "" {
		a.generationBuffer.Discard(zcodeTextScope(messageID))
	}
}

// zcodeBackgroundTask is the background-task shape session.updated carries.
type zcodeBackgroundTask struct {
	TaskID         string `json:"taskId"`
	ToolCallID     string `json:"toolCallId"`
	ToolName       string `json:"toolName"`
	TaskKind       string `json:"taskKind"`
	ChildSessionID string `json:"childSessionId"`
	Command        string `json:"command"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Blocked        bool   `json:"blocked"`
	BlockedReason  string `json:"blockedReason"`
	StdoutTail     string `json:"stdoutTail"`
}

// zcodeBackgroundStatus maps a task status onto the registry's. ok is false for a
// status that is not final, so the row stays open rather than being closed by a
// value the registry would treat as absorbing.
func zcodeBackgroundStatus(status string) (bgtask.Status, bool) {
	switch status {
	case "completed":
		return bgtask.StatusSucceeded, true
	case "failed", "spawn_error", "timed_out":
		return bgtask.StatusFailed, true
	case "cancelled":
		return bgtask.StatusStopped, true
	case "lost":
		// The app-server lost track of the task. It will never report again, so leaving
		// the row Running would pin the thinking indicator for good.
		return bgtask.StatusFailed, true
	case "running", "":
		return bgtask.StatusRunning, false
	default:
		// A token outside the app-server's enumeration. Running is the only safe answer:
		// a final status is absorbing, and it tears down the child transcript, so guessing
		// "finished" for a task that is still working destroys live output. Every sibling
		// dispatch in this file logs its unhandled input, and so does this one -- an
		// unknown status pins the row, and a silent default leaves no trace of why.
		slog.Debug("zcode unknown background task status", "status", status)
		return bgtask.StatusRunning, false
	}
}

// handleZCodeBackgroundTask maintains the background-task registry row for one task.
//
// A `bash` task and a `workflow` task reuse the launch card they already have.
// A `subagent` task gets its own child transcript because its output is a
// conversation of its own.
func (a *Agent) handleZCodeBackgroundTask(event zcodeEventEnvelope) {
	var task zcodeBackgroundTask
	if err := json.Unmarshal(event.Payload, &task); err != nil {
		slog.Warn("zcode background task unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if task.TaskID == "" {
		return
	}
	status, final := zcodeBackgroundStatus(task.Status)

	kind := bgtask.KindShell
	switch task.TaskKind {
	case TaskKindSubagent:
		kind = bgtask.KindSubagent
	case TaskKindWorkflow:
		kind = bgtask.KindWorkflow
	}
	title, titleIsCommand := zcodeBackgroundTitle(task)

	// A shell task reuses the launch card the transcript already shows, which is the
	// tool call's own span. A subagent shares its key with the tool.updated path --
	// see zcodeSubagentRowKey.
	rowKey := zcodeSubagentRowKey(task.ToolCallID, task.ChildSessionID, task.TaskID)
	if kind == bgtask.KindSubagent && task.ToolCallID != "" {
		rowKey = a.resolveZCodeSpawn(event, task.ToolCallID, task.ChildSessionID, "")
		if rowKey == "" {
			return
		}
		a.children.rememberSession(rowKey, task.ChildSessionID)
	}

	upsert := bgtask.Upsert{
		RowKey:         a.zcodeSubagentRegistryKey(rowKey),
		Kind:           kind,
		Title:          title,
		TitleIsCommand: titleIsCommand,
		Description:    strings.TrimSpace(task.Description),
		Status:         status,
	}
	if task.Blocked && !final {
		upsert.ActiveForm = task.BlockedReason
	}

	// A workflow run's own row heads the group of its subagents' rows, which only
	// the conversation subscription states. See workflow.go.
	if kind == bgtask.KindWorkflow {
		upsert.GroupKey, upsert.GroupLabel = rowKey, title
		a.Mu.Lock()
		tracked := a.workflows.track(task.TaskID, upsert)
		a.Mu.Unlock()
		if tracked {
			a.reconcileZCodeConversationInBackground()
		}
	}

	// A subagent task with a tool-call ID can resolve or create its child
	// transcript through the same internal spawn record as tool updates.
	//
	// A background task without a tool-call ID records its registry row
	// but does not create a transcript.
	if kind == bgtask.KindSubagent && task.ToolCallID != "" {
		// The task is what an Interrupt of the subagent's tab cancels.
		a.children.rememberTask(rowKey, task.TaskID)
		if status == bgtask.StatusStopped {
			upsert.Status = a.children.cancelledStatus(rowKey)
		}
		childID, childTitle, ok := a.ensureZCodeSubagentTranscript(rowKey, rowKey, title)
		if ok {
			a.children.rememberSession(rowKey, task.ChildSessionID)
			upsert.ChildAgentID = childID
			// The creator may have used the spawn's own label instead of this event's, so
			// the row states what the tab states.
			upsert.Title = childTitle
			// A subagent that reports lifecycle events ends with one, which follows
			// this final status and carries the report, so that event ends the
			// transcript. Ending it here would leave the report nowhere to land.
			if final && !a.children.reportsLifecycle(rowKey) {
				// Drop the index entry too, or the spawn's own result would clean up a
				// transcript this path already tore down.
				a.persistIncompleteZCodeToolsForChild(rowKey, zcodeIncompleteChildCompletion(upsert.Status))
				a.children.takeChild(rowKey)
				a.children.forgetTitle(rowKey)
				a.sink.CleanupChildAgent(childID)
				a.reapZCodeSettledTurns()
			}
		}
	}

	providerkit.LogRegistryRefusal("zcode", "upsert", a.sink.UpsertBackgroundTask(upsert))
}

// zcodeBackgroundTitle labels a background-task row with its command, description,
// or tool name. titleIsCommand is true only for the command, which the app-server
// supplies verbatim. The row can set that value as code without styling prose as code.
func zcodeBackgroundTitle(task zcodeBackgroundTask) (title string, titleIsCommand bool) {
	if cmd := strings.TrimSpace(task.Command); cmd != "" {
		return bgtask.CleanTitleRunes(bgtask.FirstLine(cmd), 120), true
	}
	if description := strings.TrimSpace(task.Description); description != "" {
		return description, false
	}
	return strings.TrimSpace(task.ToolName), false
}

// --- permissions the app-server decided by itself ---

// zcodePermissionResolved is the permission.resolved payload.
type zcodePermissionResolved struct {
	ChildSessionID string `json:"childSessionId"`
	RequestID      string `json:"requestId"`
	ToolCallID     string `json:"toolCallId"`
	ToolName       string `json:"toolName"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason"`
}

const zcodePermissionCancelledReason = "Permission request cancelled"

// handleZCodePermissionResolved records a decision LeapMux did not make.
//
// The app-server resolves some permissions itself -- a rule the user saved in ZCode,
// a mode that allows the tool outright, or a mode that denies it. Only the ones with
// no matching control request are recorded, so an answer the user gave through
// LeapMux is not reported back to them as an automatic decision.
func (a *Agent) handleZCodePermissionResolved(event zcodeEventEnvelope) {
	var payload zcodePermissionResolved
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode permission.resolved unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if payload.Decision == contracts.ZCodeDecisionDeny && payload.Reason == zcodePermissionCancelledReason {
		a.Mu.Lock()
		if a.stopCanCancelLocked() {
			a.stopCancelledRequest = true
		}
		a.Mu.Unlock()
	}
	if a.forgetZCodeControlRequest(payload.RequestID) {
		// The user answered this one through LeapMux; the structured answer row already
		// records it.
		return
	}
	// An automatic ALLOW is the normal case in every mode but plan, and reporting each
	// one would bury the transcript in rows saying "as configured". A DENIAL is the
	// one that needs saying, because the model's next step reacts to it.
	if payload.Decision == contracts.ZCodeDecisionAllow {
		return
	}
	a.persistZCodeNotificationTo(a.zcodePermissionSink(event, payload), event)
}

func (a *Agent) zcodePermissionSink(event zcodeEventEnvelope, payload zcodePermissionResolved) agent.ProviderServices {
	a.Mu.Lock()
	owner := a.resolveZCodeTurnLocked(event, payload.ToolCallID)
	invocation := a.findZCodeInvocationLocked(owner, payload.ToolCallID, "")
	a.Mu.Unlock()
	if invocation != nil {
		if childID, ok := a.children.toolChild(invocation.spanID); ok {
			return a.sink.ChildSink(childID)
		}
	}

	if childID, ok := a.children.sessionChild(payload.ChildSessionID); ok {
		return a.sink.ChildSink(childID)
	}
	return a.sink
}

// handleZCodeUserInputResolved drops the resolved request from the forwarded set.
//
// It renders nothing: a plan approval and a question are both answered through
// LeapMux's own control surface, which persists the answer row. What the event is
// needed for is the bookkeeping -- while the id stays in the set, a later request
// that REUSES it would be mistaken for a re-announcement and never reach the user.
func (a *Agent) handleZCodeUserInputResolved(event zcodeEventEnvelope) {
	if len(event.Payload) == 0 {
		return
	}
	var payload struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode userInput.resolved unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.forgetZCodeControlRequest(payload.RequestID)
}

// --- stream recovery ---

// handleZCodeStreamRecovery consumes the app-server's stream-recovery report.
//
// The report describes a retry of the MODEL PROVIDER's own SSE stream. It is not
// a lapse in LeapMux's event subscription. Its payloads carry `streamMode:"sse"`,
// a failure kind, a retry number and the count of assistant bytes discarded before
// the retry. The event subscription is a separate, sticky registration, and the
// app-server appends every event to its store before it delivers one. A
// re-subscribe recovers nothing. A re-subscribe on every model-provider retry would
// send one RPC that replays nothing. The outcome of the retry still reaches the
// transcript, on `turn.failed` or `turn.completed`.
//
// The one report that changes what LeapMux holds is the DISCARDED TAIL: text the
// app-server threw away before its retry. A later failure would otherwise persist
// it from the generation buffer as if it were the reply, so the discard drops both
// buffered scopes of the message that it identifies and resets their live progress.
// The payload states the
// discard through its byte counters -- the only recovery payload that carries
// them. Every other report keeps the logging-only behavior above.
func (a *Agent) handleZCodeStreamRecovery(event zcodeEventEnvelope) {
	slog.Debug("zcode stream recovery", "agent_id", a.AgentID(), "payload_len", len(event.Payload))
	var payload struct {
		AssistantMessageID      string          `json:"assistantMessageId"`
		DiscardedReasoningBytes json.RawMessage `json:"discardedReasoningBytes"`
		DiscardedTextBytes      json.RawMessage `json:"discardedTextBytes"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return
	}
	if payload.AssistantMessageID == "" {
		return
	}
	if !providerkit.RawPresent(payload.DiscardedReasoningBytes) && !providerkit.RawPresent(payload.DiscardedTextBytes) {
		return
	}
	scopes := []string{zcodeTextScope(payload.AssistantMessageID), zcodeReasoningScope(payload.AssistantMessageID)}
	a.Mu.Lock()
	owner := a.resolveZCodeTurnLocked(event, "")
	if owner != nil {
		for _, scope := range scopes {
			if content := owner.contentByScope[scope]; content != nil {
				if content.submitted == 0 {
					delete(owner.contentByScope, scope)
				} else {
					content.observed = content.observed[:content.submitted]
				}
			}
		}
		if owner.currentAssistantMessageID == payload.AssistantMessageID {
			owner.currentAssistantMessageID = ""
		}
	}
	a.Mu.Unlock()
	providerkit.DiscardGenerationScopes(&a.generationBuffer, a.sink, scopes)
}

// zcodeJoinOutputTails puts the two progress streams on their own lines.
func zcodeJoinOutputTails(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

// zcodeToolCallName returns the tool name cached for a call id, or "".
func (a *Agent) zcodeToolInvocationName(id string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if tc := a.toolInvocations[id]; tc != nil {
		return tc.name
	}
	return ""
}

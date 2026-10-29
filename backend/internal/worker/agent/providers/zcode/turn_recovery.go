package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Per-turn reconciliation watcher.
//
// A turn can stall without a terminal event: the app-server drops it, the
// stream breaks, or the agent process restarts. watchZCodeTurn polls
// session/read after a quiet interval and reconciles the turn's state from the
// snapshot: advancing watermarks prove liveness, a prompt boundary settles it,
// and a frozen watermark past the freeze limit surfaces the error path.
//
// Recovery emits only what the evidence supports. It never fabricates a
// native turn.completed frame; it persists the attributed prompt-boundary
// patch as the recovery evidence and uses settleZCodeTurn for the shared
// cleanup.

// Concrete limits for the turn recovery watcher.
const (
	// zcodeTurnRecoveryQuietInterval is how long the watcher waits between
	// session/read polls. Each poll observes watermarks; a poll whose
	// watermarks advance proves the turn is alive.
	zcodeTurnRecoveryQuietInterval = 30 * time.Second
	// zcodeRecoveredMessageWindow is the page size of one session/messages
	// fetch during final-message reconciliation.
	zcodeRecoveredMessageWindow = 200
	// zcodeTurnRecoveryFreezeLimit is how long the watermarks may stay frozen
	// before the watcher reports the turn lost. A tool call running resets the
	// clock; a watermark advance resets it too.
	zcodeTurnRecoveryFreezeLimit = 10 * time.Minute
)

// Prompt-boundary reasons on a state.updated patch.
const (
	zcodeStateReasonPromptCompleted = "prompt_completed"
	zcodeStateReasonPromptFailed    = "prompt_failed"
)

// zcodeProjectionState is the projection block a session/read reply and a
// state patch carry. It is the liveness watermark set for the recovery watcher.
type zcodeProjectionState struct {
	ContextUsed     int64  `json:"contextUsed"`
	TotalTokenCount int64  `json:"totalTokenCount"`
	TurnCount       int    `json:"turnCount"`
	CurrentTurnID   string `json:"currentTurnId"`
}

// zcodeTurnRecoveryWatermark is the last projection reading the watcher
// observed. Any field advancing proves liveness.
type zcodeTurnRecoveryWatermark struct {
	contextUsed     int64
	totalTokenCount int64
	turnCount       int
	currentTurnID   string
}

// equals reports whether two watermarks are identical.
func (w zcodeTurnRecoveryWatermark) equals(other zcodeTurnRecoveryWatermark) bool {
	return w == other
}

// zcodeTurnRecoveryState is the per-turn watcher bookkeeping. It is stored on
// the Agent (not on zcodeTurnState) so the watcher goroutine can hold a
// cancellable handle independently of the owner's lifetime under a.Mu.
type zcodeTurnRecoveryState struct {
	// cancel stops the watcher goroutine. Set when the watcher starts and
	// called at settlement, stop, or session replacement.
	cancel context.CancelFunc
	// lastWatermark is the last projection reading. Guarded by a.Mu.
	lastWatermark zcodeTurnRecoveryWatermark
	// frozenSince is when the watermark last advanced. Zero until the first
	// observation. Guarded by a.Mu.
	frozenSince time.Time
	// idleSnapshots counts consecutive session/read replies with no
	// runtime.activeTurnId. Two are needed before local settlement is allowed.
	// Guarded by a.Mu.
	idleSnapshots int
	// boundary records the attributed prompt-boundary patch, once one is
	// proven. Guarded by a.Mu.
	boundary *zcodePromptBoundaryEvidence
	// recoverGeneration is the replay generation at watcher start. An
	// obsolete watcher cancels itself. Guarded by a.Mu.
	recoverGeneration uint64
}

// zcodePromptBoundaryEvidence is the attributed prompt-boundary patch of one
// turn. It is the recovery settlement evidence: settleZCodeTurn persists these
// bytes rather than a fabricated turn.completed frame.
type zcodePromptBoundaryEvidence struct {
	reason     string
	revision   int64
	projection zcodeProjectionState
	raw        []byte
}

// --- watcher lifecycle ---

// startZCodeTurnWatcherLocked starts the per-turn recovery watcher while
// a.Mu is already held. The watcher polls session/read after each quiet
// interval and reconciles the turn's state.
func (a *Agent) startZCodeTurnWatcherLocked(owner *zcodeTurnState) {
	if owner == nil || owner.settled {
		return
	}
	if a.recoverWatchers == nil {
		a.recoverWatchers = map[string]*zcodeTurnRecoveryState{}
	}
	if existing := a.recoverWatchers[owner.id]; existing != nil && existing.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.Context())
	state := &zcodeTurnRecoveryState{
		cancel:            cancel,
		recoverGeneration: a.replay.generation,
	}
	a.recoverWatchers[owner.id] = state
	go a.runZCodeTurnWatcher(ctx, owner, state)
}

// stopZCodeTurnWatcher cancels the watcher for one owner.
func (a *Agent) stopZCodeTurnWatcher(turnID string) {
	a.Mu.Lock()
	state := a.recoverWatchers[turnID]
	delete(a.recoverWatchers, turnID)
	a.Mu.Unlock()
	if state != nil && state.cancel != nil {
		state.cancel()
	}
}

// stopAllZCodeTurnWatchers cancels every recovery watcher. Teardown and
// ClearContext call it.
func (a *Agent) stopAllZCodeTurnWatchers() {
	a.Mu.Lock()
	watchers := a.recoverWatchers
	a.recoverWatchers = nil
	a.Mu.Unlock()
	for _, state := range watchers {
		if state != nil && state.cancel != nil {
			state.cancel()
		}
	}
}

// runZCodeTurnWatcher is the watcher loop. It waits the quiet interval on the
// agent's clock and runs one recovery cycle. A settled or lost turn ends the
// loop; cancellation also ends it.
func (a *Agent) runZCodeTurnWatcher(ctx context.Context, owner *zcodeTurnState, state *zcodeTurnRecoveryState) {
	defer state.cancel()
	for {
		timer := a.Clock().NewTimer(zcodeTurnRecoveryQuietInterval, zcodeTurnRecoveryTimerTag)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.ProcessDone():
			timer.Stop()
			return
		}
		settled, err := a.checkZCodeTurnRecovery(ctx, owner, state)
		if err != nil {
			slog.Warn("zcode turn recovery check failed", "agent_id", a.AgentID(), "turn_id", owner.id, "error", err)
		}
		if settled {
			return
		}
	}
}

const zcodeTurnRecoveryTimerTag = "zcode-turn-recovery"

// checkZCodeTurnRecovery runs one recovery cycle for a turn. It polls
// session/read, advances the watermark, attributes any prompt boundary, and
// decides whether local settlement is warranted. It returns true when the turn
// is settled and the watcher can stop.
func (a *Agent) checkZCodeTurnRecovery(ctx context.Context, owner *zcodeTurnState, state *zcodeTurnRecoveryState) (bool, error) {
	a.Mu.Lock()
	sessionID := a.sessionID
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if sessionID == "" || stopped {
		return true, nil
	}

	raw, err := a.sendZCodeRequestContext(ctx, MethodSessionRead, map[string]any{"sessionId": sessionID}, a.APITimeout())
	if err != nil {
		if zcodeIsMethodNotFound(err) {
			return true, nil
		}
		return false, err
	}

	var snap zcodeSessionReadSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return false, fmt.Errorf("decode session/read: %w", err)
	}

	// If the snapshot reports a gap against the dispatch cursor, complete
	// replay BEFORE applying the observation. Otherwise the snapshot could
	// justify settlement while undelivered events still hold the real end.
	// The snapshot's own eventSeq counts: a gap it reports exists whether or
	// not the live watermark ever saw the missing events.
	a.dispatchMu.Lock()
	high := a.replay.highWatermark
	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	a.dispatchMu.Unlock()
	snapshotSeq := int64(0)
	if snap.Runtime != nil {
		snapshotSeq = snap.Runtime.EventSeq
	}
	if high > cursor || snapshotSeq > cursor {
		if err := a.replayZCodeEvents(ctx); err != nil {
			return false, err
		}
	}

	return a.applyZCodeRecoverySnapshot(owner, state, snap)
}

// zcodeSessionReadSnapshot is the subset of a session/read reply the recovery
// watcher reads.
type zcodeSessionReadSnapshot struct {
	Runtime    *zcodeRuntimeState     `json:"runtime"`
	Projection *zcodeProjectionState  `json:"projection"`
	Settings   *zcodeSettingsSnapshot `json:"settings"`
}

// applyZCodeRecoverySnapshot folds one session/read reply into the watcher's
// state and decides whether the turn settles. It returns true when settled.
func (a *Agent) applyZCodeRecoverySnapshot(owner *zcodeTurnState, state *zcodeTurnRecoveryState, snap zcodeSessionReadSnapshot) (bool, error) {
	now := a.Clock().Now()

	a.Mu.Lock()
	// Adopt the native turn ID on a provisional owner.
	if owner.id == "" || strings.HasPrefix(owner.id, "#turn-") {
		if snap.Runtime != nil && snap.Runtime.ActiveTurnID != "" {
			if existing := a.turns[snap.Runtime.ActiveTurnID]; existing == nil || existing == owner {
				if strings.HasPrefix(owner.id, "#turn-") || owner.id == "" {
					// Re-key the owner under its native ID.
					delete(a.turns, owner.id)
					owner.id = snap.Runtime.ActiveTurnID
					a.turns[owner.id] = owner
					if !owner.background {
						a.foregroundTurnID = owner.id
					}
				}
			}
		}
	}

	// Watermark.
	var projection zcodeProjectionState
	if snap.Projection != nil {
		projection = *snap.Projection
	}
	watermark := zcodeTurnRecoveryWatermark{
		contextUsed:     projection.ContextUsed,
		totalTokenCount: projection.TotalTokenCount,
		turnCount:       projection.TurnCount,
		currentTurnID:   projection.CurrentTurnID,
	}
	advanced := !watermark.equals(state.lastWatermark)
	state.lastWatermark = watermark
	if advanced {
		state.frozenSince = now
	} else if state.frozenSince.IsZero() {
		state.frozenSince = now
	}

	// Idle snapshot tracking.
	activeTurnID := ""
	if snap.Runtime != nil {
		activeTurnID = snap.Runtime.ActiveTurnID
	}
	if activeTurnID == "" {
		state.idleSnapshots++
	} else {
		state.idleSnapshots = 0
	}

	hasActiveTool := a.hasActiveZCodeToolLocked(owner)
	settled := owner.settled
	a.Mu.Unlock()

	if settled {
		a.stopZCodeTurnWatcher(owner.id)
		return true, nil
	}

	// A frozen watermark past the freeze limit, with no active tool, surfaces
	// the error path. A tool running or a watermark advance resets the clock.
	if !advanced && !hasActiveTool && !state.frozenSince.IsZero() {
		if now.Sub(state.frozenSince) >= zcodeTurnRecoveryFreezeLimit {
			a.settleZCodeTurnLost(owner, state)
			return true, nil
		}
	}

	// Local settlement requires an attributed boundary AND two idle snapshots
	// AND no active tool. Two idle snapshots alone establish only inactivity.
	a.Mu.Lock()
	boundary := state.boundary
	a.Mu.Unlock()
	if boundary != nil && state.idleSnapshots >= 2 && !hasActiveTool {
		a.settleZCodeTurnFromRecovery(owner, state, boundary)
		return true, nil
	}

	return false, nil
}

// hasActiveZCodeToolLocked reports whether any tool call of the owner is still
// open. The caller holds a.Mu.
func (a *Agent) hasActiveZCodeToolLocked(owner *zcodeTurnState) bool {
	for id := range owner.toolCallIDs {
		tc := a.toolCalls[id]
		if tc != nil && !tc.final && len(tc.lastFrame) > 0 {
			return true
		}
	}
	return false
}

// --- prompt-boundary attribution ---

// attributeZCodePromptBoundary decides whether a prompt_completed or
// prompt_failed patch is evidence for a specific turn owner. The caller holds
// a.Mu.
//
// ALL available evidence must agree:
//
//   - The patch revision is >= the owner's admissionRevision.
//   - projection.currentTurnId equals the owner's native ID when both are
//     present.
//   - projection.turnCount advanced beyond the owner's baselineTurnCount.
//   - Any input identity matches the owner's inputID.
//
// An ID-less boundary is usable only for one unambiguous unsettled owner. A
// background boundary is not evidence for the foreground watcher. Ambiguous
// evidence stays unattributed.
func (a *Agent) attributeZCodePromptBoundaryLocked(owner *zcodeTurnState, reason string, revision int64, projection zcodeProjectionState, raw []byte) *zcodePromptBoundaryEvidence {
	if owner == nil || owner.settled {
		return nil
	}
	// The boundary must be at least as new as the admission.
	if revision > 0 && owner.admissionRevision > 0 && revision < owner.admissionRevision {
		return nil
	}
	// When both sides name a turn, they must agree.
	if projection.CurrentTurnID != "" && owner.id != "" && !strings.HasPrefix(owner.id, "#turn-") {
		if projection.CurrentTurnID != owner.id {
			return nil
		}
	}
	// The turn counter must have advanced past this turn's baseline.
	if owner.baselineTurnCount > 0 && projection.TurnCount > 0 && projection.TurnCount <= owner.baselineTurnCount {
		return nil
	}
	// A background boundary is not evidence for the foreground watcher.
	if owner.background && a.foregroundTurnID == owner.id {
		return nil
	}
	return &zcodePromptBoundaryEvidence{
		reason:     reason,
		revision:   revision,
		projection: projection,
		raw:        raw,
	}
}

// attributeZCodePromptBoundaryForUnsettled applies an ID-less boundary to the
// sole unsettled owner when exactly one exists. The caller holds a.Mu.
func (a *Agent) attributeZCodePromptBoundaryForUnsettledLocked(reason string, revision int64, projection zcodeProjectionState, raw []byte) *zcodePromptBoundaryEvidence {
	var sole *zcodeTurnState
	for _, turn := range a.turns {
		if turn.settled {
			continue
		}
		if sole != nil {
			// Ambiguous: more than one unsettled owner.
			return nil
		}
		sole = turn
	}
	if sole == nil {
		return nil
	}
	return a.attributeZCodePromptBoundaryLocked(sole, reason, revision, projection, raw)
}

// recordZCodePromptBoundary stores the attributed boundary on the owner's
// recovery state so the watcher can settle from it.
func (a *Agent) recordZCodePromptBoundary(owner *zcodeTurnState, evidence *zcodePromptBoundaryEvidence) {
	if owner == nil || evidence == nil {
		return
	}
	a.Mu.Lock()
	state := a.recoverWatchers[owner.id]
	if state != nil && state.boundary == nil {
		state.boundary = evidence
	}
	a.Mu.Unlock()
}

// handleZCodePromptBoundary is the entry point for a prompt_completed or
// prompt_failed state patch. It attributes the boundary to a turn owner and
// records it as recovery evidence.
func (a *Agent) handleZCodePromptBoundary(notif zcodeStatePatch, body zcodeStatePatchBody) {
	if notif.Reason != zcodeStateReasonPromptCompleted && notif.Reason != zcodeStateReasonPromptFailed {
		return
	}
	var projection zcodeProjectionState
	if body.Projection != nil {
		projection = *body.Projection
	}
	raw := notif.Patch

	a.Mu.Lock()
	// Try each unsettled owner; attribute only when evidence is unambiguous.
	// An ID-ful boundary (projection names the turn) goes to that owner.
	if projection.CurrentTurnID != "" {
		if owner := a.turns[projection.CurrentTurnID]; owner != nil {
			evidence := a.attributeZCodePromptBoundaryLocked(owner, notif.Reason, notif.Revision, projection, raw)
			a.Mu.Unlock()
			a.recordZCodePromptBoundary(owner, evidence)
			return
		}
	}
	// ID-less boundary: try the sole unsettled owner.
	sole := a.attributeZCodePromptBoundaryForUnsettledLocked(notif.Reason, notif.Revision, projection, raw)
	var soleOwner *zcodeTurnState
	if sole != nil {
		for _, turn := range a.turns {
			if !turn.settled {
				soleOwner = turn
				break
			}
		}
	}
	a.Mu.Unlock()
	if soleOwner != nil && sole != nil {
		a.recordZCodePromptBoundary(soleOwner, sole)
	}
}

// --- final-message reconciliation ---

// zcodeNativeMessage is one message from a session/messages reply.
type zcodeNativeMessage struct {
	Info  zcodeNativeMessageInfo `json:"info"`
	Parts []zcodeNativePart      `json:"parts"`
}

// zcodeNativeMessageInfo identifies one native message.
type zcodeNativeMessageInfo struct {
	ID   string                 `json:"id"`
	Role string                 `json:"role"`
	Time zcodeNativeMessageTime `json:"time"`
}

// zcodeNativeMessageTime carries the message's creation timestamp.
type zcodeNativeMessageTime struct {
	Created float64 `json:"created"`
}

// zcodeNativePart is one part of a native message. Part IDs only order and
// deduplicate; the text aggregates into the scope of the parent message.
type zcodeNativePart struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// Native part types.
const (
	zcodeNativePartTypeText      = "text"
	zcodeNativePartTypeReasoning = "reasoning"
)

// reconcileZCodeFinalMessages fetches the owner's final assistant messages and
// emits only the proven missing suffix of each scope.
//
// Window policy: request limit 200 (newest 200). Select messages starting
// after anchorMessageID when known. If the window is full and the anchor or
// expected final assistant message is absent, retry with no limit. If the
// full read still lacks required evidence, FAIL rather than treating a
// truncated prefix as complete.
//
// Part IDs order and deduplicate. Text aggregates into zcodeTextScope,
// reasoning into zcodeReasoningScope. Comparison and emission run under
// dispatchMu so live output cannot interleave.
func (a *Agent) reconcileZCodeFinalMessages(ctx context.Context, owner *zcodeTurnState) error {
	if owner == nil {
		return nil
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("no session for message reconciliation")
	}

	// Phase 1: fetch the bounded window (outside the lock).
	messages, err := a.fetchZCodeSessionMessages(ctx, sessionID, zcodeRecoveredMessageWindow)
	if err != nil {
		return fmt.Errorf("session/messages: %w", err)
	}

	// Phase 2: select owner messages and check for truncation.
	selected, truncated := selectZCodeOwnerMessages(owner, messages)

	// Phase 3: if truncated and the anchor is absent, retry unlimited.
	if truncated {
		allMessages, err := a.fetchZCodeSessionMessages(ctx, sessionID, 0)
		if err != nil {
			return fmt.Errorf("session/messages (full): %w", err)
		}
		selected, truncated = selectZCodeOwnerMessages(owner, allMessages)
		if truncated {
			return fmt.Errorf("session/messages lacks the anchor for turn %s after a full read", owner.id)
		}
	}

	// Phase 4: aggregate parts and compare with retained content under dispatchMu.
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	return a.applyZCodeRecoveredMessagesLocked(owner, selected)
}

// fetchZCodeSessionMessages asks the app-server for the session's messages.
// limit 0 means no limit.
func (a *Agent) fetchZCodeSessionMessages(ctx context.Context, sessionID string, limit int) ([]zcodeNativeMessage, error) {
	params := map[string]any{"sessionId": sessionID}
	if limit > 0 {
		params["limit"] = limit
	}
	raw, err := a.sendZCodeRequestContext(ctx, MethodSessionMessages, params, a.APITimeout())
	if err != nil {
		return nil, err
	}
	var resp struct {
		Messages []zcodeNativeMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode session/messages response: %w", err)
	}
	return resp.Messages, nil
}

// selectZCodeOwnerMessages picks the messages that belong to the owner.
//
// With an anchor: messages starting after anchorMessageID. Provisional
// accepted turn (no anchor): assistant messages with info.time.created >=
// acceptedAt. Resumed owner without anchor: returns nothing until a replay
// establishes the anchor.
//
// truncated is true when the window may have cut the owner's messages.
func selectZCodeOwnerMessages(owner *zcodeTurnState, messages []zcodeNativeMessage) (selected []zcodeNativeMessage, truncated bool) {
	if owner == nil {
		return nil, false
	}
	if owner.anchorMessageID != "" {
		// Messages after the anchor.
		found := false
		for _, msg := range messages {
			if found {
				selected = append(selected, msg)
			}
			if msg.Info.ID == owner.anchorMessageID {
				found = true
			}
		}
		if !found {
			return nil, len(messages) >= zcodeRecoveredMessageWindow
		}
		return selected, false
	}
	// No anchor.
	if owner.acceptedAt.IsZero() {
		// Resumed owner without anchor: emit nothing until anchored.
		return nil, false
	}
	// Provisional accepted turn: assistant messages created at or after acceptance.
	for _, msg := range messages {
		if msg.Info.Role == "assistant" && msg.Info.Time.Created >= float64(owner.acceptedAt.UnixMilli())/1000.0 {
			selected = append(selected, msg)
		}
	}
	return selected, false
}

// applyZCodeRecoveredMessagesLocked aggregates native parts into scopes and
// emits only the proven missing suffix. The caller holds dispatchMu.
//
// A non-prefix rewrite is a reconciliation conflict: it is surfaced as an
// error and nothing is appended.
func (a *Agent) applyZCodeRecoveredMessagesLocked(owner *zcodeTurnState, messages []zcodeNativeMessage) error {
	a.Mu.Lock()
	if owner.contentByScope == nil {
		owner.contentByScope = map[string]string{}
	}
	a.Mu.Unlock()

	for _, msg := range messages {
		textScope := zcodeTextScope(msg.Info.ID)
		reasoningScope := zcodeReasoningScope(msg.Info.ID)
		var textParts, reasoningParts []string
		seenPartIDs := map[string]struct{}{}
		for _, part := range msg.Parts {
			// Deduplicate by part ID.
			if part.ID != "" {
				if _, seen := seenPartIDs[part.ID]; seen {
					continue
				}
				seenPartIDs[part.ID] = struct{}{}
			}
			switch part.Type {
			case zcodeNativePartTypeText:
				textParts = append(textParts, part.Text)
			case zcodeNativePartTypeReasoning:
				reasoningParts = append(reasoningParts, part.Text)
			}
		}
		textAggregate := strings.Join(textParts, "")
		reasoningAggregate := strings.Join(reasoningParts, "")

		if err := a.emitZCodeRecoveredScopeLocked(owner, textScope, textAggregate); err != nil {
			return err
		}
		if err := a.emitZCodeRecoveredScopeLocked(owner, reasoningScope, reasoningAggregate); err != nil {
			return err
		}
	}
	return nil
}

// emitZCodeRecoveredScopeLocked compares one scope's native aggregate with the
// owner's retained content and emits only the proven missing suffix. A
// non-prefix rewrite is a conflict. The caller holds dispatchMu.
func (a *Agent) emitZCodeRecoveredScopeLocked(owner *zcodeTurnState, scopeID, aggregate string) error {
	if aggregate == "" {
		return nil
	}
	a.Mu.Lock()
	retained := owner.contentByScope[scopeID]
	a.Mu.Unlock()

	if retained == "" {
		// No delivered content: emit the full aggregate.
		if a.recoverEmitContent != nil {
			a.recoverEmitContent(owner, scopeID, aggregate)
		}
		a.Mu.Lock()
		owner.contentByScope[scopeID] = aggregate
		a.Mu.Unlock()
		return nil
	}

	// Retained content must be a prefix of the native aggregate. If it is not,
	// the native data rewrote history: a reconciliation conflict.
	if !strings.HasPrefix(aggregate, retained) {
		return fmt.Errorf("reconciliation conflict for scope %s: retained %d bytes are not a prefix of the %d-byte native aggregate",
			scopeID, len(retained), len(aggregate))
	}

	suffix := aggregate[len(retained):]
	if suffix == "" {
		return nil
	}
	if a.recoverEmitContent != nil {
		a.recoverEmitContent(owner, scopeID, suffix)
	}
	a.Mu.Lock()
	owner.contentByScope[scopeID] = aggregate
	a.Mu.Unlock()
	return nil
}

// --- settlement ---

// zcodeTurnSettlement is the per-path half of one turn settlement: the
// completions that close the turn's scopes and tools, and the row its end
// leaves behind. finishZCodeTurn builds the native one and recovery builds the
// evidence one; settleZCodeTurn runs the shared transition over either.
type zcodeTurnSettlement struct {
	// textCompletion closes the owner's buffered generation scopes and
	// callCompletion closes its incomplete tools. They differ only on a
	// completed turn, whose text finished while a lost call failed.
	textCompletion agent.MessageCompletion
	callCompletion agent.MessageCompletion
	// turnEnd persists the turn-end row from the calls the transition closed:
	// the native divider with its tool count, or the attributed prompt-boundary
	// evidence. nil states that the end carries no persistable content: the
	// transition settles the owner and drops it, touching no scope, tool, span
	// or row.
	turnEnd func(closed int)
	// backgroundEnd records a background turn's outcome beside the turn rather
	// than as the user's turn end: the native notification, or the recovery
	// evidence. Every settlement that persists a turn end sets it.
	backgroundEnd func()
}

// settleZCodeTurn runs the shared turn-settlement transition: reset retry and
// stop state, flush the owner's generation scopes, close the owner's
// incomplete tools, persist the turn-end row, reset the spans, update usage,
// release queued steers, and publish activity. Both native completion
// (finishZCodeTurn) and recovery settle through here.
//
// settle states the per-path half: which completions close the turn's scopes
// and tools, and which row its end leaves behind.
func (a *Agent) settleZCodeTurn(owner *zcodeTurnState, settle zcodeTurnSettlement) {
	if owner == nil {
		return
	}
	background := owner.background
	if !background {
		a.retryResolution.Reset()
	}
	a.Mu.Lock()
	if !background {
		a.stopCancelledRequest = false
	}
	// The turn reported its own end, so the stop's window has nothing left to
	// watch -- when it watches THIS turn. A background turn's end must leave the
	// user's stop window armed. The window is dropped in the SAME critical
	// section that settles the owner: a frame the read loop delivered between
	// the two re-armed the window, and it then ended a turn that had already
	// ended and wrote a second stop row for it.
	if a.stopWindow.turnID == owner.id {
		a.cancelStoppedZCodeTurnLocked()
	}
	if !background {
		// The approved exit runs inside the turn that asked for it. A stored plan-exit
		// mode that the turn end finds was never released, and no later event is its
		// exit.
		a.pendingExitMode = ""
	}
	owner.settled = true
	if a.foregroundTurnID == owner.id {
		a.foregroundTurnID = ""
	}
	a.refreshTurnActiveLocked()
	a.Mu.Unlock()
	// Deferred, for two reasons at once. It runs on EVERY path, including the
	// two early returns below -- a background turn, and a turn whose end carries
	// no persistable content -- and a turn that published no clear would latch
	// the agent busy for the life of the process. And it runs AFTER the turn-end
	// row, which hands this turn's tool-call count to the activity latch: the
	// clear is what produces the settle edge that spends the count, so
	// publishing it first would settle the agent with no count and ring the
	// completion sound for a turn that used no tool.
	defer a.PublishTurnActive()

	if settle.turnEnd == nil {
		// The end carries no persistable content: the owner settles and drops,
		// and no scope, tool, span or row is touched.
		a.Mu.Lock()
		a.dropZCodeTurnLocked(owner)
		a.Mu.Unlock()
		return
	}

	a.flushZCodeGenerationForTurn(owner, settle.textCompletion)
	closed := a.persistIncompleteZCodeToolsForTurn(owner, settle.callCompletion)

	if background {
		a.Mu.Lock()
		a.dropZCodeTurnLocked(owner)
		a.Mu.Unlock()
		settle.backgroundEnd()
		return
	}

	// Recover any tool call that never reached a final update (an aborted turn), so
	// the next turn's progress deltas are not measured against a dead stream.
	a.ResetCumulativeOutput()
	a.sink.ReportProgress(agent.ResetModelProgress())
	settle.turnEnd(closed)
	a.sink.ResetSpans()

	a.Mu.Lock()
	// Any batch summary that could still reference these arrived within the turn, so the
	// marks are spent. A record that holds nothing else goes with them, which is what
	// keeps the map's size capped at one turn's calls rather than at the session's.
	// Only this turn's own calls are touched: another turn's records stay as they are.
	for id := range owner.toolCallIDs {
		tc := a.toolCalls[id]
		if tc == nil {
			continue
		}
		tc.final = false
		if tc.name == "" && tc.input == nil {
			delete(a.toolCalls, id)
		}
	}
	a.dropZCodeTurnLocked(owner)
	a.Mu.Unlock()

	// The app-server's own reading is authoritative, and reading it takes an RPC --
	// which the read loop must stay free to deliver, so it cannot run inline here.
	go a.refreshZCodeUsageFromSession()
}

// zcodeRecoverySettlement settles a recovered turn: its scopes and tools close
// with completion, and the attributed prompt-boundary patch is the turn-end
// row. It NEVER fabricates a native turn.completed frame or divider; the
// evidence is the patch that proves the turn ended.
func (a *Agent) zcodeRecoverySettlement(owner *zcodeTurnState, completion agent.MessageCompletion, evidence []byte) zcodeTurnSettlement {
	record := func() { a.persistZCodeRecoveryEvidence(owner, evidence) }
	return zcodeTurnSettlement{
		textCompletion: completion,
		callCompletion: completion,
		turnEnd:        func(int) { record() },
		backgroundEnd:  record,
	}
}

// persistZCodeRecoveryEvidence stores the attributed prompt-boundary patch as
// the turn-end evidence.
func (a *Agent) persistZCodeRecoveryEvidence(owner *zcodeTurnState, evidence []byte) {
	if len(evidence) == 0 {
		return
	}
	if err := a.sink.PersistTurnEnd(agent.MessageContent{Original: evidence}, agent.SpanInfo{}); err != nil {
		slog.Error("zcode persist recovery evidence", "agent_id", a.AgentID(), "turn_id", owner.id, "error", err)
	}
}

// settleZCodeTurnFromRecovery settles a turn from the attributed boundary
// evidence.
func (a *Agent) settleZCodeTurnFromRecovery(owner *zcodeTurnState, state *zcodeTurnRecoveryState, boundary *zcodePromptBoundaryEvidence) {
	a.stopZCodeTurnWatcher(owner.id)
	completion := agent.MessageCompletionComplete
	if boundary.reason == zcodeStateReasonPromptFailed {
		completion = agent.MessageCompletionError
	}
	a.settleZCodeTurn(owner, a.zcodeRecoverySettlement(owner, completion, boundary.raw))
}

// settleZCodeTurnLost settles a turn whose watermarks froze past the limit.
// It surfaces the error path: the turn is lost and the user sees the outcome.
func (a *Agent) settleZCodeTurnLost(owner *zcodeTurnState, state *zcodeTurnRecoveryState) {
	a.stopZCodeTurnWatcher(owner.id)
	a.Mu.Lock()
	boundary := state.boundary
	a.Mu.Unlock()
	var evidence []byte
	completion := agent.MessageCompletionError
	if boundary != nil {
		evidence = boundary.raw
		if boundary.reason == zcodeStateReasonPromptCompleted {
			completion = agent.MessageCompletionComplete
		}
	}
	a.settleZCodeTurn(owner, a.zcodeRecoverySettlement(owner, completion, evidence))
}

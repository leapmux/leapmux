package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Per-turn reconciliation watcher.
//
// A turn can stall without a turn-end event: the app-server drops it, the
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

// zcodeProjectionState is the projection block a session/read reply and a
// state patch carry. It is the liveness watermark set for the recovery watcher.
type zcodeProjectionState struct {
	ContextUsed     int64  `json:"contextUsed"`
	ContextWindow   int64  `json:"contextWindow"`
	Status          string `json:"status"`
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
	ownerEventSeq   int64
}

// equals reports whether two watermarks are identical.
func (w zcodeTurnRecoveryWatermark) equals(other zcodeTurnRecoveryWatermark) bool {
	return w == other
}

// zcodeTurnRecoveryState belongs to one stable turn owner. The watcher retains
// that owner until cancellation, even after the owner leaves the native-ID index.
type zcodeTurnRecoveryState struct {
	sessionID string
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
	// wake schedules a received native end without waiting for the quiet interval.
	wake chan struct{}
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

// zcodeTurnNeedsRecoveryLocked includes locally stopped turns with deferred output.
// The caller holds a.Mu.
func zcodeTurnNeedsRecoveryLocked(owner *zcodeTurnState) bool {
	return owner != nil && !owner.finalized && (!owner.settled || owner.pendingNative != nil || len(owner.deferredResponses) > 0)
}

// startZCodeTurnWatcherLocked starts the per-turn recovery watcher while
// a.Mu is already held. The watcher polls session/read after each quiet
// interval and reconciles the turn's state.
func (a *Agent) startZCodeTurnWatcherLocked(owner *zcodeTurnState) {
	if !zcodeTurnNeedsRecoveryLocked(owner) {
		return
	}
	if owner.recovery != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.Context())
	state := &zcodeTurnRecoveryState{
		cancel:    cancel,
		sessionID: a.sessionID,
		wake:      make(chan struct{}, 1),
	}
	owner.recovery = state
	go a.runZCodeTurnWatcher(ctx, owner, state)
}

func (a *Agent) stopZCodeTurnWatcherLocked(owner *zcodeTurnState) {
	if owner == nil {
		return
	}
	state := owner.recovery
	owner.recovery = nil
	if state != nil && state.cancel != nil {
		state.cancel()
	}
}

// stopAllZCodeTurnWatchers cancels every recovery watcher. Teardown and
// ClearContext call it.
func (a *Agent) stopAllZCodeTurnWatchers() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, owner := range a.turns {
		a.stopZCodeTurnWatcherLocked(owner)
	}
}

// runZCodeTurnWatcher is the watcher loop. It waits the quiet interval on the
// agent's clock and runs one recovery cycle. Final settlement ends the loop.
// Cancellation and process exit also end it.
func (a *Agent) runZCodeTurnWatcher(ctx context.Context, owner *zcodeTurnState, state *zcodeTurnRecoveryState) {
	defer a.finishZCodeTurnWatcher(owner, state)
	for {
		timer := a.Clock().NewTimer(zcodeTurnRecoveryQuietInterval, zcodeTurnRecoveryTimerTag)
		select {
		case <-timer.C:
		case <-state.wake:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.ProcessDone():
			timer.Stop()
			return
		}
		settled, err := a.checkZCodeTurnRecovery(ctx, owner, state)
		if err != nil {
			slog.Warn("zcode turn recovery check failed", "agent_id", a.AgentID(), "turn_generation", owner.generation, "error", err)
		}
		if settled {
			return
		}
	}
}

// finishZCodeTurnWatcher hands off a native end that arrived while the old
// watcher decided to exit. Detachment and rearming share the owner mutex.
func (a *Agent) finishZCodeTurnWatcher(owner *zcodeTurnState, state *zcodeTurnRecoveryState) {
	state.cancel()
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if owner.recovery != state {
		return
	}
	owner.recovery = nil
	if owner.pendingNative == nil || owner.finalized || a.turns[owner.id] != owner ||
		state.sessionID != a.sessionID || a.sessionChanging || a.StoppedLocked() || a.Context().Err() != nil {
		return
	}
	select {
	case <-a.ProcessDone():
		return
	default:
	}
	a.startZCodeTurnWatcherLocked(owner)
	if next := owner.recovery; next != nil {
		select {
		case next.wake <- struct{}{}:
		default:
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
	identity := a.captureZCodeObservationIdentityLocked()
	current := a.currentZCodeRecoveryLocked(owner, state)
	a.Mu.Unlock()
	if !current {
		return true, nil
	}
	if a.zcodeNativeSettlementPending(owner) {
		return a.checkZCodeNativeSettlement(ctx, owner, state)
	}
	snap, err := a.readZCodeSession(ctx, identity)
	a.Mu.Lock()
	current = a.currentZCodeRecoveryLocked(owner, state)
	a.Mu.Unlock()
	if !current || ctx.Err() != nil {
		return true, nil
	}
	if a.zcodeNativeSettlementPending(owner) {
		return a.checkZCodeNativeSettlement(ctx, owner, state)
	}
	if err != nil {
		if zcodeIsMethodNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return a.applyZCodeRecoverySnapshotContext(ctx, owner, state, snap)
}

// zcodeSessionReadSnapshot is the subset of a session/read reply the recovery
// watcher reads.
type zcodeSessionReadSnapshot struct {
	identity *zcodeSessionObservationIdentity
	Session  *struct {
		SessionID string `json:"sessionId"`
	} `json:"session"`
	Runtime    *zcodeRuntimeState     `json:"runtime"`
	Projection *zcodeProjectionState  `json:"projection"`
	Settings   *zcodeSettingsSnapshot `json:"settings"`
}

// applyZCodeRecoverySnapshot folds one session/read reply into the watcher's
// state and decides whether the turn settles. It returns true when settled.
func (a *Agent) applyZCodeRecoverySnapshot(owner *zcodeTurnState, state *zcodeTurnRecoveryState, snap zcodeSessionReadSnapshot) (bool, error) {
	return a.applyZCodeRecoverySnapshotContext(a.Context(), owner, state, snap)
}

func (a *Agent) applyZCodeRecoverySnapshotContext(ctx context.Context, owner *zcodeTurnState, state *zcodeTurnRecoveryState, snap zcodeSessionReadSnapshot) (bool, error) {

	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	now := a.Clock().Now()

	a.Mu.Lock()
	if !a.currentZCodeRecoveryLocked(owner, state) || ctx.Err() != nil {
		a.Mu.Unlock()
		return true, nil
	}
	if owner.pendingNative != nil {
		a.Mu.Unlock()
		a.unlockZCodeDispatch()
		settled, err := a.checkZCodeNativeSettlement(ctx, owner, state)
		a.dispatchMu.Lock()
		return settled, err
	}
	if !a.zcodeReadObservationCurrentLocked(snap.identity, snap.Runtime) {
		a.Mu.Unlock()
		return false, nil
	}
	// A runtime turn may precede this queued input. Only the persisted user
	// anchor links a missing start event to the accepted input.
	if (owner.id == "" || strings.HasPrefix(owner.id, "#turn-")) && owner.inputID != "" && snap.Projection != nil && snap.Projection.CurrentTurnID != "" && snap.Projection.TurnCount > 0 {
		sessionID, inputID, previousSeq := a.sessionID, owner.inputID, owner.lastEventSeq
		a.Mu.Unlock()
		a.unlockZCodeDispatch()
		messages, readErr := func() ([]zcodeNativeMessage, error) {
			defer a.dispatchMu.Lock()
			return a.fetchZCodeSessionMessages(ctx, sessionID, 0)
		}()
		a.Mu.Lock()
		if !a.currentZCodeRecoveryLocked(owner, state) || owner.lastEventSeq != previousSeq {
			current := a.currentZCodeRecoveryLocked(owner, state)
			a.Mu.Unlock()
			return !current, nil
		}
		if ctx.Err() != nil {
			a.Mu.Unlock()
			return true, nil
		}
		if !a.zcodeReadObservationCurrentLocked(snap.identity, snap.Runtime) {
			a.Mu.Unlock()
			return false, nil
		}
		a.Mu.Unlock()
		gap := a.zcodeReplayHasGapLocked()
		a.Mu.Lock()
		if gap {
			a.Mu.Unlock()
			return false, nil
		}
		if readErr != nil {
			a.Mu.Unlock()
			return false, readErr
		}
		for _, message := range messages {
			if message.Info.Role == "user" && message.Info.ID != "" && message.Info.Anchor.SourceCommandID == inputID && message.Info.Anchor.TurnID == snap.Projection.CurrentTurnID {
				if a.adoptZCodeTurnIDLocked(owner, message.Info.Anchor.TurnID) {
					owner.anchorMessageID = message.Info.ID
				}
				break
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
		ownerEventSeq:   owner.lastEventSeq,
	}
	if snap.Projection == nil || projection.CurrentTurnID != owner.id {
		watermark = state.lastWatermark
		watermark.ownerEventSeq = owner.lastEventSeq
	}
	advanced := !watermark.equals(state.lastWatermark)
	if watermark.ownerEventSeq != state.lastWatermark.ownerEventSeq {
		state.idleSnapshots = 0
	}
	state.lastWatermark = watermark
	if advanced {
		state.frozenSince = now
	} else if state.frozenSince.IsZero() {
		state.frozenSince = now
	}

	// Only a complete native read that identifies this exact turn can confirm idle.
	boundary := state.boundary
	confirmed := snap.Runtime != nil && snap.Projection != nil &&
		snap.Runtime.stateRevisionKnown && snap.Runtime.eventSeqKnown &&
		snap.Runtime.StateRevision >= 0 && snap.Runtime.EventSeq >= 0 &&
		snap.Runtime.ActiveTurnID == "" && projection.CurrentTurnID == owner.id &&
		(projection.Status == "idle" || projection.Status == "error") &&
		snap.Runtime.StateRevision >= owner.admissionRevision && snap.Runtime.EventSeq >= owner.lastEventSeq &&
		(boundary == nil || snap.Runtime.StateRevision >= boundary.revision)
	if confirmed {
		state.idleSnapshots++
	} else {
		state.idleSnapshots = 0
	}

	provisional := (owner.id == "" || strings.HasPrefix(owner.id, "#turn-"))
	waitingForNative := provisional && snap.Runtime != nil && snap.Runtime.activeTurnIDKnown && snap.Runtime.ActiveTurnID != "" && snap.Runtime.ActiveTurnID != owner.id
	if waitingForNative {
		state.frozenSince = time.Time{}
	}
	hasActiveTool := a.hasActiveZCodeToolLocked(owner)
	if hasActiveTool {
		state.frozenSince = now
	}
	a.Mu.Unlock()

	// A frozen watermark past the freeze limit, with no active tool, surfaces
	// the error path. A tool running or a watermark advance resets the clock.
	if !waitingForNative && !advanced && !hasActiveTool && !state.frozenSince.IsZero() {
		if now.Sub(state.frozenSince) >= zcodeTurnRecoveryFreezeLimit {
			return a.settleZCodeTurnLost(owner, state), nil
		}
	}

	// Local settlement requires an attributed boundary AND two idle snapshots
	// AND no active tool. Two idle snapshots alone establish only inactivity.
	if boundary != nil && state.idleSnapshots >= 2 && !hasActiveTool {
		confirmedBoundary := *boundary
		confirmedBoundary.reason = contracts.ZCodeStateReasonPromptCompleted
		if projection.Status == "error" {
			confirmedBoundary.reason = contracts.ZCodeStateReasonPromptFailed
		}
		a.Mu.Lock()
		sessionID := a.sessionID
		selection := &zcodeTurnState{id: owner.id, anchorMessageID: owner.anchorMessageID, acceptedAt: owner.acceptedAt}
		observedOwnerSeq := owner.lastEventSeq
		a.Mu.Unlock()
		// The read loop must deliver this RPC's reply, so no dispatch lock crosses it.
		a.unlockZCodeDispatch()
		messages, err := func() ([]zcodeNativeMessage, error) {
			defer a.dispatchMu.Lock()
			return a.fetchZCodeFinalMessages(ctx, sessionID, selection)
		}()
		a.Mu.Lock()
		current := a.currentZCodeRecoveryLocked(owner, state) && a.sessionID == sessionID && ctx.Err() == nil
		unchanged := owner.lastEventSeq == observedOwnerSeq && a.zcodeReadObservationCurrentLocked(snap.identity, snap.Runtime) && owner.pendingNative == nil
		a.Mu.Unlock()
		if !current {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !unchanged || a.zcodeReplayHasGapLocked() {
			return false, nil
		}
		if err := a.applyZCodeRecoveredMessagesLocked(owner, messages); err != nil {
			return false, err
		}
		return a.settleZCodeTurnFromRecovery(owner, state, &confirmedBoundary), nil
	}

	return false, nil
}

func (a *Agent) currentZCodeRecoveryLocked(owner *zcodeTurnState, state *zcodeTurnRecoveryState) bool {
	return zcodeTurnNeedsRecoveryLocked(owner) && state != nil && !a.StoppedLocked() &&
		a.sessionID != "" && state.sessionID == a.sessionID &&
		a.turns[owner.id] == owner && owner.recovery == state
}

// hasActiveZCodeToolLocked reports whether any tool call of the owner is still
// open. The caller holds a.Mu.
func (a *Agent) hasActiveZCodeToolLocked(owner *zcodeTurnState) bool {
	for id := range owner.toolInvocationIDs {
		tc := a.toolInvocations[id]
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
//
// An ID-less boundary is usable only for one unambiguous unsettled owner. A
// background boundary is not evidence for the foreground watcher. Ambiguous
// evidence stays unattributed.
func (a *Agent) attributeZCodePromptBoundaryLocked(owner *zcodeTurnState, reason string, revision int64, projection zcodeProjectionState, raw []byte) *zcodePromptBoundaryEvidence {
	if !zcodeTurnNeedsRecoveryLocked(owner) {
		return nil
	}
	// The boundary must be at least as new as the admission.
	if revision > 0 && owner.admissionRevision > 0 && revision < owner.admissionRevision {
		return nil
	}
	// When both sides identify a turn, they must agree.
	if projection.CurrentTurnID != "" && owner.id != "" && !strings.HasPrefix(owner.id, "#turn-") {
		if projection.CurrentTurnID != owner.id {
			return nil
		}
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

// attributeZCodePromptBoundaryForSoleOwnerLocked applies an ID-less boundary
// when exactly one owner still needs recovery. The caller holds a.Mu.
func (a *Agent) attributeZCodePromptBoundaryForSoleOwnerLocked(reason string, revision int64, projection zcodeProjectionState, raw []byte) *zcodePromptBoundaryEvidence {
	var sole *zcodeTurnState
	for _, turn := range a.turns {
		if !zcodeTurnNeedsRecoveryLocked(turn) {
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
	state := owner.recovery
	if state != nil && state.boundary == nil {
		state.boundary = evidence
	}
	a.Mu.Unlock()
}

// handleZCodePromptBoundary is the entry point for a prompt_completed or
// prompt_failed state patch. It attributes the boundary to a turn owner and
// records it as recovery evidence.
func (a *Agent) handleZCodePromptBoundary(notif zcodeStatePatch, body zcodeStatePatchBody) {
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	current := !a.StoppedLocked() && (notif.SessionID == "" || notif.SessionID == a.sessionID)
	a.Mu.Unlock()
	if !current {
		return
	}

	if notif.Reason != contracts.ZCodeStateReasonPromptCompleted && notif.Reason != contracts.ZCodeStateReasonPromptFailed {
		return
	}
	var projection zcodeProjectionState
	if body.Projection != nil {
		projection = *body.Projection
	}
	params := notif.raw
	if len(params) == 0 {
		params, _ = json.Marshal(notif)
	}
	raw := zcodeEventEnvelope{Type: contracts.ZCodeMethodStateUpdated, Payload: params}.persistBytes()

	a.Mu.Lock()
	// Attribute only when evidence identifies an owner that still needs recovery.
	// An ID-ful boundary (projection identifies the turn) goes to that owner.
	if projection.CurrentTurnID != "" {
		if owner := a.turns[projection.CurrentTurnID]; owner != nil {
			evidence := a.attributeZCodePromptBoundaryLocked(owner, notif.Reason, notif.Revision, projection, raw)
			a.Mu.Unlock()
			a.recordZCodePromptBoundary(owner, evidence)
			return
		}
		a.Mu.Unlock()
		return
	}
	// ID-less boundary: try the sole owner that still needs recovery.
	sole := a.attributeZCodePromptBoundaryForSoleOwnerLocked(notif.Reason, notif.Revision, projection, raw)
	var soleOwner *zcodeTurnState
	if sole != nil {
		for _, turn := range a.turns {
			if zcodeTurnNeedsRecoveryLocked(turn) {
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
	ID       string                 `json:"id"`
	Role     string                 `json:"role"`
	Time     zcodeNativeMessageTime `json:"time"`
	ParentID string                 `json:"parentID"`
	Anchor   struct {
		TurnID          string `json:"turnId"`
		SourceCommandID string `json:"sourceCommandId"`
	} `json:"anchor"`
	Semantics struct {
		Kind string `json:"kind"`
	} `json:"semantics"`
	Finish string `json:"finish"`
}

// zcodeNativeMessageTime carries the message's creation timestamp.
type zcodeNativeMessageTime struct {
	Created float64 `json:"created"`
}

// zcodeNativePart is one part of a native message. Native part order determines
// text order. Part IDs deduplicate repeated parts.
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

// fetchZCodeFinalMessages fetches and selects the owner's native assistant messages.
// The caller revalidates ownership before comparing and emitting missing suffixes.
//
// Request the newest 200 messages. Retry without a limit when the required
// user-message anchor is absent or an unanchored window is full.
// Reject a full read that still lacks the required user-message anchor.
// Native part order determines text order. Part IDs deduplicate repeated parts.
func (a *Agent) fetchZCodeFinalMessages(ctx context.Context, sessionID string, owner *zcodeTurnState) ([]zcodeNativeMessage, error) {
	if owner == nil || sessionID == "" {
		return nil, fmt.Errorf("no native owner for message reconciliation")
	}
	// Phase 1: fetch the limited window (outside the lock).
	messages, err := a.fetchZCodeSessionMessages(ctx, sessionID, zcodeRecoveredMessageWindow)
	if err != nil {
		return nil, fmt.Errorf("session/messages: %w", err)
	}

	// Phase 2: select owner messages and check for truncation.
	selected, truncated := selectZCodeOwnerMessages(owner, messages, false)

	// Phase 3: if truncated and the anchor is absent, retry unlimited.
	if truncated {
		allMessages, err := a.fetchZCodeSessionMessages(ctx, sessionID, 0)
		if err != nil {
			return nil, fmt.Errorf("session/messages (full): %w", err)
		}
		selected, truncated = selectZCodeOwnerMessages(owner, allMessages, true)
		if truncated {
			return nil, fmt.Errorf("session/messages lacks the anchor for turn %s after a full read", owner.id)
		}
	}

	return selected, nil
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
// acceptedAt. An exact native turn anchor also proves ownership without a local
// acceptance time, including after resume.
//
// truncated is true when the window may have cut the owner's messages.
func selectZCodeOwnerMessages(owner *zcodeTurnState, messages []zcodeNativeMessage, complete bool) (selected []zcodeNativeMessage, truncated bool) {
	if owner == nil {
		return nil, false
	}
	if owner.anchorMessageID != "" {
		// Messages after the anchor.
		found := false
		for _, msg := range messages {
			if found && zcodeMessageBelongsToOwner(owner, msg) {
				selected = append(selected, msg)
			}
			if msg.Info.ID == owner.anchorMessageID {
				found = true
			}
		}
		if !found {
			return nil, true
		}
		return selected, false
	}
	// An exact native turn anchor proves ownership independently of local timing.
	// Timestamp fallback requires a known local acceptance.
	for _, msg := range messages {
		if !zcodeMessageBelongsToOwner(owner, msg) {
			continue
		}
		if msg.Info.Anchor.TurnID != "" || (!owner.acceptedAt.IsZero() && msg.Info.Time.Created >= float64(owner.acceptedAt.UnixMilli())) {
			selected = append(selected, msg)
		}
	}
	return selected, !complete && len(messages) >= zcodeRecoveredMessageWindow
}

func zcodeMessageBelongsToOwner(owner *zcodeTurnState, message zcodeNativeMessage) bool {
	info := message.Info
	if info.Role != "assistant" || info.Finish == "stream_recovery_discarded" ||
		(info.Semantics.Kind != "" && info.Semantics.Kind != "assistant_response") {
		return false
	}
	if info.Anchor.TurnID != "" {
		return owner.id != "" && !strings.HasPrefix(owner.id, "#turn-") && info.Anchor.TurnID == owner.id
	}
	if owner.anchorMessageID != "" {
		return info.ParentID == owner.anchorMessageID
	}
	return info.ParentID == "" && !owner.acceptedAt.IsZero()
}

// zcodeRecoveredScope records a canonical scope's kind and unsubmitted suffix.
type zcodeRecoveredScope struct {
	scopeID string
	kind    agent.AssembledMessageKind
	suffix  string
}

// zcodeRecoveredMessage holds one aggregation shared by matching and scope validation.
type zcodeRecoveredMessage struct {
	id        string
	text      string
	reasoning string
	kinds     []agent.AssembledMessageKind
}

// applyZCodeRecoveredMessagesLocked validates the complete native candidate, then
// rebuilds open output in native order. Pending immutable writes keep their position.
// The caller holds dispatchMu. A conflict publishes no recovered output.
func (a *Agent) applyZCodeRecoveredMessagesLocked(owner *zcodeTurnState, messages []zcodeNativeMessage) error {
	a.Mu.Lock()
	retained := make(map[string]*zcodeScopeContent, len(owner.contentByScope))
	for scope, content := range owner.contentByScope {
		if content != nil {
			if content.response != nil && scope == content.response.fallbackScopeID {
				continue
			}
			retained[scope] = &zcodeScopeContent{observed: append([]byte(nil), content.observed...), submitted: content.submitted, response: content.response}
		}
	}
	responses := append([]zcodeModelResponseText(nil), owner.acceptedResponses...)
	deferred := append([]*zcodeDeferredModelResponse(nil), owner.deferredResponses...)
	a.Mu.Unlock()
	nativeIDs := make(map[string]bool, len(messages))
	for _, message := range messages {
		if message.Info.ID == "" {
			return fmt.Errorf("reconciliation conflict: native message has no ID")
		}
		if nativeIDs[message.Info.ID] {
			return fmt.Errorf("reconciliation conflict: duplicate native message %q", message.Info.ID)
		}
		nativeIDs[message.Info.ID] = true
	}
	identified := make(map[string]bool)
	for _, response := range responses {
		if response.messageID != "" {
			if !nativeIDs[response.messageID] {
				return fmt.Errorf("reconciliation conflict: native messages lack accepted response %q", response.messageID)
			}
			identified[response.messageID] = true
		}
	}
	aggregates := make([]zcodeRecoveredMessage, len(messages))
	for index, message := range messages {
		aggregates[index] = zcodeNativeMessageText(message)
	}
	matches, err := zcodeMatchRecoveredResponses(responses, aggregates, func(message zcodeRecoveredMessage) bool {
		content := retained[zcodeTextScope(message.id)]
		return !identified[message.id] && (content == nil || content.submitted == 0)
	})
	if err != nil {
		return err
	}
	accepted := make(map[string]bool, len(matches))
	for responseIndex, index := range matches {
		message := aggregates[index]
		scope := zcodeTextScope(message.id)
		if err := zcodeValidateRecoveredPrefix(scope, retained[scope].text(), message.text); err != nil {
			return err
		}
		retained[scope] = &zcodeScopeContent{observed: []byte(message.text), submitted: len(message.text)}
		accepted[message.id] = true
		responses[responseIndex].messageID = message.id
	}
	pending := make([]zcodeModelResponseText, len(deferred))
	for index, response := range deferred {
		pending[index] = zcodeModelResponseText{messageID: response.nativeMessageID, text: response.text}
	}
	deferredMatches, err := zcodeMatchRecoveredResponses(pending, aggregates, func(message zcodeRecoveredMessage) bool { return !accepted[message.id] })
	if err != nil {
		return err
	}
	for index, messageIndex := range deferredMatches {
		scope := zcodeTextScope(aggregates[messageIndex].id)
		if retained[scope] == nil {
			retained[scope] = &zcodeScopeContent{}
		}
		retained[scope].response = deferred[index]
	}
	var scopes []zcodeRecoveredScope
	seenScopes := make(map[string]bool)
	missingEarlier := false
	for _, message := range aggregates {
		for _, candidate := range []struct {
			scope string
			text  string
		}{
			{zcodeTextScope(message.id), message.text}, {zcodeReasoningScope(message.id), message.reasoning},
		} {
			seenScopes[candidate.scope] = true
			content := retained[candidate.scope]
			if err := zcodeValidateRecoveredPrefix(candidate.scope, content.text(), candidate.text); err != nil {
				return err
			}
			if content == nil {
				content = &zcodeScopeContent{}
			}
			content.observed = []byte(candidate.text)
			retained[candidate.scope] = content
		}
		for _, kind := range message.kinds {
			scope := zcodeTextScope(message.id)
			aggregate := message.text
			if kind == agent.AssembledMessageKindReasoning {
				scope = zcodeReasoningScope(message.id)
				aggregate = message.reasoning
			}
			content := retained[scope]
			if missingEarlier && content.submitted > 0 {
				return fmt.Errorf("reconciliation conflict: missing earlier output cannot precede accepted scope %s in transcript order", scope)
			}
			suffix := aggregate[content.submitted:]
			if suffix != "" {
				missingEarlier = true
			}
			scopes = append(scopes, zcodeRecoveredScope{scopeID: scope, kind: kind, suffix: suffix})
		}
	}
	for scope, content := range retained {
		if !seenScopes[scope] && len(content.observed) != 0 {
			return fmt.Errorf("reconciliation conflict: native messages lack retained scope %s", scope)
		}
	}
	// Validate every scope before replacing open progress or publishing recovered output.
	open := a.zcodeTurnBufferedScopes(owner, nil)
	a.Mu.Lock()
	owner.contentByScope = retained
	owner.acceptedResponses = responses
	for index, messageIndex := range deferredMatches {
		deferred[index].nativeMessageID = aggregates[messageIndex].id
	}
	a.Mu.Unlock()
	providerkit.DiscardGenerationScopes(&a.generationBuffer, a.zcodeProgressSinkForTurn(owner), open)
	for _, scope := range scopes {
		content := retained[scope.scopeID]
		if scope.suffix == "" && content.response != nil {
			if !a.persistZCodeAssistantResponse(owner, scope.scopeID, zcodeModelResponseText{messageID: content.response.nativeMessageID, text: content.response.text}, content.response.content) {
				return fmt.Errorf("reconciliation could not retain native response for scope %s", scope.scopeID)
			}
		}
		a.appendZCodeRecoveredScope(owner, scope)
	}
	return nil
}

// zcodeMatchRecoveredResponses requires one unique ordered assignment of response rows.
// Exact identities and independently submitted scopes constrain anonymous text matches.
func zcodeMatchRecoveredResponses(responses []zcodeModelResponseText, messages []zcodeRecoveredMessage, anonymousAllowed func(zcodeRecoveredMessage) bool) ([]int, error) {
	matches := make([]int, len(responses))
	eligible := func(response zcodeModelResponseText, message zcodeRecoveredMessage) bool {
		if response.messageID != "" {
			return response.messageID == message.id && response.text == message.text
		}
		return anonymousAllowed(message) && response.text == message.text
	}
	next := 0
	for index, response := range responses {
		for next < len(messages) && !eligible(response, messages[next]) {
			next++
		}
		if next == len(messages) {
			return nil, fmt.Errorf("reconciliation conflict: native messages do not contain accepted response %q", response.messageID)
		}
		matches[index] = next
		next++
	}
	next = len(messages) - 1
	for index := len(responses) - 1; index >= 0; index-- {
		for next >= 0 && !eligible(responses[index], messages[next]) {
			next--
		}
		if next != matches[index] {
			return nil, fmt.Errorf("reconciliation conflict: response %q has ambiguous native identity", responses[index].messageID)
		}
		next--
	}
	return matches, nil
}

// zcodeNativeMessageText aggregates each native part once and preserves the first occurrence of each kind.
func zcodeNativeMessageText(message zcodeNativeMessage) zcodeRecoveredMessage {
	var text, reasoning strings.Builder
	result := zcodeRecoveredMessage{id: message.Info.ID}
	seenParts := make(map[string]bool)
	seenKinds := make(map[agent.AssembledMessageKind]bool)
	for _, part := range message.Parts {
		if part.ID != "" {
			if seenParts[part.ID] {
				continue
			}
			seenParts[part.ID] = true
		}
		var kind agent.AssembledMessageKind
		switch part.Type {
		case zcodeNativePartTypeText:
			kind = agent.AssembledMessageKindText
			text.WriteString(part.Text)
		case zcodeNativePartTypeReasoning:
			kind = agent.AssembledMessageKindReasoning
			reasoning.WriteString(part.Text)
		default:
			continue
		}
		if !seenKinds[kind] {
			result.kinds = append(result.kinds, kind)
			seenKinds[kind] = true
		}
	}
	result.text, result.reasoning = text.String(), reasoning.String()
	return result
}

func zcodeValidateRecoveredPrefix(scopeID, retained, aggregate string) error {
	if !strings.HasPrefix(aggregate, retained) {
		return fmt.Errorf("reconciliation conflict for scope %s: retained %d bytes are not a prefix of the %d-byte native aggregate", scopeID, len(retained), len(aggregate))
	}
	return nil
}

func (a *Agent) appendZCodeRecoveredScope(owner *zcodeTurnState, scope zcodeRecoveredScope) {
	if scope.suffix == "" {
		return
	}
	a.generationBuffer.Append(scope.scopeID, scope.kind, scope.suffix, providerkit.JoinVerbatim)
	a.zcodeProgressSinkForTurn(owner).ReportProgress(agent.ModelTextProgress(scope.scopeID, scope.suffix))
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
	// persistEnd records this owner's outcome after its scopes and tools close.
	// Nil settles identity without persisting a row or touching buffered work.
	persistEnd func(closed int, publishTurnEnd bool)
	// afterForeground retains retry ownership until the actual settlement claim.
	afterForeground func()
}

// settleZCodeTurn retains the owner's output before it finalizes the turn.
// It then resets retry and stop state, closes incomplete tools, and records the
// turn end before it releases activity. Native completion and recovery share it.
//
// settle states the per-path half: which completions close the turn's scopes
// and tools, and which row its end leaves behind.
func (a *Agent) settleZCodeTurn(owner *zcodeTurnState, settle zcodeTurnSettlement) bool {
	if owner == nil {
		return false
	}
	background := owner.background
	a.Mu.Lock()
	if owner.finalized || a.turns[owner.id] != owner {
		a.Mu.Unlock()
		return false
	}
	a.Mu.Unlock()
	if settle.persistEnd != nil {
		if !a.prepareZCodeDeferredResponses(owner) || !a.flushZCodeGenerationForTurn(owner, settle.textCompletion) {
			return false
		}
	}
	a.Mu.Lock()
	if owner.finalized || a.turns[owner.id] != owner {
		a.Mu.Unlock()
		return false
	}
	foreground := !background && a.foregroundTurnID == owner.id && !owner.settled
	publishTurnEnd := foreground
	for _, other := range a.turns {
		if other != owner && (!other.settled || a.hasActiveZCodeToolLocked(other)) {
			publishTurnEnd = false
			break
		}
	}
	owner.finalized = true
	owner.settlementWriting = true
	a.stopZCodeTurnWatcherLocked(owner)
	if foreground {
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
	if foreground {
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
	defer func() {
		a.Mu.Lock()
		owner.settlementWriting = false
		// A finalized owner retained for background children needs only identity.
		clear(owner.contentByScope)
		owner.acceptedResponses = nil
		owner.deferredResponses = nil
		owner.outputSource = agent.MessageContent{}
		owner.currentAssistantMessageID = ""
		owner.pendingNative = nil
		owner.canonicalRequired = false
		a.dropZCodeTurnLocked(owner)
		a.Mu.Unlock()
		if background {
			a.finalizeZCodeToolsWhenIdle()
		}
		if foreground && settle.afterForeground != nil {
			settle.afterForeground()
		}
		a.PublishTurnActive()
	}()
	if foreground {
		a.retryResolution.Reset()
	}
	// The finalizer publishes inactivity after the row and retry bookkeeping.
	// That publication can release queued input into the next foreground.

	if settle.persistEnd == nil {
		// The end carries no persistable content: the owner settles and drops,
		// and no scope, tool, span or row is touched.
		a.Mu.Lock()
		a.dropZCodeTurnLocked(owner)
		a.Mu.Unlock()
		return true
	}

	closed := a.persistIncompleteZCodeToolsForTurn(owner, settle.callCompletion)

	a.resetZCodeTurnProgress(owner)
	settle.persistEnd(closed, publishTurnEnd)
	a.resetZCodeSpansWhenIdle()

	// The app-server's own reading is authoritative, and reading it takes an RPC --
	// which the read loop must stay free to deliver, so it cannot run inline here.
	a.scheduleZCodeUsageRefresh()
	return true
}

// resetZCodeSpansWhenIdle retains span state until every overlapping owner ends.
// A locally stopped owner can still hold a running tool that needs its span.
func (a *Agent) resetZCodeSpansWhenIdle() {
	a.Mu.Lock()
	for _, owner := range a.turns {
		if !owner.settled || a.hasActiveZCodeToolLocked(owner) {
			a.Mu.Unlock()
			return
		}
	}
	a.Mu.Unlock()
	a.sink.ResetSpans()
}

// zcodeRecoverySettlement settles a recovered turn: its scopes and tools close
// with completion, and the attributed prompt-boundary patch is the turn-end
// row. It NEVER fabricates a native turn.completed frame or divider; the
// evidence is the patch that proves the turn ended.
func (a *Agent) zcodeRecoverySettlement(owner *zcodeTurnState, completion agent.MessageCompletion, evidence []byte) zcodeTurnSettlement {
	record := func(publish bool) { a.persistZCodeRecoveryEvidenceForOwner(owner, completion, evidence, publish) }
	return zcodeTurnSettlement{
		textCompletion: completion,
		callCompletion: completion,
		persistEnd:     func(_ int, publish bool) { record(publish) },
	}
}

// persistZCodeRecoveryEvidence stores the attributed prompt-boundary patch as
// the turn-end evidence.
func (a *Agent) persistZCodeRecoveryEvidence(owner *zcodeTurnState, completion agent.MessageCompletion, evidence []byte) {
	a.persistZCodeRecoveryEvidenceForOwner(owner, completion, evidence, true)
}

func (a *Agent) persistZCodeRecoveryEvidenceForOwner(owner *zcodeTurnState, completion agent.MessageCompletion, evidence []byte, publish bool) {
	if len(evidence) == 0 {
		return
	}
	content := agent.MessageContent{Original: evidence, Completion: completion}
	if owner.background {
		a.persistZCodeBackgroundTurnEnd(owner, evidence, completion)
		return
	}
	var err error
	if publish {
		err = a.zcodeProgressSinkForTurn(owner).PersistTurnEnd(content, agent.SpanInfo{})
	} else {
		err = a.zcodeProgressSinkForTurn(owner).PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	}
	if err != nil {
		slog.Error("zcode persist recovery evidence", "agent_id", a.AgentID(), "turn_id", owner.id, "error", err)
	}
}

// settleZCodeTurnFromRecovery settles a turn from the attributed boundary
// evidence.
func (a *Agent) settleZCodeTurnFromRecovery(owner *zcodeTurnState, state *zcodeTurnRecoveryState, boundary *zcodePromptBoundaryEvidence) bool {
	completion := agent.MessageCompletionComplete
	if boundary.reason == contracts.ZCodeStateReasonPromptFailed {
		completion = agent.MessageCompletionError
	}
	return a.settleZCodeTurn(owner, a.zcodeRecoverySettlement(owner, completion, boundary.raw))
}

// settleZCodeTurnLost settles a turn whose watermarks froze past the limit.
// It surfaces the error path: the turn is lost and the user sees the outcome.
func (a *Agent) settleZCodeTurnLost(owner *zcodeTurnState, state *zcodeTurnRecoveryState) bool {
	return a.settleZCodeTurn(owner, zcodeTurnSettlement{
		textCompletion: agent.MessageCompletionError,
		callCompletion: agent.MessageCompletionError,
		persistEnd: func(int, bool) {
			a.persistZCodeRecoveryError(owner, "ZCode stopped reporting progress, and its native state could not confirm that this turn ended.")
		},
	})
}

func (a *Agent) persistZCodeRecoveryError(owner *zcodeTurnState, message string) {
	encoded, err := json.Marshal(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldError: message})
	if err == nil {
		_, err = a.zcodeProgressSinkForTurn(owner).PersistWorkerNotification(agent.MessageContent{Original: encoded})
	}
	if err != nil {
		slog.Error("zcode persist output restoration failure", "agent_id", a.AgentID(), "error", err)
	}
}

// zcodePendingNativeSettlement is immutable after the first native end arrives.
// Its source retains publication authority; retries do not refresh that authority.
type zcodePendingNativeSettlement struct {
	settlement zcodeTurnSettlement
	source     agent.MessageContent
	receivedAt time.Time
}

func (a *Agent) queueZCodeNativeSettlement(owner *zcodeTurnState, settle zcodeTurnSettlement, source agent.MessageContent) {
	a.Mu.Lock()
	if a.turns[owner.id] != owner || owner.finalized {
		a.Mu.Unlock()
		return
	}
	if owner.pendingNative == nil {
		owner.pendingNative = &zcodePendingNativeSettlement{settlement: settle, source: agent.MessageContent{AgentSessionID: source.AgentSessionID, Publication: source.Publication}, receivedAt: a.Clock().Now()}
	}
	if a.stopWindow.turnID == owner.id {
		a.cancelStoppedZCodeTurnLocked()
	}
	a.startZCodeTurnWatcherLocked(owner)
	state := owner.recovery
	if state != nil {
		select {
		case state.wake <- struct{}{}:
		default:
		}
	}
	a.Mu.Unlock()
}

func (a *Agent) zcodeNativeSettlementPending(owner *zcodeTurnState) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return owner.pendingNative != nil
}

// checkZCodeNativeSettlement uses the received native end as completion proof.
// Unlike idle recovery, it needs no second inference from session/read.
func (a *Agent) checkZCodeNativeSettlement(ctx context.Context, owner *zcodeTurnState, state *zcodeTurnRecoveryState) (bool, error) {
	a.Mu.Lock()
	current := a.currentZCodeRecoveryLocked(owner, state)
	pending := owner.pendingNative
	identity := a.captureZCodeObservationIdentityLocked()
	through := owner.lastEventSeq
	expired := pending != nil && a.Clock().Now().Sub(pending.receivedAt) >= zcodeTurnRecoveryFreezeLimit
	a.Mu.Unlock()
	if !current || ctx.Err() != nil {
		return true, nil
	}
	if pending == nil {
		return false, nil
	}
	if expired {
		return a.expireZCodeNativeSettlement(ctx, owner, state, pending)
	}
	if err := a.ensureZCodeReplayThrough(ctx, identity.sessionID, through, identity.epoch); err != nil {
		return false, err
	}
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	current = a.currentZCodeRecoveryLocked(owner, state) && owner.pendingNative == pending && a.zcodeSessionLifetimeCurrentLocked(identity)
	sessionID := a.sessionID
	selection := &zcodeTurnState{id: owner.id, anchorMessageID: owner.anchorMessageID, acceptedAt: owner.acceptedAt}
	observedSeq := owner.lastEventSeq
	a.Mu.Unlock()
	if !current || ctx.Err() != nil {
		return true, nil
	}
	a.unlockZCodeDispatch()
	messages, err := func() ([]zcodeNativeMessage, error) {
		defer a.dispatchMu.Lock()
		return a.fetchZCodeFinalMessages(ctx, sessionID, selection)
	}()
	a.Mu.Lock()
	current = a.currentZCodeRecoveryLocked(owner, state) && owner.pendingNative == pending && a.zcodeSessionLifetimeCurrentLocked(identity)
	unchanged := owner.lastEventSeq == observedSeq
	a.Mu.Unlock()
	if !current || ctx.Err() != nil {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !unchanged || a.zcodeReplayHasGapLocked() {
		return false, nil
	}
	if err := a.applyZCodeRecoveredMessagesLocked(owner, messages); err != nil {
		return false, err
	}
	return a.settleZCodeTurn(owner, pending.settlement), nil
}

func (a *Agent) expireZCodeNativeSettlement(ctx context.Context, owner *zcodeTurnState, state *zcodeTurnRecoveryState, pending *zcodePendingNativeSettlement) (bool, error) {
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	current := a.currentZCodeRecoveryLocked(owner, state) && owner.pendingNative == pending && ctx.Err() == nil
	a.Mu.Unlock()
	if !current {
		return true, nil
	}
	settle := pending.settlement
	settle.textCompletion, settle.callCompletion = agent.MessageCompletionError, agent.MessageCompletionError
	nativeEnd := settle.persistEnd
	settle.persistEnd = func(closed int, publish bool) {
		if nativeEnd != nil {
			nativeEnd(closed, publish)
		}
		a.persistZCodeRecoveryError(owner, "ZCode ended this turn, but LeapMux could not restore all of its output.")
	}
	return a.settleZCodeTurn(owner, settle), nil
}

package muse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type childViewStart struct {
	deadline      time.Time
	prefix        []json.RawMessage
	after         string
	head          *string
	registryItems []json.RawMessage
	empty         bool
	nativeHead    *childNativeHead
}

func (a *Agent) childViewTimeout(deadline time.Time) (time.Duration, error) {
	remaining := deadline.Sub(a.Clock().Now())
	if remaining <= 0 {
		return 0, fmt.Errorf("the Muse child view deadline expired")
	}
	if err := a.Context().Err(); err != nil {
		return 0, err
	}
	return remaining, nil
}

// readChildViewHead reads metadata at one coherent native cursor.
func (a *Agent) readChildViewHead(id string, state *sessionState, recovery *viewRecovery, deadline time.Time) (childViewStart, error) {
	start := childViewStart{deadline: deadline}
	timeout, err := a.childViewTimeout(deadline)
	if err != nil {
		return start, err
	}
	var observedErr error
	_, err = a.request(methodSessionRead, map[string]any{"sessionId": id, "excludeItems": false}, timeout, func(raw json.RawMessage, nativeErr error) {
		if nativeErr != nil {
			return
		}
		var result struct {
			Session struct {
				ID         string          `json:"sessionId"`
				Status     string          `json:"status"`
				ActiveTurn json.RawMessage `json:"activeTurnId"`
			} `json:"session"`
			Cursor  *string         `json:"viewCursor"`
			History json.RawMessage `json:"history"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Session.ID != id || result.Cursor == nil || len(result.History) == 0 {
			observedErr = fmt.Errorf("the Muse child history has no coherent owned head")
			return
		}
		start.head = result.Cursor
		active := result.Session.ActiveTurn
		// Null also describes a session that this host has not loaded. Only an
		// explicit idle load state supplies negative activity proof.
		if providerkit.RawKindOf(active) == providerkit.RawNull && result.Session.Status != "idle" {
			active = nil
		}
		items, complete := childRouteHistoryItems(result.History, *result.Cursor)
		if complete {
			start.registryItems = items
			if len(items) == 0 {
				start.after, start.empty = *result.Cursor, true
			}
		}
		a.dispatcher.mu.Lock()
		if _, err := a.childViewTimeout(deadline); err != nil {
			observedErr = err
			a.dispatcher.mu.Unlock()
			return
		}
		start.nativeHead, observedErr = a.seedChildNativeHeadLocked(id, state, recovery, *result.Cursor, active)
		a.dispatcher.mu.Unlock()
	})
	if err != nil {
		return childViewStart{deadline: deadline}, err
	}
	if observedErr != nil {
		return childViewStart{deadline: deadline}, observedErr
	}
	return start, nil
}

// readChildViewStart accepts genuine events from the beginning of the view.
// A proved empty origin view retains its coherent subscription position without inventing activity.
func (a *Agent) readChildViewStart(id string, state *sessionState, recovery *viewRecovery, deadline time.Time) (childViewStart, error) {
	start, err := a.readChildViewHead(id, state, recovery, deadline)
	if err != nil {
		return start, err
	}
	start.empty, start.after = false, ""
	prefix, next, err := a.readChildViewPage(id, state, recovery, nil, deadline)
	if err != nil {
		return start, err
	}
	if len(prefix) == 0 {
		a.dispatcher.mu.Lock()
		a.stateMu.Lock()
		emptyOrigin := recovery != nil && next == nil && start.head != nil && len(start.registryItems) == 0 &&
			a.currentSessionState(id, state) && state.viewRecovery == recovery && state.nativeHead == start.nativeHead
		if emptyOrigin {
			// An unserved fold makes no assertion about the view. A durable
			// buffered event at the cut does, and an empty page cannot replace it.
			for _, raw := range recovery.buffer {
				cursor := viewFrameCursor(raw)
				if cursor == nil || *cursor != *start.head {
					continue
				}
				anchor, err := childProjectionAnchor(raw)
				if err != nil || anchor != nil {
					emptyOrigin = false
					break
				}
			}
		}
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		if emptyOrigin {
			start.empty, start.after = true, *start.head
			return start, nil
		}
		return start, fmt.Errorf("the Muse child view has no accepted history cursor")
	}
	start.prefix = prefix
	start.after = viewCursor(prefix[len(prefix)-1])
	a.dispatcher.mu.Lock()
	a.reconcileChildNativeReplayLocked(id, state, recovery, start.nativeHead, childViewFrames(start.prefix, nil))
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	return start, nil
}

func (a *Agent) subscribeChildView(id string, state *sessionState, after string, deadline time.Time) error {
	ctx, finish := a.newChildProofContext(deadline)
	defer finish()
	a.stateMu.Lock()
	var recovery *viewRecovery
	if state != nil {
		recovery = state.viewRecovery
	}
	a.stateMu.Unlock()
	return a.subscribeChildViewContext(ctx, id, state, recovery, after)
}

func (a *Agent) subscribeChildViewContext(ctx context.Context, id string, state *sessionState, recovery *viewRecovery, after string) error {
	release, err := a.acquireSessionSubscriptionContext(ctx, state)
	if err != nil {
		return err
	}
	a.stateMu.Lock()
	// The page and subscription must retain the same recovery owner.
	current := a.currentSessionState(id, state) && state.viewRecovery == recovery
	a.stateMu.Unlock()
	if !current {
		release()
		return agent.ErrChildRouteNotReady
	}
	_, err = a.awaitOwnedSubscription(ctx, release, methodViewSubscribe, map[string]string{"sessionId": id, "after": after}, func(raw json.RawMessage) error {
		var reply struct {
			Cursor *string `json:"viewCursor"`
		}
		if json.Unmarshal(raw, &reply) != nil || reply.Cursor == nil {
			return fmt.Errorf("the Muse child subscription has no view head")
		}
		a.stateMu.Lock()
		current := a.currentSessionState(id, state)
		a.stateMu.Unlock()
		if !current {
			return agent.ErrChildRouteNotReady
		}
		return nil
	})
	return err
}

// syncChildView establishes one prefix before admitting live output. The host
// replays strictly after that accepted cursor before it delivers live events.
func (a *Agent) syncChildView(id string, state *sessionState) {
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	if !a.currentSessionState(id, state) {
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		return
	}
	requested := state.childViewRequested
	state.childViewRequested = false
	if requested && state.projection != nil && state.projection.failed != nil {
		previous := state.projection
		state.projection = newChildProjection()
		state.projection.classifiedThrough = previous.classifiedThrough
	}
	recovery := state.viewRecovery
	ready := state.acceptedViewCursor != nil
	if recovery == nil && requested {
		recovery = &viewRecovery{}
		state.viewRecovery = recovery
	}
	if recovery == nil {
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		return
	}
	after := ""
	if ready {
		after = *state.acceptedViewCursor
	}
	if ready && recovery.after != "" {
		after = recovery.after
	}
	deadline := a.childRecoveryDeadlineLocked(recovery)
	state.childViewPrefix = childViewPrefixPending
	a.stateMu.Unlock()
	a.dispatcher.mu.Unlock()
	ctx, finish := a.newChildProofContext(deadline)
	defer finish()
	if err := a.retainChildOpeningPrompt(id, state); err != nil {
		a.failChildView(id, state, recovery, nil, err)
		return
	}
	var start childViewStart
	var err error
	if ready {
		start, err = a.readChildViewHead(id, state, recovery, deadline)
		start.after = after
	} else {
		start, err = a.readChildViewStart(id, state, recovery, deadline)
	}
	if err != nil {
		a.failChildView(id, state, recovery, start.prefix, err)
		return
	}
	a.stateMu.Lock()
	if a.currentSessionState(id, state) && state.viewRecovery == recovery && state.nativeHead == start.nativeHead && start.nativeHead != nil && start.after == start.nativeHead.cursor {
		start.nativeHead.passed = true
	}
	a.stateMu.Unlock()
	err = a.subscribeChildViewContext(ctx, id, state, recovery, start.after)
	if err != nil {
		// A completed child can refuse a live subscription. Its entire stored
		// prefix must still reach the coherent head before any row is admitted.
		var refusal *providerkit.JSONRPCResponseError
		if errors.As(err, &refusal) && ready {
			head, readErr := a.readChildViewHead(id, state, recovery, deadline)
			if readErr == nil {
				head.after = start.after
				start = head
			}
		}
		a.stateMu.Lock()
		possiblyInactive := start.nativeHead != nil && (!start.nativeHead.activityKnown || start.nativeHead.activeTurnID == "")
		a.stateMu.Unlock()
		if errors.As(err, &refusal) && possiblyInactive && start.head != nil && (len(start.prefix) > 0 || start.empty || ready) {
			reached := start.empty || ready && start.after == *start.head
			if !reached {
				reached = slices.ContainsFunc(start.prefix, func(raw json.RawMessage) bool { return viewCursor(raw) == *start.head })
			}
			if !reached {
				more, readErr := a.readChildViewThrough(id, state, recovery, &start.after, *start.head, deadline)
				start.prefix = append(start.prefix, more...)
				reached = readErr == nil && slices.ContainsFunc(more, func(raw json.RawMessage) bool { return viewCursor(raw) == *start.head })
				if readErr != nil {
					err = errors.Join(err, readErr)
				}
			}
			if reached {
				reconcile := func(prefix []json.RawMessage) (known, inactive bool) {
					a.dispatcher.mu.Lock()
					a.stateMu.Lock()
					frames := childViewFrames(prefix, recovery.buffer)
					a.stateMu.Unlock()
					a.reconcileChildNativeReplayLocked(id, state, recovery, start.nativeHead, frames)
					a.stateMu.Lock()
					known = a.currentSessionState(id, state) && state.viewRecovery == recovery && state.nativeHead == start.nativeHead && start.nativeHead.activityKnown
					inactive = known && start.nativeHead.activeTurnID == ""
					a.stateMu.Unlock()
					a.dispatcher.mu.Unlock()
					a.drainDeferredOutputActions()
					return known, inactive
				}
				known, inactive := reconcile(start.prefix)
				if !known {
					prefix, readErr := a.readChildViewThrough(id, state, recovery, nil, *start.head, deadline)
					if readErr != nil {
						err = errors.Join(err, readErr)
					} else {
						// This prefix proves activity only. Its accepted transcript
						// rows must not enter the projection queue again.
						_, inactive = reconcile(prefix)
					}
				}
				if inactive {
					err = nil
				}
			}
		}
	}
	if err != nil {
		a.failChildView(id, state, recovery, start.prefix, err)
		return
	}
	if err := a.subscribeLogContext(ctx, id, deadline); err != nil {
		a.failChildView(id, state, recovery, start.prefix, err)
		return
	}
	if ctx.Err() != nil {
		a.failChildView(id, state, recovery, start.prefix, context.Cause(ctx))
		return
	}
	if _, err := a.childViewTimeout(deadline); err != nil {
		a.failChildView(id, state, recovery, start.prefix, err)
		return
	}
	a.acceptChildView(id, state, recovery, start)
}

func (a *Agent) readChildViewThrough(id string, state *sessionState, recovery *viewRecovery, after *string, head string, deadline time.Time) ([]json.RawMessage, error) {
	var events []json.RawMessage
	seen := make(map[string]bool)
	if after != nil {
		seen[*after] = true
	}
	for {
		page, next, err := a.readChildViewPage(id, state, recovery, after, deadline)
		if err != nil {
			return events, err
		}
		events = append(events, page...)
		if slices.ContainsFunc(page, func(raw json.RawMessage) bool { return viewCursor(raw) == head }) {
			return events, nil
		}
		if next == nil {
			return events, fmt.Errorf("the Muse child view ended before its coherent head")
		}
		if seen[*next] {
			return events, fmt.Errorf("the Muse child view cursor did not advance")
		}
		seen[*next] = true
		after = next
	}
}

func (a *Agent) acceptChildView(id string, state *sessionState, recovery *viewRecovery, start childViewStart) {
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	if state.viewRecovery != recovery {
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		return
	}
	buffer := recovery.buffer
	deadline := start.deadline
	if deadline.IsZero() {
		deadline = a.childRecoveryDeadlineLocked(recovery)
		start.deadline = deadline
	}
	current := a.currentSessionState(id, state)
	a.stateMu.Unlock()
	if !current {
		a.dispatcher.mu.Unlock()
		persistUnprojectedViewFrames(a.sink, id, buffer)
		return
	}
	frames := childViewFrames(start.prefix, buffer)
	a.reconcileChildNativeReplayLocked(id, state, recovery, start.nativeHead, frames)
	a.stateMu.Lock()
	state.viewRecovery = nil
	if start.empty {
		cursor := start.after
		state.acceptedViewCursor = &cursor
	}
	state.childViewPrefix = childViewPrefixReady
	state.viewOverlap = nil
	if start.nativeHead != nil {
		start.nativeHead.collecting = false
	}
	a.stateMu.Unlock()
	for _, raw := range frames {
		a.queueChildProjectionBeforeLocked(id, state, raw, deadline)
	}
	a.queueChildProjectionHeadLocked(id, state, start)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	a.drainControls()
}

func (a *Agent) failChildView(id string, state *sessionState, recovery *viewRecovery, prefix []json.RawMessage, reason error) {
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	if state.viewRecovery != recovery {
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		return
	}
	if state.projection == nil {
		state.projection = newChildProjection()
	}
	for _, raw := range prefix {
		state.projection.frames = append(state.projection.frames, childProjectionFrame{raw: raw})
	}
	for _, raw := range recovery.buffer {
		state.projection.frames = append(state.projection.frames, childProjectionFrame{raw: raw})
	}
	recovery.buffer = nil
	recovery.bufferedBytes = 0
	state.viewRecovery = nil
	if state.nativeHead != nil {
		state.nativeHead.collecting = false
	}
	a.stateMu.Unlock()
	a.failChildProjectionLocked(id, state, reason, nil)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	a.drainControls()
}

// Capture before dispatch releases, then recheck ownership before publication.
// The immutable capture refuses authority gained by a replacement after the check.
func (a *Agent) captureChildViewWarningLocked(id string, state *sessionState, reason error) func() {
	a.stateMu.Lock()
	current := a.currentSessionState(id, state)
	projection := state.projection
	a.stateMu.Unlock()
	if !current {
		return nil
	}
	raw, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldText: "The Muse child transcript is unavailable: " + reason.Error()})
	if err != nil {
		slog.Error("encode the Muse child view warning", "error", err)
		return nil
	}
	captured := agent.CaptureTranscript(a.sink, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	return func() {
		a.stateMu.Lock()
		current := a.currentSessionState(id, state) && state.childViewPrefix == childViewPrefixFailed && state.projection == projection
		a.stateMu.Unlock()
		if current {
			if _, err := captured.PersistWorkerNotification(); err != nil {
				slog.Warn("persist the Muse child view warning", "error", err)
			}
		}
	}
}

func persistUnprojectedViewFrames(writer agent.ProviderServices, id string, frames [][]byte) {
	if len(frames) == 0 {
		return
	}
	supplement, err := json.Marshal(map[string]any{contracts.MuseSupplementFieldViewProjectionUnavailable: map[string]string{"sessionId": id, "reason": "The Muse view could not be projected."}})
	if err != nil {
		slog.Error("encode the Muse view diagnostic", "error", err)
		return
	}
	// A child diagnostic would make its transcript nonempty before its opening
	// prompt can be retained. Keep unadmitted evidence on the root, with the
	// original native child session and no child item or live-state effects.
	for _, raw := range frames {
		digest := sha256.Sum256(raw)
		content := agent.MessageContent{Original: slices.Clone(raw), Supplemental: slices.Clone(supplement), AgentSessionID: id, IdempotencyKey: "muse:unprojected-view:" + hex.EncodeToString(digest[:])}
		if err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
			slog.Warn("retain unprojected Muse view evidence", "session", id, "error", err)
		}
	}
}

// childViewFrames removes only exact native replay overlap. The caller owns
// both slices, and each retained frame keeps its unchanged native bytes.
func childViewFrames(prefix []json.RawMessage, buffer [][]byte) [][]byte {
	frames := make([][]byte, 0, len(prefix)+len(buffer))
	served := make(map[[32]byte]bool)
	appendFrame := func(raw []byte) {
		if key, valid := viewFrameKey(raw); valid {
			if served[key] {
				return
			}
			served[key] = true
		}
		frames = append(frames, raw)
	}
	for _, raw := range prefix {
		appendFrame(raw)
	}
	for _, raw := range buffer {
		appendFrame(raw)
	}
	return frames
}

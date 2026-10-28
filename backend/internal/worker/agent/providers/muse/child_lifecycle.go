package muse

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Native lifecycle observations remain separate from transcript finalization.
// A failed proof must not finalize unproved text or suppress a later replay.
type childNativeTurnState struct {
	known  bool
	turnID string
	// published records admission of this exact activity to the child sink.
	published bool
}

// A coherent head supplies native activity independently of transcript replay.
// Only the bootstrap's frame identities remain here; later live events add no history.
type childNativeHead struct {
	cursor        string
	collecting    bool
	passed        bool
	activityKnown bool
	activeTurnID  string
	covered       map[[32]byte]struct{}
	observed      map[[32]byte]struct{}
}

// The caller holds stateMu.
func (state *sessionState) childTurnActive() bool {
	if state.nativeTurn.known {
		return state.nativeTurn.turnID != ""
	}
	return state.turnID != ""
}

// observeChildNativeTurn runs before recovery buffering. The caller holds dispatcher.mu.
func (a *Agent) observeChildNativeTurn(line *providerkit.ParsedLine, state *sessionState) {
	var identity struct {
		SessionID string  `json:"sessionId"`
		Cursor    *string `json:"viewCursor"`
	}
	if json.Unmarshal(line.Params, &identity) != nil {
		return
	}
	a.stateMu.Lock()
	allowed := a.currentSessionState(identity.SessionID, state) && state.childID != "" && allowChildNativeObservation(state, line, identity.Cursor)
	a.stateMu.Unlock()
	if !allowed {
		return
	}
	if line.Method != methodTurnStarted && line.Method != contracts.MuseMethodTurnCompleted {
		return
	}
	var params turnCompletion
	if json.Unmarshal(line.Params, &params) != nil || params.SessionID == "" || params.TurnID == "" || line.Method == contracts.MuseMethodTurnCompleted && params.Outcome == "" {
		return
	}
	a.stateMu.Lock()
	if !a.currentSessionState(params.SessionID, state) || state.childID == "" {
		a.stateMu.Unlock()
		return
	}
	if line.Method == methodTurnStarted {
		if !state.nativeTurn.known || state.nativeTurn.turnID != params.TurnID {
			state.nativeTurn = childNativeTurnState{known: true, turnID: params.TurnID}
		}
	} else {
		active := state.turnID
		if state.nativeTurn.known {
			active = state.nativeTurn.turnID
		}
		if active != "" && active != params.TurnID {
			a.stateMu.Unlock()
			return
		}
		if active == params.TurnID && state.completed[params.TurnID] == turnCompletionUnobserved {
			if state.completed == nil {
				state.completed = make(map[string]turnCompletionState)
			}
			state.completed[params.TurnID] = turnCompletionObserved
		}
		if !state.nativeTurn.known || state.nativeTurn.turnID != "" {
			state.nativeTurn = childNativeTurnState{known: true}
		}
	}
	if head := state.nativeHead; head != nil && head.collecting && identity.Cursor != nil {
		if key, valid := viewFrameKey(line.Raw); valid {
			head.observed[key] = struct{}{}
		}
	}
	failed := state.childViewPrefix == childViewPrefixFailed
	a.stateMu.Unlock()
	if failed {
		a.publishChildNativeStateLocked(params.SessionID, state)
	}
}

// The caller holds stateMu. Cursors are opaque, so only an observed equality
// can establish the cut. The snapshot already includes the cut event itself.
func allowChildNativeObservation(state *sessionState, line *providerkit.ParsedLine, cursor *string) bool {
	head := state.nativeHead
	if head == nil {
		return true
	}
	if cursor != nil && *cursor == head.cursor {
		head.passed = true
		// The coherent snapshot includes every event at this watermark.
		return false
	}
	if !head.passed {
		return false
	}
	if line.Method != methodTurnStarted && line.Method != contracts.MuseMethodTurnCompleted {
		return true
	}
	key, valid := viewFrameKey(line.Raw)
	if !valid {
		return false
	}
	if _, exists := head.covered[key]; exists {
		return false
	}
	if _, exists := head.observed[key]; exists {
		return false
	}
	return true
}

// The reply observer calls this before the reader accepts later events.
// The caller holds dispatcher.mu. No publication runs in the observer.
func (a *Agent) seedChildNativeHeadLocked(id string, state *sessionState, recovery *viewRecovery, cursor string, active json.RawMessage) (*childNativeHead, error) {
	var turn *string
	known := len(active) != 0
	if known && (json.Unmarshal(active, &turn) != nil || turn != nil && *turn == "") {
		return nil, fmt.Errorf("the Muse child head has an invalid active turn")
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !a.currentSessionState(id, state) || state.viewRecovery != recovery {
		return nil, agent.ErrChildRouteNotReady
	}
	if head := state.nativeHead; !known && head != nil && head.cursor == cursor && head.activityKnown {
		head.collecting = true
		return head, nil
	}
	head := &childNativeHead{cursor: cursor, collecting: true, covered: make(map[[32]byte]struct{}), observed: make(map[[32]byte]struct{})}
	if known {
		next := ""
		if turn != nil {
			next = *turn
		}
		head.activityKnown, head.activeTurnID = true, next
		if !state.nativeTurn.known || state.nativeTurn.turnID != next {
			state.nativeTurn = childNativeTurnState{known: true, turnID: next}
		}
	}
	state.nativeHead = head
	return head, nil
}

// The caller holds dispatcher.mu and supplies genuine frames in native order.
func (a *Agent) reconcileChildNativeReplayLocked(id string, state *sessionState, recovery *viewRecovery, head *childNativeHead, frames [][]byte) {
	if head == nil {
		return
	}
	a.stateMu.Lock()
	if !a.currentSessionState(id, state) || state.viewRecovery != recovery || state.nativeHead != head {
		a.stateMu.Unlock()
		return
	}
	cut := -1
	for index, raw := range frames {
		if cursor := viewFrameCursor(raw); cursor != nil && *cursor == head.cursor {
			cut = index
		}
	}
	if cut < 0 {
		a.stateMu.Unlock()
		return
	}
	activityKnown, activeTurnID := false, ""
	for _, raw := range frames[:cut+1] {
		line := providerkit.ParseLine(raw)
		if line.Method != methodTurnStarted && line.Method != contracts.MuseMethodTurnCompleted {
			continue
		}
		if key, valid := viewFrameKey(raw); valid {
			head.covered[key] = struct{}{}
		}
		if head.activityKnown {
			continue
		}
		var params turnCompletion
		if json.Unmarshal(line.Params, &params) != nil || params.SessionID != id || params.TurnID == "" || line.Method == contracts.MuseMethodTurnCompleted && params.Outcome == "" {
			continue
		}
		if line.Method == methodTurnStarted {
			activityKnown, activeTurnID = true, params.TurnID
		} else if activeTurnID == "" || activeTurnID == params.TurnID {
			activityKnown, activeTurnID = true, ""
		}
	}
	if !head.activityKnown && activityKnown {
		head.activityKnown, head.activeTurnID = true, activeTurnID
		// Install a newly proved cut once. Reusing that proof must not replace
		// native activity observed after the cut, even after bootstrap ended.
		if len(head.observed) == 0 && (!state.nativeTurn.known || state.nativeTurn.turnID != head.activeTurnID) {
			state.nativeTurn = childNativeTurnState{known: true, turnID: head.activeTurnID}
		}
	}
	head.passed = true
	a.stateMu.Unlock()
	for _, raw := range frames[cut+1:] {
		a.observeChildNativeTurn(providerkit.ParseLine(raw), state)
	}
}

// The caller holds dispatcher.mu. Normal completion keeps the divider before idle.
// A failed proof cannot supply that divider, so native lifecycle alone controls
// live activity until recovery can persist the genuine transcript.
func (a *Agent) publishChildNativeStateLocked(id string, state *sessionState) {
	a.stateMu.Lock()
	if !a.currentSessionState(id, state) || state.childID == "" || !state.nativeTurn.known || state.nativeTurn.published {
		a.stateMu.Unlock()
		return
	}
	if state.childViewPrefix != childViewPrefixFailed {
		// Provider-owned finalizations have not admitted their divider. Retained
		// writes already share the child queue with the later idle effect.
		if state.childViewPrefix != childViewPrefixReady || state.viewRecovery != nil || len(state.finalizations) != 0 || state.nativeHead != nil && !state.nativeHead.passed {
			a.stateMu.Unlock()
			return
		}
		if projection := state.projection; projection != nil && (projection.failed != nil || len(projection.frames) != 0 || len(projection.heads) != 0) {
			a.stateMu.Unlock()
			return
		}
	}
	state.nativeTurn.published = true
	native := state.nativeTurn
	writer := state.sink
	a.stateMu.Unlock()
	a.Mu.Lock()
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.deferOutputAction(func() {
		a.stateMu.Lock()
		current := a.currentSessionState(id, state) && state.nativeTurn == native
		a.stateMu.Unlock()
		if current {
			writer.SetTurnState(agent.TurnState{Active: native.turnID != "", Steerable: native.turnID != ""}, seq)
		}
	})
}

// A receipt prevents delayed view replay from reopening a settled native control.
// Transcript retention remains independent, so proof recovery can store its row.
type childControlSettlement struct{ retained bool }

// The caller holds dispatcher.mu. Cancellation runs outside provider locks.
func (a *Agent) observeChildControlSettlement(line *providerkit.ParsedLine, state *sessionState) {
	params, nativeID, valid := validSettlement(line)
	if !valid {
		return
	}
	a.stateMu.Lock()
	if !a.currentSessionState(params.SessionID, state) || state.childID == "" {
		a.stateMu.Unlock()
		return
	}
	if state.settledChildControls == nil {
		state.settledChildControls = make(map[string]*childControlSettlement)
	}
	if state.settledChildControls[nativeID] != nil {
		a.stateMu.Unlock()
		return
	}
	state.settledChildControls[nativeID] = &childControlSettlement{}
	previous := state.controls[nativeID]
	delete(state.controls, nativeID)
	a.stateMu.Unlock()
	if previous.published {
		a.enqueueControl(&controlRecord{kind: controlCancel, state: state, key: previous.key})
	}
}

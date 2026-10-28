package muse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type viewRecovery struct {
	deadline      time.Time
	after         string
	next          string
	buffer        [][]byte
	bufferedBytes int
}

// The caller holds stateMu.
func (a *Agent) childRecoveryDeadlineLocked(recovery *viewRecovery) time.Time {
	if recovery.deadline.IsZero() {
		recovery.deadline = a.Clock().Now().Add(a.childProofTimeout())
	}
	return recovery.deadline
}

type childViewPrefixState uint8

const (
	childViewPrefixUnrequested childViewPrefixState = iota
	childViewPrefixPending
	childViewPrefixReady
	childViewPrefixFailed
)

func viewCursor(raw []byte) string {
	if cursor := viewFrameCursor(raw); cursor != nil {
		return *cursor
	}
	return ""
}

func viewFrameCursor(raw []byte) *string {
	var frame struct {
		Params struct {
			Cursor *string `json:"viewCursor"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return nil
	}
	return frame.Params.Cursor
}

// syncView reads a session prefix before its buffered live overlap. Child
// refresh and native gap recovery share one keyed, process-owned operation.
func (a *Agent) syncView(id string) {
	a.stateMu.Lock()
	owned := a.sessions[id]
	child := a.currentSessionState(id, owned) && owned.childID != ""
	a.stateMu.Unlock()
	if child {
		a.syncChildView(id, owned)
		a.projectChildView(id, owned)
		return
	}
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	state := a.sessions[id]
	if !a.currentSessionState(id, state) {
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		return
	}
	recovery := state.viewRecovery
	a.stateMu.Unlock()
	a.dispatcher.mu.Unlock()
	if recovery == nil {
		return
	}
	a.runViewRecovery(id, state, recovery)
}

// readViewPage validates every event, including events after a requested head.
// A nil cursor means the beginning. An explicit empty cursor remains explicit.
func (a *Agent) readViewPage(id string, after *string, timeout time.Duration) ([]json.RawMessage, *string, error) {
	params := map[string]any{"sessionId": id, "limit": 200, "direction": "forward"}
	if after != nil {
		params["cursor"] = *after
	}
	raw, err := a.request(methodViewPage, params, timeout, nil)
	if err != nil {
		return nil, nil, err
	}
	var page struct {
		Events *[]json.RawMessage `json:"events"`
		Cursor json.RawMessage    `json:"nextCursor"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Events == nil || len(page.Cursor) == 0 {
		return nil, nil, fmt.Errorf("the Muse view page is invalid")
	}
	for _, rawEvent := range *page.Events {
		var event struct {
			Method string `json:"method"`
			Params struct {
				SessionID string  `json:"sessionId"`
				Cursor    *string `json:"viewCursor"`
			} `json:"params"`
		}
		if json.Unmarshal(rawEvent, &event) != nil || event.Method == "" || event.Params.SessionID != id || event.Params.Cursor == nil {
			return nil, nil, fmt.Errorf("the Muse view page contains an invalid or foreign event")
		}
	}
	var next *string
	if json.Unmarshal(page.Cursor, &next) != nil {
		return nil, nil, fmt.Errorf("the Muse view page cursor is invalid")
	}
	if next != nil && len(*page.Events) == 0 {
		return nil, nil, fmt.Errorf("the Muse view page cursor did not advance")
	}
	if next != nil && *next != viewCursor((*page.Events)[len(*page.Events)-1]) {
		return nil, nil, fmt.Errorf("the Muse view page cursor does not identify its last event")
	}
	return *page.Events, next, nil
}

// readViewPages keeps native cursors opaque and requires each page to advance.
func (a *Agent) readViewPages(id, after, target string) ([]json.RawMessage, error) {
	var events []json.RawMessage
	var cursor *string
	seen := make(map[string]bool)
	if after != "" {
		cursor = &after
		seen[after] = true
	}
	for {
		if err := a.Context().Err(); err != nil {
			return events, err
		}
		page, next, err := a.readViewPage(id, cursor, a.APITimeout())
		if err != nil {
			return events, err
		}
		events = append(events, page...)
		reached := target != "" && slices.ContainsFunc(page, func(raw json.RawMessage) bool { return viewCursor(raw) == target })
		if next == nil {
			return events, nil
		}
		if seen[*next] {
			return events, fmt.Errorf("the Muse view page cursor did not advance")
		}
		seen[*next] = true
		cursor = next
		if reached {
			return events, nil
		}
	}
}

// viewFrameKey compares native methods and parameters, while framing headers
// remain in the original bytes. Distinct events may share an opaque cursor.
func viewFrameKey(raw []byte) ([32]byte, bool) {
	var event struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Method == "" || len(event.Params) == 0 {
		return [32]byte{}, false
	}
	value, err := json.Marshal(event)
	if err != nil {
		return [32]byte{}, false
	}
	digest, err := agent.JSONCanonicalDigest(value)
	return digest, err == nil
}

// bufferViewFrame separates the paged prefix from its live overlap.
// The dispatcher holds dispatcher.mu. stateMu protects the recovery buffer.
func (a *Agent) bufferViewFrame(line *providerkit.ParsedLine, state *sessionState) bool {
	if line.Method == methodViewGap {
		return false
	}
	a.stateMu.Lock()
	if state.childID != "" && state.childViewPrefix == childViewPrefixFailed {
		a.stateMu.Unlock()
		writer, id, raw := a.sink, providerkit.StringField(line.Params, "sessionId"), slices.Clone(line.Raw)
		a.deferOutputAction(func() { persistUnprojectedViewFrames(writer, id, [][]byte{raw}) })
		return true
	}
	if recovery := state.viewRecovery; recovery != nil {
		if state.childID != "" {
			a.childRecoveryDeadlineLocked(recovery)
		}
		if state.childID != "" && len(line.Raw) > maxChildProjectionBytes-recovery.bufferedBytes {
			if state.projection == nil {
				state.projection = newChildProjection()
			}
			for _, raw := range recovery.buffer {
				state.projection.frames = append(state.projection.frames, childProjectionFrame{raw: raw})
			}
			recovery.buffer = nil
			recovery.bufferedBytes = 0
			state.viewRecovery = nil
			a.stateMu.Unlock()
			a.failChildProjectionLocked(providerkit.StringField(line.Params, "sessionId"), state, fmt.Errorf("the Muse child bootstrap exceeds its memory limit"), line.Raw)
			return true
		}
		recovery.buffer = append(recovery.buffer, slices.Clone(line.Raw))
		recovery.bufferedBytes += len(line.Raw)
		a.stateMu.Unlock()
		return true
	}
	defer a.stateMu.Unlock()
	// Stored pages never replay item/delta. Only an existing overlap window
	// requires a fingerprint of another live frame.
	if len(state.viewOverlap) > 0 && line.Method != contracts.MuseMethodItemDelta {
		if key, valid := viewFrameKey(line.Raw); valid {
			if _, duplicate := state.viewOverlap[key]; duplicate {
				delete(state.viewOverlap, key)
				return true
			}
		}
	}
	return false
}

func (a *Agent) recoverView(raw []byte, state *sessionState) {
	var gap struct {
		SessionID string `json:"sessionId"`
		After     string `json:"after"`
		Next      string `json:"next"`
	}
	if json.Unmarshal(raw, &gap) != nil || gap.SessionID == "" || gap.After == "" || gap.Next == "" {
		return
	}
	a.stateMu.Lock()
	if !a.currentSessionState(gap.SessionID, state) {
		a.stateMu.Unlock()
		return
	}
	if state.viewRecovery != nil {
		state.viewRecovery.next = gap.Next
		a.stateMu.Unlock()
		return
	}
	recovery := &viewRecovery{after: gap.After, next: gap.Next}
	state.viewOverlap = nil
	state.viewRecovery = recovery
	a.stateMu.Unlock()
	a.viewWork.Run(gap.SessionID, func() { a.syncView(gap.SessionID) })
}

func (a *Agent) runViewRecovery(id string, state *sessionState, recovery *viewRecovery) {
	var paged []json.RawMessage
	var readErr error
	for {
		a.stateMu.Lock()
		after, target := recovery.after, recovery.next
		retired := !a.currentSessionState(id, state)
		a.stateMu.Unlock()
		if retired {
			break
		}
		events, err := a.readViewPages(id, after, target)
		paged = append(paged, events...)
		if err != nil {
			readErr = err
			break
		}
		a.stateMu.Lock()
		if target == recovery.next {
			a.stateMu.Unlock()
			break
		}
		if len(events) > 0 {
			recovery.after = viewCursor(events[len(events)-1])
		}
		a.stateMu.Unlock()
	}
	func() {
		a.dispatcher.mu.Lock()
		defer a.dispatcher.mu.Unlock()
		a.stateMu.Lock()
		buffer := recovery.buffer
		state.viewRecovery = nil
		retired := !a.currentSessionState(id, state)
		state.viewOverlap = make(map[[32]byte]struct{})
		a.stateMu.Unlock()
		if retired {
			// The host retired mid-recovery. The buffered frames were still
			// RECEIVED native evidence, so they persist exactly as received --
			// losing one would erase output the provider really produced. The
			// paged replay is dropped instead: its gap belongs to a view the
			// retired host can no longer serve, and every dispatched frame
			// already guards its own live effects on the retired state.
			for _, raw := range buffer {
				a.dispatchOutput(providerkit.ParseLine(raw))
			}
			return
		}
		served := make(map[[32]byte]bool)
		received := make(map[[32]byte]bool, len(buffer))
		for _, raw := range buffer {
			if key, valid := viewFrameKey(raw); valid {
				received[key] = true
			}
		}
		overlapped := false
		for _, raw := range paged {
			key, valid := viewFrameKey(raw)
			if valid && served[key] {
				continue
			}
			if valid {
				served[key] = true
			}
			line := providerkit.ParseLine(raw)
			a.dispatchOutput(line)
			if received[key] && valid {
				overlapped = true
			}
			if overlapped && valid && !received[key] && !slices.Contains(museOptedOutMethods, line.Method) {
				a.stateMu.Lock()
				state.viewOverlap[key] = struct{}{}
				a.stateMu.Unlock()
			}
		}
		for _, raw := range buffer {
			if key, valid := viewFrameKey(raw); valid && served[key] {
				continue
			}
			a.dispatchOutput(providerkit.ParseLine(raw))
		}

	}()
	if readErr != nil {
		slog.Warn("recover the Muse view", "session", id, "error", readErr)
	}
	a.drainDeferredOutputActions()
	a.drainControls()
}

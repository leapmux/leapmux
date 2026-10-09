package muse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type viewRecovery struct {
	after  string
	next   string
	buffer [][]byte
}

func viewCursor(raw []byte) string {
	var frame struct {
		Params struct {
			Cursor string `json:"viewCursor"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return ""
	}
	return frame.Params.Cursor
}

// readViewPages keeps native cursors opaque and requires each page to advance.
func (a *Agent) readViewPages(id, after, target string) ([]json.RawMessage, error) {
	var events []json.RawMessage
	seen := map[string]bool{after: true}
	for {
		if err := a.Context().Err(); err != nil {
			return events, err
		}
		params := map[string]any{"sessionId": id, "limit": 200, "direction": "forward"}
		if after != "" {
			params["cursor"] = after
		}
		raw, err := a.request(methodViewPage, params, a.APITimeout(), nil)
		if err != nil {
			return events, err
		}
		var page struct {
			Events *[]json.RawMessage `json:"events"`
			Cursor json.RawMessage    `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Events == nil || len(page.Cursor) == 0 {
			return events, fmt.Errorf("the Muse view page is invalid")
		}
		reached := false
		for _, rawEvent := range *page.Events {
			var event struct {
				Method string `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
					Cursor    string `json:"viewCursor"`
				} `json:"params"`
			}
			if json.Unmarshal(rawEvent, &event) != nil || event.Method == "" || event.Params.SessionID != id || event.Params.Cursor == "" {
				return events, fmt.Errorf("the Muse view page contains an invalid or foreign event")
			}
			events = append(events, slices.Clone(rawEvent))
			if target != "" && event.Params.Cursor == target {
				reached = true
			}
		}
		if string(page.Cursor) == "null" {
			return events, nil
		}
		var next string
		if json.Unmarshal(page.Cursor, &next) != nil || next == "" || seen[next] || len(*page.Events) == 0 {
			return events, fmt.Errorf("the Muse view page cursor did not advance")
		}
		seen[next] = true
		after = next
		if reached {
			return events, nil
		}
	}
}

// bufferViewFrame separates the paged prefix from its live overlap.
// The dispatcher holds dispatchMu. stateMu protects the recovery buffer.
func (a *Agent) bufferViewFrame(line *providerkit.ParsedLine, state *sessionState) bool {
	if line.Method == methodViewGap {
		return false
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if state.viewRecovery != nil {
		state.viewRecovery.buffer = append(state.viewRecovery.buffer, slices.Clone(line.Raw))
		return true
	}
	cursor := viewCursor(line.Raw)
	if cursor != "" && state.viewTwins[cursor] {
		delete(state.viewTwins, cursor)
		return true
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
	if state.retired {
		a.stateMu.Unlock()
		return
	}
	if state.viewRecovery != nil {
		state.viewRecovery.next = gap.Next
		a.stateMu.Unlock()
		return
	}
	recovery := &viewRecovery{after: gap.After, next: gap.Next}
	state.viewRecovery = recovery
	a.stateMu.Unlock()
	go a.runViewRecovery(gap.SessionID, state, recovery)
}

func (a *Agent) runViewRecovery(id string, state *sessionState, recovery *viewRecovery) {
	var paged []json.RawMessage
	var readErr error
	for {
		a.stateMu.Lock()
		after, target := recovery.after, recovery.next
		retired := state.retired
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
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		a.stateMu.Lock()
		buffer := recovery.buffer
		state.viewRecovery = nil
		retired := state.retired
		if state.viewTwins == nil {
			state.viewTwins = make(map[string]bool)
		}
		a.stateMu.Unlock()
		if retired {
			return
		}
		served := make(map[string]bool)
		for _, raw := range paged {
			cursor := viewCursor(raw)
			if served[cursor] {
				continue
			}
			served[cursor] = true
			a.dispatchOutput(providerkit.ParseLine(raw))
			a.stateMu.Lock()
			state.viewTwins[cursor] = true
			a.stateMu.Unlock()
		}
		for _, raw := range buffer {
			cursor := viewCursor(raw)
			if served[cursor] {
				a.stateMu.Lock()
				delete(state.viewTwins, cursor)
				a.stateMu.Unlock()
				continue
			}
			a.dispatchOutput(providerkit.ParseLine(raw))
		}
	}()
	if readErr != nil {
		slog.Warn("recover the Muse view", "session", id, "error", readErr)
	}
	a.drainControls()
}

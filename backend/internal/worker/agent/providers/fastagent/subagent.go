package fastagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	fastagentArchiveRetryInterval = 250 * time.Millisecond
	fastagentArchiveRetryMax      = 4 * time.Second
	fastagentArchiveDeadline      = 5 * time.Second
	fastagentArchiveTimerTag      = "fastagent-child-archive"
)

// fastagentChildState keeps one native child and the next archive update to send.
type fastagentChildState struct {
	nativeID       string
	sessionID      string
	rowKey         string
	prompt         string
	title          string
	requestedLabel string
	// resultSeen distinguishes an empty native text result from no result.
	resultText     string
	resultSeen     bool
	claimedChildID string
	finalStatus    bgtask.Status
	finalSeen      bool
	retrying       bool
	deadline       time.Time
	done           chan struct{}
	replayMu       sync.Mutex
	messageIndex   int
	updateIndex    int
	lastError      error
}

func fastagentRawOutputText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var text *string
	if json.Unmarshal(raw, &text) != nil || text == nil {
		return "", false
	}
	return *text, true
}

func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tc.Status) {
		return a.observeSubagentCall(tc.ToolCallID, tc.Title, tc.RawInput)
	}
	if tc.ToolCallID == "" {
		return nil
	}
	input, valid := fastagentSubagentRequest(tc.Title, tc.RawInput)
	status := acp.FinalStatus(tc.Status)
	if !valid {
		a.childMu.Lock()
		existing := a.childState[tc.ToolCallID]
		a.childMu.Unlock()
		if existing == nil || !a.finishFastagentChild(existing) {
			return nil
		}
		obs := &acp.SubagentObservation{
			RowKey:   existing.rowKey,
			Status:   status,
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
		}
		if text, ok := fastagentRawOutputText(tc.RawOutput); ok && text != "" {
			obs.ReportID = existing.rowKey
			obs.Report = agent.SubagentReport{Text: text, Status: bgtask.StatusWire(status)}
		}
		return obs
	}
	label := input.Label
	if label == "" {
		label = "Subagent"
	}
	sessionID := a.CurrentSessionID()
	rowKey := fastagentChildRowKey(sessionID, tc.ToolCallID)
	a.childMu.Lock()
	if _, done := a.childDone[tc.ToolCallID]; done {
		a.childMu.Unlock()
		return nil
	}
	if existing := a.childState[tc.ToolCallID]; existing != nil {
		delete(a.childState, tc.ToolCallID)
		close(existing.done)
	}
	if a.childDone == nil {
		a.childDone = make(map[string]struct{})
	}
	a.childDone[tc.ToolCallID] = struct{}{}
	a.childMu.Unlock()
	obs := &acp.SubagentObservation{
		RowKey:        rowKey,
		ChildAgentKey: rowKey,
		Title:         label,
		Prompt:        input.Message,
		Status:        status,
		CloseRow:      true,
		Spawns:        true,
	}
	if text, ok := fastagentRawOutputText(tc.RawOutput); ok && text != "" {
		obs.ReportID = rowKey
		obs.Report = agent.SubagentReport{Text: text, Status: bgtask.StatusWire(status)}
	}
	return obs
}

type fastagentSubagentInput struct {
	Message string `json:"message"`
	Label   string `json:"label"`
}

func fastagentSubagentRequest(title string, rawInput json.RawMessage) (fastagentSubagentInput, bool) {
	var input fastagentSubagentInput
	if title != "subagent" && !strings.HasSuffix(title, "/subagent") {
		return input, false
	}
	if json.Unmarshal(rawInput, &input) != nil || input.Message == "" {
		return input, false
	}
	return input, true
}

// observeSubagentCall waits for the input that a streaming call sends later.
func (a *Agent) observeSubagentCall(toolCallID, title string, rawInput json.RawMessage) *acp.SubagentObservation {
	if toolCallID == "" {
		return nil
	}
	input, valid := fastagentSubagentRequest(title, rawInput)
	if !valid {
		return nil
	}
	label := input.Label
	if label == "" {
		label = "Subagent"
	}
	sessionID := a.CurrentSessionID()
	rowKey := fastagentChildRowKey(sessionID, toolCallID)
	a.childMu.Lock()
	if a.childState == nil {
		a.childState = make(map[string]*fastagentChildState)
	}
	if _, done := a.childDone[toolCallID]; done {
		a.childMu.Unlock()
		return nil
	}
	if _, known := a.childState[toolCallID]; known {
		a.childMu.Unlock()
		return nil
	}
	a.childState[toolCallID] = &fastagentChildState{
		nativeID: toolCallID, sessionID: sessionID, rowKey: rowKey,
		prompt: input.Message, title: label, requestedLabel: input.Label,
		done: make(chan struct{}),
	}
	a.childMu.Unlock()
	return &acp.SubagentObservation{
		RowKey: rowKey, ChildAgentKey: rowKey,
		Title: label, Prompt: input.Message, Status: bgtask.StatusRunning, Spawns: true,
	}
}

func (a *Agent) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		return a.observeSubagentCall(tcu.ToolCallID, tcu.Title, tcu.RawInput)
	}
	a.childMu.Lock()
	state, known := a.childState[tcu.ToolCallID]
	if known && !state.finalSeen {
		if text, ok := fastagentRawOutputText(tcu.RawOutput); ok {
			state.resultText = text
			state.resultSeen = true
		}
		state.finalSeen = true
		state.finalStatus = acp.FinalStatus(tcu.Status)
		state.deadline = a.archiveClock().Now().Add(fastagentArchiveDeadline)
	}
	a.childMu.Unlock()
	if !known {
		return nil
	}
	if obs := a.replayFastagentChild(state); obs != nil {
		return obs
	}
	a.startFastagentArchiveRetry(state)
	return &acp.SubagentObservation{RowKey: state.rowKey, Title: state.title, Status: bgtask.StatusRunning}
}

// childUpdateRoute puts nested tool updates under their parent subagent call.
func (a *Agent) childUpdateRoute(_ string, metadata map[string]json.RawMessage) string {
	var parent string
	if json.Unmarshal(metadata["parentToolCallId"], &parent) != nil || parent == "" {
		return ""
	}
	a.childMu.Lock()
	state := a.childState[parent]
	a.childMu.Unlock()
	if state != nil {
		return state.rowKey
	}
	return ""
}

func fastagentChildRowKey(sessionID, nativeID string) string {
	if sessionID == "" {
		return bgtask.NormalizeRowKey(nativeID)
	}
	return bgtask.NormalizeRowKey(sessionID + ":" + nativeID)
}

func (a *Agent) archiveClock() quartz.Clock {
	if a.clock != nil {
		return a.clock
	}
	return quartz.NewReal()
}

// clearChildState ends archive reads of the outgoing native session.
func (a *Agent) clearChildState() {
	a.childMu.Lock()
	if a.childStop != nil {
		close(a.childStop)
		a.childStop = nil
	}
	a.childState = nil
	a.childDone = nil
	a.childClaims = nil
	a.childMu.Unlock()
}

// retryChildArchives gives completed subagents one more immediate archive read
// and closes any subagents that never reached a final tool update before the turn ended.
func (a *Agent) retryChildArchives(err error, stopped bool) acp.PromptEnding {
	a.childMu.Lock()
	var pending []*fastagentChildState
	var incomplete []*fastagentChildState
	for _, state := range a.childState {
		if state.finalSeen {
			pending = append(pending, state)
		} else {
			incomplete = append(incomplete, state)
		}
	}
	a.childMu.Unlock()
	for _, state := range pending {
		if obs := a.replayFastagentChild(state); obs != nil {
			a.ApplySubagentObservation(obs)
		} else {
			a.startFastagentArchiveRetry(state)
		}
	}
	incompleteStatus := bgtask.StatusStopped
	if err != nil && !stopped {
		incompleteStatus = bgtask.StatusFailed
	}
	for _, state := range incomplete {
		if !a.finishFastagentChild(state) {
			continue
		}
		a.ApplySubagentObservation(&acp.SubagentObservation{
			RowKey:   state.rowKey,
			Status:   incompleteStatus,
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
		})
	}
	return acp.PromptEnding{}
}

// replayFastagentChild advances only after the child route accepts an update.
func (a *Agent) replayFastagentChild(state *fastagentChildState) *acp.SubagentObservation {
	state.replayMu.Lock()
	defer state.replayMu.Unlock()
	if !a.fastagentChildCurrent(state) {
		return nil
	}
	archive, err := readFastagentChildArchive(a.home, state.sessionID, state)
	if err != nil {
		a.noteFastagentArchiveError(state, err)
		if errors.Is(err, errFastagentArchiveAmbiguous) {
			return a.failFastagentChildArchiveLocked(state, "Child transcript is ambiguous")
		}
		return nil
	}
	if !a.claimFastagentChild(state, archive.childID) {
		a.noteFastagentArchiveError(state, errors.New("the fastagent child archive belongs to another ACP call"))
		return nil
	}
	for state.messageIndex < len(archive.messages) {
		updates, updateErr := fastagentHistoryUpdates(archive.messages[state.messageIndex], state.messageIndex == 0, state.prompt)
		if updateErr != nil {
			a.noteFastagentArchiveError(state, updateErr)
			return nil
		}
		for state.updateIndex < len(updates) {
			if !a.fastagentChildCurrent(state) {
				return nil
			}
			if !a.FeedChildUpdate(state.rowKey, updates[state.updateIndex]) {
				a.noteFastagentArchiveError(state, fmt.Errorf("child route refused archive update %d of message %d", state.updateIndex, state.messageIndex))
				return nil
			}
			state.updateIndex++
		}
		a.FinishChildTurn(state.rowKey)
		state.messageIndex++
		state.updateIndex = 0
	}
	if !a.finishFastagentChild(state) {
		return nil
	}
	return &acp.SubagentObservation{
		RowKey: state.rowKey, Status: state.finalStatus,
		CloseRow: true, Mode: acp.ModeCloseOnly,
	}
}

// claimFastagentChild keeps one native child with the ACP row that first used it.
func (a *Agent) claimFastagentChild(state *fastagentChildState, childID string) bool {
	a.childMu.Lock()
	defer a.childMu.Unlock()
	// ClearContext changes the current session before it clears provider state.
	if a.CurrentSessionID() != state.sessionID || a.childState[state.nativeID] != state || childID == "" ||
		(state.claimedChildID != "" && state.claimedChildID != childID) {
		return false
	}
	if a.childClaims == nil {
		a.childClaims = make(map[string]*fastagentChildState)
	}
	if owner := a.childClaims[childID]; owner != nil && owner != state {
		return false
	}
	state.claimedChildID = childID
	a.childClaims[childID] = state
	return true
}

func (a *Agent) fastagentChildCurrent(state *fastagentChildState) bool {
	a.childMu.Lock()
	current := a.childState[state.nativeID] == state
	a.childMu.Unlock()
	return current && a.CurrentSessionID() == state.sessionID
}

func (a *Agent) finishFastagentChild(state *fastagentChildState) bool {
	a.childMu.Lock()
	defer a.childMu.Unlock()
	if a.CurrentSessionID() != state.sessionID || a.childState[state.nativeID] != state {
		return false
	}
	delete(a.childState, state.nativeID)
	if a.childDone == nil {
		a.childDone = make(map[string]struct{})
	}
	a.childDone[state.nativeID] = struct{}{}
	close(state.done)
	return true
}

func (a *Agent) noteFastagentArchiveError(state *fastagentChildState, err error) {
	if state.lastError == nil || state.lastError.Error() != err.Error() {
		slog.Warn("fastagent child archive replay failed", "agent_id", a.AgentID(), "row_key", state.rowKey, "error", err)
	}
	state.lastError = err
}

func (a *Agent) startFastagentArchiveRetry(state *fastagentChildState) {
	a.childMu.Lock()
	if a.childState[state.nativeID] != state || state.retrying {
		a.childMu.Unlock()
		return
	}
	if a.childStop == nil {
		a.childStop = make(chan struct{})
	}
	state.retrying = true
	stop := a.childStop
	a.archiveWG.Add(1)
	a.childMu.Unlock()
	go a.retryFastagentChild(state, stop)
}

func (a *Agent) retryFastagentChild(state *fastagentChildState, stop <-chan struct{}) {
	defer a.archiveWG.Done()
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	clock := a.archiveClock()
	delay := fastagentArchiveRetryInterval
	for {
		remaining := state.deadline.Sub(clock.Now())
		if remaining <= 0 {
			if obs := a.failFastagentChildArchive(state, "Child transcript unavailable after 5 seconds"); obs != nil {
				a.ApplySubagentObservation(obs)
			}
			return
		}
		timer := clock.NewTimer(min(delay, remaining), fastagentArchiveTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(fastagentArchiveTimerTag)
			if obs := a.failFastagentChildArchive(state, "process exited before the child archive was ready"); obs != nil {
				a.ApplySubagentObservation(obs)
			}
			return
		case <-stop:
			timer.Stop(fastagentArchiveTimerTag)
			return
		case <-state.done:
			timer.Stop(fastagentArchiveTimerTag)
			return
		case <-timer.C:
		}
		if obs := a.replayFastagentChild(state); obs != nil {
			a.ApplySubagentObservation(obs)
			return
		}
		if !clock.Now().Before(state.deadline) {
			if obs := a.failFastagentChildArchive(state, "Child transcript unavailable after 5 seconds"); obs != nil {
				a.ApplySubagentObservation(obs)
			}
			return
		}
		delay = min(delay*2, fastagentArchiveRetryMax)
	}
}

func (a *Agent) failFastagentChildArchive(state *fastagentChildState, reason string) *acp.SubagentObservation {
	state.replayMu.Lock()
	defer state.replayMu.Unlock()
	return a.failFastagentChildArchiveLocked(state, reason)
}

func (a *Agent) failFastagentChildArchiveLocked(state *fastagentChildState, reason string) *acp.SubagentObservation {
	a.childMu.Lock()
	finalStatus := state.finalStatus
	resultText := state.resultText
	a.childMu.Unlock()
	if !a.fastagentChildCurrent(state) || !a.finishFastagentChild(state) {
		return nil
	}
	status := finalStatus
	if !status.IsFinished() {
		status = bgtask.StatusFailed
	}
	report := agent.SubagentReport{Status: bgtask.StatusWire(status)}
	if resultText != "" {
		report.Text = resultText
	} else {
		detail := "the archive did not become readable"
		if state.lastError != nil {
			detail = state.lastError.Error()
		}
		report.Label = "Archive unavailable"
		report.Text = reason + ": " + detail
	}
	return &acp.SubagentObservation{
		RowKey: state.rowKey, Title: state.title, Status: status, CloseRow: true,
		ReportID: state.rowKey,
		Report:   report,
	}
}

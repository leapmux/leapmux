package dirac

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// diracChildState records one native child card and the trajectory it sent.
type diracChildState struct {
	nativeID    string
	sessionID   string
	rowKey      string
	agentID     int
	agentName   string
	prompt      string
	trajectory  []diracTrajectoryEvent
	finalStatus bgtask.Status
	finalSeen   bool
	retrying    bool
	deadline    time.Time
	done        chan struct{}
	replayMu    sync.Mutex
	eventIndex  int
	lastError   error
}

type diracTrajectoryEvent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type diracChildCardInput struct {
	IsSubagent bool   `json:"isSubagent"`
	AgentID    int    `json:"agentId"`
	AgentName  string `json:"agentName"`
	TaskTitle  string `json:"taskTitle"`
	Prompt     string `json:"prompt"`
}

type diracChildCardOutput struct {
	Trajectory []diracTrajectoryEvent `json:"trajectory"`
}

func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if child := a.observeChildCard(tc.ToolCallID, tc.Title, tc.RawInput, tc.RawOutput, tc.Status); child != nil {
		return child
	}
	return diracSubagentFromToolCall(tc)
}

func (a *Agent) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	return a.observeChildCard(tcu.ToolCallID, tcu.Title, tcu.RawInput, tcu.RawOutput, tcu.Status)
}

// observeChildCard maps one native child card to a linked registry row.
func (a *Agent) observeChildCard(rowKey, title string, rawInput, rawOutput json.RawMessage, status string) *acp.SubagentObservation {
	if rowKey == "" {
		return nil
	}
	nativeID := rowKey
	var input diracChildCardInput
	if len(rawInput) > 0 && json.Unmarshal(rawInput, &input) != nil {
		return nil
	}
	var output diracChildCardOutput
	if len(rawOutput) > 0 && json.Unmarshal(rawOutput, &output) != nil {
		return nil
	}

	sessionID := a.CurrentSessionID()
	a.childMu.Lock()
	if a.childState == nil {
		a.childState = make(map[string]*diracChildState)
	}
	if _, done := a.childDone[nativeID]; done {
		a.childMu.Unlock()
		return nil
	}
	state, known := a.childState[nativeID]
	if known && state.finalSeen {
		a.childMu.Unlock()
		return nil
	}
	if !known {
		if !input.IsSubagent || input.AgentID <= 0 || input.AgentName == "" || input.Prompt == "" {
			a.childMu.Unlock()
			return nil
		}
		state = &diracChildState{
			nativeID: nativeID, sessionID: sessionID, rowKey: diracChildRowKey(sessionID, nativeID),
			agentID: input.AgentID, agentName: input.AgentName, prompt: input.Prompt,
			done: make(chan struct{}),
		}
		a.childState[nativeID] = state
	}
	newEvents := diracNewTrajectoryEvents(state.trajectory, output.Trajectory)
	final := acp.StatusIsFinal(status)
	if output.Trajectory != nil && !final {
		state.trajectory = slices.Clone(output.Trajectory)
	}
	if final {
		state.finalStatus = acp.FinalStatus(status)
		state.finalSeen = true
		state.deadline = a.archiveClock().Now().Add(diracArchiveDeadline)
		// The archive reader keeps identity and replay position. The last live
		// delta is already in newEvents, so no cumulative card must stay here.
		state.trajectory = nil
	}
	prompt := state.prompt
	agentName := state.agentName
	registryKey := state.rowKey
	a.childMu.Unlock()

	if title == "" {
		title = agentName
	}
	obs := &acp.SubagentObservation{
		RowKey: registryKey, ChildAgentKey: registryKey, Title: title,
		Status: bgtask.StatusRunning, Prompt: prompt, Spawns: !known,
	}
	if len(newEvents) > 0 {
		var text strings.Builder
		for _, event := range newEvents {
			if event.Text == "" || (event.Type != "message" && event.Type != "tool") {
				continue
			}
			fmt.Fprintf(&text, "%s: %s\n", event.Type, event.Text)
		}
		if text.Len() > 0 {
			obs.ChildTranscriptPayload, _ = json.Marshal(map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]string{"type": "text", "text": text.String()},
			})
		}
	}
	return obs
}

func diracChildRowKey(sessionID, nativeID string) string {
	if sessionID == "" {
		return bgtask.NormalizeRowKey(nativeID)
	}
	return bgtask.NormalizeRowKey(sessionID + ":" + nativeID)
}

// diracNewTrajectoryEvents returns entries a cumulative card first reports now.
func diracNewTrajectoryEvents(previous, current []diracTrajectoryEvent) []diracTrajectoryEvent {
	if current == nil {
		return nil
	}
	for overlap := min(len(previous), len(current)); overlap >= 0; overlap-- {
		if slices.Equal(previous[len(previous)-overlap:], current[:overlap]) {
			return current[overlap:]
		}
	}
	return current
}

// diracSubagentFromToolCall maps Dirac's aggregate `use_subagents` card to a
// SUBAGENT registry row. Dirac stamps the model-facing tool name on
// `rawInput.tool`, which is the one field of the call that names the tool:
// `title` is the card's display label ("Run Subagents"). Dirac runs its
// children behind one card, so the row is registry-only: no child transcript
// opens, and the row's activity is the card's own title.
func diracSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if diracRawInputTool(tc.RawInput) != contracts.DiracToolUseSubagents {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = contracts.DiracToolUseSubagents
	}
	return &acp.SubagentObservation{
		RowKey:   tc.ToolCallID,
		Title:    title,
		Activity: title,
	}
}

// diracRawInputTool reads the `tool` field Dirac stamps on every raw input.
// An input that is not a Dirac tool payload answers "".
func diracRawInputTool(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var input struct {
		Tool string `json:"tool"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	return input.Tool
}

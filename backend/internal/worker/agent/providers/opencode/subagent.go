package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The native words of a task call, in its ACP title and in its stored tool part.
const (
	// openCodeTaskTool is the tool that starts a subagent. A pending ACP call
	// of that tool states the tool's own name as its title.
	openCodeTaskTool     = "task"
	openCodePartTypeTool = "tool"
	// The states of a stored tool part that the child interrupt acts on. A task
	// part turns running before its child session exists, and states that
	// session once it does.
	openCodeToolStatusRunning   = "running"
	openCodeToolStatusCompleted = "completed"
	openCodeToolStatusError     = "error"
)

// IsHiddenPrimaryAgent reports whether a primary-agent id is an internal
// pseudo-agent that must be hidden from the picker. These ids originate in
// OpenCode's protocol but are shared by every OpenCode-family ACP provider
// (Kilo included), so both inject this as their primaryAgentHiddenFilter.
func IsHiddenPrimaryAgent(id string) bool {
	switch id {
	case HiddenCompaction, openCodeHiddenTitle, openCodeHiddenSummary:
		return true
	default:
		return false
	}
}

// OpenCode and Kilo identify the native task before its arguments arrive.
// Other titles require the native prompt and subagent discriminator.
func SubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	return openCodeSpawnObservation(tc.ToolCallID, tc.Title, tc.RawInput, tc.Title == openCodeTaskTool && tc.Kind == "think")
}

// Build the registry row from a known native task or its later arguments.
// Both event paths use the tool-call ID, so later arguments update the same row.
func openCodeSpawnObservation(toolCallID, callTitle string, rawInput json.RawMessage, knownTask bool) *acp.SubagentObservation {
	var input struct {
		Description  string          `json:"description"`
		Prompt       json.RawMessage `json:"prompt"`
		SubagentType string          `json:"subagent_type"`
		TaskID       json.RawMessage `json:"task_id"`
		// Some native builds use subagentID instead of subagent_type.
		// Either discriminator requires a parsed prompt.
		SubagentID string `json:"subagentID"`
	}
	inputParsed := json.Unmarshal(rawInput, &input) == nil
	prompt, promptParsed := providerkit.RawString(input.Prompt)
	taskID, taskIDParsed := "", true
	if len(input.TaskID) > 0 {
		taskID, taskIDParsed = providerkit.RawString(input.TaskID)
	}
	argumentsReady := inputParsed && promptParsed && taskIDParsed && (input.SubagentType != "" || input.SubagentID != "")
	// An unidentified tool requires parsed task arguments.
	if !knownTask && !argumentsReady {
		return nil
	}
	title := callTitle
	if title == "" || title == openCodeTaskTool {
		title = input.Description
	}
	if title == "" {
		title = input.SubagentType
	}
	if title == "" {
		title = "Subagent"
	}
	spawnSpanID := ""
	if argumentsReady && taskID == "" {
		spawnSpanID = toolCallID
	}
	return &acp.SubagentObservation{
		RowKey: toolCallID, Title: title, Status: bgtask.StatusRunning,
		ChildSpawnSpanID: spawnSpanID, Prompt: prompt, Spawns: true,
	}
}

// SubagentFromToolCallUpdate closes a final row and adopts the native child key from valid task metadata.
// RenameFrom preserves the initial registry row. ChildSpawnSpanID preserves the native invocation identity.
func SubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		// Kilo first supplies task arguments in an in-progress update.
		// Its initial tool_call can contain rawInput: {}.
		// Detect that request here so the final update closes an existing registry row.
		//
		// A non-final spawn update after completion recreates the tool-call row.
		// session/load replay can then send the final update again.
		// That update renames the recreated row to the existing native session row.
		// RenameBackgroundTask drops the duplicate source row on this collision.
		// The replay therefore retains one row and leaves no extra running row.
		return openCodeSpawnObservation(tcu.ToolCallID, tcu.Title, tcu.RawInput, false)
	}
	// The final rawOutput may carry the child session id under metadata.
	rowKey := tcu.ToolCallID
	renameFrom := ""
	childKey := ""
	spawnSpanID := ""
	background := false
	if len(tcu.RawOutput) > 0 {
		var out struct {
			Metadata struct {
				SessionID  string `json:"sessionId"`
				Background bool   `json:"background"`
			} `json:"metadata"`
		}
		if json.Unmarshal(tcu.RawOutput, &out) == nil {
			background = out.Metadata.Background
			if out.Metadata.SessionID != "" {
				rowKey = out.Metadata.SessionID
				renameFrom = tcu.ToolCallID
				childKey = out.Metadata.SessionID
				spawnSpanID = tcu.ToolCallID
			}
		}
	}
	return &acp.SubagentObservation{
		RowKey:           rowKey,
		RenameFrom:       renameFrom,
		ChildAgentKey:    childKey,
		ChildSpawnSpanID: spawnSpanID,
		Title:            tcu.Title,
		Status:           acp.FinalStatus(tcu.Status),
		CloseRow:         true,
		Mode:             acp.ModeCloseOnly,
		ReportID:         tcu.ToolCallID,
		Report: agent.SubagentReport{
			Text: openCodeSubagentReport(acp.ToolCallText(tcu.Content), background),
		},
	}
}

func openCodeSubagentReport(text string, background bool) string {
	if background {
		return ""
	}
	text = strings.TrimSpace(text)
	const start = "<task_result>"
	const end = "</task_result>"
	if _, after, ok := strings.Cut(text, start); ok {
		if report, _, found := strings.Cut(after, end); found {
			return strings.TrimSpace(report)
		}
	}
	return text
}

var _ agent.ChildInterrupter = (*FamilyBase)(nil)
var _ agent.ChildSteerer = (*FamilyBase)(nil)

// errOpenCodeSubagentIdle refuses a message to a subagent whose task call
// ended. A turn that the message started would reach no one: the parent
// already read the subagent's result.
var errOpenCodeSubagentIdle = errors.New("the subagent finished, so it takes no message")

// openCodeChildSession is the session of one running subagent.
type openCodeChildSession struct {
	id            string
	rootSessionID string
	// modelID is confirmed by this child's load response, independently of the root.
	modelID string
	// loaded is true once the agent loaded the session, so it streams the
	// session's updates and takes a prompt for it.
	loaded bool
}

func (child *openCodeChildSession) target(rowKey string) acp.ChildSessionTarget {
	return acp.ChildSessionTarget{RootSessionID: child.rootSessionID, SessionID: child.id, RowKey: rowKey}
}

// observeTaskPart follows the child session of a running task call of the
// current session. The task tool states that session before the child's first
// request, and only the daemon's event stream states it: the ACP stream of the
// parent carries no metadata of a running call.
func (b *FamilyBase) observeTaskPart(rootSessionID string, part openCodeTaskPart) {
	if part.Type != openCodePartTypeTool || part.Tool != openCodeTaskTool || part.CallID == "" ||
		part.State.Status != openCodeToolStatusRunning || part.State.Metadata.SessionID == "" {
		return
	}
	b.followChildSession(rootSessionID, part.CallID, part.State.Metadata.SessionID)
}

// followChildSession routes the updates of the child session of callID to the
// subagent's transcript, and loads the session into the agent. The ACP adapter
// streams only a session that it serves, so the subagent's own messages and
// tool calls reach LeapMux through the load alone. The load replays what the
// session already holds, so output from before the load is not lost.
func (b *FamilyBase) followChildSession(rootSessionID, callID, childSessionID string) {
	if rootSessionID == "" || !b.IsCurrentSession(rootSessionID) {
		return
	}
	b.childSessionsMu.Lock()
	if child := b.childSessions[callID]; child != nil && child.rootSessionID == rootSessionID {
		b.childSessionsMu.Unlock()
		return
	}
	if b.childSessions == nil {
		b.childSessions = make(map[string]*openCodeChildSession)
	}
	child := &openCodeChildSession{id: childSessionID, rootSessionID: rootSessionID}
	b.childSessions[callID] = child
	b.childSessionsMu.Unlock()
	// The daemon reader must not wait for the agent's answer.
	go func() {
		modelID, err := b.LoadChildSession(child.target(callID))
		if err != nil {
			if errors.Is(err, agent.ErrChildRouteNotReady) {
				b.childSessionsMu.Lock()
				if b.childSessions[callID] == child {
					delete(b.childSessions, callID)
				}
				b.childSessionsMu.Unlock()
				return
			}
			slog.Warn("load the OpenCode subagent session", "agent_id", b.AgentID(), "call_id", callID, "session_id", childSessionID, "error", err)
			return
		}
		_ = b.WithSessionID(func(currentRoot string) error {
			b.childSessionsMu.Lock()
			if currentRoot == rootSessionID && b.childSessions[callID] == child {
				child.modelID = modelID
				child.loaded = true
			}
			b.childSessionsMu.Unlock()
			return nil
		})
	}()
}

// forgetChildSession ends the following of the child session of callID. The
// base drops the session's route when the subagent's row closes.
func (b *FamilyBase) forgetChildSession(callID string) {
	b.childSessionsMu.Lock()
	delete(b.childSessions, callID)
	b.childSessionsMu.Unlock()
}

// runningChildSession returns the session of the running subagent of the task
// call childKey, and whether the agent loaded it yet.
func (b *FamilyBase) runningChildSession(childKey string) (sessionID string, loaded, running bool) {
	target, loaded, running := b.childSessionTarget(childKey)
	return target.SessionID, loaded, running
}

func (b *FamilyBase) childSessionTarget(childKey string) (acp.ChildSessionTarget, bool, bool) {
	rootSessionID := b.CurrentSessionID()
	if rootSessionID == "" || b.IsStopped() {
		return acp.ChildSessionTarget{}, false, false
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	child, running := b.childSessions[childKey]
	if !running || child.rootSessionID != rootSessionID {
		return acp.ChildSessionTarget{}, false, false
	}
	return child.target(childKey), child.loaded, true
}

// SendChildInput refuses a message as the start of a turn. A running subagent
// takes it as a steer of its running turn, and an ended one takes none.
func (b *FamilyBase) SendChildInput(childKey, _ string, _ []*leapmuxv1.Attachment) error {
	if _, _, running := b.runningChildSession(childKey); !running {
		return errOpenCodeSubagentIdle
	}
	return agent.ErrAgentBusy
}

// SteerChildInput sends a message into the running turn of a subagent, as a
// second prompt on the subagent's own session. The daemon adds the message to
// the session and the subagent reads it at its next step, as the root's steer
// works. The prompt carries the subagent's own agent and model, which the
// agent restored when it loaded the session. The queue writes the message into
// the subagent's transcript, so this writes no row of its own.
func (b *FamilyBase) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	target, loaded, running := b.childSessionTarget(childKey)
	if !running {
		return agent.ErrNoActiveTurn
	}
	if !loaded {
		return agent.ErrChildRouteNotReady
	}
	owner, _ := b.SessionOwner(target.SessionID)
	return b.SendChildPromptDetached(target, content, attachments, func(_ json.RawMessage, err error) {
		b.reportChildSteerFailure(target, owner, err)
	})
}

// ActiveChildTurnState reports a running subagent's turn, which takes a steer
// once the agent loaded the subagent's session.
func (b *FamilyBase) ActiveChildTurnState(childKey string) agent.TurnState {
	_, loaded, running := b.runningChildSession(childKey)
	return agent.TurnState{Active: running, Steerable: running && loaded}
}

// reportChildSteerFailure tells the reader, in the subagent's transcript, when
// the agent refused a steer. A stopped agent states nothing, as for the root.
func (b *FamilyBase) reportChildSteerFailure(target acp.ChildSessionTarget, originalOwner agent.ProviderServices, err error) {
	if err == nil || b.IsStopped() {
		return
	}
	_ = b.WithSessionID(func(rootSessionID string) error {
		owner := originalOwner
		if rootSessionID == target.RootSessionID {
			var found bool
			owner, found = b.SessionOwner(target.SessionID)
			if !found {
				owner = b.Sink()
			}
		}
		if owner == nil {
			return nil
		}
		slog.Error("acp subagent steer failed", "agent_id", b.AgentID(), "provider", b.ProviderName(), "session_id", target.SessionID, "error", err)
		owner.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("steer failed: %v", err),
		})
		return nil
	})
}

// InterruptChild stops one running subagent and leaves its parent turn running.
//
// childKey is the registry row key of the child: the native callID of the task
// call that started it, which is also its ACP toolCallId. The ACP stream states
// the child's own session only when that call ends, so this reads it from the
// parent's stored task part, where the task tool states it before the child's
// first request. The daemon then cancels that session; Kilo also cancels the
// sessions that the child started. The parent reads the failed task result and
// goes on with its turn.
func (b *FamilyBase) InterruptChild(childKey string, _ agent.StopContext) error {
	expectedRoot := b.CurrentSessionID()
	return b.WithSessionID(func(sessionID string) error {
		if sessionID == "" || sessionID != expectedRoot || b.IsStopped() {
			return agent.ErrChildRouteNotReady
		}
		ctx, cancel := context.WithTimeout(b.Context(), openCodeQuestionTimeout)
		defer cancel()
		part, found, err := b.Questions.findTaskPart(ctx, sessionID, childKey)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("the OpenCode session holds no task call %s", childKey)
		}
		switch part.State.Status {
		case openCodeToolStatusCompleted, openCodeToolStatusError:
			// The child ended before the stop arrived, so nothing remains to stop.
			return nil
		case openCodeToolStatusRunning:
			if child := part.State.Metadata.SessionID; child != "" {
				return b.stopChild(ctx, childKey, child)
			}
		}
		// A pending call, or a running call that has not created its child
		// session yet. A later click finds the session.
		return agent.ErrChildRouteNotReady
	})
}

// stopChild cancels the child session of the task call childKey, and records
// the stop for the close of the child's row (subagentFromToolCallUpdate).
//
// The record comes BEFORE the cancel, because the ACP stream can report the
// failed task before the daemon answers the cancel. A cancel that failed
// stopped nothing, so the record goes again and the native verdict stands.
func (b *FamilyBase) stopChild(ctx context.Context, childKey, childSessionID string) error {
	b.recordChildStop(childKey, true)
	if err := b.Questions.abortSession(ctx, childSessionID); err != nil {
		b.recordChildStop(childKey, false)
		return err
	}
	return nil
}

// recordChildStop records or forgets the user's stop of the subagent that the
// task call callID started.
func (b *FamilyBase) recordChildStop(callID string, stopped bool) {
	rootSessionID := b.CurrentSessionID()
	b.childStopMu.Lock()
	defer b.childStopMu.Unlock()
	if !stopped {
		delete(b.stoppedChildren, callID)
		return
	}
	if b.stoppedChildren == nil {
		b.stoppedChildren = make(map[string]string)
	}
	b.stoppedChildren[callID] = rootSessionID
}

// takeChildStop reports whether the user stopped the subagent of callID, and
// forgets the stop.
func (b *FamilyBase) takeChildStop(callID string) bool {
	rootSessionID := b.CurrentSessionID()
	b.childStopMu.Lock()
	defer b.childStopMu.Unlock()
	owner, stopped := b.stoppedChildren[callID]
	delete(b.stoppedChildren, callID)
	return stopped && owner == rootSessionID
}

// subagentFromToolCallUpdate reads a tool update as SubagentFromToolCallUpdate
// does, and closes the row of a subagent that the user stopped as interrupted.
//
// The daemon reports a cancelled child as a failed task, the same verdict as a
// child that failed on its own. LeapMux can tell the two apart, because LeapMux
// sent the cancel. A task that completed before the cancel took effect keeps
// its success.
func (b *FamilyBase) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	obs := SubagentFromToolCallUpdate(tcu)
	if obs == nil || !obs.CloseRow {
		return obs
	}
	b.forgetChildSession(tcu.ToolCallID)
	if b.takeChildStop(tcu.ToolCallID) && obs.Status != bgtask.StatusSucceeded {
		obs.Status = bgtask.StatusInterrupted
	}
	return obs
}

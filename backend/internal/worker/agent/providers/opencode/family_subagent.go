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

type openCodeTaskInput struct {
	description    string
	prompt         string
	subagentType   string
	subagentID     string
	taskID         string
	argumentsReady bool
}

func parseOpenCodeTaskInput(rawInput json.RawMessage) openCodeTaskInput {
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
	return openCodeTaskInput{
		description:    input.Description,
		prompt:         prompt,
		subagentType:   input.SubagentType,
		subagentID:     input.SubagentID,
		taskID:         taskID,
		argumentsReady: argumentsReady,
	}
}

type openCodeTaskOutput struct {
	sessionID  string
	background bool
}

func parseOpenCodeTaskOutput(rawOutput json.RawMessage) openCodeTaskOutput {
	if len(rawOutput) == 0 {
		return openCodeTaskOutput{}
	}
	var out struct {
		Metadata struct {
			SessionID  string `json:"sessionId"`
			Background bool   `json:"background"`
		} `json:"metadata"`
	}
	if json.Unmarshal(rawOutput, &out) != nil {
		return openCodeTaskOutput{}
	}
	return openCodeTaskOutput{
		sessionID:  out.Metadata.SessionID,
		background: out.Metadata.Background,
	}
}

func openCodeTaskTitle(callTitle string, input openCodeTaskInput) string {
	title := callTitle
	if title == "" || title == openCodeTaskTool {
		title = input.description
	}
	if title == "" {
		title = input.subagentType
	}
	if title == "" {
		title = "Subagent"
	}
	return title
}

// Build the registry row from a known native task or its later arguments.
// Both event paths use the tool-call ID, so later arguments update the same row.
func openCodeSpawnObservation(toolCallID, callTitle string, rawInput json.RawMessage, knownTask bool) *acp.SubagentObservation {
	input := parseOpenCodeTaskInput(rawInput)
	// An unidentified tool requires parsed task arguments.
	if !knownTask && !input.argumentsReady {
		return nil
	}
	title := openCodeTaskTitle(callTitle, input)
	spawnSpanID := ""
	if input.argumentsReady && input.taskID == "" {
		spawnSpanID = toolCallID
	}
	return &acp.SubagentObservation{
		RowKey: toolCallID, Title: title, Status: bgtask.StatusRunning,
		ChildSpawnSpanID: spawnSpanID, Prompt: input.prompt, Spawns: true,
	}
}

// SubagentFromToolCallUpdate closes a final row and adopts the native child key from valid task metadata.
// RenameFrom preserves the initial registry row. ChildSpawnSpanID preserves the native invocation identity.
// A successful background launch renames to metadata.sessionId and stays running until daemon completion.
func SubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		// Kilo first supplies task arguments in an in-progress update.
		// Its initial tool_call can contain rawInput: {}.
		// Detect that request here so the final update closes an existing registry row.
		return openCodeSpawnObservation(tcu.ToolCallID, tcu.Title, tcu.RawInput, false)
	}
	// The final rawOutput may carry the child session id under metadata.
	out := parseOpenCodeTaskOutput(tcu.RawOutput)
	rowKey := tcu.ToolCallID
	renameFrom := ""
	childKey := ""
	spawnSpanID := ""
	if out.sessionID != "" {
		rowKey = out.sessionID
		renameFrom = tcu.ToolCallID
		childKey = out.sessionID
		spawnSpanID = tcu.ToolCallID
	}
	status := acp.FinalStatus(tcu.Status)
	if out.background && out.sessionID != "" && status == bgtask.StatusSucceeded {
		return &acp.SubagentObservation{
			RowKey:           rowKey,
			RenameFrom:       renameFrom,
			ChildAgentKey:    childKey,
			ChildSpawnSpanID: spawnSpanID,
			Title:            tcu.Title,
			Status:           bgtask.StatusRunning,
			CloseRow:         false,
			Mode:             acp.ModeUpsert,
			ReportID:         tcu.ToolCallID,
			Report:           agent.SubagentReport{},
		}
	}
	return &acp.SubagentObservation{
		RowKey:           rowKey,
		RenameFrom:       renameFrom,
		ChildAgentKey:    childKey,
		ChildSpawnSpanID: spawnSpanID,
		Title:            tcu.Title,
		Status:           status,
		CloseRow:         true,
		Mode:             acp.ModeCloseOnly,
		ReportID:         tcu.ToolCallID,
		Report: agent.SubagentReport{
			Text: openCodeSubagentReport(acp.ToolCallText(tcu.Content), out.background),
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

// openCodeReusedTaskCall pairs a reuse callID with the revived task_id row key
// and the root session that owns it.
type openCodeReusedTaskCall struct {
	taskID        string
	rootSessionID string
}

// openCodeChildSession is the session of one running subagent.
type openCodeChildSession struct {
	id            string
	rootSessionID string
	// modelID is confirmed by this child's load response, independently of the root.
	modelID string
	// loaded is true once the agent loaded the session, so it streams the
	// session's updates and takes a prompt for it.
	loaded bool
	// loading is true while a followChildSession goroutine is running LoadChildSession.
	loading bool
	// background is true once the launch task call finished with background: true
	// and the row stays open until daemon SSE reports completion.
	background bool
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
	rowKey, ok := b.taskPartRowKey(rootSessionID, part.CallID, part.State.Metadata.SessionID)
	if !ok {
		return
	}
	b.followChildSession(rootSessionID, rowKey, part.State.Metadata.SessionID)
}

func (b *FamilyBase) taskPartRowKey(rootSessionID, callID, childSessionID string) (string, bool) {
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	if reused, ok := b.reusedTaskCalls[callID]; ok && (reused.rootSessionID == "" || reused.rootSessionID == rootSessionID) {
		return reused.taskID, true
	}
	if child := b.childSessions[childSessionID]; child != nil && child.background && (child.rootSessionID == "" || child.rootSessionID == rootSessionID) {
		return childSessionID, true
	}
	if owner, finalized := b.finalizedTaskCalls[callID]; finalized && (owner == "" || owner == rootSessionID) {
		return "", false
	}
	return callID, true
}

// followChildSession routes the updates of the child session of rowKey to the
// subagent's transcript, and loads the session into the agent. The ACP adapter
// streams only a session that it serves, so the subagent's own messages and
// tool calls reach LeapMux through the load alone. The load replays what the
// session already holds, so output from before the load is not lost.
func (b *FamilyBase) followChildSession(rootSessionID, rowKey, childSessionID string) {
	if rootSessionID == "" || !b.IsCurrentSession(rootSessionID) {
		return
	}
	b.childSessionsMu.Lock()
	child := b.childSessions[rowKey]
	if child != nil && child.rootSessionID == rootSessionID && (child.loaded || child.loading) {
		b.childSessionsMu.Unlock()
		return
	}
	if b.childSessions == nil {
		b.childSessions = make(map[string]*openCodeChildSession)
	}
	if child == nil || child.rootSessionID != rootSessionID {
		child = &openCodeChildSession{id: childSessionID, rootSessionID: rootSessionID}
		b.childSessions[rowKey] = child
	}
	child.id = childSessionID
	child.loading = true
	b.childSessionsMu.Unlock()
	// The daemon reader must not wait for the agent's answer.
	go func() {
		modelID, err := b.LoadChildSession(child.target(rowKey))
		if err != nil {
			if errors.Is(err, agent.ErrChildRouteNotReady) {
				b.childSessionsMu.Lock()
				child.loading = false
				if b.childSessions[rowKey] == child && !child.background {
					delete(b.childSessions, rowKey)
				}
				b.childSessionsMu.Unlock()
				return
			}
			b.childSessionsMu.Lock()
			child.loading = false
			b.childSessionsMu.Unlock()
			slog.Warn("load the OpenCode subagent session", "agent_id", b.AgentID(), "call_id", rowKey, "session_id", childSessionID, "error", err)
			return
		}
		_ = b.WithSessionID(func(currentRoot string) error {
			b.childSessionsMu.Lock()
			child.loading = false
			if currentRoot == rootSessionID && (b.childSessions[rowKey] == child || b.childSessions[childSessionID] == child) {
				child.modelID = modelID
				child.loaded = true
			}
			b.childSessionsMu.Unlock()
			return nil
		})
	}()
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
func (b *FamilyBase) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	if _, _, running := b.runningChildSession(childKey); !running {
		return agent.ChildInputResult{StartsTurn: true}, errOpenCodeSubagentIdle
	}
	return agent.ChildInputResult{StartsTurn:

	// SteerChildInput sends a message into the running turn of a subagent, as a
	// second prompt on the subagent's own session. The daemon adds the message to
	// the session and the subagent reads it at its next step, as the root's steer
	// works. The prompt carries the subagent's own agent and model, which the
	// agent restored when it loaded the session. The queue writes the message into
	// the subagent's transcript, so this writes no row of its own.
	true}, agent.ErrAgentBusy
}

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

// subagentFromToolCall inspects a task tool_call with FamilyBase's registry
// lookup so rawInput.task_id revives an existing child row directly.
func (b *FamilyBase) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	rootSessionID := b.CurrentSessionID()
	if acp.StatusIsFinal(tc.Status) {
		b.markTaskCallFinalized(tc.ToolCallID, rootSessionID)
	}
	return b.resolveSpawnObservation(tc.ToolCallID, tc.Title, tc.RawInput, tc.Title == openCodeTaskTool && tc.Kind == "think", false)
}

func (b *FamilyBase) resolveSpawnObservation(toolCallID, callTitle string, rawInput json.RawMessage, knownTask, fromUpdate bool) *acp.SubagentObservation {
	input := parseOpenCodeTaskInput(rawInput)
	if !knownTask && !input.argumentsReady {
		return nil
	}
	title := openCodeTaskTitle(callTitle, input)
	if input.argumentsReady && input.taskID != "" {
		rootSessionID := b.CurrentSessionID()
		alreadyReused := b.reusedTaskID(toolCallID, rootSessionID) == input.taskID
		if !alreadyReused {
			b.clearEarlyChildEvent(input.taskID)
		}
		var (
			childID string
			status  bgtask.Status
			found   bool
			err     error
		)
		if sink := b.Sink(); sink != nil {
			childID, status, found, err = sink.LookupBackgroundTask(input.taskID)
		}
		if err == nil && found && childID != "" {
			b.recordReusedTaskCall(rootSessionID, toolCallID, input.taskID, fromUpdate)
			if rootSessionID != "" {
				b.AttachChildSession(acp.ChildSessionTarget{
					RootSessionID: rootSessionID,
					SessionID:     input.taskID,
					RowKey:        input.taskID,
				})
			}
			renameFrom := ""
			if fromUpdate && !alreadyReused && toolCallID != input.taskID {
				renameFrom = toolCallID
			}
			return &acp.SubagentObservation{
				RowKey:        input.taskID,
				RenameFrom:    renameFrom,
				ChildAgentKey: input.taskID,
				Title:         title,
				Status:        bgtask.StatusRunning,
				ReviveRow:     !alreadyReused || status.IsFinished(),
				Prompt:        input.prompt,
				Spawns:        true,
			}
		}
		return &acp.SubagentObservation{
			RowKey: toolCallID,
			Title:  title,
			Status: bgtask.StatusRunning,
			Prompt: input.prompt,
			Spawns: true,
		}
	}
	spawnSpanID := ""
	if input.argumentsReady && input.taskID == "" {
		spawnSpanID = toolCallID
	}
	return &acp.SubagentObservation{
		RowKey:           toolCallID,
		Title:            title,
		Status:           bgtask.StatusRunning,
		ChildSpawnSpanID: spawnSpanID,
		Prompt:           input.prompt,
		Spawns:           true,
	}
}

// subagentFromToolCallUpdate reads a tool update with FamilyBase state:
//   - Ignores late non-final updates for already finalized or renamed tool calls.
//   - Revives existing task_id rows on Kilo's in-progress updates.
//   - Keeps successful background launches open until daemon SSE completion (or
//     closes immediately when an early daemon completion was already buffered).
//   - Closes a user-stopped subagent as interrupted unless it succeeded first.
func (b *FamilyBase) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	rootSessionID := b.CurrentSessionID()
	if !acp.StatusIsFinal(tcu.Status) {
		if b.isTaskCallFinalized(tcu.ToolCallID, rootSessionID) {
			return nil
		}
		return b.resolveSpawnObservation(tcu.ToolCallID, tcu.Title, tcu.RawInput, false, true)
	}
	b.markTaskCallFinalized(tcu.ToolCallID, rootSessionID)
	out := parseOpenCodeTaskOutput(tcu.RawOutput)
	reusedTaskID := b.reusedTaskID(tcu.ToolCallID, rootSessionID)

	rowKey := tcu.ToolCallID
	renameFrom := ""
	childKey := ""
	spawnSpanID := ""
	switch {
	case out.sessionID != "":
		rowKey = out.sessionID
		childKey = out.sessionID
		if reusedTaskID != "" {
			if reusedTaskID != out.sessionID {
				renameFrom = reusedTaskID
			}
		} else {
			renameFrom = tcu.ToolCallID
			spawnSpanID = tcu.ToolCallID
		}
	case reusedTaskID != "":
		rowKey = reusedTaskID
		childKey = reusedTaskID
	}

	status := acp.FinalStatus(tcu.Status)
	if out.background && out.sessionID != "" && status == bgtask.StatusSucceeded {
		if earlyStatus, closedEarly := b.claimBackgroundChildLaunch(rootSessionID, tcu.ToolCallID, reusedTaskID, out.sessionID); closedEarly {
			if b.takeTaskChildStop(tcu.ToolCallID, reusedTaskID, out.sessionID) && earlyStatus != bgtask.StatusSucceeded {
				earlyStatus = bgtask.StatusInterrupted
			}
			return &acp.SubagentObservation{
				RowKey:           rowKey,
				RenameFrom:       renameFrom,
				ChildAgentKey:    childKey,
				ChildSpawnSpanID: spawnSpanID,
				Title:            tcu.Title,
				Status:           earlyStatus,
				CloseRow:         true,
				Mode:             acp.ModeCloseOnly,
				ReportID:         tcu.ToolCallID,
				Report:           agent.SubagentReport{},
			}
		}
		return &acp.SubagentObservation{
			RowKey:           rowKey,
			RenameFrom:       renameFrom,
			ChildAgentKey:    childKey,
			ChildSpawnSpanID: spawnSpanID,
			Title:            tcu.Title,
			Status:           bgtask.StatusRunning,
			CloseRow:         false,
			Mode:             acp.ModeUpsert,
			ReportID:         tcu.ToolCallID,
			Report:           agent.SubagentReport{},
		}
	}

	b.finishForegroundChildTracking(tcu.ToolCallID, reusedTaskID, rowKey)
	if b.takeTaskChildStop(tcu.ToolCallID, reusedTaskID, rowKey) && status != bgtask.StatusSucceeded {
		status = bgtask.StatusInterrupted
	}
	return &acp.SubagentObservation{
		RowKey:           rowKey,
		RenameFrom:       renameFrom,
		ChildAgentKey:    childKey,
		ChildSpawnSpanID: spawnSpanID,
		Title:            tcu.Title,
		Status:           status,
		CloseRow:         true,
		Mode:             acp.ModeCloseOnly,
		ReportID:         tcu.ToolCallID,
		Report: agent.SubagentReport{
			Text: openCodeSubagentReport(acp.ToolCallText(tcu.Content), out.background),
		},
	}
}

func (b *FamilyBase) handleChildDaemonEvent(sessionID, eventType string, properties json.RawMessage) {
	switch eventType {
	case contracts.OpenCodeEventSessionStatus:
		var payload struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		if json.Unmarshal(properties, &payload) != nil {
			return
		}
		switch payload.Status.Type {
		case openCodeStatusTypeBusy, contracts.OpenCodeStatusTypeRetry:
			b.clearEarlyChildEvent(sessionID)
		case openCodeStatusTypeIdle:
			b.finishBackgroundChildSession(sessionID, bgtask.StatusSucceeded)
		}
	case openCodeEventSessionError:
		b.finishBackgroundChildSession(sessionID, bgtask.StatusFailed)
	case openCodeEventSessionDeleted:
		b.finishBackgroundChildSession(sessionID, bgtask.StatusStopped)
	default:
		slog.Debug("ignore OpenCode event outside the current session", "agent_id", b.AgentID(), "type", eventType)
	}
}

func (b *FamilyBase) finishBackgroundChildSession(sessionID string, status bgtask.Status) {
	b.childSessionsMu.Lock()
	child := b.childSessions[sessionID]
	if child == nil || !child.background {
		if b.earlyChildEvents == nil {
			b.earlyChildEvents = make(map[string]bgtask.Status)
		}
		if _, exists := b.earlyChildEvents[sessionID]; !exists {
			b.earlyChildEvents[sessionID] = status
		}
		b.childSessionsMu.Unlock()
		return
	}
	rootSessionID := child.rootSessionID
	delete(b.childSessions, sessionID)
	delete(b.earlyChildEvents, sessionID)
	b.childSessionsMu.Unlock()
	if b.takeChildStop(sessionID) && status != bgtask.StatusSucceeded {
		status = bgtask.StatusInterrupted
	}
	if rootSessionID == "" {
		rootSessionID = b.CurrentSessionID()
	}
	b.ApplySubagentObservationForSessionOffReader(rootSessionID, &acp.SubagentObservation{
		RowKey:   sessionID,
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	})
}

func (b *FamilyBase) claimBackgroundChildLaunch(rootSessionID, callID, reusedTaskID, sessionID string) (bgtask.Status, bool) {
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	if earlyStatus, ok := b.earlyChildEvents[sessionID]; ok {
		delete(b.earlyChildEvents, sessionID)
		delete(b.childSessions, callID)
		if reusedTaskID != "" {
			delete(b.childSessions, reusedTaskID)
		}
		delete(b.childSessions, sessionID)
		return earlyStatus, true
	}
	oldKey := callID
	if reusedTaskID != "" {
		oldKey = reusedTaskID
	}
	child := b.childSessions[oldKey]
	if child == nil && sessionID != oldKey {
		child = b.childSessions[sessionID]
	}
	if child == nil {
		child = &openCodeChildSession{id: sessionID, rootSessionID: rootSessionID}
	}
	child.id = sessionID
	if child.rootSessionID == "" {
		child.rootSessionID = rootSessionID
	}
	child.background = true
	if b.childSessions == nil {
		b.childSessions = make(map[string]*openCodeChildSession)
	}
	if oldKey != sessionID {
		delete(b.childSessions, oldKey)
	}
	if callID != sessionID {
		delete(b.childSessions, callID)
	}
	b.childSessions[sessionID] = child
	return 0, false
}

func (b *FamilyBase) finishForegroundChildTracking(callID, reusedTaskID, rowKey string) {
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	delete(b.childSessions, callID)
	if reusedTaskID != "" {
		delete(b.childSessions, reusedTaskID)
		delete(b.earlyChildEvents, reusedTaskID)
	}
	if rowKey != "" {
		delete(b.childSessions, rowKey)
		delete(b.earlyChildEvents, rowKey)
	}
}

func (b *FamilyBase) clearEarlyChildEvent(sessionID string) {
	if sessionID == "" {
		return
	}
	b.childSessionsMu.Lock()
	delete(b.earlyChildEvents, sessionID)
	b.childSessionsMu.Unlock()
}

func (b *FamilyBase) markTaskCallFinalized(callID, rootSessionID string) {
	if callID == "" {
		return
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	if b.finalizedTaskCalls == nil {
		b.finalizedTaskCalls = make(map[string]string)
	}
	b.finalizedTaskCalls[callID] = rootSessionID
}

func (b *FamilyBase) isTaskCallFinalized(callID, rootSessionID string) bool {
	if callID == "" {
		return false
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	owner, finalized := b.finalizedTaskCalls[callID]
	return finalized && (owner == "" || owner == rootSessionID)
}

func (b *FamilyBase) recordReusedTaskCall(rootSessionID, callID, taskID string, rekeyChild bool) {
	if callID == "" || taskID == "" {
		return
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	if b.reusedTaskCalls == nil {
		b.reusedTaskCalls = make(map[string]openCodeReusedTaskCall)
	}
	b.reusedTaskCalls[callID] = openCodeReusedTaskCall{taskID: taskID, rootSessionID: rootSessionID}
	if rekeyChild && callID != taskID {
		if child := b.childSessions[callID]; child != nil {
			delete(b.childSessions, callID)
			if b.childSessions[taskID] == nil {
				b.childSessions[taskID] = child
			}
		}
	}
}

func (b *FamilyBase) reusedTaskID(callID, rootSessionID string) string {
	if callID == "" {
		return ""
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	reused, ok := b.reusedTaskCalls[callID]
	if !ok || (reused.rootSessionID != "" && reused.rootSessionID != rootSessionID) {
		return ""
	}
	return reused.taskID
}

func (b *FamilyBase) takeTaskChildStop(keys ...string) bool {
	stopped := false
	for _, key := range keys {
		if key != "" && b.takeChildStop(key) {
			stopped = true
		}
	}
	return stopped
}

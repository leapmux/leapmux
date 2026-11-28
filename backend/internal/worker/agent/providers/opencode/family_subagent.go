package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
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
	// openCodeMaxTrackedEntries caps process-local tracking maps in FIFO order.
	openCodeMaxTrackedEntries = 512
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
	settings      acp.ChildSessionSettings
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
	if owner, finalized := b.finalizedTaskCalls.Get(callID); finalized && (owner == "" || owner == rootSessionID) {
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
	target := child.target(rowKey)
	b.childSessionsMu.Unlock()
	// The daemon reader must not wait for the agent's answer.
	go func() {
		settings, err := b.LoadChildSession(target)
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
				child.settings = settings
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
	child, running := b.childSession(childKey)
	return child.id, child.loaded, running
}

func (b *FamilyBase) childSession(childKey string) (openCodeChildSession, bool) {
	rootSessionID := b.CurrentSessionID()
	if rootSessionID == "" || b.IsStopped() {
		return openCodeChildSession{}, false
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	child, running := b.childSessions[childKey]
	if !running || child.rootSessionID != rootSessionID {
		return openCodeChildSession{}, false
	}
	return *child, true
}

// SendChildInput refuses a message as the start of a turn. A running subagent
// takes it as a steer of its running turn, and an ended one takes none.
func (b *FamilyBase) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	if _, _, running := b.runningChildSession(childKey); !running {
		return agent.ChildInputResult{StartsTurn: true}, errOpenCodeSubagentIdle
	}
	return agent.ChildInputResult{StartsTurn: true}, agent.ErrAgentBusy
}

// LoadedChildSteerTarget captures the running child's route and loaded settings.
// Submission validates the route again under the dispatch lease.
func (b *FamilyBase) LoadedChildSteerTarget(childKey string) (acp.ChildSessionTarget, acp.ChildSessionSettings, error) {
	child, running := b.childSession(childKey)
	if !running {
		return acp.ChildSessionTarget{}, acp.ChildSessionSettings{}, agent.ErrNoActiveTurn
	}
	if !child.loaded {
		return acp.ChildSessionTarget{}, acp.ChildSessionSettings{}, agent.ErrChildRouteNotReady
	}
	return child.target(childKey), child.settings, nil
}

// SteerChildInput sends a separate prompt to the loaded child session.
// The queue writes the steer's user row to the child transcript.
func (b *FamilyBase) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	target, _, err := b.LoadedChildSteerTarget(childKey)
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	owner, _ := b.SessionOwner(target.SessionID)
	err = b.SendChildPromptDetached(target, request.Content, request.Attachments, func(_ json.RawMessage, err error) {
		b.ReportChildSteerFailure(target, owner, err)
	})
	return agent.NativeInputReceipt{}, err
}

func (*FamilyBase) ChildSteerPolicy() agent.SteerPolicy { return agent.SteerPolicy{} }

// ActiveChildTurnState reports a running subagent's turn, which takes a steer
// once the agent loaded the subagent's session.
func (b *FamilyBase) ActiveChildTurnState(childKey string) agent.TurnState {
	_, loaded, running := b.runningChildSession(childKey)
	return agent.TurnState{Active: running, Steerable: running && loaded}
}

// ReportChildSteerFailure tells the reader, in the subagent's transcript, when
// the agent refused a steer. A stopped agent states nothing, as for the root.
func (b *FamilyBase) ReportChildSteerFailure(target acp.ChildSessionTarget, originalOwner agent.ProviderServices, err error) {
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
		if child, running := b.childSession(childKey); running && child.id != "" {
			return b.stopChild(ctx, childKey, child.id)
		}
		if b.hasActiveReusedTask(sessionID, childKey) {
			return b.stopChild(ctx, childKey, childKey)
		}
		if sink := b.Sink(); sink != nil {
			if identity, err := bgtask.ParseRowIdentity(childKey); err == nil {
				if resolved, found, err := sink.LookupChildIdentity(identity); err == nil && found && resolved.ProviderChildKey != "" {
					if _, status, ok, err := sink.LookupBackgroundTask(childKey); err == nil && ok {
						if status.IsFinished() {
							return nil
						}
						return b.stopChild(ctx, childKey, resolved.ProviderChildKey)
					}
				}
			}
		}
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
// lookup so rawInput.task_id revives an existing child row directly, and handles
// final-on-arrival task calls completely without leaking tracking state.
func (b *FamilyBase) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	rootSessionID := b.CurrentSessionID()
	knownTask := tc.Title == openCodeTaskTool && tc.Kind == "think"
	if !acp.StatusIsFinal(tc.Status) {
		return b.resolveSpawnObservation(tc.ToolCallID, tc.Title, tc.RawInput, knownTask, false)
	}
	input := parseOpenCodeTaskInput(tc.RawInput)
	if !knownTask && !input.argumentsReady {
		return nil
	}
	reusedTaskID := b.takeReusedTaskID(tc.ToolCallID, rootSessionID)
	if reusedTaskID == "" && input.argumentsReady && input.taskID != "" {
		if sink := b.Sink(); sink != nil {
			if childID, _, found, err := sink.LookupBackgroundTask(input.taskID); err == nil && found && childID != "" {
				reusedTaskID = input.taskID
			}
		}
	}
	b.markTaskCallFinalized(tc.ToolCallID, rootSessionID)
	title := openCodeTaskTitle(tc.Title, input)
	freshSpawn := input.argumentsReady && input.taskID == ""
	return b.finalizeTaskObservation(rootSessionID, tc.ToolCallID, title, tc.Status, tc.RawOutput, tc.Content, reusedTaskID, input.prompt, freshSpawn, true)
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
	reusedTaskID := b.takeReusedTaskID(tcu.ToolCallID, rootSessionID)
	b.markTaskCallFinalized(tcu.ToolCallID, rootSessionID)
	return b.finalizeTaskObservation(rootSessionID, tcu.ToolCallID, tcu.Title, tcu.Status, tcu.RawOutput, tcu.Content, reusedTaskID, "", false, false)
}

func (b *FamilyBase) finalizeTaskObservation(
	rootSessionID, callID, title, rawStatus string,
	rawOutput json.RawMessage,
	content []acp.ToolCallBlock,
	reusedTaskID, prompt string,
	freshSpawn, upsertIfMissing bool,
) *acp.SubagentObservation {
	closeMode := acp.ModeCloseOnly
	if upsertIfMissing {
		closeMode = acp.ModeUpsert
	}
	out := parseOpenCodeTaskOutput(rawOutput)

	rowKey := callID
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
			renameFrom = callID
			spawnSpanID = callID
		}
	case reusedTaskID != "":
		rowKey = reusedTaskID
		childKey = reusedTaskID
	case freshSpawn:
		spawnSpanID = callID
	}

	obs := &acp.SubagentObservation{
		RowKey:           rowKey,
		RenameFrom:       renameFrom,
		ChildAgentKey:    childKey,
		ChildSpawnSpanID: spawnSpanID,
		Title:            title,
		Prompt:           prompt,
		Spawns:           freshSpawn,
		ReportID:         callID,
	}
	status := acp.FinalStatus(rawStatus)
	if out.background && out.sessionID != "" && status == bgtask.StatusSucceeded {
		if earlyStatus, closedEarly := b.claimBackgroundChildLaunch(rootSessionID, callID, reusedTaskID, out.sessionID); closedEarly {
			if b.takeTaskChildStop(callID, reusedTaskID, out.sessionID) && earlyStatus != bgtask.StatusSucceeded {
				earlyStatus = bgtask.StatusInterrupted
			}
			obs.Status = earlyStatus
			obs.CloseRow = true
			obs.Mode = closeMode
			return obs
		}
		obs.Status = bgtask.StatusRunning
		obs.CloseRow = false
		obs.KeepOpen = true
		obs.ReviveRow = reusedTaskID != ""
		obs.Mode = acp.ModeUpsert
		return obs
	}

	b.finishForegroundChildTracking(callID, reusedTaskID, rowKey)
	if b.takeTaskChildStop(callID, reusedTaskID, rowKey) && status != bgtask.StatusSucceeded {
		status = bgtask.StatusInterrupted
	}
	obs.Status = status
	obs.CloseRow = true
	obs.Mode = closeMode
	obs.Report = agent.SubagentReport{
		Text: openCodeSubagentReport(acp.ToolCallText(content), out.background),
	}
	return obs
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
		if b.finishedForegroundSessions.Has(sessionID) {
			b.childSessionsMu.Unlock()
			return
		}
		prev, exists := b.earlyChildEvents.Get(sessionID)
		if !exists || (prev == bgtask.StatusSucceeded && status != bgtask.StatusSucceeded) {
			b.recordEarlyChildEventLocked(sessionID, status)
		}
		b.childSessionsMu.Unlock()
		return
	}
	rootSessionID := child.rootSessionID
	delete(b.childSessions, sessionID)
	b.earlyChildEvents.Delete(sessionID)
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
	b.finishedForegroundSessions.Delete(sessionID)
	if earlyStatus, ok := b.earlyChildEvents.Get(sessionID); ok {
		b.earlyChildEvents.Delete(sessionID)
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
		b.earlyChildEvents.Delete(reusedTaskID)
		b.recordFinishedForegroundSessionLocked(reusedTaskID)
	}
	if rowKey != "" {
		delete(b.childSessions, rowKey)
		b.earlyChildEvents.Delete(rowKey)
		b.recordFinishedForegroundSessionLocked(rowKey)
	}
}

func (b *FamilyBase) clearEarlyChildEvent(sessionID string) {
	if sessionID == "" {
		return
	}
	b.childSessionsMu.Lock()
	b.earlyChildEvents.Delete(sessionID)
	b.finishedForegroundSessions.Delete(sessionID)
	b.childSessionsMu.Unlock()
}

func setOpenCodeCappedLocked[V any](m *providerkit.CappedMap[string, V], key string, value V) {
	if m.Cap() == 0 {
		*m = providerkit.NewCappedMap[string, V](openCodeMaxTrackedEntries)
	}
	m.Set(key, value)
}

func (b *FamilyBase) recordEarlyChildEventLocked(sessionID string, status bgtask.Status) {
	setOpenCodeCappedLocked(&b.earlyChildEvents, sessionID, status)
}

func (b *FamilyBase) recordFinishedForegroundSessionLocked(sessionID string) {
	if sessionID == "" {
		return
	}
	setOpenCodeCappedLocked(&b.finishedForegroundSessions, sessionID, struct{}{})
}

func (b *FamilyBase) markTaskCallFinalized(callID, rootSessionID string) {
	if callID == "" {
		return
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	delete(b.reusedTaskCalls, callID)
	setOpenCodeCappedLocked(&b.finalizedTaskCalls, callID, rootSessionID)
}

func (b *FamilyBase) isTaskCallFinalized(callID, rootSessionID string) bool {
	if callID == "" {
		return false
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	owner, finalized := b.finalizedTaskCalls.Get(callID)
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

func (b *FamilyBase) takeReusedTaskID(callID, rootSessionID string) string {
	if callID == "" {
		return ""
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	reused, ok := b.reusedTaskCalls[callID]
	delete(b.reusedTaskCalls, callID)
	if !ok || (reused.rootSessionID != "" && reused.rootSessionID != rootSessionID) {
		return ""
	}
	return reused.taskID
}

func (b *FamilyBase) hasActiveReusedTask(rootSessionID, taskID string) bool {
	if taskID == "" {
		return false
	}
	b.childSessionsMu.Lock()
	defer b.childSessionsMu.Unlock()
	for _, reused := range b.reusedTaskCalls {
		if reused.taskID == taskID && (reused.rootSessionID == "" || reused.rootSessionID == rootSessionID) {
			return true
		}
	}
	return false
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

package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// copilotNativeChild is one native subagent and the transcript that holds it.
//
// The native stream states the owner of every event, so a child needs no file
// read to find its parent: `subagent.started` carries the spawning tool call,
// and this agent already knows which transcript that call opened.
type copilotNativeChild struct {
	sink            agent.ProviderServices
	owner           agent.ProviderServices
	ownerAgentID    string
	workerAgentID   string
	nativeAgentID   string
	spawnToolCallID string
	// background is true for a subagent that runs in the background. Copilot
	// keeps such a subagent after each turn, idle, for a later message; a
	// foreground subagent ends with its one turn.
	background bool
	// idle is true for a background subagent between its turns. Its row is
	// paused, and a message starts its next turn. outputMu guards it.
	idle bool
}

// copilotSubagentEvent is the part of every `subagent.*` event that LeapMux reads.
type copilotSubagentEvent struct {
	ToolCallID       string `json:"toolCallId"`
	AgentName        string `json:"agentName"`
	AgentDisplayName string `json:"agentDisplayName"`
	AgentDescription string `json:"agentDescription"`
	Cancelled        bool   `json:"cancelled"`
	// ExecutionMode is `background` for a subagent that runs in the
	// background, and `sync` for one whose parent waits for it.
	ExecutionMode string `json:"executionMode"`
}

// The execution mode of a background subagent, in `subagent.started`.
const copilotExecutionBackground = "background"

// copilotOptionExecutionMode is the child option that states the execution
// mode of a subagent, which decides whether its tab takes a message
// (ChildCapabilities).
const copilotOptionExecutionMode = "execution-mode"

// copilotTaskInput is the `task` tool's argument object.
type copilotTaskInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Mode        string `json:"mode"`
}

// sinkFor resolves the transcript that owns an event. A nil child is the root.
func (a *Agent) sinkFor(child *copilotNativeChild) agent.ProviderServices {
	if child == nil {
		return a.sink
	}
	return child.sink
}

// childForEvent resolves the subagent that emitted an event.
//
// An unknown agent ID answers nil, so the event reaches the root transcript
// rather than disappearing. That happens when a resumed session replays a
// subagent whose `subagent.started` this process never saw, and a visible row in
// the wrong transcript is recoverable where a dropped one is not.
func (a *Agent) childForEvent(event copilotEvent) *copilotNativeChild {
	if event.AgentID == "" {
		return nil
	}
	child := a.children[event.AgentID]
	if child == nil {
		slog.Debug("Copilot event identifies an unknown subagent", "agent_id", a.AgentID(), "native_agent_id", event.AgentID, "event", event.Type)
	}
	return child
}

// startNativeSubagent opens the child transcript for one spawned subagent.
//
// The spawning tool call states the owner, so a nested subagent reaches the
// transcript of the subagent that spawned it rather than the root.
func (a *Agent) startNativeSubagent(raw []byte, event copilotEvent) {
	var started copilotSubagentEvent
	if err := json.Unmarshal(event.Data, &started); err != nil || started.ToolCallID == "" || event.AgentID == "" {
		slog.Warn("Read Copilot subagent start", "agent_id", a.AgentID(), "error", err)
		a.persistNativeFrame(raw, agent.SpanInfo{})
		return
	}
	if existing := a.children[event.AgentID]; existing != nil {
		// A repeated start for a subagent that already runs. Opening a second
		// transcript would strand the first one's registry row as Running forever.
		a.persistNativeFrameTo(existing.owner, raw, agent.SpanInfo{})
		return
	}
	tool := a.openTools[started.ToolCallID]
	owner := a.sink
	ownerAgentID := a.AgentID()
	if tool != nil && tool.child != nil {
		owner, ownerAgentID = tool.child.sink, tool.child.workerAgentID
	}
	var input copilotTaskInput
	if tool != nil {
		if err := json.Unmarshal(tool.arguments, &input); err != nil {
			slog.Debug("Decode Copilot subagent arguments", "agent_id", a.AgentID(), "tool_call_id", started.ToolCallID, "error", err)
		}
	}
	title := sessionstore.FirstNonBlank(input.Name, started.AgentDisplayName, started.AgentName, input.Description, started.AgentDescription)
	background := started.ExecutionMode == copilotExecutionBackground
	workerAgentID, err := owner.EnsureChildAgent(agent.ChildAgentSpec{
		SpawnSpanID: started.ToolCallID, ProviderChildKey: event.AgentID, Title: title,
		Options: optionmap.Map{copilotOptionExecutionMode: started.ExecutionMode},
	})
	if err != nil {
		slog.Error("Open Copilot subagent transcript", "agent_id", a.AgentID(), "tool_call_id", started.ToolCallID, "error", err)
		a.persistNativeFrameTo(owner, raw, agent.SpanInfo{})
		return
	}
	child := &copilotNativeChild{
		sink: owner.ChildSink(workerAgentID), owner: owner, ownerAgentID: ownerAgentID,
		workerAgentID: workerAgentID, nativeAgentID: event.AgentID, spawnToolCallID: started.ToolCallID,
		background: background,
	}
	if a.children == nil {
		a.children = make(map[string]*copilotNativeChild)
	}
	a.children[event.AgentID] = child
	if err := owner.PersistChildPrompt(workerAgentID, input.Prompt); err != nil {
		slog.Warn("Persist Copilot subagent prompt", "agent_id", a.AgentID(), "child_agent_id", workerAgentID, "error", err)
	}
	providerkit.LogRegistryRefusal("copilot", "upsert subagent", owner.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: event.AgentID, Kind: bgtask.KindSubagent, ChildAgentID: workerAgentID,
		ParentAgentID: ownerAgentID, Title: title, Status: bgtask.StatusRunning,
	}))
	a.publishChildTurn(child, true)
	a.persistNativeFrameTo(owner, raw, agent.SpanInfo{})
}

// finishNativeSubagent closes one subagent's registry row and its transcript state.
func (a *Agent) finishNativeSubagent(raw []byte, event copilotEvent) {
	if event.AgentID != "" {
		a.resetNativeRetry(event.AgentID)
	}
	var finished copilotSubagentEvent
	decodeError := json.Unmarshal(event.Data, &finished)
	child := a.children[event.AgentID]
	if child == nil {
		child = a.childForSpawnToolCall(finished.ToolCallID)
	}
	if decodeError != nil {
		slog.Warn("Read Copilot subagent completion", "agent_id", a.AgentID(), "error", decodeError)
		a.persistNativeFrameTo(a.sinkFor(child), raw, agent.SpanInfo{})
		return
	}
	status := bgtask.StatusSucceeded
	switch {
	case finished.Cancelled:
		status = bgtask.StatusStopped
	case event.Type == contracts.CopilotEventSubagentFailed:
		status = bgtask.StatusFailed
	}
	if child == nil {
		a.persistNativeFrame(raw, agent.SpanInfo{})
		return
	}
	a.resetNativeRetry(child.nativeAgentID)
	if child.background && status == bgtask.StatusSucceeded {
		a.pauseNativeChild(raw, child)
		return
	}
	delete(a.children, child.nativeAgentID)
	a.persistNativeFrameTo(child.sink, raw, agent.SpanInfo{})
	a.publishChildTurn(child, false)
	providerkit.LogRegistryRefusal("copilot", "close subagent", child.owner.CloseBackgroundTask(child.nativeAgentID, status))
	child.sink.ReportProgress(agent.ResetProgress())
	child.sink.ResetSpans()
	child.owner.CleanupChildAgent(child.workerAgentID)
	for id, tool := range a.openTools {
		if tool.child == child {
			delete(a.openTools, id)
		}
	}
}

// pauseNativeChild keeps a background subagent whose turn completed. Copilot
// holds it idle for a later message, from the parent's `write_agent` or from
// its own tab, and a message starts its next turn in the same transcript. The
// row stays open and paused, so the subagent's later turns keep their route.
func (a *Agent) pauseNativeChild(raw []byte, child *copilotNativeChild) {
	a.persistNativeFrameTo(child.sink, raw, agent.SpanInfo{})
	a.markNativeChildIdle(child)
}

// markNativeChildIdle pauses the row of a running background subagent that
// finished a turn. It does nothing for a foreground subagent, an idle one, or
// an unknown one.
func (a *Agent) markNativeChildIdle(child *copilotNativeChild) {
	if child == nil || !child.background || child.idle {
		return
	}
	child.idle = true
	providerkit.LogRegistryRefusal("copilot", "pause subagent", child.owner.UpdateBackgroundTaskStatus(child.nativeAgentID, bgtask.StatusPaused, ""))
	a.publishChildTurn(child, false)
	child.sink.ReportProgress(agent.ResetProgress())
	child.sink.ResetSpans()
	for id, tool := range a.openTools {
		if tool.child == child {
			delete(a.openTools, id)
		}
	}
}

// resumeNativeChild reopens the row of an idle background subagent whose next
// turn starts.
func (a *Agent) resumeNativeChild(child *copilotNativeChild) {
	if child == nil || !child.idle {
		return
	}
	child.idle = false
	providerkit.LogRegistryRefusal("copilot", "resume subagent", child.owner.UpdateBackgroundTaskStatus(child.nativeAgentID, bgtask.StatusRunning, ""))
	a.publishChildTurn(child, true)
}

// publishChildTurn reports the turn of a subagent on its own tab, so the
// tab's input queue holds a message while a turn runs and delivers it once the
// subagent waits. A running background subagent takes a steer: Copilot queues
// the message for the turn that follows the running one.
func (a *Agent) publishChildTurn(child *copilotNativeChild, active bool) {
	a.stateMu.Lock()
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(child.sink, agent.TurnState{Active: active, Steerable: active && child.background}, sequence)
}

func (a *Agent) childForSpawnToolCall(toolCallID string) *copilotNativeChild {
	if toolCallID == "" {
		return nil
	}
	for _, child := range a.children {
		if child.spawnToolCallID == toolCallID {
			return child
		}
	}
	return nil
}

// clearNativeChildren ends every open subagent transcript.
//
// A session replacement and a process stop both reach it, so each child leaves
// its registry row stopped rather than running forever.
func (a *Agent) clearNativeChildren() {
	for nativeAgentID := range a.retries {
		a.resetNativeRetry(nativeAgentID)
	}
	// A session that goes away takes every unfinished segment with it, so the text
	// each transcript had already streamed is stored before its sink is dropped.
	a.closeStreamedNativeText(agent.MessageCompletionInterrupted)
	a.closeOpenNativeTools()
	children := a.children
	a.children = nil
	for _, child := range children {
		a.publishChildTurn(child, false)
		providerkit.LogRegistryRefusal("copilot", "close subagent", child.owner.CloseBackgroundTask(child.nativeAgentID, bgtask.StatusStopped))
		child.sink.ReportProgress(agent.ResetProgress())
		child.sink.ResetSpans()
		child.owner.CleanupChildAgent(child.workerAgentID)
	}
}

var _ agent.ChildInterrupter = (*Agent)(nil)
var _ agent.ChildSteerer = (*Agent)(nil)

// errCopilotSubagentEnded refuses a message to a subagent that ended. A turn
// that the message started would reach no one.
var errCopilotSubagentEnded = errors.New("the subagent ended, so it takes no message")

// errCopilotForegroundSubagent refuses a message to a foreground subagent.
// Copilot accepts the message, but a foreground subagent never reads it: it
// ends with its one turn, and only a background subagent reads its messages.
var errCopilotForegroundSubagent = errors.New("copilot passes a message only to a subagent that runs in the background")

// copilotTaskList is the part of a `session.tasks.list` answer that LeapMux reads.
type copilotTaskList struct {
	Tasks []struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		ToolCallID string `json:"toolCallId"`
	} `json:"tasks"`
}

// copilotTaskTypeAgent is the type of a subagent's task.
const copilotTaskTypeAgent = "agent"

// copilotChildState is the state of one subagent that a child operation reads.
type copilotChildState struct {
	spawnToolCallID string
	background      bool
	idle            bool
}

// childState returns the state of the subagent whose native agent ID is
// childKey, and false for a subagent that ended.
func (a *Agent) childState(childKey string) (copilotChildState, bool) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	child := a.children[childKey]
	if child == nil {
		return copilotChildState{}, false
	}
	return copilotChildState{spawnToolCallID: child.spawnToolCallID, background: child.background, idle: child.idle}, true
}

// childTaskID finds the task of the subagent that the tool call spawnToolCallID
// started. Copilot tracks each subagent as a task of the session, and the task
// states its spawning tool call. A task's own ID is the subagent's agent ID or
// that tool call, depending on how Copilot learned of the task, so the lookup
// reads the list rather than guessing. The caller holds sessionMu.
func (a *Agent) childTaskID(spawnToolCallID string) (string, error) {
	raw, err := a.requestNativeSession("tasks.list", nil)
	if err != nil {
		return "", fmt.Errorf("list the Copilot tasks: %w", err)
	}
	var list copilotTaskList
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", fmt.Errorf("read the Copilot task list: %w", err)
	}
	for _, task := range list.Tasks {
		if task.Type == copilotTaskTypeAgent && task.ToolCallID == spawnToolCallID && task.ID != "" {
			return task.ID, nil
		}
	}
	return "", agent.ErrChildRouteNotReady
}

// InterruptChild stops the running turn of one subagent with Copilot's task
// cancel, and leaves the parent's turn running. Copilot then ends the subagent
// with a cancelled completion, which closes its row as stopped.
func (a *Agent) InterruptChild(childKey string, _ agent.StopContext) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	state, known := a.childState(childKey)
	if !known || state.idle {
		// No turn of the subagent runs, so nothing remains to stop.
		return nil
	}
	taskID, err := a.childTaskID(state.spawnToolCallID)
	if err != nil {
		return err
	}
	raw, err := a.requestNativeSession("tasks.cancel", map[string]any{"id": taskID})
	if err != nil {
		return fmt.Errorf("cancel the Copilot subagent: %w", err)
	}
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("read the Copilot task cancel: %w", err)
	}
	if !result.Cancelled {
		if now, still := a.childState(childKey); still && !now.idle {
			return fmt.Errorf("copilot did not cancel the subagent task %s", taskID)
		}
	}
	return nil
}

// SendChildInput sends a message to an idle background subagent, which reads
// it in its next turn. A running background subagent answers busy, and the
// queue steers the message (SteerChildInput). A foreground subagent and an
// ended one take no message.
func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	state, known := a.childState(childKey)
	switch {
	case !known:
		return errCopilotSubagentEnded
	case !state.background:
		return errCopilotForegroundSubagent
	case !state.idle:
		return agent.ErrAgentBusy
	}
	return a.sendChildMessage(state, content, attachments)
}

// SteerChildInput sends a message to a running background subagent. Copilot
// queues it, and the subagent reads it in the turn that follows its running
// one, before it goes idle.
func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	state, known := a.childState(childKey)
	switch {
	case !known || state.idle:
		return agent.ErrNoActiveTurn
	case !state.background:
		return errCopilotForegroundSubagent
	}
	return a.sendChildMessage(state, content, attachments)
}

// ActiveChildTurnState reports the running turn of a subagent. Only a
// background subagent takes a message during it.
func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	state, known := a.childState(childKey)
	running := known && !state.idle
	return agent.TurnState{Active: running, Steerable: running && state.background}
}

// sendChildMessage sends a message to the subagent of state with Copilot's
// task message. Copilot's task message carries text alone, so an attachment is
// refused. The queue writes the message into the subagent's transcript, and
// Copilot's own echo of it is not stored (CopilotEventUserMessage), so it shows
// once.
func (a *Agent) sendChildMessage(state copilotChildState, content string, attachments []*leapmuxv1.Attachment) error {
	if len(attachments) > 0 {
		return fmt.Errorf("copilot sends a subagent text alone, not an attachment")
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	taskID, err := a.childTaskID(state.spawnToolCallID)
	if err != nil {
		return err
	}
	raw, err := a.requestNativeSession("tasks.sendMessage", map[string]any{"id": taskID, "message": content})
	if err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError("send the message to the Copilot subagent", err)
	}
	var result struct {
		Sent  *bool  `json:"sent"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("%w: read the Copilot task message: %w", agent.ErrDeliveryUncertain, err)
	}
	if result.Sent == nil {
		return fmt.Errorf("%w: read the Copilot task message: Copilot omitted the delivery receipt", agent.ErrDeliveryUncertain)
	}
	if !*result.Sent {
		reason := result.Error
		if reason == "" {
			reason = "Copilot did not deliver it"
		}
		return fmt.Errorf("the message did not reach the Copilot subagent: %s", reason)
	}
	return nil
}

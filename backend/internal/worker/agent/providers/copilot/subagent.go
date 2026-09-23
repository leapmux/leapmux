package copilot

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
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
}

// copilotSubagentEvent is the part of every `subagent.*` event that LeapMux reads.
type copilotSubagentEvent struct {
	ToolCallID       string `json:"toolCallId"`
	AgentName        string `json:"agentName"`
	AgentDisplayName string `json:"agentDisplayName"`
	AgentDescription string `json:"agentDescription"`
	Cancelled        bool   `json:"cancelled"`
}

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
	workerAgentID, err := owner.EnsureChildAgent(started.ToolCallID, event.AgentID, title)
	if err != nil {
		slog.Error("Open Copilot subagent transcript", "agent_id", a.AgentID(), "tool_call_id", started.ToolCallID, "error", err)
		a.persistNativeFrameTo(owner, raw, agent.SpanInfo{})
		return
	}
	child := &copilotNativeChild{
		sink: owner.ChildSink(workerAgentID), owner: owner, ownerAgentID: ownerAgentID,
		workerAgentID: workerAgentID, nativeAgentID: event.AgentID, spawnToolCallID: started.ToolCallID,
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
	a.persistNativeFrameTo(owner, raw, agent.SpanInfo{})
}

// finishNativeSubagent closes one subagent's registry row and its transcript state.
func (a *Agent) finishNativeSubagent(raw []byte, event copilotEvent) {
	var finished copilotSubagentEvent
	if err := json.Unmarshal(event.Data, &finished); err != nil {
		slog.Warn("Read Copilot subagent completion", "agent_id", a.AgentID(), "error", err)
	}
	child := a.children[event.AgentID]
	if child == nil {
		child = a.childForSpawnToolCall(finished.ToolCallID)
	}
	status := bgtask.StatusCompleted
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
	delete(a.children, child.nativeAgentID)
	a.persistNativeFrameTo(child.sink, raw, agent.SpanInfo{})
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
	// A session that goes away takes every unfinished segment with it, so the text
	// each transcript had already streamed is stored before its sink is dropped.
	a.closeStreamedNativeText(agent.MessageCompletionInterrupted)
	a.closeOpenNativeTools()
	children := a.children
	a.children = nil
	for _, child := range children {
		providerkit.LogRegistryRefusal("copilot", "close subagent", child.owner.CloseBackgroundTask(child.nativeAgentID, bgtask.StatusStopped))
		child.sink.ReportProgress(agent.ResetProgress())
		child.sink.ResetSpans()
		child.owner.CleanupChildAgent(child.workerAgentID)
	}
}

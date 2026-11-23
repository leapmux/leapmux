package droid

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// droidChildSpawn keeps the Task input until Droid announces the child session.
// The announcement gives a stable session id but does not repeat the prompt.
type droidChildSpawn struct {
	description string
	prompt      string
	resume      string
}

type droidChildAnnouncementID struct {
	parentSessionID string
	toolUseID       string
}

// rememberTaskSpawn saves only the Task fields needed for the child transcript.
func (a *Agent) rememberTaskSpawn(sessionID string, tool droidToolUse) {
	if tool.Name != "Task" || tool.ID == "" {
		return
	}
	var input struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Resume      string `json:"resume"`
	}
	if err := json.Unmarshal(tool.Input, &input); err != nil {
		return
	}
	if a.childSpawns == nil {
		a.childSpawns = make(map[string]droidChildSpawn)
	}
	a.childSpawns[droidSpawnKey(sessionID, tool.ID)] = droidChildSpawn{
		description: input.Description,
		prompt:      input.Prompt,
		resume:      strings.TrimSpace(input.Resume),
	}
}

func droidSpawnKey(sessionID, toolUseID string) string {
	return sessionID + "\x00" + toolUseID
}

// registerChildSession links the native session, its Task span, and a child tab.
// Droid can replay the announcement. A replay keeps the first link and tail.
// When Task explicitly sets resume to the same child session id, it revives the
// existing child row and returns resumed=true so the caller resumes tailing from EOF.
func (a *Agent) registerChildSession(parentSessionID string, payload []byte) (string, bool) {
	var n struct {
		ChildSessionID string `json:"childSessionId"`
		ToolUseID      string `json:"toolUseId"`
		SubagentType   string `json:"subagentType"`
		Description    string `json:"description"`
	}
	if err := json.Unmarshal(payload, &n); err != nil || n.ChildSessionID == "" {
		return "", false
	}
	parent, ok := a.outputTargetFor(parentSessionID)
	if !ok {
		return "", false
	}
	identity := droidChildAnnouncementID{parentSessionID: parentSessionID, toolUseID: n.ToolUseID}
	spawnKey := droidSpawnKey(parentSessionID, n.ToolUseID)
	spawn := a.childSpawns[spawnKey]
	if prior, exists := a.childAnnouncements[n.ChildSessionID]; exists && prior == identity {
		return "", false
	}
	if spawn.resume != "" && spawn.resume == n.ChildSessionID {
		childID := a.childAgents[n.ChildSessionID]
		if childID == "" {
			lookedUp, _, found, err := a.sink.LookupBackgroundTask(n.ChildSessionID)
			if err != nil {
				slog.Warn("droid: look up resumed child", "session_id", n.ChildSessionID, "error", err)
				return "", false
			}
			if found {
				childID = lookedUp
			}
		}
		if childID != "" {
			if strings.TrimSpace(spawn.prompt) != "" {
				if err := parent.sink.PersistChildUserMessage(childID, spawn.prompt); err != nil {
					slog.Warn("droid: persist resumed child prompt", "session_id", n.ChildSessionID, "error", err)
					return "", false
				}
			}
			if err := a.sink.ReviveBackgroundTask(n.ChildSessionID); err != nil {
				providerkit.LogRegistryRefusal("droid", "revive resumed child", err)
				return "", false
			}
			if a.childAgents == nil {
				a.childAgents = make(map[string]string)
			}
			if a.childAnnouncements == nil {
				a.childAnnouncements = make(map[string]droidChildAnnouncementID)
			}
			a.childAgents[n.ChildSessionID] = childID
			a.childAnnouncements[n.ChildSessionID] = identity
			if state := a.outputStates[n.ChildSessionID]; state != nil {
				state.ended = false
			}
			delete(a.childSpawns, spawnKey)
			a.armChildTurn(n.ChildSessionID)
			return n.ChildSessionID, true
		}
	}
	if _, exists := a.childAnnouncements[n.ChildSessionID]; exists {
		slog.Warn("droid: child announcement changed identity", "session_id", n.ChildSessionID)
		return "", false
	}
	if a.childAgents[n.ChildSessionID] != "" {
		slog.Warn("droid: child announcement has no stored identity", "session_id", n.ChildSessionID)
		return "", false
	}
	title := strings.TrimSpace(n.Description)
	if title == "" {
		title = strings.TrimSpace(spawn.description)
	}
	if title == "" {
		title = strings.TrimSpace(n.SubagentType)
	}
	spawnSpanID := "droid-child-" + n.ChildSessionID
	if n.ToolUseID != "" {
		spawnSpanID = "droid-tool-" + n.ToolUseID
	}
	childID, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  spawnSpanID, ProviderChildKey: n.ChildSessionID, Title: title})
	if err != nil {
		slog.Warn("droid: ensure child", "session_id", n.ChildSessionID, "error", err)
		return "", false
	}
	if err := parent.sink.PersistChildPrompt(childID, spawn.prompt); err != nil {
		slog.Warn("droid: persist child prompt", "session_id", n.ChildSessionID, "error", err)
		return "", false
	}
	parentAgentID := a.AgentID()
	if parent.childSessionID != "" {
		parentAgentID = a.childAgents[parent.childSessionID]
	}
	if err := parent.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: n.ChildSessionID, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: parentAgentID, Title: title, Status: bgtask.StatusRunning,
	}); err != nil {
		providerkit.LogRegistryRefusal("droid", "register child", err)
		return "", false
	}
	if a.childAgents == nil {
		a.childAgents = make(map[string]string)
	}
	if a.childAnnouncements == nil {
		a.childAnnouncements = make(map[string]droidChildAnnouncementID)
	}
	a.childAgents[n.ChildSessionID] = childID
	a.childAnnouncements[n.ChildSessionID] = identity
	delete(a.childSpawns, spawnKey)
	a.armChildTurn(n.ChildSessionID)
	return n.ChildSessionID, false
}

// outputTargetFor resolves a session to its own transcript and span state.
// An unknown session cannot write into the root transcript.
func (a *Agent) outputTargetFor(sessionID string) (droidOutputTarget, bool) {
	mainID := a.mainSessionID()
	if sessionID == "" || sessionID == mainID {
		sink := a.orderedSink()
		state := a.outputState(mainID)
		state.outputQueue = sink.OutputQueue()
		return droidOutputTarget{sink: sink, state: state}, true
	}
	childID := a.childAgents[sessionID]
	if childID == "" {
		return droidOutputTarget{}, false
	}
	state := a.outputState(sessionID)
	sink := a.sink.ChildSink(childID)
	queue := agent.OutputQueueFor(sink)
	if queue == nil {
		queue = state.outputQueue
		if queue == nil {
			queue = &agent.OutputQueue{}
		}
	}
	state.outputQueue = queue
	return droidOutputTarget{sink: agent.NewOrderedProviderServices(queue, sink), state: state, childSessionID: sessionID}, true
}

// outputState keeps tool and text spans separate for each native session.
func (a *Agent) outputState(sessionID string) *droidOutputState {
	if a.outputStates == nil {
		a.outputStates = make(map[string]*droidOutputState)
	}
	state := a.outputStates[sessionID]
	if state == nil {
		state = &droidOutputState{}
		a.outputStates[sessionID] = state
	}
	return state
}

// closeChildSession finishes only the child row and its in-memory output state.
func (a *Agent) closeChildSession(sessionID string, status bgtask.Status) {
	if sessionID == "" || a.childAgents[sessionID] == "" {
		return
	}
	if target, ok := a.outputTargetFor(sessionID); ok {
		a.closeOpenTools(target, providerkit.ChildCompletion(status), true)
		a.flushPendingOutput(target)
	}
	providerkit.LogRegistryRefusal("droid", "finish child", a.sink.CloseBackgroundTask(sessionID, status))
	if state := a.outputStates[sessionID]; state != nil {
		state.ended = true
		state.tools = nil
		if !state.hasPendingOutput() && !state.turnErrorSeen && !state.compactionLive {
			delete(a.outputStates, sessionID)
		}
	}
	a.disarmChildTurn(sessionID)
}

// droidOutputTarget holds the sink chosen from the native session id.
type droidOutputTarget struct {
	sink           agent.ProviderServices
	state          *droidOutputState
	childSessionID string
}

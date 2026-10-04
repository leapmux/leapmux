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
	}
	if err := json.Unmarshal(tool.Input, &input); err != nil {
		return
	}
	if a.childSpawns == nil {
		a.childSpawns = make(map[string]droidChildSpawn)
	}
	a.childSpawns[droidSpawnKey(sessionID, tool.ID)] = droidChildSpawn{description: input.Description, prompt: input.Prompt}
}

func droidSpawnKey(sessionID, toolUseID string) string {
	return sessionID + "\x00" + toolUseID
}

// registerChildSession links the native session, its Task span, and a child tab.
// Droid can replay the announcement. A replay keeps the first link and tail.
func (a *Agent) registerChildSession(parentSessionID string, payload []byte) string {
	var n struct {
		ChildSessionID string `json:"childSessionId"`
		ToolUseID      string `json:"toolUseId"`
		SubagentType   string `json:"subagentType"`
		Description    string `json:"description"`
	}
	if err := json.Unmarshal(payload, &n); err != nil || n.ChildSessionID == "" {
		return ""
	}
	parent, ok := a.outputTargetFor(parentSessionID)
	if !ok {
		return ""
	}
	identity := droidChildAnnouncementID{parentSessionID: parentSessionID, toolUseID: n.ToolUseID}
	if prior, exists := a.childAnnouncements[n.ChildSessionID]; exists {
		if prior != identity {
			slog.Warn("droid: child announcement changed identity", "session_id", n.ChildSessionID)
		}
		return ""
	}
	if a.childAgents[n.ChildSessionID] != "" {
		slog.Warn("droid: child announcement has no stored identity", "session_id", n.ChildSessionID)
		return ""
	}
	spawnKey := droidSpawnKey(parentSessionID, n.ToolUseID)
	spawn := a.childSpawns[spawnKey]
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
	childID, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnSpanID, ProviderChildKey: n.ChildSessionID, Title: title})
	if err != nil {
		slog.Warn("droid: ensure child", "session_id", n.ChildSessionID, "error", err)
		return ""
	}
	if err := parent.sink.PersistChildPrompt(childID, spawn.prompt); err != nil {
		slog.Warn("droid: persist child prompt", "session_id", n.ChildSessionID, "error", err)
		return ""
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
		return ""
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
	return n.ChildSessionID
}

// outputTargetFor resolves a session to its own transcript and span state.
// An unknown session cannot write into the root transcript.
func (a *Agent) outputTargetFor(sessionID string) (droidOutputTarget, bool) {
	mainID := a.mainSessionID()
	if sessionID == "" || sessionID == mainID {
		return droidOutputTarget{sink: a.sink, state: a.outputState(mainID)}, true
	}
	childID := a.childAgents[sessionID]
	if childID == "" {
		return droidOutputTarget{}, false
	}
	return droidOutputTarget{sink: a.sink.ChildSink(childID), state: a.outputState(sessionID), childSessionID: sessionID}, true
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
	providerkit.LogRegistryRefusal("droid", "finish child", a.sink.CloseBackgroundTask(sessionID, status))
	delete(a.outputStates, sessionID)
	a.disarmChildTurn(sessionID)
}

// droidOutputTarget holds the sink chosen from the native session id.
type droidOutputTarget struct {
	sink           agent.ProviderServices
	state          *droidOutputState
	childSessionID string
}

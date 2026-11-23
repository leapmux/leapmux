package grok

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Grok runs each subagent as an ACP session of its own. The child's updates
// stream under the child's session id, and the parent learns of the child from
// `subagent_spawned` and `subagent_finished` notifications on its own session.
//
// No notification states the tool call that spawned a child, so the link is
// rebuilt from what each side states:
//
//   - A BLOCKING spawn is still running when `subagent_spawned` arrives. The
//     notification repeats the spawn's description, so the oldest running
//     spawn with that description is the one.
//   - A BACKGROUND spawn completes at once, and its result text states the
//     `subagent_id` -- which is also the child session id -- BEFORE
//     `subagent_spawned` arrives.
//
// A child that no tool call spawned (the goal planner, an agent of a workflow)
// takes a registry row of its own, keyed by its subagent id.

// grokSubagentCancelMethod stops one subagent. session/cancel on a child
// session does nothing, because Grok keeps no child in its session registry.
const grokSubagentCancelMethod = "_x.ai/subagent/cancel"

const grokSubagentMessageMethod = "_x.ai/subagent/message"

// grokBackgroundSpawnID reads the subagent id from the text a background spawn
// completes with ("Subagent started in background.\nsubagent_id: <id>...").
var grokBackgroundSpawnID = regexp.MustCompile(`(?m)^subagent_id:\s*(\S+)\s*$`)

// grokWorkflowRowPrefix keys the registry row of one workflow run.
const grokWorkflowRowPrefix = "workflow:"

// grokTaskRowPrefix keys the registry row of one background shell command.
const grokTaskRowPrefix = "task:"

// Additional Grok spawn tool names observed across builds.
const (
	grokToolTask      = "task"
	grokToolTaskTitle = "Task"
	grokToolAgent     = "Agent"
)

func isGrokSpawnTool(name string) bool {
	switch name {
	case contracts.GrokToolSpawnSubagent, grokToolTask, grokToolTaskTitle, grokToolAgent:
		return true
	default:
		return false
	}
}

// pendingSpawn is one spawn tool call that still waits for its child session.
type pendingSpawn struct {
	toolCallID   string
	description  string
	subagentType string
}

// grokEarlyFinish buffers a subagent_finished notification that arrived before
// the matching background tool_call_update or subagent_spawned frame.
type grokEarlyFinish struct {
	status bgtask.Status
	report string
}

// childState is what links Grok's child sessions to LeapMux's registry rows.
// Guarded by Agent.stateMu.
type childState struct {
	// pending holds the blocking spawns that still wait for their child, in
	// the order they started.
	pending []pendingSpawn
	// spawnTools records every spawn tool call, so the agent recognizes the
	// closing update of a spawn, which no longer states the tool's identity.
	spawnTools map[string]bool
	// backgroundSpawns maps the subagent id a background spawn announced to
	// the tool call that spawned it.
	backgroundSpawns map[string]string
	// earlyFinished buffers subagent_finished outcomes for subagents not yet
	// linked through backgroundSpawns or subagent_spawned.
	earlyFinished map[string]grokEarlyFinish
	// finishedRowKeys retains subagent_id -> rowKey after completion so a later
	// wake revives the original registry row instead of creating a duplicate.
	finishedRowKeys map[string]string
	// settledBeforeSpawn marks subagent IDs settled via earlyFinished during a
	// background tool_call_update before their initial subagent_spawned arrived.
	settledBeforeSpawn map[string]bool
	// subagents maps the id of each subagent that runs to its registry row and
	// its child session. subagentByRow and subagentBySession map back to the
	// id: a stop reaches Grok under the id, and the end of a turn in the child
	// session reaches the row. Grok states the child session beside the id,
	// and the two can differ.
	subagents         map[string]linkedSubagent
	subagentByRow     map[string]string
	subagentBySession map[string]string
	// workflows maps a workflow run id to its name, the label of its group.
	workflows map[string]string
	// finishedWorkflows tracks workflow run IDs whose row reached a finished
	// status so a subsequent transition back to working revives the row.
	finishedWorkflows map[string]bool
	// interrupted records the subagents this process stopped through
	// InterruptChild. A cancel we asked for is a USER interrupt, so the closing
	// row reports StatusInterrupted and the divider reads "Subagent
	// interrupted"; a model-side cancel keeps the plain stop word.
	interrupted map[string]bool
	// runSeq counts runs per subagent ID so woken runs write distinct report IDs
	// instead of colliding with earlier runs.
	runSeq map[string]int
}

// linkedSubagent is the registry row and the child session of one subagent.
type linkedSubagent struct {
	rootSessionID string
	rowKey        string
	childSession  string
	agentAddress  string
}

// reset drops every link, when a context clear replaces the session.
func (c *childState) reset() {
	*c = childState{}
}

// link records a subagent that runs. A second link of the same id replaces
// the first one whole, so no reverse entry of the first one stays.
func (c *childState) link(subagentID string, linked linkedSubagent) {
	c.unlink(subagentID)
	if c.subagents == nil {
		c.subagents = make(map[string]linkedSubagent)
		c.subagentByRow = make(map[string]string)
		c.subagentBySession = make(map[string]string)
	}
	c.subagents[subagentID] = linked
	c.subagentByRow[linked.rowKey] = subagentID
	c.subagentBySession[linked.childSession] = subagentID
}

// unlink forgets a subagent that ended, and returns its row and its child
// session.
func (c *childState) unlink(subagentID string) (linkedSubagent, bool) {
	linked, ok := c.subagents[subagentID]
	if !ok {
		return linkedSubagent{}, false
	}
	delete(c.subagents, subagentID)
	delete(c.subagentByRow, linked.rowKey)
	delete(c.subagentBySession, linked.childSession)
	return linked, true
}

// markInterrupted records that InterruptChild stopped this subagent. Caller
// must hold stateMu.
func (c *childState) markInterrupted(subagentID string) {
	if c.interrupted == nil {
		c.interrupted = make(map[string]bool)
	}
	c.interrupted[subagentID] = true
}

// takeInterrupted reports whether InterruptChild stopped this subagent, and
// spends the mark so only one closer uses it. Caller must hold stateMu.
func (c *childState) takeInterrupted(subagentID string) bool {
	if !c.interrupted[subagentID] {
		return false
	}
	delete(c.interrupted, subagentID)
	return true
}

// rememberFinishedRowKeyLocked retains the subagent's registry row key for
// subsequent wake reuse. Caller must hold stateMu.
func (c *childState) rememberFinishedRowKeyLocked(subagentID, rowKey string) {
	if subagentID == "" || rowKey == "" {
		return
	}
	if c.finishedRowKeys == nil {
		c.finishedRowKeys = make(map[string]string)
	}
	c.finishedRowKeys[subagentID] = rowKey
}

// bumpRunSeqLocked advances a subagent's run counter when a woken run revives
// its row. Caller must hold stateMu.
func (c *childState) bumpRunSeqLocked(subagentID string) {
	if subagentID == "" {
		return
	}
	if c.runSeq == nil {
		c.runSeq = make(map[string]int)
	}
	if c.runSeq[subagentID] < 1 {
		c.runSeq[subagentID] = 2
		return
	}
	c.runSeq[subagentID]++
}

// reportIDLocked returns the per-run report ID for one subagent.
// Job 1 uses the bare subagent ID; woken runs append "#<run>".
// Caller must hold stateMu.
func (c *childState) reportIDLocked(subagentID string) string {
	if run := c.runSeq[subagentID]; run >= 2 {
		return fmt.Sprintf("%s#%d", subagentID, run)
	}
	return subagentID
}

// grokToolMeta is the part of a tool call's `_meta["x.ai/tool"]` that LeapMux reads.
type grokToolMeta struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// grokToolOf reads a tool call's own identity out of its `_meta`.
func grokToolOf(meta json.RawMessage) (grokToolMeta, bool) {
	var fields map[string]json.RawMessage
	if len(meta) == 0 || json.Unmarshal(meta, &fields) != nil {
		return grokToolMeta{}, false
	}
	var tool grokToolMeta
	if json.Unmarshal(fields[contracts.GrokMetaTool], &tool) != nil || tool.Name == "" {
		return grokToolMeta{}, false
	}
	return tool, true
}

// grokSpawnInput is the part of a spawn's arguments that LeapMux reads: the
// first tool_call carries the model's own arguments. Whether the child runs in
// the background is read from the spawn's result, not from these arguments.
type grokSpawnInput struct {
	Prompt       string `json:"prompt"`
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
}

// subagentFromToolCall claims a spawn at its first frame. The row and the child
// transcript open at once, with the prompt, so the child tab opens on the
// prompt before the child says anything.
func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	tool, ok := grokToolOf(tc.Meta)
	if !ok || !isGrokSpawnTool(tool.Name) {
		return nil
	}
	var input grokSpawnInput
	_ = json.Unmarshal(tc.RawInput, &input)
	desc := strings.TrimSpace(input.Description)
	subagentType := strings.TrimSpace(input.SubagentType)
	title := desc
	if title == "" {
		title = subagentType
	}
	if title == "" {
		title = "Grok subagent"
	}
	if acp.StatusIsFinal(tc.Status) {
		return &acp.SubagentObservation{
			RowKey:        tc.ToolCallID,
			ChildAgentKey: tc.ToolCallID,
			Title:         title,
			Prompt:        input.Prompt,
			Status:        acp.FinalStatus(tc.Status),
			CloseRow:      true,
			Spawns:        true,
		}
	}
	a.stateMu.Lock()
	if a.children.spawnTools == nil {
		a.children.spawnTools = make(map[string]bool)
	}
	a.children.spawnTools[tc.ToolCallID] = true
	a.children.pending = append(a.children.pending, pendingSpawn{
		toolCallID:   tc.ToolCallID,
		description:  desc,
		subagentType: subagentType,
	})
	a.stateMu.Unlock()
	return &acp.SubagentObservation{
		RowKey:        tc.ToolCallID,
		ChildAgentKey: tc.ToolCallID,
		Title:         title,
		Prompt:        input.Prompt,
		Status:        bgtask.StatusRunning,
		Spawns:        true,
	}
}

// subagentFromToolCallUpdate reads the end of a spawn tool call.
//
// A blocking spawn ends with the child's result, so its row closes with the
// child's report. A background spawn ends at once with the id of the child
// that keeps running, so its row stays open until `subagent_finished`.
func (a *Agent) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		return nil
	}
	a.stateMu.Lock()
	spawn := a.children.spawnTools[tcu.ToolCallID]
	delete(a.children.spawnTools, tcu.ToolCallID)
	a.stateMu.Unlock()
	if !spawn {
		return nil
	}
	var output struct {
		Type       string `json:"type"`
		Output     string `json:"output"`
		SubagentID string `json:"subagent_id"`
		Text       string `json:"text"`
	}
	_ = json.Unmarshal(tcu.RawOutput, &output)
	if tcu.Status == "completed" && output.Type != "SubagentCompleted" {
		text := output.Text
		if text == "" {
			text = acp.ToolCallText(tcu.Content)
		}
		if match := grokBackgroundSpawnID.FindStringSubmatch(text); match != nil {
			subagentID := match[1]
			a.stateMu.Lock()
			a.dropPendingLocked(tcu.ToolCallID)
			if early, hasEarly := a.children.earlyFinished[subagentID]; hasEarly {
				delete(a.children.earlyFinished, subagentID)
				a.children.rememberFinishedRowKeyLocked(subagentID, tcu.ToolCallID)
				if a.children.settledBeforeSpawn == nil {
					a.children.settledBeforeSpawn = make(map[string]bool)
				}
				a.children.settledBeforeSpawn[subagentID] = true
				reportID := a.children.reportIDLocked(subagentID)
				a.stateMu.Unlock()
				return &acp.SubagentObservation{
					RowKey:   tcu.ToolCallID,
					Status:   early.status,
					CloseRow: true,
					Mode:     acp.ModeCloseOnly,
					ReportID: reportID,
					Report:   agent.SubagentReport{Text: early.report},
				}
			}
			if a.children.backgroundSpawns == nil {
				a.children.backgroundSpawns = make(map[string]string)
			}
			a.children.backgroundSpawns[subagentID] = tcu.ToolCallID
			a.stateMu.Unlock()
			return nil
		}
	}
	a.stateMu.Lock()
	a.dropPendingLocked(tcu.ToolCallID)
	reportID := ""
	if output.SubagentID != "" {
		reportID = a.children.reportIDLocked(output.SubagentID)
	}
	a.stateMu.Unlock()
	report := output.Output
	if report == "" && tcu.Status == "completed" {
		report = acp.ToolCallText(tcu.Content)
	}
	if reportID == "" {
		reportID = tcu.ToolCallID
	}
	return &acp.SubagentObservation{
		RowKey:   tcu.ToolCallID,
		Status:   acp.FinalStatus(tcu.Status),
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
		ReportID: reportID,
		Report:   agent.SubagentReport{Text: report},
	}
}

// dropPendingLocked forgets a spawn that no longer waits for its child. The
// caller holds stateMu.
func (a *Agent) dropPendingLocked(toolCallID string) {
	for i, spawn := range a.children.pending {
		if spawn.toolCallID == toolCallID {
			a.children.pending = append(a.children.pending[:i], a.children.pending[i+1:]...)
			return
		}
	}
}

// grokSubagentSpawned is the part of `subagent_spawned` that LeapMux reads.
type grokSubagentSpawned struct {
	SubagentID     string `json:"subagent_id"`
	ChildSessionID string `json:"child_session_id"`
	SubagentType   string `json:"subagent_type"`
	Description    string `json:"description"`
	WorkflowRunID  string `json:"workflow_run_id"`
	AgentAddress   string `json:"agentAddress"`
	WakeOrigin     string `json:"wake_origin"`
}

func (a *Agent) takePendingSpawnLocked(rawDesc, subagentType string) (string, bool) {
	if rawDesc != "" {
		for i, spawn := range a.children.pending {
			if spawn.description == rawDesc {
				a.children.pending = append(a.children.pending[:i], a.children.pending[i+1:]...)
				return spawn.toolCallID, true
			}
		}
		return "", false
	}
	if subagentType != "" {
		for i, spawn := range a.children.pending {
			if (spawn.description == "" && spawn.subagentType == subagentType) || spawn.description == subagentType {
				a.children.pending = append(a.children.pending[:i], a.children.pending[i+1:]...)
				return spawn.toolCallID, true
			}
		}
	}
	for i, spawn := range a.children.pending {
		if spawn.description == "" {
			a.children.pending = append(a.children.pending[:i], a.children.pending[i+1:]...)
			return spawn.toolCallID, true
		}
	}
	return "", false
}

// handleSubagentSpawned links a child session to its registry row, opens a row
// for a child that no tool call spawned, and revives a finished row on wake.
func (a *Agent) handleSubagentSpawned(rootSessionID string, update json.RawMessage) {
	if rootSessionID == "" || !a.IsCurrentSession(rootSessionID) {
		return
	}
	var spawned grokSubagentSpawned
	if err := json.Unmarshal(update, &spawned); err != nil || spawned.SubagentID == "" {
		slog.Warn("grok subagent_spawned unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	childSession := spawned.ChildSessionID
	if childSession == "" {
		childSession = spawned.SubagentID
	}
	rawDesc := strings.TrimSpace(spawned.Description)
	subagentType := strings.TrimSpace(spawned.SubagentType)
	title := rawDesc
	if title == "" {
		title = subagentType
	}
	wakeOrigin := strings.TrimSpace(spawned.WakeOrigin)

	a.stateMu.Lock()
	if a.children.settledBeforeSpawn[spawned.SubagentID] && wakeOrigin == "" {
		delete(a.children.settledBeforeSpawn, spawned.SubagentID)
		a.stateMu.Unlock()
		return
	}
	delete(a.children.settledBeforeSpawn, spawned.SubagentID)

	revive := false
	rowKey, linked := a.children.backgroundSpawns[spawned.SubagentID]
	if linked {
		delete(a.children.backgroundSpawns, spawned.SubagentID)
	} else if prevRowKey, wasFinished := a.children.finishedRowKeys[spawned.SubagentID]; wasFinished {
		delete(a.children.finishedRowKeys, spawned.SubagentID)
		rowKey, linked, revive = prevRowKey, true, true
		a.children.bumpRunSeqLocked(spawned.SubagentID)
	} else if spawned.WorkflowRunID == "" {
		rowKey, linked = a.takePendingSpawnLocked(rawDesc, subagentType)
	}
	if !linked {
		rowKey = spawned.SubagentID
	}
	groupLabel := a.children.workflows[spawned.WorkflowRunID]
	openRow := func(reviveRow bool) {
		obs := &acp.SubagentObservation{
			RowKey:        rowKey,
			ChildAgentKey: rowKey,
			Title:         title,
			Status:        bgtask.StatusRunning,
			ReviveRow:     reviveRow,
		}
		if spawned.WorkflowRunID != "" {
			obs.GroupKey = spawned.WorkflowRunID
			obs.GroupLabel = groupLabel
		}
		a.ApplySubagentObservation(obs)
	}
	if early, hasEarly := a.children.earlyFinished[spawned.SubagentID]; hasEarly {
		delete(a.children.earlyFinished, spawned.SubagentID)
		a.children.rememberFinishedRowKeyLocked(spawned.SubagentID, rowKey)
		reportID := a.children.reportIDLocked(spawned.SubagentID)
		a.stateMu.Unlock()
		if !linked || revive {
			openRow(revive)
		}
		a.ApplySubagentObservation(&acp.SubagentObservation{
			RowKey:   rowKey,
			Status:   early.status,
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
			ReportID: reportID,
			Report:   agent.SubagentReport{Text: early.report},
		})
		return
	}
	a.children.link(spawned.SubagentID, linkedSubagent{rootSessionID: rootSessionID, rowKey: rowKey, childSession: childSession, agentAddress: spawned.AgentAddress})
	a.stateMu.Unlock()

	if !revive && !linked && a.Sink() != nil {
		if _, existingStatus, found, err := a.Sink().LookupBackgroundTask(rowKey); err == nil && found && existingStatus.IsFinished() {
			revive = true
			a.stateMu.Lock()
			a.children.bumpRunSeqLocked(spawned.SubagentID)
			a.stateMu.Unlock()
		}
	}
	if !linked || revive {
		// A child with no spawn tool call opens its own row and transcript, and
		// a woken child revives its finished row before streaming Job 2.
		openRow(revive)
	}
	a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: rootSessionID, SessionID: childSession, RowKey: rowKey})
}

// childRowForSession returns the registry row of the subagent that runs in a
// child session, or "".
func (a *Agent) childRowForSession(sessionID string) string {
	rootSessionID := a.CurrentSessionID()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	subagentID, ok := a.children.subagentBySession[sessionID]
	if !ok || a.children.subagents[subagentID].rootSessionID != rootSessionID {
		return ""
	}
	return a.children.subagents[subagentID].rowKey
}

// grokSubagentStatus maps the outcome word of `subagent_finished`.
func grokSubagentStatus(status string) bgtask.Status {
	switch status {
	case "completed":
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	default:
		// "cancelled", and a word a later build adds: the child stopped without
		// completing, and LeapMux cannot claim more.
		return bgtask.StatusStopped
	}
}

// handleSubagentFinished closes the row of a finished child, with its report.
// A blocking spawn's own result closes the same row a moment later, and the
// shared report id stops the sink from storing that report a second time.
func (a *Agent) handleSubagentFinished(update json.RawMessage) {
	var finished struct {
		SubagentID string `json:"subagent_id"`
		Status     string `json:"status"`
		Error      string `json:"error"`
		Output     string `json:"output"`
	}
	if err := json.Unmarshal(update, &finished); err != nil || finished.SubagentID == "" {
		slog.Warn("grok subagent_finished unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	report := finished.Output
	if report == "" {
		report = finished.Error
	}
	status := grokSubagentStatus(finished.Status)

	a.stateMu.Lock()
	child, known := a.children.unlink(finished.SubagentID)
	interrupted := a.children.takeInterrupted(finished.SubagentID)
	rowKey := child.rowKey
	if !known {
		if bgRowKey, bgFound := a.children.backgroundSpawns[finished.SubagentID]; bgFound {
			delete(a.children.backgroundSpawns, finished.SubagentID)
			if a.children.settledBeforeSpawn == nil {
				a.children.settledBeforeSpawn = make(map[string]bool)
			}
			a.children.settledBeforeSpawn[finished.SubagentID] = true
			rowKey, known = bgRowKey, true
		} else if prevRowKey, prevFound := a.children.finishedRowKeys[finished.SubagentID]; prevFound {
			rowKey, known = prevRowKey, true
		}
	}
	if known {
		a.children.rememberFinishedRowKeyLocked(finished.SubagentID, rowKey)
	}
	reportID := a.children.reportIDLocked(finished.SubagentID)
	a.stateMu.Unlock()

	// A cancel WE asked for is a user interrupt, not a plain stop: the row
	// closes as StatusInterrupted and the divider reads "Subagent interrupted".
	// A subagent that completed before the cancel reached it keeps its
	// completion.
	if interrupted && status != bgtask.StatusSucceeded {
		status = bgtask.StatusInterrupted
	}
	if !known {
		buffer := false
		if a.Sink() != nil {
			if _, _, found, err := a.Sink().LookupBackgroundTask(finished.SubagentID); err == nil && !found {
				buffer = true
			}
		} else {
			buffer = true
		}
		if buffer {
			a.stateMu.Lock()
			if a.children.earlyFinished == nil {
				a.children.earlyFinished = make(map[string]grokEarlyFinish)
			}
			a.children.earlyFinished[finished.SubagentID] = grokEarlyFinish{status: status, report: report}
			a.stateMu.Unlock()
			return
		}
		rowKey = finished.SubagentID
		a.stateMu.Lock()
		a.children.rememberFinishedRowKeyLocked(finished.SubagentID, rowKey)
		a.stateMu.Unlock()
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:   rowKey,
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
		ReportID: reportID,
		Report:   agent.SubagentReport{Text: report},
	})
}

var _ agent.ChildInterrupter = (*Agent)(nil)
var _ agent.ChildSteerer = (*Agent)(nil)

// ownedChild reads a child under the root held by WithSessionID. Opaque
// addresses and child IDs never acquire a new root through context clear.
func (a *Agent) ownedChild(rootSessionID, childKey string) (string, linkedSubagent, bool) {
	if a.IsStopped() || rootSessionID == "" {
		return "", linkedSubagent{}, false
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	id, found := a.children.subagentByRow[childKey]
	child := a.children.subagents[id]
	return id, child, found && child.rootSessionID == rootSessionID
}

// SendChildInput keeps ordinary input queued while the child runs. Grok takes
// messages only during its active task, so the queue offers Steer for this input.
func (a *Agent) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	return agent.ChildInputResult{StartsTurn: true}, a.WithSessionID(func(rootSessionID string) error {
		_, child, found := a.ownedChild(rootSessionID, childKey)
		if !found || child.agentAddress == "" {
			return agent.ErrChildRouteNotReady
		}
		if err := validateGrokChildMessage(input.Content, input.Attachments); err != nil {
			return err
		}
		return agent.ErrAgentBusy
	})
}

func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	var state agent.TurnState
	_ = a.WithSessionID(func(rootSessionID string) error {
		_, child, found := a.ownedChild(rootSessionID, childKey)
		active := found && child.agentAddress != ""
		state = agent.TurnState{Active: active, Steerable: active}
		return nil
	})
	return state
}

func validateGrokChildMessage(content string, attachments []*leapmuxv1.Attachment) error {
	if len(attachments) != 0 {
		return fmt.Errorf("the Grok subagent message accepts text without attachments")
	}
	if content == "" {
		return fmt.Errorf("the Grok subagent message is empty")
	}
	return nil
}

// SteerChildInput sends one literal human message to the active native child.
// The session lock keeps a context clear from replacing the address's owner.
func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	if err := validateGrokChildMessage(content, attachments); err != nil {
		return err
	}
	return a.WithSessionID(func(sessionID string) error {
		_, child, found := a.ownedChild(sessionID, childKey)
		if !found || child.agentAddress == "" {
			return agent.ErrChildRouteNotReady
		}
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID, "agentAddress": child.agentAddress, "queue": false,
			"content": []map[string]string{{"type": "text", "text": content}},
		})
		if err != nil {
			return fmt.Errorf("encode the Grok subagent message: %w", err)
		}
		response, err := a.SendRequest(grokSubagentMessageMethod, params, a.APITimeout())
		if err != nil {
			if providerkit.HasJSONRPCErrorCode(err, -32601) {
				return agent.ErrChildOperationUnsupported
			}
			return providerkit.ClassifyJSONRPCDeliveryError("send the message to the Grok subagent", err)
		}
		return grokChildMessageResult(response)
	})
}

func grokChildMessageResult(raw json.RawMessage) error {
	uncertain := func() error {
		return fmt.Errorf("%w: Grok did not confirm the subagent message", agent.ErrDeliveryUncertain)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return uncertain()
	}
	if providerkit.RawPresent(envelope.Error) && providerkit.RawKindOf(envelope.Error) != providerkit.RawNull {
		if providerkit.RawPresent(envelope.Result) && providerkit.RawKindOf(envelope.Result) != providerkit.RawNull {
			return uncertain()
		}
		text, ok := providerkit.RawString(envelope.Error)
		if !ok {
			text = string(envelope.Error)
		}
		return fmt.Errorf("the Grok agent refused the subagent message: %s", text)
	}
	var result struct {
		Kind          string `json:"kind"`
		MessageID     string `json:"messageId"`
		MaxBytes      int64  `json:"maxBytes"`
		ObservedBytes int64  `json:"observedBytes"`
		MaxInFlight   int64  `json:"maxInFlight"`
	}
	if json.Unmarshal(envelope.Result, &result) != nil {
		return uncertain()
	}
	switch result.Kind {
	case "accepted":
		if strings.TrimSpace(result.MessageID) == "" {
			return uncertain()
		}
		return nil
	case "not_active":
		return agent.ErrNoActiveTurn
	case "rejected":
		return agent.ErrChildIdentityRefused
	case "unsupported_content":
		return fmt.Errorf("the Grok agent refused the content of the subagent message")
	case "limit":
		return fmt.Errorf("the Grok agent refused the subagent message size: %d bytes; maximum %d bytes", result.ObservedBytes, result.MaxBytes)
	case "not_accepted_before_deadline":
		return fmt.Errorf("the Grok agent did not accept the subagent message before its deadline")
	case "saturated":
		return fmt.Errorf("the Grok agent is already processing its maximum of %d subagent messages", result.MaxInFlight)
	case "channel_closed":
		return fmt.Errorf("the Grok subagent message channel is closed")
	default:
		return uncertain()
	}
}

type grokCancelOutcome int

const (
	grokCancelAccepted grokCancelOutcome = iota
	grokCancelAlreadyFinished
	grokCancelNotFound
	grokCancelDeclined
)

func grokCancelReplyOutcome(raw json.RawMessage) grokCancelOutcome {
	if len(raw) == 0 {
		return grokCancelAccepted
	}
	var reply struct {
		Cancelled *bool  `json:"cancelled"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return grokCancelAccepted
	}
	for _, field := range []string{reply.Status, reply.Reason, reply.Error} {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "already_finished":
			return grokCancelAlreadyFinished
		case "not_found":
			return grokCancelNotFound
		}
	}
	if reply.Cancelled != nil && !*reply.Cancelled {
		return grokCancelDeclined
	}
	return grokCancelAccepted
}

// InterruptChild stops one subagent. childKey is the registry row key.
func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	var closeSessionID, closeRowKey string
	err := a.WithSessionID(func(rootSessionID string) error {
		subagentID, child, ok := a.ownedChild(rootSessionID, childKey)
		if !ok {
			// The row carries no live child: it finished, or this process never
			// linked it, as after a worker restart.
			return agent.ErrChildRouteNotReady
		}
		params, err := json.Marshal(map[string]string{"subagentId": subagentID})
		if err != nil {
			return fmt.Errorf("marshal the Grok subagent stop: %w", err)
		}
		response, err := a.SendRequest(grokSubagentCancelMethod, params, a.APITimeout())
		if err != nil {
			return fmt.Errorf("stop the Grok subagent: %w", err)
		}
		switch grokCancelReplyOutcome(response) {
		case grokCancelAlreadyFinished, grokCancelDeclined:
			return nil
		case grokCancelNotFound:
			a.stateMu.Lock()
			if current, found := a.children.subagents[subagentID]; found && current == child {
				a.children.unlink(subagentID)
				a.children.rememberFinishedRowKeyLocked(subagentID, child.rowKey)
				closeSessionID = rootSessionID
				closeRowKey = child.rowKey
			}
			a.stateMu.Unlock()
			return nil
		case grokCancelAccepted:
			// Fall through to mark the child interrupted.
		}
		// Mark after the cancel acked: the closer that reaches the transcript
		// spends this, and the divider then reads "Subagent interrupted" rather
		// than the plain stop word.
		a.stateMu.Lock()
		if current, found := a.children.subagents[subagentID]; found && current == child {
			a.children.markInterrupted(subagentID)
		}
		a.stateMu.Unlock()
		return nil
	})
	if err != nil {
		return err
	}
	if closeRowKey != "" {
		a.ApplySubagentObservationForSessionOffReader(closeSessionID, &acp.SubagentObservation{
			RowKey:   closeRowKey,
			Status:   bgtask.StatusStopped,
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
		})
	}
	return nil
}

// grokWorkflowStatus maps the status word of `workflow_updated`, and states
// whether the run still runs or is paused.
func grokWorkflowStatus(status string) bgtask.Status {
	switch status {
	case "complete":
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	case "cancelled", "interrupted":
		return bgtask.StatusStopped
	case grokGoalStatusUserPaused, grokGoalStatusBackOffPaused, grokGoalStatusNoProgressPaused,
		grokGoalStatusInfraPaused, grokGoalStatusBlocked, grokGoalStatusBudgetLimited:
		return bgtask.StatusPaused
	default:
		// active and unknown statuses keep the row open without claiming completion.
		return bgtask.StatusRunning
	}
}

// grokWorkflowReportPrefix keys the report of one workflow run, so a final
// update that Grok repeats stores the report once.
const grokWorkflowReportPrefix = "grok-workflow:"

// handleWorkflowUpdated keeps one grouped registry row for a workflow run.
// A completed run reports its result summary. A failed run reports its native
// pause message. The parent transcript stores both reports because the workflow
// row has no transcript of its own.
func (a *Agent) handleWorkflowUpdated(update json.RawMessage) {
	var workflow struct {
		RunID         string `json:"run_id"`
		Name          string `json:"name"`
		Objective     string `json:"objective"`
		Status        string `json:"status"`
		CurrentPhase  string `json:"current_phase"`
		ActiveAgents  int    `json:"active_agents"`
		PauseMessage  string `json:"pause_message"`
		ResultSummary string `json:"result_summary"`
	}
	if err := json.Unmarshal(update, &workflow); err != nil || workflow.RunID == "" {
		slog.Warn("grok workflow_updated unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	name := strings.TrimSpace(workflow.Name)
	if name == "" {
		name = "Workflow"
	}
	status := grokWorkflowStatus(workflow.Status)
	rowKey := grokWorkflowRowPrefix + workflow.RunID
	revive := false
	a.stateMu.Lock()
	if a.children.workflows == nil {
		a.children.workflows = make(map[string]string)
	}
	a.children.workflows[workflow.RunID] = name
	if status.IsFinished() {
		if a.children.finishedWorkflows == nil {
			a.children.finishedWorkflows = make(map[string]bool)
		}
		a.children.finishedWorkflows[workflow.RunID] = true
	} else if status == bgtask.StatusRunning && a.children.finishedWorkflows[workflow.RunID] {
		revive = true
		delete(a.children.finishedWorkflows, workflow.RunID)
	}
	a.stateMu.Unlock()

	if !revive && status == bgtask.StatusRunning && a.Sink() != nil {
		if _, existingStatus, found, err := a.Sink().LookupBackgroundTask(rowKey); err == nil && found && existingStatus.IsFinished() {
			revive = true
		}
	}
	title := name
	if objective := strings.TrimSpace(workflow.Objective); objective != "" {
		title = name + ": " + objective
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:     rowKey,
		Kind:       bgtask.KindWorkflow,
		Title:      title,
		Activity:   grokWorkflowActivity(workflow.Status, workflow.CurrentPhase, workflow.PauseMessage, workflow.ActiveAgents),
		Status:     status,
		GroupKey:   workflow.RunID,
		GroupLabel: name,
		CloseRow:   status.IsFinished(),
		ReviveRow:  revive,
	})
	var report string
	switch status {
	case bgtask.StatusSucceeded:
		report = workflow.ResultSummary
	case bgtask.StatusFailed:
		report = workflow.PauseMessage
	case bgtask.StatusUnspecified, bgtask.StatusPending, bgtask.StatusRunning, bgtask.StatusPaused,
		bgtask.StatusStopped, bgtask.StatusInterrupted, bgtask.StatusEndedWithUnknownOutcome:
		return
	}
	if strings.TrimSpace(report) == "" {
		return
	}
	providerkit.PersistSubagentReport(a.Sink(), agent.SubagentReportWrite{
		ReportID: grokWorkflowReportPrefix + workflow.RunID,
		Report:   agent.SubagentReport{Label: title, Text: report, Status: bgtask.StatusWire(status)},
	})
}

// grokWorkflowActivity is the line the registry shows beside a workflow run.
func grokWorkflowActivity(status, phase, pauseMessage string, activeAgents int) string {
	if strings.Contains(status, "paused") || status == "blocked" || status == "budget_limited" {
		if message := strings.TrimSpace(pauseMessage); message != "" {
			return strings.ReplaceAll(status, "_", " ") + ": " + message
		}
		return strings.ReplaceAll(status, "_", " ")
	}
	parts := make([]string, 0, 2)
	if phase = strings.TrimSpace(phase); phase != "" {
		parts = append(parts, phase)
	}
	if activeAgents > 0 {
		parts = append(parts, fmt.Sprintf("%d agents running", activeAgents))
	}
	return strings.Join(parts, " · ")
}

// grokTaskID reads a background task id, which Grok states as a string or a
// number.
func grokTaskID(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

// handleTaskBackgrounded opens a registry row for a shell command that runs in
// the background. Grok moves a command there on request or after about 15 s.
func (a *Agent) handleTaskBackgrounded(update json.RawMessage) {
	var task struct {
		TaskID             json.RawMessage `json:"task_id"`
		Command            string          `json:"command"`
		Description        string          `json:"description"`
		MonitorDescription string          `json:"monitor_description"`
	}
	if err := json.Unmarshal(update, &task); err != nil {
		return
	}
	id := grokTaskID(task.TaskID)
	if id == "" {
		return
	}
	title, isCommand := task.Description, false
	if title == "" {
		title = task.MonitorDescription
	}
	if title == "" {
		title, isCommand = task.Command, true
	}
	// Through the base, which keeps the row with the other rows of the session,
	// so a context clear ends it with them.
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:         grokTaskRowPrefix + id,
		Kind:           bgtask.KindShell,
		Title:          title,
		TitleIsCommand: isCommand,
		Status:         bgtask.StatusRunning,
	})
}

// handleTaskCompleted closes the registry row of a background shell command.
func (a *Agent) handleTaskCompleted(update json.RawMessage) {
	var completed struct {
		TaskSnapshot struct {
			TaskID           json.RawMessage `json:"task_id"`
			ExitCode         *int            `json:"exit_code"`
			Signal           *string         `json:"signal"`
			ExplicitlyKilled bool            `json:"explicitly_killed"`
		} `json:"task_snapshot"`
	}
	if err := json.Unmarshal(update, &completed); err != nil {
		return
	}
	snapshot := completed.TaskSnapshot
	id := grokTaskID(snapshot.TaskID)
	if id == "" {
		return
	}
	status := bgtask.StatusSucceeded
	switch {
	case snapshot.ExplicitlyKilled || snapshot.Signal != nil:
		status = bgtask.StatusStopped
	case snapshot.ExitCode != nil && *snapshot.ExitCode != 0:
		status = bgtask.StatusFailed
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:   grokTaskRowPrefix + id,
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	})
}

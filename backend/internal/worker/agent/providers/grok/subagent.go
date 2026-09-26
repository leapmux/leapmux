package grok

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
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

// grokBackgroundSpawnID reads the subagent id from the text a background spawn
// completes with ("Subagent started in background.\nsubagent_id: <id>...").
var grokBackgroundSpawnID = regexp.MustCompile(`(?m)^subagent_id:\s*(\S+)\s*$`)

// grokWorkflowRowPrefix keys the registry row of one workflow run.
const grokWorkflowRowPrefix = "workflow:"

// grokTaskRowPrefix keys the registry row of one background shell command.
const grokTaskRowPrefix = "task:"

// pendingSpawn is one spawn tool call that still waits for its child session.
type pendingSpawn struct {
	toolCallID  string
	description string
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
	// interrupted records the subagents this process stopped through
	// InterruptChild. A cancel we asked for is a USER interrupt, so the closing
	// row reports StatusInterrupted and the divider reads "Subagent
	// interrupted"; a model-side cancel keeps the plain stop word.
	interrupted map[string]bool
}

// linkedSubagent is the registry row and the child session of one subagent.
type linkedSubagent struct {
	rowKey       string
	childSession string
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
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
}

// subagentFromToolCall claims a spawn at its first frame. The row and the child
// transcript open at once, with the prompt, so the child tab opens on the
// prompt before the child says anything.
func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	tool, ok := grokToolOf(tc.Meta)
	if !ok || tool.Name != contracts.GrokToolSpawnSubagent {
		return nil
	}
	var input grokSpawnInput
	_ = json.Unmarshal(tc.RawInput, &input)
	title := strings.TrimSpace(input.Description)
	a.stateMu.Lock()
	if a.children.spawnTools == nil {
		a.children.spawnTools = make(map[string]bool)
	}
	a.children.spawnTools[tc.ToolCallID] = true
	a.children.pending = append(a.children.pending, pendingSpawn{toolCallID: tc.ToolCallID, description: title})
	a.stateMu.Unlock()
	if title == "" {
		title = "Grok subagent"
	}
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
			a.stateMu.Lock()
			a.dropPendingLocked(tcu.ToolCallID)
			if a.children.backgroundSpawns == nil {
				a.children.backgroundSpawns = make(map[string]string)
			}
			a.children.backgroundSpawns[match[1]] = tcu.ToolCallID
			a.stateMu.Unlock()
			return nil
		}
	}
	a.stateMu.Lock()
	a.dropPendingLocked(tcu.ToolCallID)
	a.stateMu.Unlock()
	report := output.Output
	if report == "" && tcu.Status == "completed" {
		report = acp.ToolCallText(tcu.Content)
	}
	reportID := output.SubagentID
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
}

// handleSubagentSpawned links a child session to its registry row, and opens
// a row for a child that no tool call spawned.
func (a *Agent) handleSubagentSpawned(update json.RawMessage) {
	var spawned grokSubagentSpawned
	if err := json.Unmarshal(update, &spawned); err != nil || spawned.SubagentID == "" {
		slog.Warn("grok subagent_spawned unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	childSession := spawned.ChildSessionID
	if childSession == "" {
		childSession = spawned.SubagentID
	}
	title := strings.TrimSpace(spawned.Description)
	if title == "" {
		title = spawned.SubagentType
	}

	a.stateMu.Lock()
	rowKey, linked := a.children.backgroundSpawns[spawned.SubagentID]
	if linked {
		delete(a.children.backgroundSpawns, spawned.SubagentID)
	} else if spawned.WorkflowRunID == "" {
		for i, spawn := range a.children.pending {
			if spawn.description == title {
				rowKey, linked = spawn.toolCallID, true
				a.children.pending = append(a.children.pending[:i], a.children.pending[i+1:]...)
				break
			}
		}
	}
	if !linked {
		rowKey = spawned.SubagentID
	}
	groupLabel := a.children.workflows[spawned.WorkflowRunID]
	a.children.link(spawned.SubagentID, linkedSubagent{rowKey: rowKey, childSession: childSession})
	a.stateMu.Unlock()

	if !linked {
		// A child with no spawn tool call opens its own row and transcript. The
		// subagent id stands in for the spawn span, which no row holds.
		obs := &acp.SubagentObservation{
			RowKey:        rowKey,
			ChildAgentKey: rowKey,
			Title:         title,
			Status:        bgtask.StatusRunning,
		}
		if spawned.WorkflowRunID != "" {
			obs.GroupKey = spawned.WorkflowRunID
			obs.GroupLabel = groupLabel
		}
		a.ApplySubagentObservation(obs)
	}
	a.AttachChildSession(childSession, rowKey)
}

// childRowForSession returns the registry row of the subagent that runs in a
// child session, or "".
func (a *Agent) childRowForSession(sessionID string) string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	subagentID, ok := a.children.subagentBySession[sessionID]
	if !ok {
		return ""
	}
	return a.children.subagents[subagentID].rowKey
}

// grokSubagentStatus maps the outcome word of `subagent_finished`.
func grokSubagentStatus(status string) bgtask.Status {
	switch status {
	case "completed":
		return bgtask.StatusCompleted
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
	a.stateMu.Lock()
	child, known := a.children.unlink(finished.SubagentID)
	interrupted := a.children.takeInterrupted(finished.SubagentID)
	a.stateMu.Unlock()
	rowKey := child.rowKey
	if !known {
		rowKey = finished.SubagentID
	}
	report := finished.Output
	if report == "" {
		report = finished.Error
	}
	status := grokSubagentStatus(finished.Status)
	// A cancel WE asked for is a user interrupt, not a plain stop: the row
	// closes as StatusInterrupted and the divider reads "Subagent interrupted".
	// A subagent that completed before the cancel reached it keeps its
	// completion.
	if interrupted && status != bgtask.StatusCompleted {
		status = bgtask.StatusInterrupted
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:   rowKey,
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
		ReportID: finished.SubagentID,
		Report:   agent.SubagentReport{Text: report},
	})
}

var _ agent.ChildInterrupter = (*Agent)(nil)

// InterruptChild stops one subagent. childKey is the registry row key.
func (a *Agent) InterruptChild(childKey string) error {
	a.stateMu.Lock()
	subagentID, ok := a.children.subagentByRow[childKey]
	a.stateMu.Unlock()
	if !ok {
		// The row carries no live child: it finished, or this process never
		// linked it, as after a worker restart.
		return agent.ErrChildRouteNotReady
	}
	params, err := json.Marshal(map[string]string{"subagentId": subagentID})
	if err != nil {
		return fmt.Errorf("marshal the Grok subagent stop: %w", err)
	}
	if _, err := a.SendRequest(grokSubagentCancelMethod, params, a.APITimeout()); err != nil {
		return fmt.Errorf("stop the Grok subagent: %w", err)
	}
	// Mark after the cancel acked: the closer that reaches the transcript
	// spends this, and the divider then reads "Subagent interrupted" rather
	// than the plain stop word.
	a.stateMu.Lock()
	a.children.markInterrupted(subagentID)
	a.stateMu.Unlock()
	return nil
}

// grokWorkflowStatus maps the status word of `workflow_updated`, and states
// whether the run still runs.
func grokWorkflowStatus(status string) bgtask.Status {
	switch status {
	case "complete":
		return bgtask.StatusCompleted
	case "failed":
		return bgtask.StatusFailed
	case "cancelled", "interrupted":
		return bgtask.StatusStopped
	default:
		// active, the four paused words, blocked and budget_limited: the run
		// exists and can go on, so its row stays open and its activity says why.
		return bgtask.StatusRunning
	}
}

// grokWorkflowReportPrefix keys the report of one workflow run, so a final
// update that Grok repeats stores the report once.
const grokWorkflowReportPrefix = "grok-workflow:"

// handleWorkflowUpdated keeps one registry row for a workflow run, grouped with
// the agents it spawns, and closes it with the run. A completed run states its
// result summary -- the findings of a `/deep-research` run -- which becomes a
// report in the transcript of this agent: the row of a run has no transcript
// of its own.
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
	a.stateMu.Lock()
	if a.children.workflows == nil {
		a.children.workflows = make(map[string]string)
	}
	a.children.workflows[workflow.RunID] = name
	a.stateMu.Unlock()

	status := grokWorkflowStatus(workflow.Status)
	title := name
	if objective := strings.TrimSpace(workflow.Objective); objective != "" {
		title = name + ": " + objective
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:     grokWorkflowRowPrefix + workflow.RunID,
		Kind:       bgtask.KindWorkflow,
		Title:      title,
		Activity:   grokWorkflowActivity(workflow.Status, workflow.CurrentPhase, workflow.PauseMessage, workflow.ActiveAgents),
		Status:     status,
		GroupKey:   workflow.RunID,
		GroupLabel: name,
		CloseRow:   status.IsFinished(),
	})
	// Grok writes the summary only when a run completes.
	if status == bgtask.StatusCompleted && strings.TrimSpace(workflow.ResultSummary) != "" {
		providerkit.PersistSubagentReport(a.Sink(), agent.SubagentReportWrite{
			ReportID: grokWorkflowReportPrefix + workflow.RunID,
			Report:   agent.SubagentReport{Label: title, Text: workflow.ResultSummary, Status: bgtask.StatusWire(status)},
		})
	}
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
	status := bgtask.StatusCompleted
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

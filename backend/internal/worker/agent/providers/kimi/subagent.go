package kimi

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Kimi Code's subagents.
//
// The `Agent` tool starts one subagent and `AgentSwarm` starts up to 128. Each
// one is an agent of the same session with an id of its own (`agent-0`,
// `agent-1`, ...), and the server streams its events on the same WebSocket,
// tagged with that id. `subagent.spawned` links the id to the call that started
// it (`parentToolCallId`), so the child transcript hangs off that call's card and
// the registry row states the subagent's lifecycle.
//
// A swarm's members are one dynamic workflow: each gets a KindWorkflow row,
// grouped under the AgentSwarm call and headed with its description.
//
// A subagent outlives its first run: the model resumes one with `Agent {resume}`,
// and the user can send one a message from its tab. Its id therefore stays linked
// to its transcript for the life of the session.

// kimiChild is the transcript and the registry row of one subagent.
type kimiChild struct {
	childAgentID string
	rowKey       string
	// taskID is the background task the subagent runs as, which the server
	// cancels on request. It is empty once the task ended, for a swarm member,
	// and for a follow-up turn the user started, which runs as no task.
	taskID string
	// background marks a subagent the model started in the background. Its
	// result reaches the parent through a notification turn rather than the
	// spawn's own result, so its report is written to the parent as well.
	background bool
	// title labels the subagent's report.
	title string
	// ended is true while the subagent's registry row is closed. A resync reads
	// it to tell a row it must close or open again from one that is right.
	ended bool
	// interrupted records that InterruptChild stopped this subagent. A cancel
	// we asked for is a USER interrupt, so the closing row reports
	// StatusInterrupted and the divider reads "Subagent interrupted"; a
	// model-side cancel keeps the plain stop word.
	interrupted bool
}

// kimiChildIndex maps each subagent of the current session to its child.
type kimiChildIndex struct {
	mu sync.Mutex
	// byAgent maps the server's subagent id to its child.
	byAgent map[string]*kimiChild
	// byRow maps a registry row key back to the subagent id, for a child
	// operation the worker addresses by the row.
	byRow map[string]string
}

func (i *kimiChildIndex) link(agentID string, child *kimiChild) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.byAgent == nil {
		i.byAgent = make(map[string]*kimiChild)
		i.byRow = make(map[string]string)
	}
	i.byAgent[agentID] = child
	i.byRow[child.rowKey] = agentID
}

// childOf returns the child transcript of a subagent.
func (i *kimiChildIndex) childOf(agentID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	child := i.byAgent[agentID]
	if child == nil {
		return "", false
	}
	return child.childAgentID, true
}

// get returns a copy of a subagent's child record.
func (i *kimiChildIndex) get(agentID string) (kimiChild, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	child := i.byAgent[agentID]
	if child == nil {
		return kimiChild{}, false
	}
	return *child, true
}

// update applies fn to a subagent's child record.
func (i *kimiChildIndex) update(agentID string, fn func(*kimiChild)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if child := i.byAgent[agentID]; child != nil {
		fn(child)
	}
}

// agentOfRow returns the subagent a registry row key belongs to.
func (i *kimiChildIndex) agentOfRow(rowKey string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	agentID, ok := i.byRow[rowKey]
	return agentID, ok
}

// openForeground returns, in id order, the subagents that run in the
// foreground and whose rows are open.
func (i *kimiChildIndex) openForeground() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	var ids []string
	for agentID, child := range i.byAgent {
		if !child.ended && !child.background {
			ids = append(ids, agentID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (i *kimiChildIndex) clear() {
	i.mu.Lock()
	defer i.mu.Unlock()
	clear(i.byAgent)
	clear(i.byRow)
}

// kimiChildRowKey is the registry row key of one subagent. The server numbers
// subagents per session, so the next session after a context clear starts at
// `agent-0` again, and the session qualifies the id.
func kimiChildRowKey(sessionID, agentID string) string {
	return sessionID + "/" + agentID
}

// kimiSubagentSpawned is the subagent.spawned payload.
type kimiSubagentSpawned struct {
	SubagentID      string `json:"subagentId"`
	SubagentName    string `json:"subagentName"`
	ParentToolCall  string `json:"parentToolCallId"`
	ParentAgentID   string `json:"parentAgentId"`
	Description     string `json:"description"`
	SwarmIndex      int    `json:"swarmIndex"`
	RunInBackground bool   `json:"runInBackground"`
	TaskID          string `json:"taskId"`
}

func (a *Agent) handleSubagentSpawned(event kimiEvent) {
	var payload kimiSubagentSpawned
	if !event.decode(&payload) {
		return
	}
	parentAgent := payload.ParentAgentID
	if parentAgent == "" {
		parentAgent = event.AgentID
	}
	a.linkSubagent(payload, parentAgent)
}

// linkSubagent links a subagent to a child transcript under parentAgent and
// opens its registry row, or opens the row of a subagent that has a transcript
// already. The caller holds dispatchMu.
func (a *Agent) linkSubagent(payload kimiSubagentSpawned, parentAgent string) {
	if payload.SubagentID == "" || payload.SubagentID == kimiMainAgentID {
		return
	}
	parentRun := a.run(parentAgent)
	parentSink := a.runSink(parentRun)
	if parentSink == nil {
		// The dispatcher holds a spawn by an unlinked subagent until that subagent
		// is linked, so only a payload whose parent differs from its own agent
		// reaches this.
		slog.Debug("kimi subagent spawned by an unlinked agent", "agent_id", a.AgentID(), "parent", parentAgent, "subagent", payload.SubagentID)
		return
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	spawn, known := parentRun.spawns[payload.ParentToolCall]
	child := a.runLocked(payload.SubagentID)
	a.Mu.Unlock()
	if !known {
		// The spawn's own call opened no span in this process -- the session was
		// resumed while it ran. The child transcript still needs a parent span, and
		// the call id is the only one there is. Only a swarm member states a swarm
		// index, which counts from 1.
		spawn = kimiSpawn{spanID: kimiSpanID(sessionID, parentAgent, 0, payload.ParentToolCall), name: contracts.KimiToolAgent}
		if payload.SwarmIndex > 0 {
			spawn.name = contracts.KimiToolAgentSwarm
		}
	}

	rowKey := kimiChildRowKey(sessionID, payload.SubagentID)
	title := strings.TrimSpace(payload.Description)
	if title == "" {
		title = strings.TrimSpace(payload.SubagentName)
	}
	if existing, linked := a.children.get(payload.SubagentID); linked {
		// The model resumed a subagent that already has a transcript. Its row
		// comes back to Running; the resumed run's prompt reaches the transcript
		// through its turn.started.
		a.children.update(payload.SubagentID, func(c *kimiChild) {
			c.taskID = payload.TaskID
			c.background = payload.RunInBackground
			c.ended = false
		})
		providerkit.LogRegistryRefusal("kimi", "revive", a.sink.ReviveBackgroundTask(existing.rowKey))
		return
	}

	childID, err := parentSink.EnsureChildAgent(kimiChildSpawnSpan(spawn, payload.SubagentID), rowKey, title)
	if err != nil {
		slog.Warn("kimi subagent ensure child failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
		return
	}
	a.children.link(payload.SubagentID, &kimiChild{
		childAgentID: childID, rowKey: rowKey, taskID: payload.TaskID,
		background: payload.RunInBackground, title: title,
	})

	upsert := bgtask.Upsert{
		RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: title, Description: strings.TrimSpace(payload.SubagentName), Status: bgtask.StatusRunning,
	}
	if spawn.name == contracts.KimiToolAgentSwarm {
		// A swarm is one dynamic workflow: its members share a group under the
		// swarm's call, headed with the swarm's own description.
		upsert.Kind = bgtask.KindWorkflow
		upsert.GroupKey = kimiChildRowKey(sessionID, payload.ParentToolCall)
		upsert.GroupLabel = spawn.label
	}
	providerkit.LogRegistryRefusal("kimi", "upsert", parentSink.UpsertBackgroundTask(upsert))

	// The spawn's prompt opens the child transcript. A swarm member states none
	// of its own in the call, so its first turn.started opens it instead. That
	// event waited for this link and replays below.
	if spawn.prompt != "" {
		if err := a.sink.PersistChildPrompt(childID, spawn.prompt); err != nil {
			slog.Warn("kimi subagent persist prompt failed", "agent_id", a.AgentID(), "child", childID, "error", err)
		}
	}
	a.replayPending(child)
}

// kimiChildSpawnSpan is the spawn span a child transcript records.
//
// The worker keeps one child transcript for each spawn span of a parent: the
// `agents` table has a unique index on (parent_agent_id, spawn_span_id), and
// EnsureChildAgent returns the transcript a span already has. An Agent call
// starts one subagent, so its own span is the spawn span. An AgentSwarm call
// starts many from ONE span, so each member takes the call's span qualified by
// its own id; the call's span alone would give every member the first member's
// transcript.
func kimiChildSpawnSpan(spawn kimiSpawn, subagentID string) string {
	if spawn.name == contracts.KimiToolAgentSwarm {
		return spawn.spanID + "#" + subagentID
	}
	return spawn.spanID
}

// kimiSubagentEnded is the subagent.completed, subagent.failed and
// subagent.cancelled payload.
type kimiSubagentEnded struct {
	SubagentID    string `json:"subagentId"`
	ResultSummary string `json:"resultSummary"`
	Error         any    `json:"error"`
}

func (a *Agent) handleSubagentEnded(event kimiEvent) {
	var payload kimiSubagentEnded
	if !event.decode(&payload) || payload.SubagentID == "" {
		return
	}
	status := bgtask.StatusCompleted
	switch event.Type {
	case contracts.KimiEventSubagentFailed:
		status = bgtask.StatusFailed
	case contracts.KimiEventSubagentCancelled:
		status = bgtask.StatusStopped
	}
	a.endSubagent(payload.SubagentID, status, payload.ResultSummary, kimiErrorText(payload.Error))
}

// endSubagent closes the row of a subagent that ended with status, and writes
// its report and its failure. The caller holds dispatchMu.
//
// The subagent's own turn.ended comes before its end, so its turn is normally
// settled already. A turn that is still open ended in a gap of the stream, and
// what it left open ends with it.
func (a *Agent) endSubagent(subagentID string, status bgtask.Status, summary, failure string) {
	child, linked := a.children.get(subagentID)
	if !linked {
		return
	}
	// A cancel WE asked for is a user interrupt, not a plain stop: the row
	// closes as StatusInterrupted and the divider reads "Subagent interrupted".
	// A subagent that completed before the cancel reached it keeps its
	// completion. The mark is spent here so only one closer uses it.
	interrupted := child.interrupted
	if interrupted && status != bgtask.StatusCompleted {
		status = bgtask.StatusInterrupted
	}
	completion := agent.MessageCompletionInterrupted
	if status == bgtask.StatusFailed {
		completion = agent.MessageCompletionError
	}
	a.settleChildTurn(subagentID, child, completion)
	if status == bgtask.StatusFailed && failure != "" {
		a.persistChildText(child.childAgentID, failure, agent.MessageCompletionError)
	}
	if summary := strings.TrimSpace(summary); summary != "" {
		write := agent.SubagentReportWrite{
			ReportID: kimiReportID(child.rowKey, summary),
			Report:   agent.SubagentReport{Label: child.title, Text: summary},
		}
		providerkit.PersistChildSubagentReport(a.sink, agent.ChildSubagentReportWrite{RowKey: child.rowKey, Write: write})
		if child.background {
			// A background subagent's result reaches the parent through a
			// notification turn, not through the spawn's result, so the parent
			// transcript records the report itself.
			providerkit.PersistSubagentReport(a.sink, write)
		}
	}
	a.children.update(subagentID, func(c *kimiChild) {
		c.taskID = ""
		c.ended = true
		c.interrupted = false
	})
	providerkit.LogRegistryRefusal("kimi", "close", a.sink.CloseBackgroundTask(child.rowKey, status))
	a.sink.CleanupChildAgent(child.childAgentID)
}

// settleChildTurn ends a subagent's open turn: its streamed text and its open
// tool calls end with completion, and its tab stops showing the turn. A turn
// that is not open needs nothing.
func (a *Agent) settleChildTurn(subagentID string, child kimiChild, completion agent.MessageCompletion) {
	a.Mu.Lock()
	run := a.runLocked(subagentID)
	active := run.turnActive
	run.turnActive = false
	run.userTurn = false
	a.Mu.Unlock()
	if sink := a.runSink(run); sink != nil {
		a.flushRun(run, sink, completion)
		a.closeOpenTools(run, sink, completion)
	}
	if active {
		a.publishChildTurn(child.childAgentID, false)
	}
}

// kimiReportID keys one subagent report. A subagent can report once per run,
// and the text distinguishes the runs.
func kimiReportID(rowKey, text string) string {
	return providerkit.SubagentReportContentID("kimi", rowKey, text)
}

// kimiErrorText reads the error of a failed subagent, which the server states
// as a string or as an error object.
func kimiErrorText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if message, ok := v["message"].(string); ok {
			return strings.TrimSpace(message)
		}
	}
	return ""
}

// persistChildText appends a line of text to a child transcript.
func (a *Agent) persistChildText(childID, text string, completion agent.MessageCompletion) {
	raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, completion)
	if err != nil {
		slog.Warn("kimi subagent text marshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{}); err != nil {
		slog.Warn("kimi subagent text persist failed", "agent_id", a.AgentID(), "child", childID, "error", err)
	}
}

// --- a resync ---

// kimiRosterSubagent is the part of one subagent of the snapshot's roster that
// a resync reads.
//
// The server's roster (SubagentRosterTracker) lists every foreground subagent
// that an agent of the session spawned, swarm members included, and no
// background one. It forgets every entry when a main turn starts, and a main
// turn starts only after the foreground subagents of the turn before it ended.
type kimiRosterSubagent struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	Phase           string `json:"subagent_phase"`
	SubagentType    string `json:"subagent_type"`
	Description     string `json:"description"`
	ParentToolCall  string `json:"parent_tool_call_id"`
	SwarmIndex      int    `json:"swarm_index"`
	RunInBackground bool   `json:"run_in_background"`
	// OutputPreview is the report of a subagent that completed, and the error
	// of one that failed.
	OutputPreview string `json:"output_preview"`
}

// reconcileSubagents repairs the subagent rows and turn flags after a gap that
// the stream could not replay. The roster states the foreground subagents of
// the current main turn, and the main agent's task list states its subagent
// tasks, foreground and background. The caller holds dispatchMu.
//
//   - A subagent that a source states and that no spawn linked started during
//     the gap: its row opens.
//   - A subagent that a source states as ended closes with the status that the
//     source states, and its open turn ends.
//   - A foreground subagent that no source states as running ended during the
//     gap, and the server keeps no status for it: the roster forgot it when the
//     next main turn started, and the task list lists no foreground task that
//     ended. Its row reads Interrupted. When the task list could not be read
//     (tasksRead is false), nothing proves that it ended.
//   - The roster's phase states whether a subagent runs a turn.
func (a *Agent) reconcileSubagents(roster []kimiRosterSubagent, tasks []kimiTaskItem, tasksRead bool) {
	running := make(map[string]bool)
	for _, entry := range roster {
		if entry.ID == kimiMainAgentID || kimiCheckID("subagent", entry.ID) != nil {
			continue
		}
		if _, linked := a.children.get(entry.ID); !linked {
			parent := a.spawnParent(entry.ParentToolCall)
			a.linkSubagent(kimiSubagentSpawned{
				SubagentID: entry.ID, SubagentName: entry.SubagentType, ParentToolCall: entry.ParentToolCall,
				ParentAgentID: parent, Description: entry.Description, SwarmIndex: entry.SwarmIndex,
				RunInBackground: entry.RunInBackground,
			}, parent)
		}
		status, final := kimiWireTaskStatus(entry.Status)
		if !final {
			running[entry.ID] = true
		}
		// The preview is the report of a subagent that completed, and the error of
		// one that failed.
		var summary, failure string
		if status == bgtask.StatusCompleted {
			summary = entry.OutputPreview
		}
		if status == bgtask.StatusFailed {
			failure = strings.TrimSpace(entry.OutputPreview)
		}
		a.reconcileSubagent(entry.ID, status, final, entry.Phase, summary, failure)
	}

	a.Mu.Lock()
	attachedAt := a.attachedAt
	a.Mu.Unlock()
	for _, item := range tasks {
		if item.Kind != kimiWireTaskKindSubagent || item.AgentID == kimiMainAgentID || kimiCheckID("subagent", item.AgentID) != nil {
			continue
		}
		status, final := kimiWireTaskStatus(item.Status)
		taskID := ""
		if !final && kimiCheckID("task", item.ID) == nil {
			taskID = item.ID
		}
		if _, linked := a.children.get(item.AgentID); !linked {
			if kimiStartedBefore(item.StartedAt, attachedAt) {
				continue
			}
			// The task list is the main agent's, so the main agent spawned it.
			a.linkSubagent(kimiSubagentSpawned{
				SubagentID: item.AgentID, SubagentName: item.SubagentType, ParentToolCall: item.ParentToolCall,
				ParentAgentID: kimiMainAgentID, Description: item.Description, RunInBackground: item.RunInBackground,
				TaskID: taskID,
			}, kimiMainAgentID)
		} else if taskID != "" {
			// The task id is what stops the subagent, and its task.started can be
			// lost with the gap.
			a.children.update(item.AgentID, func(c *kimiChild) {
				if c.taskID == "" {
					c.taskID = taskID
				}
			})
		}
		if !final {
			running[item.AgentID] = true
		}
		a.reconcileSubagent(item.AgentID, status, final, "", "", "")
	}

	if !tasksRead {
		return
	}
	for _, agentID := range a.children.openForeground() {
		if !running[agentID] {
			a.reconcileSubagent(agentID, bgtask.StatusInterrupted, true, "", "", "")
		}
	}
}

// reconcileSubagent sets one linked subagent's row and turn flag to the state
// that the server states. final is true for a subagent that ended with status.
// phase is the roster's, or empty when the source states none. The caller
// holds dispatchMu.
func (a *Agent) reconcileSubagent(agentID string, status bgtask.Status, final bool, phase, summary, failure string) {
	child, linked := a.children.get(agentID)
	if !linked {
		return
	}
	a.Mu.Lock()
	run := a.runLocked(agentID)
	active, userTurn := run.turnActive, run.turnActive && run.userTurn
	a.Mu.Unlock()
	if userTurn {
		// A follow-up turn that the user started from the subagent's tab is no
		// subagent run and no task, so no source states it. Its own turn end
		// closes the row.
		return
	}
	if final {
		if !child.ended {
			a.endSubagent(agentID, status, summary, failure)
		}
		return
	}
	if child.ended {
		// The row is closed, and the subagent runs: the model resumed it during
		// the gap.
		a.children.update(agentID, func(c *kimiChild) { c.ended = false })
		providerkit.LogRegistryRefusal("kimi", "revive", a.sink.ReviveBackgroundTask(child.rowKey))
	}
	switch phase {
	case kimiSubagentPhaseWorking:
		if !active {
			// The turn started during the gap. Its own turn.ended ends it, and so
			// does the subagent's end (endSubagent).
			a.Mu.Lock()
			run.turnActive = true
			a.Mu.Unlock()
			a.publishChildTurn(child.childAgentID, true)
		}
	case kimiSubagentPhaseQueued, kimiSubagentPhaseSuspended:
		a.settleChildTurn(agentID, child, agent.MessageCompletionInterrupted)
	}
}

// spawnParent returns the agent whose run opened callID, the call that spawned
// a subagent. A call that opened during a gap is in no run, and then the main
// agent stands in: the roster does not state the parent agent.
func (a *Agent) spawnParent(callID string) string {
	if callID == "" {
		return kimiMainAgentID
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, agentID := range slices.Sorted(maps.Keys(a.runs)) {
		if _, opened := a.runs[agentID].spawns[callID]; opened {
			return agentID
		}
	}
	return kimiMainAgentID
}

// --- a subagent's own turns ---

func (a *Agent) handleChildTurnStarted(run *kimiRun, payload kimiTurnStarted) {
	child, linked := a.children.get(run.agentID)
	if !linked {
		// The dispatcher holds the events of an unlinked subagent, so this is not
		// reachable. A turn with no transcript to show it moves nothing.
		return
	}
	a.Mu.Lock()
	run.turnID = payload.TurnID
	run.turnActive = true
	run.userTurn = payload.Origin.Kind == contracts.KimiOriginUser
	resumed := run.started
	run.started = true
	a.Mu.Unlock()
	run.generation.Reset()
	prompt := kimiStripGitContext(payload.Prompt)

	a.publishChildTurn(child.childAgentID, true)
	switch {
	case payload.Origin.Kind == contracts.KimiOriginUser:
		// A message from the subagent's own tab. LeapMux recorded the message
		// itself; the row returns to Running, because the subagent works again.
		a.children.update(run.agentID, func(c *kimiChild) { c.ended = false })
		providerkit.LogRegistryRefusal("kimi", "revive", a.sink.ReviveBackgroundTask(child.rowKey))
	case resumed && prompt != "":
		// The model resumed the subagent with a new prompt, which lands in the
		// middle of its transcript.
		if err := a.sink.PersistChildUserMessage(child.childAgentID, prompt); err != nil {
			slog.Warn("kimi subagent persist message failed", "agent_id", a.AgentID(), "child", child.childAgentID, "error", err)
		}
	default:
		// The subagent's first turn. Its prompt opens the transcript, unless the
		// spawn's own prompt already did: PersistChildPrompt writes nothing into
		// a transcript that holds a row.
		if err := a.sink.PersistChildPrompt(child.childAgentID, prompt); err != nil {
			slog.Warn("kimi subagent persist prompt failed", "agent_id", a.AgentID(), "child", child.childAgentID, "error", err)
		}
	}
}

func (a *Agent) handleChildTurnEnded(run *kimiRun, payload kimiTurnEnded) {
	child, linked := a.children.get(run.agentID)
	if !linked {
		// Not reachable, for the reason handleChildTurnStarted states.
		return
	}
	sink := a.runSink(run)
	completion := kimiTurnCompletion(payload.Reason)
	a.flushRun(run, sink, completion)
	a.closeOpenTools(run, sink, completion)
	a.Mu.Lock()
	run.turnActive = false
	userTurn := run.userTurn
	run.userTurn = false
	a.Mu.Unlock()
	a.publishChildTurn(child.childAgentID, false)
	if userTurn {
		// A follow-up turn from the subagent's tab runs as no task, so no
		// subagent.* event ends its row. The turn end is the end. Every other
		// turn belongs to a run that its subagent.* event or its task.terminated
		// ends: a swarm member has no task id, and the swarm runs a member that a
		// rate limit suspended again in a retry turn after its first turn failed.
		a.children.update(run.agentID, func(c *kimiChild) { c.ended = true })
		providerkit.LogRegistryRefusal("kimi", "close", a.sink.CloseBackgroundTask(child.rowKey, kimiChildTurnStatus(payload.Reason)))
	}
}

// kimiChildTurnStatus is the registry status a subagent's own turn leaves.
func kimiChildTurnStatus(reason string) bgtask.Status {
	switch reason {
	case contracts.KimiTurnEndCompleted:
		return bgtask.StatusCompleted
	case contracts.KimiTurnEndCancelled:
		return bgtask.StatusStopped
	default:
		return bgtask.StatusFailed
	}
}

// publishChildTurn reports a subagent's turn on its own tab. A subagent's turn
// takes no steering: the server steers the main agent's queue alone.
func (a *Agent) publishChildTurn(childID string, active bool) {
	if childID == "" {
		return
	}
	a.Mu.Lock()
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	providerkit.PublishTurnStateTo(a.sink.ChildSink(childID), agent.TurnState{Active: active}, seq)
}

// kimiStripGitContext drops the `<git-context>` block the server puts in front
// of a subagent's prompt. It is the engine's own injection, not the instruction
// the subagent was given.
func kimiStripGitContext(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	const open, close = "<git-context>", "</git-context>"
	if !strings.HasPrefix(trimmed, open) {
		return trimmed
	}
	end := strings.Index(trimmed, close)
	if end < 0 {
		return trimmed
	}
	return strings.TrimSpace(trimmed[end+len(close):])
}

// --- child input and interrupt ---

// SendChildInput sends a message to a subagent from its own tab. The server runs
// it as the subagent's next turn. A subagent that is running refuses it with
// ErrAgentBusy, so the input queue holds the message until the turn ends.
func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	agentID, sessionID, err := a.childRoute(childKey)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	busy := a.runLocked(agentID).turnActive
	model := a.settings.model
	a.Mu.Unlock()
	if busy {
		return &agent.AgentBusyError{Err: agent.ErrAgentBusy}
	}
	parts, err := buildKimiContent(content, attachments, a.modelTakesImages(model))
	if err != nil {
		return err
	}
	_, err = a.submitPrompt(sessionID, agentID, parts)
	return err
}

// SteerChildInput is not possible: the server steers the main agent's queue
// alone, and a steer for a subagent's queued prompt reports it as not pending.
func (a *Agent) SteerChildInput(string, string, []*leapmuxv1.Attachment) error {
	return agent.ErrChildOperationUnsupported
}

// ActiveChildTurnState reports a subagent's turn. It takes no steering.
func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	agentID, _, err := a.childRoute(childKey)
	if err != nil {
		return agent.TurnState{}
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return agent.TurnState{Active: a.runLocked(agentID).turnActive}
}

// InterruptChild stops a subagent that runs as a task. The server cancels the
// task, and the subagent's turn ends as cancelled. A swarm member and a
// follow-up turn the user started run as no task, and the server offers no
// route that stops one of them alone.
func (a *Agent) InterruptChild(childKey string) error {
	agentID, sessionID, err := a.childRoute(childKey)
	if err != nil {
		return err
	}
	child, _ := a.children.get(agentID)
	if child.taskID == "" {
		return fmt.Errorf("%w: this subagent runs as no task, and Kimi Code cannot stop it alone", agent.ErrChildOperationUnsupported)
	}
	if err := kimiCheckID("task", child.taskID); err != nil {
		return err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	if err := a.api.post(ctx, kimiItemPath(sessionID, "tasks", child.taskID, kimiActionCancel), nil, nil); err != nil {
		return err
	}
	// Mark after the cancel acked: the closer that reaches the transcript
	// spends this, and the divider then reads "Subagent interrupted" rather
	// than the plain stop word.
	a.children.update(agentID, func(c *kimiChild) { c.interrupted = true })
	return nil
}

// errKimiChildUnknown reports a row key that identifies no subagent of the
// running session.
var errKimiChildUnknown = errors.New("unknown Kimi Code subagent")

// childRoute resolves a registry row key to the subagent and the session it
// belongs to. A row of a previous session -- before a context clear or a
// restart -- identifies a subagent the running session does not have.
func (a *Agent) childRoute(childKey string) (agentID, sessionID string, err error) {
	a.Mu.Lock()
	sessionID = a.sessionID
	a.Mu.Unlock()
	agentID, ok := a.children.agentOfRow(childKey)
	if !ok {
		if strings.HasPrefix(childKey, sessionID+"/") {
			// The row belongs to this session and its spawn was not reported to
			// this process yet: the route comes once it is.
			return "", "", fmt.Errorf("%w: %q", agent.ErrChildRouteNotReady, childKey)
		}
		return "", "", fmt.Errorf("%w %q", errKimiChildUnknown, childKey)
	}
	return agentID, sessionID, nil
}

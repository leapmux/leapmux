package ohmypi

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// omp's subagents.
//
// The `task` tool starts one or more subagents: one for each entry of its `tasks`
// list. omp reports each of them through three frames, which the worker
// subscribes to at startup (`set_subagent_subscription {level:"events"}`):
//
//   - subagent_lifecycle states that a subagent started and how it ended. It
//     carries the subagent's id and the id of the `task` call that started it.
//   - subagent_progress states what a running subagent does now.
//   - subagent_event carries one session event of the subagent: its messages and
//     its tool calls, in the same frames the session's own stream uses.
//
// Each subagent gets a registry row and a child transcript. Its events drive a
// conversation of its own, written to the child sink.
//
// By default omp runs a subagent in the BACKGROUND: the `task` call ends at once,
// the parent's run ends, and omp starts a new run when the subagent yields its
// result. The lifecycle frame, not the `task` call's end, is what ends the row.

// subagentState is one subagent the worker follows. Guarded by Process.Mu.
type subagentState struct {
	// id is omp's agent id, "ScoutOne". A subagent of a subagent has the id
	// "<parent id>.<name>".
	id      string
	rowKey  string
	childID string
	title   string
	conv    *conversation
	// activeForm is the last progress line the row showed, so an unchanged line
	// is not written again.
	activeForm string
	// report is what the subagent yielded, the row's report when it ends.
	report string
}

// taskSpec is one entry of a `task` call's `tasks` list.
type taskSpec struct {
	Name  string `json:"name"`
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// taskSpawn keeps the tasks of one `task` call until each of them started.
type taskSpawn struct {
	tasks   []taskSpec
	started int
}

// rememberSpawns keeps the tasks a `task` call states, so the lifecycle frame that
// starts each subagent can label its row with the task the parent wrote.
//
// omp accepts a list (`{context, tasks:[...]}`, the default) and, with
// `task.batch` off, a single task (`{agent, task, name?}`).
func (a *Agent) rememberSpawns(toolCallID string, args json.RawMessage) {
	var batch struct {
		Tasks []taskSpec `json:"tasks"`
	}
	if json.Unmarshal(args, &batch) != nil {
		return
	}
	tasks := batch.Tasks
	if len(tasks) == 0 {
		var single taskSpec
		if json.Unmarshal(args, &single) != nil || single.Task == "" {
			return
		}
		tasks = []taskSpec{single}
	}
	a.Mu.Lock()
	if a.spawns == nil {
		a.spawns = make(map[string]*taskSpawn)
	}
	a.spawns[toolCallID] = &taskSpawn{tasks: tasks}
	a.Mu.Unlock()
}

// forgetSpawns drops a `task` call's tasks when the call ends: at once for a call
// that failed, since none of its missing subagents starts after that, and once
// every subagent started for a call that succeeded. A background subagent can
// start after its call ended, so a successful call keeps the tasks that did not
// start yet.
func (a *Agent) forgetSpawns(toolCallID string, failed bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if spawn := a.spawns[toolCallID]; spawn != nil && (failed || spawn.started >= len(spawn.tasks)) {
		delete(a.spawns, toolCallID)
	}
}

// subagentLifecycle is the payload of a subagent_lifecycle frame.
type subagentLifecycle struct {
	ID               string `json:"id"`
	Agent            string `json:"agent"`
	ParentToolCallID string `json:"parentToolCallId"`
	Status           string `json:"status"`
	Description      string `json:"description"`
	Index            int    `json:"index"`
}

// handleSubagentLifecycle starts or ends one subagent.
func (a *Agent) handleSubagentLifecycle(raw []byte) {
	var frame struct {
		Payload subagentLifecycle `json:"payload"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Payload.ID == "" {
		slog.Warn("omp subagent_lifecycle decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	payload := frame.Payload
	switch payload.Status {
	case subagentStatusStarted:
		a.startSubagent(payload)
	case subagentStatusCompleted:
		a.endSubagent(payload.ID, bgtask.StatusCompleted)
	case subagentStatusFailed:
		a.endSubagent(payload.ID, bgtask.StatusFailed)
	case subagentStatusAborted:
		a.endSubagent(payload.ID, bgtask.StatusStopped)
	default:
		slog.Debug("omp subagent_lifecycle status unknown", "agent_id", a.AgentID(), "status", payload.Status)
	}
}

// subagentRowKey is the registry row key of one subagent.
//
// omp's agent id is unique inside one session, and a new session can reuse it:
// the first subagent of every session is likely to take the same name. The session
// id makes the key unique across the sessions one agent runs.
func subagentRowKey(sessionID, id string) string {
	if sessionID == "" {
		return id
	}
	return sessionID + "/" + id
}

// startSubagent opens one subagent's child transcript and registry row.
func (a *Agent) startSubagent(payload subagentLifecycle) {
	a.Mu.Lock()
	if _, exists := a.subagents[payload.ID]; exists {
		a.Mu.Unlock()
		return
	}
	// A subagent of a subagent was started by a `task` call in its PARENT's
	// transcript, so its spawn span lives there.
	parentSink := a.sink
	if dot := strings.LastIndex(payload.ID, "."); dot > 0 {
		if parent := a.subagents[payload.ID[:dot]]; parent != nil && parent.conv != nil {
			parentSink = parent.conv.sink
		}
	}
	var spec taskSpec
	if spawn := a.spawns[payload.ParentToolCallID]; spawn != nil {
		if payload.Index >= 0 && payload.Index < len(spawn.tasks) {
			spec = spawn.tasks[payload.Index]
		}
		spawn.started++
		if spawn.started >= len(spawn.tasks) {
			delete(a.spawns, payload.ParentToolCallID)
		}
	}
	rowKey := subagentRowKey(a.sessionID, payload.ID)
	a.Mu.Unlock()

	title := payload.ID
	description := firstLine(payload.Description)
	if description == "" {
		description = firstLine(spec.Task)
	}
	childID := ""
	if payload.ParentToolCallID != "" {
		id, err := parentSink.EnsureChildAgent(payload.ParentToolCallID, rowKey, title)
		if err != nil {
			slog.Warn("omp subagent child transcript failed", "agent_id", a.AgentID(), "subagent", payload.ID, "error", err)
		} else {
			childID = id
		}
	}
	state := &subagentState{id: payload.ID, rowKey: rowKey, childID: childID, title: title}
	if childID != "" {
		state.conv = newConversation(a.sink.ChildSink(childID), childID)
	}
	a.Mu.Lock()
	if a.subagents == nil {
		a.subagents = make(map[string]*subagentState)
	}
	a.subagents[payload.ID] = state
	a.Mu.Unlock()
	providerkit.LogRegistryRefusal("ohmypi", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:       rowKey,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: childID,
		Title:        title,
		Description:  description,
		Status:       bgtask.StatusRunning,
	}))
}

// endSubagent closes one subagent: it persists what its transcript left
// unfinished and the report it yielded, closes its row, and releases its child
// state.
func (a *Agent) endSubagent(id string, status bgtask.Status) {
	a.Mu.Lock()
	state := a.subagents[id]
	delete(a.subagents, id)
	sessionID := a.sessionID
	a.Mu.Unlock()
	if state == nil {
		// A subagent whose start this worker never saw. Its row, if a previous
		// process opened one, still closes.
		providerkit.LogRegistryRefusal("ohmypi", "close", a.sink.CloseBackgroundTask(subagentRowKey(sessionID, id), status))
		return
	}
	a.finishSubagent(state, status)
}

// finishSubagent persists what one subagent left unfinished, its report, and its
// final status, then releases its child state.
func (a *Agent) finishSubagent(state *subagentState, status bgtask.Status) {
	if state.conv != nil {
		a.persistIncompleteTools(state.conv, completionForStatus(status))
	}
	a.Mu.Lock()
	report := state.report
	a.Mu.Unlock()
	if report != "" && state.childID != "" {
		providerkit.PersistChildSubagentReport(a.sink, agent.ChildSubagentReportWrite{
			RowKey: state.rowKey,
			Write: agent.SubagentReportWrite{
				ReportID: "omp:" + state.rowKey,
				Report:   agent.SubagentReport{Label: state.title, Text: report},
			},
		})
	}
	providerkit.LogRegistryRefusal("ohmypi", "close", a.sink.CloseBackgroundTask(state.rowKey, status))
	if state.childID != "" {
		a.sink.CleanupChildAgent(state.childID)
	}
}

// closeSubagents closes every subagent the worker follows, when the process stops
// or the session is replaced. omp ends every subagent with the session.
func (a *Agent) closeSubagents(status bgtask.Status) {
	a.Mu.Lock()
	states := make([]*subagentState, 0, len(a.subagents))
	for _, state := range a.subagents {
		states = append(states, state)
	}
	clear(a.subagents)
	clear(a.spawns)
	a.Mu.Unlock()
	for _, state := range states {
		a.finishSubagent(state, status)
	}
}

// subagentStatusForCompletion is the row status of a subagent that the end of its
// session ended.
func subagentStatusForCompletion(completion agent.MessageCompletion) bgtask.Status {
	switch completion {
	case agent.MessageCompletionError:
		return bgtask.StatusFailed
	case agent.MessageCompletionComplete:
		return bgtask.StatusCompleted
	default:
		return bgtask.StatusStopped
	}
}

// completionForStatus is the completion of the output a subagent left unfinished
// when it ended with a status.
func completionForStatus(status bgtask.Status) agent.MessageCompletion {
	if status == bgtask.StatusFailed {
		return agent.MessageCompletionError
	}
	return agent.MessageCompletionInterrupted
}

// handleSubagentProgress shows a running subagent's latest activity on its row.
func (a *Agent) handleSubagentProgress(raw []byte) {
	var frame struct {
		Payload struct {
			Progress struct {
				ID           string   `json:"id"`
				RecentOutput []string `json:"recentOutput"`
				RecentTools  []struct {
					Tool string `json:"tool"`
					Args string `json:"args"`
				} `json:"recentTools"`
			} `json:"progress"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		slog.Warn("omp subagent_progress decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	progress := frame.Payload.Progress
	activeForm := ""
	if n := len(progress.RecentTools); n > 0 {
		tool := progress.RecentTools[n-1]
		activeForm = strings.TrimSpace(tool.Tool + " " + firstLine(tool.Args))
	} else if n := len(progress.RecentOutput); n > 0 {
		activeForm = firstLine(progress.RecentOutput[n-1])
	}
	a.Mu.Lock()
	state := a.subagents[progress.ID]
	if state == nil || activeForm == "" || activeForm == state.activeForm {
		a.Mu.Unlock()
		return
	}
	state.activeForm = activeForm
	rowKey := state.rowKey
	a.Mu.Unlock()
	providerkit.LogRegistryRefusal("ohmypi", "status", a.sink.UpdateBackgroundTaskStatus(rowKey, bgtask.StatusRunning, activeForm))
}

// handleSubagentEvent drives one subagent's conversation with one of its session
// events.
//
// The event is a frame of the subagent's own stream, in the shape the session's
// stream uses. Its messages and tool calls reach the child transcript, and a run's
// end draws the child's own turn-end row. Everything else -- its streamed deltas,
// its settings, its notices -- stays with the subagent.
func (a *Agent) handleSubagentEvent(raw []byte) {
	var frame struct {
		Payload struct {
			ID    string          `json:"id"`
			Event json.RawMessage `json:"event"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Payload.ID == "" || len(frame.Payload.Event) == 0 {
		slog.Warn("omp subagent_event decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	state := a.subagents[frame.Payload.ID]
	a.Mu.Unlock()
	if state == nil || state.conv == nil {
		return
	}
	event := frame.Payload.Event
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(event, &head) != nil {
		return
	}
	switch head.Type {
	case contracts.OhMyPiEventAgentStart:
		a.startChildRun(state.conv)
	case contracts.OhMyPiEventAgentEnd:
		a.endChildRun(state.conv, event)
	case contracts.OhMyPiEventMessageEnd:
		a.handleMessageEnd(state.conv, event)
	case contracts.OhMyPiEventToolExecutionStart:
		a.handleToolStart(state.conv, event)
	case contracts.OhMyPiEventToolExecutionUpdate:
		a.handleToolUpdate(state.conv, event)
	case contracts.OhMyPiEventToolExecutionEnd:
		a.handleToolEnd(state.conv, event)
	}
}

// startChildRun marks the start of a subagent's run, for the duration of its
// turn-end row.
func (a *Agent) startChildRun(c *conversation) {
	now := a.Clock().Now()
	a.Mu.Lock()
	c.runStartedAt = now
	c.toolUses = 0
	a.Mu.Unlock()
}

// endChildRun persists a subagent's turn-end row. A subagent that a message from
// its parent wakes runs again, so each run ends with its own row, and the registry
// row closes only with the lifecycle frame.
func (a *Agent) endChildRun(c *conversation, raw []byte) {
	var env agentEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("omp subagent agent_end decode failed", "agent_id", a.AgentID(), "error", err)
	}
	endedAt := a.Clock().Now()
	a.Mu.Lock()
	startedAt := c.runStartedAt
	c.runStartedAt = time.Time{}
	toolUses := c.toolUses
	c.toolUses = 0
	a.Mu.Unlock()
	a.persistIncompleteTools(c, env.completion())
	content := agentEndContent(raw, usageSnapshot{}, turnDurationMs(startedAt, endedAt))
	if err := c.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
		slog.Warn("omp persist subagent agent_end", "agent_id", a.AgentID(), "child_agent_id", c.childID, "error", err)
	}
	c.sink.ResetSpans()
}

// persistChildPrompt opens a child transcript with the prompt its parent gave the
// subagent: the subagent's first user message. Later user messages are no-ops,
// because PersistChildPrompt writes only into an empty transcript.
func (a *Agent) persistChildPrompt(c *conversation, content json.RawMessage) {
	text := messageText(content)
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := a.sink.PersistChildPrompt(c.childID, text); err != nil {
		slog.Warn("omp persist subagent prompt", "agent_id", a.AgentID(), "child_agent_id", c.childID, "error", err)
	}
}

// rememberYield keeps what a subagent yielded, as the report its row states when
// it ends. The `yield` tool puts the result in `details.data`: text, or a value
// an output schema shaped.
func (a *Agent) rememberYield(c *conversation, result json.RawMessage) {
	var envelope struct {
		Details struct {
			Data json.RawMessage `json:"data"`
		} `json:"details"`
	}
	if json.Unmarshal(result, &envelope) != nil || len(envelope.Details.Data) == 0 || string(envelope.Details.Data) == "null" {
		return
	}
	report := ""
	var text string
	if json.Unmarshal(envelope.Details.Data, &text) == nil {
		report = text
	} else {
		report = string(envelope.Details.Data)
	}
	if strings.TrimSpace(report) == "" {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, state := range a.subagents {
		if state.conv == c {
			state.report = report
			return
		}
	}
}

// firstLine is the first non-empty line of a text, trimmed.
func firstLine(text string) string {
	return strings.TrimSpace(bgtask.FirstLine(strings.TrimSpace(text)))
}

// shellRowKey is the registry row key of one background shell job.
func shellRowKey(sessionID, jobID string) string {
	return "bash:" + subagentRowKey(sessionID, jobID)
}

// openBackgroundShell opens a SHELL row for a `bash` call that omp moved to the
// background. omp does that on its own for a long command, and for a command that
// a steering message interrupted; the call's result then states the job in
// `details.async`, and the job's output arrives later in an async-result message.
func (a *Agent) openBackgroundShell(toolCallID string, args, result json.RawMessage) {
	var envelope struct {
		Details struct {
			Async *struct {
				State string `json:"state"`
				JobID string `json:"jobId"`
				Type  string `json:"type"`
			} `json:"async"`
		} `json:"details"`
	}
	if json.Unmarshal(result, &envelope) != nil || envelope.Details.Async == nil {
		return
	}
	job := envelope.Details.Async
	if job.JobID == "" || job.State != contracts.OhMyPiAsyncJobStateRunning || job.Type != asyncJobTypeBash {
		return
	}
	var call struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(args, &call)
	a.Mu.Lock()
	rowKey := shellRowKey(a.sessionID, job.JobID)
	if a.shells == nil {
		a.shells = make(map[string]string)
	}
	a.shells[job.JobID] = rowKey
	a.Mu.Unlock()
	title := strings.TrimSpace(call.Command)
	titleIsCommand := title != ""
	if title == "" {
		title = job.JobID
	}
	providerkit.LogRegistryRefusal("ohmypi", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:         rowKey,
		Kind:           bgtask.KindShell,
		Title:          title,
		TitleIsCommand: titleIsCommand,
		Description:    "Background job " + job.JobID + " of call " + toolCallID,
		Status:         bgtask.StatusRunning,
	}))
}

// closeDeliveredShells closes the SHELL rows of the jobs an async-result message
// delivers. The message states each finished job in `details.jobs`.
func (a *Agent) closeDeliveredShells(details json.RawMessage) {
	var envelope struct {
		Jobs []struct {
			JobID string `json:"jobId"`
		} `json:"jobs"`
	}
	if len(details) == 0 || json.Unmarshal(details, &envelope) != nil {
		return
	}
	for _, job := range envelope.Jobs {
		a.Mu.Lock()
		rowKey, ok := a.shells[job.JobID]
		delete(a.shells, job.JobID)
		a.Mu.Unlock()
		if ok {
			providerkit.LogRegistryRefusal("ohmypi", "close", a.sink.CloseBackgroundTask(rowKey, bgtask.StatusCompleted))
		}
	}
}

// closeAllShells closes every SHELL row, when the session is replaced. omp drops
// the result of a job whose session ended, so no async-result would ever close
// the row.
func (a *Agent) closeAllShells() {
	a.Mu.Lock()
	rowKeys := make([]string, 0, len(a.shells))
	for _, rowKey := range a.shells {
		rowKeys = append(rowKeys, rowKey)
	}
	clear(a.shells)
	a.Mu.Unlock()
	for _, rowKey := range rowKeys {
		providerkit.LogRegistryRefusal("ohmypi", "close", a.sink.CloseBackgroundTask(rowKey, bgtask.StatusStopped))
	}
}

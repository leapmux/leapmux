package cline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Subagents.
//
// Cline runs a subagent as the `spawn_agent` tool call of its parent: the call
// starts a child agent with the call's `task`, waits for it, and ends with the
// child's answer. The child runs its tools with no approval, which the approval
// card of the call states.
//
// Cline states no agent on the output of a child: the child's iterations, text,
// reasoning and tool calls arrive among the parent's, between the call's
// tool.started and its tool.finished. The worker attributes that output by what
// it saw before it:
//
//   - The parent waits inside its tool calls while a child runs, so the first
//     iteration that starts after the call started is the child's.
//   - From then on, output that no tool call id routes is the child's, and so
//     is a new tool call. A child's own spawn_agent call nests the same rule.
//   - A tool call keeps the transcript it started in, by its id.
//
// When two children of one parent run at once -- the model can start several
// calls in one step, and Cline runs them in parallel -- their output
// interleaves with nothing that tells it apart. The worker then writes none of
// it live. When each such child ends, Cline stores the child's conversation as
// a session of its own (`<root>__<agentId>`), and the worker writes the child's
// transcript from that session instead (startBackfill). The stored session
// states the child's task but not the call, so the worker matches it by the
// task and by the time it started.
//
// Each call gets a registry row, keyed by the call's id, that links the child
// transcript. The row closes with the call, and the child's answer ends the
// child transcript as its report.

// spawnTitleFallback titles a subagent whose task states nothing.
const spawnTitleFallback = "Cline subagent"

// spawnCall is one spawn_agent call that runs. Guarded by Agent.Mu.
type spawnCall struct {
	id   string
	task string
	// parent is the transcript that shows the call.
	parent *transcript
	// child is the subagent's own transcript, or nil when the worker could not
	// create it.
	child *transcript
	// rootSession is the session the call ran in.
	rootSession string
	// startedAtMs is when the daemon published the call's start, in
	// milliseconds since the Unix epoch.
	startedAtMs int64
	// started records that the child's first iteration started.
	started bool
	// ambiguous records that output of the child reached no transcript live,
	// so the child transcript comes from Cline's stored session.
	ambiguous bool
}

// markAmbiguous marks the call, and every call below it, as one whose child
// transcript comes from Cline's store.
func (s *spawnCall) markAmbiguous() {
	s.ambiguous = true
	if s.child == nil {
		return
	}
	for _, nested := range s.child.spawns {
		nested.markAmbiguous()
	}
}

// startsSubagent reports whether a tool runs a child agent whose output
// arrives untagged among the lead's: spawn_agent, and a configured agent of
// `.cline/agents/`, whose tool is `subagent_<name>_<hash>`
// (configured-agent-tool.ts of Cline 3.0.64). Both return the same result, and
// both run the child's tools with no approval.
func startsSubagent(toolName string) bool {
	return toolName == contracts.ClineToolSpawnAgent || strings.HasPrefix(toolName, contracts.ClineToolPrefixConfiguredAgent)
}

// spawnInput is the part of a subagent call that the worker reads. spawn_agent
// states the child's task as `task`, and a configured agent as `prompt`.
type spawnInput struct {
	Task   string `json:"task"`
	Prompt string `json:"prompt"`
}

// task is the child's task, whichever field states it.
func (in spawnInput) task() string {
	if strings.TrimSpace(in.Task) != "" {
		return in.Task
	}
	return in.Prompt
}

// spawnOutput is the result of a spawn_agent call.
type spawnOutput struct {
	Text         string `json:"text"`
	FinishReason string `json:"finishReason"`
}

// routeContent returns the transcript that one output event without an agent
// belongs to, or nil when the worker cannot tell and the event reaches no
// transcript. The caller holds dispatchMu.
//
// Output that arrives with no turn running is either a teammate's, while a
// teammate run is active (team.go), or the start of a lead run that Cline
// began by itself.
func (a *Agent) routeContent() *transcript {
	a.Mu.Lock()
	active, teamBusy := a.turn.active, a.team.busy()
	a.Mu.Unlock()
	if !active {
		if teamBusy {
			return nil
		}
		a.ensureTurn()
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	t := a.out.lead
	for {
		switch len(t.spawns) {
		case 0:
			return t
		case 1:
			s := t.spawns[0]
			if !s.started {
				// The call runs, and its child has not begun: the output is still
				// the parent's.
				return t
			}
			if s.ambiguous || s.child == nil {
				return nil
			}
			t = s.child
		default:
			for _, s := range t.spawns {
				s.markAmbiguous()
			}
			return nil
		}
	}
}

// handleIterationStarted records the start of a child's first iteration. An
// iteration that starts while one call of a transcript runs, and its child has
// not begun, is that child's.
func (a *Agent) handleIterationStarted() {
	a.Mu.Lock()
	active, teamBusy := a.turn.active, a.team.busy()
	a.Mu.Unlock()
	if !active {
		if teamBusy {
			return
		}
		a.ensureTurn()
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	t := a.out.lead
	for {
		switch len(t.spawns) {
		case 0:
			return
		case 1:
			s := t.spawns[0]
			if !s.started {
				s.started = true
				return
			}
			if s.ambiguous || s.child == nil {
				return
			}
			t = s.child
		default:
			for _, s := range t.spawns {
				s.started = true
				s.markAmbiguous()
			}
			return
		}
	}
}

// spawnRuns reports whether a spawn_agent call runs.
func (a *Agent) spawnRuns() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return len(a.out.spawns) > 0
}

// openSpawn opens the registry row and the child transcript of one
// spawn_agent call that transcript t shows.
func (a *Agent) openSpawn(t *transcript, event hubEvent, call toolEvent) {
	var input spawnInput
	_ = json.Unmarshal(call.Input, &input)
	task := strings.TrimSpace(input.task())
	title := firstLine(task)
	if title == "" {
		title = spawnTitleFallback
	}
	sink := a.sinkFor(t)
	childID, err := sink.EnsureChildAgent(call.ToolCallID, call.ToolCallID, title)
	if err != nil {
		slog.Warn("cline subagent ensure child failed", "agent_id", a.AgentID(), "tool_call_id", call.ToolCallID, "error", err)
	}
	providerkit.LogRegistryRefusal("cline", "open subagent", sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:        call.ToolCallID,
		Kind:          bgtask.KindSubagent,
		ChildAgentID:  childID,
		ParentAgentID: t.childID,
		Title:         title,
		Description:   task,
		Status:        bgtask.StatusRunning,
	}))
	spawn := &spawnCall{
		id:          call.ToolCallID,
		task:        task,
		parent:      t,
		rootSession: event.SessionID,
		startedAtMs: event.Timestamp,
	}
	if childID != "" {
		spawn.child = newChildTranscript(sink, childID)
		if err := sink.PersistChildPrompt(childID, task); err != nil {
			slog.Warn("cline subagent persist prompt failed", "agent_id", a.AgentID(), "child", childID, "error", err)
		}
	}
	a.Mu.Lock()
	t.spawns = append(t.spawns, spawn)
	a.out.spawns[spawn.id] = spawn
	a.Mu.Unlock()
}

// closeSpawn ends one spawn_agent call with its result.
func (a *Agent) closeSpawn(s *spawnCall, call toolEvent) {
	var output spawnOutput
	_ = json.Unmarshal(call.Output, &output)
	status := spawnStatus(output.FinishReason, call.Error)
	report := strings.TrimSpace(output.Text)
	if report == "" {
		report = strings.TrimSpace(call.Error)
	}
	a.finishSpawn(s, status, report)
}

// spawnStatus maps a subagent's end onto its row's final status. A call that
// Cline reports with an error failed. A child that the abort of the run ended
// stopped.
func spawnStatus(finishReason, callError string) bgtask.Status {
	switch {
	case finishReason == contracts.ClineRunReasonAborted:
		return bgtask.StatusStopped
	case callError != "":
		return bgtask.StatusFailed
	case finishReason == contracts.ClineRunReasonError, finishReason == contracts.ClineRunReasonMistakeLimit:
		return bgtask.StatusFailed
	default:
		return bgtask.StatusCompleted
	}
}

// spawnCompletions states how the text and the open tool calls of a child end
// when its call ends with status. A child that completed wrote its text in
// full, and a tool call it still held open did not finish. A child that
// failed ends both with the error, and every other end is a stop.
func spawnCompletions(status bgtask.Status) (text, tools agent.MessageCompletion) {
	switch status {
	case bgtask.StatusCompleted:
		return agent.MessageCompletionComplete, agent.MessageCompletionError
	case bgtask.StatusFailed:
		return agent.MessageCompletionError, agent.MessageCompletionError
	default:
		return agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted
	}
}

// finishSpawn ends one call: its children first, then its own child
// transcript, its report and its row. A child whose output the worker could
// not attribute gets its transcript from Cline's store first.
func (a *Agent) finishSpawn(s *spawnCall, status bgtask.Status, report string) {
	a.Mu.Lock()
	if _, open := a.out.spawns[s.id]; !open {
		a.Mu.Unlock()
		return
	}
	delete(a.out.spawns, s.id)
	s.parent.spawns = removeSpawn(s.parent.spawns, s)
	var nested []*spawnCall
	if s.child != nil {
		nested = append(nested, s.child.spawns...)
	}
	sessionID := a.sessionID
	a.Mu.Unlock()

	for _, n := range nested {
		// A call cannot end before the calls its child made; one that did was
		// cut short with it.
		a.finishSpawn(n, stoppedWith(status), "")
	}
	parentSink := a.sinkFor(s.parent)
	finalize := func() {
		if s.child != nil && report != "" {
			providerkit.PersistChildSubagentReport(parentSink, agent.ChildSubagentReportWrite{
				RowKey: s.id,
				Write: agent.SubagentReportWrite{
					ReportID: "cline-spawn:" + s.id,
					Report:   agent.SubagentReport{Text: report, Status: bgtask.StatusWire(status)},
				},
			})
		}
		providerkit.LogRegistryRefusal("cline", "close subagent", parentSink.CloseBackgroundTask(s.id, status))
		if s.child != nil {
			parentSink.CleanupChildAgent(s.child.childID)
		}
	}
	if s.child == nil {
		finalize()
		return
	}
	text, tools := spawnCompletions(status)
	a.flushPending(s.child, sessionID, text)
	a.closeOpenTools(s.child, tools)
	if !s.ambiguous {
		finalize()
		return
	}
	a.startBackfill(backfillJob{
		target:    s.child,
		match:     spawnSessionMatch(s),
		notBefore: s.startedAtMs,
		finalize:  finalize,
		label:     "subagent " + s.id,
	})
}

// stoppedWith is the status of a nested call that its parent's end cut short.
func stoppedWith(status bgtask.Status) bgtask.Status {
	if status == bgtask.StatusFailed {
		return bgtask.StatusFailed
	}
	return bgtask.StatusStopped
}

// settleSpawns ends every call that the turn left running, with the status
// that the turn's end gives it.
func (a *Agent) settleSpawns(completion agent.MessageCompletion) {
	status := bgtask.StatusStopped
	if completion == agent.MessageCompletionError {
		status = bgtask.StatusFailed
	}
	a.Mu.Lock()
	open := append([]*spawnCall(nil), a.out.lead.spawns...)
	a.Mu.Unlock()
	for _, s := range open {
		a.finishSpawn(s, status, "")
	}
}

// removeSpawn returns spawns without s.
func removeSpawn(spawns []*spawnCall, s *spawnCall) []*spawnCall {
	kept := spawns[:0]
	for _, other := range spawns {
		if other != s {
			kept = append(kept, other)
		}
	}
	return kept
}

// firstLine returns the first non-blank line of text, trimmed.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// Child transcripts from Cline's store.
//
// Cline stores each finished subagent and each finished teammate task as a
// session of its own, a moment after it ends: `<root>__<agentId>` for a
// subagent and `<root>__teamtask__<agentId>__<suffix>` for a teammate task.
// Its session.list row states the root session, the agent and the task. The
// worker reads the finished session and writes its conversation into the
// child transcript, after what reached the transcript live.

// backfillWaits are the waits before each read of Cline's store. Cline writes a
// finished child's session on its own schedule, so the first reads can find it
// still running.
var backfillWaits = []time.Duration{0, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}

// backfillListLimit caps the rows of one session.list. The list is newest
// first, and the child ended a moment ago.
const backfillListLimit = 200

// storedStatusRunning is the status of a stored session that has not ended.
const storedStatusRunning = "running"

// storedSession is one row of session.list that a backfill reads.
type storedSession struct {
	SessionID string `json:"sessionId"`
	CreatedAt int64  `json:"createdAt"`
	Status    string `json:"status"`
	Metadata  struct {
		ParentSessionID string `json:"parentSessionId"`
		AgentID         string `json:"agentId"`
		Prompt          string `json:"prompt"`
	} `json:"metadata"`
}

// backfillJob is one child transcript that the worker writes from Cline's
// store.
type backfillJob struct {
	target *transcript
	// match selects the stored sessions that can be the child's.
	match func(storedSession) bool
	// notBefore is when the child started, in milliseconds since the Unix
	// epoch, or 0 when that is not known. A stored session created earlier is
	// not the child's.
	notBefore int64
	// prompt writes the stored session's first message as the child's prompt,
	// through parent, the sink that created the child. A teammate's task
	// reaches the worker only there.
	prompt bool
	parent agent.ProviderServices
	// finalize runs after the worker wrote the transcript, or after it gave up:
	// it writes the report and closes the row.
	finalize func()
	label    string
}

// spawnSessionMatch selects the stored sessions of the subagent that s started:
// a subagent of the same root session with the same task.
func spawnSessionMatch(s *spawnCall) func(storedSession) bool {
	return func(stored storedSession) bool {
		return stored.Metadata.ParentSessionID == s.rootSession &&
			stored.SessionID != s.rootSession &&
			!strings.Contains(stored.SessionID, teamTaskSessionMarker) &&
			strings.TrimSpace(stored.Metadata.Prompt) == s.task
	}
}

// startBackfill writes the child transcript of job from Cline's store on a
// goroutine of its own.
func (a *Agent) startBackfill(job backfillJob) {
	if a.ctx.Err() != nil {
		// The agent ends: no hub is left to read the child from, and a goroutine
		// would change the rows after Stop or Wait returned.
		job.finalize()
		return
	}
	a.background.Add(1)
	go func() {
		defer a.background.Done()
		defer job.finalize()
		a.backfill(job)
	}()
}

// backfill finds the child's stored session and writes it.
func (a *Agent) backfill(job backfillJob) {
	for _, wait := range backfillWaits {
		if wait > 0 {
			timer := a.clock.NewTimer(wait, "cline", "backfill")
			select {
			case <-timer.C:
			case <-a.ctx.Done():
				timer.Stop()
				return
			}
		}
		stored, finished, err := a.findStoredChild(job)
		if err != nil {
			slog.Warn("cline list the stored child sessions", "agent_id", a.AgentID(), "child", job.label, "error", err)
			continue
		}
		if !finished {
			continue
		}
		ctx, cancel := a.requestContext()
		messages, err := a.readMessages(ctx, stored.SessionID)
		cancel()
		if err != nil {
			slog.Warn("cline read the stored child session", "agent_id", a.AgentID(), "child", job.label, "session_id", stored.SessionID, "error", err)
			return
		}
		a.writeStoredConversation(job, stored.SessionID, messages)
		return
	}
	slog.Info("cline stored no finished session for a child", "agent_id", a.AgentID(), "child", job.label)
}

// findStoredChild returns the earliest stored session that job matches and no
// other child took, and whether it finished. A child takes a session only once
// it finished.
func (a *Agent) findStoredChild(job backfillJob) (storedSession, bool, error) {
	ctx, cancel := a.requestContext()
	defer cancel()
	reply, err := a.hub.command(ctx, commandSessionList, "", map[string]any{"limit": backfillListLimit})
	if err != nil {
		return storedSession{}, false, err
	}
	var list struct {
		Sessions []storedSession `json:"sessions"`
	}
	if err := json.Unmarshal(reply, &list); err != nil {
		return storedSession{}, false, fmt.Errorf("read the session list: %w", err)
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	var best *storedSession
	for i := range list.Sessions {
		stored := &list.Sessions[i]
		if a.out.claimed[stored.SessionID] || !job.match(*stored) {
			continue
		}
		// The daemon creates the child's session a moment after it publishes the
		// start of the call; the slack covers clocks that round differently.
		if job.notBefore > 0 && stored.CreatedAt > 0 && stored.CreatedAt < job.notBefore-backfillClockSlack {
			continue
		}
		if best == nil || stored.CreatedAt < best.CreatedAt {
			best = stored
		}
	}
	if best == nil || best.Status == storedStatusRunning {
		return storedSession{}, false, nil
	}
	a.out.claimed[best.SessionID] = true
	return *best, true, nil
}

// backfillClockSlack is how much earlier than the call's start Cline can create
// a child's session that still matches.
const backfillClockSlack = int64(time.Second / time.Millisecond)

// storedMessage is one message of Cline's stored conversation.
type storedMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// storedBlock is one content block of a stored message.
type storedBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// blocks returns the content blocks of a message. A message whose content is a
// string holds one text block.
func (m storedMessage) blocks() []storedBlock {
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []storedBlock{{Type: "text", Text: text}}
	}
	var blocks []storedBlock
	_ = json.Unmarshal(m.Content, &blocks)
	return blocks
}

// writeStoredConversation writes a stored conversation into the child
// transcript of job, in Cline's live event shapes: `reasoning.finished`,
// `assistant.finished`, `tool.started` and `tool.finished`. What reached the
// transcript live stays: the first texts and reasonings it counted, and every
// tool call it showed. The first user message is the task, which the
// transcript opens with already.
func (a *Agent) writeStoredConversation(job backfillJob, sessionID string, raw json.RawMessage) {
	var messages []storedMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		slog.Warn("cline stored child conversation cannot be read", "agent_id", a.AgentID(), "child", job.label, "error", err)
		return
	}
	t := job.target
	a.Mu.Lock()
	skipTexts, skipReasoning := t.written.texts, t.written.reasoning
	shown := maps.Clone(t.written.tools)
	a.Mu.Unlock()
	sink := a.sinkFor(t)

	opened := map[string]string{}
	var order []string
	for i, message := range messages {
		for _, block := range message.blocks() {
			switch {
			case message.Role == "user" && block.Type == "text":
				if i == 0 && job.prompt && job.parent != nil {
					if err := job.parent.PersistChildPrompt(t.childID, stripUserInput(block.Text)); err != nil {
						slog.Warn("cline persist the stored child prompt", "agent_id", a.AgentID(), "child", job.label, "error", err)
					}
				}
			case block.Type == "thinking":
				if skipReasoning > 0 {
					skipReasoning--
					continue
				}
				if strings.TrimSpace(block.Thinking) != "" {
					a.persistRow(t, eventRow(sessionID, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": block.Thinking}), noCompletion)
				}
			case block.Type == "text":
				if skipTexts > 0 {
					skipTexts--
					continue
				}
				if strings.TrimSpace(block.Text) != "" {
					a.persistRow(t, eventRow(sessionID, contracts.ClineEventAssistantFinished, map[string]any{"text": block.Text}), noCompletion)
				}
			case block.Type == "tool_use":
				if block.ID == "" || shown[block.ID] {
					continue
				}
				row := eventRow(sessionID, contracts.ClineEventToolStarted, map[string]any{
					"toolCallId": block.ID, "toolName": block.Name, "input": rawOrNull(block.Input),
				})
				if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: row}, block.ID, block.Name, startsSubagent(block.Name)); err != nil {
					slog.Warn("cline persist a stored tool call", "agent_id", a.AgentID(), "child", job.label, "error", err)
					continue
				}
				opened[block.ID] = block.Name
				order = append(order, block.ID)
			case block.Type == "tool_result":
				name, open := opened[block.ToolUseID]
				if !open {
					continue
				}
				delete(opened, block.ToolUseID)
				payload := map[string]any{"toolCallId": block.ToolUseID, "toolName": name, "output": rawOrNull(block.Content)}
				if block.IsError {
					payload["error"] = storedErrorText(block.Content)
				}
				a.closeToolRow(t, block.ToolUseID, name, eventRow(sessionID, contracts.ClineEventToolFinished, payload), noCompletion)
			}
		}
	}
	// A call with no stored result did not finish.
	for _, id := range order {
		if name, open := opened[id]; open {
			row := eventRow(sessionID, contracts.ClineEventToolStarted, map[string]any{"toolCallId": id, "toolName": name})
			a.closeToolRow(t, id, name, row, agent.MessageCompletionError)
		}
	}
}

// rawOrNull returns raw, or JSON null for an empty value.
func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// storedErrorText is the text of a failed tool result: the string it holds, or
// its JSON.
func storedErrorText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

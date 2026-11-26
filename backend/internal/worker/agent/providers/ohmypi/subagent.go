package ohmypi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var (
	_ agent.ChildSteerer     = (*Agent)(nil)
	_ agent.ChildInterrupter = (*Agent)(nil)
)

// childForKeyLocked resolves the complete stored identity, including its session.
// The caller holds Process.Mu. A completed child is absent from this map.
func (a *Agent) childForKeyLocked(key string) *subagentState {
	for _, child := range a.subagents {
		if child.rowKey == key {
			return child
		}
	}
	return nil
}

// ActiveChildTurnState reports whether native child events describe a running turn.
func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	child := a.childForKeyLocked(childKey)
	active := child != nil && child.conv != nil && child.conv.childLoopActive()
	return agent.TurnState{Active: active, Steerable: active}
}

// SendChildInput starts a child round, or requires an explicit steer during a running round.
func (a *Agent) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	receipt, err := a.sendChildInput(childKey, agent.SteerRequest{
		InputID: input.InputID, Content: input.Content, Attachments: input.Attachments,
	}, false)
	return agent.ChildInputResult{StartsTurn: true, NativeReceipt: receipt}, err
}

// SteerChildInput delivers input to the child's current native round.
func (a *Agent) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	_, err := a.sendChildInput(childKey, request, true)
	return agent.NativeInputReceipt{}, err
}

// ChildSteerPolicy holds the steer row until echo and keeps it across a racing yield.
func (*Agent) ChildSteerPolicy() agent.SteerPolicy {
	return agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: true}
}

func (a *Agent) sendChildInput(childKey string, request agent.SteerRequest, steer bool) (agent.NativeInputReceipt, error) {
	var receipt agent.NativeInputReceipt
	// Native child input carries a message string only, unlike the root prompt's image array.
	for _, attachment := range agent.ClassifyAttachments(request.Attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindText {
			return receipt, fmt.Errorf("the Oh My Pi child message supports text attachments only")
		}
	}
	payload := promptPayload(request.Content, request.Attachments)
	message := payload["message"].(string)
	if strings.TrimSpace(message) == "" {
		return receipt, fmt.Errorf("the child message must not be empty")
	}
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	child := a.childForKeyLocked(childKey)
	if child == nil || child.conv == nil {
		a.Mu.Unlock()
		return receipt, agent.ErrChildRouteNotReady
	}
	id := child.id
	active := child.conv.childLoopActive()
	if active && !steer {
		a.Mu.Unlock()
		return receipt, agent.ErrAgentBusy
	}
	if !active && steer {
		a.Mu.Unlock()
		return receipt, agent.ErrNoActiveTurn
	}
	steerAtEcho := steer && request.InputID != "" && request.AttemptID != ""
	if request.InputID != "" && (!steer || steerAtEcho) &&
		a.steerBridgeReady && child.conv.sessionFile != "" && !strings.HasPrefix(strings.TrimSpace(message), "/") {
		receipt = a.registerOMPChildInputLocked(child.conv, request, steerAtEcho)
		payload["message"] = a.steerBridge.ChildPrefix + receipt.InputID + "\n" + message
	}
	a.Mu.Unlock()
	payload["subagentId"] = id
	wait, err := a.beginCommand(CommandSteerSubagent, payload)
	if err != nil {
		a.forgetOMPChildInput(child.conv, receipt.InputID)
		return agent.NativeInputReceipt{}, err
	}
	_, err = wait(a.APITimeout())
	var refused *commandError
	if err != nil && !errors.As(err, &refused) {
		if !steer {
			// Ordinary sends return an empty receipt on ErrDeliveryUncertain, so
			// forget the unrecorded input ID rather than publishing an orphan reply.
			a.forgetOMPChildInput(child.conv, receipt.InputID)
		}
		// A lost acknowledgement cannot establish whether native input was accepted.
		return agent.NativeInputReceipt{}, fmt.Errorf("%w: send the Oh My Pi child message: %w", agent.ErrDeliveryUncertain, err)
	}
	if err != nil {
		a.forgetOMPChildInput(child.conv, receipt.InputID)
		return agent.NativeInputReceipt{}, err
	}
	if steerAtEcho && receipt.InputID == "" {
		// The unmarked path records native admission, without an exact receipt.
		a.reportOMPSteerTo(child.conv.sink, agent.SteerResolution{
			InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeEchoed,
		})
	}
	return receipt, nil
}

// InterruptChild cancels one owned child. Native lifecycle events close its transcript.
func (a *Agent) InterruptChild(childKey string, _ agent.StopContext) error {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	child := a.childForKeyLocked(childKey)
	if child == nil {
		a.Mu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	id := child.id
	a.Mu.Unlock()
	_, err := a.sendCommand(CommandCancelSubagent, map[string]any{"subagentId": id}, a.APITimeout())
	return err
}

// omp's subagents.
//
// The `task` tool starts one or more subagents: one for each entry of its `tasks`
// list. omp reports each of them through three frames, which the worker
// subscribes to at startup (`set_subagent_subscription {level:"events"}`):
//
//   - subagent_lifecycle states that a subagent started and how it ended. It
//     carries the subagent's id and the id of the `task` call that started it.
//   - subagent_progress states what a running subagent does now, and the label
//     of its task once omp has one (see handleSubagentProgress).
//   - subagent_event carries one session event of the subagent: its messages and
//     its tool calls, in the same frames the session's own stream uses.
//
// Each subagent gets a registry row and a child transcript. Its events drive a
// conversation of its own, written to the child sink. The row shows what omp's
// own task view shows: omp's id for the subagent as its title, and the label of
// the task as its description, or the first line of the task until a label
// arrives.
//
// By default omp runs a subagent in the BACKGROUND: the `task` call ends at once,
// the parent's run ends, and omp starts a new run when the subagent yields its
// result. The lifecycle frame, not the `task` call's end, is what ends the row.

// subagentState is one subagent the worker follows. Guarded by Process.Mu.
type subagentState struct {
	// id is omp's agent id, "ScoutOne". A subagent of a subagent has the id
	// "<parent id>.<name>".
	id         string
	rowKey     string
	childID    string
	title      string
	parentSink agent.ProviderServices
	conv       *conversation
	// description is the description that the row shows: the label of the task
	// once omp states one, and the first line of the task before that. An
	// unchanged label is not written again.
	description string
	// activeForm is the last progress line the row showed, so an unchanged line
	// is not written again.
	activeForm string
	// report is what the subagent yielded, the row's report when it ends.
	report string
}

// rowUpsert returns the registry row of a running subagent, from what the worker
// knows of it. The caller holds Process.Mu, or owns a state that no map holds yet.
//
// The row carries every field, not only the one that changed, so a write that
// finds no row, because the start's upsert was refused, writes a whole row.
func (s *subagentState) rowUpsert() bgtask.Upsert {
	return bgtask.Upsert{
		RowKey:       s.rowKey,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: s.childID,
		Title:        s.title,
		Description:  s.description,
		ActiveForm:   s.activeForm,
		Status:       bgtask.StatusRunning,
	}
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
	SessionFile      string `json:"sessionFile"`
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
		a.endSubagent(payload.ID, bgtask.StatusSucceeded)
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
	// The start states a description only for a label that the caller supplied.
	// Otherwise omp labels the task later, in a progress frame, and the row shows
	// the first line of the task until then.
	description := firstLine(payload.Description)
	if description == "" {
		description = firstLine(spec.Task)
	}
	childID := ""
	id, err := parentSink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		SpawnSpanID:      payload.ParentToolCallID,
		ProviderChildKey: rowKey,
		AgentSessionID:   payload.SessionFile,
		Title:            title,
	})
	if err != nil {
		slog.Warn("omp subagent child transcript failed", "agent_id", a.AgentID(), "subagent", payload.ID, "error", err)
	} else {
		childID = id
	}
	if _, status, found, err := a.sink.LookupBackgroundTask(rowKey); err != nil {
		slog.Warn("omp subagent lookup failed", "agent_id", a.AgentID(), "subagent", payload.ID, "error", err)
	} else if found && status.IsFinished() {
		providerkit.LogRegistryRefusal("ohmypi", "revive", a.sink.ReviveBackgroundTask(rowKey))
	}
	state := &subagentState{id: payload.ID, rowKey: rowKey, childID: childID, title: title, parentSink: parentSink, description: description}
	if childID != "" {
		if childSink := parentSink.ChildSink(childID); childSink != nil {
			state.conv = newConversation(childSink, childID)
			state.conv.parentSink = parentSink
			state.conv.sessionFile = payload.SessionFile
		}
	}
	row := state.rowUpsert()
	a.Mu.Lock()
	if a.subagents == nil {
		a.subagents = make(map[string]*subagentState)
	}
	a.subagents[payload.ID] = state
	a.Mu.Unlock()
	providerkit.LogRegistryRefusal("ohmypi", "upsert", a.sink.UpsertBackgroundTask(row))
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
		a.retireOMPChildInputs(state.conv)
		a.persistIncompleteTools(state.conv, completionForStatus(status))
		providerkit.ResetSpansAndProgress(state.conv.sink)
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
		ownerSink := state.parentSink
		if ownerSink == nil {
			ownerSink = a.sink
		}
		ownerSink.CleanupChildAgent(state.childID)
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

// subagentStatusForCompletion returns the registry status when a subagent session ends.
func subagentStatusForCompletion(completion agent.MessageCompletion) bgtask.Status {
	switch completion {
	case agent.MessageCompletionError:
		return bgtask.StatusFailed
	case agent.MessageCompletionComplete:
		return bgtask.StatusSucceeded
	case agent.MessageCompletionFinished:
		return bgtask.StatusEndedWithUnknownOutcome
	default:
		return bgtask.StatusStopped
	}
}

// completionForStatus returns the completion for a subagent's unfinished output.
func completionForStatus(status bgtask.Status) agent.MessageCompletion {
	if status == bgtask.StatusEndedWithUnknownOutcome {
		return agent.MessageCompletionFinished
	}
	if status == bgtask.StatusFailed {
		return agent.MessageCompletionError
	}
	return agent.MessageCompletionInterrupted
}

// handleSubagentProgress shows a running subagent's latest activity and the label
// of its task on its row.
//
// omp labels a task with its tiny model, in the background, after the subagent
// started, and sets `progress.description` once to the label. Each later frame
// repeats it. omp omits the field before the label and when no tiny model is
// configured, and it never sets an empty label. A label that the caller supplied
// is in the start frame already, and each progress frame repeats it, so it writes
// nothing again.
//
// omp's RPC layer drops a progress frame of a subagent that did not start or
// that ended, so a label reaches only a running subagent. The last progress
// frame precedes the lifecycle frame that ends the subagent, so the end states
// no label that a progress frame did not.
func (a *Agent) handleSubagentProgress(raw []byte) {
	var frame struct {
		Payload struct {
			Progress struct {
				ID string `json:"id"`
				// Description is decoded apart (see taskLabel), so a value of
				// another type drops the label and keeps the rest of the frame.
				Description  json.RawMessage `json:"description"`
				RecentOutput []string        `json:"recentOutput"`
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
	label := taskLabel(progress.Description)
	a.Mu.Lock()
	state := a.subagents[progress.ID]
	if state == nil {
		a.Mu.Unlock()
		return
	}
	labelChanged := label != "" && label != state.description
	if labelChanged {
		state.description = label
	}
	activityChanged := activeForm != "" && activeForm != state.activeForm
	if activityChanged {
		state.activeForm = activeForm
	}
	row := state.rowUpsert()
	a.Mu.Unlock()
	switch {
	case labelChanged:
		// The row carries the activity too, so one write states both changes.
		providerkit.LogRegistryRefusal("ohmypi", "upsert", a.sink.UpsertBackgroundTask(row))
	case activityChanged:
		providerkit.LogRegistryRefusal("ohmypi", "status", a.sink.UpdateBackgroundTaskStatus(row.RowKey, bgtask.StatusRunning, row.ActiveForm))
	}
}

// taskLabel reads omp's label of a task from `progress.description`: the first
// line of a string. An absent field, a blank string, and a value of another type
// state no label.
func taskLabel(raw json.RawMessage) string {
	description, ok := providerkit.RawString(raw)
	if !ok {
		return ""
	}
	return firstLine(description)
}

// handleSubagentEvent drives one subagent's conversation with one of its session
// events.
//
// The event is a frame of the subagent's own stream, in the shape the session's
// stream uses. Messages, tools, and notices reach the child transcript.
// A compaction start updates the child's live state. A run end stores its divider.
// The worker consumes redundant deltas and lifecycle frames.
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
	if a.dispatchConversationEvent(state.conv, head.Type, event) {
		return
	}
	switch head.Type {
	case contracts.OhMyPiEventAgentStart:
		a.startChildRun(state.conv)
	case contracts.OhMyPiEventAgentEnd:
		a.endChildRun(state.conv, event)
	case ompEventAgentPause:
		a.pauseChildRun(state.conv, event)
	case contracts.OhMyPiEventAutoRetryStart, contracts.OhMyPiEventAutoRetryEnd,
		contracts.OhMyPiEventNotice, contracts.OhMyPiEventExtensionError, contracts.OhMyPiEventCommandOutput:
		a.persistConversationNotification(state.conv, event)
	case contracts.OhMyPiEventMessageUpdate, contracts.OhMyPiEventTurnStart,
		contracts.OhMyPiEventToolStreamUpdate, contracts.OhMyPiEventQueueUpdate,
		contracts.OhMyPiEventSessionSettled, contracts.OhMyPiEventCacheWarmingStart,
		contracts.OhMyPiEventCacheWarmingEnd, contracts.OhMyPiEventAdvisorCostChanged:
		// Finished messages and tool events already carry this transcript content.
		// Queue state, cache warming, and advisor costs supply no child transcript row.
	default:
		a.persistRaw(state.conv, event)
	}
}

// startChildRun marks the start of a subagent's run, for the duration of its
// turn-end row.
func (a *Agent) startChildRun(c *conversation) {
	now := a.Clock().Now()
	a.Mu.Lock()
	c.ttsrPending = false
	c.runStartedAt = now
	c.awaitingWake = false
	c.toolUses = 0
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	providerkit.PublishSteerableTurnActiveTo(c.sink, true, seq)
}

func (a *Agent) pauseChildRun(c *conversation, raw []byte) {
	var env agentEndEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	a.finishOMPChildReplyAtYield(c, env, false)
	if env.Yielded == nil || !*env.Yielded || !env.Awaiting {
		return
	}
	a.Mu.Lock()
	c.awaitingWake = true
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	providerkit.PublishSteerableTurnActiveTo(c.sink, false, seq)
}

// endChildRun persists a subagent's turn-end row. A subagent that a message from
// its parent wakes runs again, so each run ends with its own row, and the registry
// row closes only with the lifecycle frame.
func (a *Agent) endChildRun(c *conversation, raw []byte) {
	var env agentEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("omp subagent agent_end decode failed", "agent_id", a.AgentID(), "error", err)
	}
	a.finishOMPChildReplyAtYield(c, env, true)
	endedAt := a.Clock().Now()
	a.Mu.Lock()
	c.ttsrPending = false
	startedAt := c.runStartedAt
	c.runStartedAt = time.Time{}
	c.awaitingWake = false
	toolUses := c.toolUses
	c.toolUses = 0
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	interrupted := env.statesAborted()
	if interrupted {
		a.persistIncompleteTools(c, agent.MessageCompletionInterrupted)
	}
	content := agentEndContent(raw, usageSnapshot{}, providerkit.TurnDurationMillis(startedAt, endedAt))
	if err := c.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
		slog.Warn("omp persist subagent agent_end", "agent_id", a.AgentID(), "child_agent_id", c.childID, "error", err)
	}
	if interrupted {
		providerkit.ResetSpansAndProgress(c.sink)
	}
	providerkit.PublishSteerableTurnActiveTo(c.sink, false, seq)
}

// persistChildPrompt opens a child transcript with the prompt its parent gave the
// subagent: the subagent's first user message. Later user messages are no-ops,
// because PersistChildPrompt writes only into an empty transcript.
func (a *Agent) persistChildPrompt(c *conversation, content json.RawMessage) {
	text := messageText(content)
	if strings.TrimSpace(text) == "" {
		return
	}
	ownerSink := c.parentSink
	if ownerSink == nil {
		ownerSink = a.sink
	}
	if err := ownerSink.PersistChildPrompt(c.childID, text); err != nil {
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
	if json.Unmarshal(result, &envelope) != nil || !providerkit.RawPresent(envelope.Details.Data) {
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

// shellJobStatus maps an omp async shell job status to a final registry status.
// An absent status defaults to StatusSucceeded because older async-result frames
// omit status on normal completion.
func shellJobStatus(status string) bgtask.Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return bgtask.StatusFailed
	case "aborted", "cancelled", "killed", "stopped":
		return bgtask.StatusStopped
	case "completed", "succeeded", "":
		return bgtask.StatusSucceeded
	default:
		return bgtask.StatusEndedWithUnknownOutcome
	}
}

func isNonFinalShellStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "pending", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func (a *Agent) closeTrackedShell(jobID string, status bgtask.Status) {
	if jobID == "" {
		return
	}
	a.Mu.Lock()
	rowKey, ok := a.shells[jobID]
	delete(a.shells, jobID)
	a.Mu.Unlock()
	if ok {
		providerkit.LogRegistryRefusal("ohmypi", "close", a.sink.CloseBackgroundTask(rowKey, status))
	}
}

// closeDeliveredShells closes the SHELL rows of the jobs an async-result message
// delivers. The message states each finished job in `details.jobs`.
func (a *Agent) closeDeliveredShells(details json.RawMessage) {
	var envelope struct {
		Jobs []struct {
			JobID  string `json:"jobId"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if len(details) == 0 || json.Unmarshal(details, &envelope) != nil {
		return
	}
	for _, job := range envelope.Jobs {
		a.closeTrackedShell(job.JobID, shellJobStatus(job.Status))
	}
}

// closeWaitedShells closes tracked SHELL rows that a `wait` tool call resolved.
// Waiting on a background job suppresses a later async-result frame.
func (a *Agent) closeWaitedShells(args, result json.RawMessage, isError bool) {
	type waitedJob struct {
		JobID  string `json:"jobId"`
		ID     string `json:"id"`
		Status string `json:"status"`
		State  string `json:"state"`
	}
	var envelope toolResultEnvelope
	var details struct {
		Jobs   []waitedJob `json:"jobs"`
		JobID  string      `json:"jobId"`
		ID     string      `json:"id"`
		Status string      `json:"status"`
		State  string      `json:"state"`
	}
	if len(result) > 0 && json.Unmarshal(result, &envelope) == nil && len(envelope.Details) > 0 {
		_ = json.Unmarshal(envelope.Details, &details)
	}
	firstNonEmpty := func(a, b string) string {
		if strings.TrimSpace(a) != "" {
			return strings.TrimSpace(a)
		}
		return strings.TrimSpace(b)
	}
	if len(details.Jobs) > 0 {
		for _, job := range details.Jobs {
			id := firstNonEmpty(job.JobID, job.ID)
			st := firstNonEmpty(job.Status, job.State)
			if id == "" || isNonFinalShellStatus(st) {
				continue
			}
			a.closeTrackedShell(id, shellJobStatus(st))
		}
		return
	}
	if id := firstNonEmpty(details.JobID, details.ID); id != "" {
		st := firstNonEmpty(details.Status, details.State)
		if !isNonFinalShellStatus(st) {
			a.closeTrackedShell(id, shellJobStatus(st))
		}
		return
	}
	if isError || len(args) == 0 {
		return
	}
	var call struct {
		JobID  string   `json:"jobId"`
		ID     string   `json:"id"`
		JobIDs []string `json:"jobIds"`
		IDs    []string `json:"ids"`
	}
	if json.Unmarshal(args, &call) != nil {
		return
	}
	st := firstNonEmpty(details.Status, details.State)
	if isNonFinalShellStatus(st) {
		return
	}
	status := shellJobStatus(st)
	if id := firstNonEmpty(call.JobID, call.ID); id != "" {
		a.closeTrackedShell(id, status)
	}
	for _, id := range call.JobIDs {
		a.closeTrackedShell(strings.TrimSpace(id), status)
	}
	for _, id := range call.IDs {
		a.closeTrackedShell(strings.TrimSpace(id), status)
	}
}

const (
	procURIPrefix  = "proc://"
	procKillSuffix = "/kill"
)

func procKillJobID(target string) (string, bool) {
	rest, ok := strings.CutPrefix(target, procURIPrefix)
	if !ok {
		return "", false
	}
	jobID, ok := strings.CutSuffix(rest, procKillSuffix)
	if !ok || jobID == "" || strings.Contains(jobID, "/") {
		return "", false
	}
	return jobID, true
}

// closeCancelledShell closes a tracked SHELL row when a `write` call targets
// `proc://<jobID>/kill`, which cancels the job and suppresses async-result.
func (a *Agent) closeCancelledShell(args json.RawMessage) {
	if len(args) == 0 {
		return
	}
	a.Mu.Lock()
	empty := len(a.shells) == 0
	a.Mu.Unlock()
	if empty {
		return
	}
	var call struct {
		Path string `json:"path"`
		URI  string `json:"uri"`
	}
	if json.Unmarshal(args, &call) != nil {
		return
	}
	target := strings.TrimSpace(call.Path)
	if target == "" {
		target = strings.TrimSpace(call.URI)
	}
	jobID, ok := procKillJobID(target)
	if !ok {
		return
	}
	a.closeTrackedShell(jobID, bgtask.StatusStopped)
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

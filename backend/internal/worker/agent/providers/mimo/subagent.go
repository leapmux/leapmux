package mimo

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// MiMo's subagents.
//
// A subagent is an ACTOR that runs inside the parent's own session. The model
// starts one with the `actor` tool: `spawn` returns at once and runs the actor
// in the background, and `run` blocks until it finishes. Each message the actor
// writes names it in `agentID`, which is how its parts reach a child transcript
// rather than the parent's (output.go routes them).
//
// The spawn call and the actor meet on the call's running update, whose
// `metadata.actorId` names the actor. That update arrives before the actor's
// first message, so the child transcript exists by the time its rows do. An
// actor that no call started -- one a workflow spawned -- gets its transcript on
// its first row instead.
//
// actor.status reports each turn that the actor tool runs: running opens (or
// reopens) the registry row, and idle closes it with the outcome. A message to a
// running actor joins its turn. A message that wakes an IDLE actor runs a turn
// that no actor.status reports: the parent's `actor send` does that, and so
// would LeapMux's own child input, which sendChildInput therefore refuses. The
// rows of such a turn still reach the actor's transcript, because each message
// names its actor, but its registry row stays closed.

// actorModeMain is the mode of the session's own actor.
const actorModeMain = "main"

// mimoActor is what the worker knows about one subagent.
type mimoActor struct {
	id          string
	description string
	agentType   string
	background  bool
	// rowKey is the registry row key and the child key: the spawn call's id when
	// a call started the actor, else the session and the actor id.
	rowKey      string
	spawnCallID string
	// childAgentID is the child transcript, once it exists.
	childAgentID string
	// running is true while the actor runs a turn.
	running bool
	// closed is true while the registry row holds a final status.
	closed    bool
	turnCount int
	// lastText is the actor's last finished text, which its report states.
	lastText string
	// workflowRunID is the workflow run that spawned the actor, if any.
	workflowRunID string
}

// title labels the actor's tab and its registry row: its description, else its
// agent type, else its id.
func (actor *mimoActor) title() string {
	for _, candidate := range []string{actor.description, actor.agentType, actor.id} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return bgtask.CleanTitleRunes(bgtask.FirstLine(trimmed), 80)
		}
	}
	return "MiMo subagent"
}

// runningActorsLocked counts the subagents that run a turn. The caller holds
// a.Mu.
func (a *Agent) runningActorsLocked() int {
	running := 0
	for _, actor := range a.actors {
		if actor.running {
			running++
		}
	}
	return running
}

// actorLocked returns the actor record for id, creating it on first sight. The
// caller holds a.Mu.
func (a *Agent) actorLocked(actorID string) *mimoActor {
	actor := a.actors[actorID]
	if actor == nil {
		actor = &mimoActor{id: actorID}
		a.actors[actorID] = actor
	}
	return actor
}

// mimoSpawnInput is the part of an actor call's input the worker reads.
type mimoSpawnInput struct {
	Operation struct {
		Action       string `json:"action"`
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
	} `json:"operation"`
}

func spawnInput(input json.RawMessage) mimoSpawnInput {
	var in mimoSpawnInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			slog.Debug("mimo actor input unmarshal failed", "error", err)
		}
	}
	return in
}

// isSpawnCall reports whether a tool part starts a subagent: an actor call
// whose operation is spawn or run. The other actor operations address an actor
// that already exists.
func isSpawnCall(part mimoPart) bool {
	if part.Tool != contracts.MiMoToolActor || part.State == nil {
		return false
	}
	action := spawnInput(part.State.Input).Operation.Action
	return action == contracts.MiMoActorActionSpawn || action == contracts.MiMoActorActionRun
}

// mimoReturnFormatMarker opens the instruction that MiMo appends to the first
// message of a general subagent (actor/spawn.ts, RETURN_FORMAT_INSTRUCTION), at
// v0.1.14 and at HEAD. It asks the subagent for a report header. It is MiMo's
// own text, not the task that the subagent was given.
const mimoReturnFormatMarker = "\n\n---\n\n## Return format (required)"

// subagentInstruction is the task in an actor's first user message: the text
// before the return-format instruction that MiMo appended, trimmed. A task can
// quote the heading itself, so only the last one is cut.
func subagentInstruction(text string) string {
	if index := strings.LastIndex(text, mimoReturnFormatMarker); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

// spawnPrompt reads the instruction a spawn gave its subagent, which opens the
// child transcript.
func spawnPrompt(input json.RawMessage) string {
	return strings.TrimSpace(spawnInput(input).Operation.Prompt)
}

// spawnTitle reads the description a spawn gave its subagent.
func spawnTitle(part mimoPart) string {
	if part.State == nil {
		return ""
	}
	in := spawnInput(part.State.Input).Operation
	if description := strings.TrimSpace(in.Description); description != "" {
		return description
	}
	if title := strings.TrimSpace(part.State.Title); title != "" {
		return title
	}
	return strings.TrimSpace(in.SubagentType)
}

// mimoActorRegistered is actor.registered.
type mimoActorRegistered struct {
	SessionID   string `json:"sessionID"`
	ActorID     string `json:"actorID"`
	Mode        string `json:"mode"`
	Description string `json:"description"`
	Agent       string `json:"agent"`
	Background  bool   `json:"background"`
}

func (a *Agent) handleActorRegistered(event mimoEvent) {
	var payload mimoActorRegistered
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo actor.registered unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if payload.ActorID == "" || payload.ActorID == mainActorID || payload.Mode == actorModeMain {
		return
	}
	a.Mu.Lock()
	if !a.ownsSessionLocked(payload.SessionID) {
		// A peer actor registers in its own child session, whose events this agent
		// does not read.
		a.Mu.Unlock()
		return
	}
	actor := a.actorLocked(payload.ActorID)
	if description := strings.TrimSpace(payload.Description); description != "" {
		actor.description = description
	}
	actor.agentType = payload.Agent
	actor.background = payload.Background
	// The registration states no workflow, so an actor that registers while one
	// run is active is taken as that run's. A spawn call's actor registers before
	// the call's update names it, so linkSpawn undoes this guess for it.
	if actor.workflowRunID == "" && actor.spawnCallID == "" {
		actor.workflowRunID = a.soleRunningWorkflowLocked()
	}
	a.Mu.Unlock()
}

// linkSpawn records that a spawn call started an actor, and gives the actor its
// child transcript. The call's id becomes the actor's row key, because it is the
// one identifier both the call and the actor's registry row can carry.
func (a *Agent) linkSpawn(callID, actorID, title string) {
	if callID == "" || actorID == "" || actorID == mainActorID {
		return
	}
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	if actor.spawnCallID == "" {
		actor.spawnCallID = callID
		if actor.rowKey == "" {
			actor.rowKey = callID
		}
		// A workflow's actor has no spawn call, so an actor that a call started
		// is the main agent's own, whichever workflow ran when it registered.
		actor.workflowRunID = ""
		a.spawnActors[callID] = actorID
	}
	if actor.description == "" {
		actor.description = strings.TrimSpace(title)
	}
	a.Mu.Unlock()
	a.ensureActorTranscript(actorID)
}

// ensureActorTranscript returns the child transcript of an actor, creating it,
// its registry row and its opening prompt on first sight. It reports false when
// the worker could not create the transcript.
func (a *Agent) ensureActorTranscript(actorID string) (string, bool) {
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	if actor.childAgentID != "" {
		childID := actor.childAgentID
		a.Mu.Unlock()
		return childID, true
	}
	if actor.rowKey == "" {
		// No call started this actor, so it gets a key of its own. The session id
		// keeps an actor id that a later session reuses apart from this one.
		actor.rowKey = a.sessionID + "/" + actorID
	}
	rowKey, spawnSpan, title := actor.rowKey, actor.spawnCallID, actor.title()
	if spawnSpan == "" {
		spawnSpan = rowKey
	}
	a.Mu.Unlock()

	childID, err := a.sink.EnsureChildAgent(spawnSpan, rowKey, title)
	if err != nil {
		slog.Warn("mimo subagent ensure child failed", "agent_id", a.AgentID(), "actor_id", actorID, "error", err)
		return "", false
	}
	a.Mu.Lock()
	actor = a.actorLocked(actorID)
	first := actor.childAgentID == ""
	actor.childAgentID = childID
	closed := actor.closed
	a.Mu.Unlock()
	if !first {
		return childID, true
	}
	if prompt := a.spawnPrompts.Take(spawnSpan); prompt != "" {
		if err := a.sink.PersistChildPrompt(childID, prompt); err != nil {
			slog.Warn("mimo subagent persist prompt failed", "agent_id", a.AgentID(), "child", childID, "error", err)
		}
	}
	if !closed {
		a.upsertActorRow(actorID, bgtask.StatusRunning)
	}
	return childID, true
}

// upsertActorRow writes the actor's registry row with status.
func (a *Agent) upsertActorRow(actorID string, status bgtask.Status) {
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	upsert := bgtask.Upsert{
		RowKey:       actor.rowKey,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: actor.childAgentID,
		Title:        actor.title(),
		Description:  actor.agentType,
		Status:       status,
	}
	if workflow := a.workflows[actor.workflowRunID]; workflow != nil {
		upsert.GroupKey = workflow.groupKey()
		upsert.GroupLabel = workflow.label()
	}
	a.Mu.Unlock()
	if upsert.RowKey == "" {
		return
	}
	providerkit.LogRegistryRefusal("mimo", "upsert", a.sink.UpsertBackgroundTask(upsert))
}

// mimoActorStatus is actor.status.
type mimoActorStatus struct {
	SessionID   string `json:"sessionID"`
	ActorID     string `json:"actorID"`
	Status      string `json:"status"`
	LastOutcome string `json:"lastOutcome"`
	TurnCount   int    `json:"turnCount"`
	Error       string `json:"error"`
}

func (a *Agent) handleActorStatus(event mimoEvent) {
	var payload mimoActorStatus
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo actor.status unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if payload.ActorID == "" || payload.ActorID == mainActorID {
		return
	}
	a.Mu.Lock()
	if !a.ownsSessionLocked(payload.SessionID) {
		a.Mu.Unlock()
		return
	}
	actor := a.actorLocked(payload.ActorID)
	actor.turnCount = payload.TurnCount
	wasRunning, wasClosed := actor.running, actor.closed
	a.Mu.Unlock()

	switch payload.Status {
	case contracts.MiMoActorStatusRunning:
		a.startActorTurn(payload.ActorID, wasRunning, wasClosed)
	case contracts.MiMoActorStatusIdle:
		if wasRunning || !wasClosed {
			a.endActorTurn(payload)
		}
	default:
		// pending: the actor waits for its first turn, and its row opens when the
		// turn starts.
	}
}

// startActorTurn opens the actor's registry row for a turn. A row that a
// previous turn closed is revived: a running status after an idle one is the
// positive proof of a restart that ReviveBackgroundTask requires.
func (a *Agent) startActorTurn(actorID string, wasRunning, wasClosed bool) {
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	actor.running = true
	actor.closed = false
	rowKey := actor.rowKey
	a.Mu.Unlock()
	if wasRunning {
		return
	}
	if _, ok := a.ensureActorTranscript(actorID); !ok {
		return
	}
	if wasClosed && rowKey != "" {
		providerkit.LogRegistryRefusal("mimo", "revive", a.sink.ReviveBackgroundTask(rowKey))
	} else {
		a.upsertActorRow(actorID, bgtask.StatusRunning)
	}
	a.publishChildTurn(actorID)
}

// publishChildTurn reports an actor's turn on its own tab, from the running flag
// that the caller wrote. The tab's input queue then holds a message while the
// turn runs, and offers it as a steer, which a running actor takes. The token
// comes from the same critical section as the flag.
func (a *Agent) publishChildTurn(actorID string) {
	a.Mu.Lock()
	actor := a.actors[actorID]
	if actor == nil || actor.childAgentID == "" {
		a.Mu.Unlock()
		return
	}
	childID, active := actor.childAgentID, actor.running
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	providerkit.PublishSteerableTurnActiveTo(a.sink.ChildSink(childID), active, seq)
}

// endActorTurn closes one turn of an actor: what it left unfinished is
// persisted, its registry row takes the turn's outcome, and a background actor
// reports its last text to the parent.
func (a *Agent) endActorTurn(payload mimoActorStatus) {
	status, completion := actorOutcomeStatus(payload.LastOutcome)
	a.flushActorText(payload.ActorID, completion)
	a.closeUnfinishedTools(payload.ActorID, completion)
	childID, ok := a.ensureActorTranscript(payload.ActorID)

	a.Mu.Lock()
	actor := a.actorLocked(payload.ActorID)
	actor.running = false
	actor.closed = true
	rowKey, title, background, report := actor.rowKey, actor.title(), actor.background, actor.lastText
	actor.lastText = ""
	a.Mu.Unlock()
	a.retireActorControls(payload.ActorID)

	if ok && payload.Error != "" {
		a.persistChildText(childID, payload.Error, agent.MessageCompletionError)
	}
	// The turn ends on the subagent's tab after its last row, and before the
	// caches of the tab go.
	a.publishChildTurn(payload.ActorID)
	if background && strings.TrimSpace(report) != "" && rowKey != "" {
		providerkit.PersistSubagentReport(a.sink, agent.SubagentReportWrite{
			ReportID: "mimo-report:" + rowKey + ":" + strconv.Itoa(payload.TurnCount),
			Report: agent.SubagentReport{
				Label:  title,
				Text:   report,
				Status: bgtask.StatusWire(status),
			},
		})
	}
	if rowKey != "" {
		providerkit.LogRegistryRefusal("mimo", "close", a.sink.CloseBackgroundTask(rowKey, status))
	}
	if ok {
		// The actor can run again, and the service re-creates the child's caches on
		// its next row. Holding them for an idle actor would keep every past
		// subagent's span tracker for the life of the session.
		a.sink.CleanupChildAgent(childID)
	}
	a.flushHeldFailure()
}

// actorOutcomeStatus maps an actor's outcome onto the registry status and the
// completion its unfinished rows take.
func actorOutcomeStatus(outcome string) (bgtask.Status, agent.MessageCompletion) {
	switch outcome {
	case contracts.MiMoActorOutcomeFailure:
		return bgtask.StatusFailed, agent.MessageCompletionError
	case contracts.MiMoActorOutcomeCancelled:
		return bgtask.StatusStopped, agent.MessageCompletionInterrupted
	case contracts.MiMoActorOutcomeSuccess, "":
		return bgtask.StatusCompleted, agent.MessageCompletionComplete
	default:
		// An outcome this build does not know still ended the turn. Completed is
		// the reading that claims no failure the actor did not report.
		slog.Debug("mimo unknown actor outcome", "outcome", outcome)
		return bgtask.StatusCompleted, agent.MessageCompletionComplete
	}
}

// persistChildText writes one line of text into a child transcript.
func (a *Agent) persistChildText(childID, text string, completion agent.MessageCompletion) {
	raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, completion)
	if err != nil {
		slog.Warn("mimo subagent text marshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{}); err != nil {
		slog.Warn("mimo subagent text persist failed", "agent_id", a.AgentID(), "error", err)
	}
}

// rememberActorText keeps an actor's last finished text for its report.
func (a *Agent) rememberActorText(actorID, text string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if actor := a.actors[actorID]; actor != nil {
		actor.lastText = text
	}
}

// actorStatusForCompletion is the registry status of a subagent that the end of
// its session or of its process ended, for the completion of that end.
func actorStatusForCompletion(completion agent.MessageCompletion) bgtask.Status {
	switch completion {
	case agent.MessageCompletionError:
		return bgtask.StatusFailed
	case agent.MessageCompletionComplete:
		return bgtask.StatusCompleted
	default:
		return bgtask.StatusStopped
	}
}

// closeSessionActors ends every actor, with status: at a session this agent
// leaves, and at the end of the process. No later event can close their rows:
// the events of a session the agent left name a session that it no longer
// reads, and an ended process sends none.
func (a *Agent) closeSessionActors(status bgtask.Status) {
	type open struct {
		rowKey, childID string
		// turnSeq orders the end of a turn that still ran on the actor's tab. It
		// is zero for an actor that ran none.
		turnSeq uint64
	}
	a.Mu.Lock()
	var rows []open
	for _, actor := range a.actors {
		if actor.rowKey == "" || actor.closed {
			continue
		}
		row := open{rowKey: actor.rowKey, childID: actor.childAgentID}
		if actor.running && actor.childAgentID != "" {
			row.turnSeq = a.NextTurnSeq()
		}
		rows = append(rows, row)
	}
	clear(a.actors)
	clear(a.spawnActors)
	a.Mu.Unlock()
	for _, row := range rows {
		if row.turnSeq != 0 {
			providerkit.PublishSteerableTurnActiveTo(a.sink.ChildSink(row.childID), false, row.turnSeq)
		}
		providerkit.LogRegistryRefusal("mimo", "close", a.sink.CloseBackgroundTask(row.rowKey, status))
		if row.childID != "" {
			a.sink.CleanupChildAgent(row.childID)
		}
	}
}

// restoreActorLinks rebuilds the spawn links of a resumed session from its
// main-agent history. A worker restart empties the in-memory links, and the
// spawn call's id keys the actor's registry row and its tab. Without the link,
// a later row of the actor would open a second tab under a new key.
//
// Each restored actor starts closed: the new process runs no turn of it, and its
// next running status revives its row.
func (a *Agent) restoreActorLinks(messages []mimoMessageWithParts) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type != contracts.MiMoPartTypeTool || part.CallID == "" || !isSpawnCall(part) {
				continue
			}
			actorID := toolMetadata(part.State).ActorID
			if actorID == "" || actorID == mainActorID {
				continue
			}
			actor := a.actorLocked(actorID)
			actor.spawnCallID = part.CallID
			actor.rowKey = part.CallID
			actor.closed = true
			if actor.description == "" {
				actor.description = spawnTitle(part)
			}
			a.spawnActors[part.CallID] = actorID
		}
	}
}

// --- child input ---

// errSubagentIdle refuses a message to a subagent that runs no turn. See
// sendChildInput for why.
var errSubagentIdle = errors.New("MiMo reports no turn that a message starts on an idle subagent, " +
	"so LeapMux sends a message to a subagent only while the subagent runs")

// actorForRowKey resolves a registry row key to its actor.
func (a *Agent) actorForRowKey(rowKey string) (mimoActor, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if actorID, ok := a.spawnActors[rowKey]; ok {
		if actor := a.actors[actorID]; actor != nil {
			return *actor, true
		}
	}
	for _, actor := range a.actors {
		if actor.rowKey == rowKey {
			return *actor, true
		}
	}
	return mimoActor{}, false
}

// SendChildInput takes the message that the queue dispatches to a subagent, and
// always refuses it. A subagent that runs refuses with ErrAgentBusy, so the queue
// holds the message and offers it as a steer. A subagent that runs no turn
// refuses with errSubagentIdle (see sendChildInput).
func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChildInput(childKey, content, attachments, false)
}

// SteerChildInput adds a message to a subagent's running turn. The actor reads
// it at its next step, as the main agent does.
func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChildInput(childKey, content, attachments, true)
}

// ActiveChildTurnState classifies a subagent's turn after a refusal.
func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	actor, ok := a.actorForRowKey(childKey)
	running := ok && actor.running
	return agent.TurnState{Active: running, Steerable: running}
}

// sendChildInput sends one message to a subagent, as a steer into its running
// turn.
//
// A subagent that runs no turn refuses input. This is a choice between two
// designs, and the other design cannot be made correct:
//
//   - MiMo runs a message to an idle actor through the session loop directly
//     (prompt_async, then SessionPrompt.loop, then ensureRunning). Only the
//     turns that the actor tool starts publish actor.status, and the runner of
//     a subagent publishes no session.status. So no event states the start or
//     the end of that turn, and GET /session/:id/actors still reports "idle".
//   - To follow that turn, the worker would have to copy the rules by which
//     MiMo ends a loop (session/classify.ts and the steps of session/prompt.ts
//     that continue by themselves). That copy ends the turn early each time
//     MiMo continues on its own, and it goes stale with each MiMo release.
//   - The synchronous route POST /session/:id/message does return at the end
//     of the turn. But it refuses with 409 while the main agent runs, and it
//     cancels the MAIN agent's turn when its connection closes.
//
// A running actor takes a message as a steer: MiMo joins it into the running
// loop, and actor.status idle ends the turn. In a short window after the loop
// ends and before its idle status arrives, MiMo still runs a steer as a new,
// unreported turn. No event can close that window.
func (a *Agent) sendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	actor, ok := a.actorForRowKey(childKey)
	if !ok {
		return fmt.Errorf("MiMo holds no subagent for %q in this session", childKey)
	}
	if !actor.running {
		if steer {
			return agent.ErrNoActiveTurn
		}
		return errSubagentIdle
	}
	if !steer {
		return agent.ErrAgentBusy
	}
	parts, err := buildPromptParts(content, attachments)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	request := a.promptRequestLocked(parts, actor.id)
	a.Mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("agent has no MiMo session")
	}
	// The queue writes the message into the child transcript when it accepts the
	// delivery, so this call writes no row of its own.
	if err := a.rpc.promptAsync(a.Context(), sessionID, request); err != nil {
		return classifyDeliveryError("subagent message", err)
	}
	return nil
}

// mimoSendInput is the part of an `actor send` call's input the worker reads.
type mimoSendInput struct {
	Operation struct {
		Action    string `json:"action"`
		ToActorID string `json:"to_actor_id"`
		Content   string `json:"content"`
	} `json:"operation"`
}

// recordParentMessage writes a message that the main agent sent to a subagent
// with `actor send` into the subagent's transcript. The subagent receives it in
// its inbox, and MiMo's own copy is a user message, which the worker persists
// nowhere else.
func (a *Agent) recordParentMessage(part mimoPart) {
	if part.State == nil || part.State.Status != contracts.MiMoToolStatusCompleted {
		return
	}
	var in mimoSendInput
	if err := json.Unmarshal(part.State.Input, &in); err != nil || in.Operation.Action != contracts.MiMoActorActionSend {
		return
	}
	actorID := strings.TrimSpace(in.Operation.ToActorID)
	if actorID == "" || actorID == mainActorID || strings.TrimSpace(in.Operation.Content) == "" {
		return
	}
	a.Mu.Lock()
	_, known := a.actors[actorID]
	a.Mu.Unlock()
	if !known {
		return
	}
	childID, ok := a.ensureActorTranscript(actorID)
	if !ok {
		return
	}
	if err := a.sink.PersistChildUserMessage(childID, in.Operation.Content); err != nil {
		slog.Warn("mimo persist parent message to subagent", "agent_id", a.AgentID(), "actor_id", actorID, "error", err)
	}
}

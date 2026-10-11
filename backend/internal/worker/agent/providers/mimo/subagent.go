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
// Each subagent is an actor in the parent's session. The actor tool starts that actor.
// spawn returns immediately and runs the actor in the background.
// run waits for the actor to finish. Each native message identifies its actor through agentID.
// output.go routes its parts to that actor's child transcript.
//
// The spawn call and the actor meet on the call's running update, whose
// `metadata.actorId` identifies the actor. That update arrives before the actor's
// first message, so the child transcript exists by the time its rows do. An
// actor that no call started -- one a workflow spawned -- gets its transcript on
// its first row instead.
//
// actor.status reports turns that the actor tool starts.
// Running opens or reopens the registry row. Idle closes that row with its outcome.
// A message to a running actor joins its turn.
//
// A message to an idle actor starts a turn without actor.status events.
// The parent's actor send command can start that turn. LeapMux child input refuses that case.
// Output still reaches the actor's transcript because native messages identify the actor.
// The actor's registry row stays closed without native turn events.

// actorModeMain is the mode of the session's own actor.
const actorModeMain = "main"

// mimoActor is what the worker knows about one subagent.
type mimoActor struct {
	id          string
	description string
	agentType   string
	background  bool
	// rowKey identifies the registry row and child transcript.
	// A spawn call supplies its native part ID.
	// An actor with no spawn call uses its session and actor IDs.
	rowKey      string
	spawnSpanID string
	// childAgentID is the child transcript, once it exists.
	childAgentID string
	// pendingSpawnPrompt holds only the original native spawn instruction.
	pendingSpawnPrompt string
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

// title selects the first nonempty value for the actor's tab and registry row:
//   - Its description.
//   - Its agent type.
//   - Its ID.
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

// actorLocked returns the actor record and creates it on the first observation.
// The caller holds a.Mu.
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
// v0.1.14 and v0.1.15. It asks the subagent for a report header.
// MiMo appends this format instruction after the user's task.
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

// persistUserInstruction keeps one native instruction on its captured child transcript.
// The service adds only the first prompt. A failed write leaves the part available for retry.
func (a *Agent) persistUserInstruction(record *mimoMessageRecord, part mimoPart) error {
	if record.role != roleUser || part.Type != partTypeText || part.Synthetic || part.Ignored {
		return nil
	}
	if !record.actorKnown {
		return fmt.Errorf("the native MiMo instruction owner remains unresolved")
	}
	if record.actorID == mainActorID {
		// The worker already stores the confirmed main user's instruction.
		return nil
	}
	if record.actor == nil {
		return fmt.Errorf("the captured MiMo instruction actor is absent")
	}
	instruction := subagentInstruction(part.Text)
	if instruction == "" {
		return nil
	}
	childID, ok := a.ensureActorRecordTranscript(record.actor, record.sessionID)
	if !ok || a.actorSpawnPromptPending(record.actor) {
		return fmt.Errorf("create the captured MiMo instruction transcript")
	}
	if err := a.sink.PersistChildPrompt(childID, instruction); err != nil {
		return fmt.Errorf("persist the captured MiMo instruction: %w", err)
	}
	return nil
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
	// The registration supplies no workflow ID.
	// An actor that registers during one active workflow initially belongs to that workflow.
	// linkSpawn removes that derived workflow when a native call identifies the actor.
	if actor.workflowRunID == "" && actor.spawnSpanID == "" {
		actor.workflowRunID = a.soleRunningWorkflowLocked()
	}
	a.Mu.Unlock()
}

// linkSpawn records that a spawn call started an actor, and gives the actor its
// child transcript. The native part ID becomes the actor's row key. It is the
// one identifier both the call and the actor's registry row can carry.
func (a *Agent) linkSpawn(spanID, actorID, title, prompt string) {
	if spanID == "" || actorID == "" || actorID == mainActorID {
		return
	}
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	if actor.spawnSpanID == "" {
		actor.spawnSpanID = spanID
		actor.pendingSpawnPrompt = prompt
		if actor.rowKey == "" {
			actor.rowKey = spanID
		}
		// A workflow's actor has no spawn call, so an actor that a call started
		// is the main agent's own, whichever workflow ran when it registered.
		actor.workflowRunID = ""
		a.spawnActors[spanID] = actorID
	}
	if actor.description == "" {
		actor.description = strings.TrimSpace(title)
	}
	a.retainSpawnPromptLocked(actor, a.sessionID)
	a.Mu.Unlock()
	a.ensureActorTranscript(actorID)
}

// retainSpawnPromptLocked keeps the captured actor alive while its native instruction waits.
// The actor holds the text. This observation holds only its lifetime and order.
func (a *Agent) retainSpawnPromptLocked(actor *mimoActor, sessionID string) {
	if actor.pendingSpawnPrompt == "" {
		return
	}
	id := "mimo-spawn-prompt:" + sessionID + ":" + actor.spawnSpanID
	if a.messages[id] != nil {
		return
	}
	record := &mimoMessageRecord{
		role: roleUser, actorID: actor.id, actor: actor, identityKnown: true, actorKnown: true,
		sessionID: sessionID, turnEpoch: a.turnEpoch, completed: true,
	}
	a.messages[id] = record
	a.appendPendingPartLocked(record, &mimoPendingPart{kind: mimoPendingSpawnPrompt, partID: id})
}

// ensureActorTranscript returns the actor's child transcript.
// The first observation creates its transcript, registry row, and opening prompt.
// It returns false when the worker cannot create the transcript.
func (a *Agent) ensureActorTranscript(actorID string) (string, bool) {
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	sessionID := a.sessionID
	a.Mu.Unlock()
	return a.ensureActorRecordTranscript(actor, sessionID)
}

// ensureActorRecordTranscript keeps the original actor across a session change.
func (a *Agent) ensureActorRecordTranscript(actor *mimoActor, sessionID string) (string, bool) {
	a.Mu.Lock()
	if actor.rowKey == "" {
		// The native session separates actors that reuse the same ID.
		actor.rowKey = sessionID + "/" + actor.id
	}
	rowKey, spawnSpan, title, childID := actor.rowKey, actor.spawnSpanID, actor.title(), actor.childAgentID
	if spawnSpan == "" {
		spawnSpan = rowKey
	}
	a.Mu.Unlock()
	created := childID == ""
	if created {
		var err error
		childID, err = a.sink.EnsureChildAgent(agent.ChildAgentSpec{
			SpawnSpanID: spawnSpan, ProviderChildKey: rowKey, AgentSessionID: sessionID, Title: title,
		})
		if err != nil {
			slog.Warn("mimo subagent ensure child failed", "agent_id", a.AgentID(), "actor_id", actor.id, "error", err)
			return "", false
		}
		a.Mu.Lock()
		actor.childAgentID = childID
		closed, running := actor.closed, actor.running
		a.Mu.Unlock()
		if !closed {
			a.upsertActorRow(actor, sessionID, bgtask.StatusRunning)
			if running {
				a.publishChildTurn(actor, sessionID)
			}
		}
	}
	// The durable child and its native activity remain visible while the prompt waits.
	if err := a.persistActorSpawnPrompt(actor, childID); err != nil {
		slog.Warn("mimo subagent persist prompt failed", "agent_id", a.AgentID(), "child", childID, "error", err)
	}
	return childID, true
}

func (a *Agent) actorSpawnPromptPending(actor *mimoActor) bool {
	if actor == nil {
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return actor.pendingSpawnPrompt != ""
}

func (a *Agent) persistActorSpawnPrompt(actor *mimoActor, childID string) error {
	a.Mu.Lock()
	prompt := actor.pendingSpawnPrompt
	a.Mu.Unlock()
	if prompt == "" {
		return nil
	}
	if err := a.sink.PersistChildPrompt(childID, prompt); err != nil {
		return err
	}
	a.Mu.Lock()
	if actor.pendingSpawnPrompt == prompt {
		actor.pendingSpawnPrompt = ""
	}
	a.Mu.Unlock()
	return nil
}

// upsertActorRow writes the actor's registry row with status.
func (a *Agent) upsertActorRow(actor *mimoActor, sessionID string, status bgtask.Status) {
	a.Mu.Lock()
	if a.actors[actor.id] != actor || a.sessionID != sessionID {
		a.Mu.Unlock()
		return
	}
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

// startActorTurn opens the actor's registry row.
// A native running status after idle confirms a new turn.
// ReviveBackgroundTask requires that proof before it reopens a finished row.
func (a *Agent) startActorTurn(actorID string, wasRunning, wasClosed bool) {
	a.Mu.Lock()
	actor := a.actorLocked(actorID)
	actor.running, actor.closed = true, false
	rowKey, sessionID, hadChild := actor.rowKey, a.sessionID, actor.childAgentID != ""
	a.Mu.Unlock()
	if _, ok := a.ensureActorRecordTranscript(actor, sessionID); !ok || wasRunning || !hadChild {
		return
	}
	if wasClosed && rowKey != "" {
		providerkit.LogRegistryRefusal("mimo", "revive", a.sink.ReviveBackgroundTask(rowKey))
	} else {
		a.upsertActorRow(actor, sessionID, bgtask.StatusRunning)
	}
	a.publishChildTurn(actor, sessionID)
}

// publishChildTurn reports an actor's turn on its own tab, from the running flag
// that the caller wrote. The tab's input queue then holds a message while the
// turn runs, and offers it as a steer, which a running actor takes. The token
// comes from the same critical section as the flag.
func (a *Agent) publishChildTurn(actor *mimoActor, sessionID string) {
	a.Mu.Lock()
	if actor == nil || a.actors[actor.id] != actor || a.sessionID != sessionID || actor.childAgentID == "" {
		a.Mu.Unlock()
		return
	}
	childID, active := actor.childAgentID, actor.running
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	providerkit.PublishSteerableTurnActiveTo(a.sink.ChildSink(childID), active, seq)
}

// endActorTurn persists unfinished output and updates the actor's registry outcome.
// A background actor also reports its last text to the parent.
func (a *Agent) endActorTurn(payload mimoActorStatus) {
	status, completion := actorOutcomeStatus(payload.LastOutcome)
	a.flushActorText(payload.ActorID, completion)
	a.closeUnfinishedTools(payload.ActorID, completion)
	childID, ok := a.ensureActorTranscript(payload.ActorID)

	a.Mu.Lock()
	actor := a.actorLocked(payload.ActorID)
	actor.running = false
	actor.closed = true
	rowKey, title, background, report, sessionID := actor.rowKey, actor.title(), actor.background, actor.lastText, a.sessionID
	actor.lastText = ""
	a.Mu.Unlock()
	a.retireActorControls(payload.ActorID)

	if payload.Error != "" {
		a.retainActorFailure(payload, actor)
	}
	// The turn ends on the subagent's tab after its last row, and before the
	// caches of the tab go.
	a.publishChildTurn(actor, sessionID)
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
		return bgtask.StatusSucceeded, agent.MessageCompletionComplete
	default:
		// An outcome this build does not know still ended the turn. Completed is
		// the reading that claims no failure the actor did not report.
		slog.Debug("mimo unknown actor outcome", "outcome", outcome)
		return bgtask.StatusSucceeded, agent.MessageCompletionComplete
	}
}

// retainActorFailure keeps the native error on its captured actor until persistence succeeds.
func (a *Agent) retainActorFailure(payload mimoActorStatus, actor *mimoActor) {
	a.Mu.Lock()
	sessionID := payload.SessionID
	if sessionID == "" {
		sessionID = a.sessionID
	}
	// Native message IDs cannot contain a colon. This identity stays stable on retry.
	id := fmt.Sprintf("mimo-actor-failure:%d", a.pendingPartOrder)
	record := &mimoMessageRecord{
		role: roleAssistant, actorID: payload.ActorID, identityKnown: true, actorKnown: true,
		sessionID: sessionID, actor: actor, turnEpoch: a.turnEpoch, completed: true,
	}
	a.messages[id] = record
	a.appendPendingPartLocked(record, &mimoPendingPart{kind: mimoPendingActorFailure, partID: id, actorFailure: &payload})
	a.Mu.Unlock()
	a.flushPendingMessage(id, "")
}

// actorStatusForCompletion returns the registry status when a subagent session or its process ends.
func actorStatusForCompletion(completion agent.MessageCompletion) bgtask.Status {
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

// closeSessionActors ends every actor with status when its session or process ends.
// The worker no longer reads events from a previous session.
// An ended process sends no further events. No later native event can close these registry rows.
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
		wasRunning, wasClosed := actor.running, actor.closed
		actor.running = false
		actor.closed = true
		if actor.rowKey == "" || wasClosed {
			continue
		}
		row := open{rowKey: actor.rowKey, childID: actor.childAgentID}
		if wasRunning && actor.childAgentID != "" {
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
			if part.Type != contracts.MiMoPartTypeTool || part.ID == "" || !isSpawnCall(part) {
				continue
			}
			actorID := toolMetadata(part.State).ActorID
			if actorID == "" || actorID == mainActorID {
				continue
			}
			actor := a.actorLocked(actorID)
			actor.spawnSpanID = part.ID
			actor.pendingSpawnPrompt = spawnPrompt(part.State.Input)
			a.retainSpawnPromptLocked(actor, a.sessionID)
			actor.rowKey = part.ID
			actor.closed = true
			if actor.description == "" {
				actor.description = spawnTitle(part)
			}
			a.spawnActors[part.ID] = actorID
		}
	}
}

// --- child input ---

// errSubagentIdle refuses a message to a subagent that runs no turn. See
// sendChildInput for why.
var errSubagentIdle = errors.New("MiMo reports no turn that a message starts on an idle subagent, " +
	"so LeapMux sends a message to a subagent only while the subagent runs")

// actorForRowKey resolves a registry row key to its actor.
func (a *Agent) actorForRowKey(rowKey string) (mimoActor, string, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if actorID, ok := a.spawnActors[rowKey]; ok {
		if actor := a.actors[actorID]; actor != nil {
			return *actor, a.sessionID, true
		}
	}
	for _, actor := range a.actors {
		if actor.rowKey == rowKey {
			return *actor, a.sessionID, true
		}
	}
	return mimoActor{}, "", false
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
	actor, _, ok := a.actorForRowKey(childKey)
	running := ok && actor.running
	return agent.TurnState{Active: running, Steerable: running}
}

// sendChildInput sends input to a running actor as a steer.
//
// It refuses input to an idle actor because MiMo supplies no lifecycle for that input:
//
//   - MiMo runs a message to an idle actor through the session loop directly
//     (prompt_async, then SessionPrompt.loop, then ensureRunning).
//     Only turns from the actor tool emit actor.status.
//     Child runners emit no session.status. GET /session/:id/actors still reports idle.
//     No native event identifies the new turn's start or end.
//   - To follow that turn, the worker would have to copy the rules by which
//     MiMo ends a loop (session/classify.ts and the steps of session/prompt.ts
//     that continue by themselves).
//     MiMo can continue its loop without a new request.
//     That classifier would end those turns early and diverge across native releases.
//   - The synchronous route POST /session/:id/message does return at the end
//     of the turn. It returns 409 while the parent runs.
//     A closed connection also cancels the parent's turn.
//
// MiMo joins input to a running actor's loop. The native idle status ends that turn.
// The loop can finish before its idle event arrives.
// Input in that interval can start another unreported turn.
// No native event can eliminate that interval.
func (a *Agent) sendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	return a.sendChildInputBuilt(childKey, steer, func() ([]mimoPromptPart, error) {
		return buildPromptParts(content, attachments)
	})
}

// sendChildInputBuilt separates prompt construction from the captured delivery target.
func (a *Agent) sendChildInputBuilt(childKey string, steer bool, build func() ([]mimoPromptPart, error)) error {
	actor, sessionID, ok := a.actorForRowKey(childKey)
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
	parts, err := build()
	if err != nil {
		return err
	}
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
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

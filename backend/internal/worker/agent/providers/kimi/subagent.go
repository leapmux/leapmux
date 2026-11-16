package kimi

import (
	"errors"
	"fmt"
	"log/slog"
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
// `agent-1`, ...). Native transcript frames link each child to its parent tool
// through agentRefs. Task frames supply the task address and outcome.
//
// Parent invocation outcomes group swarm members by the complete AgentSwarm
// operation. Each child keeps a separate canonical activity marker.
//
// A subagent outlives its first run: the model resumes one with `Agent {resume}`,
// and the user can send one a message from its tab. Its id therefore stays linked
// to its transcript for the life of the session.

// kimiChild is the transcript and the registry row of one subagent.
type kimiChild struct {
	childAgentID string
	rowKey       string
	// pendingPrompt is the exact queued submission, independent from turn activity.
	pendingPrompt *kimiPromptSubmission
	// title is the current label of the child activity row.
	title string
	// ended is true after the registry or its output queue accepts a final state.
	// Native child turn headers control when the row opens again.
	ended bool
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

// kimiChildRowKey is the registry row key of one subagent. The server numbers
// subagents per session, so the next session after a context clear starts at
// `agent-0` again, and the session qualifies the id.
func kimiChildRowKey(sessionID, agentID string) string {
	return sessionID + "/" + agentID
}

// kimiChildAdoption holds native child metadata supplied by a parent tool or task.
type kimiChildAdoption struct {
	SubagentID  string
	Description string
	SwarmIndex  int
}

// linkSubagent links a subagent to a child transcript under parentAgent and
// opens its registry row, or opens the row of a subagent that has a transcript
// already. The caller holds dispatchMu.
func (a *Agent) linkSubagent(payload kimiChildAdoption, parentAgent string, spawn kimiSpawn) {
	if payload.SubagentID == "" || payload.SubagentID == kimiMainAgentID || payload.SubagentID == parentAgent {
		return
	}
	parentRun := a.run(parentAgent)
	if a.ProcessExited() || parentRun.childGraph != nil && parentRun.childGraph.retired.Load() {
		return
	}
	if parentAgent != kimiMainAgentID && !a.activateChildLifetime(parentRun) {
		return
	}
	parentSink := a.runSink(parentRun)
	if parentSink == nil {
		// A child can adopt another child only after its own transcript is available.
		slog.Debug("kimi subagent spawned by an unlinked agent", "agent_id", a.AgentID(), "parent", parentAgent, "subagent", payload.SubagentID)
		return
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	child := a.runLocked(payload.SubagentID)
	a.Mu.Unlock()
	if spawn.name == "" {
		spawn.name = contracts.KimiToolAgent
		if payload.SwarmIndex > 0 {
			spawn.name = contracts.KimiToolAgentSwarm
		}
	}

	rowKey := kimiChildRowKey(sessionID, payload.SubagentID)
	title := strings.TrimSpace(payload.Description)
	if existing, linked := a.children.get(payload.SubagentID); linked {
		if !a.activateChildLifetime(child) {
			return
		}
		// The model resumed a subagent that already has a transcript. Its row
		// comes back to Running; the resumed run's prompt reaches the transcript
		// through its native turn header.
		a.children.update(payload.SubagentID, func(c *kimiChild) {
			c.ended = false
		})
		providerkit.LogRegistryRefusal("kimi", "revive", a.queueRegistryRevive(a.sink, existing.rowKey))
		return
	}

	childID, err := parentSink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildActivity,
		SpawnSpanID:  kimiChildSpawnSpan(spawn, payload.SubagentID), ProviderChildKey: rowKey, Title: title})
	if err != nil {
		slog.Warn("kimi subagent ensure child failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
		return
	}
	if err := a.installChildLifetime(child, parentRun, parentSink, parentSink.AcquireChildSink(childID)); err != nil {
		slog.Warn("acquire Kimi child lifetime", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
		return
	}
	child.childLifetime.lease.Activate()
	a.children.link(payload.SubagentID, &kimiChild{
		childAgentID: childID, rowKey: rowKey,

		title: title, ended: true,
	})

	upsert := bgtask.Upsert{
		RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: title, Status: bgtask.StatusEndedWithUnknownOutcome,
	}
	providerkit.LogRegistryRefusal("kimi", "upsert", a.queueRegistryUpsert(parentSink, upsert))

	// A parent prompt opens the child transcript. A swarm member supplies its
	// prompt in its native turn header, which replays after this link.
	if spawn.prompt != "" {
		if err := parentSink.PersistChildPrompt(childID, spawn.prompt); err != nil {
			slog.Warn("kimi subagent persist prompt failed", "agent_id", a.AgentID(), "child", childID, "error", err)
		}
	}
	a.replayPendingNativeTranscript(child)
	a.publishNativeTodo(child)
	a.replayNativeTasksOwnedBy(payload.SubagentID)
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
	if spawn.spanID == "" {
		return ""
	}
	if spawn.name == contracts.KimiToolAgentSwarm {
		return spawn.spanID + "#" + subagentID
	}
	return spawn.spanID
}

// attachChildSpawnSpan fills missing parent identity without changing task state.
func (a *Agent) attachChildSpawnSpan(sink agent.ProviderServices, child kimiChild, spanID string) bool {
	if spanID == "" {
		return false
	}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildActivity,
		SpawnSpanID:  spanID, ProviderChildKey: child.rowKey, Title: child.title})
	if err != nil {
		slog.Warn("attach Kimi child spawn span", "agent_id", a.AgentID(), "row_key", child.rowKey, "error", err)
		return false
	}
	if childID != child.childAgentID {
		slog.Error("Kimi child identity changed during span attachment", "agent_id", a.AgentID(), "row_key", child.rowKey, "expected_child_id", child.childAgentID, "actual_child_id", childID)
		return false
	}
	return true
}

// updateChildRegistryMetadata preserves lifecycle state while native feeds fill descriptive fields.
// The caller holds dispatchMu.
func (a *Agent) updateChildRegistryMetadata(sink agent.ProviderServices, agentID string, update bgtask.Upsert) error {
	child, linked := a.children.get(agentID)
	if !linked {
		return fmt.Errorf("the Kimi child %q is not linked", agentID)
	}
	update = update.Clean()
	if update.Title == child.title {
		update.Title = ""
	}
	if update.Title == "" && update.Kind == bgtask.KindUnspecified && update.GroupKey == "" && update.GroupLabel == "" {
		return nil
	}
	update.RowKey, update.ChildAgentID = child.rowKey, child.childAgentID
	err := a.queueRegistryMetadata(sink, update)
	if agent.OutputRetained(err) && update.Title != "" {
		a.children.update(agentID, func(current *kimiChild) { current.title = update.Title })
	}
	return err
}

func (a *Agent) closeSubagentRegistryRow(subagentID string, child kimiChild, status bgtask.Status) error {
	err := a.queueRegistryClose(a.sink, child.rowKey, status)
	providerkit.LogRegistryRefusal("kimi", "close", err)
	if !agent.OutputRetained(err) {
		return err
	}
	a.children.update(subagentID, func(c *kimiChild) {
		c.ended = true

	})
	return err
}

// --- a resync ---

// --- a subagent's own turns ---

func (a *Agent) startChildTranscriptTurn(run *kimiRun, origin, promptText string) {
	child, linked := a.children.get(run.agentID)
	if !linked {
		return
	}
	if !a.activateChildLifetime(run) || !a.reviveNativeChildRow(run) {
		return
	}
	a.Mu.Lock()
	wasActive := run.turnActive
	run.turnActive = true
	run.userTurn = origin == contracts.KimiOriginUser
	resumed := run.started
	run.started = true
	a.Mu.Unlock()
	run.generation.Reset()
	prompt := kimiStripGitContext(promptText)

	if !wasActive {
		a.publishChildTurn(run, true)
	}
	switch {
	case origin == contracts.KimiOriginUser:
		// A message from the subagent's own tab. LeapMux recorded the message
		// itself. The shared activation above returns its row to Running.
	case resumed && prompt != "":
		// The model resumed the subagent with a new prompt, which lands in the
		// middle of its transcript.
		if err := run.childLifetime.parent.PersistChildUserMessage(child.childAgentID, prompt); err != nil {
			slog.Warn("kimi subagent persist message failed", "agent_id", a.AgentID(), "child", child.childAgentID, "error", err)
		}
	default:
		// The subagent's first turn. Its prompt opens the transcript, unless the
		// spawn's own prompt already did: PersistChildPrompt writes nothing into
		// a transcript that holds a row.
		if err := run.childLifetime.parent.PersistChildPrompt(child.childAgentID, prompt); err != nil {
			slog.Warn("kimi subagent persist prompt failed", "agent_id", a.AgentID(), "child", child.childAgentID, "error", err)
		}
	}
}

// kimiChildTurnStatus is the registry status a subagent's own turn leaves.
func kimiChildTurnStatus(reason string) bgtask.Status {
	switch reason {
	case contracts.KimiTranscriptTurnCompleted:
		return bgtask.StatusSucceeded
	case contracts.KimiTranscriptTurnCancelled:
		return bgtask.StatusStopped
	default:
		return bgtask.StatusFailed
	}
}

// publishChildTurn reports a subagent's turn on its own tab. A subagent's turn
// takes no steering: the server steers the main agent's queue alone.
func (a *Agent) publishChildTurn(run *kimiRun, active bool) {
	writer := a.runSink(run)
	if writer == nil {
		return
	}
	a.Mu.Lock()
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	providerkit.PublishTurnStateTo(writer, agent.TurnState{Active: active}, seq)
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
func (a *Agent) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	agentID, _, err := a.childRoute(childKey)
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	a.Mu.Lock()
	busy := a.runLocked(agentID).turnActive
	model := a.settings.model
	a.Mu.Unlock()
	if busy {
		return agent.ChildInputResult{StartsTurn: true}, &agent.AgentBusyError{Err: agent.ErrAgentBusy}
	}
	parts, err := buildKimiContent(input.Content, input.Attachments, a.modelTakesImages(model))
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	submission, err := a.reserveChildPromptSubmission(childKey, input)
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	_, err = a.submitReservedPrompt(submission, parts)
	return agent.ChildInputResult{}, err
}

func (a *Agent) reserveChildPromptSubmission(childKey string, input agent.ChildInput) (*kimiPromptSubmission, error) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	agentID, sessionID, err := a.childRoute(childKey)
	if err != nil {
		return nil, err
	}
	a.Mu.Lock()
	busy := a.runLocked(agentID).turnActive
	a.Mu.Unlock()
	child, linked := a.children.get(agentID)
	if !linked {
		return nil, errKimiChildUnknown
	}
	if busy {
		return nil, &agent.AgentBusyError{Err: agent.ErrAgentBusy}
	}
	if child.pendingPrompt != nil {
		return nil, agent.ErrInputPending
	}
	run := a.run(agentID)
	if !a.ensureChildLifetime(run) {
		return nil, agent.ErrChildRouteNotReady
	}
	submission, err := a.registerPromptSubmissionLocked(sessionID, agentID)
	if err != nil {
		return nil, err
	}
	submission.ready = input.Ready
	if input.Ready != nil {
		submission.readyCapture = agent.CaptureTranscript(a.runSink(run), agent.MessageContent{}, agent.SpanInfo{})
	}
	a.children.update(agentID, func(child *kimiChild) { child.pendingPrompt = submission })
	return submission, nil
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

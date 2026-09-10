package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// codexChildState is one child thread's routing and lifecycle state. Identity
// fields survive a completed run, so a follow-up turn reuses the same virtual
// agent. A live event sets active and proves that the row can reopen. A stale
// agentsStates snapshot does not supply that proof.
type codexChildState struct {
	spawnCorrelationID string
	parentThreadID     string
	childAgentID       string
	agentPath          string
	promptTitle        string
	active             bool
}

func (s codexChildState) displayTitle() string {
	if s.agentPath != "" {
		return bgtask.CleanTitleRunes(codexAgentPathTitle(s.agentPath), 80)
	}
	return s.promptTitle
}

type codexChildRoute struct {
	agentID       string
	parentAgentID string
	parentSink    ProviderServices
	childSink     ProviderServices
}

// This file holds the Codex subagent integration: the legacy collab registry
// adapter, the V2 activity lifecycle, direct-parent transcript routing, and the
// ChildSteerer implementation. It keeps codex_output.go focused on item output.

// codexCollabStatusToRegistry maps a collab agentsStates status to the registry
// status. interrupted does NOT close the row (a child can be resumed via
// resumeAgent), so the caller distinguishes close-vs-update separately.
//
// A resumable "interrupted" child stays Running in the registry (with a paused
// activity line). This is an IN-SESSION Codex collab pause, not a worker
// restart: the child thread is paused inside the running owner process and
// resumeAgent restarts it in the same process, so the same row_key legitimately
// cycles running -> interrupted -> running. The wire "interrupted" must NOT map
// to StatusInterrupted: that status is final (IsFinished reports true), so
// the registry's monotonic-final guard would absorb the later "running"
// upsert that arrives when resumeAgent resumes the child, leaving the row stuck
// at Interrupted forever. StatusInterrupted is reserved for the boot sweep that
// marks tasks left active by a crashed worker (a genuine ending: a background
// task never survives a worker restart).
func codexCollabStatusToRegistry(s string) (status bgtask.Status, finished bool, activity string) {
	switch s {
	case "pendingInit", "running":
		return bgtask.StatusRunning, false, ""
	case "completed":
		return bgtask.StatusCompleted, true, ""
	case "errored", "notFound":
		return bgtask.StatusFailed, true, ""
	case "shutdown":
		return bgtask.StatusStopped, true, ""
	case "interrupted":
		return bgtask.StatusRunning, false, "paused"
	default:
		return bgtask.StatusRunning, false, ""
	}
}

// codexChildTurnRegistryStatus interprets a child turn boundary. V2 emits no
// failed activity item, so a failed turn is the only final failure signal.
// An interrupted turn remains resumable and keeps a paused row.
func codexChildTurnRegistryStatus(params json.RawMessage) (status bgtask.Status, finished bool, activity string) {
	var value struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &value) != nil {
		return bgtask.StatusRunning, false, ""
	}
	switch strings.ToLower(value.Turn.Status) {
	case "completed":
		return bgtask.StatusCompleted, true, ""
	case "failed":
		return bgtask.StatusFailed, true, ""
	case "cancelled", "canceled", "interrupted", "aborted":
		return bgtask.StatusRunning, false, "paused"
	default:
		return bgtask.StatusRunning, false, ""
	}
}

// collabAgentsStatesToRegistry walks a collab item's agentsStates and upserts/
// closes the registry rows for each child thread. The child threadId is the
// registry row_key. When the child route knows the thread, the ID is also the
// EnsureChildAgent providerChildKey (linking the row to a transcript). Final
// states close the row; interrupted updates without closing (resumable). The
// durable route stays so a later run can reuse the same transcript.
func (a *CodexAgent) collabAgentsStatesToRegistry(collab *codexCollabAgentToolCall) {
	if collab == nil || a.sink == nil {
		return
	}
	for threadID, st := range collab.AgentsStates {
		if threadID == "" {
			continue
		}
		status, finished, activity := codexCollabStatusToRegistry(st.Status)
		title := a.collabChildTitle(threadID)
		route, routed := a.resolveCodexChild(threadID)
		childAgentID := ""
		parentAgentID := ""
		if routed {
			childAgentID = route.agentID
			parentAgentID = route.parentAgentID
			if prompt := a.collabChildPrompts.take(threadID); prompt != "" {
				// Multi-Agent V1 supplies a prompt. Multi-Agent V2 supplies only
				// the canonical task path, so its transcript starts with output.
				if err := route.parentSink.PersistChildPrompt(childAgentID, prompt); err != nil {
					slog.Warn("codex collab persist prompt failed", "thread", threadID, "error", err)
				}
			}
		}
		// upsertCollabChildRow reopens the row first when this walk reports the
		// child still running after its row went final. The reopened row keeps a
		// closer: this same walk closes it when the state goes final again.
		if err := a.upsertCollabChildRow(bgtask.Upsert{
			RowKey:        threadID,
			Kind:          bgtask.KindSubagent,
			ChildAgentID:  childAgentID,
			ParentAgentID: parentAgentID,
			Title:         title,
			ActiveForm:    activity,
			Status:        status,
		}); err != nil {
			slog.Warn("codex collab registry upsert failed", "thread", threadID, "error", err)
		}
		if finished {
			completion := MessageCompletionError
			switch status {
			case bgtask.StatusCompleted:
				completion = MessageCompletionComplete
			case bgtask.StatusStopped, bgtask.StatusInterrupted:
				completion = MessageCompletionInterrupted
			default:
				// Failed and unexpected active states keep the error completion.
			}
			a.completeCodexChildRun(threadID, status, completion)
		}
	}
}

// reviveFinishedCollabChild returns a collab child's row to Running when the
// registry still holds it in a final status. A no-op for an absent row and for
// one that is already active, so the common case costs one cache read. A row
// past the display cap costs one indexed point lookup instead, and still
// revives -- which is the point: a session's older collab children must reopen
// the same way its newest one does.
func (a *CodexAgent) reviveFinishedCollabChild(threadID string) {
	_, status, ok, err := a.sink.LookupBackgroundTask(threadID)
	if err != nil {
		slog.Warn("codex collab registry lookup failed", "thread", threadID, "error", err)
		return
	}
	if !ok || !status.IsFinished() {
		return
	}
	if err := a.sink.ReviveBackgroundTask(threadID); err != nil {
		slog.Warn("codex collab revive failed", "thread", threadID, "error", err)
	}
}

// upsertCollabChildRow writes a collab child's registry row, reopening the row
// first when this write reports the child ACTIVE again and the registry still
// holds it finished.
//
// The proof and the write are ONE call, so a fourth writer cannot forget the
// reopen -- which is the whole hazard, because the upsert deliberately absorbs
// a non-final status against a final row. Three sites report a child active (a
// child turn/started, a collab agentsStates walk, a subAgentActivity), and each
// carried its own hand-placed copy of the same pair.
//
// The active field is the proof. The durable route survives a close, so a
// later direct child turn can reuse it. A replayed snapshot cannot set active.
func (a *CodexAgent) upsertCollabChildRow(up bgtask.Upsert) error {
	if !up.Status.IsFinished() && a.activeCollabChild(up.RowKey) {
		a.reviveFinishedCollabChild(up.RowKey)
	}
	return a.sink.UpsertBackgroundTask(up)
}

// handleCodexSubAgentActivity handles a v2 subAgentActivity item (registry
// only; never persisted). The started item is the V2 spawn authority: it
// supplies the spawn call ID, child thread ID, and canonical task path. The
// completed item closes the run. Interacted and interrupted remain resumable.
func (a *CodexAgent) handleCodexSubAgentActivity(item json.RawMessage, parentThreadID string) bool {
	var act struct {
		Type          string `json:"type"`
		ID            string `json:"id"`
		AgentThreadID string `json:"agentThreadId"`
		AgentPath     string `json:"agentPath"`
		Kind          string `json:"kind"`
	}
	if json.Unmarshal(item, &act) != nil || act.Type != "subAgentActivity" {
		return false
	}
	if act.AgentThreadID == "" {
		return true
	}
	if act.AgentPath != "" {
		a.recordCollabChildAgentPath(act.AgentThreadID, act.AgentPath)
	}
	if act.Kind == "completed" {
		a.completeCodexChildRun(act.AgentThreadID, bgtask.StatusCompleted, MessageCompletionComplete)
		return true
	}

	var activity string
	switch act.Kind {
	case "started":
	case "interacted":
		activity = "received input"
	case "interrupted":
		activity = "paused"
	default:
		return true
	}

	// A started activity always comes from the direct parent. Later activity can
	// come from another initiator, so registerCollabReceiver never replaces a
	// parent that the start already recorded.
	a.registerCollabReceiver(act.AgentThreadID, act.ID, parentThreadID)
	route, routed := a.resolveCodexChild(act.AgentThreadID)
	childAgentID := ""
	parentAgentID := ""
	if routed {
		childAgentID = route.agentID
		parentAgentID = route.parentAgentID
	}
	// upsertCollabChildRow reopens the row first. Without it an activity that
	// follows a close lands its ActiveForm ("received input") on a row that still
	// reads "completed" -- a finished subagent that just took a message.
	if err := a.upsertCollabChildRow(bgtask.Upsert{
		RowKey:        act.AgentThreadID,
		Kind:          bgtask.KindSubagent,
		ChildAgentID:  childAgentID,
		ParentAgentID: parentAgentID,
		Title:         a.collabChildTitle(act.AgentThreadID),
		ActiveForm:    activity,
		Status:        bgtask.StatusRunning,
	}); err != nil {
		slog.Warn("codex subAgentActivity upsert failed", "thread", act.AgentThreadID, "error", err)
		return true
	}
	// An upsert cannot CLEAR a field: a blank one means "keep", so the row would
	// still read "paused" after the subagent resumed. `started` is exactly that
	// transition, so the activity line is set through the primitive that writes
	// it unconditionally. Same monotonic guard, so this cannot resurrect a row
	// that already ended.
	if activity == "" {
		if err := a.sink.UpdateBackgroundTaskStatus(act.AgentThreadID, bgtask.StatusRunning, ""); err != nil {
			slog.Warn("codex subAgentActivity clear activity failed", "thread", act.AgentThreadID, "error", err)
		}
	}
	return true
}

func codexAgentPathTitle(agentPath string) string {
	agentPath = strings.TrimRight(strings.TrimSpace(agentPath), "/")
	if i := strings.LastIndexByte(agentPath, '/'); i >= 0 {
		agentPath = agentPath[i+1:]
	}
	return agentPath
}

// --- Child index ---

// finishCollabChildRun marks the current run final but keeps its durable route.
// A follow-up turn can then reuse the same transcript and direct-parent sink.
func (a *CodexAgent) finishCollabChildRun(threadID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.collabChildren[threadID]
	if ok {
		state.active = false
		a.collabChildren[threadID] = state
		if state.childAgentID != "" {
			delete(a.childGenerationBuffers, state.childAgentID)
		}
	}
	a.collabChildPrompts.forget(threadID)
}

func (a *CodexAgent) activeCollabChild(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.collabChildren[threadID].active
}

func (a *CodexAgent) activateCollabChild(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.collabChildren[threadID]
	if !ok {
		return false
	}
	state.active = true
	a.collabChildren[threadID] = state
	return true
}

// completeCodexChildRun closes one active run. Both child turn/completed and
// the parent's completed activity can report the same run, so Active makes the
// operation idempotent without deleting the route that a follow-up needs.
func (a *CodexAgent) completeCodexChildRun(
	threadID string,
	status bgtask.Status,
	completion MessageCompletion,
) {
	if !a.activeCollabChild(threadID) {
		return
	}
	route, routed := a.resolveCodexChild(threadID)
	if routed {
		a.flushCodexChildGeneration(route.agentID, completion)
		a.persistIncompleteCodexTools(route.agentID, false, completion)
		if a.childTurnID(threadID) != "" {
			a.clearChildTurnID(threadID)
			publishSteerableTurnActiveTo(route.childSink, false, a.childTurnSeq())
		}
	}
	if err := a.sink.CloseBackgroundTask(threadID, status); err != nil {
		slog.Warn("codex child registry close failed", "thread", threadID, "error", err)
		// Keep the run active. Codex sends duplicate lifecycle notifications,
		// and the next one can retry this registry write.
		return
	}
	a.finishCollabChildRun(threadID)
	if routed {
		route.parentSink.CleanupChildAgent(route.agentID)
	}
}

// collabChildTitle returns the V2 path segment or the first V1 prompt line.
// It returns an empty string when neither spawn form supplied a title.
func (a *CodexAgent) collabChildTitle(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.collabChildren[threadID].displayTitle()
}

// recordCollabChildPromptTitle records the first V1 prompt line as the title.
func (a *CodexAgent) recordCollabChildPromptTitle(threadID, prompt string) {
	if threadID == "" {
		return
	}
	title := bgtask.CleanTitleRunes(bgtask.FirstLine(prompt), 80)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabChildren == nil {
		a.collabChildren = make(map[string]codexChildState)
	}
	state := a.collabChildren[threadID]
	state.promptTitle = title
	a.collabChildren[threadID] = state
}

// recordCollabChildAgentPath records the canonical V2 path. The title remains
// derived from this source, and the path stays available after the run.
func (a *CodexAgent) recordCollabChildAgentPath(threadID, agentPath string) {
	if threadID == "" {
		return
	}
	agentPath = strings.TrimRight(strings.TrimSpace(agentPath), "/")
	if agentPath == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabChildren == nil {
		a.collabChildren = make(map[string]codexChildState)
	}
	state := a.collabChildren[threadID]
	state.agentPath = agentPath
	a.collabChildren[threadID] = state
}

// --- ChildSteerer implementation ---

// SendChildInput starts a new turn on a child conversation (childKey = child
// threadId). It never steers. SteerChildInput is the one method that adds input
// to an active child turn.
//
// turn/steer and turn/start are not safely composable, so this method must not
// try one and then the other. A transport failure after the host applies the
// steer starts a DUPLICATE concurrent turn on the same thread, which
// interleaves the output and leaves an orphaned turn that nothing can steer.
// So this method refuses an active child turn and returns ErrNoActiveTurn. The
// Worker queue then holds the input until that turn ends.
func (a *CodexAgent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	threadID := childKey
	if !a.knownCollabChild(threadID) {
		// The owner process runs, but its in-memory child route does not know
		// this thread. The worker rebuilds that route only when a live spawn
		// reports the thread again, so the route is empty after a worker
		// restart. The persisted registry row resolves, so the child IS
		// steerable in principle. ErrChildNotSteerableYet states exactly that
		// condition; its declaration lists the caller that maps it and where.
		return fmt.Errorf("%w: unknown codex subagent thread %q", ErrChildNotSteerableYet, childKey)
	}
	// The child already runs a turn, so it cannot take a NEW turn now. That is
	// the busy condition, not the absent-turn condition: ErrNoActiveTurn here
	// would tell the queue to store a permanent failure whose text says the
	// child has no turn, while the child is visibly working.
	if a.childTurnID(threadID) != "" {
		// No publish here, unlike the main-thread refusals. A child's activity
		// state comes from its background-task registry row rather than from a
		// turn flag, and the child's own queue already holds the turn that its
		// dispatch opened -- so the publish would need a registry write to
		// resolve the child agent id, and would then tell nobody anything new.
		return ErrAgentBusy
	}
	return a.sendTurnStartChild(threadID, codexChildInput(content, attachments))
}

func (a *CodexAgent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	if !a.knownCollabChild(childKey) {
		return fmt.Errorf("%w: unknown codex subagent thread %q", ErrChildNotSteerableYet, childKey)
	}
	turnID := a.childTurnID(childKey)
	if turnID == "" {
		return ErrNoActiveTurn
	}
	if err := a.sendTurnSteer(childKey, turnID, []map[string]interface{}{{"type": "text", "text": codexChildInput(content, attachments)}}); err != nil {
		return err
	}
	if a.childTurnID(childKey) != turnID {
		return ErrNoActiveTurn
	}
	return nil
}

func (a *CodexAgent) ActiveChildTurnState(childKey string) TurnState {
	active := a.childTurnID(childKey) != ""
	return TurnState{Active: active, Steerable: active}
}

func codexChildInput(content string, attachments []*leapmuxv1.Attachment) string {
	// Codex child turns accept only string input. Preserve each attachment name
	// so the child can ask the user for content that it cannot receive directly.
	for _, attachment := range attachments {
		if attachment.GetFilename() != "" {
			content += "\n\n[attachment: " + attachment.GetFilename() + "]"
		}
	}
	return content
}

// InterruptChild aborts a child's current turn inside the owner process.
func (a *CodexAgent) InterruptChild(childKey string) error {
	threadID := childKey
	if !a.knownCollabChild(threadID) {
		// See SendChildInput: the live index may be empty after a restart even
		// though the registry row resolves. Retry, not a permanent failure.
		return fmt.Errorf("%w: unknown codex subagent thread %q", ErrChildNotSteerableYet, childKey)
	}
	turnID := a.childTurnID(threadID)
	return a.interruptCodexTurn(threadID, turnID)
}

// sendTurnStartChild starts a new turn on a child thread. The turn/started
// notification is the acceptance signal across Codex versions.
func (a *CodexAgent) sendTurnStartChild(threadID, input string) error {
	params := map[string]any{
		"threadId": threadID,
		"input":    input,
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}
	ack := make(chan struct{})
	a.mu.Lock()
	if a.childTurnStartAcks == nil {
		a.childTurnStartAcks = make(map[string]chan struct{})
	}
	a.childTurnStartAcks[threadID] = ack
	a.mu.Unlock()
	requestErr := make(chan error, 1)
	go func() {
		if _, err := a.sendRequest("turn/start", paramsJSON, 0); err != nil {
			requestErr <- err
		}
	}()

	select {
	case <-ack:
		return nil
	case err := <-requestErr:
		a.clearChildTurnStartAck(threadID, ack)
		return classifyCodexTurnStartRequestError("turn/start child", err)
	case <-a.processDone:
		a.clearChildTurnStartAck(threadID, ack)
		return classifyCodexTurnStartRequestError("turn/start child", a.processExitError())
	case <-time.After(turnStartAckTimeout):
		a.clearChildTurnStartAck(threadID, ack)
		return fmt.Errorf("%w: child turn/start received no turn/started within %s", ErrDeliveryUncertain, turnStartAckTimeout)
	}
}

// knownCollabChild reports whether the thread is a registered collab child.
func (a *CodexAgent) knownCollabChild(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.collabChildren[threadID]
	return ok && (state.spawnCorrelationID != "" || state.childAgentID != "")
}

// childTurnID returns the active turn id for a child thread ("" if none).
func (a *CodexAgent) childTurnID(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.childTurnIDs == nil {
		return ""
	}
	return a.childTurnIDs[threadID]
}

func (a *CodexAgent) setChildTurnID(threadID, turnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.childTurnIDs == nil {
		a.childTurnIDs = make(map[string]string)
	}
	a.childTurnIDs[threadID] = turnID
	if ack := a.childTurnStartAcks[threadID]; ack != nil {
		close(ack)
		delete(a.childTurnStartAcks, threadID)
	}
}

func (a *CodexAgent) clearChildTurnStartAck(threadID string, expected chan struct{}) {
	a.mu.Lock()
	if a.childTurnStartAcks[threadID] == expected {
		delete(a.childTurnStartAcks, threadID)
	}
	a.mu.Unlock()
}

func (a *CodexAgent) clearChildTurnID(threadID string) {
	a.mu.Lock()
	if a.childTurnIDs != nil {
		delete(a.childTurnIDs, threadID)
	}
	a.mu.Unlock()
	a.clearInterruptCallsForThread(threadID)
}

package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// This file holds the Codex subagent integration (Part 4b): the background-task
// registry drive from collab agentsStates + subAgentActivity, the child index
// repurposed as EnsureChildAgent backing, child-thread routing, and the
// ChildSteerer implementation. It keeps codex_output.go focused on the main-
// thread item handlers; the collab-specific translation lives here.

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

// collabAgentsStatesToRegistry walks a collab item's agentsStates and upserts/
// closes the registry rows for each child thread. The child threadId is the
// registry row_key; when the thread is known to the child index it is also the
// EnsureChildAgent providerChildKey (linking the row to a transcript). Terminal
// states close the row AND remove the child-index entry; interrupted updates
// without closing (resumable).
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
		// Link the registry row to a child transcript when the index knows the
		// thread (EnsureChildAgent is idempotent).
		childAgentID := ""
		spawnSpan := a.collabSpanForThread(threadID)
		if spawnSpan != "" {
			var err error
			childAgentID, err = a.sink.EnsureChildAgent(spawnSpan, threadID, title)
			if err != nil {
				slog.Warn("codex collab ensure child failed", "thread", threadID, "error", err)
			} else if prompt := a.collabChildPrompts.take(threadID); prompt != "" {
				// The transcript exists now, so open it on the spawn prompt.
				if err := a.sink.PersistChildPrompt(childAgentID, prompt); err != nil {
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
			ParentAgentID: a.agentID,
			Title:         title,
			ActiveForm:    activity,
			Status:        status,
		}); err != nil {
			slog.Warn("codex collab registry upsert failed", "thread", threadID, "error", err)
		}
		if finished {
			if err := a.sink.CloseBackgroundTask(threadID, status); err != nil {
				slog.Warn("codex collab registry close failed", "thread", threadID, "error", err)
			}
			a.removeCollabChildIndex(threadID)
			// Release the child's per-agent service state (span tracker, todos,
			// cached child sink) so a long-running root that cycles many collab
			// children does not accumulate a stale entry per closed child until
			// the root itself closes. The transcript row survives.
			if childAgentID != "" {
				a.sink.CleanupChildAgent(childAgentID)
			}
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
// The child INDEX is the proof: removeCollabChildIndex drops the entry at the
// close, so a thread that is still known was re-registered by a later collab
// call, which a replayed snapshot never does.
func (a *CodexAgent) upsertCollabChildRow(up bgtask.Upsert) error {
	if !up.Status.IsFinished() && a.knownCollabChild(up.RowKey) {
		a.reviveFinishedCollabChild(up.RowKey)
	}
	return a.sink.UpsertBackgroundTask(up)
}

// handleCodexSubAgentActivity handles a v2 subAgentActivity item (registry
// only; never persisted): {kind: started|interacted|interrupted, agentThreadId}.
// started→Running, interacted→activity "received input", interrupted→Running
// with activity "paused" (resumable; see codexCollabStatusToRegistry for why a
// resumable interrupt must not use the final StatusInterrupted).
func (a *CodexAgent) handleCodexSubAgentActivity(item json.RawMessage) bool {
	var act struct {
		Type          string `json:"type"`
		AgentThreadID string `json:"agentThreadId"`
		Kind          string `json:"kind"`
	}
	if json.Unmarshal(item, &act) != nil || act.Type != "subAgentActivity" {
		return false
	}
	if act.AgentThreadID == "" {
		return true
	}
	var status bgtask.Status
	var activity string
	switch act.Kind {
	case "started":
		status = bgtask.StatusRunning
	case "interacted":
		status = bgtask.StatusRunning
		activity = "received input"
	case "interrupted":
		status = bgtask.StatusRunning
		activity = "paused"
	default:
		status = bgtask.StatusRunning
	}
	// upsertCollabChildRow reopens the row first. Without it an activity that
	// follows a close lands its ActiveForm ("received input") on a row that still
	// reads "completed" -- a finished subagent that just took a message.
	if err := a.upsertCollabChildRow(bgtask.Upsert{
		RowKey:     act.AgentThreadID,
		Kind:       bgtask.KindSubagent,
		Title:      a.collabChildTitle(act.AgentThreadID),
		ActiveForm: activity,
		Status:     status,
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
		if err := a.sink.UpdateBackgroundTaskStatus(act.AgentThreadID, status, ""); err != nil {
			slog.Warn("codex subAgentActivity clear activity failed", "thread", act.AgentThreadID, "error", err)
		}
	}
	return true
}

// --- Child index (collabThreadSpans repurposed as EnsureChildAgent backing) ---

// collabSpanForThread returns the owning spawnAgent span id for a child thread,
// or "" when the thread is unknown to the index.
func (a *CodexAgent) collabSpanForThread(threadID string) string {
	if threadID == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabThreadSpans == nil {
		return ""
	}
	return a.collabThreadSpans[threadID]
}

// removeCollabChildIndex drops a child thread from the index (it reached a final status).
// Both the thread->span mapping and the thread->title cache are cleared so a
// long-lived session that repeatedly spawns and closes subagents does not
// accumulate stale title entries (collabChildTitles is otherwise only cleared
// on ClearContext).
func (a *CodexAgent) removeCollabChildIndex(threadID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabThreadSpans != nil {
		delete(a.collabThreadSpans, threadID)
	}
	if a.collabChildTitles != nil {
		delete(a.collabChildTitles, threadID)
	}
	a.collabChildPrompts.forget(threadID)
}

// collabChildTitle returns a best-effort title for a child thread (the first
// line of the spawn prompt). Empty when no spawn item has been seen yet.
func (a *CodexAgent) collabChildTitle(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabChildTitles == nil {
		return ""
	}
	return a.collabChildTitles[threadID]
}

// recordCollabChildTitle records the spawn prompt's first line as the title for
// a child thread, for the registry + the child tab.
func (a *CodexAgent) recordCollabChildTitle(threadID, prompt string) {
	if threadID == "" {
		return
	}
	title := bgtask.CleanTitleRunes(bgtask.FirstLine(prompt), 80)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabChildTitles == nil {
		a.collabChildTitles = make(map[string]string)
	}
	a.collabChildTitles[threadID] = title
}

// --- ChildSteerer implementation ---

// SendChildInput sends a user message to a child conversation (childKey = child
// threadId). If an active child turn is known (childTurnIDs), it steers via
// turn/steer; with no active turn it starts a new turn on the child thread via
// turn/start. A steer error is RETURNED, never swallowed into a second
// turn/start: turn/steer and turn/start are not safely composable because a
// transport hiccup after the host applied the steer would start a DUPLICATE
// concurrent turn on the same thread (interleaved output, an unsteerable
// orphaned turn). The turn-ended race between the childTurnID check and the
// steer resolves on the user's next send, which finds no active turn and
// starts cleanly.
func (a *CodexAgent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	threadID := childKey
	if !a.knownCollabChild(threadID) {
		// The owner process is running but its in-memory spawn index does not
		// know this thread (the index is rebuilt only when the live collab
		// spawn re-fires, so it is empty after a worker restart). The persisted
		// registry row resolves, so the child IS steerable in principle -- map
		// this to ErrChildNotSteerableYet so the service tells the client to
		// retry instead of persisting a permanent delivery error.
		return fmt.Errorf("%w: unknown codex subagent thread %q", ErrChildNotSteerableYet, childKey)
	}
	// Attachments: Codex's turn/start input is a string; fold attachment
	// filenames into the content as a best-effort (Codex collab child threads
	// do not accept structured attachments over the host protocol).
	input := content
	for _, att := range attachments {
		if att.GetFilename() != "" {
			input += "\n\n[attachment: " + att.GetFilename() + "]"
		}
	}
	if turnID := a.childTurnID(threadID); turnID != "" {
		return a.sendTurnSteer(threadID, turnID, []map[string]interface{}{{"type": "text", "text": input}})
	}
	return a.sendTurnStartChild(threadID, input)
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
	params := map[string]any{"threadId": threadID}
	if turnID != "" {
		params["turnId"] = turnID
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/interrupt params: %w", err)
	}
	if _, err := a.sendRequest("turn/interrupt", paramsJSON, 0); err != nil {
		return fmt.Errorf("turn/interrupt child: %w", err)
	}
	return nil
}

// sendTurnStartChild starts a new turn on a child thread. Mirrors sendTurnStart
// but targets a non-main thread and stores the returned turn id under the child.
func (a *CodexAgent) sendTurnStartChild(threadID, input string) error {
	params := map[string]any{
		"threadId": threadID,
		"input":    input,
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}
	resp, err := a.sendRequest("turn/start", paramsJSON, 0)
	if err != nil {
		return fmt.Errorf("turn/start child: %w", err)
	}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(resp, &res) == nil && res.Turn.ID != "" {
		a.setChildTurnID(threadID, res.Turn.ID)
	}
	return nil
}

// knownCollabChild reports whether the thread is a registered collab child.
func (a *CodexAgent) knownCollabChild(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.collabThreadSpans[threadID]
	return ok
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
}

func (a *CodexAgent) clearChildTurnID(threadID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.childTurnIDs != nil {
		delete(a.childTurnIDs, threadID)
	}
}

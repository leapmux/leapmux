package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	codexPendingChildGenerationLimit = 1 << 20
	codexPendingChildEventLimit      = 256
	codexPendingChildEventBytesLimit = 4 << 20
)

type codexChildPhase uint8

const (
	codexChildPending codexChildPhase = iota
	codexChildRunning
	codexChildClosing
	codexChildInactive
)

type codexChildTransition struct {
	status     bgtask.Status
	completion MessageCompletion
	activity   string
}

func (t codexChildTransition) finished() bool {
	return t.status.IsFinished()
}

type codexPendingChildEventKind uint8

const (
	codexPendingItemStarted codexPendingChildEventKind = iota
	codexPendingItemCompleted
	codexPendingTurnStarted
	codexPendingTurnCompleted
)

type codexPendingChildEvent struct {
	kind   codexPendingChildEventKind
	raw    json.RawMessage
	params json.RawMessage
}

// codexChildState owns one child thread's route, output, and run lifecycle.
// Route identity survives a completed run so a later turn reuses the same
// transcript. Pending events stay capped until the authoritative start event
// supplies the route.
type codexChildState struct {
	spawnCorrelationID     string
	parentThreadID         string
	childAgentID           string
	agentPath              string
	promptTitle            string
	prompt                 string
	phase                  codexChildPhase
	finalTransition        codexChildTransition
	transcriptFinalized    bool
	generationBuffer       GenerationBuffer
	pendingGenerationBytes int
	pendingEvents          []codexPendingChildEvent
	pendingEventBytes      int
	pendingOutputDropped   bool
	turnID                 string
}

func (s *codexChildState) displayTitle() string {
	if s.agentPath != "" {
		return codexAgentPathTitle(s.agentPath)
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
// child interruption. It keeps codex_output.go focused on item output.

// codexCollabTransition maps a collab agentsStates status to one child
// transition. An interrupted child remains resumable.
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
func codexCollabTransition(s string) codexChildTransition {
	switch s {
	case "pendingInit", "running":
		return codexChildTransition{status: bgtask.StatusRunning}
	case "completed":
		return codexChildTransition{status: bgtask.StatusCompleted, completion: MessageCompletionComplete}
	case "errored", "notFound":
		return codexChildTransition{status: bgtask.StatusFailed, completion: MessageCompletionError}
	case "shutdown":
		return codexChildTransition{status: bgtask.StatusStopped, completion: MessageCompletionInterrupted}
	case "interrupted":
		return codexChildTransition{status: bgtask.StatusRunning, activity: "paused"}
	default:
		return codexChildTransition{status: bgtask.StatusRunning}
	}
}

// codexChildTurnTransition interprets a child turn boundary. V2 emits no
// failed activity item, so a failed turn is the only final failure signal.
// An interrupted turn remains resumable and keeps a paused row.
func codexChildTurnTransition(params json.RawMessage) codexChildTransition {
	var value struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &value) != nil {
		return codexChildTransition{status: bgtask.StatusRunning}
	}
	switch strings.ToLower(value.Turn.Status) {
	case "completed":
		return codexChildTransition{status: bgtask.StatusCompleted, completion: MessageCompletionComplete}
	case "failed":
		return codexChildTransition{status: bgtask.StatusFailed, completion: MessageCompletionError}
	case "cancelled", "canceled", "interrupted", "aborted":
		return codexChildTransition{status: bgtask.StatusRunning, activity: "paused"}
	default:
		return codexChildTransition{status: bgtask.StatusRunning}
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
		transition := codexCollabTransition(st.Status)
		if _, _, err := a.upsertCodexChildRegistryRow(threadID, transition); err != nil {
			slog.Warn("codex collab registry upsert failed", "thread", threadID, "error", err)
		}
		if transition.finished() {
			a.completeCodexChildRun(threadID, transition)
		}
	}
}

// upsertCodexChildRegistryRow builds the registry row from the child route.
// Route creation is explicit here because this write can link a transcript.
func (a *CodexAgent) upsertCodexChildRegistryRow(
	threadID string,
	transition codexChildTransition,
) (codexChildRoute, bool, error) {
	route, routed := a.ensureCodexChildRoute(threadID)
	childAgentID := ""
	parentAgentID := ""
	if routed {
		childAgentID = route.agentID
		parentAgentID = route.parentAgentID
		if prompt := a.takeCollabChildPrompt(threadID); prompt != "" {
			if err := route.parentSink.PersistChildPrompt(childAgentID, prompt); err != nil {
				slog.Warn("codex collab persist prompt failed", "thread", threadID, "error", err)
			}
		}
	}
	err := a.upsertCollabChildRow(bgtask.Upsert{
		RowKey:        threadID,
		Kind:          bgtask.KindSubagent,
		ChildAgentID:  childAgentID,
		ParentAgentID: parentAgentID,
		Title:         a.collabChildTitle(threadID),
		ActiveForm:    transition.activity,
		Status:        transition.status,
	})
	return route, routed, err
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
	if codexAgentPathIsRoot(act.AgentPath) || a.isMainThreadID(act.AgentThreadID) ||
		a.isRetiredCodexThread(act.AgentThreadID) {
		return true
	}
	if act.AgentPath != "" {
		a.recordCollabChildAgentPath(act.AgentThreadID, act.AgentPath)
	}
	if act.Kind == "completed" {
		a.completeCodexChildRun(act.AgentThreadID, codexChildTransition{
			status:     bgtask.StatusCompleted,
			completion: MessageCompletionComplete,
		})
		return true
	}

	transition := codexChildTransition{status: bgtask.StatusRunning}
	switch act.Kind {
	case "started":
		if !a.registerCodexV2ChildStart(act.AgentThreadID, act.ID, parentThreadID) {
			return true
		}
	case "interacted":
		transition.activity = "received input"
		a.activateCollabChild(act.AgentThreadID)
	case "interrupted":
		transition.activity = "paused"
		a.activateCollabChild(act.AgentThreadID)
	default:
		// An unknown activity still proves that the child exists. Do not infer
		// route identity from its call ID; only started supplies that authority.
		transition.activity = "working"
		a.activateCollabChild(act.AgentThreadID)
	}

	route, routed, err := a.upsertCodexChildRegistryRow(act.AgentThreadID, transition)
	if err != nil {
		slog.Warn("codex subAgentActivity upsert failed", "thread", act.AgentThreadID, "error", err)
		return true
	}
	if routed {
		a.replayPendingCodexChildEvents(act.AgentThreadID, route)
		a.retryCodexChildCompletion(act.AgentThreadID)
	}
	// An upsert cannot CLEAR a field: a blank one means "keep", so the row would
	// still read "paused" after the subagent resumed. `started` is exactly that
	// transition, so the activity line is set through the primitive that writes
	// it unconditionally. Same monotonic guard, so this cannot resurrect a row
	// that already ended.
	if transition.activity == "" {
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

func codexAgentPathIsRoot(agentPath string) bool {
	return strings.TrimRight(strings.TrimSpace(agentPath), "/") == "/root"
}

// --- Child index ---

func (a *CodexAgent) codexChildStateLocked(threadID string) *codexChildState {
	if threadID == "" {
		return nil
	}
	if a.collabChildren == nil {
		a.collabChildren = make(map[string]*codexChildState)
	}
	state := a.collabChildren[threadID]
	if state == nil {
		state = &codexChildState{}
		a.collabChildren[threadID] = state
	}
	return state
}

func (a *CodexAgent) registerCodexV2ChildStart(threadID, spawnCorrelationID, parentThreadID string) bool {
	if threadID == "" {
		return false
	}
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	if state.phase == codexChildInactive && state.spawnCorrelationID == spawnCorrelationID {
		a.mu.Unlock()
		return false
	}
	state.spawnCorrelationID = spawnCorrelationID
	if parentThreadID == "" {
		parentThreadID = a.threadID
	}
	state.parentThreadID = parentThreadID
	if state.phase != codexChildClosing {
		a.activateCodexChildStateLocked(state)
	}
	a.mu.Unlock()
	return true
}

func (a *CodexAgent) activateCodexChildStateLocked(state *codexChildState) {
	if state.phase == codexChildInactive || state.phase == codexChildPending {
		state.finalTransition = codexChildTransition{}
		state.transcriptFinalized = false
	}
	state.phase = codexChildRunning
}

func (a *CodexAgent) activeCollabChild(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.collabChildren[threadID]
	return state != nil && state.phase == codexChildRunning
}

func (a *CodexAgent) activateCollabChild(threadID string) bool {
	if threadID == "" {
		return false
	}
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	if state.phase != codexChildClosing {
		a.activateCodexChildStateLocked(state)
	}
	known := state.spawnCorrelationID != "" || state.childAgentID != ""
	a.mu.Unlock()
	return known
}

// completeCodexChildRun moves one run through closing to inactive. Transcript
// finalization runs once. A duplicate completion retries only the registry
// close that failed.
func (a *CodexAgent) completeCodexChildRun(threadID string, transition codexChildTransition) {
	if threadID == "" || !transition.finished() {
		return
	}
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	if state.phase == codexChildInactive {
		a.mu.Unlock()
		return
	}
	if state.phase != codexChildClosing || transition.status == bgtask.StatusFailed {
		state.finalTransition = transition
	}
	state.phase = codexChildClosing
	hadTurn := state.turnID != ""
	state.turnID = ""
	finalized := state.transcriptFinalized
	a.mu.Unlock()

	route, routed := a.ensureCodexChildRoute(threadID)
	if !routed {
		return
	}
	if !finalized {
		a.flushCodexChildGeneration(threadID, transition.completion)
		a.persistIncompleteCodexTools(threadID, false, transition.completion)
		if hadTurn {
			a.publishCodexChildTurnActive(route.childSink, false)
		}
		a.mu.Lock()
		if current := a.collabChildren[threadID]; current != nil && current.phase == codexChildClosing {
			current.transcriptFinalized = true
		}
		a.mu.Unlock()
	}
	if err := a.sink.CloseBackgroundTask(threadID, transition.status); err != nil {
		slog.Warn("codex child registry close failed", "thread", threadID, "error", err)
		return
	}
	a.finishCollabChildRun(threadID)
	route.parentSink.CleanupChildAgent(route.agentID)
}

func (a *CodexAgent) retryCodexChildCompletion(threadID string) {
	a.mu.Lock()
	state := a.collabChildren[threadID]
	if state == nil || state.phase != codexChildClosing {
		a.mu.Unlock()
		return
	}
	transition := state.finalTransition
	a.mu.Unlock()
	a.completeCodexChildRun(threadID, transition)
}

// finishCollabChildRun keeps route identity and releases per-run state.
func (a *CodexAgent) finishCollabChildRun(threadID string) {
	a.mu.Lock()
	state := a.collabChildren[threadID]
	if state != nil {
		state.phase = codexChildInactive
		state.finalTransition = codexChildTransition{}
		state.transcriptFinalized = false
		state.prompt = ""
		state.pendingEvents = nil
		state.pendingEventBytes = 0
		state.pendingOutputDropped = false
		state.pendingGenerationBytes = 0
		state.generationBuffer.Reset()
	}
	a.mu.Unlock()
}

// collabChildTitle returns the V2 path segment or the first V1 prompt line.
// It returns an empty string when neither spawn form supplied a title.
func (a *CodexAgent) collabChildTitle(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.collabChildren[threadID]
	if state == nil {
		return ""
	}
	return state.displayTitle()
}

// recordCollabChildPromptTitle records the first V1 prompt line as the title.
func (a *CodexAgent) recordCollabChildPromptTitle(threadID, prompt string) {
	if threadID == "" {
		return
	}
	title := bgtask.FirstLine(prompt)
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.codexChildStateLocked(threadID)
	state.promptTitle = title
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
	state := a.codexChildStateLocked(threadID)
	state.agentPath = agentPath
}

func (a *CodexAgent) rememberCollabChildPrompt(threadID, prompt string) {
	if threadID == "" || prompt == "" {
		return
	}
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	if state.prompt == "" {
		state.prompt = prompt
	}
	a.mu.Unlock()
}

func (a *CodexAgent) takeCollabChildPrompt(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.collabChildren[threadID]
	if state == nil {
		return ""
	}
	prompt := state.prompt
	state.prompt = ""
	return prompt
}

// InterruptChild aborts a child's current turn inside the owner process.
func (a *CodexAgent) InterruptChild(childKey string) error {
	threadID := childKey
	if !a.knownCollabChild(threadID) {
		// The live route can be empty after a restart while the registry row
		// still resolves. Report a retryable failure.
		return fmt.Errorf("%w: unknown codex subagent thread %q", ErrChildRouteNotReady, childKey)
	}
	turnID := a.childTurnID(threadID)
	return a.interruptCodexTurn(threadID, turnID)
}

// publishCodexChildTurnActive reports activity without a steering capability.
// Multi-Agent V2 rejects direct app-server input for spawned child threads.
func (a *CodexAgent) publishCodexChildTurnActive(sink TurnServices, active bool) {
	publishTurnStateTo(sink, TurnState{Active: active}, a.childTurnSeq())
}

// knownCollabChild reports whether the thread is a registered collab child.
func (a *CodexAgent) knownCollabChild(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.collabChildren[threadID]
	return ok && state != nil && (state.spawnCorrelationID != "" || state.childAgentID != "")
}

// childTurnID returns the active turn id for a child thread ("" if none).
func (a *CodexAgent) childTurnID(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.collabChildren[threadID]
	if state == nil {
		return ""
	}
	return state.turnID
}

func (a *CodexAgent) setChildTurnID(threadID, turnID string) {
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	state.turnID = turnID
	a.mu.Unlock()
}

func (a *CodexAgent) clearChildTurnID(threadID string) {
	a.mu.Lock()
	if state := a.collabChildren[threadID]; state != nil {
		state.turnID = ""
	}
	a.mu.Unlock()
	a.clearInterruptCallsForThread(threadID)
}

package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeStateSnapshot is the state document every session RPC that changes
// something returns: session/create, session/resume, and each setter.
type zcodeStateSnapshot struct {
	Session struct {
		SessionID string `json:"sessionId"`
		Mode      string `json:"mode"`
		Title     string `json:"title"`
		// Target is where the app-server actually states the session goal. ZCode
		// calls the goal a target in its storage and runtime, and the shipped
		// build sends it HERE and nowhere else: a session/create reply carries
		// `session.target: null`, and a session/goal reply carries the whole
		// object. Raw for the same three-case reason as Goal below.
		Target json.RawMessage `json:"target"`
	} `json:"session"`
	Settings *zcodeSettingsSnapshot `json:"settings"`
	// Runtime carries stateRevision as well as eventSeq, because session/create
	// and session/resume are the ONLY places a resumed session learns its
	// revision before a turn ends. Reading eventSeq alone left stateRevision at
	// 0, and session/goal then sent expectedRevision 0 against a live session
	// whose revision was higher -- the app-server refused every goal action
	// with a conflict that did not exist.
	Runtime struct {
		EventSeq      int64 `json:"eventSeq"`
		StateRevision int64 `json:"stateRevision"`
	} `json:"runtime"`
	Projection struct {
		ContextUsed   int64 `json:"contextUsed"`
		ContextWindow int64 `json:"contextWindow"`
	} `json:"projection"`
	// Goal is RAW so the three cases stay distinct: absent (key missing),
	// null (no goal), and an object. A decoded pointer would fold the first
	// two together, and a snapshot with no goal must CLEAR one that a previous
	// process stored.
	//
	// The shipped build sends no `goal` key at all -- see Session.Target, which
	// is where it puts one. This stays because it costs nothing and a build that
	// does send the documented key keeps working.
	Goal json.RawMessage `json:"goal"`
}

// goalState returns the goal a state document states, from whichever key carries
// it.
//
// The `goal` key is what ZCode's protocol documents. `session.target` is what the
// shipped app-server sends, under the name its own storage uses. A document that
// carries both is read from `goal`, because that is the documented one.
func (s zcodeStateSnapshot) goalState() json.RawMessage {
	if len(s.Goal) > 0 {
		return s.Goal
	}
	return s.Session.Target
}

// openSession creates a fresh session, or resumes the one the client specified.
//
// A resume that does not hold fails the whole start. See providerkit.ResumeFailedError.
func (a *Agent) openSession(resumeID string, timeout time.Duration) error {
	if resumeID != "" {
		params := map[string]any{
			"sessionId": resumeID,
			"workspace": a.workspace,
		}
		a.Mu.Lock()
		accountConfig := a.accountProviderConfig
		a.Mu.Unlock()
		if accountConfig {
			params["dynamicWorkflowEnabled"] = true
		}
		raw, err := a.sendZCodeRequest(MethodSessionResume, params, timeout)
		if err != nil {
			return providerkit.ResumeFailedError(resumeID, err)
		}
		// "unknown" is the app-server's placeholder for "no session exists", so a
		// reply that carries it describes no session -- although it still holds a
		// sequence, a mode and a context window. The id is tested BEFORE the fold
		// for that reason, and an unusable document is abandoned WHOLE: a carried
		// eventSeq becomes a watermark that makes dispatchZCodeEvent drop later
		// events as duplicates, and `yolo` is a mode that nothing asked for.
		snap, ok := a.parseStateSnapshot(raw)
		if !ok || !zcodeUsableSessionID(snap.Session.SessionID) {
			return providerkit.ResumeFailedError(resumeID, fmt.Errorf("%s returned no session id", MethodSessionResume))
		}
		a.applyParsedStateSnapshot(snap)
		// A resume RESTATES the session's goal, so it is reported as a snapshot:
		// it updates the panel and writes no transcript row for a goal that may
		// be hours old. Outside applyParsedStateSnapshot, which holds a.Mu for
		// its whole body and must not call into the sink.
		a.reportZCodeGoal(snap.goalState(), true)
		return nil
	}

	params := map[string]any{"workspace": a.workspace}
	a.Mu.Lock()
	mode, model, accountConfig := a.mode, a.model, a.accountProviderConfig
	a.Mu.Unlock()
	if accountConfig {
		params["dynamicWorkflowEnabled"] = true
	}
	if mode != "" {
		params["mode"] = mode
	}
	// The legacy model id cannot identify whether the account now uses an
	// individual or a team plan. Let the account registry select the initial
	// model. applyStartupSettings re-applies an explicit request after the create
	// snapshot supplies the live account-provider ids.
	if !accountConfig {
		if ref, ok := a.catalog.refs[model]; ok {
			params["model"] = ref
		}
	}
	raw, err := a.sendZCodeRequest(MethodSessionCreate, params, timeout)
	if err != nil {
		return err
	}
	snap, ok := a.parseStateSnapshot(raw)
	if !ok || !zcodeUsableSessionID(snap.Session.SessionID) {
		return fmt.Errorf("%s returned no session id", MethodSessionCreate)
	}
	a.applyParsedStateSnapshot(snap)
	// A create RESTATES the session's goal -- normally that it has none, which
	// is what clears one a previous session left on the row.
	a.reportZCodeGoal(snap.goalState(), true)
	return nil
}

// applyStartupSettings pins the model, thought level and mode the launch asked
// for. Each step is best-effort and reports what the app-server settled on.
func (a *Agent) applyStartupSettings(req zcodeSettingsRequest, timeout time.Duration) {
	// Re-pin the REQUEST before any setter runs. `openSession` folded the create/resume
	// reply through applySettingsSnapshotLocked, which overwrote a.thoughtLevel with the
	// app-server's own `thoughtLevel.current` -- its fallback, the LOWEST level, not the
	// default the model declares. Reading the fields back after that fold would compare
	// the observed level against itself, so a launch that asked for `max` would send no
	// setter, and Auto would never reach the catalog default that applyZCodeModel
	// resolves it to. Where the launch asked for nothing, the observed value stands.
	a.Mu.Lock()
	// The mode the opened session RUNS in, read before the request is pinned over it.
	// `settings.mode.current` is the app-server's own live value, so it decides whether
	// the mode setter below has any work to do -- and modeObserved says whether the
	// opened session reported that value at all.
	observedMode, modeObserved := a.mode, a.modeObserved
	if req.Model != "" {
		a.model = req.Model
	}
	if req.ThoughtLevel != "" {
		a.thoughtLevel = req.ThoughtLevel
	}
	if req.Mode != "" {
		a.mode = req.Mode
	}
	// The model and the level fall back to the observed value, because the app-server
	// resolves both and a launch that asked for neither still runs on something. The
	// MODE does not: a launch that asked for no mode has nothing to pin, and the mode
	// the opened session chose is already the right one.
	model, level, mode := a.model, a.thoughtLevel, req.Mode
	a.Mu.Unlock()

	if model != "" {
		if err := a.applyZCodeModel(model, timeout); err != nil {
			slog.Warn("zcode setModel on startup failed", "agent_id", a.AgentID(), "model", model, "error", err)
		}
	}
	// EffortAuto is LeapMux's sentinel for "send no thought level at all", so the
	// app-server keeps whatever default it resolved for the model.
	if level != "" && level != agent.EffortAuto {
		if err := a.applyZCodeThoughtLevel(level, timeout); err != nil {
			slog.Warn("zcode setThoughtLevel on startup failed", "agent_id", a.AgentID(), "level", level, "error", err)
		}
	}
	// session/create HONORS its mode parameter, and openSession sends it, so the
	// session usually already runs in the requested mode and the setter is one
	// blocking RPC of pure repetition. The comparison is against
	// `settings.mode.current` from the create or resume reply -- the app-server's live
	// mode -- and never against `session.mode`, which reports the projection's seed
	// (`build`) whatever the session runs in. A reply that reported no mode is no
	// evidence, and the setter runs: the cost of a redundant RPC is a round trip, and
	// the cost of a wrong skip is a session that runs in another session's mode.
	if mode != "" && (!modeObserved || mode != observedMode) {
		if err := a.applyZCodeMode(mode, timeout); err != nil {
			slog.Warn("zcode setMode on startup failed", "agent_id", a.AgentID(), "mode", mode, "error", err)
		}
	}
}

// subscribe opens the event stream.
//
// `includeSnapshot` stays false: a snapshot is O(messages) and building it is the
// main cause of subscribe timeouts on a long session. `afterSeq` is the sequence
// the state snapshot already reported, so a RESUMED session does not replay its
// whole history into a transcript LeapMux already persisted.
func (a *Agent) subscribe(timeout time.Duration) error {
	a.Mu.Lock()
	sessionID, afterSeq := a.sessionID, a.lastSeq
	a.Mu.Unlock()

	raw, err := a.sendZCodeRequest(MethodSessionSubscribe, map[string]any{
		"sessionId":       sessionID,
		"deliveryKind":    DeliveryContinuous,
		"includeSnapshot": false,
		"afterSeq":        afterSeq,
	}, timeout)
	if err != nil {
		return err
	}
	var resp struct {
		EventSeq int64                `json:"eventSeq"`
		Events   []zcodeEventEnvelope `json:"events"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		slog.Warn("zcode subscribe response unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}
	// Events the subscription replayed arrive in the RESPONSE, not as notifications,
	// so they are dispatched here or they are lost.
	for _, event := range resp.Events {
		a.dispatchZCodeEvent(event)
	}
	a.Mu.Lock()
	if resp.EventSeq > a.lastSeq {
		a.lastSeq = resp.EventSeq
	}
	a.Mu.Unlock()
	return nil
}

// applyStateSnapshot folds a state document into the agent's own state.
//
// It does NOT report the goal, and that omission is the point. Three settings
// setters fold their reply documents through here -- applyZCodeModel,
// applyZCodeThoughtLevel and applyZCodeMode -- so reporting the goal from this
// function would let a model, effort or permission-mode change write the goal
// columns. Worse, reportZCodeGoal reads a `null` goal as "the goal is gone", so
// a settings reply that spells it would DELETE a live goal and broadcast the
// removal with no transcript row.
//
// The goal is reported from the two calls that genuinely restate a session,
// openSession's create and resume branches, which is where a session snapshot
// is the authority on what the goal is.
func (a *Agent) applyStateSnapshot(raw json.RawMessage) (zcodeStateSnapshot, bool) {
	if snap, ok := a.parseStateSnapshot(raw); ok {
		a.applyParsedStateSnapshot(snap)
		return snap, true
	}
	return zcodeStateSnapshot{}, false
}

// parseStateSnapshot decodes a state document. ok is false for an absent or a
// malformed document, which is a diagnostic loss rather than a failure.
//
// The parse is separate from the fold because openSession must READ a resume reply
// before it decides to keep it, and a document that it abandons must leave nothing
// behind.
func (a *Agent) parseStateSnapshot(raw json.RawMessage) (zcodeStateSnapshot, bool) {
	var snap zcodeStateSnapshot
	if len(raw) == 0 {
		return snap, false
	}
	// A session/goal reply is not a bare state document: it wraps one under
	// `snapshot`, beside its own `response` text and a `startedTurn` flag.
	// Parsing that envelope as a document read nothing at all -- not the goal,
	// and not the `stateRevision` the next goal action needs for its
	// expectedRevision. Unwrapping here keeps one parser for both shapes.
	var envelope struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Snapshot) > 0 {
		raw = envelope.Snapshot
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		slog.Warn("zcode state snapshot unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return snap, false
	}
	return snap, true
}

// applyParsedStateSnapshot folds a decoded state document into the agent's state.
func (a *Agent) applyParsedStateSnapshot(snap zcodeStateSnapshot) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if zcodeUsableSessionID(snap.Session.SessionID) {
		if snap.Session.SessionID != a.sessionID {
			// Sequence and revision counters belong to one native session.
			a.lastSeq = 0
			a.stateRevision = 0
			a.modeObserved = false
		}
		a.sessionID = snap.Session.SessionID
	}
	if snap.Runtime.EventSeq > a.lastSeq {
		a.lastSeq = snap.Runtime.EventSeq
	}
	// Monotonic, for the reason noteZCodeStateRevision gives. Written inline
	// rather than through it because this function already holds a.Mu.
	if snap.Runtime.StateRevision > a.stateRevision {
		a.stateRevision = snap.Runtime.StateRevision
	}
	if snap.Projection.ContextWindow > 0 {
		a.contextWindow = snap.Projection.ContextWindow
	}
	if snap.Settings != nil && snap.Settings.Model != nil && snap.Settings.Model.Current.ModelID != "" {
		delete(a.unresolvedSettings, agent.OptionIDModel)
	}
	if snap.Settings != nil && snap.Settings.ThoughtLevel != nil &&
		(!snap.Settings.ThoughtLevel.Enabled || snap.Settings.ThoughtLevel.Current != "") {
		delete(a.unresolvedSettings, agent.OptionIDEffort)
	}
	if snap.Settings != nil && snap.Settings.Mode != nil && snap.Settings.Mode.Current != "" {
		delete(a.unresolvedSettings, agent.OptionIDPermissionMode)
	}
	a.applySettingsSnapshotLocked(snap.Settings)
}

func (a *Agent) markSettingUnresolved(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.unresolvedSettings == nil {
		a.unresolvedSettings = make(map[string]struct{})
	}
	a.unresolvedSettings[id] = struct{}{}
}

// zcodeUsableSessionID reports whether a state document's session id can be
// adopted. The app-server builds that field as
// `String(session?.id ?? app?.sessionId ?? "unknown")`, so "unknown" is its
// placeholder for a document that belongs to no session, and an RPC that carries
// the placeholder back is rejected.
func zcodeUsableSessionID(id string) bool {
	return id != "" && id != "unknown"
}

// ClearContext starts a fresh session on the same workspace.
//
// Every piece of per-session state is dropped with it. The per-tool-call side
// tables matter most: tool.updated is their only other removal, and it never
// arrives for a call the replaced session was still running, so without this a
// spawn prompt is retained for the life of the process and a reused tool-call id
// would open the next child transcript on the previous session's instruction.
func (a *Agent) ClearContext() (string, error) {
	timeout := a.APITimeout()
	a.Mu.Lock()
	// The three axes the user currently runs on are the request for the fresh session.
	// Reading them AFTER openSession would read the new session's defaults instead, and
	// a context clear would silently drop the level and the model back to them.
	current := zcodeSettingsRequest{Model: a.model, ThoughtLevel: a.thoughtLevel, Mode: a.mode}
	a.Mu.Unlock()

	if err := a.openSession("", timeout); err != nil {
		return "", err
	}
	a.applyStartupSettings(current, timeout)
	subscribeErr := a.subscribe(timeout)
	// The turn belonged to the session this call replaced, so it ends with it. The
	// flag and the window go in ONE critical section: a frame that arrived between
	// them re-armed the window, which then fired into the NEW session.
	a.Mu.Lock()
	a.turnActive = false
	a.backgroundTurn = false
	a.cancelStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteZCodeTools(agent.MessageCompletionInterrupted)

	a.Mu.Lock()
	a.TurnToolUses = 0
	a.latestContextUsage = nil
	clear(a.toolCalls)
	a.nextToolOrder = 0
	clear(a.pendingControls)
	sessionID := a.sessionID
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.toolCallPrompts.Clear()
	a.children.clear()
	a.ResetCumulativeOutput()
	a.generationBuffer.Reset()
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.ResetSpans()
	// A goal belongs to a SESSION, and this call replaced the session. The
	// create snapshot omits the `goal` key rather than spelling it null, and
	// reportZCodeGoal returns early on an absent key, so nothing else removes
	// the previous session's goal.
	a.sink.ClearGoal(false)

	if sessionID == "" {
		return "", fmt.Errorf("the new ZCode session has no ID")
	}
	a.sink.UpdateSessionID(sessionID)
	return sessionID, subscribeErr
}

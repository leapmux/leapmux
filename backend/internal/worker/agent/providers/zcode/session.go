package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeSessionObservationIdentity fixes the lifetime and observation order before an RPC.
type zcodeSessionObservationIdentity struct {
	sessionID           string
	epoch               uint64
	observationSequence uint64
	usable              bool
}

func (a *Agent) captureZCodeObservationIdentityLocked() zcodeSessionObservationIdentity {
	return zcodeSessionObservationIdentity{sessionID: a.sessionID, epoch: a.sessionEpoch, observationSequence: a.observationSequence, usable: !a.sessionChanging && !a.StoppedLocked()}
}

func (a *Agent) zcodeSessionLifetimeCurrentLocked(identity zcodeSessionObservationIdentity) bool {
	return identity.usable && identity.sessionID != "" && identity.sessionID == a.sessionID && identity.epoch == a.sessionEpoch && !a.sessionChanging && !a.StoppedLocked()
}

func (a *Agent) zcodeReadObservationCurrentLocked(identity *zcodeSessionObservationIdentity, runtime *zcodeRuntimeState) bool {
	if runtime != nil && runtime.stateRevisionKnown && runtime.StateRevision < a.stateRevision {
		return false
	}
	if identity == nil {
		return true
	}
	if !identity.usable || a.sessionChanging || identity.sessionID != a.sessionID || identity.epoch != a.sessionEpoch {
		return false
	}
	if identity.observationSequence != a.observationSequence {
		return runtime != nil && runtime.stateRevisionKnown && runtime.StateRevision > a.stateRevision
	}
	return true
}

// readZCodeSession processes the known retained event range before exposing a snapshot.
func (a *Agent) readZCodeSession(ctx context.Context, identity zcodeSessionObservationIdentity) (zcodeSessionReadSnapshot, error) {
	var snapshot zcodeSessionReadSnapshot
	if identity.sessionID == "" || !identity.usable {
		return snapshot, errNoZCodeSession
	}
	raw, err := a.sendZCodeRequestContext(ctx, MethodSessionRead, map[string]any{"sessionId": identity.sessionID}, a.APITimeout())
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, fmt.Errorf("decode session/read: %w", err)
	}
	if snapshot.Session != nil && snapshot.Session.SessionID != identity.sessionID {
		return snapshot, fmt.Errorf("session/read returned a snapshot for another session")
	}
	observedSeq := int64(0)
	if snapshot.Runtime != nil {
		observedSeq = snapshot.Runtime.EventSeq
	}
	if err := a.ensureZCodeReplayThrough(ctx, identity.sessionID, observedSeq, identity.epoch); err != nil {
		return snapshot, err
	}
	snapshot.identity = &identity
	return snapshot, nil
}

// zcodeStateSnapshot is the state document every session RPC that changes
// something returns: session/create, session/resume, and each setter.
type zcodeStateSnapshot struct {
	Session struct {
		SessionID string `json:"sessionId"`
		Mode      string `json:"mode"`
		Title     string `json:"title"`
		// Target states the session goal. ZCode calls the goal a target in its
		// storage and runtime, and the snapshot carries it here and nowhere
		// else: a session/create reply carries `session.target: null`, and a
		// session/goal reply carries the whole object. ZCode 3.14.5 has no
		// top-level `goal` key in its snapshot schema, and its `state.updated`
		// patches carry none either.
		//
		// Target is RAW so the three cases stay distinct: absent (key missing),
		// null (no goal), and an object. A decoded pointer would fold the first
		// two together, and a snapshot with no goal must CLEAR one that a
		// previous process stored.
		Target json.RawMessage `json:"target"`
	} `json:"session"`
	Settings *zcodeSettingsSnapshot `json:"settings"`
	// Runtime carries stateRevision as well as eventSeq, because session/create
	// and session/resume are the ONLY places a resumed session learns its
	// revision before a turn ends. Reading eventSeq alone left stateRevision at
	// 0, and session/goal then sent expectedRevision 0 against a live session
	// whose revision was higher -- the app-server refused every goal action
	// with a conflict that did not exist.
	Runtime    zcodeRuntimeState    `json:"runtime"`
	Projection zcodeProjectionState `json:"projection"`
}

// openSession creates a fresh session, or resumes the one the client specified.
//
// A resume that does not hold fails the whole start. See providerkit.ResumeFailedError.
func (a *Agent) openSession(resumeID string, timeout time.Duration) error {
	a.lockZCodeSessionLifecycle()
	snap, err := a.openSessionState(resumeID, timeout)
	a.unlockZCodeSessionLifecycle()
	if err == nil {
		// Goal reporting can call the sink, so it runs outside the lifecycle lock.
		a.reportZCodeGoal(snap.Session.Target, true)
	}
	return err
}

// zcodeToolDenylist is the tool policy every session/create and session/resume
// states. LeapMux answers none of ZCode's Cron controls, and the installed
// app-server 0.17.0 schema accepts the field on both methods, so the family is
// hidden at session open instead of staying visible and unanswerable.
//
// The field is a session policy: session/send carries no toolDenylist, where it
// would take its per-turn meaning, which differs from this one.
var zcodeToolDenylist = []string{
	contracts.ZCodeToolNameCronCreate,
	contracts.ZCodeToolNameCronList,
	contracts.ZCodeToolNameCronUpdate,
	contracts.ZCodeToolNameCronDelete,
}

// buildZCodeResumeParams assembles the parameters of every session/resume.
// Startup, catalog refresh and later recovery all resume through this one
// builder, so one workspace and one tool policy apply to every resume.
func (a *Agent) buildZCodeResumeParams(resumeID string) map[string]any {
	params := map[string]any{
		"sessionId":    resumeID,
		"workspace":    a.workspace,
		"toolDenylist": zcodeToolDenylist,
	}
	a.Mu.Lock()
	accountConfig := a.account != nil
	a.Mu.Unlock()
	if accountConfig {
		params["dynamicWorkflowEnabled"] = true
	}
	return params
}

// buildZCodeCreateParams assembles the parameters of a session/create and
// reports the axis mode the request states. The caller records that mode from
// here rather than from a second read, so the plan flag it records is exactly
// what the create asked for.
func (a *Agent) buildZCodeCreateParams() (params map[string]any, mode string) {
	params = map[string]any{
		"workspace":    a.workspace,
		"toolDenylist": zcodeToolDenylist,
	}
	a.Mu.Lock()
	// The axis value: session/create accepts `plan` and folds it into the plan flag.
	// The native mode alone would open the fresh session outside plan mode.
	mode = a.permissionModeLocked()
	model, accountConfig := a.model, a.account != nil
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
	return params, mode
}

func (a *Agent) openSessionState(resumeID string, timeout time.Duration) (zcodeStateSnapshot, error) {
	if resumeID != "" {
		raw, err := a.sendZCodeRequest(MethodSessionResume, a.buildZCodeResumeParams(resumeID), timeout)
		if err != nil {
			return zcodeStateSnapshot{}, providerkit.ResumeFailedError(resumeID, err)
		}
		// "unknown" is the app-server's placeholder for "no session exists", so a
		// reply that carries it describes no session -- although it still holds a
		// sequence, a mode and a context window. The id is tested BEFORE the fold
		// for that reason, and an unusable document is abandoned WHOLE: a carried
		// eventSeq becomes a watermark that makes dispatchZCodeEvent drop later
		// events as duplicates, and `yolo` is a mode that nothing asked for.
		snap, ok := a.parseStateSnapshot(raw)
		if !ok || !zcodeUsableSessionID(snap.Session.SessionID) {
			return zcodeStateSnapshot{}, providerkit.ResumeFailedError(resumeID, fmt.Errorf("%s returned no session id", MethodSessionResume))
		}
		a.applyCompleteSessionSnapshot(snap)
		return snap, nil
	}

	params, mode := a.buildZCodeCreateParams()
	raw, err := a.sendZCodeRequest(MethodSessionCreate, params, timeout)
	if err != nil {
		return zcodeStateSnapshot{}, err
	}
	snap, ok := a.parseStateSnapshot(raw)
	if !ok || !zcodeUsableSessionID(snap.Session.SessionID) {
		return zcodeStateSnapshot{}, fmt.Errorf("%s returned no session id", MethodSessionCreate)
	}
	a.adoptCompleteZCodeSessionSnapshot(snap, true)
	if mode != "" {
		// The runtime folds the create's mode as it folds a session/setMode request,
		// and the reply states the native mode only. A create with no mode takes a
		// mode from ZCode's own preferences, so its flag stays unknown.
		a.Mu.Lock()
		a.recordPlanRequestLocked(mode)
		a.Mu.Unlock()
	}
	return snap, nil
}

// applyStartupSettings pins the model, thought level and mode the launch asked
// for. Each step is best-effort and reports what the app-server settled on.
func (a *Agent) applyStartupSettings(req zcodeSettingsRequest, timeout time.Duration) {
	// Opening a session reports its own settings. The request remains local until
	// a setter succeeds, so a refused request cannot replace those observations.
	a.Mu.Lock()
	observedMode, observedBoth := a.permissionModeLocked(), a.modeObserved && a.planObserved
	model, level, mode := a.model, a.thoughtLevel, req.Mode
	if req.Model != "" {
		model = req.Model
	}
	if req.ThoughtLevel != "" {
		level = req.ThoughtLevel
	}
	a.Mu.Unlock()
	applyLevel := level != "" && level != agent.EffortAuto
	if model != "" {
		res, err := a.applyZCodeModel(model, level, timeout)
		if err != nil {
			slog.Warn("zcode setModel on startup failed", "agent_id", a.AgentID(), "model", model, "error", err)
			// The ladder rejected the level the launch asked for. The separate
			// setter must never reapply a level that was just rejected.
			if zcodeIsReasoningLevelRejection(err) {
				applyLevel = false
			}
		} else if res.Recovered || res.ThoughtLevel == level {
			// The switch settled on the requested level, or the ladder recovered a
			// different one. Either way the level needs no second request.
			applyLevel = false
		}
	}
	// EffortAuto is LeapMux's sentinel for "send no thought level at all", so the
	// app-server keeps whatever default it resolved for the model.
	a.Mu.Lock()
	unavailable := a.zcodeCurrentModelUnavailableLocked()
	a.Mu.Unlock()
	if applyLevel && !unavailable {
		if err := a.applyZCodeThoughtLevel(level, timeout); err != nil {
			slog.Warn("zcode setThoughtLevel on startup failed", "agent_id", a.AgentID(), "level", level, "error", err)
		}
	}
	// session/create HONORS its mode parameter, and openSession sends it, so the
	// session usually already runs in the requested mode and the setter is one
	// blocking RPC of pure repetition. The comparison is against the axis value of the
	// opened session: `settings.mode.current` from the create or resume reply -- the
	// app-server's live native mode, never `session.mode`, which reports the
	// projection's seed (`build`) whatever the session runs in -- and the plan flag
	// that the create request stated. A reply that reported no mode is no evidence,
	// and neither is a resume, whose plan flag ZCode restores and reports nowhere: the
	// setter runs. The cost of a redundant RPC is a round trip, and the cost of a wrong
	// skip is a session that runs in another session's mode.
	if mode != "" && (!observedBoth || mode != observedMode) {
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
//
// The reply must be the full {sessionId, eventSeq, events} document for the
// session the request specified. `snapshot` is optional and ignored when present.
// A decode or identity failure is returned: startup and ClearContext must not
// proceed on a subscription that proved nothing.
//
// Events the subscription replays arrive in the response and are dispatched
// through the ordered coordinator. lastSeq advances only through dispatched
// events; response.eventSeq is never assigned to it, so a truncated response
// leaves the gap for replayZCodeEvents to recover.
func (a *Agent) subscribe(timeout time.Duration) error {
	a.Mu.Lock()
	identity := a.captureZCodeObservationIdentityLocked()
	sessionID, afterSeq, epoch := identity.sessionID, a.lastSeq, identity.epoch
	usable := a.zcodeSessionLifetimeCurrentLocked(identity)
	a.Mu.Unlock()

	if !usable {
		return errNoZCodeSession
	}
	raw, err := a.sendZCodeSessionRequest(a.Context(), identity, MethodSessionSubscribe, map[string]any{
		"sessionId":       sessionID,
		"deliveryKind":    DeliveryContinuous,
		"includeSnapshot": false,
		"afterSeq":        afterSeq,
	}, timeout, nil)
	if err != nil {
		return err
	}
	// The pointers separate an absent field from a present zero: eventSeq 0 is
	// the sequence of a fresh session, while a missing field is not an answer.
	var resp struct {
		SessionID *string               `json:"sessionId"`
		EventSeq  *int64                `json:"eventSeq"`
		Events    *[]zcodeEventEnvelope `json:"events"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("decode %s response: %w", MethodSessionSubscribe, err)
	}
	if resp.SessionID == nil || *resp.SessionID == "" {
		return fmt.Errorf("%s returned no session id", MethodSessionSubscribe)
	}
	if *resp.SessionID != sessionID {
		return fmt.Errorf("%s returned a receipt for session %s, expected %s", MethodSessionSubscribe, *resp.SessionID, sessionID)
	}
	if resp.EventSeq == nil || *resp.EventSeq < 0 {
		return fmt.Errorf("%s returned no valid event sequence", MethodSessionSubscribe)
	}
	if resp.Events == nil {
		return fmt.Errorf("%s returned no event list", MethodSessionSubscribe)
	}
	events, err := prepareZCodeArchiveEvents(*resp.Events, sessionID)
	if err != nil {
		return fmt.Errorf("%s: %w", MethodSessionSubscribe, err)
	}
	// The receipt's identity applies to ID-less entries too. A session replacement
	// during this request must not admit its events or watermark to the new lifetime.
	a.dispatchMu.Lock()
	a.Mu.Lock()
	current := a.zcodeSessionLifetimeCurrentLocked(identity)
	a.Mu.Unlock()
	if !current {
		a.unlockZCodeDispatch()
		return errNoZCodeSession
	}
	a.markZCodeCanonicalRecoveryLocked()
	for _, event := range events {
		a.admitZCodeEventLocked(event)
	}
	a.unlockZCodeDispatch()
	if err := a.ensureZCodeReplayThrough(a.Context(), sessionID, *resp.EventSeq, epoch); err != nil {
		return fmt.Errorf("recover %s events: %w", MethodSessionSubscribe, err)
	}

	return nil
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

// applyCompleteSessionSnapshot folds create/resume, whose model list is complete.
func (a *Agent) applyCompleteSessionSnapshot(snap zcodeStateSnapshot) {
	a.adoptCompleteZCodeSessionSnapshot(snap, false)
}

// adoptCompleteZCodeSessionSnapshot commits a new session lifetime with its replay state.
// A create replaces the lifetime even if the response repeats the native session ID.
func (a *Agent) adoptCompleteZCodeSessionSnapshot(snap zcodeStateSnapshot, fresh bool) {
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	oldSessionID := a.sessionID
	replace := fresh || snap.Session.SessionID != oldSessionID
	if replace {
		a.sessionEpoch++
		a.sessionChanging = true
	}
	a.Mu.Unlock()
	if replace {
		a.retireZCodeSessionObservationLocked()
		if oldSessionID != "" {
			a.retireZCodeSessionState(oldSessionID)
		}
		a.invalidateZCodeEventReplayLocked()
	}
	a.Mu.Lock()
	if replace {
		a.resetZCodeSessionCountersLocked()
	}
	if snap.Settings != nil && snap.Settings.Model != nil && snap.Settings.Model.Available != nil {
		a.replaceZCodeLiveModelsLocked(snap.Settings.Model.Available, snap.Settings.Model.Current)
	}
	a.applyParsedStateSnapshotLocked(snap)
	a.lastSeq = max(a.lastSeq, snap.Runtime.EventSeq)
	if snap.Runtime.ActiveTurnID != "" && a.turns[snap.Runtime.ActiveTurnID] == nil {
		a.createResumedZCodeTurnLocked(snap)
	}
	a.Mu.Unlock()
	if replace {
		a.sink.AdoptNativeSession(snap.Session.SessionID)
		a.Mu.Lock()
		a.sessionChanging = false
		a.Mu.Unlock()
	}
}

// retireZCodeSessionState finishes the replaced session before adoption can expose
// new output. The caller holds dispatchMu and issues no native request here.
func (a *Agent) retireZCodeSessionState(sessionID string) {
	a.cancelZCodeRuntimeHeaderAnswersForSession(sessionID)
	a.dropZCodeSteersOfReplacedSession()
	a.retryResolution.Reset()
	a.stopAllZCodeTurnWatchers()
	a.endZCodeCompaction()
	a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteZCodeTools(agent.MessageCompletionInterrupted)
	a.Mu.Lock()
	a.clearZCodeTurnsLocked()
	a.compactionSessionID = ""
	a.cancelStoppedZCodeTurnLocked()
	a.TurnToolUses = 0
	a.latestContextUsage = nil
	a.sessionCostUsd = 0
	a.sessionCostKnown = false
	clear(a.toolInvocations)
	a.nextToolOrder = 0
	upserts := a.workflows.clear(agent.IncompleteTaskStatus(agent.MessageCompletionInterrupted))
	a.Mu.Unlock()
	a.upsertZCodeWorkflowRows(upserts)
	a.withdrawPendingControls()
	a.toolCallPrompts.Clear()
	a.children.clear()
	a.ResetCumulativeOutput()
	// Pending writes retain their captured old session and must survive a storage failure.
	a.generationBuffer.DiscardMatching(func(string) bool { return true })
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.ResetSpans()
	a.clearZCodeGoal(false)
}

func (a *Agent) resetZCodeSessionCountersLocked() {
	a.observationSequence++
	a.modelObserved = false
	a.thoughtLevelObserved = false
	a.observedThoughtLevels = nil
	a.observedThoughtDefault = ""
	a.lastSeq = 0
	a.stateRevision = 0
	a.modeObserved = false
	a.planEnabled = false
	a.planObserved = false
	a.pendingExitMode = ""
}

// createResumedZCodeTurnLocked adopts the active turn a resume snapshot
// reports. The owner starts without content or a user-message anchor.
// Recovery can select native messages whose turn anchor matches this turn.
// The caller holds a.Mu.
func (a *Agent) createResumedZCodeTurnLocked(snap zcodeStateSnapshot) {
	a.createZCodeTurnLocked(zcodeTurnState{
		id:                snap.Runtime.ActiveTurnID,
		background:        false,
		admissionRevision: snap.Runtime.StateRevision,
	})
}

func (a *Agent) applyParsedStateSnapshotLocked(snap zcodeStateSnapshot) {
	if zcodeUsableSessionID(snap.Session.SessionID) {
		if snap.Session.SessionID != a.sessionID {
			// Sequence and revision counters belong to one native session, and so does
			// what LeapMux knows about its mode. A resumed session restores its plan
			// flag from ZCode's store, and no snapshot states it, so the flag is
			// unknown until a mode request or a mode event states it.
			a.resetZCodeSessionCountersLocked()
		}
		a.sessionID = snap.Session.SessionID
	}
	a.applyZCodeRuntimeStateLocked(&snap.Runtime)
	if snap.Projection.ContextWindow > 0 {
		a.contextWindow = snap.Projection.ContextWindow
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
	// a context clear would silently drop the level and the model back to them. The
	// mode is the axis value, so a session in plan mode is replaced by one in plan mode.
	current := zcodeSettingsRequest{Model: a.model, ThoughtLevel: a.thoughtLevel, Mode: a.permissionModeLocked()}
	oldSessionID := a.sessionID
	a.Mu.Unlock()

	if err := a.openSession("", timeout); err != nil {
		return "", err
	}
	a.applyStartupSettings(current, timeout)
	if err := a.reconcileZCodeConversation(); err != nil {
		slog.Warn("zcode conversation subscription", "agent_id", a.AgentID(), "error", err)
	}
	a.PublishTurnActive()
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()

	if sessionID == "" {
		return "", fmt.Errorf("the new ZCode session has no ID")
	}
	err := a.subscribe(timeout)
	if oldSessionID != "" && oldSessionID != sessionID {
		go a.retireZCodeSession(oldSessionID)
	}
	return sessionID, err
}

// retireZCodeSession stops the old runtime before it closes the old session.
// A close alone does not cancel an outstanding interaction request.
func (a *Agent) retireZCodeSession(sessionID string) {
	params := map[string]any{"sessionId": sessionID}
	if _, err := a.sendZCodeRequest(MethodSessionStop, params, zcodeStopTimeout); err != nil && !zcodeIsSessionGone(err) {
		slog.Debug("zcode failed to stop a retired session", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
	if _, err := a.sendZCodeRequest(MethodSessionClose, params, a.APITimeout()); err != nil && !zcodeIsSessionGone(err) {
		slog.Debug("zcode failed to close a retired session", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
}

func zcodeIsSessionGone(err error) bool {
	code, ok := zcodeErrorCode(err)
	return ok && (code == ErrMethodNotFound || code == ErrSessionNotActive)
}

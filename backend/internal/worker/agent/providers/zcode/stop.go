package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeStopTimeout limits a session/stop. The app-server aborts the turn's
// controller synchronously and replies with an empty object, so a longer wait only
// makes a user-driven interrupt look slower than it is.
const zcodeStopTimeout = 2 * time.Second

// zcodeStoppedSilenceWindow is how long a stopped turn may say NOTHING before
// LeapMux ends it locally.
//
// The window has to outlast the gaps a live turn leaves between its own frames. A
// census of a hundred and twenty reads left gaps up to eighteen seconds while the
// agent was plainly still working, so thirty seconds is the margin above that.
const zcodeStoppedSilenceWindow = 30 * time.Second

// zcodeStopIgnoredGrace is how long a turn may keep SPEAKING after the
// app-server accepted a stop before LeapMux calls the stop ignored.
//
// The shipped app-server clears the turn's abort controller at admission, so a
// session/stop that lands after those first moments aborts nothing: the tool
// runs on, the runtime makes its next model call, and the turn completes as if
// no stop was asked. Session events that keep arriving past this grace are that
// no-op -- the row they trigger tells the reader to press Interrupt again, and the
// second press is the one the worker may escalate into a forced restart.
const zcodeStopIgnoredGrace = 3 * time.Second

// stoppedTurnWindow watches ONE stop that the app-server accepted.
//
// The fields move together under three rules that used to live only in prose,
// spread over seven methods: an ARM swaps the timer and raises the generation while
// it KEEPS the stop's timestamp, once-flag and watched turn, a CANCEL retires
// everything, and an OPEN stamps a fresh stop against the turn it stopped. Each is
// one method here, so a future edit cannot take half of one -- which is how one
// Stop press wrote two interrupted rows.
//
// It takes NO lock of its own. Every caller holds the agent's a.Mu, and three of
// them require the window move and the turn-state write to sit in the SAME critical
// section: settleZCodeTurn, ClearContext and Stop each say so at their own site. A
// self-locking window would break that atomicity.
type stoppedTurnWindow struct {
	// timer fires once the agent has been silent for zcodeStoppedSilenceWindow.
	timer *time.Timer
	// generation identifies the window that timer watches. Every arm and every
	// cancel raises it, so a callback proves it still owns the current window by
	// comparing the generation it captured.
	//
	// time.Timer.Stop cannot stop a callback that already began, so a cancel and a
	// refresh both leave one running with nothing to refuse it. Without the
	// generation the callback ends a turn whose stop another path already recorded,
	// and the reader gets TWO interrupted rows for one Stop press.
	generation uint64
	// turnID and turnGeneration identify the TURN this window watches: the owner
	// record's key and the generation stored on it at the open. The callback
	// settles that owner only -- never whichever turn started most recently --
	// and retires itself silently when the turn ended or was replaced meanwhile.
	turnID         string
	turnGeneration uint64
	// armedAt is when the app-server ACCEPTED the stop this window watches. Zero
	// whenever no window is armed. It clocks the two decisions a no-op stop drives:
	// when the "stop ignored" row may be written (events still arriving past the
	// grace) and when a second Interrupt may escalate to a forced stop.
	armedAt time.Time
	// notified keeps the ignored-stop row to ONE per accepted stop. A cancel clears
	// it and an open clears it; an ARM keeps it, because the window an arm swaps in
	// watches the same accepted stop the old one did.
	notified bool
	// contexts identify each native stop that this window observes.
	contexts []agent.StopContext
}

// open records a stop that Interrupt or SendRawInput received a native acknowledgement for.
// turnID and turnGeneration identify the owner the stop targeted; both are empty
// and zero when the stop targeted a native compaction instead.
func (w *stoppedTurnWindow) open(now time.Time, stop agent.StopContext, turnID string, turnGeneration uint64) {
	w.armedAt = now
	w.notified = false
	w.contexts = append(w.contexts, stop)
	w.turnID = turnID
	w.turnGeneration = turnGeneration
}

// arm swaps in a fresh timer and returns the generation it owns.
//
// The timer is dropped, not CANCELLED: a cancel also retires the stop's timestamp
// and once-flag, and an arm must keep both -- the window it swaps in watches the
// same accepted stop.
func (w *stoppedTurnWindow) arm(after func(time.Duration, func()) *time.Timer, d time.Duration, fire func(generation uint64)) {
	w.dropTimer()
	w.generation++
	generation := w.generation
	w.timer = after(d, func() { fire(generation) })
}

// cancel retires the whole window, because the turn ended on its own.
//
// time.Timer.Stop takes no lock of the agent's, so it is safe under a.Mu. The
// callback it cannot stop -- one already running on the timer goroutine -- takes
// a.Mu itself and finds the generation raised past the one it captured. The
// generation is what refuses it: a turn-state check alone cannot, because Stop
// clears the turn state and the window in one section and the callback may have
// read its guards already.
func (w *stoppedTurnWindow) cancel() {
	w.dropTimer()
	w.generation++
	w.turnID = ""
	w.turnGeneration = 0
	w.armedAt = time.Time{}
	w.notified = false
	w.contexts = nil
}

// armed reports whether a stop is being watched now.
func (w *stoppedTurnWindow) armed() bool { return w.timer != nil }

// owns reports that the generation a callback captured is still the current one.
func (w *stoppedTurnWindow) owns(generation uint64) bool { return w.generation == generation }

// provenIgnored reports that the app-server ACCEPTED a stop and went on speaking
// past the grace, which is the only evidence a stop was ignored.
func (w *stoppedTurnWindow) provenIgnored(grace time.Duration) bool {
	return w.armed() && time.Since(w.armedAt) >= grace
}

func (w *stoppedTurnWindow) dropTimer() {
	if w.timer == nil {
		return
	}
	w.timer.Stop()
	w.timer = nil
}

// armStoppedZCodeTurnLocked starts the window that ends a turn the app-server
// accepted a stop for and then never reported. The caller holds a.Mu.
//
// It replaces an immediate local end. The app-server accepts `session/stop` in both
// of the cases LeapMux must tell apart: the one where the abort cuts the model
// stream, and the one where the turn goes on making tool calls for minutes. Ending
// the turn on the accepted stop alone read the second case as an idle chat while the
// agent was still writing to it.
//
// Silence is the signal, not elapsed time. Every event of the watched turn's
// own work restarts the window (see stopEventExtendsLocked), so a turn that
// still speaks keeps the indicator it has earned, and a turn that stops
// speaking ends once.
//
// It refuses once the agent goes down. Stop and Wait drop the window, and the read
// loop can still deliver a frame after they do: a re-armed window would fire into a
// dead process, end a turn the tear-down already ended, and write its stop row after
// the transcript closed. The three flags are the three ways the agent goes down --
// intentionalStop marks the graceful stop before Process.Stop sets stopped, and
// processExited marks an exit nobody asked for.
func (a *Agent) armStoppedZCodeTurnLocked() {
	if (!a.turnActive && a.compactionSessionID == "") || a.StoppedLocked() || a.ProcessExitedLocked() || a.IntentionalStopRequested() {
		return
	}
	after := a.afterFunc
	if after == nil {
		after = time.AfterFunc
	}
	a.stopWindow.arm(after, zcodeStoppedSilenceWindow, a.endStoppedZCodeTurn)
}

// refreshStoppedZCodeTurn restarts the window, because the stopped turn just
// spoke.
//
// A no-op unless a stop armed the window AND the event reports the work the
// window watches, so an ordinary turn pays nothing and background traffic never
// postpones a stopped turn's end.
//
// The check and the re-arm share ONE critical section. Split in two, a turn that
// ended between them re-armed the window it had just dropped, because the re-arm
// read a turnActive that the other goroutine was about to clear.
//
// Speaking past the grace is also the only evidence an accepted stop was
// IGNORED: the first event to arrive after it writes the stop-ignored row, once
// per accepted stop.
func (a *Agent) refreshStoppedZCodeTurn(event zcodeEventEnvelope) {
	a.Mu.Lock()
	if !a.stopWindow.armed() || !a.stopEventExtendsLocked(event) {
		a.Mu.Unlock()
		return
	}
	notifyIgnored := !a.stopWindow.notified && a.stopWindow.provenIgnored(zcodeStopIgnoredGrace)
	var contexts []agent.StopContext
	if notifyIgnored {
		a.stopWindow.notified = true
		contexts = a.stopWindow.contexts
		a.stopWindow.contexts = nil
	}
	a.armStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	if notifyIgnored {
		a.persistZCodeStopIgnoredRow(contexts)
	}
}

// stopEventExtendsLocked reports whether one session event proves the work the
// window watches is still running. The caller holds a.Mu.
//
// An event that IDENTIFIES a turn belongs to that turn alone: a background turn's
// events say nothing about the stopped user turn, and the window must never
// keep a stopped turn alive on someone else's speech. The match takes the turn
// id the window opened with and the generation of its owner record -- a
// provisional turn adopts its native id at turn.started, and the generation is
// the identity that survives the rename.
//
// An event that identifies no turn extends only when its type reports the watched
// work itself: the turn's lifecycle, model stream and tools, or the model
// request of the running turn. A background task's notifications, subagent
// traffic and the rest of the session machinery are none of the stopped turn's
// speech and must not postpone its end. A compaction's own progress extends a
// compaction window.
func (a *Agent) stopEventExtendsLocked(event zcodeEventEnvelope) bool {
	w := &a.stopWindow
	if event.TurnID != "" {
		if w.turnID == "" {
			return false
		}
		if event.TurnID == w.turnID {
			return true
		}
		eventOwner := a.turns[event.TurnID]
		return eventOwner != nil && eventOwner.generation == w.turnGeneration
	}
	if w.turnID == "" && (a.compactionSessionID == "" || w.turnGeneration != a.compactionGeneration) {
		return false
	}
	return stopEventReportsZCodeWork(event, w.turnID == "")
}

// stopEventReportsZCodeWork reports whether one ID-less session event reports
// the work the stop window watches. compacting states a compaction window, whose
// progress is the compaction's own session updates.
func stopEventReportsZCodeWork(event zcodeEventEnvelope, compacting bool) bool {
	switch event.Type {
	case contracts.ZCodeEventTurnStarted, contracts.ZCodeEventTurnCompleted,
		contracts.ZCodeEventTurnFailed, contracts.ZCodeEventModelStreaming,
		contracts.ZCodeEventToolUpdated:
		return true
	case contracts.ZCodeEventSessionUpdated:
		var payload zcodeSessionUpdatedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return false
		}
		switch payload.kind() {
		case zcodeSessionUpdateNetworkStatus:
			return zcodeIsMainQuery(payload.QuerySource)
		case zcodeSessionUpdateModelResponse:
			stopReason, _ := providerkit.RawString(payload.StopReason)
			return zcodeIsMainReply(payload.QuerySource, stopReason)
		case zcodeSessionUpdateCompaction, zcodeSessionUpdateMicrocompact:
			return compacting
		case zcodeSessionUpdateDrop, zcodeSessionUpdateBackgroundTask,
			zcodeSessionUpdateSubagent, zcodeSessionUpdateGoalVerification,
			zcodeSessionUpdateGoalChange, zcodeSessionUpdateModeChange,
			zcodeSessionUpdateModelSelected, zcodeSessionUpdateHookRun,
			zcodeSessionUpdateHookAdmission, zcodeSessionUpdateSteerDiscarded:
			// Background notifications and session machinery: none of it is the
			// watched work speaking.
			return false
		}
	}
	return false
}

// cancelStoppedZCodeTurn drops the window, because the turn ended on its own.
func (a *Agent) cancelStoppedZCodeTurn() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.cancelStoppedZCodeTurnLocked()
}

// cancelStoppedZCodeTurnLocked drops the window, because the turn ended on its
// own. The caller holds a.Mu. See stoppedTurnWindow.cancel.
func (a *Agent) cancelStoppedZCodeTurnLocked() {
	a.stopWindow.cancel()
}

// zcodeStopTargetLocked returns the turn a stop targets: the user's own turn
// when one runs, else the latest active background turn. The caller holds a.Mu.
func (a *Agent) zcodeStopTargetLocked() *zcodeTurnState {
	if turn := a.foregroundZCodeTurnLocked(); turn != nil {
		return turn
	}
	var latest *zcodeTurnState
	for _, turn := range a.turns {
		if turn.settled {
			continue
		}
		if latest == nil || turn.generation > latest.generation {
			latest = turn
		}
	}
	return latest
}

// sendZCodeStopExecution stops the turn the caller selected and reports whether
// ZCode ACCEPTED the stop. accepted == false with a nil error states the guarded
// stop found its execution gone: the caller must arm no window and send no
// second stop, because an unguarded stop could abort a newer execution.
//
// The v4 command is the primary path and carries the captured execution id as
// its guard. The legacy session/stop covers the two cases the guard cannot: an
// owner that recorded no execution id, and an app-server with no command inbox.
// Every other refusal and every transport failure returns the error and falls
// through to nothing.
func (a *Agent) sendZCodeStopExecution(sessionID, executionID string) (accepted bool, err error) {
	if executionID == "" {
		// An unknown execution id selects the legacy stop. A v4 stop without
		// the guard aborts whatever execution is current.
		_, err = a.sendZCodeRequest(MethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout)
		return err == nil, err
	}
	ack, err := a.sendZCodeStopCommand(sessionID, executionID)
	if err != nil {
		if zcodeIsMethodNotFound(err) {
			_, legacyErr := a.sendZCodeRequest(MethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout)
			return legacyErr == nil, legacyErr
		}
		return false, err
	}
	switch ack.Status {
	case zcodeCommandAccepted, zcodeCommandDuplicate:
		return true, nil
	case zcodeCommandNoop:
		if ack.ReasonCode == zcodeCommandGuardStopTargetChanged {
			return false, nil
		}
	}
	return false, fmt.Errorf("ZCode did not accept the stop: %s", zcodeCommandRefusal(ack))
}

// Interrupt aborts the running turn.
//
// A no-op when no turn is active, so a caller need not probe first.
//
// A steer still queued for the turn is taken back first: the stop would drain it
// into the conversation, where no model request reads it. The queue then runs it
// as the next turn. steerMu holds every new steer until the stop is sent.
//
// The stop targets the user's turn, the latest background turn when no user turn
// runs, or the live compaction. When the owner recorded a foreground execution
// id, the primary path is the guarded v4 `stop` command: it aborts that one
// execution and refuses one that took its place. The legacy session/stop remains
// for an owner with no execution id and for an app-server with no command inbox,
// and for the compaction. Never an unguarded v4 stop: it can abort a newer
// execution than the one the user stopped.
//
// The acknowledgement is compared against the generation stored on that OWNING
// turn: a background turn that starts meanwhile neither invalidates the stop nor
// gets settled by its window.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	owner := a.zcodeStopTargetLocked()
	executionID := ""
	if owner != nil {
		executionID = owner.foregroundExecutionID
	}
	compactionGeneration, compactionSessionID, sessionID := a.compactionGeneration, a.compactionSessionID, a.sessionID
	a.Mu.Unlock()
	if (owner == nil && compactionSessionID == "") || sessionID == "" {
		return nil
	}
	a.withdrawZCodeSteersLocked()
	a.beginZCodeStop()
	accepted, err := a.sendZCodeStopExecution(sessionID, executionID)
	// The end of the request, the open and the arm share ONE critical section. Split
	// in two, a reader that ran between them saw a fresh armedAt with no window
	// armed -- the half-applied state that stopProvenIgnoredLocked must never
	// observe -- or a cancellation that it could attribute to no stop.
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.stopsInFlight--
	if err != nil {
		// The stop stopped nothing, so the turn is still running. Reporting it
		// finished would hide a live agent behind an idle chat.
		return err
	}
	if !accepted {
		// The guarded stop found its execution gone. Arm nothing and send no
		// second stop: an unguarded stop could abort a newer execution.
		return nil
	}
	a.acceptZCodeStopLocked(stop, sessionID, owner, compactionGeneration)
	return nil
}

// beginZCodeStop counts a session/stop that is about to be sent.
//
// ZCode can report what the stop cut before LeapMux reads the stop's reply: the
// read loop hands the reply to the waiting stop and handles the next frames at
// once, before the stop takes a.Mu to arm its window. A cancellation that arrives
// in that gap is still the stop's work, and stopCanCancelLocked counts it so.
func (a *Agent) beginZCodeStop() {
	a.Mu.Lock()
	a.stopsInFlight++
	a.Mu.Unlock()
}

// stopCanCancelLocked reports whether a stop of LeapMux's can explain a native
// cancellation now: a stop waits for its reply, or the window watches one that
// ZCode accepted. The caller holds a.Mu.
func (a *Agent) stopCanCancelLocked() bool {
	return a.stopsInFlight > 0 || a.stopWindow.armed()
}

// SendRawInput observes native stop replies without changing the caller's frame.
// A stop request requires its native integer ID and the current native session.
// It takes the queued steers back first, as Interrupt does. The answer to the
// hook-trust question goes to the question, because no ZCode request waits for it.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	if handled, err := a.workspaceTrust.Answer(a.sink, zcodeWorkspaceTrustHost{a}, data); handled {
		return err
	}
	var method struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(data, &method) != nil || method.Method != MethodSessionStop {
		return a.sendControlOrRaw(data, stop)
	}
	var frame struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if json.Unmarshal(data, &frame) != nil {
		return fmt.Errorf("decode ZCode stop request")
	}
	if frame.ID == nil {
		return fmt.Errorf("ZCode stop request has no integer ID")
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	owner := a.zcodeStopTargetLocked()
	compactionGeneration := a.compactionGeneration
	compactionSessionID := a.compactionSessionID
	active := owner != nil || compactionSessionID != ""
	a.Mu.Unlock()
	if frame.Params.SessionID == "" || frame.Params.SessionID != sessionID {
		return agent.ErrInputSessionChanged
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	if active {
		a.withdrawZCodeSteersLocked()
		a.beginZCodeStop()
	}
	_, err := a.sendZCodeFrameContext(a.Context(), *frame.ID, MethodSessionStop, data, zcodeStopTimeout, nil)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if active {
		a.stopsInFlight--
	}
	if err != nil {
		return err
	}
	// The window watches the turn the stop targeted, and the acknowledgement is
	// compared against that OWNING turn's generation -- see Interrupt.
	if !active || a.sessionID != sessionID {
		return nil
	}
	a.acceptZCodeStopLocked(stop, sessionID, owner, compactionGeneration)
	return nil
}

// acceptZCodeStopLocked installs a window only for the work captured before the RPC.
func (a *Agent) acceptZCodeStopLocked(stop agent.StopContext, sessionID string, owner *zcodeTurnState, compactionGeneration uint64) {
	if a.sessionID != sessionID {
		return
	}
	if owner != nil {
		if a.turns[owner.id] != owner || owner.settled {
			return
		}
		a.stopWindow.open(time.Now(), stop, owner.id, owner.generation)
	} else {
		if a.compactionSessionID == "" || a.compactionGeneration != compactionGeneration {
			return
		}
		a.stopWindow.open(time.Now(), stop, "", compactionGeneration)
	}
	a.armStoppedZCodeTurnLocked()
}

// InterruptEscalationReady reports whether an EARLIER accepted stop has been
// proven ignored and a fresh Interrupt may escalate to a forced stop.
//
// The proof is the still-armed window (the stop was accepted and the turn never
// ended) plus the grace (the turn had its chance to fall silent). A stop that
// worked leaves nothing armed; a stop too recent to judge stays unescalated, so
// a double-click retries the plain stop instead of restarting the agent.
func (a *Agent) InterruptEscalationReady() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.stopProvenIgnoredLocked()
}

// stopProvenIgnoredLocked reports whether the armed window's stop is proven
// ignored: the app-server accepted it, and the turn has had the grace to fall
// silent and did not. The caller holds a.Mu.
//
// ONE rule, and it decides two different things -- the stop-ignored row that
// refreshStoppedZCodeTurn writes, and the escalation to a forced restart that
// InterruptEscalationReady permits. Spelled twice, the two drift apart.
func (a *Agent) stopProvenIgnoredLocked() bool {
	return a.stopWindow.provenIgnored(zcodeStopIgnoredGrace)
}

// endStoppedZCodeTurn records what session/stop actually cut.
//
// The app-server's handler aborts the running model stream and answers an empty
// object. It announces the end of the turn ONLY sometimes: a census of this provider
// recorded a turn whose model stream the abort cut, and that turn sent neither
// turn.completed nor turn.failed, which left the turn active for the rest of the
// session -- the thinking indicator ran without end, the durable input queue could
// never drain, and the model output the turn already produced reached no store. Every other provider reports the end of
// the turn it cancelled, so every other provider's Interrupt can wait for that report.
//
// It settles only the OWNER the window watches: the turn the stop targeted. A
// background turn running beside it keeps its output, its tools and its
// activity. The compaction state goes only when the stop watched the compaction
// itself.
//
// What this does NOT do is close the turn's tool calls. The abort reaches the model
// stream, not a command the runtime already launched: an interrupted `sleep 90` ran
// its full ninety seconds and then reported success. Marking that call interrupted
// would state an outcome the runtime goes on to contradict, so an open call keeps its
// running card until its own update arrives, or until the process ends. That is also
// why the settled owner stays in turns until its last tool result.
//
// No PROVIDER row is written either. A turn-end divider is built from the frame that
// reports the end, the app-server sent none, and LeapMux invents no provider output. A
// turn.completed that arrives later still writes its own divider through
// finishZCodeTurn. What the reader gets instead is LeapMux's own row, from
// persistZCodeStopRow.
//
// See ZC-001 in docs/provider-parity/protocol-evidence.md.
func (a *Agent) endStoppedZCodeTurn(generation uint64) {
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	if !a.stopWindow.owns(generation) {
		// This callback watched a window that a cancel or a refresh already retired.
		// time.Timer.Stop cannot stop a callback that already started, so the one
		// that lost that race arrives here. It must write nothing: the path that
		// retired the window either recorded the stop itself (Stop does) or armed a
		// fresh window that is still watching (a refresh does).
		a.Mu.Unlock()
		return
	}
	turnID, ownerGeneration := a.stopWindow.turnID, a.stopWindow.turnGeneration
	var owner *zcodeTurnState
	if turnID != "" {
		owner = a.turns[turnID]
	}
	if turnID != "" && (owner == nil || owner.generation != ownerGeneration || owner.settled) {
		// The turn ended or was replaced on its own while this window watched, and
		// the path that ended it left the window armed. Retire it and write nothing.
		a.cancelStoppedZCodeTurnLocked()
		a.Mu.Unlock()
		return
	}
	if turnID == "" && (a.compactionSessionID == "" || ownerGeneration != a.compactionGeneration) {
		// The compaction this window watched ended on its own. Same verdict.
		a.cancelStoppedZCodeTurnLocked()
		a.Mu.Unlock()
		return
	}
	compacting := turnID == ""
	if owner != nil {
		if owner.pendingNative == nil {
			a.stopZCodeTurnWatcherLocked(owner)
		}
		owner.settled = true
		if a.foregroundTurnID == owner.id {
			a.foregroundTurnID = ""
		}
	}
	if compacting {
		a.compactionSessionID = ""
	}
	a.cancelStoppedZCodeTurnLocked()
	a.refreshTurnActiveLocked()
	a.Mu.Unlock()
	defer a.PublishTurnActive()

	if compacting {
		a.endZCodeCompaction()
		// A compaction has no generation scopes of its own, and the buffer holds
		// nothing else at a compaction: flush what is there and stop everything,
		// as this path always did for the one thing it ended.
		a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
		a.ResetCumulativeOutput()
		a.sink.ReportProgress(agent.ResetProgress())
	} else {
		// The text the model had already produced is a real segment that ended early.
		// The buffer is empty when the abort found the stream idle, so this costs
		// nothing in the case where a tool was running. Only the stopped owner's
		// scopes are flushed and only its progress is reset: a turn still running
		// keeps its output and its meters.
		a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionInterrupted)
		a.resetZCodeTurnProgress(owner)
	}
	a.persistZCodeStopRow()
	if owner != nil {
		a.Mu.Lock()
		a.dropZCodeTurnLocked(owner)
		a.Mu.Unlock()
	}
}

// resetZCodeTurnProgress resets one owner's model progress and the cumulative
// output baselines of its own tool calls. Every other turn's scopes and calls
// keep their live state.
func (a *Agent) resetZCodeTurnProgress(owner *zcodeTurnState) {
	a.Mu.Lock()
	var scopes []string
	for scopeID := range owner.contentByScope {
		scopes = append(scopes, scopeID)
	}
	var tools []string
	for toolCallID := range owner.toolInvocationIDs {
		if a.liveZCodeBackgroundToolLocked(toolCallID) {
			continue
		}
		tools = append(tools, toolCallID)
	}
	a.Mu.Unlock()
	for _, scopeID := range scopes {
		a.sink.ReportProgress(agent.ResetModelScopeProgress(scopeID))
	}
	for _, toolCallID := range tools {
		a.ClearCumulativeOutput(toolCallID)
		a.sink.ReportProgress(agent.ResetOutputProgress(toolCallID))
	}
}

// persistZCodeStopRow states the stop that the app-server never reported.
//
// Without it the transcript held NOTHING about the stop: a census pressed Stop on
// `sleep 45` for ten providers, and ZCode alone drew neither a result row nor a turn
// divider. The reader saw a running command card stop being updated.
//
// The row is LeapMux's own, and it states only what LeapMux did: it asked for the
// stop, the app-server accepted it, and the turn then went silent for the whole
// window. It carries the shared `interrupted` notification type, which is the row
// Claude Code's transcript already draws for a stopped turn, so the two read alike.
//
// The spans stay OPEN, unlike the Claude path, which resets them here. The abort
// reaches the model stream and not a command the runtime already launched, so a call
// that is still running needs its span for the update it still sends -- the same
// reason endStoppedZCodeTurn closes no tool call.
func (a *Agent) persistZCodeStopRow() {
	content, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeInterrupted})
	if err != nil {
		slog.Error("zcode marshal stop row", "agent_id", a.AgentID(), "error", err)
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, agent.MessageContent{Original: content}); err != nil {
		slog.Error("zcode persist stop row", "agent_id", a.AgentID(), "error", err)
	}
}

// persistZCodeStopIgnoredRow states that an accepted stop changed nothing.
//
// The row is what turns the no-op from invisible to actionable: the turn keeps
// running, so the transcript owes the reader an explanation and an instruction --
// press Interrupt again, and the worker escalates that press into a forced stop. One
// row per accepted stop, from refreshStoppedZCodeTurn's once-flag.
func (a *Agent) persistZCodeStopIgnoredRow(contexts []agent.StopContext) {
	// Restore the Worker's activity before the transcript write. The provider
	// still runs the same turn even if persistence fails, so a database error
	// must not leave the Interrupt button hidden from the user.
	for _, stop := range contexts {
		stop.ReportIgnored()
	}
	content, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeStopIgnored})
	if err != nil {
		slog.Error("zcode marshal stop-ignored row", "agent_id", a.AgentID(), "error", err)
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, agent.MessageContent{Original: content}); err != nil {
		slog.Error("zcode persist stop-ignored row", "agent_id", a.AgentID(), "error", err)
	}
}

// Stop aborts a running turn, then tears the process down.
//
// The stop is issued SYNCHRONOUSLY before Process.Stop sets stopped and closes
// stdin: on a goroutine it would race that flag and be dropped in the common case.
func (a *Agent) Stop() {
	// NoteIntentionalStop runs first for two reasons: it marks the graceful stop for
	// Wait, and armStoppedZCodeTurnLocked reads it to refuse the window from here on.
	// The cancel that follows therefore drops a window the read loop cannot arm again.
	a.NoteIntentionalStop()
	a.dispatchMu.Lock()
	a.retireZCodeSessionObservationLocked()
	a.unlockZCodeDispatch()
	// A pending window here is a stop the app-server already ignored, and the
	// tear-down is the forced stop that finally ended the turn. The row it earns is
	// read from the window BEFORE the cancel drops it.
	//
	// ONE critical section reads the window, cancels it, and reads the turn state.
	// Split in two, the window's own callback fired in the gap and wrote its own
	// interrupted row, while this function went on to write a second one from the
	// stopWasPending it had already read. The reader saw two rows for one Stop
	// press, and they did not even fold into one notification thread, because the
	// persists between them break it. The cancel raises the window's generation,
	// so the late callback now refuses itself.
	a.Mu.Lock()
	stopWasPending := a.stopWindow.armed()
	a.cancelStoppedZCodeTurnLocked()
	stopped, turnActive, sessionID := a.StoppedLocked(), a.turnActive || a.compactionSessionID != "", a.sessionID
	a.Mu.Unlock()
	if !stopped && turnActive && sessionID != "" {
		// Best-effort: a failure falls through to the hard tear-down below.
		_, _ = a.sendZCodeRequest(MethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout)
	}
	a.Process.Stop()
	a.stopAllZCodeTurnWatchers()
	a.cancelAllZCodeRuntimeHeaderAnswers()
	a.withdrawPendingControls()
	a.Mu.Lock()
	a.compactionSessionID = ""
	// The tear-down clears every turn owner: nothing survives the process.
	a.clearZCodeTurnsLocked()
	a.Mu.Unlock()
	a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteZCodeTools(agent.MessageCompletionInterrupted)
	if stopWasPending {
		a.persistZCodeStopRow()
	}
	a.sink.ReportProgress(agent.ResetProgress())
	a.resetZCodeSteers()
	a.forgetZCodeConversation()
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.dispatchMu.Lock()
	a.retireZCodeSessionObservationLocked()
	a.unlockZCodeDispatch()
	a.stopAllZCodeTurnWatchers()
	a.cancelAllZCodeRuntimeHeaderAnswers()
	a.withdrawPendingControls()
	a.cancelStoppedZCodeTurn()
	a.Mu.Lock()
	a.compactionSessionID = ""
	a.clearZCodeTurnsLocked()
	a.Mu.Unlock()
	completion := a.ProcessExitCompletion()
	a.flushZCodeGeneration(completion)
	a.persistIncompleteZCodeTools(completion)
	a.sink.ReportProgress(agent.ResetProgress())
	a.resetZCodeSteers()
	a.forgetZCodeConversation()
	return err
}

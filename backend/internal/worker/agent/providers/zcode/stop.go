package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
// The four fields move together under three rules that used to live only in prose,
// spread over seven methods: an ARM swaps the timer and raises the generation while
// it KEEPS the stop's timestamp and once-flag, a CANCEL retires all four, and an
// OPEN stamps a fresh stop. Each is one method here, so a future edit cannot take
// half of one -- which is how one Stop press wrote two interrupted rows.
//
// It takes NO lock of its own. Every caller holds the agent's a.Mu, and three of
// them require the window move and the turnActive write to sit in the SAME critical
// section: finishZCodeTurn, ClearContext and Stop each say so at their own site. A
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
	// armedAt is when the app-server ACCEPTED the stop this window watches. Zero
	// whenever no window is armed. It clocks the two decisions a no-op stop drives:
	// when the "stop ignored" row may be written (events still arriving past the
	// grace) and when a second Interrupt may escalate to a forced stop.
	armedAt time.Time
	// notified keeps the ignored-stop row to ONE per accepted stop. A cancel clears
	// it and an open clears it; an ARM keeps it, because the window an arm swaps in
	// watches the same accepted stop the old one did.
	notified bool
}

// open stamps a newly accepted stop. Interrupt alone calls it, before it arms.
func (w *stoppedTurnWindow) open(now time.Time) {
	w.armedAt = now
	w.notified = false
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
// a.Mu itself and finds the generation raised past the one it captured. Do NOT
// rely on turnActive to refuse it: Stop cancels the window without clearing that
// flag, so the callback would run its whole body and write a second stop row.
func (w *stoppedTurnWindow) cancel() {
	w.dropTimer()
	w.generation++
	w.armedAt = time.Time{}
	w.notified = false
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
// Silence is the signal, not elapsed time. Every session event restarts the window,
// so a turn that still speaks keeps the indicator it has earned, and a turn that
// stops speaking ends once.
//
// It refuses once the agent goes down. Stop and Wait drop the window, and the read
// loop can still deliver a frame after they do: a re-armed window would fire into a
// dead process, end a turn the tear-down already ended, and write its stop row after
// the transcript closed. The three flags are the three ways the agent goes down --
// intentionalStop marks the graceful stop before Process.Stop sets stopped, and
// processExited marks an exit nobody asked for.
func (a *Agent) armStoppedZCodeTurnLocked() {
	if !a.turnActive || a.StoppedLocked() || a.ProcessExitedLocked() || a.IntentionalStopRequested() {
		return
	}
	after := a.afterFunc
	if after == nil {
		after = time.AfterFunc
	}
	a.stopWindow.arm(after, zcodeStoppedSilenceWindow, a.endStoppedZCodeTurn)
}

// refreshStoppedZCodeTurn restarts the window, because the agent just spoke.
//
// A no-op unless a stop armed the window, so an ordinary turn pays nothing.
//
// The check and the re-arm share ONE critical section. Split in two, a turn that
// ended between them re-armed the window it had just dropped, because the re-arm
// read a turnActive that the other goroutine was about to clear.
//
// Speaking past the grace is also the only evidence an accepted stop was
// IGNORED: the first event to arrive after it writes the stop-ignored row, once
// per accepted stop.
func (a *Agent) refreshStoppedZCodeTurn() {
	a.Mu.Lock()
	if !a.stopWindow.armed() {
		a.Mu.Unlock()
		return
	}
	notifyIgnored := !a.stopWindow.notified && a.stopWindow.provenIgnored(zcodeStopIgnoredGrace)
	if notifyIgnored {
		a.stopWindow.notified = true
	}
	a.armStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	if notifyIgnored {
		a.persistZCodeStopIgnoredRow()
	}
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

// Interrupt aborts the running turn.
//
// A no-op when no turn is active, so a caller need not probe first. session/stop
// is the same request Stop issues; the app-server aborts the turn's controller and
// replies with an empty object.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	stopped, turnActive, sessionID := a.StoppedLocked(), a.turnActive, a.sessionID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !turnActive || sessionID == "" {
		return nil
	}
	if _, err := a.sendZCodeRequest(MethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout); err != nil {
		// The stop stopped nothing, so the turn is still running. Reporting it
		// finished would hide a live agent behind an idle chat.
		return err
	}
	// The open and the arm share ONE critical section. Split in two, a reader that
	// ran between them saw a fresh armedAt with no window armed -- the half-applied
	// state that stopProvenIgnoredLocked must never observe.
	a.Mu.Lock()
	a.stopWindow.open(time.Now())
	a.armStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	return nil
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
// What this does NOT do is close the turn's tool calls. The abort reaches the model
// stream, not a command the runtime already launched: an interrupted `sleep 90` ran
// its full ninety seconds and then reported success. Marking that call interrupted
// would state an outcome the runtime goes on to contradict, so an open call keeps its
// running card until its own update arrives, or until the process ends.
//
// No PROVIDER row is written either. A turn-end divider is built from the frame that
// reports the end, the app-server sent none, and LeapMux invents no provider output. A
// turn.completed that arrives later still writes its own divider through
// finishZCodeTurn. What the reader gets instead is LeapMux's own row, from
// persistZCodeStopRow.
//
// See ZC-001 in docs/provider-parity/protocol-evidence.md.
func (a *Agent) endStoppedZCodeTurn(generation uint64) {
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
	if !a.turnActive {
		// The turn ended on its own while this window watched, and the path that
		// ended it left the window armed. Retire it and write nothing.
		a.cancelStoppedZCodeTurnLocked()
		a.Mu.Unlock()
		return
	}
	a.turnActive = false
	a.backgroundTurn = false
	a.cancelStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	defer a.PublishTurnActive()

	// The text the model had already produced is a real segment that ended early.
	// The buffer is empty when the abort found the stream idle, so this costs
	// nothing in the case where a tool was running.
	a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
	a.persistZCodeStopRow()
	a.ResetCumulativeOutput()
	a.sink.ReportProgress(agent.ResetProgress())
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
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content); err != nil {
		slog.Error("zcode persist stop row", "agent_id", a.AgentID(), "error", err)
	}
}

// persistZCodeStopIgnoredRow states that an accepted stop changed nothing.
//
// The row is what turns the no-op from invisible to actionable: the turn keeps
// running, so the transcript owes the reader an explanation and an instruction --
// press Interrupt again, and the worker escalates that press into a forced stop. One
// row per accepted stop, from refreshStoppedZCodeTurn's once-flag.
func (a *Agent) persistZCodeStopIgnoredRow() {
	// Restore the Worker's activity before the transcript write. The provider
	// still runs the same turn even if persistence fails, so a database error
	// must not leave the Interrupt button hidden from the user.
	a.sink.ReportInterruptIgnored()
	content, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeStopIgnored})
	if err != nil {
		slog.Error("zcode marshal stop-ignored row", "agent_id", a.AgentID(), "error", err)
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content); err != nil {
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
	// A pending window here is a stop the app-server already ignored, and the
	// tear-down is the forced stop that finally ended the turn. The row it earns is
	// read from the window BEFORE the cancel drops it.
	//
	// ONE critical section reads the window, cancels it, and reads the turn state.
	// Split in two, the window's own callback fired in the gap, found turnActive
	// still set -- Stop never clears it -- and wrote its own interrupted row, while
	// this function went on to write a second one from the stopWasPending it had
	// already read. The reader saw two rows for one Stop press, and they did not
	// even fold into one notification thread, because the persists between them
	// break it. The cancel raises the window's generation, so the late callback now
	// refuses itself.
	a.Mu.Lock()
	stopWasPending := a.stopWindow.armed()
	a.cancelStoppedZCodeTurnLocked()
	stopped, turnActive, sessionID := a.StoppedLocked(), a.turnActive, a.sessionID
	a.Mu.Unlock()
	if !stopped && turnActive && sessionID != "" {
		// Best-effort: a failure falls through to the hard tear-down below.
		_, _ = a.sendZCodeRequest(MethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout)
	}
	a.Process.Stop()
	a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteZCodeTools(agent.MessageCompletionInterrupted)
	if stopWasPending {
		a.persistZCodeStopRow()
	}
	a.sink.ReportProgress(agent.ResetProgress())
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.cancelStoppedZCodeTurn()
	completion := a.ProcessExitCompletion()
	a.flushZCodeGeneration(completion)
	a.persistIncompleteZCodeTools(completion)
	a.sink.ReportProgress(agent.ResetProgress())
	return err
}

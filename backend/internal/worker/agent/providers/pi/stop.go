package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

const piStopAbortDeliveryTimeout = time.Second
const piStopAbortDeliveryTimerTag = "pi-stop-abort-delivery"

// Stop permits graceful abort delivery before it closes the process.
// A delivery deadline prevents a blocked stdin write from delaying that close.
// Stop joins its helper and the output drainer before it flushes unfinished output.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	// ReadLines records process completion after its output handler returns.
	// Release a producer at the first in, first out (FIFO) limit before Process.Stop waits.
	a.disposePiInterruptOutput()
	a.stopPiGoalRefresh()
	a.dialogDeadlines.StopAll()
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	turnActive := a.currentTurnActive
	a.clearPiQuestionStateLocked()
	a.Mu.Unlock()
	var abortDone <-chan struct{}
	if !stopped && turnActive {
		abortDone = a.requestPiShutdownAbort()
	}
	a.Process.Stop()
	if abortDone != nil {
		<-abortDone
	}
	a.interruptOutput.waitForDrain()
	a.flushPiGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompletePiTools(agent.MessageCompletionInterrupted)
	a.sink.ReportProgress(agent.ResetProgress())
}

// requestPiShutdownAbort permits delivery until its deadline, then returns the helper's completion channel.
// Closing stdin releases a helper whose write still waits. Stop joins it after Process.Stop.
func (a *Agent) requestPiShutdownAbort() <-chan struct{} {
	done := make(chan struct{})
	delivered := make(chan error, 1)
	timer := a.Clock().NewTimer(piStopAbortDeliveryTimeout, piStopAbortDeliveryTimerTag)
	go func() {
		defer close(done)
		wait, err := a.beginPiCommand(CommandAbort, nil)
		delivered <- err
		if wait != nil {
			_, _ = wait(time.Second)
		}
	}()
	acknowledge := false
	select {
	case err := <-delivered:
		acknowledge = err == nil
	case <-timer.C:
	case <-a.ProcessDone():
	}
	timer.Stop(piStopAbortDeliveryTimerTag)
	if acknowledge {
		// Successful delivery keeps the native acknowledgement before close.
		<-done
	}
	return done
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.disposePiInterruptOutput()
	a.interruptOutput.waitForDrain()
	a.stopPiGoalRefresh()
	a.dialogDeadlines.StopAll()
	completion := a.ProcessExitCompletion()
	a.flushPiGeneration(completion)
	a.persistIncompletePiTools(completion)
	a.sink.ReportProgress(agent.ResetProgress())
	return err
}

// Interrupt writes the abort command for the active Pi turn.
// piProvider.IsInterrupt recognizes {type:"abort"}. beginPiCommand applies the envelope.
// It then cancels the dialogs that the same turn still owns. See settleOpenDialogs.
//
// A completed write permits dialog cancellation before the acknowledgement wait.
// Pi acknowledges the abort only when the agent is idle. An open dialog keeps it busy.
// Pi handles each stdin line as it arrives, so the abort marks the run first.
// Cancelling a dialog then releases its tool and ends the stopped run.
// Cancelling before the abort would permit the next model request.
//
// An idle turn needs no abort, so callers need not check currentTurnActive first.
// Interrupt still cancels open idle dialogs. The native abort does not settle those dialogs.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	scope, turnActive, stopped := a.beginPiInterrupt()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !turnActive {
		return a.settleOpenDialogs(scope, stop)
	}
	wait, writeErr := a.beginPiCommandObserved(CommandAbort, nil, func(raw json.RawMessage) {
		a.observePiAbortReply(scope, raw)
	})
	a.finishPiInterruptWrite(scope, writeErr)
	if wait == nil {
		return writeErr
	}
	var settleErr error
	if writeErr == nil {
		settleErr = a.settleOpenDialogs(scope, stop)
	}
	// Keep the acknowledgement wait short so a refused stop returns promptly.
	_, replyErr := wait(1 * time.Second)
	if writeErr != nil && replyErr == nil {
		// The matching native acknowledgement resolves an uncertain write.
		return a.settleOpenDialogs(scope, stop)
	}
	return errors.Join(writeErr, settleErr, replyErr)
}

type piInterruptDelivery uint8

const (
	piInterruptPending piInterruptDelivery = iota
	piInterruptDelivered
	piInterruptUncertain
)

type piInterruptScope struct {
	attempt        uint64
	turnGeneration uint64
}

func (a *Agent) beginPiInterrupt() (scope piInterruptScope, active, stopped bool) {
	if !a.interruptOutput.begin(func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		active, stopped = a.currentTurnActive, a.StoppedLocked()
		scope.turnGeneration = a.turnGeneration
		if !active || stopped {
			return false
		}
		a.interruptAttempt++
		scope.attempt = a.interruptAttempt
		if a.interruptRequests == nil {
			a.interruptRequests = make(map[uint64]piInterruptDelivery)
		}
		a.interruptRequests[scope.attempt] = piInterruptPending
		return true
	}) {
		stopped = true
	}
	return
}

func (a *Agent) finishPiInterruptWrite(scope piInterruptScope, err error) {
	a.interruptOutput.finish(func() {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		state, present := a.interruptRequests[scope.attempt]
		if !present || state == piInterruptDelivered {
			return
		}
		switch {
		case err == nil:
			a.interruptRequests[scope.attempt] = piInterruptDelivered
		case errors.Is(err, agent.ErrDeliveryUncertain):
			a.interruptRequests[scope.attempt] = piInterruptUncertain
		default:
			delete(a.interruptRequests, scope.attempt)
		}
	}, a.dispatchPiOutput)
}

func (a *Agent) observePiAbortReply(scope piInterruptScope, raw json.RawMessage) {
	var reply struct {
		Command string `json:"command"`
		Success bool   `json:"success"`
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if _, present := a.interruptRequests[scope.attempt]; !present {
		return
	}
	if json.Unmarshal(raw, &reply) == nil && reply.Command == CommandAbort && reply.Success {
		a.interruptRequests[scope.attempt] = piInterruptDelivered
	} else {
		delete(a.interruptRequests, scope.attempt)
	}
}

func (a *Agent) piInterruptScopeCurrentLocked(scope piInterruptScope) bool {
	if scope.turnGeneration != a.turnGeneration {
		return false
	}
	if scope.attempt == 0 {
		return !a.currentTurnActive
	}
	return a.interruptRequests[scope.attempt] == piInterruptDelivered
}

// piInterruptDeliveredLocked reports whether any abort passed the delivery boundary.
// Pi reports stopReason:"aborted" for a clean abort.
// A running tool can instead report stopReason:"error" and "This operation was aborted".
// That frame shares its shape with a failure. Delivered local intent tells those outcomes apart.
func (a *Agent) piInterruptDeliveredLocked() bool {
	for _, state := range a.interruptRequests {
		if state == piInterruptDelivered {
			return true
		}
	}
	return false
}

package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexPendingTurnStart owns a request until its delivery resolves.
// A caller timeout does not retire the request or release the input queue.
// Agent.Mu protects these fields. The channels retain their identities.
type codexPendingTurnStart struct {
	threadID         string
	input            []map[string]interface{}
	settings         turnSettings
	settingRevisions map[string]uint64
	retry            *codexTurnRetry
	done             chan struct{}
	responseError    chan error
	cancelDone       chan error
	settled          bool
	err              error
	turnID           string
	cancelled        bool
	failureReported  bool
	modelObserved    bool
	effortObserved   bool
}

func newCodexTurnStart(threadID string, input []map[string]interface{}, settings turnSettings) *codexPendingTurnStart {
	return &codexPendingTurnStart{
		threadID: threadID, input: slices.Clone(input), settings: settings,
		done: make(chan struct{}), responseError: make(chan error, 1),
	}
}

// checkCodexInputLocked validates input admission without changing state. The caller holds Mu.
func (a *Agent) checkCodexInputLocked(expected *string) error {
	if err := providerkit.CheckInputSession(expected, a.threadID); err != nil {
		return err
	}
	if a.StoppedLocked() {
		return fmt.Errorf("agent is stopped")
	}
	if a.retry != nil || a.pendingStart != nil {
		return fmt.Errorf("%w: input delivery is pending", agent.ErrAgentBusy)
	}
	if a.mainTurn.id != "" {
		return fmt.Errorf("%w: %s", agent.ErrAgentBusy, a.mainTurn.id)
	}
	if a.threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}
	return nil
}

// dispatchInputForSession captures settings and sends the request under one dispatch lock.
// The caller waits for acceptance after this method releases that lock.
func (a *Agent) dispatchInputForSession(expected *string, input []map[string]interface{}) (*codexPendingTurnStart, error) {
	a.turnDispatchMu.Lock()
	defer a.turnDispatchMu.Unlock()
	a.Mu.Lock()
	if err := a.checkCodexInputLocked(expected); err != nil {
		a.Mu.Unlock()
		return nil, err
	}
	threadID, settings := a.threadID, a.turnSettingsLocked()
	a.Mu.Unlock()
	return a.beginCodexTurnStart(threadID, input, settings, nil)
}

// sendTurnStart sends explicit settings. Runtime input dispatch captures current settings separately.
func (a *Agent) sendTurnStart(threadID string, input []map[string]interface{}, settings turnSettings) error {
	a.turnDispatchMu.Lock()
	pending, err := a.beginCodexTurnStart(threadID, input, settings, nil)
	a.turnDispatchMu.Unlock()
	if pending != nil {
		a.PublishTurnActive()
	}
	if err != nil {
		return err
	}
	return a.awaitCodexTurnStart(pending)
}

// beginCodexTurnStart registers and writes one request. The caller holds turnDispatchMu.
// SendDetachedRequest writes synchronously, then waits for its response separately.
// Do not hold Mu during the write, because the process transport acquires it.
func (a *Agent) beginCodexTurnStart(threadID string, input []map[string]interface{}, settings turnSettings, retry *codexTurnRetry) (*codexPendingTurnStart, error) {
	params := map[string]interface{}{"threadId": threadID, "input": input}
	addCodexTurnSettingsParams(params, settings)
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal turn/start params: %w", err)
	}
	a.Mu.Lock()
	if a.StoppedLocked() || a.threadID != threadID || retry != nil && (a.retry != retry || retry.cancelled) {
		a.Mu.Unlock()
		if retry != nil {
			return nil, errCodexTurnRetryCancelled
		}
		return nil, agent.ErrInputSessionChanged
	}
	if a.pendingStart != nil || a.retry != nil && a.retry != retry {
		a.Mu.Unlock()
		return nil, agent.ErrAgentBusy
	}
	pending := newCodexTurnStart(threadID, input, settings)
	pending.settingRevisions = maps.Clone(a.settingRevisions)
	pending.retry = retry
	a.pendingStart = pending
	if retry != nil {
		retry.start = pending
		retry.input = nil
	}
	a.Mu.Unlock()
	err = a.SendDetachedRequest("turn/start", raw, func(_ json.RawMessage, responseErr error) {
		a.handleCodexTurnStartResponse(pending, responseErr)
	})
	if err != nil {
		a.handleCodexTurnStartResponse(pending, err)
		return pending, classifyCodexTurnStartRequestError("turn/start", err)
	}
	return pending, nil
}

// finishCodexTurnStartLocked settles only this request. The caller holds Mu.
// A response from a retired request must not remove a newer request's state.
func (a *Agent) finishCodexTurnStartLocked(pending *codexPendingTurnStart, turnID string, err error) {
	if pending.settled {
		return
	}
	pending.settled, pending.turnID, pending.err = true, turnID, err
	pending.input = nil
	if a.pendingStart == pending {
		a.pendingStart = nil
	}
	if pending.retry != nil && a.retry == pending.retry {
		a.retry = nil
	}
	close(pending.done)
}

func (a *Agent) handleCodexTurnStartResponse(pending *codexPendingTurnStart, responseErr error) {
	if responseErr == nil {
		return
	}
	err := classifyCodexTurnStartRequestError("turn/start", responseErr)
	processEnded := false
	select {
	case <-a.ProcessDone():
		processEnded = true
	default:
	}
	a.Mu.Lock()
	if pending.settled || a.pendingStart != pending {
		a.Mu.Unlock()
		return
	}
	if errors.Is(err, agent.ErrDeliveryUncertain) && !processEnded {
		pending.responseError <- err
		a.Mu.Unlock()
		return
	}
	a.finishCodexTurnStartLocked(pending, "", err)
	a.Mu.Unlock()
	if pending.retry != nil {
		pending.retry.fail(codexTurnRetryStarting, err)
	} else {
		a.PublishTurnActive()
	}
}

// awaitCodexTurnStart limits the caller's wait, not the request's lifetime.
func (a *Agent) awaitCodexTurnStart(pending *codexPendingTurnStart) error {
	timer := a.Clock().NewTimer(turnStartAckTimeout, codexTurnStartAckTimerTag)
	defer timer.Stop(codexTurnStartAckTimerTag)
	var err error
	select {
	case <-pending.done:
	case err = <-pending.responseError:
	case <-a.ProcessDone():
		err = classifyCodexTurnStartRequestError("turn/start", a.ProcessExitError())
		a.Mu.Lock()
		a.finishCodexTurnStartLocked(pending, "", err)
		a.Mu.Unlock()
	case <-timer.C:
		err = fmt.Errorf("%w: turn/start received no turn/started within %s", agent.ErrDeliveryUncertain, turnStartAckTimeout)
	}
	a.Mu.Lock()
	// A confirmed result wins a simultaneous timeout or transport error.
	if pending.settled {
		err = pending.err
	}
	a.Mu.Unlock()
	return err
}

func classifyCodexTurnStartRequestError(operation string, err error) error {
	return providerkit.ClassifyJSONRPCDeliveryError(operation, err)
}

const codexSteerClientIDPrefix = "leapmux-steer-"

// sendTurnSteer serializes its write with context replacement, then waits without the lock.
// A caller that owns its row itself passes trackEcho false, and the steer joins no ledger.
func (a *Agent) sendTurnSteer(threadID, turnID string, input []map[string]interface{}, trackEcho bool) (ticket providerkit.SteerTicket, err error) {
	defer func() {
		if err != nil {
			a.outputMu.Lock()
			a.forgetCodexSteer(ticket)
			a.outputMu.Unlock()
		}
	}()
	clientID := codexSteerClientIDPrefix + id.Short()
	raw, err := json.Marshal(map[string]interface{}{"threadId": threadID, "expectedTurnId": turnID, "input": input, "clientUserMessageId": clientID})
	if err != nil {
		return 0, fmt.Errorf("marshal turn/steer params: %w", err)
	}
	a.turnDispatchMu.Lock()
	a.Mu.Lock()
	current := a.threadID == threadID && a.mainTurn.id == turnID && a.pendingSteerTurnEnd == nil
	if !current {
		a.Mu.Unlock()
		a.turnDispatchMu.Unlock()
		return 0, agent.ErrNoActiveTurn
	}
	if trackEcho {
		ticket = a.steerLedger.Add(clientID)
		if a.pendingSteerCalls == nil {
			a.pendingSteerCalls = make(map[providerkit.SteerTicket]struct{})
		}
		a.pendingSteerCalls[ticket] = struct{}{}
	}
	a.Mu.Unlock()
	reply := make(chan error, 1)
	err = a.SendDetachedRequest("turn/steer", raw, func(_ json.RawMessage, err error) { reply <- err })
	a.turnDispatchMu.Unlock()
	if err == nil {
		err = <-reply
	}
	if err == nil {
		return ticket, nil
	}
	if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
		return ticket, agent.ErrNoActiveTurn
	}
	return ticket, providerkit.ClassifyJSONRPCDeliveryError("turn/steer", err)
}

// forgetCodexSteer releases later echoes after a failed call. Call it with outputMu held.
func (a *Agent) forgetCodexSteer(ticket providerkit.SteerTicket) {
	a.steerLedger.Forget(ticket)
	a.steerLedger.Settle(a.sink)
}

// finishCodexSteersAfterExit forgets requests whose delivery the process never confirmed.
// The service marks unresolved queue inputs uncertain. Call this with outputMu held.
func (a *Agent) finishCodexSteersAfterExit() {
	a.Mu.Lock()
	for ticket := range a.pendingSteerCalls {
		a.steerLedger.Forget(ticket)
	}
	clear(a.pendingSteerCalls)
	a.mainTurn.steers = 0
	a.Mu.Unlock()
	a.steerLedger.Settle(a.sink)
	a.finishDeferredCodexSteerOutput()
	a.steerLedger.Reset()
}

// interruptCancelledCodexStart stops only the acknowledged turn of this request.
func (a *Agent) interruptCancelledCodexStart(threadID, turnID string) error {
	a.turnDispatchMu.Lock()
	defer a.turnDispatchMu.Unlock()
	a.Mu.Lock()
	current := a.threadID == threadID && a.mainTurn.id == turnID
	a.Mu.Unlock()
	if !current {
		return nil
	}
	if _, err := a.interruptMainTurn(threadID, turnID); err != nil {
		slog.Warn("Cannot stop the cancelled Codex turn", "thread", threadID, "turn", turnID, "error", err)
		return err
	}
	return nil
}

// retireCodexInputAfterExit releases input state after the process ends.
// The caller holds outputMu. Worker lifecycle events publish the stopped state.
func (a *Agent) retireCodexInputAfterExit() {
	err := classifyCodexTurnStartRequestError("turn/start", a.ProcessExitError())
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.retry != nil {
		a.retry.cancelled = true
		a.retry.input = nil
		a.retry = nil
	}
	if pending := a.pendingStart; pending != nil {
		pending.cancelled = true
		a.finishCodexTurnStartLocked(pending, "", err)
	}
	a.mainTurn.finish()
}

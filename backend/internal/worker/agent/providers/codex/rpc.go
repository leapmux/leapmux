package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	reply            *codexTrackedReply
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
	if a.authenticationRecoveryRequired {
		return errCodexInputRecoveryRequired()
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

// errCodexInputRecoveryRequired is the one wording of the recovery refusal, so
// every admission site states the same fact.
func errCodexInputRecoveryRequired() error {
	return fmt.Errorf("%w: the Codex login that this process holds cannot accept further input",
		agent.ErrInputProcessRestartRequired)
}

// dispatchInputForSession captures settings and sends the request under one dispatch lock.
// The caller waits for acceptance after this method releases that lock.
func (a *Agent) dispatchInputForSession(expected *string, input []map[string]interface{}, tracked bool) (*codexPendingTurnStart, error) {
	a.turnDispatchMu.Lock()
	defer a.turnDispatchMu.Unlock()
	a.Mu.Lock()
	if err := a.checkCodexInputLocked(expected); err != nil {
		a.Mu.Unlock()
		return nil, err
	}
	threadID, settings := a.threadID, a.turnSettingsLocked()
	a.Mu.Unlock()
	return a.beginCodexTurnStartWithTracking(threadID, input, settings, nil, tracked)
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
	return a.beginCodexTurnStartWithTracking(threadID, input, settings, retry, false)
}

func (a *Agent) beginCodexTurnStartWithTracking(threadID string, input []map[string]interface{}, settings turnSettings, retry *codexTurnRetry, tracked bool) (*codexPendingTurnStart, error) {
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
	if a.authenticationRecoveryRequired {
		a.Mu.Unlock()
		return nil, errCodexInputRecoveryRequired()
	}
	if a.pendingStart != nil || a.retry != nil && a.retry != retry {
		a.Mu.Unlock()
		return nil, agent.ErrAgentBusy
	}
	if err := a.validateCodexAudioInputLocked(input, settings.model); err != nil {
		a.Mu.Unlock()
		return nil, err
	}
	pending := newCodexTurnStart(threadID, input, settings)
	if tracked {
		reply := newCodexTrackedReply(agent.NativeInputReceipt{SessionID: threadID, InputID: id.Generate()})
		pending.reply = reply
		if a.replyStarts == nil {
			a.replyStarts = make(map[string]*codexTrackedReply)
		}
		a.replyStarts[reply.receipt.InputID] = reply
	}
	pending.settingRevisions = maps.Clone(a.settingRevisions)
	pending.retry = retry
	a.pendingStart = pending
	if retry != nil {
		retry.start = pending
		retry.input = nil
	}
	a.Mu.Unlock()
	var replyTimeout time.Duration
	if tracked {
		replyTimeout = turnStartAckTimeout
	}
	err = a.SendDetachedRequest("turn/start", raw, providerkit.DetachedRequestOptions{Timeout: replyTimeout}, func(result json.RawMessage, responseErr error) {
		a.handleCodexTurnStartResult(pending, result, responseErr)
	})
	if err != nil {
		a.handleCodexTurnStartResult(pending, nil, err)
		return pending, classifyCodexTurnStartRequestError("turn/start", err)
	}
	return pending, nil
}

// finishCodexTurnStartLocked settles only this request and reports whether it did.
// The caller holds Mu and closes pending.done after its event handling completes.
// A response from a retired request must not remove a newer request's state.
func (a *Agent) finishCodexTurnStartLocked(pending *codexPendingTurnStart, turnID string, err error) bool {
	if pending.settled {
		return false
	}
	pending.settled, pending.turnID, pending.err = true, turnID, err
	pending.input = nil
	if a.pendingStart == pending {
		a.pendingStart = nil
	}
	if pending.retry != nil && a.retry == pending.retry {
		a.retry = nil
	}
	return true
}

// handleCodexTurnStartResult reconciles turn-start admission and tracked reply
// ownership from one turn/start RPC result under a single Agent.Mu transition.
func (a *Agent) handleCodexTurnStartResult(pending *codexPendingTurnStart, raw json.RawMessage, responseErr error) {
	processEnded := false
	select {
	case <-a.ProcessDone():
		processEnded = true
	default:
	}
	if responseErr == nil && pending.reply != nil && codexReplyTurnID(raw) == "" && processEnded {
		responseErr = a.ProcessExitError()
	}
	if responseErr != nil {
		err := classifyCodexTurnStartRequestError("turn/start", responseErr)
		// A retired request cannot settle its successor, but its authentication
		// rejection still describes this physical process's cached login.
		if codexAuthenticationRejection(responseErr) {
			a.markAuthenticationRecoveryRequired()
			err = fmt.Errorf("%w: %w", agent.ErrInputProcessRestartRequired, err)
		}
		var (
			failRetry     bool
			publishActive bool
			retireReply   *codexTrackedReply
			deliveryErr   error
		)
		a.Mu.Lock()
		reply := pending.reply
		if reply != nil && a.replyStarts[reply.receipt.InputID] == reply && !reply.settled && reply.turnID == "" {
			delete(a.replyStarts, reply.receipt.InputID)
			reply.endedBeforeAck = nil
			reply.detached = true
			deliveryErr = pending.err
			if deliveryErr == nil {
				deliveryErr = err
			}
			retireReply = reply
		}
		if !pending.settled && a.pendingStart == pending {
			keepPending := errors.Is(err, agent.ErrDeliveryUncertain) && !processEnded &&
				(reply == nil || a.mainTurn.id == "")
			if keepPending {
				if reply == nil {
					pending.responseError <- err
				}
			} else {
				if a.finishCodexTurnStartLocked(pending, "", err) {
					close(pending.done)
				}
				if pending.retry != nil {
					failRetry = true
				} else {
					publishActive = true
				}
			}
		}
		a.Mu.Unlock()
		if retireReply != nil {
			outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
			if deliveryErr != nil && !errors.Is(deliveryErr, agent.ErrDeliveryUncertain) {
				outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
			}
			a.retireCodexNativeReply(retireReply.receipt, outcome)
		}
		if failRetry {
			pending.retry.fail(codexTurnRetryStarting, err)
		} else if publishActive {
			a.PublishTurnActive()
		}
		if retireReply != nil {
			a.finishCodexReply(retireReply, deliveryErr)
		}
		return
	}

	reply := pending.reply
	if reply == nil {
		return
	}
	turnID := codexReplyTurnID(raw)
	a.Mu.Lock()
	if a.replyStarts[reply.receipt.InputID] != reply || reply.settled || reply.turnID != "" {
		a.Mu.Unlock()
		return
	}
	if turnID == "" {
		delete(a.replyStarts, reply.receipt.InputID)
		reply.endedBeforeAck = nil
		reply.detached = true
		var attaching, settings, settled bool
		var attachedTurnID string
		if a.pendingStart == pending && a.mainTurn.id != "" {
			attaching = true
			attachedTurnID = a.mainTurn.id
			settings, settled = a.attachCodexTurnStartLocked(pending, attachedTurnID)
		}
		a.Mu.Unlock()
		a.retireCodexNativeReply(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		a.finishCodexReply(reply, nil)
		if attaching {
			a.publishCodexTurnStart(pending, reply.receipt.SessionID, attachedTurnID, settings, settled)
			a.PublishTurnActive()
		}
		return
	}
	reply.turnID = turnID
	retained, ended := reply.endedBeforeAck[turnID]
	if ended {
		delete(a.replyStarts, reply.receipt.InputID)
	}
	reply.endedBeforeAck = nil
	var settings, settled bool
	attaching := !ended && a.pendingStart == pending && a.mainTurn.id == turnID
	if attaching {
		settings, settled = a.attachCodexTurnStartLocked(pending, turnID)
	} else if ended && a.pendingStart == pending {
		settled = a.finishCodexTurnStartLocked(pending, turnID, nil)
	}
	a.Mu.Unlock()

	err := a.sink.BindNativeInputTurn(reply.receipt, turnID)
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Codex native input binding", "error", err)
	}
	if !agent.OutputRetained(err) || ended && !retained {
		a.retireCodexNativeReply(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	if attaching {
		a.publishCodexTurnStart(pending, reply.receipt.SessionID, turnID, settings, settled)
	} else if settled {
		close(pending.done)
	}
	a.PublishTurnActive()
	a.finishCodexReply(reply, nil)
}

// awaitCodexTurnStart limits the caller's wait, not the request's lifetime.
func (a *Agent) awaitCodexTurnStart(pending *codexPendingTurnStart) error {
	if pending.reply != nil {
		<-pending.reply.done
		return pending.reply.err
	}
	timer := a.Clock().NewTimer(turnStartAckTimeout, codexTurnStartAckTimerTag)
	defer timer.Stop(codexTurnStartAckTimerTag)
	var err error
	select {
	case <-pending.done:
	case err = <-pending.responseError:
	case <-a.ProcessDone():
		err = classifyCodexTurnStartRequestError("turn/start", a.ProcessExitError())
		a.Mu.Lock()
		if a.finishCodexTurnStartLocked(pending, "", err) {
			close(pending.done)
		}
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
// sessionValidated marks a steer whose session the Worker already validated --
// a recorded question's answer. It joins no echo ledger and stays deliverable
// during input recovery, so the turn that asked the question can finish.
func (a *Agent) sendTurnSteer(threadID, turnID string, input []map[string]interface{}, request agent.SteerRequest, sessionValidated bool) (providerkit.SteerTicket, error) {
	clientID := request.AttemptID
	if clientID == "" {
		clientID = id.Short()
	}
	clientID = codexSteerClientIDPrefix + clientID
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
	if a.authenticationRecoveryRequired && !sessionValidated {
		a.Mu.Unlock()
		a.turnDispatchMu.Unlock()
		return 0, errCodexInputRecoveryRequired()
	}
	if err := a.validateCodexAudioInputLocked(input, a.mainTurn.model); err != nil {
		a.Mu.Unlock()
		a.turnDispatchMu.Unlock()
		return 0, err
	}
	// The queue already owns the row for a control reply. It supplies an attempt
	// ID only for a dispatch whose transcript row must wait for the native echo.
	var ticket providerkit.SteerTicket
	if !sessionValidated && request.AttemptID != "" {
		ticket = a.steerLedger.Add(agent.SteerResolution{
			InputID: request.InputID, AttemptID: request.AttemptID,
			Receipt: agent.NativeInputReceipt{SessionID: threadID, TurnID: turnID, InputID: clientID},
		})
		if a.pendingSteerCalls == nil {
			a.pendingSteerCalls = make(map[providerkit.SteerTicket]struct{})
		}
		a.pendingSteerCalls[ticket] = struct{}{}
	}
	a.Mu.Unlock()
	reply := make(chan error, 1)
	err = a.SendDetachedRequest("turn/steer", raw, providerkit.DetachedRequestOptions{}, func(_ json.RawMessage, err error) { reply <- err })
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
		if a.finishCodexTurnStartLocked(pending, "", err) {
			close(pending.done)
		}
	}
	a.mainTurn.finish()
}

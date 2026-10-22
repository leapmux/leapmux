package codex

// This file owns context replacement and retry through native history revert.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexMethodThreadUnsubscribe is a Codex method that no contract table lists,
// because only Go reads it.
const codexMethodThreadUnsubscribe = "thread/unsubscribe"

// codexTurnRetryEffort is the effort that the retry sends. A faster model with
// a lower effort gives the fastest answer to a request that buffers.
const codexTurnRetryEffort = "low"

// codexTurnRetryEndTimerTag tags the timer that limits the wait for the
// stopped turn to end. A test traps it on a mock clock.
const codexTurnRetryEndTimerTag = "codex-turn-retry-end"

const codexTurnRevertTimerTag = "codex-turn-revert"

// errCodexTurnRetryCancelled ends a retry that a user stop or a context clear
// cancelled. It is never a transcript notice: the user asked for the stop.
var errCodexTurnRetryCancelled = errors.New("a user stop or a context clear cancelled the turn retry")

// errCodexTurnRetryTurnEnded ends a retry whose turn finished by itself before
// the retry could stop it. It is never a transcript notice: the finished
// answer is in the transcript, and the turn end already withdrew the offer.
var errCodexTurnRetryTurnEnded = errors.New("the turn ended before the retry could stop it")

// ClearContext sends thread/start on the running Codex process.
// It replaces the current thread with a fresh one.
func (a *Agent) ClearContext() (string, error) {
	a.turnDispatchMu.Lock()
	defer a.turnDispatchMu.Unlock()
	a.Mu.Lock()
	permissions := a.permissionSettingsLocked()
	serviceTier := a.serviceTier
	model := a.model
	workingDir := a.workingDir
	a.Mu.Unlock()

	threadParams := codexThreadParams(model, workingDir, permissions, serviceTier)
	// Codex uses this source to distinguish a deliberate context clear from an
	// unrelated new conversation. Both sources start with empty model history.
	threadParams["sessionStartSource"] = "clear"

	thread, err := a.startThread(threadParams, a.APITimeout())
	if err != nil {
		return "", err
	}
	if thread.ID == "" {
		return "", fmt.Errorf("the new Codex thread has no ID")
	}
	// The clear replaces the retry target. Its late reply must not start input in the new context.
	a.cancelTurnRetry()
	a.withdrawTurnRetryOffer()
	oldThreadID, err := a.replaceMainThread(thread)
	if err != nil {
		return "", err
	}
	go a.unsubscribeCodexThread(oldThreadID)
	return thread.ID, nil
}

// replaceMainThread installs a fresh context and retires the old thread tree.
// A retry uses native revert and preserves the current thread tree instead.
func (a *Agent) replaceMainThread(thread codexThreadResult) (string, error) {
	a.outputMu.Lock()
	a.Mu.Lock()
	sameThread := thread.ID == a.threadID
	a.Mu.Unlock()
	if sameThread {
		a.outputMu.Unlock()
		return "", fmt.Errorf("the new Codex thread reused the current thread ID")
	}
	a.steerLedger.DropAll()
	a.steerLedger.Settle(a.sink)
	a.Mu.Lock()
	a.mainTurn.steers = 0
	clear(a.pendingSteerCalls)
	a.Mu.Unlock()
	a.finishDeferredCodexSteerOutput()
	a.retryResolution.Reset()
	a.Mu.Lock()
	compactingThreads := make([]string, 0, len(a.openCompactions))
	for threadID := range a.openCompactions {
		compactingThreads = append(compactingThreads, threadID)
	}
	a.Mu.Unlock()
	slices.Sort(compactingThreads)
	for _, threadID := range compactingThreads {
		a.persistOpenCodexCompactionEnd(threadID, agent.MessageCompletionInterrupted)
	}
	a.flushAllCodexGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteCodexTools("", true, agent.MessageCompletionInterrupted)
	a.finishAllCodexRawExecutions(agent.MessageCompletionInterrupted)

	a.Mu.Lock()
	if a.retry != nil {
		a.retry.cancelled = true
		a.retry.input = nil
		if a.retry.invalidated != nil {
			close(a.retry.invalidated)
		}
		a.retry = nil
	}
	if pending := a.pendingStart; pending != nil {
		pending.cancelled = true
		err := agent.ErrInputSessionChanged
		if pending.retry != nil {
			pending.retry.cancelled = true
			err = errCodexTurnRetryCancelled
		}
		if a.finishCodexTurnStartLocked(pending, "", err) {
			close(pending.done)
		}
	}
	oldThreadID := a.threadID
	if a.retiredCodexThreads == nil {
		a.retiredCodexThreads = make(map[string]struct{})
	}
	if oldThreadID != "" {
		a.retiredCodexThreads[oldThreadID] = struct{}{}
	}
	for childThreadID, state := range a.collabChildren {
		state.retryResolution.Reset()
		a.retiredCodexThreads[childThreadID] = struct{}{}
	}
	a.applyThreadResult(thread)
	// The response confirms the settings of the new thread.
	a.permissionsAcknowledged = true
	a.threadID = thread.ID
	a.mainTurn.finish()
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.turnAssistantText = ""
	clear(a.reasoningStreamKind)
	clear(a.reasoningRetainedKind)
	clear(a.reasoningSummaryIndex)
	clear(a.reasoningSummarySeen)
	clear(a.reasoningSummaryBreak)
	// Clear the child routes: a new root thread owns a different child tree. A
	// completed run keeps its route only while its root thread lives.
	clear(a.collabChildren)
	clear(a.collabChildItems)
	clear(a.codexSpawnPrompts)
	a.rawExecutionOrder = 0
	clear(a.incompleteTools)
	a.incompleteToolOrder = 0
	a.Mu.Unlock()
	a.generationBuffer.Reset()
	// New-thread output must observe the new transcript identity.
	a.sink.UpdateSessionID(thread.ID)
	a.outputMu.Unlock()

	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.ClearGoal(false)
	return oldThreadID, nil
}

// unsubscribeCodexThread tells Codex to stop sending the frames of one retired
// thread to this client. When no client subscribes, Codex also unloads the
// thread, which frees its memory. The retired thread's frames reach no
// transcript either way, because isRetiredCodexOutput drops them. A failure
// goes to the log and never fails the operation that replaced the thread.
func (a *Agent) unsubscribeCodexThread(threadID string) {
	if threadID == "" {
		return
	}
	params, err := json.Marshal(map[string]string{"threadId": threadID})
	if err != nil {
		return
	}
	if _, err := a.SendRequest(codexMethodThreadUnsubscribe, params, a.APITimeout()); err != nil {
		slog.Warn("codex thread/unsubscribe failed", "agent_id", a.AgentID(), "thread", threadID, "error", err)
	}
}

// Agent retries a turn on a faster model. The assertion makes a drift from the
// neutral seam a compile error.
var _ agent.TurnRetrier = (*Agent)(nil)

// codexTurnRetry is one turn retry that BeginTurnRetry accepted. Its fields
// hold the state of the stopped turn at the acceptance. Manager.RetryTurn runs
// its phases in order: Replace, then Start, or Abandon in place of both.
type codexTurnRetry struct {
	a *Agent
	// turnID is the turn that the retry stops. threadID is its thread.
	turnID   string
	threadID string
	// model is the faster model that the new turn runs on.
	model string
	// input is the retained input of the stopped turn: the first input, then
	// each steer after its separator.
	input []map[string]interface{}
	// ended closes when the stopped turn's turn/completed arrives.
	ended <-chan struct{}
	// Agent.Mu protects cancellation and the pending start.
	cancelled bool
	start     *codexPendingTurnStart
	// noticeMu orders an uncertain-delivery notice before a later final failure.
	noticeMu sync.Mutex

	// Replace writes one request. Start retains its reply registration across the deadline.
	revertReply chan error
	// A context replacement closes invalidated. A normal stop waits for the mutation to settle.
	invalidated chan struct{}
}

var _ agent.TurnRetry = (*codexTurnRetry)(nil)

// codexTurnRetryStage identifies the phase in which a retry failed. Each stage
// states a different consequence to the user.
type codexTurnRetryStage uint8

const (
	// codexTurnRetryStopping: the turn/interrupt failed, so the stopped attempt
	// continues.
	codexTurnRetryStopping codexTurnRetryStage = iota
	// codexTurnRetryPreparing: interruption lacks a completed turn.
	codexTurnRetryPreparing
	// codexTurnRetryReverting: the history change has no confirmed successful outcome.
	codexTurnRetryReverting
	// codexTurnRetryStarting: history reverted, but the new turn did not confirm its start.
	codexTurnRetryStarting
)

func (s codexTurnRetryStage) String() string {
	switch s {
	case codexTurnRetryStopping:
		return "stopping"
	case codexTurnRetryPreparing:
		return "preparing"
	case codexTurnRetryReverting:
		return "reverting"
	case codexTurnRetryStarting:
		return "starting"
	default:
		return fmt.Sprintf("codexTurnRetryStage(%d)", uint8(s))
	}
}

// BeginTurnRetry validates the offer and records the active retry. It
// implements agent.TurnRetrier. It sends no request: the returned retry does.
//
// One critical section validates the offer and records the retry.
// It withdraws the offer before a buffering notice can publish another offer.
func (a *Agent) BeginTurnRetry(turnID, model string) (agent.TurnRetry, error) {
	a.retryOfferMu.Lock()
	a.Mu.Lock()
	offer := a.retryOffer
	ended := a.mainTurn.ended
	if !a.turnRetrySupported || a.StoppedLocked() || a.retry != nil || a.pendingStart != nil || a.pendingSteerTurnEnd != nil || offer == nil || offer.turnID != turnID || offer.model != model ||
		a.mainTurn.id != turnID || !a.turnInputRetainedLocked(turnID) || ended == nil {
		a.Mu.Unlock()
		a.retryOfferMu.Unlock()
		return nil, agent.ErrTurnRetryUnavailable
	}
	if a.mainTurn.steers != 0 {
		a.Mu.Unlock()
		a.retryOfferMu.Unlock()
		return nil, fmt.Errorf("%w: a steering request still awaits a response", agent.ErrAgentBusy)
	}
	retry := &codexTurnRetry{
		a:           a,
		turnID:      turnID,
		threadID:    a.threadID,
		model:       model,
		input:       slices.Clone(a.mainTurn.input),
		ended:       ended,
		invalidated: make(chan struct{}),
	}
	a.retry = retry
	a.retryOffer = nil
	a.Mu.Unlock()
	a.broadcastTurnRetryOffer(nil)
	a.retryOfferMu.Unlock()

	// Keep the queue paused until the history change and replacement input settle.
	a.PublishTurnActive()
	return retry, nil
}

// Replace stops the turn and waits for its end.
// It submits the history revert under the lifecycle lock. Start waits after that lock releases.
func (r *codexTurnRetry) Replace() error {
	a := r.a
	if r.cancelledForRetry() {
		r.abandon()
		return errCodexTurnRetryCancelled
	}
	if r.stoppedTurnEnded() {
		r.abandon()
		return errCodexTurnRetryTurnEnded
	}
	accepted, err := a.interruptMainTurn(r.threadID, r.turnID)
	if !accepted {
		// Codex refuses to interrupt a turn that already ended, and it writes
		// that turn's turn/completed before the refusal. So a closed channel here
		// means that the attempt finished by itself, and its answer stands.
		if r.stoppedTurnEnded() {
			r.abandon()
			return fmt.Errorf("%w: %w", errCodexTurnRetryTurnEnded, err)
		}
		r.fail(codexTurnRetryStopping, err)
		return err
	}
	if err != nil {
		// Codex accepted the interrupt, so the attempt stops and the retry
		// continues. A command process that the attempt started did not stop,
		// and the transcript states it, because no stop reply reaches the user.
		a.reportUnstoppedTurnCommands(err)
	}
	if err := r.awaitStoppedTurn(); err != nil {
		r.fail(codexTurnRetryPreparing, err)
		return err
	}
	a.turnDispatchMu.Lock()
	defer a.turnDispatchMu.Unlock()
	if r.cancelledForRetry() {
		r.abandon()
		return errCodexTurnRetryCancelled
	}
	a.Mu.Lock()
	current := a.threadID == r.threadID && !a.StoppedLocked()
	a.Mu.Unlock()
	if !current {
		r.abandon()
		return errCodexTurnRetryCancelled
	}
	params, err := json.Marshal(map[string]string{"threadId": r.threadID, "beforeTurnId": r.turnID})
	if err != nil {
		r.abandon()
		return err
	}
	r.revertReply = make(chan error, 1)
	err = a.SendDetachedRequest("thread/revert", params, func(raw json.RawMessage, err error) {
		if err == nil {
			var result struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil {
				err = fmt.Errorf("decode thread/revert: %w", decodeErr)
			} else if result.Thread.ID != r.threadID {
				err = fmt.Errorf("thread/revert returned thread %q instead of %q", result.Thread.ID, r.threadID)
			}
		}
		r.revertReply <- err
	})
	if err != nil {
		r.revertReply <- providerkit.ClassifyJSONRPCDeliveryError("thread/revert", err)
	}
	return nil
}

// awaitRevert keeps one request registered after its deadline. Never resend a history mutation.
func (r *codexTurnRetry) awaitRevert() error {
	if r.revertReply == nil {
		return fmt.Errorf("the retry has no history revert request")
	}
	a := r.a
	timeout := a.APITimeout()
	timer := a.Clock().NewTimer(timeout, codexTurnRevertTimerTag)
	defer timer.Stop(codexTurnRevertTimerTag)
	deadline := timer.C
	for {
		select {
		case err := <-r.revertReply:
			if err != nil {
				r.fail(codexTurnRetryReverting, err)
			}
			return err
		case <-deadline:
			r.fail(codexTurnRetryReverting, fmt.Errorf("%w: thread/revert has no reply after %s", agent.ErrDeliveryUncertain, timeout))
			deadline = nil
		case <-r.invalidated:
			return errCodexTurnRetryCancelled
		case <-a.ProcessDone():
			r.abandon()
			return a.ProcessExitError()
		}
	}
}

// stoppedTurnEnded reports whether the stopped turn's turn/completed arrived.
func (r *codexTurnRetry) stoppedTurnEnded() bool {
	select {
	case <-r.ended:
		return true
	default:
		return false
	}
}

// awaitStoppedTurn waits for the stopped turn's turn/completed. The order of
// the interrupt reply and the notification does not matter, because
// handleTurnCompleted closes the channel either way.
func (r *codexTurnRetry) awaitStoppedTurn() error {
	a := r.a
	timeout := a.APITimeout()
	timer := a.Clock().NewTimer(timeout, codexTurnRetryEndTimerTag)
	defer timer.Stop(codexTurnRetryEndTimerTag)
	select {
	case <-r.ended:
		return nil
	case <-a.ProcessDone():
		return a.ProcessExitError()
	case <-timer.C:
		return fmt.Errorf("the stopped turn did not end within %s", timeout)
	}
}

// Start confirms the history revert, then resends input on the same native thread.
// The manager releases its lifecycle lock before this wait, so a context clear remains available.
func (r *codexTurnRetry) Start() error {
	a := r.a
	if err := r.awaitRevert(); err != nil {
		return err
	}
	a.outputMu.Lock()
	a.Mu.Lock()
	current := !r.cancelled && a.retry == r && a.threadID == r.threadID
	a.Mu.Unlock()
	if !current {
		a.outputMu.Unlock()
		r.abandon()
		return errCodexTurnRetryCancelled
	}
	notice := a.captureCodexRetryNotice(map[string]string{
		contracts.NotificationFieldType:  contracts.NotificationTypeTurnRetried,
		contracts.NotificationFieldModel: r.model,
	})
	a.outputMu.Unlock()
	a.persistCodexRetryNotice(notice)
	a.turnDispatchMu.Lock()
	a.Mu.Lock()
	settings := a.turnSettingsLocked()
	input := r.input
	settings.model = r.model
	settings.effort = codexTurnRetryEffort
	a.Mu.Unlock()
	pending, err := a.beginCodexTurnStart(r.threadID, input, settings, r)
	a.turnDispatchMu.Unlock()
	if err != nil {
		r.fail(codexTurnRetryStarting, err)
		return err
	}
	a.PublishTurnActive()
	if err := a.awaitCodexTurnStart(pending); err != nil {
		r.fail(codexTurnRetryStarting, err)
		return err
	}
	if pending.cancelDone != nil {
		return <-pending.cancelDone
	}
	return nil
}

// Abandon ends a retry whose provider instance changed before Replace ran. It
// implements agent.TurnRetry.
func (r *codexTurnRetry) Abandon(err error) {
	slog.Info("codex turn retry abandoned", "agent_id", r.a.AgentID(), "turn", r.turnID, "reason", err)
	r.abandon()
}

// cancelTurnRetry makes a running retry stop at its next phase. It does
// nothing when no retry runs.
func (a *Agent) cancelTurnRetry() {
	a.Mu.Lock()
	if a.retry != nil {
		a.retry.cancelled = true
	}
	a.Mu.Unlock()
}

func (r *codexTurnRetry) cancelledForRetry() bool {
	r.a.Mu.Lock()
	defer r.a.Mu.Unlock()
	return r.cancelled || r.a.retry != r
}

// abandon releases only this operation's hold, without a transcript notice.
func (r *codexTurnRetry) abandon() {
	a := r.a
	a.Mu.Lock()
	if a.retry == r {
		a.retry = nil
	}
	r.input = nil
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// fail reports this operation's failure. Unresolved mutations retain the queue hold.
// A revert error can follow a committed history change or a pending runtime shutdown.
func (r *codexTurnRetry) fail(stage codexTurnRetryStage, err error) {
	r.noticeMu.Lock()
	defer r.noticeMu.Unlock()
	a := r.a
	processEnded := false
	select {
	case <-a.ProcessDone():
		processEnded = true
	default:
	}
	a.outputMu.Lock()
	a.Mu.Lock()
	quiet := r.cancelled && stage != codexTurnRetryReverting || a.StoppedLocked() || a.threadID != r.threadID
	if stage == codexTurnRetryStarting && r.start != nil && r.start.settled {
		quiet = quiet || r.start.err == nil || r.start.failureReported
		if r.start.err != nil {
			err = r.start.err
			r.start.failureReported = true
		}
	}
	uncertain := stage == codexTurnRetryStarting && errors.Is(err, agent.ErrDeliveryUncertain) &&
		a.pendingStart != nil && a.pendingStart.retry == r
	held := (uncertain || stage == codexTurnRetryReverting) && !processEnded && !a.StoppedLocked() && a.threadID == r.threadID
	if !held && a.retry == r {
		a.retry = nil
	}
	awaitingRevert := stage == codexTurnRetryReverting && errors.Is(err, agent.ErrDeliveryUncertain)
	if !held || !uncertain && !awaitingRevert {
		r.input = nil
	}
	a.Mu.Unlock()
	if quiet {
		a.outputMu.Unlock()
		a.PublishTurnActive()
		return
	}
	slog.Warn("codex turn retry failed", "agent_id", a.AgentID(), "stage", stage, "error", err)
	notice := a.captureCodexRetryNotice(map[string]string{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: codexTurnRetryFailureText(stage, err),
	})
	a.outputMu.Unlock()
	a.persistCodexRetryNotice(notice)
	a.PublishTurnActive()
}

// captureCodexRetryNotice captures the session while the caller holds outputMu.
func (a *Agent) captureCodexRetryNotice(data map[string]string) agent.MessageContent {
	raw, err := json.Marshal(data)
	if err != nil {
		slog.Error("Cannot encode the Codex retry notice", "agent_id", a.AgentID(), "error", err)
		return agent.MessageContent{}
	}
	return a.sink.CaptureMessage(agent.MessageContent{Original: raw}, agent.SpanInfo{})
}

func (a *Agent) persistCodexRetryNotice(content agent.MessageContent) {
	if len(content.Original) == 0 {
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content); err != nil {
		slog.Error("Cannot persist the Codex retry notice", "agent_id", a.AgentID(), "error", err)
	}
}

// reportUnstoppedTurnCommands states in the transcript that the retry stopped
// the attempt but not every command process of that attempt.
func (a *Agent) reportUnstoppedTurnCommands(err error) {
	slog.Warn("codex turn retry left a command process of the stopped attempt", "agent_id", a.AgentID(), "error", err)
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("The retry stopped the current attempt, but a command that the attempt started can still run: %v.", err),
	})
}

// codexTurnRetryFailureText states one retry failure and what the user can do.
func codexTurnRetryFailureText(stage codexTurnRetryStage, err error) string {
	switch {
	case stage == codexTurnRetryReverting:
		return fmt.Sprintf("Cannot confirm the retry's history change: %v. Input remains paused. Clear context or restart this agent to recover.", err)
	case stage == codexTurnRetryStopping:
		return fmt.Sprintf("Failed to retry with a faster model: %v. The current attempt continues.", err)
	case stage == codexTurnRetryStarting && errors.Is(err, agent.ErrDeliveryUncertain):
		return fmt.Sprintf("Failed to retry with a faster model: %v. The retry can still start. Wait before you send the message again.", err)
	default:
		return fmt.Sprintf("Failed to retry with a faster model: %v. Send the message again to retry.", err)
	}
}

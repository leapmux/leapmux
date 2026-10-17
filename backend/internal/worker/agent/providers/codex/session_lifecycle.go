package codex

// This file owns the session lifecycle operations that replace the main
// thread: a context clear and a turn retry.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

// errCodexTurnRetryCancelled ends a retry that a user stop or a context clear
// cancelled. It is never a transcript notice: the user asked for the stop.
var errCodexTurnRetryCancelled = errors.New("a user stop or a context clear cancelled the turn retry")

// errCodexTurnRetryTurnEnded ends a retry whose turn finished by itself before
// the retry could stop it. It is never a transcript notice: the finished
// answer is in the transcript, and the turn end already withdrew the offer.
var errCodexTurnRetryTurnEnded = errors.New("the turn ended before the retry could stop it")

// ClearContext sends a new thread/start on the running Codex process,
// replacing the current thread with a fresh one.
func (a *Agent) ClearContext() (string, error) {
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
	// The clear replaces the thread that a running retry targets. A fork of that
	// thread would bring back the conversation that the clear removed, so the
	// retry stops.
	a.cancelTurnRetry()
	a.withdrawTurnRetryOffer()
	oldThreadID := a.replaceMainThread(thread, true)
	a.unsubscribeCodexThread(oldThreadID)
	return thread.ID, nil
}

// replaceMainThread retires the running thread and its children, installs
// thread as the main thread, and returns the ID of the retired thread.
// ClearContext and the turn retry both call it.
//
// clearGoal is for a fresh thread only. A goal belongs to a THREAD. Codex sends
// thread/goal/cleared only after a real removal and on a resume, so a fresh
// thread/start reports nothing at all: the stored goal would stay, and the card
// would offer Pause and Clear for a thread that has no goal. A fork keeps the
// goal of its source thread, so the retry passes false.
//
// The caller unsubscribes the retired thread after this returns. The retired
// thread's frames reach no transcript either way, because isRetiredCodexOutput
// drops them.
func (a *Agent) replaceMainThread(thread codexThreadResult, clearGoal bool) string {
	a.outputMu.Lock()
	a.flushAllCodexGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteCodexTools("", true, agent.MessageCompletionInterrupted)

	a.Mu.Lock()
	oldThreadID := a.threadID
	if a.retiredCodexThreads == nil {
		a.retiredCodexThreads = make(map[string]struct{})
	}
	if oldThreadID != "" {
		a.retiredCodexThreads[oldThreadID] = struct{}{}
	}
	for childThreadID := range a.collabChildren {
		a.retiredCodexThreads[childThreadID] = struct{}{}
	}
	a.applyThreadResult(thread)
	// The response states the settings that the new thread holds, so Codex
	// acknowledged them. A fork states them too, because its params carry the
	// complete permission settings.
	a.permissionsAcknowledged = true
	a.threadID = thread.ID
	a.turnID = ""
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
	clear(a.rawExecutionCalls)
	a.rawExecutionOrder = 0
	clear(a.incompleteTools)
	a.incompleteToolOrder = 0
	clear(a.turnInput)
	// nil, not clear(): clear() keeps the length, and a slice of nil blocks
	// would count as the retained input of the next turn.
	a.pendingTurnInput = nil
	clear(a.turnAgentMessageStarted)
	// No turn of the retired thread reports its end to this agent, because
	// isRetiredCodexOutput drops its frames. Close each channel, so that a
	// waiter wakes now and not at its timeout.
	for _, ended := range a.turnEnded {
		close(ended)
	}
	clear(a.turnEnded)
	a.Mu.Unlock()
	a.generationBuffer.Reset()
	a.outputMu.Unlock()

	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())
	if clearGoal {
		a.sink.ClearGoal(false)
	}
	a.sink.UpdateSessionID(thread.ID)
	return oldThreadID
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
	// settings are the agent settings at the acceptance. The fork uses them
	// unchanged, and only the new turn overrides the model and the effort, as
	// the Codex TUI does.
	settings   turnSettings
	workingDir string

	// Replace sets these: the installed fork, and the retired thread that Start
	// unsubscribes.
	forkThreadID string
	oldThreadID  string
}

var _ agent.TurnRetry = (*codexTurnRetry)(nil)

// codexTurnRetryStage identifies the phase in which a retry failed. Each stage
// states a different consequence to the user.
type codexTurnRetryStage uint8

const (
	// codexTurnRetryStopping: the turn/interrupt failed, so the stopped attempt
	// continues.
	codexTurnRetryStopping codexTurnRetryStage = iota
	// codexTurnRetryReplacing: the attempt stopped, but the thread swap failed.
	codexTurnRetryReplacing
	// codexTurnRetryStarting: the fork is installed, but the new turn did not
	// confirm its start.
	codexTurnRetryStarting
)

func (s codexTurnRetryStage) String() string {
	switch s {
	case codexTurnRetryStopping:
		return "stopping"
	case codexTurnRetryReplacing:
		return "replacing"
	case codexTurnRetryStarting:
		return "starting"
	default:
		return fmt.Sprintf("codexTurnRetryStage(%d)", uint8(s))
	}
}

// BeginTurnRetry validates the offer and marks the retry in flight. It
// implements agent.TurnRetrier. It sends no request: the returned retry does.
//
// The offer check, the retry mark and the offer withdrawal run in one critical
// section, so a buffering notice cannot make a new offer between them.
func (a *Agent) BeginTurnRetry(turnID, model string) (agent.TurnRetry, error) {
	a.retryOfferMu.Lock()
	a.Mu.Lock()
	offer := a.retryOffer
	ended := a.turnEnded[turnID]
	if a.StoppedLocked() || a.retrying || offer == nil || offer.turnID != turnID || offer.model != model ||
		a.turnID != turnID || !a.turnInputRetainedLocked(turnID) || ended == nil {
		a.Mu.Unlock()
		a.retryOfferMu.Unlock()
		return nil, agent.ErrTurnRetryUnavailable
	}
	retry := &codexTurnRetry{
		a:          a,
		turnID:     turnID,
		threadID:   a.threadID,
		model:      model,
		input:      slices.Clone(a.turnInput[turnID]),
		ended:      ended,
		settings:   a.turnSettingsLocked(),
		workingDir: a.workingDir,
	}
	a.retrying = true
	a.retryCancelled = false
	a.retryStarting = false
	a.retryOffer = nil
	a.Mu.Unlock()
	a.broadcastTurnRetryOffer(nil)
	a.retryOfferMu.Unlock()

	// The turn state never falls in the gap: the queue must not drain a queued
	// item into a thread that is about to be replaced.
	a.PublishTurnActive()
	return retry, nil
}

// Replace stops the turn, waits for its end, forks its thread without it, and
// installs the fork. It implements agent.TurnRetry.
func (r *codexTurnRetry) Replace() error {
	a := r.a
	if a.turnRetryCancelled() {
		a.abandonTurnRetry()
		return errCodexTurnRetryCancelled
	}
	if r.stoppedTurnEnded() {
		a.abandonTurnRetry()
		return errCodexTurnRetryTurnEnded
	}
	accepted, err := a.interruptMainTurn(r.threadID, r.turnID)
	if !accepted {
		// Codex refuses to interrupt a turn that already ended, and it writes
		// that turn's turn/completed before the refusal. So a closed channel here
		// means that the attempt finished by itself, and its answer stands.
		if r.stoppedTurnEnded() {
			a.abandonTurnRetry()
			return fmt.Errorf("%w: %w", errCodexTurnRetryTurnEnded, err)
		}
		a.failTurnRetry(codexTurnRetryStopping, err)
		return err
	}
	if err != nil {
		// Codex accepted the interrupt, so the attempt stops and the retry
		// continues. A command process that the attempt started did not stop,
		// and the transcript states it, because no stop reply reaches the user.
		a.reportUnstoppedTurnCommands(err)
	}
	if err := r.awaitStoppedTurn(); err != nil {
		a.failTurnRetry(codexTurnRetryReplacing, err)
		return err
	}
	if a.turnRetryCancelled() {
		a.abandonTurnRetry()
		return errCodexTurnRetryCancelled
	}
	fork, err := r.fork()
	if err != nil {
		a.failTurnRetry(codexTurnRetryReplacing, err)
		return err
	}
	if a.turnRetryCancelled() {
		// The user stopped the retry while the fork ran. The source thread keeps
		// the stopped turn, as after a plain stop, and the retry drops the fork.
		// The caller holds the lifecycle lock here, and the unsubscribe waits for
		// its reply, so it runs in its own goroutine.
		a.retireCodexThread(fork.ID)
		go a.unsubscribeCodexThread(fork.ID)
		a.abandonTurnRetry()
		return errCodexTurnRetryCancelled
	}
	r.oldThreadID = a.replaceMainThread(fork, false)
	r.forkThreadID = fork.ID
	return nil
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

// fork sends thread/fork for the stopped turn's thread, before the stopped
// turn, with the settings of the acceptance.
func (r *codexTurnRetry) fork() (codexThreadResult, error) {
	a := r.a
	s := r.settings
	// The fork loses the raw events. Codex attaches the fork's listener with raw
	// events off and reads `experimentalRawEvents` on thread/start only, so the
	// value that codexThreadParams sends has no effect here (app-server
	// thread_processor.rs, thread_fork_inner). A thread/resume has the same
	// limit. After a retry, the main thread therefore sends no
	// rawResponseItem/completed frame: a native exec call stores no raw
	// execution rows, and a Multi-Agent V2 child gets no prompt row.
	params := codexThreadParams(s.model, r.workingDir, s.permissions, s.serviceTier)
	params["threadId"] = r.threadID
	params["beforeTurnId"] = r.turnID
	// The fork keeps the goal. Its automatic continuation waits until the new
	// turn ends, so the retried input runs first.
	params["deferGoalContinuation"] = true
	params["excludeTurns"] = true
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return codexThreadResult{}, fmt.Errorf("marshal thread/fork params: %w", err)
	}
	raw, err := a.SendRequest("thread/fork", paramsJSON, a.APITimeout())
	if err != nil {
		return codexThreadResult{}, err
	}
	return parseCodexThreadResponse("thread/fork", raw)
}

// Start sends the retained input on the fork with the faster model. It
// implements agent.TurnRetry. It unsubscribes the retired thread last, after
// the lifecycle lock released.
func (r *codexTurnRetry) Start() error {
	a := r.a
	defer a.unsubscribeCodexThread(r.oldThreadID)

	a.Mu.Lock()
	if a.retryCancelled {
		a.Mu.Unlock()
		a.abandonTurnRetry()
		return errCodexTurnRetryCancelled
	}
	// Only the retry's own turn/started ends the retry hold. A turn that Codex
	// starts by itself in the gap leaves the hold in place.
	a.retryStarting = true
	a.Mu.Unlock()

	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeTurnRetried,
		contracts.NotificationFieldModel: r.model,
	})
	settings := r.settings
	settings.model = r.model
	settings.effort = codexTurnRetryEffort
	err := a.sendTurnStart(r.forkThreadID, r.input, settings)

	a.Mu.Lock()
	a.retryStarting = false
	cancelled := a.retryCancelled
	threadID, turnID := a.threadID, a.turnID
	if err == nil {
		// Codex keeps the override for the turns after this one, so the agent
		// state and the settings display follow it.
		a.model = r.model
		a.effort = codexTurnRetryEffort
	}
	a.Mu.Unlock()
	if err != nil {
		a.failTurnRetry(codexTurnRetryStarting, err)
		return err
	}
	a.publishSettings()
	if cancelled {
		// A user stop that arrived after the check above found no turn to stop,
		// because the new turn had not started. The stop still wins, so the new
		// turn stops now.
		if _, err := a.interruptMainTurn(threadID, turnID); err != nil {
			return fmt.Errorf("stop the retried turn after a user stop: %w", err)
		}
	}
	return nil
}

// Abandon ends a retry whose provider instance changed before Replace ran. It
// implements agent.TurnRetry.
func (r *codexTurnRetry) Abandon(err error) {
	slog.Info("codex turn retry abandoned", "agent_id", r.a.AgentID(), "turn", r.turnID, "reason", err)
	r.a.abandonTurnRetry()
}

// cancelTurnRetry makes a running retry stop at its next phase. It does
// nothing when no retry runs.
func (a *Agent) cancelTurnRetry() {
	a.Mu.Lock()
	if a.retrying {
		a.retryCancelled = true
	}
	a.Mu.Unlock()
}

func (a *Agent) turnRetryCancelled() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.retryCancelled
}

// abandonTurnRetry ends a retry without a transcript notice, and the input
// queue resumes.
func (a *Agent) abandonTurnRetry() {
	a.Mu.Lock()
	a.retrying = false
	a.retryStarting = false
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// failTurnRetry ends a retry that failed in stage: the input queue resumes,
// and the transcript states the failure and its consequence. A cancelled retry
// and a retry of a stopped agent state nothing, because the user asked for the
// stop.
func (a *Agent) failTurnRetry(stage codexTurnRetryStage, err error) {
	a.Mu.Lock()
	quiet := a.retryCancelled || a.StoppedLocked()
	a.Mu.Unlock()
	a.abandonTurnRetry()
	if quiet {
		return
	}
	slog.Warn("codex turn retry failed", "agent_id", a.AgentID(), "stage", stage, "error", err)
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: codexTurnRetryFailureText(stage, err),
	})
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
	case stage == codexTurnRetryStopping:
		return fmt.Sprintf("Failed to retry with a faster model: %v. The current attempt continues.", err)
	case stage == codexTurnRetryStarting && errors.Is(err, agent.ErrDeliveryUncertain):
		return fmt.Sprintf("Failed to retry with a faster model: %v. The retry can still start. Wait before you send the message again.", err)
	default:
		return fmt.Sprintf("Failed to retry with a faster model: %v. Send the message again to retry.", err)
	}
}

// retireCodexThread makes isRetiredCodexOutput drop the frames of one thread
// that never became the main thread.
func (a *Agent) retireCodexThread(threadID string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.retiredCodexThreads == nil {
		a.retiredCodexThreads = make(map[string]struct{})
	}
	a.retiredCodexThreads[threadID] = struct{}{}
}

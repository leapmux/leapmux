package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// PrepareSteer stores buffered output before the queue reserves the steer's position.
// A native refusal can leave this harmless segment boundary in the transcript.
func (b *Base) PrepareSteer() {
	b.FlushBufferedOutputOffReader(agent.MessageCompletionComplete)
}

// This file holds the turns that LeapMux did not start. An agent can start a
// turn by itself, with no session/prompt: a background subagent that finished
// wakes its parent, a goal runs its next round, and a steer that reached an
// idle session becomes a turn of its own. Such a turn has no session/prompt
// response, so the provider brackets it with its own frames.

// BeginAgentTurn records a turn that the agent started by itself. The turn is
// then active exactly like a prompt that LeapMux sent: the reader sees the
// agent working, SendInput refuses a second turn with ErrAgentBusy, and the
// worker queues the next message behind it.
//
// An agent starts a turn only when the one before it ended. The end of a
// prompt that LeapMux sent reaches the base on its own goroutine, after the
// reader delivered the response, so the frame that starts the agent turn can
// arrive first -- Qwen Code even starts the first round of a goal before it
// answers the prompt that set the goal. The agent turn is then QUEUED: the end
// of the prompt hands the busy state to it, with no idle state between the two.
//
// The output that the agent streams while its turn waits belongs to the agent
// turn, so the prompt's output ends here: the prompt's text is persisted now,
// and only an interrupted or stopped prompt end closes the tool calls that are
// open now.
//
// COMPROMISE: rows of the waiting agent turn that persist before the prompt's
// end (a tool call row, a text segment that a tool call ends) appear above the
// prompt's divider. The divider holds the prompt response, which has not
// arrived. To hold those rows back until then, the base would have to defer the
// reader's updates across Stop, a session swap, and each provider hook that the
// reader runs. The window is the time between the agent's first frame and the
// prompt response, which is short.
//
// It returns false when an agent turn already runs or waits: the output then
// joins that turn. A turn that starts while no turn runs first stores the
// output that no turn owns, as a prompt does (see takeUnownedOutputLocked).
//
// The queue flag and the boundary change in one step under turnMu, so a
// reader of the two (EndAgentTurnWithoutRow) never finds a queued turn with
// no boundary that the prompt's end did not take yet.
func (b *Base) BeginAgentTurn() bool {
	return b.beginAgentTurn("")
}

// BeginAgentTurnWithReply identifies an agent-initiated turn by the native ID
// that its end will carry. The Worker binds each steer when the provider echoes it.
func (b *Base) BeginAgentTurnWithReply(turnID string) bool {
	return b.beginAgentTurn(turnID)
}

func (b *Base) beginAgentTurn(turnID string) bool {
	b.turnMu.Lock()
	b.Mu.Lock()
	switch {
	case b.agentTurnActive || b.agentTurnQueued:
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return false
	case b.promptActive:
		b.agentTurnQueued = true
		sessionID := b.sessionID
		drained := b.promptOutputDrained
		b.Mu.Unlock()
		var turn acpTurnSnapshot
		if !drained {
			turn = b.markPromptBoundaryLocked()
		}
		target := b.openAgentTurnTranscriptLocked(sessionID, turnID, b.promptTarget)
		b.rootTarget = target
		if drained {
			b.promptTarget = target
		}
		b.turnMu.Unlock()
		if err := target.prepare(); err != nil {
			slog.Error("prepare ACP root transcript", "error", err)
		}
		b.endPromptOutput(turn)
		return true
	}
	return b.startIdleAgentTurnLocked(turnID)
}

func (b *Base) startIdleAgentTurnLocked(turnID string) bool {
	b.promptActive = true
	b.promptOutputDrained = false
	b.agentTurnActive = true
	sessionID := b.sessionID
	b.Mu.Unlock()
	turn := b.takeUnownedOutputLocked()
	b.preparePriorTextLocked(&turn)
	target := b.openAgentTurnTranscriptLocked(sessionID, turnID, nil)
	b.rootTarget, b.promptTarget = target, target
	b.turnMu.Unlock()
	b.persistUnownedText(turn)
	if err := target.prepare(); err != nil {
		slog.Error("prepare ACP root transcript", "error", err)
	}
	b.notePromptActive()
	return true
}

func (b *Base) openAgentTurnTranscriptLocked(sessionID, turnID string, preceding *acpTranscriptTarget) *acpTranscriptTarget {
	var receipt agent.NativeInputReceipt
	if turnID != "" {
		receipt = agent.NativeInputReceipt{SessionID: sessionID, TurnID: turnID}
	}
	target := b.openRootTranscriptLocked(sessionID, true, preceding, receipt)
	if turnID != "" {
		target.receiptStarted = true
	}
	return target
}

// AdmitAgentTurn records a turn that the agent ASKS to start, and only when no
// turn runs. It returns false while a prompt of LeapMux's runs, while an agent
// turn runs, and while one waits behind a prompt. The agent then defers its
// turn and asks again later.
//
// It differs from BeginAgentTurn, which serves an agent that STATES a turn that
// it already started. Such a turn cannot be refused, so BeginAgentTurn queues it
// behind a running prompt. An agent that asks first can wait, and the refusal
// keeps the two turns apart: an agent that aborts its running turn for a new
// prompt (Qwen Code) would otherwise start the admitted turn, lose it to the
// worker's next prompt, and leave the base to count that prompt's output as the
// agent turn's. The check and the record share one critical section, so a
// prompt cannot start between them.
func (b *Base) AdmitAgentTurn() bool {
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.promptActive {
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return false
	}
	return b.startIdleAgentTurnLocked("")
}

// endPromptOutput stores the text of the running prompt that the boundary took,
// because an agent turn now waits behind the prompt. See BeginAgentTurn.
func (b *Base) endPromptOutput(turn acpTurnSnapshot) {
	b.main().persistBufferedText(turn, agent.MessageCompletionComplete)
}

// persistUnownedText stores the text that takeUnownedOutputLocked took when a
// turn started. Each kind that holds text is a completed segment of its own,
// stored before the turn writes anything. A kind with no text stores nothing
// and reports nothing, because no live counter opened for it.
//
// The base cannot tell a replay from content, so it keeps the text. A dropped
// segment would lose content that an idle agent sends: Reasonix sends the
// warning of a background job as text, and OpenCode and Kilo forward the
// output of each client of a session. A kept replay is a visible copy. A
// provider can consume it at its hook, as Gemini does, or avoid it with a
// resume that sends no replay.
func (b *Base) persistUnownedText(turn acpTurnSnapshot) {
	b.main().persistBufferedText(turn, agent.MessageCompletionComplete)
}

// AgentTurnActive reports whether the running turn is one that the agent
// started by itself.
func (b *Base) AgentTurnActive() bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.agentTurnActive
}

// EndAgentTurn ends the turn that BeginAgentTurn recorded. frame is the
// agent's own record of that end, and it becomes the turn-end row, as the
// session/prompt response does for a turn that LeapMux started. The provider
// plugin of the browser reads that frame into the divider.
//
// An agent turn that is still queued keeps the frame, and the end of the prompt
// before it persists both ends in order. With no agent turn it does nothing:
// an end frame that the agent sends for a turn that LeapMux started closes
// nothing here, because the session/prompt response of that turn does.
//
// It takes turnMu before b.Mu, and never takes sessionMu. A provider calls it from the reader
// goroutine, and ClearContext holds sessionMu for the whole session/new round
// trip, whose response only that reader can deliver: a read lock here would
// stop the reader until the round trip timed out. The session swap resets
// the agent turn under b.Mu, so a swap that wins the race leaves nothing for
// this to end, and each drain of the turn output takes its own snapshot.
func (b *Base) EndAgentTurn(frame json.RawMessage) {
	b.endAgentTurn(acpAgentTurnEnd{frame: frame})
}

type acpAgentTurnEnd struct {
	frame   json.RawMessage
	turnID  string
	outcome leapmuxv1.AgentInputTurnOutcome
}

// EndAgentTurnWithReply completes only the agent-initiated turn with turnID.
// The provider supplies the outcome from the native end event.
func (b *Base) EndAgentTurnWithReply(turnID string, frame json.RawMessage, outcome leapmuxv1.AgentInputTurnOutcome) {
	b.endAgentTurn(acpAgentTurnEnd{frame: frame, turnID: turnID, outcome: outcome})
}

func (b *Base) endAgentTurn(end acpAgentTurnEnd) {
	b.turnMu.Lock()
	b.Mu.Lock()
	if target := b.rootTarget; end.turnID != "" &&
		(target == nil || !target.agentInitiated || target.inputReceipt.TurnID != end.turnID) {
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return
	}
	if b.agentTurnQueued {
		if len(end.frame) > 0 {
			end.frame = append(json.RawMessage(nil), end.frame...)
		}
		b.queuedAgentTurnEnd = &end
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return
	}
	active := b.agentTurnActive
	sessionID := b.sessionID
	stopped := b.StoppedLocked() || b.interruptRequested || b.isCancelledTurnEnd(end.frame) ||
		end.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	outcome := end.outcome
	if stopped && outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	}
	b.agentTurnActive = false
	b.Mu.Unlock()
	if !active {
		b.turnMu.Unlock()
		return
	}
	turn, _ := b.drainPromptTurnLocked(!stopped)
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, false)
	b.Mu.Unlock()
	b.turnMu.Unlock()
	textCompletion, incomplete := b.turnOutputCompletions(stopped)
	wroteOutcome := false
	b.finishRootSnapshot(turn, textCompletion, incomplete, RootTranscriptPromptEnded, func(writer agent.TranscriptServices, count int) {
		content := agent.MessageContent{Original: end.frame}
		if end.turnID != "" && outcome != 0 {
			content = turn.endingAdmission.tagReply(content)
			content.InputTurnOutcome = outcome
			wroteOutcome = true
		}
		b.persistPromptResponseTo(writer, content, count, sessionID, stopped)
	})
	if stopped {
		b.releaseAwaitedTerminals()
	}
	if !wroteOutcome {
		b.retireSharedInputs(turn.endingAdmission, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	b.endTurn(sessionID)
}

// EndAgentTurnWithoutRow ends the turn that BeginAgentTurn recorded, for work
// of the agent's own that is no conversation turn and keeps no record of its
// end: Kiro compacts its context through a request, and the request's
// response is the only end. The turn state goes idle, so the worker's queue
// releases the next message, but no turn-end row joins the transcript. The
// text and the tool calls that the work left open are stored as a turn end
// stores them.
//
// An agent turn that is still queued behind a prompt of LeapMux's is dropped:
// the work ended before its turn could begin. Its turn set a boundary on the
// prompt's output (see BeginAgentTurn). Store the work's text as a separate
// segment before restoring the prompt's output target. The prompt's end still
// closes the remaining tools and keeps their count. When
// the prompt's end already took its part of the output, the rest is the
// work's, and this stores it. With no agent turn it does nothing.
func (b *Base) EndAgentTurnWithoutRow() {
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.agentTurnQueued {
		b.agentTurnQueued = false
		b.queuedAgentTurnEnd = nil
		sessionID := b.sessionID
		stopped := b.StoppedLocked() || b.interruptRequested
		b.Mu.Unlock()
		if b.dropPromptBoundaryLocked() {
			text := b.takeBufferedTextLocked()
			text.sessionID = sessionID
			if b.IsDiscardingOutput() {
				b.turnMu.Unlock()
				text.releaseTranscriptAdmissions()
				return
			}
			text.textProgressCompleted = true
			b.reserveBufferedTextLocked(&text)
			b.turnMu.Unlock()
			b.completeBufferedTextProgress(text)
			textCompletion, _ := b.turnOutputCompletions(stopped)
			b.main().persistBufferedText(text, textCompletion)
			return
		}
		turn := b.drainTurnLocked(!stopped)
		b.Mu.Lock()
		b.captureRootSnapshotLocked(&turn, false)
		b.Mu.Unlock()
		b.turnMu.Unlock()
		b.storeWorkOutput(turn, sessionID, stopped)
		return
	}
	active := b.agentTurnActive
	sessionID := b.sessionID
	stopped := b.StoppedLocked() || b.interruptRequested
	b.agentTurnActive = false
	b.Mu.Unlock()
	if !active {
		b.turnMu.Unlock()
		return
	}
	turn := b.drainTurnLocked(!stopped)
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, false)
	b.Mu.Unlock()
	b.turnMu.Unlock()
	b.storeWorkOutput(turn, sessionID, stopped)
	b.endTurn(sessionID)
}

// storeWorkOutput stores the output of agent work that ends with no turn-end
// row, keeping open tools and their output progress alive unless stopped.
func (b *Base) storeWorkOutput(turn acpTurnSnapshot, sessionID string, stopped bool) {
	textCompletion, incomplete := b.turnOutputCompletions(stopped)
	b.finishRootSnapshot(turn, textCompletion, incomplete, RootTranscriptWorkEnded, nil)
	if stopped {
		b.releaseAwaitedTerminals()
		b.resetCompletedRootSpans(sessionID, true)
		return
	}
	b.sink.ReportProgress(agent.ResetModelProgress())
}

// takeQueuedAgentTurn hands the busy state of the turn that just ended to the
// agent turn queued behind it, and reports whether one was. The fields of the
// ended turn go, as they go at any turn end, and promptActive stays, so no
// idle state is published between the two turns. end is the queued turn's
// own end record when it arrived before this.
func (b *Base) takeQueuedAgentTurn(sessionID string) (end *acpAgentTurnEnd, queued bool) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if !b.agentTurnQueued || b.sessionID != sessionID {
		return nil, false
	}
	end = b.queuedAgentTurnEnd
	b.agentTurnQueued = false
	b.queuedAgentTurnEnd = nil
	b.agentTurnActive = true
	b.promptOutputDrained = false
	b.steerRunID = ""
	b.interruptRequested = false
	return end, true
}

// endTurn reports the turn of sessionID as over. An agent turn queued behind
// it takes the busy state over instead. Otherwise input that the provider
// accepted for the turn and never read becomes the next prompt at once, and the
// turn state stays active across the two, so no message that the worker queued
// in the meantime can start a turn between them. The caller has persisted the
// turn-end row.
//
// The next prompt goes out on its own goroutine. A provider ends an agent turn
// on the reader goroutine (EndAgentTurn), and the send waits for its stdin
// write. An agent that blocks on its own stdout reads no stdin until that
// reader drains it, so a send on the reader would stop both sides. The
// providerkit frame queue for a reader-side write is no option either: it drops
// a frame when the queue is full, and this frame is the reader's own message.
func (b *Base) endTurn(sessionID string) {
	b.Mu.Lock()
	current := b.sessionID == sessionID
	b.Mu.Unlock()
	if !current {
		return
	}
	if b.handoffQueuedAgentTurn(sessionID) {
		return
	}
	if b.schedulePendingFollowUp(sessionID) {
		return
	}
	stopped := b.acpInterruptRequested() || b.IsStopped()
	if b.hooks.FollowUpPrompt != nil {
		content, attachments, ok := b.hooks.FollowUpPrompt(sessionID, stopped)
		if ok && !stopped {
			batch := newACPFollowUpPrompt(sessionID, content, attachments)
			b.Mu.Lock()
			if b.sessionID != sessionID || b.StoppedLocked() {
				b.Mu.Unlock()
				return
			}
			if b.pendingFollowUp != nil {
				b.Mu.Unlock()
				panic("ACP selected another follow-up batch while retained input exists")
			}
			b.pendingFollowUp = batch
			b.Mu.Unlock()
			b.schedulePendingFollowUp(sessionID)
			return
		}
	}
	if !b.clearActivePromptForSession(sessionID) {
		b.handoffQueuedAgentTurn(sessionID)
	}
}

// sendFollowUpPrompt starts the next prompt that endTurn took from the
// provider. The turn state stays active until the prompt starts, so no queued
// message can overtake it. A prompt that cannot start ends the turn, and the
// reader learns that the message never reached the agent.
func (b *Base) sendFollowUpPrompt(sessionID, content string, attachments []*leapmuxv1.Attachment) {
	err := b.continueWithPrompt(sessionID, content, attachments)
	if err != nil {
		b.reportFollowUpFailure(sessionID, err)
	}
}

func (b *Base) reportFollowUpFailure(sessionID string, err error) {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.Mu.Lock()
	current := b.sessionID == sessionID
	b.Mu.Unlock()
	if !current {
		return
	}
	slog.Error("acp follow-up prompt failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
	b.sink.PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("the message sent during the turn could not start a new turn: %v", err),
	})
	if !b.clearActivePromptForSession(sessionID) {
		b.handoffQueuedAgentTurn(sessionID)
	}
}

// continueWithPrompt starts the next prompt of sessionID with content and its
// attachments, while the turn state stays active. The fields of the turn that
// ended go, as they go at any turn end, because they describe that turn alone.
func (b *Base) continueWithPrompt(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return b.continueWithPromptForBatch(sessionID, content, attachments, nil, nil)
}

func (b *Base) continueWithPromptForBatch(sessionID, content string, attachments []*leapmuxv1.Attachment, batch *acpFollowUpPrompt, releaseOperation func()) error {
	if releaseOperation == nil {
		var operationErr error
		releaseOperation, operationErr = b.rootCallbacks.acquire()
		if operationErr != nil {
			return operationErr
		}
	}
	transferred := false
	defer func() {
		if !transferred {
			releaseOperation()
		}
	}()
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.sessionID != sessionID || b.StoppedLocked() {
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return fmt.Errorf("the session of the turn is no longer active")
	}
	if batch != nil {
		if b.pendingFollowUp != batch {
			b.Mu.Unlock()
			b.turnMu.Unlock()
			return nil
		}
		if b.agentTurnQueued || b.agentTurnActive {
			batch.scheduled = false
			b.Mu.Unlock()
			b.turnMu.Unlock()
			return errNativeTurnPrecedesFollowUp
		}
		b.pendingFollowUp = nil
	}
	b.steerRunID = ""
	b.interruptRequested = false
	b.agentTurnActive = false
	b.promptOutputDrained = false
	b.Mu.Unlock()
	// The turn that ended drained its output, so what the buffer holds now came
	// after that turn and before this prompt.
	turn := b.takeUnownedOutputLocked()
	b.preparePriorTextLocked(&turn)
	target := b.openRootTranscriptLocked(sessionID, false, nil, agent.NativeInputReceipt{})
	b.rootTarget, b.promptTarget = target, target
	b.turnMu.Unlock()
	b.persistUnownedText(turn)
	if err := target.prepare(); err != nil {
		b.finishUnsentPrompt(sessionID)
		return err
	}
	if b.hooks.PromptPrepared != nil {
		b.hooks.PromptPrepared()
	}
	_, err := b.sendOwnedPromptDetachedLocked(sessionID, target, content, attachments, releaseOperation)
	if err != nil {
		b.finishUnsentPrompt(sessionID)
	}
	if err == nil {
		transferred = true
	}
	return err
}

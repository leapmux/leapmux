package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

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
// and the prompt's end closes only the tool calls that are open now.
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
// joins that turn.
//
// The queue flag and the boundary change in one step under turnMu, so a
// reader of the two (EndAgentTurnWithoutRow) never finds a queued turn with
// no boundary that the prompt's end did not take yet.
func (b *Base) BeginAgentTurn() bool {
	b.turnMu.Lock()
	b.Mu.Lock()
	switch {
	case b.agentTurnActive || b.agentTurnQueued:
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return false
	case b.promptActive:
		b.agentTurnQueued = true
		b.Mu.Unlock()
		assistantText, thoughtText := b.markPromptBoundaryLocked()
		b.turnMu.Unlock()
		b.endPromptOutput(assistantText, thoughtText)
		return true
	}
	b.promptActive = true
	b.agentTurnActive = true
	b.Mu.Unlock()
	b.turnMu.Unlock()
	b.notePromptActive()
	return true
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
	b.Mu.Lock()
	if b.promptActive {
		b.Mu.Unlock()
		return false
	}
	b.promptActive = true
	b.agentTurnActive = true
	b.Mu.Unlock()
	b.notePromptActive()
	return true
}

// endPromptOutput stores the text of the running prompt that the boundary took,
// because an agent turn now waits behind the prompt. See BeginAgentTurn.
func (b *Base) endPromptOutput(assistantText, thoughtText string) {
	main := b.main()
	main.persistCompletedText(agent.AssembledMessageKindReasoning, thoughtText)
	main.persistCompletedText(agent.AssembledMessageKindText, assistantText)
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
// It takes b.Mu only, never sessionMu. A provider calls it from the reader
// goroutine, and ClearContext holds sessionMu for the whole session/new round
// trip, whose response only that reader can deliver: a read lock here would
// stop the reader until the round trip timed out. The session swap resets
// the agent turn under b.Mu, so a swap that wins the race leaves nothing for
// this to end, and each drain of the turn output takes its own snapshot.
func (b *Base) EndAgentTurn(frame json.RawMessage) {
	b.Mu.Lock()
	if b.agentTurnQueued {
		b.queuedAgentTurnEnd = append(json.RawMessage(nil), frame...)
		b.Mu.Unlock()
		return
	}
	active := b.agentTurnActive
	sessionID := b.sessionID
	b.agentTurnActive = false
	b.Mu.Unlock()
	if !active {
		return
	}
	b.persistFinishedTurn(frame)
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
// prompt's output (see BeginAgentTurn), and a turn that never runs takes
// nothing after that boundary. So the boundary goes, and the prompt's end
// stores the whole output, with the prompt's own count of finished tools. When
// the prompt's end already took its part of the output, the rest is the
// work's, and this stores it. With no agent turn it does nothing.
func (b *Base) EndAgentTurnWithoutRow() {
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.agentTurnQueued {
		b.agentTurnQueued = false
		b.queuedAgentTurnEnd = nil
		b.Mu.Unlock()
		drained := !b.dropPromptBoundaryLocked()
		var turn acpTurnSnapshot
		if drained {
			turn = b.drainTurnLocked()
		}
		b.turnMu.Unlock()
		if drained {
			b.storeWorkOutput(turn)
		}
		return
	}
	active := b.agentTurnActive
	sessionID := b.sessionID
	b.agentTurnActive = false
	b.Mu.Unlock()
	b.turnMu.Unlock()
	if !active {
		return
	}
	b.storeWorkOutput(b.drainTurn())
	b.endTurn(sessionID)
}

// storeWorkOutput stores the output of agent work that ends with no turn-end
// row, as a turn end stores it: the text, and each tool call that the work left
// open, which failed unless the reader stopped the work.
func (b *Base) storeWorkOutput(turn acpTurnSnapshot) {
	main := b.main()
	main.persistCompletedText(agent.AssembledMessageKindReasoning, turn.thoughtText)
	main.persistCompletedText(agent.AssembledMessageKindText, turn.assistantText)
	incomplete := agent.MessageCompletionError
	if b.acpInterruptRequested() {
		incomplete = agent.MessageCompletionInterrupted
	}
	main.persistIncompleteTools(turn.incompleteTools, incomplete)
	b.clearCompletedTerminals()
	b.sink.ReportProgress(agent.ResetProgress())
}

// takeQueuedAgentTurn hands the busy state of the turn that just ended to the
// agent turn queued behind it, and reports whether one was. The fields of the
// ended turn go, as they go at any turn end, and promptActive stays, so no
// idle state is published between the two turns. end is the queued turn's
// own end frame when it arrived before this.
func (b *Base) takeQueuedAgentTurn(sessionID string) (end json.RawMessage, queued bool) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if !b.agentTurnQueued || b.sessionID != sessionID {
		return nil, false
	}
	end = b.queuedAgentTurnEnd
	b.agentTurnQueued = false
	b.queuedAgentTurnEnd = nil
	b.agentTurnActive = true
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
	if end, queued := b.takeQueuedAgentTurn(sessionID); queued {
		if end != nil {
			b.EndAgentTurn(end)
		}
		return
	}
	stopped := b.acpInterruptRequested() || b.IsStopped()
	if b.hooks.FollowUpPrompt != nil {
		content, attachments, ok := b.hooks.FollowUpPrompt(stopped)
		if ok && !stopped {
			go b.sendFollowUpPrompt(sessionID, content, attachments)
			return
		}
	}
	b.clearActivePrompt()
}

// sendFollowUpPrompt starts the next prompt that endTurn took from the
// provider. The turn state stays active until the prompt starts, so no queued
// message can overtake it. A prompt that cannot start ends the turn, and the
// reader learns that the message never reached the agent.
func (b *Base) sendFollowUpPrompt(sessionID, content string, attachments []*leapmuxv1.Attachment) {
	err := b.continueWithPrompt(sessionID, content, attachments)
	if err == nil {
		return
	}
	slog.Error("acp follow-up prompt failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
	b.sink.PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("the message sent during the turn could not start a new turn: %v", err),
	})
	b.clearActivePrompt()
}

// continueWithPrompt starts the next prompt of sessionID with content and its
// attachments, while the turn state stays active. The fields of the turn that
// ended go, as they go at any turn end, because they describe that turn alone.
func (b *Base) continueWithPrompt(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	b.Mu.Lock()
	if b.sessionID != sessionID || b.StoppedLocked() {
		b.Mu.Unlock()
		return fmt.Errorf("the session of the turn is no longer active")
	}
	b.steerRunID = ""
	b.interruptRequested = false
	b.agentTurnActive = false
	b.Mu.Unlock()
	return b.SendPromptDetached(content, attachments, func(response json.RawMessage, err error) {
		b.finishPromptRequest(sessionID, response, err)
	})
}

package grok

import (
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

const grokInterjectionFallbackPrefix = "interject-fallback-"

// Grok can start a fresh prompt when it receives the interjection after the
// preceding execution ends. The reply scope waits for the echo and the next
// native prompt before it can verify the new prompt's journal input.
type grokSteerHandoff struct {
	sessionID, predecessorID, group string
	baseline                        grokJournalBaseline
	input                           grokSteerAdmission
	done                            chan struct{}
}

func (handoff *grokSteerHandoff) receipt() agent.NativeInputReceipt {
	return agent.NativeInputReceipt{SessionID: handoff.sessionID, InputID: handoff.input.id}
}

// The reply state mutex protects both the handoff and its completion.
func (replies *grokSteerReplies) takeHandoff() *grokSteerHandoff {
	handoff := replies.handoff
	if handoff != nil {
		replies.handoff = nil
		close(handoff.done)
	}
	return handoff
}

// The caller holds the reply state mutex. Only one input whose echo has yet
// to arrive can own the immediate successor.
func (a *Agent) retainGrokSteerHandoffLocked(predecessor *grokSteerReply) {
	pending := -1
	for index, input := range predecessor.inputs {
		if !input.echoed {
			if pending >= 0 {
				return
			}
			pending = index
		}
	}
	if pending < 0 {
		return
	}
	handoff := &grokSteerHandoff{
		sessionID: predecessor.sessionID, predecessorID: predecessor.promptID,
		group: predecessor.group, baseline: predecessor.baseline, input: predecessor.inputs[pending],
		done: make(chan struct{}),
	}
	predecessor.inputs = slices.Delete(predecessor.inputs, pending, pending+1)
	replies := &a.steerReplies
	replies.handoff = handoff
	replies.jobs.Add(1)
	go func() {
		defer replies.jobs.Done()
		deadline := a.Clock().NewTimer(a.APITimeout(), "grok-interjection-handoff")
		defer deadline.Stop()
		select {
		case <-handoff.done:
			return
		case <-deadline.C:
		case <-a.Context().Done():
		}
		replies.mu.Lock()
		if replies.handoff != handoff {
			replies.mu.Unlock()
			return
		}
		replies.takeHandoff()
		replies.mu.Unlock()
		a.retireGrokSteer(handoff.receipt())
	}()
}

func (a *Agent) acceptGrokSteerSuccessor(sessionID, promptID, text string, agentInitiated bool) {
	replies := &a.steerReplies
	replies.mu.Lock()
	handoff := replies.handoff
	if handoff == nil || handoff.sessionID != sessionID || handoff.predecessorID == promptID {
		replies.mu.Unlock()
		return
	}
	replies.takeHandoff()
	if !replies.closed && replies.active == nil && agentInitiated && handoff.input.echoed &&
		handoff.input.payload.text == text && isGrokInterjectionFallback(promptID) {
		replies.active = &grokSteerReply{
			sessionID: sessionID, promptID: promptID, turnID: promptID, group: handoff.group,
			predecessorID: handoff.predecessorID, baseline: handoff.baseline,
			inputs: []grokSteerAdmission{handoff.input},
		}
		replies.mu.Unlock()
		return
	}
	replies.mu.Unlock()
	a.retireGrokSteer(handoff.receipt())
}

func isGrokInterjectionFallback(promptID string) bool {
	suffix, ok := strings.CutPrefix(promptID, grokInterjectionFallbackPrefix)
	if !ok {
		return false
	}
	_, err := uuid.Parse(suffix)
	return err == nil
}

package acp

import (
	"encoding/json"
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Conversation is the identity of one actual prompt or native agent turn.
// Manual client work has no conversation. Callers retain this opaque identity
// before asynchronous observations instead of borrowing a later active turn.
type Conversation struct {
	base         *Base
	sessionID    string
	ending       bool
	cancellation *conversationCancellation
}

type conversationCancellation struct {
	done    chan struct{}
	settled bool
	err     error
}

// The caller holds Base.Mu.
func (conversation *Conversation) internallyInterrupted() bool {
	return conversation != nil && conversation.cancellation != nil
}

func (b *Base) startConversationLocked(queued bool) {
	b.conversation = &Conversation{base: b, sessionID: b.sessionID}
	if !queued {
		b.promptConversation = b.conversation
	}
}

// CurrentConversation captures the actual work whose observations may cancel it.
func (b *Base) CurrentConversation() *Conversation {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if !b.promptActive || b.StoppedLocked() {
		return nil
	}
	return b.conversation
}

// CancelConversation atomically validates ownership and queues cancellation.
// claim runs under the state locks and returns settlement, or nil to refuse.
// Both callbacks may take a provider mutex, but must not call Base, wait, or do
// I/O. Settlement runs before prompt completion can observe the retry verdict.
// The method waits off-reader. A false result means no cancellation was admitted.
func (b *Base) CancelConversation(conversation *Conversation, claim func() func(error)) (bool, error) {
	if conversation == nil || conversation.base != b || claim == nil {
		return false, nil
	}
	for {
		b.turnMu.Lock()
		b.Mu.Lock()
		if b.conversation != conversation || b.sessionID != conversation.sessionID || !b.promptActive || b.StoppedLocked() || conversation.ending || conversation.cancellation != nil {
			b.Mu.Unlock()
			b.turnMu.Unlock()
			return false, nil
		}
		settle := claim()
		if settle == nil {
			b.Mu.Unlock()
			b.turnMu.Unlock()
			return false, nil
		}
		params, err := json.Marshal(map[string]string{"sessionId": conversation.sessionID})
		if err != nil {
			panic(err) // A string-only object always marshals.
		}
		batch, ready, err := b.QueueControlCancellation(conversation.sessionID, MethodSessionCancel, params, b.hooks.CancelBeforeControlWithdrawal, b.hooks.AnswerlessControlsOutliveTurns)
		if batch == nil {
			if err == nil {
				err = errors.New("cancellation waits for control-response admission")
			}
			settle(err)
			b.Mu.Unlock()
			b.turnMu.Unlock()
			if ready == nil {
				return false, err
			}
			select {
			case <-ready:
				continue
			case <-b.Context().Done():
				return false, b.Context().Err()
			}
		}
		operation := &conversationCancellation{done: make(chan struct{})}
		conversation.cancellation = operation
		b.Mu.Unlock()
		b.turnMu.Unlock()
		err = batch.Settle(b.sink)
		b.forgetResolvedURLElicitations()
		b.Mu.Lock()
		settle(err)
		operation.err, operation.settled = err, true
		if conversation.cancellation == operation {
			if err != nil && !errors.Is(err, agent.ErrDeliveryUncertain) {
				conversation.cancellation = nil
			}
		}
		close(operation.done)
		b.Mu.Unlock()
		return true, err
	}
}

// interruptConversation preserves the user's stop while an internal stop
// settles. Another batch is needed only when the earlier batch delivered no bytes.
func (b *Base) interruptConversation(conversation *Conversation) error {
	for {
		b.Mu.Lock()
		if b.conversation != conversation || b.sessionID != conversation.sessionID || !b.promptActive || conversation.ending || b.StoppedLocked() {
			b.Mu.Unlock()
			return nil
		}
		operation := conversation.cancellation
		if operation == nil {
			b.Mu.Unlock()
			admitted, err := b.CancelConversation(conversation, func() func(error) { return func(error) {} })
			if admitted || err != nil {
				return err
			}
			continue
		}
		settled, err, done := operation.settled, operation.err, operation.done
		b.Mu.Unlock()
		if settled {
			return err
		}
		<-done
	}
}

// finishConversationAdmission waits off-reader and closes admission under the
// same locks as cancellation. No new observation can claim this finished turn.
func (b *Base) finishConversationAdmission(sessionID string, target *acpTranscriptTarget, conversation *Conversation) (bool, bool) {
	for {
		b.turnMu.Lock()
		b.Mu.Lock()
		if b.sessionID != sessionID || b.promptConversation != conversation || b.promptTarget != target || b.promptOutputDrained || conversation != nil && conversation.ending {
			b.Mu.Unlock()
			b.turnMu.Unlock()
			return false, false
		}
		if conversation != nil && conversation.cancellation != nil && !conversation.cancellation.settled {
			done := conversation.cancellation.done
			b.Mu.Unlock()
			b.turnMu.Unlock()
			<-done
			continue
		}
		if conversation != nil {
			conversation.ending = true
		}
		stopped := b.StoppedLocked() || b.interruptRequested || conversation.internallyInterrupted()
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return stopped, true
	}
}

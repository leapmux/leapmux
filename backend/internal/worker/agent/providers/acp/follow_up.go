package acp

import (
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/proto"
)

var errNativeTurnPrecedesFollowUp = errors.New("native work precedes the retained follow-up input")

// A selected provider batch transfers to the base until one prompt claims it.
// Mu protects its scheduling flag and the base's pointer to this batch.
type acpFollowUpPrompt struct {
	sessionID   string
	content     string
	attachments []*leapmuxv1.Attachment
	scheduled   bool
}

func newACPFollowUpPrompt(sessionID, content string, attachments []*leapmuxv1.Attachment) *acpFollowUpPrompt {
	batch := &acpFollowUpPrompt{sessionID: sessionID, content: content, attachments: make([]*leapmuxv1.Attachment, len(attachments))}
	for i, attachment := range attachments {
		if attachment != nil {
			batch.attachments[i] = proto.Clone(attachment).(*leapmuxv1.Attachment)
		}
	}
	return batch
}

// handoffQueuedAgentTurn hands the busy state to the agent turn queued behind
// the turn that just ended, and reports whether one was. When the queued
// turn's end already arrived, it ends that turn too, with the retry verdict
// that the end carried.
func (b *Base) handoffQueuedAgentTurn(sessionID string) bool {
	end, verdict, queued := b.takeQueuedAgentTurn(sessionID)
	if queued && end != nil {
		b.EndAgentTurnWithVerdict(end, verdict)
	}
	return queued
}

func (b *Base) schedulePendingFollowUp(sessionID string) bool {
	b.Mu.Lock()
	batch := b.pendingFollowUp
	if b.sessionID != sessionID || batch == nil || batch.sessionID != sessionID {
		b.Mu.Unlock()
		return false
	}
	if b.StoppedLocked() {
		b.pendingFollowUp = nil
		b.Mu.Unlock()
		return false
	}
	if b.agentTurnQueued {
		b.Mu.Unlock()
		b.handoffQueuedAgentTurn(sessionID)
		return true
	}
	if b.agentTurnActive || batch.scheduled {
		b.Mu.Unlock()
		return true
	}
	batch.scheduled = true
	b.Mu.Unlock()
	// Register before the goroutine can race Stop or Wait's admission closure.
	release, err := b.rootCallbacks.acquire()
	if err != nil {
		b.Mu.Lock()
		if b.pendingFollowUp == batch {
			b.pendingFollowUp = nil
		}
		b.Mu.Unlock()
		if !b.IsStopped() {
			b.reportFollowUpFailure(sessionID, err)
		}
		return true
	}
	go func() {
		defer release()
		// The scheduled operation owns error handling too. The inner prompt lease
		// transfers separately to its response callback after a successful write.
		err := b.continueWithPromptForBatch(sessionID, batch.content, batch.attachments, batch, nil)
		if errors.Is(err, errNativeTurnPrecedesFollowUp) {
			b.handoffQueuedAgentTurn(sessionID)
			return
		}
		if err != nil && !b.IsStopped() {
			b.reportFollowUpFailure(sessionID, err)
		}
	}()
	return true
}

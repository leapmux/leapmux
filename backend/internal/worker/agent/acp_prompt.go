package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// SendInput starts one prompt. The worker queue holds later input until this prompt ends.
func (b *acpBase) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return b.sendPreparedPrompt(content, attachments, nil)
}

// sendPreparedPrompt keeps preparation and the prompt write in one session.
func (b *acpBase) sendPreparedPrompt(content string, attachments []*leapmuxv1.Attachment, prepare func(string) error) error {
	return b.sendPreparedPromptForSession(nil, content, attachments, prepare)
}

// The response wait runs separately and does not hold sessionMu.
func (b *acpBase) sendPreparedPromptForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, prepare func(string) error) error {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	b.mu.Lock()
	if err := checkInputSession(expected, b.sessionID); err != nil {
		b.mu.Unlock()
		return err
	}
	if b.sessionID == "" {
		b.mu.Unlock()
		return fmt.Errorf("agent has no active session")
	}
	if b.stopped {
		b.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if b.promptActive {
		b.mu.Unlock()
		return ErrAgentBusy
	}
	sessionID := b.sessionID
	b.promptActive = true
	b.mu.Unlock()
	b.notePromptActive()
	if prepare != nil {
		if err := prepare(sessionID); err != nil {
			b.clearActivePrompt()
			return err
		}
	}
	err := b.sendACPPromptDetached(content, attachments, func(response json.RawMessage, err error) {
		b.finishPromptRequest(sessionID, response, err)
	})
	if err != nil {
		b.clearActivePrompt()
	}
	return err
}

func (b *acpBase) finishPromptRequest(sessionID string, response json.RawMessage, err error) {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.mu.Lock()
	current := b.sessionID == sessionID
	b.mu.Unlock()
	if !current {
		return
	}
	defer b.clearActivePrompt()
	if err == nil {
		b.handleACPPromptResponse(response)
		return
	}
	// A prompt that fails BECAUSE the reader stopped it is a stop, not a failure.
	//
	// `IsStopped` asks whether the agent PROCESS is shut down, which a stop button
	// does not do. So a cancelled prompt whose RPC returns an error finished the turn
	// as an error, and every tool still in flight inherited it: Cursor's stopped
	// command stored `status: in_progress` with `completion: error`, and the row read
	// `Error` for work the reader had chosen to end.
	stopped := b.IsStopped() || b.acpInterruptRequested()
	completion := MessageCompletionError
	if stopped {
		completion = MessageCompletionInterrupted
	}
	b.finishIncompleteACPPrompt(completion)
	if !stopped {
		slog.Error("acp prompt failed", "agent_id", b.agentID, "error", err)
		b.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("prompt failed: %v", err),
		})
	}
}

func (b *acpBase) sendACPPromptDetached(content string, attachments []*leapmuxv1.Attachment, handle func(json.RawMessage, error)) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    buildACPPromptBlocks(content, classifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP prompt params: %w", err)
	}
	return b.sendDetachedRequest(acpMethodSessionPrompt, params, handle)
}

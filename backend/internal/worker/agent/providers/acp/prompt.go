package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// SendInput starts one prompt. The worker queue holds later input until this prompt ends.
func (b *Base) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return b.SendPreparedPrompt(content, attachments, nil)
}

// SendPreparedPrompt keeps preparation and the prompt write in one session.
func (b *Base) SendPreparedPrompt(content string, attachments []*leapmuxv1.Attachment, prepare func(string) error) error {
	return b.sendPreparedPromptForSession(nil, content, attachments, prepare)
}

// The response wait runs separately and does not hold sessionMu.
func (b *Base) sendPreparedPromptForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, prepare func(string) error) error {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	b.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, b.sessionID); err != nil {
		b.Mu.Unlock()
		return err
	}
	if b.sessionID == "" {
		b.Mu.Unlock()
		return fmt.Errorf("agent has no active session")
	}
	if b.StoppedLocked() {
		b.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if b.promptActive {
		b.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	sessionID := b.sessionID
	b.promptActive = true
	b.Mu.Unlock()
	b.notePromptActive()
	if prepare != nil {
		if err := prepare(sessionID); err != nil {
			b.clearActivePrompt()
			return err
		}
	}
	err := b.SendPromptDetached(content, attachments, func(response json.RawMessage, err error) {
		b.finishPromptRequest(sessionID, response, err)
	})
	if err != nil {
		b.clearActivePrompt()
	}
	return err
}

func (b *Base) finishPromptRequest(sessionID string, response json.RawMessage, err error) {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.Mu.Lock()
	current := b.sessionID == sessionID
	b.Mu.Unlock()
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
	completion := agent.MessageCompletionError
	if stopped {
		completion = agent.MessageCompletionInterrupted
	}
	b.finishIncompleteACPPrompt(completion)
	if !stopped {
		slog.Error("acp prompt failed", "agent_id", b.AgentID(), "error", err)
		b.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("prompt failed: %v", err),
		})
	}
}

func (b *Base) SendPromptDetached(content string, attachments []*leapmuxv1.Attachment, handle func(json.RawMessage, error)) error {
	b.Mu.Lock()
	sessionID := b.sessionID
	b.Mu.Unlock()
	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP prompt params: %w", err)
	}
	return b.SendDetachedRequest(MethodSessionPrompt, params, handle)
}

func (a *Base) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendPreparedPromptForSession(&sessionID, content, attachments, nil)
}

// BuildPromptBlocks converts text + classified attachments into ACP prompt
// blocks compatible with ACP agents.
func BuildPromptBlocks(content string, classified []agent.ClassifiedAttachment) []map[string]interface{} {
	var prompt []map[string]interface{}
	if content != "" {
		prompt = append(prompt, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		if attachment.Kind == agent.AttachmentKindImage {
			prompt = append(prompt, map[string]interface{}{
				"type":     "image",
				"mimeType": attachment.MIMEType,
				"data":     base64.StdEncoding.EncodeToString(attachment.Data),
				"uri":      attachment.Filename,
			})
			continue
		}

		resource := map[string]interface{}{
			"uri":      attachment.Filename,
			"mimeType": attachment.MIMEType,
		}
		if attachment.Kind == agent.AttachmentKindText {
			resource["text"] = string(attachment.Data)
		} else {
			resource["blob"] = base64.StdEncoding.EncodeToString(attachment.Data)
		}
		prompt = append(prompt, map[string]interface{}{
			"type":     "resource",
			"resource": resource,
		})
	}
	return prompt
}

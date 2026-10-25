package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

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
	// turnMu comes before b.Mu, as in BeginAgentTurn. The prompt becomes active
	// while turnMu holds the buffer, so a chunk that the reader adds goes either
	// to the output before the prompt or to the prompt, never to both.
	b.turnMu.Lock()
	b.Mu.Lock()
	sessionID, err := b.claimPromptLocked(expected)
	b.Mu.Unlock()
	if err != nil {
		b.turnMu.Unlock()
		return err
	}
	turn := b.takeUnownedOutputLocked()
	b.turnMu.Unlock()
	b.persistUnownedText(turn)
	b.notePromptActive()
	if b.hooks.PromptPrepared != nil {
		b.hooks.PromptPrepared()
	}
	if prepare != nil {
		if err := prepare(sessionID); err != nil {
			b.clearActivePrompt()
			return err
		}
	}
	err = b.SendPromptDetached(content, attachments, func(response json.RawMessage, err error) {
		b.finishPromptRequest(sessionID, response, err)
	})
	if err != nil {
		b.clearActivePrompt()
	}
	return err
}

// claimPromptLocked checks that a prompt can start in the current session, and
// marks the prompt active. expected, when set, is the session that the input
// was written for. It returns the session of the prompt. The caller holds b.Mu.
func (b *Base) claimPromptLocked(expected *string) (string, error) {
	if err := providerkit.CheckInputSession(expected, b.sessionID); err != nil {
		return "", err
	}
	if b.sessionID == "" {
		return "", fmt.Errorf("agent has no active session")
	}
	if b.StoppedLocked() {
		return "", fmt.Errorf("agent is stopped")
	}
	if b.promptActive {
		return "", agent.ErrAgentBusy
	}
	b.promptActive = true
	return b.sessionID, nil
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
	// A stopped prompt can return an RPC error while the agent process stays alive.
	// Keep its text and tools interrupted rather than failed.
	stopped := b.IsStopped() || b.acpInterruptRequested()
	var ending PromptEnding
	if b.hooks.PromptEnded != nil {
		ending = b.hooks.PromptEnded(err, stopped)
	}
	if err == nil {
		b.handleACPPromptResponse(response)
	} else if stopped {
		b.finishIncompleteACPPrompt(agent.MessageCompletionInterrupted)
	} else {
		b.finishFailedACPPrompt(err, ending)
	}
	// A stop ends the command that the cut tool still awaits. This runs after
	// the rows above, which read a running terminal where it stands, and
	// before the turn ends, so the stopped command does not outlive its turn.
	if stopped {
		b.releaseAwaitedTerminals()
	}
	// Every turn ends here, a failed one included: an agent turn queued behind
	// it still takes over, and input that it never read still reaches the agent.
	b.endTurn(sessionID)
}

func (b *Base) SendPromptDetached(content string, attachments []*leapmuxv1.Attachment, handle func(json.RawMessage, error)) error {
	b.Mu.Lock()
	sessionID := b.sessionID
	b.Mu.Unlock()
	fields := map[string]any{
		"sessionId": sessionID,
		"prompt":    BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	}
	if b.hooks.PromptParams != nil {
		b.hooks.PromptParams(fields)
	}
	params, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal ACP prompt params: %w", err)
	}
	return b.SendDetachedRequest(MethodSessionPrompt, params, handle)
}

// SendChildPromptDetached sends a prompt to sessionID, a session that a
// subagent runs in, and returns without waiting for the turn. handle receives
// the answer when the turn ends. The provider's PromptParams hook does not
// run: it states the settings of the main session.
func (b *Base) SendChildPromptDetached(sessionID, content string, attachments []*leapmuxv1.Attachment, handle func(json.RawMessage, error)) error {
	if sessionID == "" {
		return fmt.Errorf("the subagent prompt has no session")
	}
	params, err := json.Marshal(map[string]any{
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

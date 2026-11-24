package acp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// SendInput starts one prompt. The worker queue holds later input until this prompt ends.
func (b *Base) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return b.SendPreparedPrompt(content, attachments, nil)
}

var _ agent.InputReceiptSender = (*Base)(nil)

func (b *Base) SendInputWithReceipt(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return b.sendPreparedPromptForSession(nil, content, attachments, nil, id.Generate())
}

// SendPreparedPrompt keeps preparation and the prompt write in one session.
func (b *Base) SendPreparedPrompt(content string, attachments []*leapmuxv1.Attachment, prepare func(string) error) error {
	_, err := b.sendPreparedPromptForSession(nil, content, attachments, prepare, "")
	return err
}

// The response wait runs separately and does not hold sessionMu.
func (b *Base) sendPreparedPromptForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, prepare func(string) error, inputID string) (agent.NativeInputReceipt, error) {
	if b.inputAdmissionClosed() {
		return agent.NativeInputReceipt{}, errors.New("agent is stopped")
	}
	releaseOperation, operationErr := b.rootCallbacks.acquire()
	if operationErr != nil {
		return agent.NativeInputReceipt{}, operationErr
	}
	transferred := false
	defer func() {
		if !transferred {
			releaseOperation()
		}
	}()
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
		return agent.NativeInputReceipt{}, err
	}
	var receipt agent.NativeInputReceipt
	if inputID != "" {
		receipt = agent.NativeInputReceipt{SessionID: sessionID, InputID: inputID, TurnID: inputID}
	}
	turn := b.takeUnownedOutputLocked()
	b.preparePriorTextLocked(&turn)
	target := b.openRootTranscriptLocked(sessionID, false, nil, receipt)
	b.rootTarget, b.promptTarget = target, target
	b.turnMu.Unlock()
	b.persistUnownedText(turn)
	b.notePromptActive()
	if err := target.prepare(); err != nil {
		b.finishUnsentPrompt(sessionID)
		return agent.NativeInputReceipt{}, err
	}
	if b.hooks.PromptPrepared != nil {
		b.hooks.PromptPrepared()
	}
	if prepare != nil {
		if err := prepare(sessionID); err != nil {
			b.finishUnsentPrompt(sessionID)
			return agent.NativeInputReceipt{}, err
		}
	}
	err = b.sendOwnedPromptDetachedLocked(sessionID, target, content, attachments, releaseOperation)
	if err != nil {
		b.finishUnsentPrompt(sessionID)
		return agent.NativeInputReceipt{}, err
	}
	transferred = true
	return receipt, nil
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
	if b.inputAdmissionClosedLocked() {
		return "", fmt.Errorf("agent is stopped")
	}
	if b.promptActive {
		return "", agent.ErrAgentBusy
	}
	b.promptActive = true
	return b.sessionID, nil
}

func (b *Base) finishPromptRequest(sessionID string, response json.RawMessage, err error) {
	b.turnMu.Lock()
	target, request := b.promptTarget, b.rootPromptRequest
	b.turnMu.Unlock()
	b.finishOwnedPromptRequest(sessionID, target, request, response, err)
}

func acpResponseStopReason(response json.RawMessage) string {
	if len(response) == 0 {
		return ""
	}
	var payload struct {
		StopReason string `json:"stopReason"`
		Content    struct {
			StopReason string `json:"stopReason"`
		} `json:"content"`
	}
	if json.Unmarshal(response, &payload) != nil {
		return ""
	}
	if payload.StopReason != "" {
		return payload.StopReason
	}
	return payload.Content.StopReason
}

func (b *Base) isCancelledTurnEnd(frame json.RawMessage) bool {
	if acpResponseStopReason(frame) == "cancelled" {
		return true
	}
	return b.hooks.IsCancelledTurnEnd != nil && b.hooks.IsCancelledTurnEnd(frame)
}

func (b *Base) finishOwnedPromptRequest(sessionID string, target *acpTranscriptTarget, request *acpRootPromptRequest, response json.RawMessage, err error) {
	// Flush registry closes queued by PromptEnded or incomplete-tool cleanup
	// immediately after releasing sessionMu, because checkRegistryPublicationLocks
	// defers flushes while sessionMu is held.
	defer b.FlushRegistryObservations()
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	// The reader must finish accepting each tool before the callback detaches its state.
	// Capture and archive recovery run after these locks release.
	b.sessionUpdates.mu.Lock()
	b.turnMu.Lock()
	if !b.ownsRootPromptEndLocked(sessionID, target, request) {
		b.turnMu.Unlock()
		b.sessionUpdates.mu.Unlock()
		return
	}
	b.Mu.Lock()
	stopped := b.StoppedLocked() || b.interruptRequested || b.isCancelledTurnEnd(response)
	b.Mu.Unlock()
	turn := b.takeRootPromptLocked(!stopped)
	b.turnMu.Unlock()
	b.sessionUpdates.mu.Unlock()
	var ending PromptEnding
	if b.hooks.PromptEnded != nil {
		ending = b.hooks.PromptEnded(err, stopped)
	}
	proof := proveACPInputReply(request, turn, response, err)
	if proof.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED {
		stopped = true
	}
	switch {
	case err == nil && response != nil:
		textCompletion, incomplete := b.turnOutputCompletions(stopped)
		b.finishRootSnapshot(turn, textCompletion, incomplete, RootTranscriptPromptEnded, func(writer agent.TranscriptServices, count int) {
			b.persistPromptResponseTo(writer, proof.endRecord(response), count, sessionID, stopped)
		})
	case err == nil || stopped:
		b.finishRootSnapshot(turn, agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted, RootTranscriptPromptEnded, nil)
		if stopped {
			b.resetCompletedRootSpans(sessionID, true)
		}
	default:
		b.finishFailedRootSnapshot(turn, err, ending)
	}
	if proof.retirement != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		b.retireInputReply(proof.receipt, proof.retirement)
		b.retireSharedInputs(turn.endingAdmission, proof.retirement)
	}
	if stopped {
		b.releaseAwaitedTerminals()
	}
	b.endTurn(sessionID)
}

// SendPromptDetached keeps validation and the write in the captured root session.
func (b *Base) SendPromptDetached(sessionID, content string, attachments []*leapmuxv1.Attachment, handle func(json.RawMessage, error)) error {
	return b.SendPromptDetachedUsing(sessionID, content, attachments, func(params json.RawMessage) error {
		return b.SendDetachedRequestObserved(MethodSessionPrompt, params, providerkit.DetachedRequestObservation{}, handle)
	})
}

// SendPromptDetachedUsing prepares a root prompt for a caller-owned transport.
// The sender receives the prepared parameters under the session lease. It
// submits the prompt and returns before waiting for the turn to complete.
func (b *Base) SendPromptDetachedUsing(sessionID, content string, attachments []*leapmuxv1.Attachment, send func(json.RawMessage) error) error {
	if b.inputAdmissionClosed() {
		return errors.New("agent is stopped")
	}
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	b.Mu.Lock()
	err := providerkit.CheckInputSession(&sessionID, b.sessionID)
	stopped := b.inputAdmissionClosedLocked()
	b.Mu.Unlock()
	if err != nil {
		return err
	}
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	params, err := b.prepareDetachedPromptLocked(sessionID, content, attachments, nil)
	if err != nil {
		return err
	}
	return send(params)
}

func (b *Base) prepareDetachedPromptLocked(sessionID, content string, attachments []*leapmuxv1.Attachment, target *acpTranscriptTarget) (json.RawMessage, error) {
	blocks, err := b.preparePromptBlocks(sessionID, content, attachments)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"sessionId": sessionID,
		"prompt":    blocks,
	}
	if b.hooks.PromptParams != nil {
		b.hooks.PromptParams(fields)
	}
	params, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("marshal ACP prompt params: %w", err)
	}
	if target != nil {
		var sent struct {
			SessionID string          `json:"sessionId"`
			Prompt    json.RawMessage `json:"prompt"`
		}
		if err := json.Unmarshal(params, &sent); err != nil {
			return nil, err
		}
		if sent.SessionID != target.sessionID {
			return nil, errors.New("the prepared ACP prompt belongs to another session")
		}
		trimmed := bytes.TrimSpace(sent.Prompt)
		if len(trimmed) == 0 || trimmed[0] != '[' {
			return nil, errors.New("the prepared ACP prompt is not a block array")
		}
		if target.transcript != nil {
			var selected []json.RawMessage
			if err := json.Unmarshal(trimmed, &selected); err != nil {
				return nil, err
			}
			// Unmarshal copies each raw value. The provider cannot mutate the packet.
			if err := target.transcript.PrepareInput(selected); err != nil {
				return nil, err
			}
		}
	}
	if err := b.writeDeferredOptionsBeforePrompt(sessionID); err != nil {
		return nil, err
	}
	return params, nil
}

// writeDeferredOptionsBeforePrompt runs under the prompt's exclusive session
// lease. The live settings batch cannot write during it because native writes
// take the corresponding read lease.
func (b *Base) writeDeferredOptionsBeforePrompt(sessionID string) error {
	b.Mu.Lock()
	writes := maps.Clone(b.options.deferredWrites)
	b.Mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(writes)) {
		if err := b.setConfigOptionForSession(sessionID, id, writes[id], nil, false); err != nil {
			return fmt.Errorf("apply ACP option %s before the prompt: %w", id, err)
		}
	}
	return nil
}

// preparePromptBlocks keeps the provider's validation on the original bytes.
// The hook can rewrite the standard ACP blocks without decoding their blobs.
func (b *Base) preparePromptBlocks(sessionID, content string, attachments []*leapmuxv1.Attachment) ([]map[string]any, error) {
	classified := agent.ClassifyAttachments(attachments)
	blocks := BuildPromptBlocks(content, classified)
	if b.hooks.PreparePrompt != nil {
		if err := b.hooks.PreparePrompt(sessionID, classified, blocks); err != nil {
			return nil, err
		}
	}
	return blocks, nil
}

// SendChildPromptDetached sends a prompt to an owned child session and
// returns without waiting for the turn. handle receives
// the answer when the turn ends. The provider's PromptParams hook does not
// run: it states the settings of the main session.
func (b *Base) SendChildPromptDetached(target ChildSessionTarget, content string, attachments []*leapmuxv1.Attachment, handle func(json.RawMessage, error)) error {
	return b.SendChildPromptDetachedUsing(target, content, attachments, func(params json.RawMessage) error {
		return b.SendDetachedRequest(MethodSessionPrompt, params, handle)
	})
}

// SendChildPromptDetachedUsing prepares the prompt under the captured child's
// dispatch lease. The sender submits it without waiting for the turn to end.
func (b *Base) SendChildPromptDetachedUsing(target ChildSessionTarget, content string, attachments []*leapmuxv1.Attachment, send func(json.RawMessage) error) error {
	if !target.valid() || b.inputAdmissionClosed() {
		return agent.ErrChildRouteNotReady
	}
	release, admitted := b.childDispatch.acquire(b.Context())
	if !admitted {
		return agent.ErrChildRouteNotReady
	}
	defer release()
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	b.Mu.Lock()
	current := b.sessionID == target.RootSessionID && !b.inputAdmissionClosedLocked()
	b.Mu.Unlock()
	if !current || b.childSessionRow(target.SessionID) != target.RowKey {
		return agent.ErrChildRouteNotReady
	}
	blocks, err := b.preparePromptBlocks(target.SessionID, content, attachments)
	if err != nil {
		return err
	}
	params, err := json.Marshal(map[string]any{"sessionId": target.SessionID, "prompt": blocks})
	if err != nil {
		return fmt.Errorf("marshal ACP prompt params: %w", err)
	}
	return send(params)
}

func (a *Base) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	_, err := a.sendPreparedPromptForSession(&sessionID, content, attachments, nil, "")
	return err
}

// BuildPromptBlocks converts text + classified attachments into ACP prompt
// blocks compatible with ACP agents.
func BuildPromptBlocks(content string, classified []agent.ClassifiedAttachment) []map[string]interface{} {
	var prompt []map[string]interface{}
	if content != "" {
		prompt = append(prompt, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		if attachment.Kind == contracts.AttachmentMediaKindImage {
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
		if attachment.Kind == contracts.AttachmentMediaKindText {
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

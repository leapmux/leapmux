package pi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

type piCustomQuestionAnswer struct{ Text string }

func (a *Agent) clearPiQuestionStateLocked() {
	a.questionGeneration++
	clear(a.questionDialogs)
	clear(a.customQuestionAnswers)
}

func (a *Agent) piQuestionActiveLocked(source *piQuestionSource) bool {
	tool := a.toolStates[source.Key.ToolCallID]
	return source.Key.Generation == a.questionGeneration && tool != nil && tool.Order == source.Key.ToolOrder
}

func (a *Agent) clearPiQuestionToolLocked(toolCallID string) {
	for id, dialog := range a.questionDialogs {
		if dialog.Key.ToolCallID == toolCallID {
			delete(a.questionDialogs, id)
		}
	}
	for key := range a.customQuestionAnswers {
		if key.ToolCallID == toolCallID {
			delete(a.customQuestionAnswers, key)
		}
	}
}

func (a *Agent) preparePiQuestionDialog(id string, raw []byte) (*piQuestionSource, bool) {
	source := a.matchPiQuestionDialog(raw)
	if source == nil {
		return nil, false
	}
	a.Mu.Lock()
	if !a.piQuestionActiveLocked(source) {
		a.Mu.Unlock()
		return nil, false
	}
	answer := a.customQuestionAnswers[source.Key]
	if source.Dialog.Method == contracts.PiDialogMethodInput && source.Dialog.Placeholder == "" && answer != nil {
		delete(a.customQuestionAnswers, source.Key)
		a.Mu.Unlock()
		response, err := json.Marshal(map[string]any{"type": contracts.PiEventExtensionUIResponse, "id": id, "value": answer.Text})
		if err == nil {
			err = a.Process.SendRawInput(response)
		}
		if err == nil {
			return source, true
		}
		slog.Warn("send pi custom question answer", "agent_id", a.AgentID(), "request_id", id, "error", err)
		a.Mu.Lock()
		if !a.piQuestionActiveLocked(source) {
			a.Mu.Unlock()
			return nil, false
		}
		// The send failed, so the code below publishes the dialog again. Restore the
		// text that the user typed, or the next response carries an empty answer. A
		// newer answer for the same key wins, exactly as the sibling path in
		// SendRawInput keeps the newer one.
		if a.customQuestionAnswers == nil {
			a.customQuestionAnswers = make(map[piQuestionKey]*piCustomQuestionAnswer)
		}
		if a.customQuestionAnswers[source.Key] == nil {
			a.customQuestionAnswers[source.Key] = answer
		}
	}
	if a.questionDialogs == nil {
		a.questionDialogs = make(map[string]*piQuestionSource)
	}
	a.questionDialogs[id] = source
	a.Mu.Unlock()
	return source, false
}

// SendRawInput converts one custom answer into rpiv's select-then-input exchange.
// Ordinary responses and unrelated Pi commands retain their original bytes.
func (a *Agent) SendRawInput(data []byte) error {
	var response struct {
		Type      string  `json:"type"`
		ID        string  `json:"id"`
		Value     *string `json:"value"`
		Cancelled bool    `json:"cancelled"`
	}
	if json.Unmarshal(data, &response) != nil || response.Type != contracts.PiEventExtensionUIResponse {
		return a.Process.SendRawInput(data)
	}
	// Both forwarding paths below carry the value unchanged, and only a plan
	// menu ever offers it, so the mark is set before the dialog lookup rather
	// than duplicated in each branch.
	if response.Value != nil && *response.Value == contracts.PiPlanActionImplementFresh {
		a.notePiPlanFreshApproval()
	}
	a.Mu.Lock()
	dialog := a.questionDialogs[response.ID]
	delete(a.questionDialogs, response.ID)
	if dialog == nil {
		a.Mu.Unlock()
		return a.Process.SendRawInput(data)
	}
	active := a.piQuestionActiveLocked(dialog)
	if response.Cancelled {
		delete(a.customQuestionAnswers, dialog.Key)
	}
	custom := active && !response.Cancelled && response.Value != nil && dialog.Dialog.Method == contracts.PiDialogMethodSelect &&
		!slices.Contains(dialog.Dialog.Options, *response.Value) && len(dialog.Dialog.Options) > 0
	if !custom {
		a.Mu.Unlock()
		return a.Process.SendRawInput(data)
	}
	answer := &piCustomQuestionAnswer{Text: *response.Value}
	if a.customQuestionAnswers == nil {
		a.customQuestionAnswers = make(map[piQuestionKey]*piCustomQuestionAnswer)
	}
	a.customQuestionAnswers[dialog.Key] = answer
	a.Mu.Unlock()

	var fields map[string]json.RawMessage
	err := json.Unmarshal(data, &fields)
	if err == nil {
		fields["value"], err = json.Marshal(dialog.Dialog.Options[len(dialog.Dialog.Options)-1])
	}
	var encoded []byte
	if err == nil {
		encoded, err = json.Marshal(fields)
	}
	if err == nil {
		err = a.Process.SendRawInput(encoded)
	}
	if err == nil {
		return nil
	}
	a.Mu.Lock()
	if a.customQuestionAnswers[dialog.Key] == answer {
		delete(a.customQuestionAnswers, dialog.Key)
	}
	if a.piQuestionActiveLocked(dialog) && a.questionDialogs[response.ID] == nil {
		a.questionDialogs[response.ID] = dialog
	}
	a.Mu.Unlock()
	return fmt.Errorf("start pi custom answer: %w", err)
}

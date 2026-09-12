package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

type piCustomQuestionAnswer struct{ Text string }

func (a *PiAgent) clearPiQuestionStateLocked() {
	a.questionGeneration++
	clear(a.questionDialogs)
	clear(a.customQuestionAnswers)
}

func (a *PiAgent) piQuestionActiveLocked(source *piQuestionSource) bool {
	tool := a.toolStates[source.Key.ToolCallID]
	return source.Key.Generation == a.questionGeneration && tool != nil && tool.Order == source.Key.ToolOrder
}

func (a *PiAgent) clearPiQuestionToolLocked(toolCallID string) {
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

func (a *PiAgent) preparePiQuestionDialog(id string, raw []byte) (*piQuestionSource, bool) {
	source := a.matchPiQuestionDialog(raw)
	if source == nil {
		return nil, false
	}
	a.mu.Lock()
	if !a.piQuestionActiveLocked(source) {
		a.mu.Unlock()
		return nil, false
	}
	answer := a.customQuestionAnswers[source.Key]
	if source.Dialog.Method == contracts.PiDialogMethodInput && source.Dialog.Placeholder == "" && answer != nil {
		delete(a.customQuestionAnswers, source.Key)
		a.mu.Unlock()
		response, err := json.Marshal(map[string]any{"type": contracts.PiEventExtensionUIResponse, "id": id, "value": answer.Text})
		if err == nil {
			err = a.processBase.SendRawInput(response)
		}
		if err == nil {
			return source, true
		}
		slog.Warn("send pi custom question answer", "agent_id", a.agentID, "request_id", id, "error", err)
		a.mu.Lock()
		if !a.piQuestionActiveLocked(source) {
			a.mu.Unlock()
			return nil, false
		}
	}
	if a.questionDialogs == nil {
		a.questionDialogs = make(map[string]*piQuestionSource)
	}
	a.questionDialogs[id] = source
	a.mu.Unlock()
	return source, false
}

// SendRawInput converts one custom answer into rpiv's select-then-input exchange.
// Ordinary responses and unrelated Pi commands retain their original bytes.
func (a *PiAgent) SendRawInput(data []byte) error {
	var response struct {
		Type      string  `json:"type"`
		ID        string  `json:"id"`
		Value     *string `json:"value"`
		Cancelled bool    `json:"cancelled"`
	}
	if json.Unmarshal(data, &response) != nil || response.Type != contracts.PiEventExtensionUIResponse {
		return a.processBase.SendRawInput(data)
	}
	a.mu.Lock()
	dialog := a.questionDialogs[response.ID]
	delete(a.questionDialogs, response.ID)
	if dialog == nil {
		a.mu.Unlock()
		return a.processBase.SendRawInput(data)
	}
	active := a.piQuestionActiveLocked(dialog)
	if response.Cancelled {
		delete(a.customQuestionAnswers, dialog.Key)
	}
	custom := active && !response.Cancelled && response.Value != nil && dialog.Dialog.Method == contracts.PiDialogMethodSelect &&
		!slices.Contains(dialog.Dialog.Options, *response.Value) && len(dialog.Dialog.Options) > 0
	if !custom {
		a.mu.Unlock()
		return a.processBase.SendRawInput(data)
	}
	answer := &piCustomQuestionAnswer{Text: *response.Value}
	if a.customQuestionAnswers == nil {
		a.customQuestionAnswers = make(map[piQuestionKey]*piCustomQuestionAnswer)
	}
	a.customQuestionAnswers[dialog.Key] = answer
	a.mu.Unlock()

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
		err = a.processBase.SendRawInput(encoded)
	}
	if err == nil {
		return nil
	}
	a.mu.Lock()
	if a.customQuestionAnswers[dialog.Key] == answer {
		delete(a.customQuestionAnswers, dialog.Key)
	}
	if a.piQuestionActiveLocked(dialog) && a.questionDialogs[response.ID] == nil {
		a.questionDialogs[response.ID] = dialog
	}
	a.mu.Unlock()
	return fmt.Errorf("start pi custom answer: %w", err)
}

package pi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

type piQuestionDialog struct {
	Method      string   `json:"method"`
	Title       string   `json:"title"`
	Placeholder string   `json:"placeholder"`
	Options     []string `json:"options"`
}

func piQuestionText(text string) string { return strings.ReplaceAll(text, "\r\n", "\n") }

// view reads what piSourceQuestion.matches compares of the dialog.
func (d piQuestionDialog) view() piDialogView {
	return piDialogView{method: d.Method, title: d.Title, placeholder: d.Placeholder, options: d.Options, optionsOK: true}
}

// piQuestionIndex returns the index of the one question of an
// ask_user_question call's arguments that dialog asks, as
// rpiv-ask-user-question formats its RPC dialogs. It reads the questions as
// the prompt does (readPiSourceQuestion): a question that the dialog cannot
// have asked is skipped, and a dialog that two questions match matches none.
func piQuestionIndex(dialog piQuestionDialog, args json.RawMessage) (int, bool) {
	var input struct {
		Questions []json.RawMessage `json:"questions"`
	}
	if json.Unmarshal(args, &input) != nil {
		return 0, false
	}
	view := dialog.view()
	matched := -1
	for index, raw := range input.Questions {
		question := readPiSourceQuestion(index, raw)
		if question == nil || !question.matches(view) {
			continue
		}
		if matched >= 0 {
			return 0, false
		}
		matched = index
	}
	return matched, matched >= 0
}

type piQuestionKey struct {
	Generation uint64
	ToolCallID string
	ToolOrder  uint64
	Index      int
}

type piQuestionSource struct {
	Key    piQuestionKey
	Args   json.RawMessage
	Dialog piQuestionDialog
}

// matchPiQuestionDialog selects one active call and keeps its execution identity.
func (a *Agent) matchPiQuestionDialog(raw []byte) *piQuestionSource {
	var dialog piQuestionDialog
	if json.Unmarshal(raw, &dialog) != nil {
		return nil
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	var source *piQuestionSource
	for id, tool := range a.toolStates {
		if tool == nil || tool.ToolName != contracts.PiToolAskUserQuestion {
			continue
		}
		index, matches := piQuestionIndex(dialog, tool.Args)
		if !matches {
			continue
		}
		if source != nil {
			return nil
		}
		source = &piQuestionSource{Key: piQuestionKey{Generation: a.questionGeneration, ToolCallID: id, ToolOrder: tool.Order, Index: index}, Args: append(json.RawMessage(nil), tool.Args...), Dialog: dialog}
	}
	return source
}

// piControlSourceSeq uses an explicit question match or the only active tool.
// Renderers validate that tool's input before they recover omitted dialog details.
func (a *Agent) piControlSourceSeq(match *piQuestionSource) int64 {
	a.Mu.Lock()
	callID := ""
	if match != nil {
		if match.Key.Generation != a.questionGeneration {
			a.Mu.Unlock()
			return 0
		}
		callID = match.Key.ToolCallID
	} else {
		for id, tool := range a.toolStates {
			if tool == nil {
				continue
			}
			if callID != "" {
				a.Mu.Unlock()
				return 0
			}
			callID = id
		}
	}
	tool := a.toolStates[callID]
	if tool == nil || (match != nil && tool.Order != match.Key.ToolOrder) {
		a.Mu.Unlock()
		return 0
	}
	generation, order, toolName := a.questionGeneration, tool.Order, tool.ToolName
	args := append(json.RawMessage(nil), tool.Args...)
	a.Mu.Unlock()
	stored, err := a.sink.ReadToolRequest(callID)
	if err != nil {
		slog.Warn("read pi control source", "agent_id", a.AgentID(), "tool_call_id", callID, "error", err)
		return 0
	}
	if stored == nil {
		return 0
	}
	var source struct {
		Type string `json:"type"`
		piToolExecutionEnvelope
	}
	if json.Unmarshal(stored.Content.Original, &source) != nil || source.Type != contracts.PiEventToolExecutionStart || source.ToolCallID != callID || source.ToolName != toolName {
		return 0
	}
	sourceArgs := source.Args
	if len(sourceArgs) == 0 {
		sourceArgs = source.Input
	}
	if !bytes.Equal(sourceArgs, args) {
		return 0
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	current := a.toolStates[callID]
	if generation != a.questionGeneration || current == nil || current.Order != order || !bytes.Equal(current.Args, args) {
		return 0
	}
	return stored.Seq
}

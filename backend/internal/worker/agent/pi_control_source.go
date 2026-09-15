package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

type piQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type piQuestion struct {
	Question    string             `json:"question"`
	Header      string             `json:"header"`
	MultiSelect bool               `json:"multiSelect"`
	Options     []piQuestionOption `json:"options"`
}

type piQuestionDialog struct {
	Method      string   `json:"method"`
	Title       string   `json:"title"`
	Placeholder string   `json:"placeholder"`
	Options     []string `json:"options"`
}

func piQuestionText(text string) string { return strings.ReplaceAll(text, "\r\n", "\n") }

// piQuestionIndex verifies the formatting that rpiv-ask-user-question uses for RPC dialogs.
func piQuestionIndex(dialog piQuestionDialog, args json.RawMessage) (int, bool) {
	var input struct {
		Questions []piQuestion `json:"questions"`
	}
	if json.Unmarshal(args, &input) != nil {
		return 0, false
	}
	matched := -1
	for index, question := range input.Questions {
		if question.Question == "" || len(question.Options) == 0 {
			continue
		}
		title := piQuestionText(question.Question)
		if question.Header != "" {
			title = "[" + piQuestionText(question.Header) + "] " + title
		}
		options := make([]string, len(question.Options))
		for optionIndex, option := range question.Options {
			options[optionIndex] = strconv.Itoa(optionIndex+1) + ". " + piQuestionText(option.Label) + " — " + piQuestionText(option.Description)
		}
		valid := false
		switch dialog.Method {
		case contracts.PiDialogMethodSelect:
			if question.MultiSelect || len(dialog.Options) != len(options)+1 {
				continue
			}
			valid = dialog.Title == title || strings.HasPrefix(dialog.Title, title+"\n\n--- ")
			for optionIndex, option := range options {
				valid = valid && dialog.Options[optionIndex] == option
			}
			valid = valid && strings.HasPrefix(dialog.Options[len(options)], strconv.Itoa(len(options)+1)+". ")
		case contracts.PiDialogMethodInput:
			if question.MultiSelect {
				valid = dialog.Placeholder == "1,3" && strings.HasPrefix(dialog.Title, title+"\n\n"+strings.Join(options, "\n")+"\n\n")
			} else {
				valid = dialog.Placeholder == "" && strings.HasPrefix(dialog.Title, title+"\n\n") && !strings.HasPrefix(dialog.Title, title+"\n\n--- ")
			}
		}
		if !valid {
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
func (a *PiAgent) matchPiQuestionDialog(raw []byte) *piQuestionSource {
	var dialog piQuestionDialog
	if json.Unmarshal(raw, &dialog) != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
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
func (a *PiAgent) piControlSourceSeq(match *piQuestionSource) int64 {
	a.mu.Lock()
	callID := ""
	if match != nil {
		if match.Key.Generation != a.questionGeneration {
			a.mu.Unlock()
			return 0
		}
		callID = match.Key.ToolCallID
	} else {
		for id, tool := range a.toolStates {
			if tool == nil {
				continue
			}
			if callID != "" {
				a.mu.Unlock()
				return 0
			}
			callID = id
		}
	}
	tool := a.toolStates[callID]
	if tool == nil || (match != nil && tool.Order != match.Key.ToolOrder) {
		a.mu.Unlock()
		return 0
	}
	generation, order, toolName := a.questionGeneration, tool.Order, tool.ToolName
	args := append(json.RawMessage(nil), tool.Args...)
	a.mu.Unlock()
	stored, err := a.sink.ReadToolRequest(callID)
	if err != nil {
		slog.Warn("read pi control source", "agent_id", a.agentID, "tool_call_id", callID, "error", err)
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
	a.mu.Lock()
	defer a.mu.Unlock()
	current := a.toolStates[callID]
	if generation != a.questionGeneration || current == nil || current.Order != order || !bytes.Equal(current.Args, args) {
		return 0
	}
	return stored.Seq
}

package semanticevents

import (
	"fmt"
	"strings"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// PromptKind describes the shape of a provider-neutral request.
func PromptKind(prompt *leapmuxv1.ControlPrompt) leapmuxv1.SemanticControlPromptKind {
	switch prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_PERMISSION
	case *leapmuxv1.ControlPrompt_Plan:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_PLAN
	case *leapmuxv1.ControlPrompt_Question:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_QUESTION
	case *leapmuxv1.ControlPrompt_Elicitation:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_ELICITATION
	case *leapmuxv1.ControlPrompt_Dialog:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_DIALOG
	case *leapmuxv1.ControlPrompt_Unreadable:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_UNREADABLE
	default:
		return leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_UNSPECIFIED
	}
}

const promptDetailLength = 120

// PromptDetail supplies one short line to identify a request for display.
func PromptDetail(prompt *leapmuxv1.ControlPrompt) string {
	raw := promptDetail(prompt)
	var b strings.Builder
	b.Grow(min(len(raw), promptDetailLength))
	count := 0
	truncateLen := 0
	emit := func(r rune) bool {
		if count == promptDetailLength {
			return false
		}
		if count == promptDetailLength-1 {
			truncateLen = b.Len()
		}
		b.WriteRune(r)
		count++
		return true
	}
	seenWord := false
	needSpace := false
	for _, r := range raw {
		if unicode.IsSpace(r) {
			if seenWord {
				needSpace = true
			}
			continue
		}
		if needSpace {
			if !emit(' ') {
				return b.String()[:truncateLen] + "…"
			}
			needSpace = false
		}
		if !emit(r) {
			return b.String()[:truncateLen] + "…"
		}
		seenWord = true
	}
	return b.String()
}

func promptDetail(prompt *leapmuxv1.ControlPrompt) string {
	switch value := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		return firstNonEmpty(value.Permission.GetTitle(), value.Permission.GetCommand(), value.Permission.GetText())
	case *leapmuxv1.ControlPrompt_Plan:
		return "approve the plan"
	case *leapmuxv1.ControlPrompt_Question:
		questions := value.Question.GetQuestions()
		if len(questions) == 0 {
			return ""
		}
		text := firstNonEmpty(questions[0].GetQuestion(), questions[0].GetHeader())
		switch more := len(questions) - 1; more {
		case 0:
			return text
		case 1:
			return text + " (and 1 more question)"
		default:
			return fmt.Sprintf("%s (and %d more questions)", text, more)
		}
	case *leapmuxv1.ControlPrompt_Elicitation:
		return firstNonEmpty(value.Elicitation.GetMessage(), value.Elicitation.GetTitle(), value.Elicitation.GetDescription())
	case *leapmuxv1.ControlPrompt_Dialog:
		return firstNonEmpty(value.Dialog.GetTitle(), value.Dialog.GetMessage())
	case *leapmuxv1.ControlPrompt_Unreadable:
		if label := leapmuxv1.ControlPayloadFault_name[int32(value.Unreadable)]; value.Unreadable != 0 && label != "" {
			fault := strings.ToLower(strings.TrimPrefix(label, "CONTROL_PAYLOAD_FAULT_"))
			return fmt.Sprintf("LeapMux cannot read it (%s)", fault)
		}
		return "LeapMux cannot read it"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

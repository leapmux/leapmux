package codewhale

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// TestStructTagsMatchTheContract pins the JSON tags of the hand-written structs
// that read or write a contracted key to contracts/codewhale-protocol.json.
//
// The browser reads the same keys from the generated constants. Without this pin
// a renamed tag fails SILENTLY: the item metadata decodes into empty fields and
// a tool call loses its identity, a saved answer stops naming its question, or a
// stored child row states a field the browser no longer reads, and nothing
// states that a key went unread.
func TestStructTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		value any
		field string
		want  string
	}{
		{"item tool name", itemMetadata{}, "ToolName", contracts.CodewhaleItemMetadataToolName},
		{"item tool use id", itemMetadata{}, "ToolUseID", contracts.CodewhaleItemMetadataToolUseID},
		{"item tool call id", itemMetadata{}, "ToolCallID", contracts.CodewhaleItemMetadataToolCallID},
		{"item tool input", itemMetadata{}, "ToolInput", contracts.CodewhaleItemMetadataToolInput},
		{"item deferred load", itemMetadata{}, "DeferredToolLoaded", contracts.CodewhaleItemMetadataDeferredToolLoaded},
		{"reply frame", codewhaleReplyFrame{}, "Frame", contracts.CodewhaleReplyFieldFrame},
		{"reply decision", codewhaleReplyFrame{}, "Decision", contracts.CodewhaleReplyFieldDecision},
		{"reply remember", codewhaleReplyFrame{}, "Remember", contracts.CodewhaleReplyFieldRemember},
		{"reply answers", codewhaleReplyFrame{}, "Answers", contracts.CodewhaleReplyFieldAnswers},
		{"reply declined", codewhaleReplyFrame{}, "Declined", contracts.CodewhaleReplyFieldDeclined},
		{"answer id", userInputAnswer{}, "ID", contracts.CodewhaleAnswerFieldID},
		{"answer label", userInputAnswer{}, "Label", contracts.CodewhaleAnswerFieldLabel},
		{"answer value", userInputAnswer{}, "Value", contracts.CodewhaleAnswerFieldValue},
		{"envelope event", codewhaleEnvelope{}, "Event", contracts.CodewhaleEnvelopeFieldEvent},
		{"envelope turn id", codewhaleEnvelope{}, "TurnID", contracts.CodewhaleEnvelopeFieldTurnID},
		{"envelope payload", codewhaleEnvelope{}, "Payload", contracts.CodewhaleEnvelopeFieldPayload},
		{"item payload item", itemEventPayload{}, "Item", contracts.CodewhaleItemFieldItem},
		{"item payload tool", itemEventPayload{}, "Tool", contracts.CodewhaleItemFieldTool},
		{"item kind", turnItem{}, "Kind", contracts.CodewhaleItemFieldKind},
		{"item summary", turnItem{}, "Summary", contracts.CodewhaleItemFieldSummary},
		{"item detail", turnItem{}, "Detail", contracts.CodewhaleItemFieldDetail},
		{"item metadata", turnItem{}, "Metadata", contracts.CodewhaleItemFieldMetadata},
		{"tool id", toolStart{}, "ID", contracts.CodewhaleToolFieldID},
		{"tool name", toolStart{}, "Name", contracts.CodewhaleToolFieldName},
		{"tool input", toolStart{}, "Input", contracts.CodewhaleToolFieldInput},
		{"turn record", turnEventPayload{}, "Turn", contracts.CodewhaleTurnFieldTurn},
		{"turn status", turnRecord{}, "Status", contracts.CodewhaleTurnFieldStatus},
		{"approval tool name", approvalEventPayload{}, "ToolName", contracts.CodewhaleApprovalFieldToolName},
		{"user input request", userInputEventPayload{}, "Request", contracts.CodewhaleUserInputFieldRequest},
		{"request questions", userInputRequest{}, "Questions", contracts.CodewhaleQuestionFieldQuestions},
		{"question id", questionRecord{}, "ID", contracts.CodewhaleQuestionFieldID},
		{"question header", questionRecord{}, "Header", contracts.CodewhaleQuestionFieldHeader},
		{"question text", questionRecord{}, "Question", contracts.CodewhaleQuestionFieldQuestion},
		{"question options", questionRecord{}, "Options", contracts.CodewhaleQuestionFieldOptions},
		{"option label", questionOption{}, "Label", contracts.CodewhaleQuestionFieldLabel},
		{"agent input action", agentToolInput{}, "Action", contracts.CodewhaleAgentInputFieldAction},
		{"agent input prompt", agentToolInput{}, "Prompt", contracts.CodewhaleAgentInputFieldPrompt},
		{"agent input name", agentToolInput{}, "Name", contracts.CodewhaleAgentInputFieldName},
		{"agent input type", agentToolInput{}, "Type", contracts.CodewhaleAgentInputFieldType},
		{"agent result id", agentToolResult{}, "AgentID", contracts.CodewhaleResultFieldAgentID},
		{"agent result settled", agentToolResult{}, "Settled", contracts.CodewhaleResultFieldSettled},
		{"settled run id", settledRun{}, "AgentID", contracts.CodewhaleResultFieldAgentID},
		{"workflow status", workflowResultMetadata{}, "Status", contracts.CodewhaleResultFieldStatus},
		{"todo task updates", todoResultMetadata{}, "TaskUpdates", contracts.CodewhaleResultFieldTaskUpdates},
		{"todo checklist", todoTaskUpdates{}, "Checklist", contracts.CodewhaleResultFieldChecklist},
		{"todo items", todoChecklist{}, "Items", contracts.CodewhaleResultFieldItems},
		{"transcript kind", transcriptRecord{}, "Kind", contracts.CodewhaleTranscriptFieldKind},
		{"transcript index", transcriptRecord{}, "Index", contracts.CodewhaleTranscriptFieldIndex},
		{"transcript message", transcriptRecord{}, "Message", contracts.CodewhaleTranscriptFieldMessage},
		{"transcript role", transcriptMessage{}, "Role", contracts.CodewhaleTranscriptFieldRole},
		{"transcript content", transcriptMessage{}, "Content", contracts.CodewhaleTranscriptFieldContent},
		{"stored row kind", childBlockRow{}, "Kind", contracts.CodewhaleTranscriptFieldKind},
		{"stored row index", childBlockRow{}, "Index", contracts.CodewhaleTranscriptFieldIndex},
		{"stored row message", childBlockRow{}, "Message", contracts.CodewhaleTranscriptFieldMessage},
		{"stored row role", childRowMessage{}, "Role", contracts.CodewhaleTranscriptFieldRole},
		{"stored row content", childRowMessage{}, "Content", contracts.CodewhaleTranscriptFieldContent},
		{"block type", transcriptBlock{}, "Type", contracts.CodewhaleBlockFieldType},
		{"block id", transcriptBlock{}, "ID", contracts.CodewhaleBlockFieldID},
		{"block name", transcriptBlock{}, "Name", contracts.CodewhaleBlockFieldName},
		{"block tool use id", transcriptBlock{}, "ToolUseID", contracts.CodewhaleBlockFieldToolUseID},
		{"block text", transcriptBlock{}, "Text", contracts.CodewhaleBlockFieldText},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tt.value)
			field, found := typ.FieldByName(tt.field)
			require.True(t, found, "%s has no field %s", typ.Name(), tt.field)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			assert.Equal(t, tt.want, name, "%s.%s must carry the contract field name", typ.Name(), tt.field)
		})
	}
}

package letta

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

// Letta Code owns these words. The worker writes the approval_response payload
// and the question response through generated structs, whose tags follow the
// contract, and it reads the stream through the hand-written structs below.
// This test pins each word to Letta Code's spelling, so a rename in
// contracts/letta-protocol.json fails here instead of silently leaving the wire
// stale. It is also the Go reader of the contract tables that no production
// call site branches on.
func TestLettaContractVocabularyMatchesTheWire(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "kind", contracts.LettaReplyKind)
	assert.Equal(t, "approval_response", contracts.LettaReplyKindApprovalResponse)
	assert.Equal(t, "request_id", contracts.LettaReplyRequestID)
	assert.Equal(t, "decision", contracts.LettaReplyDecision)
	assert.Equal(t, "behavior", contracts.LettaReplyBehavior)
	assert.Equal(t, "message", contracts.LettaReplyMessage)
	assert.Equal(t, "error", contracts.LettaReplyError)

	assert.Equal(t, "input", contracts.LettaCommandInput)
	assert.Equal(t, "abort_message", contracts.LettaCommandAbortMessage)
	assert.Equal(t, "runtime_start", contracts.LettaCommandRuntimeStart)
	assert.Equal(t, "sync", contracts.LettaCommandSync)
	assert.Equal(t, "app_server_info", contracts.LettaCommandAppServerInfo)
	assert.Equal(t, "agent_list", contracts.LettaCommandAgentList)
	assert.Equal(t, "conversation_list", contracts.LettaCommandConversationList)
	assert.Equal(t, "list_models", contracts.LettaCommandListModels)
	assert.Equal(t, "update_model", contracts.LettaCommandUpdateModel)

	assert.Equal(t, "ask_user_question", contracts.LettaQuestionReceiptType)
	assert.Equal(t, "ask_user_question_response", contracts.LettaQuestionResponseType)
	assert.Equal(t, "answered", contracts.LettaQuestionStatusAnswered)
	assert.Equal(t, "dismissed", contracts.LettaQuestionStatusDismissed)
	assert.Equal(t, "type", contracts.LettaQuestionFieldType)
	assert.Equal(t, "version", contracts.LettaQuestionFieldVersion)
	assert.Equal(t, "toolCallId", contracts.LettaQuestionFieldToolCallID)
	assert.Equal(t, "questions", contracts.LettaQuestionFieldQuestions)
	assert.Equal(t, "status", contracts.LettaQuestionFieldStatus)
	assert.Equal(t, "answers", contracts.LettaQuestionFieldAnswers)
	assert.Equal(t, "cancelled", contracts.LettaStopReasonCancelled)
	assert.Equal(t, "error", contracts.LettaStopReasonError)
	assert.Equal(t, "llm_api_error", contracts.LettaStopReasonLLMAPIError)

	assert.Equal(t, "pending", contracts.LettaSubagentStatePending)
	assert.Equal(t, "running", contracts.LettaSubagentStateRunning)
	assert.Equal(t, "completed", contracts.LettaSubagentStateCompleted)
	assert.Equal(t, "error", contracts.LettaSubagentStateError)
	assert.Equal(t, "synthetic-tool-return-stream-", contracts.LettaToolOutputStreamIDPrefix)
	assert.Equal(t, "success", contracts.LettaToolStatusSuccess)
	assert.Equal(t, "error", contracts.LettaToolStatusError)
	for field, key := range map[string]string{
		"ID": contracts.LettaDeltaFieldID, "RunID": contracts.LettaDeltaFieldRunID,
		"ToolReturns": contracts.LettaDeltaFieldToolReturns,
		"EventType":   contracts.LettaDeltaFieldEventType,
		"EventData":   contracts.LettaDeltaFieldEventData,
		"Message":     contracts.LettaDeltaFieldMessage,
	} {
		decoded, found := reflect.TypeFor[lettaDelta]().FieldByName(field)
		assert.True(t, found, field)
		assert.Equal(t, key, decoded.Tag.Get("json"))
	}
	for field, key := range map[string]string{
		"CommandID": contracts.LettaDeltaFieldCommandID,
		"Input":     contracts.LettaDeltaFieldInput,
		"Output":    contracts.LettaDeltaFieldOutput,
		"Success":   contracts.LettaDeltaFieldSuccess,
	} {
		decoded, found := reflect.TypeFor[lettaCompactionDelta]().FieldByName(field)
		assert.True(t, found, field)
		assert.Equal(t, key, decoded.Tag.Get("json"))
	}
	stopReason, found := reflect.TypeFor[lettaTurnFinished]().FieldByName("StopReason")
	assert.True(t, found, "StopReason")
	assert.Equal(t, contracts.LettaDeltaFieldStopReason, stopReason.Tag.Get("json"))
	errorField, found := reflect.TypeFor[lettaTurnFinished]().FieldByName("Error")
	assert.True(t, found, "Error")
	assert.Equal(t, contracts.LettaDeltaFieldError, errorField.Tag.Get("json"))
}

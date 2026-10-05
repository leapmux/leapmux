package letta

import (
	"reflect"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

// The flat approval_response payload and the command vocabulary are written
// from these names. This test is the Go reader of the contract tables that no
// production call site branches on: a rename in contracts/letta-protocol.json
// moves it here and fails, instead of silently leaving the wire stale.
func TestLettaContractVocabularyMatchesTheWire(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "kind", contracts.LettaReplyKind)
	assert.Equal(t, "approval_response", contracts.LettaReplyKindApprovalResponse)
	assert.Equal(t, "request_id", contracts.LettaReplyRequestID)
	assert.Equal(t, "decision", contracts.LettaReplyDecision)
	assert.Equal(t, "behavior", contracts.LettaReplyBehavior)
	assert.Equal(t, "message", contracts.LettaReplyMessage)

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
	} {
		decoded, found := reflect.TypeFor[lettaDelta]().FieldByName(field)
		assert.True(t, found, field)
		assert.Equal(t, key, decoded.Tag.Get("json"))
	}
	stopReason, found := reflect.TypeFor[lettaTurnFinished]().FieldByName("StopReason")
	assert.True(t, found, "StopReason")
	assert.Equal(t, contracts.LettaDeltaFieldStopReason, stopReason.Tag.Get("json"))

	// The receipt and the response read and write the same fields, so one table pins both structs.
	for _, record := range []reflect.Type{reflect.TypeFor[lettaQuestionReceipt](), reflect.TypeFor[lettaQuestionResponse]()} {
		for field, key := range map[string]string{
			"Type": contracts.LettaQuestionFieldType, "Version": contracts.LettaQuestionFieldVersion,
			"ToolCallID": contracts.LettaQuestionFieldToolCallID, "Questions": contracts.LettaQuestionFieldQuestions,
		} {
			decoded, found := record.FieldByName(field)
			assert.True(t, found, record.Name()+"."+field)
			assert.Equal(t, key, decoded.Tag.Get("json"), record.Name()+"."+field)
		}
	}
	for field, key := range map[string]string{"Status": contracts.LettaQuestionFieldStatus, "Answers": contracts.LettaQuestionFieldAnswers} {
		decoded, found := reflect.TypeFor[lettaQuestionResponse]().FieldByName(field)
		assert.True(t, found, field)
		// The answers are omitted from a dismissal, so the tag carries an option.
		assert.Equal(t, key, strings.Split(decoded.Tag.Get("json"), ",")[0], field)
	}
}

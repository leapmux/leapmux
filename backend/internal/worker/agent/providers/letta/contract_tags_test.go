package letta

import (
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
	assert.Equal(t, "request_id", contracts.LettaReplyRequestID)
	assert.Equal(t, "decision", contracts.LettaReplyDecision)
	assert.Equal(t, "behavior", contracts.LettaReplyBehavior)
	assert.Equal(t, "message", contracts.LettaReplyMessage)
	assert.Equal(t, "updated_input", contracts.LettaReplyUpdatedInput)
	assert.Equal(t, "questions", contracts.LettaReplyQuestions)
	assert.Equal(t, "answers", contracts.LettaReplyAnswers)

	assert.Equal(t, "input", contracts.LettaCommandInput)
	assert.Equal(t, "abort_message", contracts.LettaCommandAbortMessage)
	assert.Equal(t, "runtime_start", contracts.LettaCommandRuntimeStart)
	assert.Equal(t, "sync", contracts.LettaCommandSync)
	assert.Equal(t, "app_server_info", contracts.LettaCommandAppServerInfo)
	assert.Equal(t, "agent_list", contracts.LettaCommandAgentList)
	assert.Equal(t, "conversation_list", contracts.LettaCommandConversationList)
	assert.Equal(t, "list_models", contracts.LettaCommandListModels)
	assert.Equal(t, "update_model", contracts.LettaCommandUpdateModel)

	assert.Equal(t, "pending", contracts.LettaSubagentStatePending)
	assert.Equal(t, "running", contracts.LettaSubagentStateRunning)
	assert.Equal(t, "completed", contracts.LettaSubagentStateCompleted)
	assert.Equal(t, "error", contracts.LettaSubagentStateError)
}

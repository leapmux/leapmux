package droid

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

// A struct tag cannot hold a constant, and the reply envelope Droid reads is
// written from these names. This test is the Go reader of the contract tables
// that no production call site branches on: a rename in contracts/droid-protocol.json
// moves it here and fails, instead of silently leaving the wire stale.
func TestDroidContractVocabularyMatchesTheWire(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "selectedOption", contracts.DroidReplySelectedOption)
	assert.Equal(t, "comment", contracts.DroidReplyComment)
	assert.Equal(t, "answers", contracts.DroidReplyAnswers)
	assert.Equal(t, "cancelled", contracts.DroidReplyCancelled)

	assert.Equal(t, "Edit", contracts.DroidConfirmationTypeEdit)
	assert.Equal(t, "exec", contracts.DroidConfirmationTypeExec)
	assert.Equal(t, "create", contracts.DroidConfirmationTypeCreate)
	assert.Equal(t, "ask_user", contracts.DroidConfirmationTypeAskUser)
	assert.Equal(t, "exit_spec_mode", contracts.DroidConfirmationTypeExitSpecMode)
	assert.Equal(t, "apply_patch", contracts.DroidConfirmationTypeApplyPatch)
	assert.Equal(t, "mcp_tool", contracts.DroidConfirmationTypeMCPTool)
	assert.Equal(t, "sandbox_violation", contracts.DroidConfirmationTypeSandboxViolation)
}

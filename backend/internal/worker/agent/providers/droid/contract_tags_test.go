package droid

import (
	"reflect"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Droid owns these words. The worker writes the reply envelope through generated
// structs, whose tags follow the contract, so this test pins each word to Droid's
// spelling: a rename in contracts/droid-protocol.json fails here instead of
// silently leaving the wire stale. It is also the Go reader of the contract
// tables that no production call site branches on.
func TestDroidContractVocabularyMatchesTheWire(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "selectedOption", contracts.DroidReplySelectedOption)
	assert.Equal(t, "answers", contracts.DroidReplyAnswers)
	assert.Equal(t, "cancelled", contracts.DroidReplyCancelled)

	// The words are Droid's own, in the order Droid's SDK declares them (the
	// ConfirmationType enum of droid 0.233.0). Edit is lowercase there; the
	// display name is the capitalized one.
	assert.Equal(t, "edit", contracts.DroidConfirmationTypeEdit)
	assert.Equal(t, "exec", contracts.DroidConfirmationTypeExec)
	assert.Equal(t, "create", contracts.DroidConfirmationTypeCreate)
	assert.Equal(t, "ask_user", contracts.DroidConfirmationTypeAskUser)
	assert.Equal(t, "exit_spec_mode", contracts.DroidConfirmationTypeExitSpecMode)
	assert.Equal(t, "recommend_mission", contracts.DroidConfirmationTypeRecommendMission)
	assert.Equal(t, "propose_mission", contracts.DroidConfirmationTypeProposeMission)
	assert.Equal(t, "start_mission_run", contracts.DroidConfirmationTypeStartMissionRun)
	assert.Equal(t, "apply_patch", contracts.DroidConfirmationTypeApplyPatch)
	assert.Equal(t, "mcp_tool", contracts.DroidConfirmationTypeMCPTool)
	assert.Equal(t, "sandbox_violation", contracts.DroidConfirmationTypeSandboxViolation)
	assert.Equal(t, "droid_shield_violation", contracts.DroidConfirmationTypeDroidShieldViolation)
	assert.Equal(t, "script", contracts.DroidConfirmationTypeScript)
}

// jsonName is the name that the `json` tag of one field gives.
func jsonName(t *testing.T, structType reflect.Type, field string) string {
	t.Helper()
	structField, found := structType.FieldByName(field)
	require.True(t, found, "%s has no field %s", structType, field)
	name, _, _ := strings.Cut(structField.Tag.Get("json"), ",")
	return name
}

// The worker reads Droid's question request, the questions of the request that it
// published, and the answers of a decision through hand-written struct tags, and a
// struct tag cannot hold a constant. Each tag below carries a word of the
// `askUserFields` table of contracts/droid-protocol.json. The worker writes the same
// words through the generated constants and structs, so a rename that moves the
// contract and leaves a tag behind would break the question form with no failing test.
func TestDroidStructTagsMatchTheContractWords(t *testing.T) {
	t.Parallel()

	request := reflect.TypeOf(droidAskUserRequest{})
	assert.Equal(t, contracts.DroidAskUserFieldToolCallID, jsonName(t, request, "ToolCallID"))
	assert.Equal(t, contracts.DroidAskUserFieldQuestions, jsonName(t, request, "Questions"))
	question, found := request.FieldByName("Questions")
	require.True(t, found)
	asked := question.Type.Elem()
	assert.Equal(t, contracts.DroidAskUserFieldIndex, jsonName(t, asked, "Index"))
	assert.Equal(t, contracts.DroidAskUserFieldQuestion, jsonName(t, asked, "Question"))
	assert.Equal(t, contracts.DroidAskUserFieldOptions, jsonName(t, asked, "Options"))
	assert.Equal(t, contracts.DroidAskUserFieldMultiSelect, jsonName(t, asked, "MultiSelect"))

	stored := reflect.TypeOf(droidStoredQuestion{})
	assert.Equal(t, contracts.DroidAskUserFieldIndex, jsonName(t, stored, "Index"))
	assert.Equal(t, contracts.DroidAskUserFieldQuestion, jsonName(t, stored, "Question"))

	decided := reflect.TypeOf(droidDecidedAnswer{})
	assert.Equal(t, contracts.DroidAskUserFieldIndex, jsonName(t, decided, "Index"))
	assert.Equal(t, contracts.DroidAskUserFieldAnswer, jsonName(t, decided, "Answer"))
}

package qoder

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

// The init.capabilities strings gate runtime behavior. This test is the Go
// reader of that contract table: a rename in contracts/qoder-protocol.json
// moves it here and fails, instead of silently leaving the negotiation stale.
func TestQoderCapabilityVocabularyMatchesTheWire(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "goal_v1", contracts.QoderCapabilityGoalV1)
	assert.Equal(t, "goal_max_turns_v1", contracts.QoderCapabilityGoalMaxTurnsV1)
	assert.Equal(t, "goal_resume_v1", contracts.QoderCapabilityGoalResumeV1)
	assert.Equal(t, "plan_mode_v1", contracts.QoderCapabilityPlanModeV1)
	assert.Equal(t, "interrupt_receipt_v1", contracts.QoderCapabilityInterruptReceiptV1)
	assert.Equal(t, "interrupt_cancel_queued_v1", contracts.QoderCapabilityInterruptCancelQueuedV1)
	assert.Equal(t, "session_rewind_v1", contracts.QoderCapabilitySessionRewindV1)
	assert.Equal(t, "background_tasks_v1", contracts.QoderCapabilityBackgroundTasksV1)
}

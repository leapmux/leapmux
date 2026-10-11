package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

func TestOptionalStorageEnumPreservesAbsenceAndExplicitOrdinals(t *testing.T) {
	t.Parallel()
	assert.Nil(t, workerdb.OptionalStorageEnum(leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED))
	assert.Nil(t, workerdb.OptionalStorageEnum(leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED))
	assert.Nil(t, workerdb.OptionalStorageEnum(leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED))
	assert.Nil(t, workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED))
	assert.Nil(t, workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_UNSPECIFIED))
	for _, value := range []leapmuxv1.MarkType{leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, leapmuxv1.MarkType(-1), leapmuxv1.MarkType(99)} {
		stored := workerdb.OptionalStorageEnum(value)
		require.NotNil(t, stored)
		assert.Equal(t, value, *stored)
		assert.Equal(t, value, workerdb.StorageEnumValue(stored))
	}
	var absent *leapmuxv1.MarkType
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED, workerdb.StorageEnumValue(absent))
}

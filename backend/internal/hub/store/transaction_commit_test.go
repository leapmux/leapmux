package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitOutcomeSurvivesWrappingAndPreservesItsCause(t *testing.T) {
	require.NoError(t, TransactionCommitResult(nil))
	cause := errors.New("commit acknowledgement was lost")
	wrapped := fmt.Errorf("create account: %w", TransactionCommitResult(cause))
	assert.True(t, CommitOutcomeUnknown(wrapped))
	require.ErrorIs(t, wrapped, cause)
	assert.False(t, CommitOutcomeUnknown(cause))
	assert.False(t, CommitOutcomeUnknown(nil))
}

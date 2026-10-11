package agent

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranscriptWriteReceiptRequiresACommittedNewWrite(t *testing.T) {
	t.Parallel()
	var absent *TranscriptWriteReceipt
	absent.RecordStoredWrite(true)
	assert.False(t, absent.ClaimModelReset())
	assert.False(t, absent.ClaimSourceObservation())
	assert.False(t, absent.ClaimContextUsage())
	receipt := NewTranscriptWriteReceipt()
	assert.False(t, receipt.ClaimModelReset())
	receipt.RecordStoredWrite(false)
	assert.False(t, receipt.ClaimModelReset())
	assert.False(t, receipt.ClaimSourceObservation())
	assert.False(t, receipt.ClaimContextUsage())
	receipt.RecordStoredWrite(true)
	receipt.RecordStoredWrite(false)
	assert.True(t, receipt.ClaimModelReset())
	assert.True(t, receipt.ClaimSourceObservation())
	assert.True(t, receipt.ClaimContextUsage())
	assert.False(t, receipt.ClaimModelReset())
	assert.False(t, receipt.ClaimSourceObservation())
	assert.False(t, receipt.ClaimContextUsage())
}

func TestTranscriptWriteReceiptConcurrentConsumersClaimEachEffectOnce(t *testing.T) {
	t.Parallel()
	receipt := NewTranscriptWriteReceipt()
	receipt.RecordStoredWrite(true)
	var resets, observations, usage atomic.Int64
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if receipt.ClaimModelReset() {
				resets.Add(1)
			}
			if receipt.ClaimSourceObservation() {
				observations.Add(1)
			}
			if receipt.ClaimContextUsage() {
				usage.Add(1)
			}
		})
	}
	group.Wait()
	assert.Equal(t, int64(1), resets.Load())
	assert.Equal(t, int64(1), observations.Load())
	assert.Equal(t, int64(1), usage.Load())
}

func TestTranscriptWriteReceiptKeepsTheFirstCommittedMessageSequence(t *testing.T) {
	t.Parallel()
	var absent *TranscriptWriteReceipt
	absent.RecordStoredMessage(1)
	sequence, committed := absent.StoredMessageSequence()
	assert.Zero(t, sequence)
	assert.False(t, committed)
	receipt := NewTranscriptWriteReceipt()
	for _, invalid := range []int64{0, -1, -9223372036854775808} {
		receipt.RecordStoredMessage(invalid)
		sequence, committed = receipt.StoredMessageSequence()
		assert.Zero(t, sequence)
		assert.False(t, committed)
	}
	receipt.RecordStoredMessage(9223372036854775807)
	receipt.RecordStoredMessage(1)
	sequence, committed = receipt.StoredMessageSequence()
	assert.True(t, committed)
	assert.Equal(t, int64(9223372036854775807), sequence)
	assert.False(t, receipt.ClaimModelReset())
	assert.False(t, receipt.ClaimSourceObservation())
	assert.False(t, receipt.ClaimContextUsage())
	assert.False(t, receipt.ClaimOutputCompletion())
	receipt.RecordStoredWrite(false)
	assert.False(t, receipt.ClaimModelReset())
	receipt.RecordStoredWrite(true)
	assert.True(t, receipt.ClaimModelReset())
	assert.True(t, receipt.ClaimSourceObservation())
	assert.True(t, receipt.ClaimContextUsage())
	assert.True(t, receipt.ClaimOutputCompletion())
}

func TestTranscriptWriteReceiptSequenceRemainsImmutableForConcurrentReaders(t *testing.T) {
	t.Parallel()
	receipt := NewTranscriptWriteReceipt()
	receipt.RecordStoredMessage(17)
	var group sync.WaitGroup
	for index := range 16 {
		group.Go(func() {
			receipt.RecordStoredMessage(int64(index + 18))
			sequence, committed := receipt.StoredMessageSequence()
			assert.True(t, committed)
			assert.Equal(t, int64(17), sequence)
		})
	}
	group.Wait()
}

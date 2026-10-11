package agent

import (
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageEnrichmentReceiptClonesItsFirstCommittedResult(t *testing.T) {
	t.Parallel()
	receipt := NewMessageEnrichmentReceipt()
	_, _, _, committed := receipt.CommittedEnrichment()
	assert.False(t, committed)
	input := []byte(`{"extra":0}`)
	receipt.RecordStoredEnrichment(0, 1, input)
	input[0] = '!'
	receipt.RecordStoredEnrichment(1, 2, []byte(`{"replacement":true}`))
	previous, revision, supplemental, committed := receipt.CommittedEnrichment()
	require.True(t, committed)
	assert.Zero(t, previous)
	assert.Equal(t, int64(1), revision)
	assert.Equal(t, []byte(`{"extra":0}`), supplemental)
	supplemental[0] = '!'
	_, _, retained, _ := receipt.CommittedEnrichment()
	assert.Equal(t, []byte(`{"extra":0}`), retained)
}

func TestMessageEnrichmentReceiptRejectsInvalidRevisionAndOverflow(t *testing.T) {
	t.Parallel()
	var absent *MessageEnrichmentReceipt
	absent.RecordStoredEnrichment(0, 1, nil)
	_, _, _, committed := absent.CommittedEnrichment()
	assert.False(t, committed)
	for _, pair := range [][2]int64{{-1, 0}, {0, 0}, {0, 2}, {math.MaxInt64, math.MinInt64}, {math.MaxInt64, math.MaxInt64}} {
		receipt := NewMessageEnrichmentReceipt()
		receipt.RecordStoredEnrichment(pair[0], pair[1], nil)
		_, _, _, committed := receipt.CommittedEnrichment()
		assert.False(t, committed)
	}
	receipt := NewMessageEnrichmentReceipt()
	receipt.RecordStoredEnrichment(math.MaxInt64-1, math.MaxInt64, nil)
	previous, revision, supplemental, committed := receipt.CommittedEnrichment()
	assert.True(t, committed)
	assert.Equal(t, int64(math.MaxInt64-1), previous)
	assert.Equal(t, int64(math.MaxInt64), revision)
	assert.Nil(t, supplemental)
}

func TestMessageEnrichmentReceiptConcurrentReadersKeepOneCompleteResult(t *testing.T) {
	t.Parallel()
	receipt := NewMessageEnrichmentReceipt()
	var group sync.WaitGroup
	for index := range 16 {
		group.Go(func() {
			receipt.RecordStoredEnrichment(int64(index), int64(index+1), []byte{byte(index)})
			previous, revision, supplemental, committed := receipt.CommittedEnrichment()
			if assert.True(t, committed) && assert.Len(t, supplemental, 1) {
				assert.Equal(t, previous+1, revision)
				assert.Equal(t, byte(previous), supplemental[0])
			}
		})
	}
	group.Wait()
}

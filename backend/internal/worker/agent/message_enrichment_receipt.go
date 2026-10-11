package agent

import (
	"math"
	"slices"
	"sync/atomic"
)

// MessageEnrichmentReceipt exposes a committed row before its synchronous publication returns.
type MessageEnrichmentReceipt struct {
	result atomic.Pointer[messageEnrichmentResult]
}

type messageEnrichmentResult struct {
	previousRevision int64
	revision         int64
	supplemental     []byte
}

func NewMessageEnrichmentReceipt() *MessageEnrichmentReceipt { return &MessageEnrichmentReceipt{} }

// RecordStoredEnrichment fixes the first valid committed result and copies its mutable bytes.
func (receipt *MessageEnrichmentReceipt) RecordStoredEnrichment(previousRevision, revision int64, supplemental []byte) {
	if receipt == nil || previousRevision < 0 || previousRevision == math.MaxInt64 || revision != previousRevision+1 {
		return
	}
	result := &messageEnrichmentResult{
		previousRevision: previousRevision, revision: revision, supplemental: slices.Clone(supplemental),
	}
	receipt.result.CompareAndSwap(nil, result)
}

// CommittedEnrichment returns a copy of the first accepted row result.
func (receipt *MessageEnrichmentReceipt) CommittedEnrichment() (previousRevision, revision int64, supplemental []byte, committed bool) {
	if receipt == nil {
		return 0, 0, nil, false
	}
	result := receipt.result.Load()
	if result == nil {
		return 0, 0, nil, false
	}
	return result.previousRevision, result.revision, slices.Clone(result.supplemental), true
}

package agent

import "sync/atomic"

// TranscriptWriteReceipt derives one reset boundary from a committed storage result.
type TranscriptWriteReceipt struct {
	messageSequence atomic.Int64
	created         atomic.Bool
	resetClaimed    atomic.Bool
	sourceClaimed   atomic.Bool
	usageClaimed    atomic.Bool
	outputClaimed   atomic.Bool
}

func NewTranscriptWriteReceipt() *TranscriptWriteReceipt { return &TranscriptWriteReceipt{} }

// RecordStoredMessage fixes the first committed sequence before message publication.
// This data changes no effect eligibility or claim.
func (receipt *TranscriptWriteReceipt) RecordStoredMessage(seq int64) {
	if receipt != nil && seq > 0 {
		receipt.messageSequence.CompareAndSwap(0, seq)
	}
}

// StoredMessageSequence returns the original committed sequence, when one exists.
func (receipt *TranscriptWriteReceipt) StoredMessageSequence() (int64, bool) {
	if receipt == nil {
		return 0, false
	}
	seq := receipt.messageSequence.Load()
	return seq, seq > 0
}

// RecordStoredWrite preserves an accepted write across a rejected outer attempt.
func (receipt *TranscriptWriteReceipt) RecordStoredWrite(created bool) {
	if receipt != nil && created {
		receipt.created.Store(true)
	}
}

// ClaimModelReset permits one reset after a new visible write commits.
func (receipt *TranscriptWriteReceipt) ClaimModelReset() bool {
	return receipt != nil && receipt.created.Load() && receipt.resetClaimed.CompareAndSwap(false, true)
}

// ClaimSourceObservation permits one source observation after a new write commits.
func (receipt *TranscriptWriteReceipt) ClaimSourceObservation() bool {
	return receipt != nil && receipt.created.Load() && receipt.sourceClaimed.CompareAndSwap(false, true)
}

// ClaimContextUsage permits one context-usage read after a new boundary commits.
func (receipt *TranscriptWriteReceipt) ClaimContextUsage() bool {
	return receipt != nil && receipt.created.Load() && receipt.usageClaimed.CompareAndSwap(false, true)
}

// ClaimOutputCompletion permits one tail removal after a new closing row commits.
func (receipt *TranscriptWriteReceipt) ClaimOutputCompletion() bool {
	return receipt != nil && receipt.created.Load() && receipt.outputClaimed.CompareAndSwap(false, true)
}

package agent

import (
	"sync"
	"sync/atomic"
)

// TranscriptWriteReceipt derives one reset boundary from a committed storage result.
type TranscriptWriteReceipt struct {
	orderedMu        sync.Mutex
	ordered          *orderedOutputRecord
	messageSequence  atomic.Int64
	messageAdmission atomic.Uint32
	created          atomic.Bool
	resetClaimed     atomic.Bool
	sourceClaimed    atomic.Bool
	usageClaimed     atomic.Bool
	outputClaimed    atomic.Bool
}

func NewTranscriptWriteReceipt() *TranscriptWriteReceipt { return &TranscriptWriteReceipt{} }

const (
	transcriptAdmissionAccepted uint32 = iota + 1
	transcriptAdmissionRetracted
)

// AdmitMessage reads a revocable frame's receipt at its active queue position.
// Storage failure leaves the admission unset for retry before any effects.
func (receipt *TranscriptWriteReceipt) AdmitMessage(writer TranscriptServices, content MessageContent) (bool, error) {
	if content.IdempotencyKey == "" {
		return true, nil
	}
	switch receipt.messageAdmission.Load() {
	case transcriptAdmissionAccepted:
		return true, nil
	case transcriptAdmissionRetracted:
		return false, nil
	}
	retracted, err := writer.IsMessageRetracted(content, content.IdempotencyKey)
	if err != nil {
		return false, err
	}
	if retracted {
		receipt.messageAdmission.Store(transcriptAdmissionRetracted)
		return false, nil
	}
	receipt.messageAdmission.Store(transcriptAdmissionAccepted)
	return true, nil
}

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

// CapturedPublication returns the original publication and native session of an
// ordered write. A receipt without a prepared ordered write returns false.
// This reads immutable capture data without retaining another payload copy.
func (receipt *TranscriptWriteReceipt) CapturedPublication() (TranscriptPublication, string, bool) {
	if receipt == nil {
		return nil, "", false
	}
	receipt.orderedMu.Lock()
	record := receipt.ordered
	receipt.orderedMu.Unlock()
	if record == nil || !record.ready.Load() || record.captured.write == nil {
		return nil, "", false
	}
	content := record.captured.write.content
	return content.Publication, content.AgentSessionID, true
}

// OrderedWriteComplete reports whether this receipt's ordered operation finished.
// A receipt without an ordered operation returns false.
func (receipt *TranscriptWriteReceipt) OrderedWriteComplete() bool {
	if receipt == nil {
		return false
	}
	receipt.orderedMu.Lock()
	record := receipt.ordered
	receipt.orderedMu.Unlock()
	return record != nil && record.ticket.Done()
}

package providerkit

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ControlCancellation owns exact control instances until the single stdin
// batch settles. A definite failure releases surviving claims without reviving
// requests withdrawn or replaced during delivery.
type ControlCancellation struct {
	process    *JSONRPCProcess
	claims     []controlResponseWrite
	result     <-chan error
	err        error
	once       sync.Once
	settledErr error
}

// QueueControlCancellation reserves one complete framed batch without waiting.
// A non-nil ready signal means another claimed response has not settled;
// the caller releases its state locks, waits, and revalidates before trying again.
// The caller calls Settle outside all state locks when a batch is returned.
func (b *JSONRPCProcess) QueueControlCancellation(sessionID, method string, params json.RawMessage, cancelFirst, preserveAnswerless bool) (*ControlCancellation, <-chan struct{}, error) {
	notification, err := json.Marshal(jsonrpcMessage{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return nil, nil, err
	}
	b.outstandingMu.Lock()
	defer b.outstandingMu.Unlock()
	ids := make([]string, 0, len(b.outstandingControls))
	for requestID, record := range b.outstandingControls {
		if record.sessionID != "" && record.sessionID != sessionID {
			continue
		}
		if record.response != nil {
			return nil, record.response.ready, nil
		}
		if record.cancelAnswer != nil || !preserveAnswerless {
			ids = append(ids, requestID)
		}
	}
	slices.Sort(ids)
	batch := &ControlCancellation{process: b}
	var answers []byte
	for _, requestID := range ids {
		record := b.outstandingControls[requestID]
		if record.cancelAnswer != nil {
			data, err := json.Marshal(jsonrpcResponseMessage{JSONRPC: "2.0", ID: record.wireID, Result: record.cancelAnswer})
			if err != nil {
				return nil, nil, err
			}
			answers = append(answers, b.frameJSONRPCMessage(data)...)
		}
		batch.claims = append(batch.claims, controlResponseWrite{requestID: requestID, record: record})
	}
	for index := range batch.claims {
		write := &batch.claims[index]
		write.claim = newControlResponseClaim(controlResponseCancellation)
		write.record.response = write.claim
	}
	framed := b.frameJSONRPCMessage(notification)
	if cancelFirst {
		framed = append(framed, answers...)
	} else {
		framed = append(answers, framed...)
	}
	batch.result, batch.err = b.enqueueStdinResult(framed)
	return batch, nil, nil
}

// Settle waits for delivery and updates only surviving exact instances. It
// performs sink operations after releasing the registry lock.
func (batch *ControlCancellation) Settle(sink agent.ControlServices) error {
	batch.once.Do(func() { batch.settledErr = batch.settle(sink) })
	return batch.settledErr
}

func (batch *ControlCancellation) settle(sink agent.ControlServices) error {
	err := batch.err
	if err == nil {
		err = <-batch.result
	}
	b := batch.process
	var retired, failed []controlResponseWrite
	b.outstandingMu.Lock()
	for _, write := range batch.claims {
		record := write.record
		if b.outstandingControls[write.requestID] != record {
			finishControlResponse(record, write.claim)
			continue
		}
		if err == nil || errors.Is(err, agent.ErrDeliveryUncertain) {
			record.retired = true
			delete(b.outstandingControls, write.requestID)
			if record.published {
				retired = append(retired, write)
			}
		} else if record.publicationErr != nil {
			delete(b.outstandingControls, write.requestID)
			failed = append(failed, write)
		}
		finishControlResponse(record, write.claim)
	}
	b.outstandingMu.Unlock()
	for _, write := range retired {
		sink.CancelControlRequestInstance(write.requestID, write.record.instanceID)
	}
	for _, write := range failed {
		b.SendErrorResponseDetached(write.record.wireID, -32603, ControlPublicationFailure, "control publication failure")
	}
	return err
}

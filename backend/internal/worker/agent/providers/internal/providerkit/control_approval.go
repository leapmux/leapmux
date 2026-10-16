package providerkit

import (
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type controlApprovalWrite struct {
	requestID string
	record    *outstandingControlRequest
	result    <-chan error
	err       error
}

type controlApprovalBatch struct {
	writes   []controlApprovalWrite
	sink     agent.ControlServices
	complete func(int)
	ready    chan struct{}
}

// ApproveOutstandingControlRequests claims open requests without waiting for delivery.
// The completion receives the number of successful writes. It runs outside all process locks.
// Callers can hold their settings mutex across this call to make authorization and claims atomic.
func (b *JSONRPCProcess) ApproveOutstandingControlRequests(sink agent.ControlServices, complete func(int)) int {
	b.outstandingMu.Lock()
	var writes []controlApprovalWrite
	for id, record := range b.outstandingControls {
		if record.approveAnswer != nil && !record.pending {
			record.pending = true
			writes = append(writes, controlApprovalWrite{requestID: id, record: record})
		}
	}
	b.outstandingMu.Unlock()
	slices.SortFunc(writes, func(a, c controlApprovalWrite) int {
		return cmp.Compare(a.requestID, c.requestID)
	})
	if !b.dispatchControlApprovals(sink, writes, complete) {
		return 0
	}
	return len(writes)
}

// ApproveOutstandingControlRequest claims one request and queues its answer.
func (b *JSONRPCProcess) ApproveOutstandingControlRequest(sink agent.ControlServices, publication PublishedControlRequest) bool {
	requestID := publication.RequestID
	b.outstandingMu.Lock()
	record := b.outstandingControls[requestID]
	if record == nil || record != publication.record || record.pending || record.approveAnswer == nil {
		b.outstandingMu.Unlock()
		return false
	}
	record.pending = true
	b.outstandingMu.Unlock()
	return b.dispatchControlApprovals(sink, []controlApprovalWrite{{requestID: requestID, record: record}}, nil)
}

// ApproveUnpublishedControlRequest normally answers without a browser card.
// A definite delivery failure publishes the original request so the user can still answer it.
func (b *JSONRPCProcess) ApproveUnpublishedControlRequest(sink agent.ControlServices, content []byte, cancelAnswer, answer any) bool {
	return b.ApproveUnpublishedSessionControlRequest(sink, "", content, cancelAnswer, answer)
}

// ApproveUnpublishedSessionControlRequest keeps the session identity if failed delivery requires publication.
func (b *JSONRPCProcess) ApproveUnpublishedSessionControlRequest(sink agent.ControlServices, sessionID string, content []byte, cancelAnswer, answer any) bool {
	wireID, _, found := agent.ExtractJSONRPCID(content)
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !found || !valid {
		return false
	}
	record := b.registerControl(identity, &outstandingControlRequest{payload: content, sessionID: sessionID, cancelAnswer: cancelAnswer, approveAnswer: answer, pending: true, publishOnFailure: true})
	if record == nil {
		return false
	}
	if b.dispatchControlApprovals(sink, []controlApprovalWrite{{requestID: identity.Key, record: record}}, nil) {
		return true
	}
	return false
}

// dispatchControlApprovals reserves completion capacity before it queues any frame.
// Frames enter the existing stdin queue before this call returns, so later frames cannot pass them.
func (b *JSONRPCProcess) dispatchControlApprovals(sink agent.ControlServices, writes []controlApprovalWrite, complete func(int)) bool {
	if len(writes) == 0 {
		return false
	}
	b.approvalMu.Lock()
	defer b.approvalMu.Unlock()
	b.stdinMu.Lock()
	_, closed := b.stdinWriterLocked()
	b.stdinMu.Unlock()
	if b.approvalQueue == nil {
		b.approvalQueue = make(chan *controlApprovalBatch, stdinQueueDepth)
		go b.runControlApprovals(b.approvalQueue, closed)
	}
	batch := &controlApprovalBatch{writes: writes, sink: sink, complete: complete, ready: make(chan struct{})}
	select {
	case <-closed:
		b.releaseControlApprovals(writes)
		return false
	default:
	}
	select {
	case b.approvalQueue <- batch:
	default:
		b.releaseControlApprovals(writes)
		return false
	}
	for i := range batch.writes {
		write := &batch.writes[i]
		data, err := json.Marshal(jsonrpcResponseMessage{JSONRPC: "2.0", ID: write.record.wireID, Result: write.record.approveAnswer})
		if err != nil {
			write.err = err
			continue
		}
		write.result, write.err = b.enqueueStdinResult(b.frameJSONRPCMessage(data))
	}
	close(batch.ready)
	return true
}

func (b *JSONRPCProcess) releaseControlApprovals(writes []controlApprovalWrite) {
	var failed []*outstandingControlRequest
	b.outstandingMu.Lock()
	for _, write := range writes {
		if b.outstandingControls[write.requestID] == write.record {
			if write.record.publishOnFailure {
				// The caller publishes after admission fails. No intermediate open record can enter another sweep.
				delete(b.outstandingControls, write.requestID)
				continue
			}
			write.record.pending = false
			if write.record.publicationErr != nil {
				delete(b.outstandingControls, write.requestID)
				failed = append(failed, write.record)
			}
		}
	}
	b.outstandingMu.Unlock()
	for _, record := range failed {
		b.SendErrorResponseDetached(record.wireID, -32603, ControlPublicationFailure, "control publication failure")
	}
}

// runControlApprovals keeps storage and broadcasts off the stdout reader and stdin writer.
// One queue with fixed capacity owns completion work for every approval in this process.
func (b *JSONRPCProcess) runControlApprovals(queue <-chan *controlApprovalBatch, closed <-chan struct{}) {
	for {
		select {
		case batch := <-queue:
			b.completeControlApprovals(batch)
		case <-closed:
			// Serializing with admission prevents a job from arriving after the final drain.
			b.approvalMu.Lock()
			var remaining []*controlApprovalBatch
		drain:
			for {
				select {
				case batch := <-queue:
					remaining = append(remaining, batch)
				default:
					break drain
				}
			}
			b.approvalMu.Unlock()
			for _, batch := range remaining {
				b.completeControlApprovals(batch)
			}
			return
		}
	}
}

func (b *JSONRPCProcess) completeControlApprovals(batch *controlApprovalBatch) {
	<-batch.ready
	delivered := 0
	for _, write := range batch.writes {
		err := write.err
		if err == nil {
			// Admission and shutdown share stdinMu. Every accepted frame receives a writer receipt.
			err = <-write.result
		}
		b.completeControlApproval(batch.sink, write, err)
		if err == nil {
			delivered++
		}
	}
	if batch.complete != nil {
		batch.complete(delivered)
	}
}

func (b *JSONRPCProcess) completeControlApproval(sink agent.ControlServices, write controlApprovalWrite, err error) {
	b.outstandingMu.Lock()
	record := write.record
	if b.outstandingControls[write.requestID] != record {
		b.outstandingMu.Unlock()
		return
	}
	if err == nil || errors.Is(err, agent.ErrDeliveryUncertain) {
		record.retired = true
		delete(b.outstandingControls, write.requestID)
		b.withdrawGeneration++
		published := record.published
		b.outstandingMu.Unlock()
		if published {
			sink.CancelControlRequestInstance(write.requestID, record.instanceID)
		}
		return
	}
	record.pending = false
	publish := record.publishOnFailure
	record.publishOnFailure = false
	failedPublication := record.publicationErr != nil
	if failedPublication {
		delete(b.outstandingControls, write.requestID)
	}
	b.outstandingMu.Unlock()
	slog.Warn("standing approval did not reach the agent", "agent_id", b.agentID, "request_id", write.requestID, "error", err)
	if failedPublication {
		b.SendErrorResponseDetached(record.wireID, -32603, ControlPublicationFailure, "control publication failure")
	} else if publish {
		b.publishRegisteredControl(sink, write.requestID, record)
	}
}

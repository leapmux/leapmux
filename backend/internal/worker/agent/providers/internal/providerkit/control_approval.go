package providerkit

import (
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type controlApprovalBatch struct {
	writes   []controlResponseWrite
	sink     agent.ControlServices
	complete func(int)
	ready    chan struct{}
}

// ApproveOutstandingControlRequests claims open requests without waiting for delivery.
// The completion receives the number of successful writes. It runs outside all process locks.
// Callers can hold their settings mutex across this call to make authorization and claims atomic.
func (b *JSONRPCProcess) ApproveOutstandingControlRequests(sink agent.ControlServices, complete func(int)) int {
	b.outstandingMu.Lock()
	var writes []controlResponseWrite
	for id, record := range b.outstandingControls {
		if record.approveAnswer != nil && record.response == nil {
			claim := newControlResponseClaim(controlResponseApproval)
			record.response = claim
			writes = append(writes, controlResponseWrite{requestID: id, record: record, claim: claim, answer: record.approveAnswer})
		}
	}
	b.outstandingMu.Unlock()
	slices.SortFunc(writes, func(a, c controlResponseWrite) int {
		return cmp.Compare(a.requestID, c.requestID)
	})
	if !b.dispatchControlApprovals(sink, writes, complete) {
		return 0
	}
	return len(writes)
}

// ApproveOutstandingControlRequest claims one request and queues its answer.
func (b *JSONRPCProcess) ApproveOutstandingControlRequest(sink agent.ControlServices, publication PublishedControlRequest) bool {
	return b.answerPublishedControlRequest(sink, publication, nil, nil)
}

// AnswerPublishedControlRequest queues one explicit answer to the exact publication.
// It refuses an already claimed request. complete runs outside process locks after
// delivery settles. A definite failure leaves the publication answerable.
func (b *JSONRPCProcess) AnswerPublishedControlRequest(sink agent.ControlServices, publication PublishedControlRequest, answer any, complete func(error)) bool {
	if answer == nil {
		return false
	}
	return b.answerPublishedControlRequest(sink, publication, answer, complete)
}

func (b *JSONRPCProcess) answerPublishedControlRequest(sink agent.ControlServices, publication PublishedControlRequest, answer any, complete func(error)) bool {
	requestID := publication.RequestID
	b.outstandingMu.Lock()
	record := b.outstandingControls[requestID]
	if record == nil || record != publication.record || record.response != nil {
		b.outstandingMu.Unlock()
		return false
	}
	// No explicit answer selects this request's registered standing approval.
	if answer == nil {
		answer = record.approveAnswer
	}
	if answer == nil {
		b.outstandingMu.Unlock()
		return false
	}
	claim := newControlResponseClaim(controlResponseApproval)
	record.response = claim
	b.outstandingMu.Unlock()
	return b.dispatchControlApprovals(sink, []controlResponseWrite{{requestID: requestID, record: record, claim: claim, answer: answer, complete: complete}}, nil)
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
	record := b.registerControl(identity, &outstandingControlRequest{payload: content, sessionID: sessionID, cancelAnswer: cancelAnswer, approveAnswer: answer, response: newControlResponseClaim(controlResponseApproval), publishOnFailure: true})
	if record == nil {
		return false
	}
	if b.dispatchControlApprovals(sink, []controlResponseWrite{{requestID: identity.Key, record: record, claim: record.response, answer: answer}}, nil) {
		return true
	}
	return false
}

// dispatchControlApprovals reserves completion capacity before it queues any frame.
// Frames enter the existing stdin queue before this call returns, so later frames cannot pass them.
func (b *JSONRPCProcess) dispatchControlApprovals(sink agent.ControlServices, writes []controlResponseWrite, complete func(int)) bool {
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
		data, err := json.Marshal(jsonrpcResponseMessage{JSONRPC: "2.0", ID: write.record.wireID, Result: write.answer})
		if err != nil {
			write.err = err
			continue
		}
		write.result, write.err = b.enqueueStdinResult(b.frameJSONRPCMessage(data))
	}
	close(batch.ready)
	return true
}

func (b *JSONRPCProcess) releaseControlApprovals(writes []controlResponseWrite) {
	var failed []*outstandingControlRequest
	b.outstandingMu.Lock()
	for _, write := range writes {
		if b.outstandingControls[write.requestID] == write.record {
			if write.record.publishOnFailure {
				// The caller publishes after admission fails. No intermediate open record can enter another sweep.
				delete(b.outstandingControls, write.requestID)
				finishControlResponse(write.record, write.claim)
				continue
			}
			if write.record.publicationErr != nil {
				delete(b.outstandingControls, write.requestID)
				failed = append(failed, write.record)
			}
		}
		finishControlResponse(write.record, write.claim)
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
		if write.complete != nil {
			write.complete(err)
		}
		if err == nil {
			delivered++
		}
	}
	if batch.complete != nil {
		batch.complete(delivered)
	}
}

func (b *JSONRPCProcess) completeControlApproval(sink agent.ControlServices, write controlResponseWrite, err error) {
	b.outstandingMu.Lock()
	record := write.record
	if b.outstandingControls[write.requestID] != record {
		finishControlResponse(record, write.claim)
		b.outstandingMu.Unlock()
		return
	}
	if err == nil || errors.Is(err, agent.ErrDeliveryUncertain) {
		record.retired = true
		delete(b.outstandingControls, write.requestID)
		published := record.published
		finishControlResponse(record, write.claim)
		b.outstandingMu.Unlock()
		if published {
			sink.CancelControlRequestInstance(write.requestID, record.instanceID)
		}
		return
	}
	publish := record.publishOnFailure
	record.publishOnFailure = false
	failedPublication := record.publicationErr != nil
	if failedPublication {
		delete(b.outstandingControls, write.requestID)
	}
	finishControlResponse(record, write.claim)
	b.outstandingMu.Unlock()
	slog.Warn("standing approval did not reach the agent", "agent_id", b.agentID, "request_id", write.requestID, "error", err)
	if failedPublication {
		b.SendErrorResponseDetached(record.wireID, -32603, ControlPublicationFailure, "control publication failure")
	} else if publish {
		b.publishRegisteredControl(sink, write.requestID, record)
	}
}

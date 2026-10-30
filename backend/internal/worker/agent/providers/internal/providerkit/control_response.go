package providerkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type controlResponseOwner uint8

const (
	controlResponseApproval controlResponseOwner = iota + 1
	controlResponseCancellation
	controlResponseUser
)

// A response has one owner and one settlement signal. An absent claim means
// the request is answerable; no separate pending flag can disagree with it.
// outstandingMu protects the claim and its deferred native withdrawal.
type controlResponseClaim struct {
	owner          controlResponseOwner
	ready          chan struct{}
	once           sync.Once
	withdrawalSink agent.ControlServices
}

func newControlResponseClaim(owner controlResponseOwner) *controlResponseClaim {
	switch owner {
	case controlResponseApproval, controlResponseCancellation, controlResponseUser:
		return &controlResponseClaim{owner: owner, ready: make(chan struct{})}
	default:
		panic("invalid control response owner")
	}
}

type controlResponseWrite struct {
	requestID string
	record    *outstandingControlRequest
	claim     *controlResponseClaim
	answer    any
	result    <-chan error
	err       error
	complete  func(error)
}

// The caller holds outstandingMu and settles registry state before waking
// another response writer. The captured claim also survives native withdrawal.
func finishControlResponse(record *outstandingControlRequest, claim *controlResponseClaim) {
	if claim == nil {
		panic("control response has no ownership claim")
	}
	if record.response == claim {
		record.response = nil
	}
	claim.once.Do(func() { close(claim.ready) })
}

func (b *JSONRPCProcess) sendRawControlResponse(data []byte, requireRegistered bool) error {
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Method != nil || !RawPresent(frame.ID) {
		return b.writeRawFrame(data)
	}
	identity, valid := agent.NewControlRequestIdentity(frame.ID)
	if !valid {
		if requireRegistered {
			return ErrControlRequestNotPending
		}
		return b.writeRawFrame(data)
	}
	if b.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	b.outstandingMu.Lock()
	record := b.outstandingControls[identity.Key]
	if record == nil {
		b.outstandingMu.Unlock()
		if requireRegistered {
			return ErrControlRequestNotPending
		}
		return b.writeRawFrame(data)
	}
	if record.response != nil {
		b.outstandingMu.Unlock()
		return ErrControlRequestNotPending
	}
	claim := newControlResponseClaim(controlResponseUser)
	record.response = claim
	// Admission shares the registry lock. Cancellation can never observe an
	// unclaimed response whose bytes have not entered the stdin queue yet.
	result, err := b.enqueueStdinResult(b.frameJSONRPCMessage(append([]byte(nil), data...)))
	b.outstandingMu.Unlock()
	if err == nil {
		err = <-result
	}
	b.settleUserControlResponse(identity.Key, record, claim, err)
	if err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

func (b *JSONRPCProcess) settleUserControlResponse(requestID string, record *outstandingControlRequest, claim *controlResponseClaim, err error) {
	delivered := err == nil || errors.Is(err, agent.ErrDeliveryUncertain)
	b.outstandingMu.Lock()
	failedPublication := false
	if b.outstandingControls[requestID] == record {
		if delivered || record.publicationErr != nil {
			delete(b.outstandingControls, requestID)
			failedPublication = !delivered && record.publicationErr != nil
		}
	}
	var withdrawal agent.ControlServices
	if !delivered && claim.withdrawalSink != nil {
		// A native resolution that raced the failed answer remains authoritative.
		// A successful user answer keeps its accepted card and transcript instead.
		record.retired = true
		if record.published {
			withdrawal = claim.withdrawalSink
		}
	}
	finishControlResponse(record, claim)
	b.outstandingMu.Unlock()
	if withdrawal != nil {
		withdrawal.CancelControlRequestInstance(requestID, record.instanceID)
	}
	if failedPublication {
		b.SendErrorResponseDetached(record.wireID, -32603, ControlPublicationFailure, "control publication failure")
	}
}

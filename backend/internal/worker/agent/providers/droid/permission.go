package droid

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) registerControl(record *droidPendingControl) {
	if record.rpcID == "" {
		return
	}
	if record.instanceID == "" {
		record.instanceID = uuid.NewString()
	}
	record.publicationDone = make(chan struct{})
	a.Mu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*droidPendingControl)
	}
	if a.controls[record.requestID] != nil || a.stopped || a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	if record.sessionID == "" {
		record.sessionID = a.sessionID
	}
	a.controls[record.requestID] = record
	bypass := a.canBypassControlLocked(record)
	if bypass {
		record.ready = true
	}
	a.Mu.Unlock()
	if bypass {
		a.queuePermissionApprovals()
		return
	}
	a.publishRegisteredControl(record)
}

func (a *Agent) canBypassControlLocked(record *droidPendingControl) bool {
	return record.root && record.sessionID == a.sessionID && a.settings.permissionMode == contracts.DroidModeAutoHigh &&
		len(record.automaticReply) > 0 && !record.automaticFailed && !a.stopped && !a.StoppedLocked()
}

func (a *Agent) publishRegisteredControl(record *droidPendingControl) {
	err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: record.sessionID, NativeTurnID: record.nativeTurnID,
		RequestID: record.requestID, InstanceID: record.instanceID, Payload: record.payload,
	})
	a.Mu.Lock()
	current := a.controls[record.requestID] == record
	record.published, record.ready = err == nil, true
	record.publicationOnce.Do(func() { close(record.publicationDone) })
	refuse := current && err != nil && !record.answering
	if refuse {
		delete(a.controls, record.requestID)
	}
	bypass := current && err == nil && a.canBypassControlLocked(record)
	a.Mu.Unlock()
	if !current && err == nil {
		a.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
	if refuse {
		a.respond(record.rpcID, record.cancelAnswer)
	}
	if bypass {
		a.queuePermissionApprovals()
	}
}

func (a *Agent) queuePermissionApprovals() {
	a.Mu.Lock()
	needed := false
	for _, record := range a.controls {
		if record.ready && !record.answering && (a.canBypassControlLocked(record) || !record.published) {
			needed = true
			break
		}
	}
	a.Mu.Unlock()
	if needed {
		a.approvalWork.Run("permissions", a.approvePendingPermissions)
	}
}

func (a *Agent) approvePendingPermissions() {
	a.Mu.Lock()
	claimed := make([]*droidPendingControl, 0)
	publish := make([]*droidPendingControl, 0)
	for _, record := range a.controls {
		if !record.ready || record.answering {
			continue
		}
		if a.canBypassControlLocked(record) {
			record.answering = true
			claimed = append(claimed, record)
		} else if !record.published {
			record.ready = false
			publish = append(publish, record)
		}
	}
	a.Mu.Unlock()
	for _, record := range publish {
		a.publishRegisteredControl(record)
	}
	for _, record := range claimed {
		a.Mu.Lock()
		current := a.controls[record.requestID] == record && record.sessionID == a.sessionID && !a.stopped && !a.StoppedLocked()
		a.Mu.Unlock()
		if !current {
			continue
		}
		err := a.Process.SendRawInput(record.automaticReply, agent.StopContext{})
		a.finishControlAnswer(record, err, true)
		if err != nil {
			slog.Warn("Droid could not approve a pending tool", "request_id", record.requestID, "error", err)
		}
	}
}

var errDroidControlNotPending = fmt.Errorf("%w: the Droid control request is no longer pending", agent.ErrControlRequestNotPending)

// SendRawInput keeps one native RPC answer in flight for each current request.
// Answers to a bound child process route to the matching child by control
// instance ID.
func (a *Agent) SendRawInput(input agent.RawInput) error {
	raw, stop := input.Data, input.Stop
	if isDroidRawInterrupt(raw) {
		return a.Interrupt(stop)
	}
	var env droidEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Type != droidTypeResponse {
		return a.Process.SendRawInput(raw, stop)
	}
	record, child, err := a.routeControlAnswer(env.ID, input.ControlInstanceID)
	if err != nil {
		return err
	}
	if child != nil {
		return child.SendRawInput(input)
	}
	select {
	case <-record.publicationDone:
	case <-a.Context().Done():
		return a.Context().Err()
	case <-a.ProcessDone():
		return a.ProcessExitError()
	}
	a.Mu.Lock()
	if current := a.controls[record.requestID]; current != record {
		a.Mu.Unlock()
		if current != nil && current.instanceID != input.ControlInstanceID {
			return agent.ErrControlInstanceReplaced
		}
		return errDroidControlNotPending
	}
	if !record.published || record.answering || a.stopped || a.StoppedLocked() {
		a.Mu.Unlock()
		return errDroidControlNotPending
	}
	if record.sessionID != a.sessionID {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	record.answering = true
	a.Mu.Unlock()
	err = a.Process.SendRawInput(raw, stop)
	a.finishControlAnswer(record, err, false)
	return err
}

func (a *Agent) routeControlAnswer(rpcID, instanceID string) (*droidPendingControl, *Agent, error) {
	a.Mu.Lock()
	exact, rpcMatch := a.findControlLocked(rpcID, instanceID)
	a.Mu.Unlock()
	if exact != nil {
		return exact, nil, nil
	}
	a.childConnMu.Lock()
	children := make([]*Agent, 0, len(a.childConns))
	for _, connection := range a.childConns {
		select {
		case <-connection.ready:
			if connection.agent != nil {
				children = append(children, connection.agent)
			}
		default:
		}
	}
	a.childConnMu.Unlock()
	var matchedInstanceID string
	if rpcMatch != nil {
		matchedInstanceID = rpcMatch.instanceID
	}
	for _, child := range children {
		child.Mu.Lock()
		cExact, cRPC := child.findControlLocked(rpcID, instanceID)
		child.Mu.Unlock()
		if cExact != nil {
			return nil, child, nil
		}
		if cRPC != nil && matchedInstanceID == "" {
			matchedInstanceID = cRPC.instanceID
		}
	}
	if matchedInstanceID != "" {
		return nil, nil, agent.ControlInstanceRefusal(matchedInstanceID, instanceID)
	}
	return nil, nil, errDroidControlNotPending
}

func (a *Agent) findControlLocked(rpcID, instanceID string) (exact, rpcMatch *droidPendingControl) {
	for _, candidate := range a.controls {
		if candidate.rpcID != rpcID {
			continue
		}
		if rpcMatch == nil {
			rpcMatch = candidate
		}
		if instanceID != "" && candidate.instanceID == instanceID {
			return candidate, rpcMatch
		}
	}
	return nil, rpcMatch
}

func (a *Agent) finishControlAnswer(record *droidPendingControl, err error, automatic bool) {
	a.Mu.Lock()
	current := a.controls[record.requestID] == record
	published := record.published
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	publish := false
	if current {
		if err == nil {
			delete(a.controls, record.requestID)
			record.publicationOnce.Do(func() { close(record.publicationDone) })
		} else if uncertain {
			record.published = false
			record.publicationOnce.Do(func() { close(record.publicationDone) })
		} else {
			record.answering = false
			record.automaticFailed = record.automaticFailed || automatic
			publish = !record.published && record.ready
			if publish {
				record.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if current && published && (err == nil && automatic || uncertain) {
		a.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
	if current && publish {
		a.publishRegisteredControl(record)
	}
}

func (a *Agent) takeControlsLocked(rootOnly bool) []*droidPendingControl {
	var retired []*droidPendingControl
	for id, record := range a.controls {
		if rootOnly && !record.root {
			continue
		}
		if record.published {
			retired = append(retired, record)
		}
		record.publicationOnce.Do(func() { close(record.publicationDone) })
		delete(a.controls, id)
	}
	return retired
}

func (a *Agent) cancelControls(records []*droidPendingControl) {
	for _, record := range records {
		a.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
}

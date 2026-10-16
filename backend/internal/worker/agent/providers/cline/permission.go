package cline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) registerControl(requestID string, control *pendingControl) {
	control.payload = bytes.Clone(control.payload)
	control.instanceID = id.Generate()
	control.publicationDone = make(chan struct{})
	a.Mu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*pendingControl)
	}
	if a.controls[requestID] != nil {
		a.Mu.Unlock()
		return
	}
	a.controls[requestID] = control
	automatic := a.canApproveControlLocked(control)
	if automatic {
		control.ready = true
		control.publicationOnce.Do(func() { close(control.publicationDone) })
	}
	a.Mu.Unlock()
	if automatic {
		a.queuePermissionApprovals()
		return
	}
	a.publishRegisteredControl(requestID, control)
}

func (a *Agent) canApproveControlLocked(control *pendingControl) bool {
	return control.kind == controlApproval && control.toolName != "" && control.toolName != contracts.ClineToolSwitchToActMode &&
		!control.automaticFailed && control.sessionID == a.sessionID && !a.StoppedLocked() &&
		(a.bypassPermissionsLocked() || safeAutoApproveTools[control.toolName])
}

func (a *Agent) publishRegisteredControl(requestID string, control *pendingControl) {
	err := a.sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: control.sessionID, RequestID: requestID,
		InstanceID: control.instanceID, Payload: control.payload, SourceSeq: control.sourceSeq})
	a.Mu.Lock()
	current := a.controls[requestID] == control
	if err == nil && !control.answered {
		control.published = true
	}
	control.ready = true
	control.publicationOnce.Do(func() { close(control.publicationDone) })
	automatic := current && a.canApproveControlLocked(control)
	refuse := current && err != nil && !control.answering && !automatic
	cancel := err == nil && (!current || control.answered) || refuse && control.published
	if refuse {
		delete(a.controls, requestID)
	}
	a.Mu.Unlock()
	if cancel {
		a.sink.CancelControlRequestInstance(requestID, control.instanceID)
	}
	if automatic {
		a.queuePermissionApprovals()
	}
	if !refuse {
		return
	}
	slog.Error("Cline could not publish a control request", "request_id", requestID, "error", err)
	if control.kind == controlApproval {
		reason := fmt.Sprintf("LeapMux could not show the permission request: %v", err)
		a.sendInBackground(func() error {
			return a.respondApproval(contracts.ClineApprovalReply{ApprovalId: requestID, Reason: reason})
		})
	} else {
		reason := fmt.Sprintf("LeapMux could not show the question: %v", err)
		a.sendInBackground(func() error {
			return a.respondCapability(contracts.ClineCapabilityReply{RequestId: requestID, Error: reason})
		})
	}
}

func (a *Agent) queuePermissionApprovals() {
	a.Mu.Lock()
	needed := false
	for _, control := range a.controls {
		if control.ready && !control.answering && (a.canApproveControlLocked(control) || !control.published) {
			needed = true
			break
		}
	}
	a.Mu.Unlock()
	if needed {
		a.sendInBackground(a.approvePendingPermissions)
	}
}

func (a *Agent) approvePendingPermissions() error {
	a.Mu.Lock()
	claimed := make(map[string]*pendingControl)
	publish := make(map[string]*pendingControl)
	for requestID, control := range a.controls {
		if !control.ready || control.answering {
			continue
		}
		if a.canApproveControlLocked(control) {
			control.answering = true
			control.automatic = true
			claimed[requestID] = control
		} else if !control.published {
			control.ready = false
			publish[requestID] = control
		}
	}
	a.Mu.Unlock()
	for requestID, control := range publish {
		a.publishRegisteredControl(requestID, control)
	}
	var failures []error
	for requestID, control := range claimed {
		a.Mu.Lock()
		current := a.controls[requestID] == control && control.sessionID == a.sessionID && !a.StoppedLocked()
		a.Mu.Unlock()
		if !current {
			continue
		}
		err := a.respondApproval(contracts.ClineApprovalReply{ApprovalId: requestID, Approved: true})
		a.finishControlAnswer(requestID, control, err)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (a *Agent) claimPublishedControl(requestID string, kind controlKind) (*pendingControl, error) {
	a.Mu.Lock()
	control := a.controls[requestID]
	a.Mu.Unlock()
	if control == nil || control.kind != kind {
		return nil, errUnknownControl
	}
	select {
	case <-control.publicationDone:
	case <-a.ctx.Done():
		return nil, a.ctx.Err()
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.controls[requestID] != control || !control.published || control.answering || a.StoppedLocked() {
		return nil, errUnknownControl
	}
	if control.sessionID != a.sessionID {
		return nil, agent.ErrInputSessionChanged
	}
	control.answering = true
	control.automatic = false
	return control, nil
}

// sendControlCommand distinguishes an unsent frame from a missing acknowledgement.
func (a *Agent) sendControlCommand(ctx context.Context, name string, payload any) (json.RawMessage, error) {
	requestID, replies, err := a.hub.send(ctx, name, "", payload, nil)
	if err != nil {
		var write *hubWriteError
		if errors.As(err, &write) {
			return nil, fmt.Errorf("%w: %w", agent.ErrDeliveryUncertain, err)
		}
		return nil, err
	}
	defer a.hub.forget(requestID)
	result, err := a.hub.await(ctx, name, replies)
	var refusal *HubCommandError
	if err != nil && !errors.As(err, &refusal) {
		return nil, fmt.Errorf("%w: %w", agent.ErrDeliveryUncertain, err)
	}
	return result, err
}

func (a *Agent) finishControlAnswer(requestID string, control *pendingControl, err error) {
	a.Mu.Lock()
	current := a.controls[requestID] == control
	published, automatic := control.published, control.automatic
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	code, _ := hubErrorCode(err)
	gone := errors.Is(err, errUnknownControl) || code == "approval_not_found"
	publish := false
	if current {
		if gone {
			delete(a.controls, requestID)
		} else if err == nil || uncertain {
			control.answered = true
			control.published = false
		} else {
			control.answering = false
			control.automaticFailed = control.automaticFailed || automatic
			publish = !control.published && control.ready
			if publish {
				control.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if current && published && (automatic && err == nil || uncertain || gone) {
		a.sink.CancelControlRequestInstance(requestID, control.instanceID)
	}
	if current && publish {
		a.publishRegisteredControl(requestID, control)
	}
}

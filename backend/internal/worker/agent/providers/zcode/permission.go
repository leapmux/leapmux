package zcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type pendingControl struct {
	payload         json.RawMessage
	wireID          json.RawMessage
	wireKey         string
	sessionID       string
	ownerSessionID  string
	instanceID      string
	automaticReply  json.RawMessage
	ready           bool
	published       bool
	answering       bool
	answered        bool
	automatic       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

func newPendingControl(payload json.RawMessage) *pendingControl {
	record := &pendingControl{payload: append(json.RawMessage(nil), payload...), instanceID: id.Generate(), publicationDone: make(chan struct{})}
	var stored zcodeControlRequestPayload
	if json.Unmarshal(payload, &stored) != nil {
		return record
	}
	identity, valid := agent.NewControlRequestIdentity(stored.WireID)
	if valid {
		record.wireID = identity.Native
		record.wireKey = identity.Key
	}
	var request zcodePermissionRequest
	if json.Unmarshal(stored.Params, &request) == nil {
		record.sessionID = request.SessionID
		if stored.Method == contracts.ZCodeMethodRequestPermission && request.ToolName != "" && request.ToolName != zcodeControlAskUserQuestion && (zcodeProvider{}).PlanModeControl(request.ToolName) == agent.PlanModeControlNone {
			record.automaticReply = oncePermissionResult(request.Options)
		}
	}
	return record
}

// The native option ID identifies the one-time grant. Its complete response stays provider-owned.
func oncePermissionResult(options []zcodePermissionOption) json.RawMessage {
	var result json.RawMessage
	for _, option := range options {
		if option.OptionID != "allow_once" {
			continue
		}
		var response struct {
			Decision string          `json:"decision"`
			Updates  json.RawMessage `json:"permissionUpdates"`
			Scope    string          `json:"scope"`
		}
		if json.Unmarshal(option.Response, &response) != nil || response.Decision != contracts.ZCodeDecisionAllow || response.Scope != "" && response.Scope != "once" {
			return nil
		}
		updates := bytes.TrimSpace(response.Updates)
		if len(updates) > 0 && !bytes.Equal(updates, []byte("null")) && !bytes.Equal(updates, []byte("[]")) {
			return nil
		}
		if result != nil {
			return nil
		}
		result = append(json.RawMessage(nil), option.Response...)
	}
	return result
}

func (a *Agent) canBypassControlLocked(record *pendingControl) bool {
	_, unresolved := a.unresolvedSettings[agent.OptionIDPermissionMode]
	return !a.StoppedLocked() && a.modeObserved && a.planObserved && !unresolved && a.permissionModeLocked() == contracts.ZCodeModeYolo &&
		record.ownerSessionID == a.sessionID && record.sessionID == a.sessionID && record.wireKey != "" && len(record.automaticReply) > 0 && !record.automaticFailed
}

func (a *Agent) queuePermissionApprovals() {
	a.Mu.Lock()
	needed := false
	for _, record := range a.pendingControls {
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
	claimed := make(map[string]*pendingControl)
	publish := make(map[string]*pendingControl)
	for requestID, record := range a.pendingControls {
		if !record.ready || record.answering {
			continue
		}
		if a.canBypassControlLocked(record) {
			record.answering = true
			record.automatic = true
			claimed[requestID] = record
		} else if !record.published {
			record.ready = false
			publish[requestID] = record
		}
	}
	a.Mu.Unlock()
	for requestID, record := range publish {
		a.publishRegisteredControl(requestID, record)
	}
	for requestID, record := range claimed {
		a.Mu.Lock()
		current := a.pendingControls[requestID] == record && record.sessionID == a.sessionID && !a.StoppedLocked()
		a.Mu.Unlock()
		if !current {
			continue
		}
		err := a.sendZCodeReply(record.wireID, record.automaticReply)
		a.finishControlAnswer(requestID, record, err)
		if err != nil {
			slog.Warn("ZCode could not approve a pending tool", "request_id", requestID, "error", err)
		}
	}
}

func (a *Agent) sendControlOrRaw(data []byte, stop agent.StopContext) error {
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Method != nil || len(frame.ID) == 0 {
		return a.Process.SendRawInput(data, stop)
	}
	identity, valid := agent.NewControlRequestIdentity(frame.ID)
	if !valid {
		return fmt.Errorf("the ZCode control answer has no valid native identity")
	}
	a.Mu.Lock()
	var requestID string
	var record *pendingControl
	for key, candidate := range a.pendingControls {
		if candidate.wireKey == identity.Key {
			requestID, record = key, candidate
			break
		}
	}
	a.Mu.Unlock()
	if record == nil {
		return fmt.Errorf("the ZCode control request is no longer pending")
	}
	select {
	case <-record.publicationDone:
	case <-a.Context().Done():
		return a.Context().Err()
	case <-a.ProcessDone():
		return a.ProcessExitError()
	}
	a.Mu.Lock()
	if a.pendingControls[requestID] != record || !record.published || record.answering || a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("the ZCode control request is no longer pending")
	}
	if record.ownerSessionID != a.sessionID {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	record.answering = true
	record.automatic = false
	a.Mu.Unlock()
	err := a.Process.SendRawInput(data, stop)
	a.finishControlAnswer(requestID, record, err)
	return err
}

func (a *Agent) finishControlAnswer(requestID string, record *pendingControl, err error) {
	a.Mu.Lock()
	current := a.pendingControls[requestID] == record
	published, automatic := record.published, record.automatic
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	publish := false
	if current {
		if err == nil || uncertain {
			record.answered = true
			record.published = false
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
	if current && published && (automatic && err == nil || uncertain) {
		a.sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
	if current && publish {
		a.publishRegisteredControl(requestID, record)
	}
}

func (a *Agent) withdrawPendingControls() {
	a.Mu.Lock()
	canceled := make(map[string]string)
	for requestID, record := range a.pendingControls {
		if record.published && (!record.answering || record.automatic) {
			canceled[requestID] = record.instanceID
		}
		record.publicationOnce.Do(func() { close(record.publicationDone) })
	}
	clear(a.pendingControls)
	a.Mu.Unlock()
	for requestID, instanceID := range canceled {
		a.sink.CancelControlRequestInstance(requestID, instanceID)
	}
	// The hook-trust question belongs to the session too.
	a.workspaceTrust.Retire(a.sink)
}

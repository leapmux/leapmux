package codebuddy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type nativeControl struct {
	payload         []byte
	automaticReply  []byte
	rootPermission  bool
	published       bool
	ready           bool
	answering       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

// handleInboundControlRequest keeps each native request separate from its publication.
func (a *Agent) handleInboundControlRequest(raw []byte) {
	var envelope controlRequestEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.RequestID == "" {
		return
	}
	var request struct {
		Subtype   string          `json:"subtype"`
		AgentID   string          `json:"agent_id"`
		ToolUseID string          `json:"tool_use_id"`
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
	}
	if json.Unmarshal(envelope.Request, &request) != nil {
		return
	}
	record := &nativeControl{payload: append([]byte(nil), raw...), publicationDone: make(chan struct{})}
	a.mu.Lock()
	_, ownTool := a.turnToolCalls[request.ToolUseID]
	record.rootPermission = request.Subtype == contracts.CodebuddyControlRequestSubtypeCanUseTool && request.AgentID == "" && ownTool
	if a.controls == nil {
		a.controls = make(map[string]*nativeControl)
	}
	if a.controls[envelope.RequestID] != nil {
		a.mu.Unlock()
		return
	}
	if record.rootPermission && request.ToolName != "" && request.ToolName != "AskUserQuestion" && (codebuddyProvider{}).PlanModeControl(request.ToolName) == agent.PlanModeControlNone {
		record.automaticReply, _ = json.Marshal(map[string]any{"type": frameTypeControlResponse, "response": map[string]any{"subtype": "success", "request_id": envelope.RequestID, "response": contracts.CodebuddyCanUseToolAnswer{Allowed: true, UpdatedInput: request.Input}}})
	}
	a.controls[envelope.RequestID] = record
	bypass := a.canBypassControlLocked(record)
	if bypass {
		record.ready = true
		record.publicationOnce.Do(func() { close(record.publicationDone) })
	}
	a.mu.Unlock()
	if bypass {
		a.queuePermissionApprovals()
		return
	}
	a.publishRegisteredControl(envelope.RequestID, record)
}

func (a *Agent) canBypassControlLocked(record *nativeControl) bool {
	return a.permissionMode == contracts.CodebuddyModeBypassPermissions && len(record.automaticReply) > 0 && !record.automaticFailed && !a.IsStopped()
}

func (a *Agent) publishRegisteredControl(id string, record *nativeControl) {
	err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: id, Payload: record.payload})
	a.mu.Lock()
	current := a.controls[id] == record
	record.published, record.ready = err == nil, true
	record.publicationOnce.Do(func() { close(record.publicationDone) })
	refuse := current && err != nil && !record.answering
	if refuse {
		delete(a.controls, id)
	}
	bypass := current && err == nil && a.canBypassControlLocked(record)
	a.mu.Unlock()
	if !current && err == nil {
		a.sink.CancelControlRequest(id)
	}
	if bypass {
		a.queuePermissionApprovals()
	}
	if refuse {
		response, _ := json.Marshal(map[string]any{"type": frameTypeControlResponse, "response": map[string]any{"subtype": "error", "request_id": id, "error": providerkit.ControlPublicationFailure}})
		if writeErr := a.Process.SendRawInput(response, agent.StopContext{}); writeErr != nil {
			slog.Warn("CodeBuddy could not refuse an unpublished control", "error", writeErr)
		}
	}
}

func (a *Agent) queuePermissionApprovals() {
	a.mu.Lock()
	needed := false
	for _, record := range a.controls {
		if record.ready && !record.answering && (a.canBypassControlLocked(record) || !record.published) {
			needed = true
			break
		}
	}
	a.mu.Unlock()
	if !needed {
		return
	}
	a.approvalWork.Run("permissions", a.approvePendingPermissions)
}

func (a *Agent) approvePendingPermissions() {
	a.mu.Lock()
	candidates := make(map[string]*nativeControl)
	for id, record := range a.controls {
		if record.ready && !record.answering {
			candidates[id] = record
		}
	}
	a.mu.Unlock()
	for id, record := range candidates {
		claimed, err := a.writeControlAnswer(id, record, record.automaticReply, agent.StopContext{}, true)
		if claimed {
			a.finishControlWrite(id, record, err, true, true)
			if err != nil {
				slog.Warn("CodeBuddy could not approve a pending tool", "request_id", id, "error", err)
			}
			continue
		}
		a.mu.Lock()
		publish := a.controls[id] == record && record.ready && !record.answering && !record.published && !a.canBypassControlLocked(record)
		if publish {
			record.ready = false
		}
		a.mu.Unlock()
		if publish {
			a.publishRegisteredControl(id, record)
		}
	}
}

// Publication and cancellation callbacks run after this function releases the write lock.
func (a *Agent) writeControlAnswer(id string, record *nativeControl, data []byte, stop agent.StopContext, automatic bool) (bool, error) {
	a.permissionWriteMu.Lock()
	defer a.permissionWriteMu.Unlock()
	a.mu.Lock()
	available := a.controls[id] == record && record.ready && !record.answering
	if automatic {
		available = available && a.canBypassControlLocked(record)
	} else {
		available = available && record.published
	}
	if !available {
		a.mu.Unlock()
		return false, fmt.Errorf("the CodeBuddy control request is no longer pending")
	}
	record.answering = true
	a.mu.Unlock()
	return true, a.Process.SendRawInput(data, stop)
}

func (a *Agent) finishControlWrite(id string, record *nativeControl, err error, retireCard, automatic bool) {
	a.mu.Lock()
	current := a.controls[id] == record
	published := record.published
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	publish := false
	if current {
		if err == nil {
			delete(a.controls, id)
		} else if uncertain {
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
	a.mu.Unlock()
	if current && published && (err == nil && retireCard || uncertain) {
		a.sink.CancelControlRequest(id)
	}
	if current && publish {
		a.publishRegisteredControl(id, record)
	}
}

func (a *Agent) withdrawControls() {
	a.mu.Lock()
	var published []string
	for id, record := range a.controls {
		if record.published {
			published = append(published, id)
		}
		record.publicationOnce.Do(func() { close(record.publicationDone) })
	}
	clear(a.controls)
	a.mu.Unlock()
	for _, id := range published {
		a.sink.CancelControlRequest(id)
	}
}

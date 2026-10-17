package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type claudePendingControl struct {
	instanceID string
	sessionID  string
	agentID    string
	toolUseID  string
	// elicitationID identifies the MCP elicitation that an `elicitation`
	// request asks. An `elicitation_complete` frame finds the request by it
	// (see handleElicitationComplete). Empty for every other request.
	elicitationID   string
	payload         []byte
	automaticReply  []byte
	ready           bool
	published       bool
	answering       bool
	answered        bool
	automatic       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

func (a *Agent) claudeCodeHandleControlRequest(content []byte) {
	var envelope struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if json.Unmarshal(content, &envelope) != nil || envelope.RequestID == "" {
		return
	}
	// The optional fields that touch the live state decode one at a time, so a
	// mistyped field costs only itself. A `can_use_tool` is the permission
	// decision that reaches the reader, so the call that it escalates waits on
	// no automatic check any more. An `elicitation` keeps its elicitation id on
	// the record, because a later `elicitation_complete` states the
	// elicitation alone (see handleElicitationComplete).
	fields := parseClaudeControlRequestFields(content)
	if fields.Subtype == claudeControlRequestCanUseTool {
		if sink, ok := a.claudePermissionCheckSink(fields.AgentID); ok {
			clearClaudePermissionCheck(sink, fields.ToolUseID)
		}
	}
	elicitationID := ""
	if fields.Subtype == contracts.MCPElicitationSubtypeClaude {
		elicitationID = fields.ElicitationID
	}
	var request struct {
		Subtype             string          `json:"subtype"`
		ToolName            string          `json:"tool_name"`
		ToolUseID           string          `json:"tool_use_id"`
		AgentID             string          `json:"agent_id"`
		Input               json.RawMessage `json:"input"`
		RequiresInteraction bool            `json:"requires_user_interaction"`
	}
	valid := json.Unmarshal(envelope.Request, &request) == nil
	record := &claudePendingControl{instanceID: id.Generate(), agentID: request.AgentID, toolUseID: request.ToolUseID,
		elicitationID: elicitationID, payload: bytes.Clone(content), publicationDone: make(chan struct{})}
	var input map[string]json.RawMessage
	if valid && request.Subtype == "can_use_tool" && request.ToolName != "" && !request.RequiresInteraction &&
		request.ToolName != ToolNameAskUserQuestion && (claudeProvider{}).PlanModeControl(request.ToolName) == agent.PlanModeControlNone &&
		json.Unmarshal(request.Input, &input) == nil && input != nil {
		record.automaticReply, _ = json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": envelope.RequestID,
			"response": map[string]any{"behavior": "allow", "updatedInput": request.Input}}})
	}
	a.Mu.Lock()
	record.sessionID = a.sessionID
	if a.incomingControls == nil {
		a.incomingControls = make(map[string]*claudePendingControl)
	}
	if existing := a.incomingControls[envelope.RequestID]; existing != nil {
		if existing.answering {
			a.Mu.Unlock()
			return
		}
		if bytes.Equal(existing.payload, content) {
			record = existing
		} else {
			existing.publicationOnce.Do(func() { close(existing.publicationDone) })
		}
	}
	a.incomingControls[envelope.RequestID] = record
	bypass := a.canBypassControlLocked(record)
	if bypass {
		record.ready = true
		record.publicationOnce.Do(func() { close(record.publicationDone) })
	}
	a.Mu.Unlock()
	if bypass {
		a.queuePermissionApprovals()
		return
	}
	a.publishRegisteredControl(envelope.RequestID, record)
}

func (a *Agent) canBypassControlLocked(record *claudePendingControl) bool {
	_, unresolved := a.unresolvedSettings[agent.OptionIDPermissionMode]
	return !a.StoppedLocked() && !unresolved && a.permissionModeLocked() == contracts.ClaudeModeBypassPermissions &&
		record.sessionID == a.sessionID && len(record.automaticReply) > 0 && !record.automaticFailed
}

func (a *Agent) publishRegisteredControl(requestID string, record *claudePendingControl) {
	err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, InstanceID: record.instanceID, AgentSessionID: record.sessionID, Payload: record.payload})
	a.Mu.Lock()
	current := a.incomingControls[requestID] == record
	if err == nil && !record.answered {
		record.published = true
	}
	record.ready = true
	record.publicationOnce.Do(func() { close(record.publicationDone) })
	bypass := current && a.canBypassControlLocked(record)
	refuse := current && err != nil && !record.answering && !bypass
	cancel := err == nil && (!current || record.answered) || refuse && record.published
	if refuse {
		delete(a.incomingControls, requestID)
	}
	a.Mu.Unlock()
	if cancel {
		a.sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
	if bypass {
		a.queuePermissionApprovals()
	}
	if refuse {
		response, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": requestID, "error": providerkit.ControlPublicationFailure}})
		if writeErr := a.Process.SendRawInput(response, agent.StopContext{}); writeErr != nil {
			slog.Warn("Claude Code could not refuse an unpublished control", "request_id", requestID, "error", writeErr)
		}
	}
}

func (a *Agent) queuePermissionApprovals() {
	a.Mu.Lock()
	needed := false
	for _, record := range a.incomingControls {
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
	claimed := make(map[string]*claudePendingControl)
	publish := make(map[string]*claudePendingControl)
	for requestID, record := range a.incomingControls {
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
		current := a.incomingControls[requestID] == record && record.sessionID == a.sessionID && !a.StoppedLocked()
		a.Mu.Unlock()
		if !current {
			continue
		}
		err := a.Process.SendRawInput(record.automaticReply, agent.StopContext{})
		a.finishControlAnswer(requestID, record, err)
		if err != nil {
			slog.Warn("Claude Code could not approve a pending tool", "request_id", requestID, "error", err)
		}
	}
}

// SendRawInput claims control answers and preserves every other native frame.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	var frame struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Type != claudeMsgTypeControlResponse {
		return a.Process.SendRawInput(data, stop)
	}
	a.Mu.Lock()
	record := a.incomingControls[frame.Response.RequestID]
	a.Mu.Unlock()
	if record == nil {
		return fmt.Errorf("the Claude Code control request is no longer pending")
	}
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-record.publicationDone:
	case <-ctx.Done():
		return ctx.Err()
	case <-a.ProcessDone():
		return a.ProcessExitError()
	}
	a.Mu.Lock()
	if a.incomingControls[frame.Response.RequestID] != record || !record.published || record.answering || a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("the Claude Code control request is no longer pending")
	}
	if record.sessionID != a.sessionID {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	record.answering = true
	record.automatic = false
	a.Mu.Unlock()
	err := a.Process.SendRawInput(data, stop)
	a.finishControlAnswer(frame.Response.RequestID, record, err)
	return err
}

func (a *Agent) finishControlAnswer(requestID string, record *claudePendingControl, err error) {
	a.Mu.Lock()
	current := a.incomingControls[requestID] == record
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

// claimElicitation claims the published elicitation request that elicitationID
// identifies, for an answer that the Worker sends. It returns nil when no such
// request waits for an answer: the id is unknown, the reader answered it
// already, or the request belongs to an earlier session.
func (a *Agent) claimElicitation(elicitationID string) (string, *claudePendingControl) {
	if elicitationID == "" {
		return "", nil
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.StoppedLocked() {
		return "", nil
	}
	for requestID, record := range a.incomingControls {
		if record.elicitationID != elicitationID || !record.ready || record.answering || record.answered ||
			record.sessionID != a.sessionID {
			continue
		}
		record.answering = true
		record.automatic = true
		return requestID, record
	}
	return "", nil
}

func (a *Agent) retireControl(requestID string) {
	if requestID == "" {
		return
	}
	a.Mu.Lock()
	record := a.incomingControls[requestID]
	delete(a.incomingControls, requestID)
	published := record != nil && record.published
	if record != nil {
		record.publicationOnce.Do(func() { close(record.publicationDone) })
	}
	a.Mu.Unlock()
	if record == nil {
		a.sink.CancelControlRequest(requestID)
	} else if published {
		a.sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
}

func (a *Agent) takeControlsLocked(match func(*claudePendingControl) bool) map[string]string {
	canceled := make(map[string]string)
	for requestID, record := range a.incomingControls {
		if !match(record) {
			continue
		}
		if record.published {
			canceled[requestID] = record.instanceID
		}
		record.publicationOnce.Do(func() { close(record.publicationDone) })
		delete(a.incomingControls, requestID)
	}
	return canceled
}

func (a *Agent) cancelControls(canceled map[string]string) {
	for requestID, instanceID := range canceled {
		a.sink.CancelControlRequestInstance(requestID, instanceID)
	}
}

func (a *Agent) withdrawControls() {
	a.Mu.Lock()
	canceled := a.takeControlsLocked(func(*claudePendingControl) bool { return true })
	a.Mu.Unlock()
	a.cancelControls(canceled)
}

func (a *Agent) settleToolControls(blocks []contentBlock) {
	completed := make(map[string]bool)
	for _, block := range blocks {
		if block.Type == "tool_result" && block.ToolUseID != "" {
			completed[block.ToolUseID] = true
		}
	}
	if len(completed) == 0 {
		return
	}
	a.Mu.Lock()
	for _, record := range a.incomingControls {
		if completed[record.toolUseID] && record.answering && !record.automatic {
			record.published = false
		}
	}
	canceled := a.takeControlsLocked(func(record *claudePendingControl) bool { return completed[record.toolUseID] })
	a.Mu.Unlock()
	a.cancelControls(canceled)
}

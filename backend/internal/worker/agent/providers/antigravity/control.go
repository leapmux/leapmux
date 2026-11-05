package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const antigravityMaxPlanBytes = 1 << 20

type antigravityControl struct {
	requestID     string
	instanceID    string
	cascadeID     string
	trajectoryID  string
	stepIndex     int
	kind          string
	permission    *permissionInteraction
	payload       []byte
	automatic     bool
	automaticDone bool
	published     bool
}

func (a *Agent) registerInteraction(state *cascadeState, index int, step antigravityStep) {
	kind, err := interactionKind(step.RequestedInteraction)
	if err != nil {
		state.sink.PersistLeapMuxNotification(map[string]any{
			contracts.NotificationFieldType: contracts.NotificationTypeAgentError,
			contracts.NotificationFieldText: "Antigravity CLI raised an interaction LeapMux cannot answer: " + err.Error(),
		})
		if cancelErr := a.rpc.cancel(a.Context(), state.id); cancelErr != nil {
			slog.Warn("antigravity cancel an unknown interaction", "agent_id", a.AgentID(), "error", cancelErr)
		}
		return
	}
	var interaction requestedInteraction
	if err := json.Unmarshal(step.RequestedInteraction, &interaction); err != nil {
		slog.Warn("antigravity decode interaction", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	if existing := state.controls[index]; existing != nil {
		a.Mu.Unlock()
		return
	}
	control := &antigravityControl{
		requestID:  fmt.Sprintf("antigravity:%s:%s:%d", state.id, state.trajectoryID, index),
		instanceID: id.Generate(), cascadeID: state.id, trajectoryID: state.trajectoryID,
		stepIndex: index, kind: kind,
	}
	if kind == "permission" {
		control.permission = interaction.Permission
		control.automatic = a.autoAnswerLocked(a.mode, interaction.Permission)
	}
	state.controls[index] = control
	a.Mu.Unlock()
	if control.automatic {
		a.queuePermissionApprovals()
		return
	}
	a.publishInteraction(state, control)
}

func (a *Agent) publishInteraction(state *cascadeState, control *antigravityControl) {
	a.Mu.Lock()
	if control.published || state.controls[control.stepIndex] != control || state.closed {
		a.Mu.Unlock()
		return
	}
	step := state.steps[control.stepIndex]
	var raw json.RawMessage
	if step != nil {
		raw = step.raw
	}
	a.Mu.Unlock()
	var decoded antigravityStep
	_ = json.Unmarshal(raw, &decoded)
	payload, err := json.Marshal(map[string]any{
		"name": contracts.AntigravityFrameInteraction,
		"request": map[string]any{
			"tool_name":   decoded.Metadata.ToolCall.Name,
			"tool_use_id": decoded.Metadata.ToolCall.ID,
			"input":       decoded.RequestedInteraction,
		},
		"cascadeId":            control.cascadeID,
		"trajectoryId":         control.trajectoryID,
		"stepIndex":            control.stepIndex,
		"requestedInteraction": decoded.RequestedInteraction,
		"toolCall":             decoded.Metadata.ToolCall,
	})
	if err != nil {
		slog.Error("antigravity encode control", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := state.sink.PublishControlRequest(agent.ControlRequest{
		RequestID: control.requestID, InstanceID: control.instanceID, Payload: payload, AgentSessionID: control.cascadeID,
	}); err != nil {
		slog.Error("antigravity publish control", "agent_id", a.AgentID(), "request_id", control.requestID, "error", err)
		return
	}
	a.Mu.Lock()
	control.payload = payload
	control.published = true
	a.Mu.Unlock()
}

func (a *Agent) withdrawInteraction(state *cascadeState, index int) {
	a.Mu.Lock()
	control := state.controls[index]
	delete(state.controls, index)
	a.Mu.Unlock()
	if control != nil && control.published {
		state.sink.CancelControlRequestInstance(control.requestID, control.instanceID)
	}
}

func (a *Agent) retireAllInteractions(state *cascadeState) {
	a.Mu.Lock()
	controls := make([]*antigravityControl, 0, len(state.controls))
	for _, control := range state.controls {
		controls = append(controls, control)
	}
	clear(state.controls)
	a.Mu.Unlock()
	for _, control := range controls {
		if control.published {
			state.sink.CancelControlRequestInstance(control.requestID, control.instanceID)
		}
	}
}

func knownPermissionAction(action string) bool {
	switch action {
	case contracts.AntigravityPermissionActionCommand, contracts.AntigravityPermissionActionReadFile,
		contracts.AntigravityPermissionActionWriteFile, contracts.AntigravityPermissionActionMcp:
		return true
	default:
		return false
	}
}

func (a *Agent) autoAnswerLocked(mode string, permission *permissionInteraction) bool {
	if permission == nil || !knownPermissionAction(permission.Resource.Action) {
		return false
	}
	switch mode {
	case contracts.AntigravityModeBypass:
		return true
	case contracts.AntigravityModeAcceptEdits:
		return permission.Resource.Action == contracts.AntigravityPermissionActionWriteFile && pathWithinWorkspace(a.workspace, permission.Resource.Target)
	default:
		return false
	}
}

func pathWithinWorkspace(workspace, target string) bool {
	if workspace == "" || target == "" {
		return false
	}
	resolved, err := resolveExistingPath(target)
	if err != nil {
		return false
	}
	resolvedWorkspace, err := resolveExistingPath(workspace)
	if err == nil {
		workspace = resolvedWorkspace
	}
	workspace = filepath.Clean(workspace)
	resolved = filepath.Clean(resolved)
	if resolved == workspace {
		return false
	}
	return strings.HasPrefix(resolved, workspace+string(filepath.Separator))
}

func resolveExistingPath(path string) (string, error) {
	path = filepath.Clean(path)
	remainder := ""
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, remainder), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

func (a *Agent) queuePermissionApprovals() {
	a.approvalWork.Run("permissions", a.approvePendingPermissions)
}

func (a *Agent) approvePendingPermissions() {
	approved := 0
	for {
		a.Mu.Lock()
		var next *cascadeState
		var control *antigravityControl
		for _, state := range a.cascades {
			for _, candidate := range state.controls {
				if candidate.kind == "permission" && candidate.automatic && !candidate.automaticDone {
					candidate.automaticDone = true
					next, control = state, candidate
					break
				}
			}
			if next != nil {
				break
			}
		}
		a.Mu.Unlock()
		if next == nil {
			break
		}
		err := a.rpc.handleInteraction(a.Context(), next.id, map[string]any{
			"trajectoryId": control.trajectoryID, "stepIndex": control.stepIndex,
			"permission": map[string]any{"allow": true, "scope": "PERMISSION_SCOPE_ONCE"},
		})
		err = providerkit.ClassifyHTTPDeliveryError("answer the Antigravity permission", err)
		if err == nil || isInputNotRegistered(err) {
			approved++
			continue
		}
		if errors.Is(err, agent.ErrDeliveryUncertain) {
			break
		}
		slog.Warn("antigravity automatic approval refused; publishing the request", "agent_id", a.AgentID(), "request_id", control.requestID, "error", err)
		a.publishInteraction(next, control)
	}
	if approved > 0 {
		a.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypePendingRequestsApproved})
	}
}

func (a *Agent) executeControlReply(data []byte) error {
	var reply struct {
		CascadeID   string         `json:"cascadeId"`
		Interaction map[string]any `json:"interaction"`
	}
	if err := json.Unmarshal(data, &reply); err != nil || reply.CascadeID == "" || reply.Interaction == nil {
		return fmt.Errorf("the Antigravity control answer is malformed")
	}
	var address struct {
		TrajectoryID string `json:"trajectoryId"`
		StepIndex    int    `json:"stepIndex"`
	}
	interactionBytes, err := json.Marshal(reply.Interaction)
	if err != nil {
		return fmt.Errorf("the Antigravity control answer has a malformed interaction: %w", err)
	}
	if err := json.Unmarshal(interactionBytes, &address); err != nil {
		return fmt.Errorf("the Antigravity control answer has a malformed interaction address: %w", err)
	}
	a.Mu.Lock()
	state, known := a.cascades[reply.CascadeID]
	pending := known && state.trajectoryID == address.TrajectoryID && state.controls[address.StepIndex] != nil
	a.Mu.Unlock()
	if !known {
		return fmt.Errorf("the Antigravity control answer addresses an unknown cascade")
	}
	err = a.rpc.handleInteraction(a.Context(), reply.CascadeID, reply.Interaction)
	if err != nil {
		slog.Warn("antigravity answer interaction failed", "agent_id", a.AgentID(), "cascade_id", reply.CascadeID, "trajectory_id", address.TrajectoryID, "step_index", address.StepIndex, "pending", pending, "error", err)
	} else {
		slog.Info("antigravity interaction answered", "agent_id", a.AgentID(), "cascade_id", reply.CascadeID, "trajectory_id", address.TrajectoryID, "step_index", address.StepIndex)
	}
	if isInputNotRegistered(err) && !pending {
		return nil
	}
	return err
}

func (a *Agent) recordPlanStep(state *cascadeState, step antigravityStep) {
	a.Mu.Lock()
	mode := a.mode
	a.Mu.Unlock()
	if mode != contracts.AntigravityModePlan || step.Metadata.ToolCall.Name != contracts.AntigravityToolWriteFile && step.Metadata.ToolCall.Name != contracts.AntigravityToolEditFile {
		return
	}
	var arguments struct {
		Path             string `json:"Path"`
		ArtifactMetadata struct {
			RequestFeedback bool `json:"RequestFeedback"`
		} `json:"ArtifactMetadata"`
	}
	if json.Unmarshal([]byte(step.Metadata.ToolCall.ArgumentsJSON), &arguments) != nil || !arguments.ArtifactMetadata.RequestFeedback || arguments.Path == "" {
		return
	}
	a.Mu.Lock()
	state.planPath = arguments.Path
	a.Mu.Unlock()
}

func (a *Agent) maybePublishPlan(state *cascadeState) {
	a.Mu.Lock()
	path := state.planPath
	state.planPath = ""
	mode := a.mode
	a.Mu.Unlock()
	if path == "" || mode != contracts.AntigravityModePlan {
		return
	}
	text, err := a.readPlanFile(state.id, path)
	if err != nil {
		slog.Warn("antigravity plan file is not eligible for approval", "agent_id", a.AgentID(), "error", err)
		return
	}
	compressed, compression := msgcodec.Compress(text)
	state.sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(string(text)))
	a.Mu.Lock()
	superseded := a.planPromptRequestID
	requestID := "antigravity-plan:" + id.Generate()
	a.planPromptRequestID = requestID
	a.Mu.Unlock()
	if superseded != "" {
		state.sink.CancelControlRequest(superseded)
	}
	payload, _ := json.Marshal(map[string]any{
		"name":    contracts.AntigravityFrameInteraction,
		"request": map[string]any{"tool_name": contracts.AntigravityControlToolPlanReview, "input": map[string]any{contracts.AntigravityControlPayloadPlanContent: string(text)}},
	})
	if err := state.sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, InstanceID: id.Generate(), Payload: payload, AgentSessionID: state.id}); err != nil {
		slog.Error("antigravity publish plan review", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) readPlanFile(cascadeID, path string) ([]byte, error) {
	base := filepath.Join(a.homeDir, ".gemini", "antigravity-cli", "brain", cascadeID)
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resolved, filepath.Clean(resolvedBase)+string(filepath.Separator)) {
		return nil, fmt.Errorf("the plan path is outside brain/%s", cascadeID)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > antigravityMaxPlanBytes {
		return nil, fmt.Errorf("the plan exceeds %d bytes", antigravityMaxPlanBytes)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	if len(content) > antigravityMaxPlanBytes {
		return nil, fmt.Errorf("the plan exceeds %d bytes", antigravityMaxPlanBytes)
	}
	return content, nil
}

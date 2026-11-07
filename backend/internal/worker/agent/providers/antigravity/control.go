package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const antigravityMaxPlanBytes = 1 << 20

type antigravityControl struct {
	requestID        string
	instanceID       string
	cascadeID        string
	trajectoryID     string
	stepIndex        int
	kind             string
	permission       *permissionInteraction
	approvalClaimed  bool
	published        bool
	publicationOwner *agent.OutputOwner
	publication      *agent.OutputTicket
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
		automatic := existing.kind == "permission" && a.autoAnswerLocked(a.mode, existing.permission)
		retryPublication := existing.publication != nil || !automatic && !existing.approvalClaimed
		retryApproval := automatic && !existing.approvalClaimed
		a.Mu.Unlock()
		if retryPublication {
			a.publishInteraction(state, existing)
		}
		if retryApproval {
			a.queuePermissionApprovals()
		}
		return
	}
	control := &antigravityControl{
		requestID:  fmt.Sprintf("antigravity:%s:%s:%d", state.id, state.trajectoryID, index),
		instanceID: id.Generate(), cascadeID: state.id, trajectoryID: state.trajectoryID,
		stepIndex: index, kind: kind,
	}
	if kind == "permission" {
		control.permission = interaction.Permission
	}
	state.controls[index] = control
	automatic := kind == "permission" && a.autoAnswerLocked(a.mode, control.permission)
	a.Mu.Unlock()
	if automatic {
		a.queuePermissionApprovals()
		return
	}
	a.publishInteraction(state, control)
}

func (a *Agent) publishInteraction(state *cascadeState, control *antigravityControl) {
	a.Mu.Lock()
	if control.published || !a.controlCurrentLocked(state, control) {
		a.Mu.Unlock()
		return
	}
	if pending := control.publication; pending != nil {
		a.Mu.Unlock()
		a.flushControlPublications(pending.Queue())
		return
	}
	step := state.steps[control.stepIndex]
	var raw json.RawMessage
	if step != nil {
		raw = step.raw
	}
	a.Mu.Unlock()
	var decoded antigravityStep
	if err := json.Unmarshal(raw, &decoded); err != nil {
		slog.Error("antigravity decode the control step", "agent_id", a.AgentID(), "error", err)
		return
	}
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
	a.queueControlPublication(state, control, agent.ControlRequest{
		RequestID: control.requestID, InstanceID: control.instanceID, Payload: payload, AgentSessionID: control.cascadeID,
	})
}

func (a *Agent) controlCurrentLocked(state *cascadeState, control *antigravityControl) bool {
	if a.IsDiscardingOutput() || state.closed || a.cascades[state.id] != state || state.generation != a.generation {
		return false
	}
	if control.kind == "plan" {
		return state.planControl == control
	}
	return state.controls[control.stepIndex] == control && state.trajectoryID == control.trajectoryID
}

func (a *Agent) queueControlPublication(state *cascadeState, control *antigravityControl, request agent.ControlRequest) {
	queue := state.sink.OutputQueue()
	a.Mu.Lock()
	if control.published || !a.controlCurrentLocked(state, control) {
		a.Mu.Unlock()
		return
	}
	if control.publication != nil {
		a.Mu.Unlock()
		a.flushControlPublications(queue)
		return
	}
	owner := agent.NewOutputOwner()
	control.publicationOwner = owner
	control.publication = queue.Append(agent.OutputWrite{
		Owner: owner, ScopeID: request.RequestID, RetainedBytes: len(request.Payload), Label: "publish the Antigravity control request",
		Run: func() error {
			a.Mu.Lock()
			current := a.controlCurrentLocked(state, control)
			a.Mu.Unlock()
			if !current {
				a.finishControlPublication(control, owner, false)
				return nil
			}
			if err := state.sink.PublishControlRequest(request); err != nil {
				return err
			}
			a.Mu.Lock()
			current = a.controlCurrentLocked(state, control)
			if current {
				control.published = true
			}
			a.Mu.Unlock()
			a.finishControlPublication(control, owner, current)
			if !current {
				state.sink.CancelControlRequestInstance(request.RequestID, request.InstanceID)
			}
			return nil
		},
	})[0]
	a.Mu.Unlock()
	a.flushControlPublications(queue)
}

func (a *Agent) finishControlPublication(control *antigravityControl, owner *agent.OutputOwner, published bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if control.publicationOwner == owner {
		control.publicationOwner = nil
		control.publication = nil
		control.published = published
	}
}

func (a *Agent) flushControlPublications(queue *agent.OutputQueue) {
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Error("antigravity retain control publication for retry", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireControl(state *cascadeState, control *antigravityControl) {
	if control == nil {
		return
	}
	a.Mu.Lock()
	owner, pending, published := control.publicationOwner, control.publication, control.published
	control.publicationOwner, control.publication = nil, nil
	a.Mu.Unlock()
	if owner != nil && pending != nil {
		pending.Queue().ClearOwner(owner)
	}
	if published {
		state.sink.CancelControlRequestInstance(control.requestID, control.instanceID)
	}
}

func (a *Agent) withdrawInteraction(state *cascadeState, index int) {
	a.Mu.Lock()
	control := state.controls[index]
	delete(state.controls, index)
	a.Mu.Unlock()
	a.retireControl(state, control)
}

func (a *Agent) retireAllInteractions(state *cascadeState) {
	a.Mu.Lock()
	controls := make([]*antigravityControl, 0, len(state.controls))
	for _, control := range state.controls {
		controls = append(controls, control)
	}
	if state.planControl != nil {
		controls = append(controls, state.planControl)
		state.planControl = nil
	}
	clear(state.controls)
	a.Mu.Unlock()
	for _, control := range controls {
		a.retireControl(state, control)
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
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return false
	}
	resolvedWorkspace, err = filepath.Abs(resolvedWorkspace)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(resolvedWorkspace, resolved)
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func resolveExistingPath(path string) (string, error) {
	// Cleaning an unresolved path can remove a symlink before the operating
	// system follows it. Preserve parent components until the prefix resolves.
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			slices.Reverse(suffix)
			return filepath.Join(append([]string{resolved}, suffix...)...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		path = trimUnresolvedPathSeparators(path)
		if _, statErr := os.Lstat(path); statErr == nil {
			// A dangling symlink exists even though its destination does not.
			return "", err
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent, component := filepath.Split(path)
		if component == "" || component == "." || component == ".." {
			return "", err
		}
		suffix = append(suffix, component)
		if parent == "" {
			parent = "."
		}
		path = parent
	}
}

func trimUnresolvedPathSeparators(path string) string {
	minimum := len(filepath.VolumeName(path))
	if len(path) > minimum && os.IsPathSeparator(path[minimum]) {
		minimum++
	}
	for len(path) > minimum && os.IsPathSeparator(path[len(path)-1]) {
		path = path[:len(path)-1]
	}
	return path
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
		type manualRequest struct {
			state   *cascadeState
			control *antigravityControl
		}
		var manual []manualRequest
		for _, state := range a.cascades {
			for _, candidate := range state.controls {
				if candidate.kind != "permission" || candidate.approvalClaimed || !a.controlCurrentLocked(state, candidate) {
					continue
				}
				if !a.autoAnswerLocked(a.mode, candidate.permission) {
					manual = append(manual, manualRequest{state: state, control: candidate})
					continue
				}
				if next == nil {
					candidate.approvalClaimed = true
					next, control = state, candidate
				}
			}
		}
		a.Mu.Unlock()
		for _, request := range manual {
			a.publishInteraction(request.state, request.control)
		}
		if next == nil {
			break
		}
		err := a.rpc.handleInteraction(a.Context(), next.id, map[string]any{
			"trajectoryId": control.trajectoryID, "stepIndex": control.stepIndex,
			"permission": map[string]any{"allow": true, "scope": "PERMISSION_SCOPE_ONCE"},
		})
		if err == nil {
			approved++
			continue
		}
		a.Mu.Lock()
		current := a.controlCurrentLocked(next, control)
		a.Mu.Unlock()
		if !current {
			continue
		}
		if errors.Is(err, agent.ErrDeliveryUncertain) {
			next.sink.PersistLeapMuxNotification(map[string]any{
				contracts.NotificationFieldType: contracts.NotificationTypeAgentError,
				contracts.NotificationFieldText: "Antigravity did not confirm the automatic permission answer. Review the pending request before answering again.",
			})
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
	if !known {
		a.Mu.Unlock()
		return fmt.Errorf("the Antigravity control answer addresses an unknown cascade")
	}
	control := state.controls[address.StepIndex]
	if control == nil || !a.controlCurrentLocked(state, control) || control.trajectoryID != address.TrajectoryID || a.streamCtx.Err() != nil {
		a.Mu.Unlock()
		return nil
	}
	generationCtx := a.streamCtx
	a.Mu.Unlock()
	err = a.rpc.handleInteraction(generationCtx, reply.CascadeID, reply.Interaction)
	a.Mu.Lock()
	pending := control != nil && a.cascades[reply.CascadeID] == state && state.generation == a.generation &&
		!state.closed && state.trajectoryID == address.TrajectoryID && state.controls[address.StepIndex] == control
	a.Mu.Unlock()
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
	if a.IsDiscardingOutput() {
		return
	}
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return
	}
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
	if !a.acceptsCascadeUpdate(state) {
		return
	}
	state.sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(string(text)))
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed || a.IsDiscardingOutput() {
		a.Mu.Unlock()
		return
	}
	superseded := state.planControl
	control := &antigravityControl{requestID: "antigravity-plan:" + id.Generate(), instanceID: id.Generate(), cascadeID: state.id, kind: "plan"}
	state.planControl = control
	a.Mu.Unlock()
	a.retireControl(state, superseded)
	payload, _ := json.Marshal(map[string]any{
		"name":    contracts.AntigravityFrameInteraction,
		"request": map[string]any{"tool_name": contracts.AntigravityControlToolPlanReview, "input": map[string]any{contracts.AntigravityControlPayloadPlanContent: string(text)}},
	})
	a.queueControlPublication(state, control, agent.ControlRequest{RequestID: control.requestID, InstanceID: control.instanceID, Payload: payload, AgentSessionID: state.id})
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
	return readPlanContent(file)
}

func readPlanContent(file *os.File) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, antigravityMaxPlanBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > antigravityMaxPlanBytes {
		return nil, fmt.Errorf("the plan exceeds %d bytes", antigravityMaxPlanBytes)
	}
	return content, nil
}

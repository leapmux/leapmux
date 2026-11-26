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

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const antigravityMaxPlanBytes = 1 << 20

type antigravityControlKind uint8

const (
	antigravityControlPermission antigravityControlKind = iota + 1
	antigravityControlQuestion
	antigravityControlPlan
)

// antigravityControl is one request that the Worker publishes for a cascade:
// one interaction that a waiting step requests, or the plan review that the
// Worker raises itself (antigravityControlPlan).
type antigravityControl struct {
	requestID    string
	instanceID   string
	cascadeID    string
	trajectoryID string
	executionID  string
	stepIndex    int
	kind         antigravityControlKind
	// payload is the request that the Worker stores and shows.
	payload []byte
	// interaction and completedInteractions identify the interaction of the
	// step that the control answers: the requested interaction, and the
	// number of interactions that the step completed before it. A step that
	// requests another interaction needs another control (registerInteraction).
	interaction           json.RawMessage
	completedInteractions int
	// permission is the requested permission that the automatic approval
	// policy reads. It is nil for a question, and for a permission whose
	// resource LeapMux cannot read, which only the user answers.
	permission       *permissionInteraction
	approvalClaimed  bool
	published        bool
	publicationOwner *agent.OutputOwner
	publication      *agent.OutputTicket
	steerOwner       *agent.OutputOwner
	steerBinding     *agent.OutputTicket
}

// newInteractionControl builds the control that answers the interaction that
// a waiting step requests. It fails for an interaction that LeapMux cannot
// answer, and for a step whose trajectory the cascade's stream has not
// stated yet, because an answer must state that trajectory.
func newInteractionControl(cascadeID, trajectoryID string, index int, step antigravityStep) (*antigravityControl, error) {
	kind, err := interactionKind(step.RequestedInteraction)
	if err != nil {
		return nil, err
	}
	address := antigravityAddress{cascadeID: cascadeID, trajectoryID: trajectoryID, stepIndex: index}
	switch {
	case trajectoryID == "":
		return nil, errors.New("the language server did not state the trajectory (the step history) of the step, and an answer must state it")
	case !address.valid():
		return nil, fmt.Errorf("an answer cannot state the address %q of the step", address.requestID())
	}
	payload, err := json.Marshal(map[string]any{
		"name": contracts.AntigravityFrameInteraction,
		"request": map[string]any{
			"tool_name":   step.Metadata.ToolCall.Name,
			"tool_use_id": step.Metadata.ToolCall.ID,
			"input":       step.RequestedInteraction,
		},
		"cascadeId":            cascadeID,
		"trajectoryId":         trajectoryID,
		"stepIndex":            index,
		"requestedInteraction": step.RequestedInteraction,
		"toolCall":             step.Metadata.ToolCall,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the request: %w", err)
	}
	control := &antigravityControl{
		requestID: address.requestID(), instanceID: id.Generate(), cascadeID: cascadeID, trajectoryID: trajectoryID,
		executionID: step.Metadata.ExecutionID,
		stepIndex:   index, kind: kind, payload: payload,
		interaction: step.RequestedInteraction, completedInteractions: len(step.CompletedInteractions),
	}
	if kind == antigravityControlPermission {
		// The prompt reads a permission leniently (ReadControlRequest), so a
		// permission that the policy cannot read is still published, for the
		// user to answer.
		var interaction struct {
			Permission *permissionInteraction `json:"permission"`
		}
		if json.Unmarshal(step.RequestedInteraction, &interaction) == nil {
			control.permission = interaction.Permission
		}
	}
	return control, nil
}

// answersSameInteraction reports whether the control answers the same
// interaction of the same step as other. A nil control answers none.
func (c *antigravityControl) answersSameInteraction(other *antigravityControl) bool {
	return c != nil && c.trajectoryID == other.trajectoryID && c.stepIndex == other.stepIndex &&
		c.executionID == other.executionID &&
		c.completedInteractions == other.completedInteractions && agent.JSONCanonicalEqual(c.interaction, other.interaction)
}

func (c *antigravityControl) matchesStep(trajectoryID string, index int, step antigravityStep) bool {
	return c != nil && c.trajectoryID == trajectoryID && c.stepIndex == index &&
		c.executionID == step.Metadata.ExecutionID &&
		c.completedInteractions == len(step.CompletedInteractions) &&
		agent.JSONCanonicalEqual(c.interaction, step.RequestedInteraction)
}

// registerInteraction registers the interaction that a waiting step requests.
// A repeated frame of a registered interaction retries what its control has
// not done yet. Any other interaction replaces the step's control, because the
// language server no longer waits for an answer to the earlier one.
func (a *Agent) registerInteraction(state *cascadeState, index int, step antigravityStep) {
	a.Mu.Lock()
	trajectoryID := state.trajectoryID
	existing := state.controls[index]
	mode := a.mode
	a.Mu.Unlock()
	if existing.matchesStep(trajectoryID, index, step) {
		automatic := existing.kind == antigravityControlPermission && a.autoAnswer(mode, existing.permission)
		a.Mu.Lock()
		if state.controls[index] == existing && a.mode == mode {
			retryPublication := existing.publication != nil || existing.steerBinding != nil || !automatic && !existing.approvalClaimed
			retryApproval := automatic && !existing.approvalClaimed
			a.Mu.Unlock()
			if retryPublication {
				a.publishControl(state, existing)
			}
			if retryApproval {
				a.queuePermissionApprovals()
			}
			return
		}
		a.Mu.Unlock()
	}

	var control *antigravityControl
	var err error
	for {
		a.Mu.Lock()
		trajectoryID = state.trajectoryID
		mode = a.mode
		a.Mu.Unlock()
		if control == nil || control.trajectoryID != trajectoryID {
			control, err = newInteractionControl(state.id, trajectoryID, index, step)
			if err != nil {
				a.refuseInteraction(state, index, err)
				return
			}
		}
		automatic := control.kind == antigravityControlPermission && a.autoAnswer(mode, control.permission)
		a.Mu.Lock()
		if state.trajectoryID != trajectoryID || a.mode != mode {
			a.Mu.Unlock()
			continue
		}
		existing = state.controls[index]
		if existing.answersSameInteraction(control) {
			retryPublication := existing.publication != nil || existing.steerBinding != nil || !automatic && !existing.approvalClaimed
			retryApproval := automatic && !existing.approvalClaimed
			a.Mu.Unlock()
			if retryPublication {
				a.publishControl(state, existing)
			}
			if retryApproval {
				a.queuePermissionApprovals()
			}
			return
		}
		state.controls[index] = control
		a.Mu.Unlock()
		a.retireControl(state, existing)
		if automatic {
			a.queuePermissionApprovals()
			return
		}
		a.publishControl(state, control)
		return
	}
}

// refuseInteraction handles a waiting step that LeapMux cannot answer. It
// withdraws the control of the step's earlier interaction, reports the fault
// to the user, and cancels the cascade. Otherwise the language server would
// wait for an answer that never comes, and the user would not see why.
func (a *Agent) refuseInteraction(state *cascadeState, index int, cause error) {
	a.withdrawInteraction(state, index)
	state.sink.PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentError,
		contracts.NotificationFieldText: "Antigravity CLI raised an interaction LeapMux cannot answer: " + cause.Error(),
	})
	if err := a.rpc.cancel(a.Context(), state.id); err != nil {
		slog.Warn("antigravity cancel a cascade that waits for an interaction LeapMux cannot answer", "agent_id", a.AgentID(), "cascade_id", state.id, "error", err)
	}
}

func (a *Agent) controlCurrentLocked(state *cascadeState, control *antigravityControl) bool {
	if a.IsDiscardingOutput() || state.closed || a.cascades[state.id] != state || state.generation != a.generation {
		return false
	}
	if control.kind == antigravityControlPlan {
		return state.planControl == control
	}
	return state.controls[control.stepIndex] == control && state.trajectoryID == control.trajectoryID
}

// stepControlLocked returns the current control of the step at stepIndex of
// the trajectory trajectoryID, or nil when LeapMux holds none for the step.
// The caller holds a.Mu.
//
// The language server addresses an answer to a step, not to one interaction
// of it, and a step can replace the interaction that it waits for
// (registerInteraction). So every answer, manual or automatic, states the
// control instance that it answers, and reaches the language server only
// while that instance is the step's current control. The check cannot cover
// an answer that is already on its way to the language server when the step
// replaces its interaction, because the language server reads no instance.
func (a *Agent) stepControlLocked(state *cascadeState, trajectoryID string, stepIndex int) *antigravityControl {
	control := state.controls[stepIndex]
	if control == nil || !a.controlCurrentLocked(state, control) || control.trajectoryID != trajectoryID {
		return nil
	}
	return control
}

// publishControl publishes the control's request once, while the control is
// current. Publication and steer-input binding run as separate queued writes
// so a transient binding failure retries only the binding and never republishes
// or drops cancellation of an already-published control.
func (a *Agent) publishControl(state *cascadeState, control *antigravityControl) {
	queue := state.sink.OutputQueue()
	a.Mu.Lock()
	if !a.controlCurrentLocked(state, control) {
		a.Mu.Unlock()
		return
	}
	if control.published {
		retryBinding := control.steerBinding != nil
		a.Mu.Unlock()
		if retryBinding {
			a.flushControlPublications(queue)
		}
		return
	}
	if control.publication != nil {
		a.Mu.Unlock()
		a.flushControlPublications(queue)
		return
	}
	request := agent.ControlRequest{
		RequestID: control.requestID, InstanceID: control.instanceID, Payload: control.payload, AgentSessionID: control.cascadeID,
		NativeTurnID: control.executionID,
	}
	owner := agent.NewOutputOwner()
	bindOwner := agent.NewOutputOwner()
	control.publicationOwner = owner
	control.steerOwner = bindOwner
	tickets := queue.Append(
		agent.OutputWrite{
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
				if control.publicationOwner == owner {
					control.publicationOwner = nil
					control.publication = nil
					control.published = current
				}
				a.Mu.Unlock()
				if !current {
					state.sink.CancelControlRequestInstance(request.RequestID, request.InstanceID)
				}
				return nil
			},
		},
		agent.OutputWrite{
			Owner: bindOwner, ScopeID: request.RequestID, Label: "bind an Antigravity control to its steers",
			Run: func() error {
				if err := a.setSteerControlInputs(state, control, false); err != nil {
					return err
				}
				a.Mu.Lock()
				if control.steerOwner == bindOwner {
					control.steerOwner = nil
					control.steerBinding = nil
				}
				a.Mu.Unlock()
				return nil
			},
		},
	)
	control.publication = tickets[0]
	control.steerBinding = tickets[1]
	a.Mu.Unlock()
	a.flushControlPublications(queue)
}

// The stream can deliver a control before the echo it follows. Refresh
// its binding in the same output queue that publishes the control.
func (a *Agent) refreshSteerControlInputs(state *cascadeState, executionID string) {
	queue := state.sink.OutputQueue()
	a.Mu.Lock()
	var updates []agent.OutputWrite
	for _, control := range state.controls {
		if control.executionID == executionID {
			updates = append(updates, agent.OutputWrite{
				ScopeID: control.requestID, Label: "bind an Antigravity control to its steers",
				Run: func() error { return a.setSteerControlInputs(state, control, true) },
			})
		}
	}
	queue.Append(updates...)
	a.Mu.Unlock()
	if len(updates) > 0 {
		a.flushControlPublications(queue)
	}
}

// setSteerControlInputs runs in the control's output queue. The stored
// request instance limits the binding to this publication.
func (a *Agent) setSteerControlInputs(state *cascadeState, control *antigravityControl, refresh bool) error {
	a.Mu.Lock()
	if !control.published || control.executionID == "" || !a.controlCurrentLocked(state, control) {
		a.Mu.Unlock()
		return nil
	}
	var inputIDs []string
	for _, reply := range state.inputReplies {
		if reply.steered && !reply.conflicting && reply.executionID == control.executionID &&
			reply.trajectoryID == control.trajectoryID && reply.inputIndex < control.stepIndex {
			inputIDs = append(inputIDs, reply.receipt.InputID)
		}
	}
	a.Mu.Unlock()
	if len(inputIDs) == 0 && !refresh {
		return nil
	}
	slices.Sort(inputIDs)
	return agent.DirectProviderServices(state.sink).SetNativeControlInputs(agent.NativeControlInputs{
		SessionID: state.id, RequestID: control.requestID, InstanceID: control.instanceID, InputIDs: inputIDs,
	})
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
	steerOwner, steerBinding := control.steerOwner, control.steerBinding
	control.publicationOwner, control.publication = nil, nil
	control.steerOwner, control.steerBinding = nil, nil
	a.Mu.Unlock()
	if owner != nil && pending != nil {
		pending.Queue().ClearOwner(owner)
	}
	if steerOwner != nil && steerBinding != nil {
		steerBinding.Queue().ClearOwner(steerOwner)
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

func (a *Agent) autoAnswer(mode string, permission *permissionInteraction) bool {
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

func resolveWithinDirectory(base, target string) (string, error) {
	if base == "" || target == "" {
		return "", errors.New("path is empty")
	}
	resolved, err := resolveExistingPath(target)
	if err != nil {
		return "", err
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	resolvedBase, err = filepath.Abs(resolvedBase)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(resolvedBase, resolved)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("path %q is outside %q", target, base)
	}
	return resolved, nil
}

func pathWithinWorkspace(workspace, target string) bool {
	_, err := resolveWithinDirectory(workspace, target)
	return err == nil
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
				if candidate.kind != antigravityControlPermission || candidate.approvalClaimed || !a.controlCurrentLocked(state, candidate) {
					continue
				}
				if !a.autoAnswer(a.mode, candidate.permission) {
					manual = append(manual, manualRequest{state: state, control: candidate})
					continue
				}
				if next == nil {
					next, control = state, candidate
				}
			}
		}
		a.Mu.Unlock()
		for _, request := range manual {
			a.publishControl(request.state, request.control)
		}
		if next == nil {
			return
		}
		// The step or permission policy can change while a manual request
		// is being published. Claim only the interaction the policy read.
		a.Mu.Lock()
		generationCtx := a.streamCtx
		if generationCtx.Err() != nil {
			a.Mu.Unlock()
			return
		}
		if !a.controlCurrentLocked(next, control) || control.approvalClaimed || !a.autoAnswer(a.mode, control.permission) {
			a.Mu.Unlock()
			continue
		}
		control.approvalClaimed = true
		a.Mu.Unlock()
		err := a.rpc.handleInteraction(generationCtx, next.id, map[string]any{
			"trajectoryId": control.trajectoryID, "stepIndex": control.stepIndex,
			"permission": map[string]any{"allow": true, "scope": antigravityPermissionScopeOnce},
		})
		if err == nil {
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
		a.publishControl(next, control)
	}
}

// executeControlReply sends the answer data to the step that it addresses, as
// the answer to the control instance instanceID. It refuses an answer that
// reaches nothing, so the Worker releases the answer instead of recording it
// as delivered:
//
//   - The step replaced the instance (stepControlLocked), or the answer
//     states none: agent.ControlInstanceRefusal.
//   - The step's control is gone, the cascade is gone or not current, or
//     the language server took no answer from a step whose control LeapMux
//     retired meanwhile: agent.ErrControlRequestNotPending.
func (a *Agent) executeControlReply(data []byte, instanceID string) error {
	reply, err := decodeAntigravityControlReply(data)
	if err != nil {
		return err
	}
	var interaction map[string]any
	if err := json.Unmarshal(reply.interactionRaw, &interaction); err != nil {
		return fmt.Errorf("the Antigravity control answer has a malformed interaction: %w", err)
	}
	a.Mu.Lock()
	state, known := a.cascades[reply.cascadeID]
	if !known {
		a.Mu.Unlock()
		return fmt.Errorf("%w: Antigravity CLI runs no cascade %q", agent.ErrControlRequestNotPending, reply.cascadeID)
	}
	control := a.stepControlLocked(state, reply.address.trajectoryID, reply.address.stepIndex)
	if control == nil || a.streamCtx.Err() != nil {
		a.Mu.Unlock()
		return fmt.Errorf("%w: the step of Antigravity CLI waits for no answer", agent.ErrControlRequestNotPending)
	}
	if err := agent.ControlInstanceRefusal(control.instanceID, instanceID); err != nil {
		a.Mu.Unlock()
		return err
	}
	generationCtx := a.streamCtx
	a.Mu.Unlock()
	err = a.rpc.handleInteraction(generationCtx, reply.cascadeID, interaction)
	a.Mu.Lock()
	pending := a.cascades[reply.cascadeID] == state && state.generation == a.generation &&
		!state.closed && state.trajectoryID == reply.address.trajectoryID && state.controls[reply.address.stepIndex] == control
	a.Mu.Unlock()
	if err != nil {
		slog.Warn("antigravity answer interaction failed", "agent_id", a.AgentID(), "cascade_id", reply.cascadeID, "trajectory_id", reply.address.trajectoryID, "step_index", reply.address.stepIndex, "pending", pending, "error", err)
	} else {
		slog.Info("antigravity interaction answered", "agent_id", a.AgentID(), "cascade_id", reply.cascadeID, "trajectory_id", reply.address.trajectoryID, "step_index", reply.address.stepIndex)
	}
	if isInputNotRegistered(err) && !pending {
		return fmt.Errorf("%w: the step of Antigravity CLI took no answer: %w", agent.ErrControlRequestNotPending, err)
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
	// The plan review addresses no step of the language server, so it has a
	// key of its own, which its payload states as the control_request envelope
	// states its key (antigravityProvider.ControlRequestKey).
	requestID := "antigravity-plan:" + id.Generate()
	payload, _ := json.Marshal(map[string]any{
		"name":       contracts.AntigravityFrameInteraction,
		"request_id": requestID,
		"request":    map[string]any{"tool_name": contracts.AntigravityControlToolPlanReview, "input": map[string]any{contracts.AntigravityControlPayloadPlanContent: string(text)}},
	})
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed || a.IsDiscardingOutput() {
		a.Mu.Unlock()
		return
	}
	superseded := state.planControl
	control := &antigravityControl{requestID: requestID, instanceID: id.Generate(), cascadeID: state.id, kind: antigravityControlPlan, payload: payload}
	state.planControl = control
	a.Mu.Unlock()
	a.retireControl(state, superseded)
	a.publishControl(state, control)
}

func (a *Agent) readPlanFile(cascadeID, path string) ([]byte, error) {
	base := filepath.Join(a.homeDir, ".gemini", "antigravity-cli", "brain", cascadeID)
	resolved, err := resolveWithinDirectory(base, path)
	if err != nil {
		return nil, fmt.Errorf("the plan path is outside brain/%s: %w", cascadeID, err)
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

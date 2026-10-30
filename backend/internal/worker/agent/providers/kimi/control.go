package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's control requests: an approval (a permission, a plan review, a goal
// start) and a question.
//
// The server announces each through an unsequenced `interaction` frame and
// parks the tool call until a REST answer resolves it. The worker publishes
// the complete native frame as the control
// request, under the server's own interaction ID, and the browser reads
// it through the Kimi plugin. The answer comes back through
// SendControlResponse: ResolveControlResponse (control_response.go) turns the
// browser's decision into the server's own body, and deliverControlResponse
// posts it.
//
// The server resolves a request by itself when the turn ends (an abort, a
// failure) and states that with an `interaction` frame whose status is `ended`. The worker then withdraws the request, so no banner waits on an
// answer the server no longer takes.

// kimiControlKind identifies the native reply that answers a control request.
type kimiControlKind int

const (
	kimiControlApproval kimiControlKind = iota + 1
	kimiControlQuestion
)

// kimiPendingControl is one published control request.
type kimiPendingControl struct {
	kind      kimiControlKind
	sessionID string
	// answered is set once LeapMux delivered an answer, so the server's
	// resolution that follows does not withdraw a request the user answered.
	answered        bool
	bypassable      bool
	payload         []byte
	ready           bool
	published       bool
	answering       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

// kimiInteraction reads the native interaction identity and its nested request.
type kimiInteraction struct {
	Type          string                  `json:"type"`
	Kind          string                  `json:"kind"`
	InteractionID string                  `json:"interaction_id"`
	SessionID     string                  `json:"session_id"`
	AgentID       string                  `json:"agent_id"`
	Status        string                  `json:"status"`
	Request       *kimiInteractionRequest `json:"request"`
	Raw           json.RawMessage         `json:"-"`
}

// kimiInteractionRequest contains the native fields needed to publish and answer a control.
type kimiInteractionRequest struct {
	ToolName         string `json:"tool_name"`
	ToolInputDisplay struct {
		Kind    string              `json:"kind"`
		Plan    string              `json:"plan"`
		Options []kimiLabeledOption `json:"options"`
	} `json:"tool_input_display"`
	Questions []struct {
		ID      string `json:"id"`
		Options []struct {
			ID string `json:"id"`
		} `json:"options"`
		MultiSelect bool `json:"multi_select"`
	} `json:"questions"`
}

// agent returns the native interaction's agent, or the main agent when absent.
func (r kimiInteraction) agent() string {
	if r.AgentID != "" {
		return r.AgentID
	}
	return kimiMainAgentID
}

// kimiLabeledOption is one option a plan review offers.
type kimiLabeledOption struct {
	Label string `json:"label"`
}

// handleInteraction decodes a native control frame outside the durable event stream.
func (a *Agent) handleInteraction(raw json.RawMessage) {
	var interaction kimiInteraction
	if err := json.Unmarshal(raw, &interaction); err != nil {
		slog.Warn("Kimi control interaction does not decode", "agent_id", a.AgentID(), "error", err)
		return
	}
	interaction.Raw = raw
	a.dispatchInteraction(interaction)
}

func (a *Agent) dispatchInteraction(interaction kimiInteraction) {
	if interaction.Type != contracts.KimiFrameInteraction || interaction.InteractionID == "" || a.IsDiscardingOutput() {
		return
	}
	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if interaction.SessionID == "" || interaction.SessionID != current {
		return
	}
	if interaction.Kind != contracts.KimiInteractionKindApproval && interaction.Kind != contracts.KimiInteractionKindQuestion {
		return
	}
	if interaction.Status == kimiInteractionEnded {
		a.resolveControl(interaction.InteractionID)
		return
	}
	if interaction.Status != kimiInteractionPending || interaction.Request == nil {
		return
	}
	switch interaction.Kind {
	case contracts.KimiInteractionKindApproval:
		a.handleApprovalRequested(interaction)
	case contracts.KimiInteractionKindQuestion:
		a.handleQuestionRequested(interaction)
	}
}

func (a *Agent) handleApprovalRequested(interaction kimiInteraction) {
	agentID := interaction.agent()
	// Preserve the model's text above the control that asks about its next call.
	a.flushAgentText(agentID)
	display := interaction.Request.ToolInputDisplay
	if display.Kind == contracts.KimiDisplayPlanReview && agentID == kimiMainAgentID {
		if plan := strings.TrimSpace(display.Plan); plan != "" {
			a.sink.UpdatePlan([]byte(display.Plan), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, providerkit.ExtractPlanTitle(plan))
		}
	}
	tool := interaction.Request.ToolName
	bypassable := tool != "" && tool != contracts.KimiToolExitPlanMode && tool != contracts.KimiToolCreateGoal && tool != contracts.KimiToolAskUserQuestion && display.Kind != contracts.KimiDisplayPlanReview && display.Kind != contracts.KimiDisplayGoalStart
	a.publishControl(interaction.InteractionID, kimiControlApproval, interaction.Raw, bypassable)
}

func (a *Agent) handleQuestionRequested(interaction kimiInteraction) {
	a.flushAgentText(interaction.agent())
	a.publishControl(interaction.InteractionID, kimiControlQuestion, interaction.Raw, false)
}

// flushAgentText persists what an agent streamed so far. An empty id is the
// main agent, as parseKimiEvent reads an event that states no agent.
func (a *Agent) flushAgentText(agentID string) {
	if agentID == "" {
		agentID = kimiMainAgentID
	}
	run := a.run(agentID)
	if sink := a.runSink(run); sink != nil {
		a.flushRun(run, sink, agent.MessageCompletionComplete)
	}
}

// publishControl stores and broadcasts one control request.
//
// A request the store refused cannot reach the user, and the tool call it
// parks would wait for good. The worker cancels it instead, which the server
// reports to the model as a refusal it can act on.
func (a *Agent) publishControl(requestID string, kind kimiControlKind, payload []byte, bypassable bool) {
	a.Mu.Lock()
	sessionID := a.sessionID
	if a.controls == nil {
		a.controls = make(map[string]*kimiPendingControl)
	}
	if _, repeat := a.controls[requestID]; repeat {
		a.Mu.Unlock()
		return
	}
	bypass := bypassable && a.settings.permissionOption() == contracts.KimiModeAuto && !a.StoppedLocked()
	pending := &kimiPendingControl{kind: kind, sessionID: sessionID, bypassable: bypassable,
		payload: append([]byte(nil), payload...), ready: bypass, publicationDone: make(chan struct{})}
	a.controls[requestID] = pending
	a.Mu.Unlock()
	if bypass {
		a.queuePermissionApprovals()
		return
	}
	a.publishRegisteredControl(requestID, pending)
}

func (a *Agent) publishRegisteredControl(requestID string, pending *kimiPendingControl) {
	a.Mu.Lock()
	if a.controls[requestID] != pending || a.StoppedLocked() {
		if a.controls[requestID] == pending {
			delete(a.controls, requestID)
		}
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	err := a.sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID, Payload: pending.payload, AgentSessionID: pending.sessionID,
	})
	a.Mu.Lock()
	pending.ready, pending.published = true, err == nil
	current := a.controls[requestID] == pending && !a.StoppedLocked()
	if (err != nil || !current) && a.controls[requestID] == pending {
		delete(a.controls, requestID)
	}
	bypass := a.settings.permissionOption() == contracts.KimiModeAuto && pending.bypassable && !pending.automaticFailed
	pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	a.Mu.Unlock()
	if err == nil {
		if !current {
			a.sink.CancelControlRequest(requestID)
		} else if bypass {
			a.queuePermissionApprovals()
		}
		return
	}
	slog.Error("kimi publish control request", "agent_id", a.AgentID(), "request_id", requestID, "error", err)
	if current {
		go a.cancelUnpublishedControl(pending.sessionID, requestID, pending.kind)
	}
}

func (a *Agent) queuePermissionApprovals() {
	a.approvalWork.Run("permissions", func() {
		a.sessionMu.Lock()
		defer a.sessionMu.Unlock()
		a.approvePendingPermissions()
	})
}

// approvePendingPermissions runs while the caller holds sessionMu.
// Plan reviews, goal confirmations and questions always keep their own answers.
func (a *Agent) approvePendingPermissions() {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	claimed := make(map[string]*kimiPendingControl)
	toPublish := make(map[string]*kimiPendingControl)
	for id, pending := range a.controls {
		if pending.sessionID != a.sessionID || !pending.ready || pending.answering || pending.answered {
			continue
		}
		if a.settings.permissionOption() == contracts.KimiModeAuto && pending.bypassable && !pending.automaticFailed {
			pending.answering = true
			claimed[id] = pending
		} else if !pending.published {
			pending.ready = false
			toPublish[id] = pending
		}
	}
	a.Mu.Unlock()
	for id, pending := range toPublish {
		a.publishRegisteredControl(id, pending)
	}
	answer, _ := json.Marshal(contracts.KimiApprovalReply{Kind: contracts.KimiInteractionKindApproval, Decision: contracts.KimiDecisionApproved})
	for id, pending := range claimed {
		err := a.deliverApproval(pending.sessionID, id, answer)
		a.finishControlReply(id, pending, err, true)
		if err != nil {
			slog.Warn("kimi could not approve a pending tool", "request_id", id, "error", err)
		}
	}
}

func (a *Agent) finishControlReply(requestID string, pending *kimiPendingControl, err error, automatic bool) {
	a.Mu.Lock()
	current := a.controls[requestID] == pending
	retire := errors.Is(err, agent.ErrDeliveryUncertain) || errors.Is(err, errKimiControlGone) || a.StoppedLocked()
	published := pending.published
	if current {
		pending.answering = false
		if retire {
			delete(a.controls, requestID)
			pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		} else if err == nil {
			pending.answered = true
		} else {
			pending.automaticFailed = pending.automaticFailed || automatic
			if !published {
				pending.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if !current {
		return
	}
	if retire || err == nil {
		if published && (automatic || retire) {
			a.sink.CancelControlRequest(requestID)
		}
	} else if !published {
		a.publishRegisteredControl(requestID, pending)
	}
}

// cancelUnpublishedControl resolves a request no user can answer. It runs off
// the dispatcher, because the REST call must not hold the event stream.
func (a *Agent) cancelUnpublishedControl(sessionID, requestID string, kind kimiControlKind) {
	if kimiCheckID("session", sessionID) != nil || kimiCheckID("control", requestID) != nil {
		return
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	var reply any
	switch kind {
	case kimiControlApproval:
		reply = contracts.KimiApprovalReply{Kind: contracts.KimiInteractionKindApproval, Decision: contracts.KimiDecisionCancelled}
	case kimiControlQuestion:
		reply = contracts.KimiQuestionReply{Kind: contracts.KimiInteractionKindQuestion, Dismissed: true}
	default:
		return
	}
	err := a.api.post(ctx, kimiItemPath(sessionID, "interactions", requestID, ""), reply, nil, kimiCodeInteractionEnded, kimiCodeInteractionNotFound)
	if err != nil {
		slog.Warn("kimi cancel an unpublished control request", "agent_id", a.AgentID(), "request_id", requestID, "error", err)
	}
}

// resolveControl drops a request the server resolved. One LeapMux did not
// answer -- the turn ended, or another client answered it -- is withdrawn from
// the user's banner.
func (a *Agent) resolveControl(requestID string) {
	if requestID == "" {
		return
	}
	a.Mu.Lock()
	pending := a.controls[requestID]
	delete(a.controls, requestID)
	withdraw := pending != nil && !pending.answered && pending.published
	if pending != nil {
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	}
	a.Mu.Unlock()
	if !withdraw {
		return
	}
	a.sink.CancelControlRequest(requestID)
}

// withdrawAllControls withdraws every pending request, for a session the agent
// no longer drives.
func (a *Agent) withdrawAllControls() {
	a.Mu.Lock()
	ids := make([]string, 0, len(a.controls))
	for id, pending := range a.controls {
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		if !pending.answered && pending.published {
			ids = append(ids, id)
		}
	}
	clear(a.controls)
	a.Mu.Unlock()
	for _, id := range ids {
		a.sink.CancelControlRequest(id)
	}
	// The workspace-trust question belongs to the session too.
	a.workspaceTrust.Retire(a.sink)
}

// reconcileControls makes the published requests match the ones the server
// states as pending after a reconnect. Interactions are unsequenced, so durable
// event replay cannot recover their changes. A request that the server raised
// during the disconnect is published, and one that it resolved is withdrawn
// because an answer to it would fail. The caller holds dispatchMu.
func (a *Agent) reconcileControls(sessionID string, approvals, questions []json.RawMessage) {
	pending := make(map[string]bool, len(approvals)+len(questions))
	var raised []kimiInteraction
	collect := func(items []json.RawMessage, interactionKind string) {
		for _, item := range items {
			interaction, requestID, ok := kimiSnapshotInteraction(interactionKind, sessionID, item)
			if !ok {
				slog.Warn("kimi snapshot states an interaction that does not decode", "agent_id", a.AgentID(), "kind", interactionKind)
				continue
			}
			pending[requestID] = true
			raised = append(raised, interaction)
		}
	}
	collect(approvals, contracts.KimiInteractionKindApproval)
	collect(questions, contracts.KimiInteractionKindQuestion)

	a.Mu.Lock()
	var resolved []string
	for requestID, control := range a.controls {
		if pending[requestID] || control.sessionID != sessionID {
			continue
		}
		delete(a.controls, requestID)
		control.publicationOnce.Do(func() { close(control.publicationDone) })
		if !control.answered && control.published {
			resolved = append(resolved, requestID)
		}
	}
	a.Mu.Unlock()
	slices.Sort(resolved)
	for _, requestID := range resolved {
		a.sink.CancelControlRequest(requestID)
	}
	// publishControl skips a request that is published already.
	for _, interaction := range raised {
		a.dispatchInteraction(interaction)
	}
}

// kimiSnapshotInteraction projects the current REST snapshot item into the same
// native interaction shape that the WebSocket supplies. REST lists approvals and
// questions separately and keeps their request fields beside their identity.
func kimiSnapshotInteraction(kind, sessionID string, item json.RawMessage) (kimiInteraction, string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(item, &fields) != nil || fields == nil {
		return kimiInteraction{}, "", false
	}
	var ids struct {
		ApprovalID string `json:"approval_id"`
		QuestionID string `json:"question_id"`
		AgentID    string `json:"agent_id"`
		ToolCallID string `json:"tool_call_id"`
	}
	if json.Unmarshal(item, &ids) != nil {
		return kimiInteraction{}, "", false
	}
	var requestID string
	switch kind {
	case contracts.KimiInteractionKindApproval:
		requestID = ids.ApprovalID
	case contracts.KimiInteractionKindQuestion:
		requestID = ids.QuestionID
	default:
		return kimiInteraction{}, "", false
	}
	if requestID == "" {
		return kimiInteraction{}, "", false
	}
	agentID := ids.AgentID
	if agentID == "" {
		agentID = kimiMainAgentID
	}
	createdAt := fields["created_at"]
	for _, key := range []string{"approval_id", "question_id", "session_id", "agent_id", "turn_id", "tool_call_id", "created_at", "type", "agentId", "sessionId"} {
		delete(fields, key)
	}
	frame := map[string]any{
		"type": contracts.KimiFrameInteraction, "session_id": sessionID, "agent_id": agentID,
		"interaction_id": requestID, "kind": kind, "status": kimiInteractionPending, "request": fields,
	}
	if ids.ToolCallID != "" {
		frame["tool_call_id"] = ids.ToolCallID
	}
	if len(createdAt) != 0 {
		frame["event_created_at"] = createdAt
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return kimiInteraction{}, "", false
	}
	var interaction kimiInteraction
	if json.Unmarshal(raw, &interaction) != nil {
		return kimiInteraction{}, "", false
	}
	interaction.Raw = raw
	return interaction, requestID, true
}

// kimiControlEnvelope is the control-response envelope the service forwards:
// the browser's own, with its inner response already turned into the server's
// body by ResolveControlResponse.
type kimiControlEnvelope struct {
	Response struct {
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
	} `json:"response"`
}

// errKimiControlGone reports an answer for a request the server no longer
// waits on.
var errKimiControlGone = errors.New("the Kimi Code control request is no longer pending")

// deliverControlResponse posts one resolved answer.
func (a *Agent) deliverControlResponse(raw []byte) error {
	var envelope kimiControlEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode the Kimi Code control response: %w", err)
	}
	requestID := envelope.Response.RequestID
	if requestID == "" || len(envelope.Response.Response) == 0 {
		return errors.New("the Kimi Code control response states no request or no answer")
	}
	if err := kimiCheckID("control", requestID); err != nil {
		return err
	}
	a.Mu.Lock()
	pending := a.controls[requestID]
	unpublished := pending != nil && pending.ready && !pending.published
	a.Mu.Unlock()
	if pending == nil || unpublished {
		return errKimiControlGone
	}
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-pending.publicationDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	if pending.sessionID != a.sessionID {
		a.Mu.Unlock()
		return errors.New("the Kimi Code control request belongs to a previous session")
	}
	if a.controls[requestID] != pending || pending.answered || pending.answering || !pending.published {
		a.Mu.Unlock()
		return errKimiControlGone
	}
	if err := kimiCheckID("session", pending.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	pending.answering = true
	a.Mu.Unlock()
	var err error
	switch pending.kind {
	case kimiControlApproval:
		err = a.deliverApproval(pending.sessionID, requestID, envelope.Response.Response)
	case kimiControlQuestion:
		err = a.deliverAnswer(pending.sessionID, requestID, envelope.Response.Response)
	default:
		err = fmt.Errorf("unknown Kimi Code control kind %d", pending.kind)
	}
	a.finishControlReply(requestID, pending, err, false)
	return err
}

// deliverApproval posts an approval decision. An approval that switches the
// permission mode applies the mode first. For a plan, plan mode still holds at
// that point: the server leaves plan mode as it takes the approval, and the
// mode the agent runs in afterwards must be the one the user chose. For a goal
// start, the server makes the same switch itself and reports it on no event.
func (a *Agent) deliverApproval(sessionID, approvalID string, raw json.RawMessage) error {
	var reply contracts.KimiApprovalReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("decode the Kimi Code approval: %w", err)
	}
	if mode := reply.PermissionMode; mode != "" {
		if err := a.applyApprovalPermissionMode(sessionID, mode); err != nil {
			return err
		}
	}
	reply.PermissionMode = ""
	reply.Kind = contracts.KimiInteractionKindApproval
	ctx, cancel := a.requestContext()
	defer cancel()
	err := a.api.post(ctx, kimiItemPath(sessionID, "interactions", approvalID, ""), reply, nil)
	return classifyKimiControlError(err)
}

// deliverAnswer posts a question's answers, or dismisses it.
func (a *Agent) deliverAnswer(sessionID, questionID string, raw json.RawMessage) error {
	var reply contracts.KimiQuestionReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("decode the Kimi Code question answer: %w", err)
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	reply.Kind = contracts.KimiInteractionKindQuestion
	if reply.Dismissed {
		reply.Answers = nil
		reply.Method = ""
	} else {
		reply.Method = kimiQuestionMethod
	}
	err := a.api.post(ctx, kimiItemPath(sessionID, "interactions", questionID, ""), reply, nil)
	return classifyKimiControlError(err)
}

// classifyKimiControlError reads a failed answer. An answer for a request that
// another path resolved is gone, and one the transport lost may have landed.
func classifyKimiControlError(err error) error {
	if err == nil {
		return nil
	}
	if code, stated := kimiErrorCode(err); stated {
		if code == kimiCodeInteractionEnded || code == kimiCodeInteractionNotFound || code == kimiCodeSessionNotFound {
			return errKimiControlGone
		}
		return err
	}
	return providerkit.ClassifyHTTPDeliveryError("Kimi Code control answer", err)
}

package deepseekharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type nativeControl struct {
	event           string
	toolName        string
	agentID         string
	instanceID      string
	request         json.RawMessage
	sink            agent.ProviderServices
	payload         []byte
	published       bool
	ready           bool
	answering       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
	// Mu protects reply ownership as the native tool and request streams meet.
	replyCallID   string
	replyInputIDs []string
	replyBound    bool
	tracksUnbound bool
}

type nativeControlKind uint8

const (
	nativeControlKindUnknown nativeControlKind = iota
	nativeControlKindApproval
	nativeControlKindQuestions
	nativeControlKindPlanReview
)

type nativeQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type nativeQuestionIntent struct {
	Kind    string `json:"kind"`
	Approve string `json:"approve"`
	CallID  string `json:"callId"`
}

type nativeQuestion struct {
	ID          string                 `json:"id"`
	Header      string                 `json:"header"`
	Question    string                 `json:"question"`
	Detail      string                 `json:"detail"`
	MultiSelect bool                   `json:"multiSelect"`
	Options     []nativeQuestionOption `json:"options"`
	Intent      *nativeQuestionIntent  `json:"intent"`
}

type parsedApprovalRequest struct {
	ToolName string
	CallID   string
	Reason   string
}

type parsedWaterfallControl struct {
	Type          string
	Event         string
	EventID       string
	AgentID       string
	HasRequestObj bool
	Kind          nativeControlKind
	Approval      parsedApprovalRequest
	Questions     []nativeQuestion
	WaitCallID    string
	StrictValid   bool
}

func parseWaterfallControl(raw []byte) parsedWaterfallControl {
	fields, ok := providerkit.RawObject(raw)
	if !ok {
		return parsedWaterfallControl{}
	}
	parsed := parsedWaterfallControl{
		Type:    providerkit.ObjectString(fields, "type"),
		Event:   providerkit.ObjectString(fields, "event"),
		EventID: providerkit.ObjectString(fields, "eventId"),
		AgentID: providerkit.ObjectString(fields, "agentId"),
	}
	requestObj, hasRequestObj := providerkit.RawObject(fields["request"])
	parsed.HasRequestObj = hasRequestObj
	if !hasRequestObj && parsed.Event == contracts.DeepseekHarnessControlEventUserQuestions {
		// A user-questions payload without a nested request object reads its own questions.
		requestObj = fields
	}
	parsed.parseRequestObject(requestObj)
	return parsed
}

func parseWaterfallRequest(event string, raw json.RawMessage) parsedWaterfallControl {
	parsed := parsedWaterfallControl{Event: event}
	requestObj, ok := providerkit.RawObject(raw)
	parsed.HasRequestObj = ok
	if ok {
		parsed.parseRequestObject(requestObj)
	}
	return parsed
}

func (p *parsedWaterfallControl) parseRequestObject(requestObj map[string]json.RawMessage) {
	if requestObj == nil {
		return
	}
	switch p.Event {
	case contracts.DeepseekHarnessControlEventApproval:
		toolName := providerkit.ObjectString(requestObj, "toolName")
		callID := providerkit.ObjectString(requestObj, "callId")
		displayReason, _ := providerkit.RawObject(requestObj["displayReason"])
		reason := providerkit.ObjectString(displayReason, "en")
		if reason == "" {
			reason = providerkit.ObjectString(requestObj, "reason")
		}
		p.Approval = parsedApprovalRequest{ToolName: toolName, CallID: callID, Reason: reason}
		if toolName != "" {
			p.Kind = nativeControlKindApproval
			p.StrictValid = true
		}
	case contracts.DeepseekHarnessControlEventUserQuestions:
		waitObj, _ := providerkit.RawObject(requestObj["wait"])
		p.WaitCallID = providerkit.ObjectString(waitObj, "callId")
		elements, hasArray := providerkit.RawArray(requestObj["questions"])
		strictValid := hasArray && len(elements) > 0
		seenIDs := map[string]bool{}
		hasPlanReview := false
		records := make([]nativeQuestion, 0, len(elements))
		for _, element := range elements {
			record, ok := providerkit.RawObject(element)
			if !ok {
				strictValid = false
				continue
			}
			question := nativeQuestion{
				ID:          providerkit.ObjectString(record, "id"),
				Header:      providerkit.ObjectString(record, "header"),
				Question:    providerkit.ObjectString(record, "question"),
				Detail:      providerkit.ObjectString(record, "detail"),
				MultiSelect: providerkit.RawBool(record["multiSelect"]),
			}
			if intentObj, ok := providerkit.RawObject(record["intent"]); ok {
				question.Intent = &nativeQuestionIntent{
					Kind:    providerkit.ObjectString(intentObj, "kind"),
					Approve: providerkit.ObjectString(intentObj, "approve"),
					CallID:  providerkit.ObjectString(intentObj, "callId"),
				}
				if question.Intent.Kind == deepseekHarnessIntentPlanReview {
					hasPlanReview = true
				}
			}
			optElems, hasOpts := providerkit.RawArray(record["options"])
			if !hasOpts && providerkit.RawPresent(record["options"]) {
				strictValid = false
			}
			seenLabels := map[string]bool{}
			for _, optElem := range optElems {
				optObj, ok := providerkit.RawObject(optElem)
				label := providerkit.ObjectString(optObj, "label")
				if !ok || label == "" {
					strictValid = false
					continue
				}
				if seenLabels[label] {
					strictValid = false
				}
				seenLabels[label] = true
				question.Options = append(question.Options, nativeQuestionOption{
					Label:       label,
					Description: providerkit.ObjectString(optObj, "description"),
				})
			}
			if question.ID == "" || question.Question == "" || seenIDs[question.ID] {
				strictValid = false
			} else {
				seenIDs[question.ID] = true
			}
			if question.Intent != nil && question.Intent.Kind == deepseekHarnessIntentPlanReview {
				if question.Intent.CallID == "" || !seenLabels[question.Intent.Approve] {
					strictValid = false
				}
			}
			records = append(records, question)
		}
		if hasPlanReview {
			if len(records) == 1 && records[0].Intent != nil && records[0].Intent.Kind == deepseekHarnessIntentPlanReview {
				p.Kind = nativeControlKindPlanReview
				p.Questions = records
				p.StrictValid = strictValid
			}
			return
		}
		p.Kind = nativeControlKindQuestions
		p.StrictValid = strictValid
		p.Questions = make([]nativeQuestion, 0, len(records))
		for _, question := range records {
			if question.ID != "" && question.Question != "" {
				p.Questions = append(p.Questions, question)
			}
		}
	}
}

func (a *Agent) noteControlUnboundLocked(control *nativeControl) {
	if control != nil && control.replyCallID != "" && len(control.replyInputIDs) == 0 && !control.tracksUnbound {
		control.tracksUnbound = true
		a.unboundControls.Add(1)
	}
}

func (a *Agent) clearControlUnboundLocked(control *nativeControl) {
	if control != nil && control.tracksUnbound {
		control.tracksUnbound = false
		a.unboundControls.Add(-1)
	}
}

func (a *Agent) deleteControlLocked(id string) {
	if control := a.controls[id]; control != nil {
		a.clearControlUnboundLocked(control)
		delete(a.controls, id)
	}
}

func (a *Agent) clearControlsLocked() {
	for _, control := range a.controls {
		a.clearControlUnboundLocked(control)
	}
	clear(a.controls)
}

// ControlRequestKey reads the key that the agent publishes a waterfall control
// under: its eventId (publishNativeControl).
func (deepseekHarnessProvider) ControlRequestKey(payload []byte, _ string) (string, bool) {
	id := providerkit.StringField(payload, "eventId")
	return id, id != ""
}

// resolveStoredControl validates the stored request and the answer envelope.
// An empty refusal means that both are valid. The Worker resolves only a
// stored request whose eventId is its key (ControlRequestKey), so the key
// needs no check here.
func resolveStoredControl(ctx agent.ControlResponseContext) (parsedWaterfallControl, string) {
	parsed := parseWaterfallControl(ctx.RequestPayload)
	if parsed.Type != "waterfall" || parsed.AgentID == "" || !parsed.HasRequestObj || !parsed.StrictValid || parsed.Kind == nativeControlKindUnknown {
		return parsed, agent.RefusalUnreadableRequest
	}
	_, behavior, _, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok || (behavior != contracts.ControlAnswerBehaviorAllow && behavior != contracts.ControlAnswerBehaviorDeny) {
		return parsed, agent.RefusalNoDecision
	}
	return parsed, ""
}

func (a *Agent) publishNativeControl(event, id, sessionID string, request, raw []byte) error {
	if id == "" || sessionID == "" || len(request) == 0 {
		return fmt.Errorf("DeepSeek Harness control has no identity")
	}
	if event != contracts.DeepseekHarnessControlEventApproval && event != contracts.DeepseekHarnessControlEventUserQuestions {
		return a.replyRemoteEvent(id, map[string]any{"kind": "next"})
	}
	sink := a.sink
	if sessionID != a.session() {
		a.Mu.Lock()
		var child string
		for _, stream := range a.streams {
			if stream.sessionID == sessionID {
				child = stream.childAgentID
				break
			}
		}
		a.Mu.Unlock()
		if child == "" {
			return a.replyRemoteEvent(id, map[string]any{"kind": "next"})
		}
		sink = a.sink.ChildSink(child)
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	var header map[string]any
	decoder = json.NewDecoder(bytes.NewReader(request))
	decoder.UseNumber()
	if err := decoder.Decode(&header); err != nil || header == nil {
		return fmt.Errorf("DeepSeek Harness control request is invalid")
	}
	parsed := parseWaterfallRequest(event, request)
	if event == contracts.DeepseekHarnessControlEventApproval {
		if tool, ok := header["toolName"].(string); ok {
			header["tool_name"] = tool
		}
		if call, ok := header["callId"].(string); ok {
			header["tool_use_id"] = call
		}
	} else {
		header["tool_name"] = contracts.DeepseekHarnessToolAskUserQuestion
		if parsed.Kind == nativeControlKindPlanReview {
			review := parsed.Questions[0]
			header["tool_name"] = contracts.DeepseekHarnessToolExitPlanMode
			header["tool_use_id"] = review.Intent.CallID
			// The Worker requires the declared compression. A review without plan text stores no plan.
			if plan := review.Detail; plan != "" {
				compressed, compression := msgcodec.Compress([]byte(plan))
				sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(plan))
			}
		}
	}
	payload["request"] = header
	content, err := encodeJSON(payload)
	if err != nil {
		return err
	}
	toolName, _ := header["tool_name"].(string)
	callID, _ := header["tool_use_id"].(string)
	if callID == "" && event == contracts.DeepseekHarnessControlEventUserQuestions {
		callID = parsed.WaitCallID
	}
	control := &nativeControl{
		event: event, toolName: toolName, agentID: sessionID, instanceID: uuid.NewString(),
		request: append([]byte(nil), request...), sink: sink, payload: content, publicationDone: make(chan struct{}),
		replyCallID: callID, replyInputIDs: a.replyToolInputs(sessionID, callID),
	}
	a.Mu.Lock()
	if a.controls[id] != nil {
		a.Mu.Unlock()
		return nil
	}
	if a.StoppedLocked() || a.streamFailure != nil {
		a.Mu.Unlock()
		return fmt.Errorf("DeepSeek Harness cannot publish a control after its connection ends")
	}
	a.controls[id] = control
	a.noteControlUnboundLocked(control)
	automatic := a.canBypassControlLocked(control)
	if automatic {
		control.ready = true
		control.publicationOnce.Do(func() { close(control.publicationDone) })
	}
	a.Mu.Unlock()
	if automatic {
		a.queuePermissionApprovals()
		return nil
	}
	return a.publishRegisteredControl(id, control)
}

func (a *Agent) publishRegisteredControl(id string, control *nativeControl) error {
	a.Mu.Lock()
	inputIDs := control.replyInputIDs
	a.Mu.Unlock()
	err := control.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: control.agentID, RequestID: id, InstanceID: control.instanceID,
		Payload: control.payload, NativeInputIDs: inputIDs,
	})
	a.Mu.Lock()
	current := a.controls[id] == control
	control.published, control.ready = err == nil, true
	if err == nil && len(inputIDs) > 0 {
		control.replyBound = true
	}
	control.publicationOnce.Do(func() { close(control.publicationDone) })
	if err != nil && current {
		a.deleteControlLocked(id)
	}
	var binding agent.NativeControlInputs
	if current {
		binding = control.takeReplyInputsLocked(id)
	}
	automatic := current && err == nil && a.canBypassControlLocked(control)
	a.Mu.Unlock()
	a.bindReplyControl(control.sink, binding)
	if !current && err == nil {
		control.sink.CancelControlRequestInstance(id, control.instanceID)
	}
	if automatic {
		a.queuePermissionApprovals()
	}
	return err
}

func (a *Agent) replyRemoteEvent(id string, outcome map[string]any) error {
	a.Mu.Lock()
	clientID := a.clientID
	a.Mu.Unlock()
	if clientID == "" {
		return fmt.Errorf("DeepSeek Harness has no Remote generation")
	}
	return a.rpc.call(a.Context(), "$events/result", map[string]any{"clientId": clientID, "eventId": id, "outcome": outcome}, nil)
}

func (a *Agent) answerControl(input agent.RawInput) error {
	raw := input.Data
	id, behavior, _, ok := agent.DecodeControlBehavior(raw)
	if !ok || id == "" || behavior != contracts.ControlAnswerBehaviorAllow && behavior != contracts.ControlAnswerBehaviorDeny {
		return fmt.Errorf("DeepSeek Harness control response is invalid")
	}
	a.Mu.Lock()
	pending, exists := a.controls[id]
	a.Mu.Unlock()
	if !exists {
		return fmt.Errorf("DeepSeek Harness control response has no pending request")
	}
	if err := agent.ControlInstanceRefusal(pending.instanceID, input.ControlInstanceID); err != nil {
		return err
	}
	if pending.publicationDone != nil {
		select {
		case <-pending.publicationDone:
		case <-a.Context().Done():
			return a.Context().Err()
		}
	}
	var value any
	if pending.event == contracts.DeepseekHarnessControlEventApproval {
		value = "rejected"
		if behavior == contracts.ControlAnswerBehaviorAllow {
			value = "allowed-once"
		}
	} else {
		parsed := parseWaterfallRequest(pending.event, pending.request)
		if parsed.Kind == nativeControlKindPlanReview {
			question := parsed.Questions[0]
			label, err := planReviewLabel(question, behavior == contracts.ControlAnswerBehaviorAllow, agent.DecodeControlChoice(raw))
			if err != nil {
				return err
			}
			value = map[string]any{"answers": []map[string]any{{"id": question.ID, "selected": []string{label}}}}
		} else {
			if behavior != contracts.ControlAnswerBehaviorAllow {
				return a.sendControlOutcome(id, pending, input.ControlInstanceID, map[string]any{"kind": "next"})
			}
			var inner struct {
				Answers json.RawMessage `json:"answers"`
			}
			if err := json.Unmarshal(agent.DecodeControlInnerResponse(raw), &inner); err != nil {
				return err
			}
			var answers []map[string]any
			if len(inner.Answers) == 0 || json.Unmarshal(inner.Answers, &answers) != nil || answers == nil {
				return fmt.Errorf("DeepSeek Harness question response has no answers")
			}
			value = map[string]any{"answers": answers}
		}
	}
	return a.sendControlOutcome(id, pending, input.ControlInstanceID, map[string]any{"kind": "result", "value": value})
}

// planReviewLabel is the option of a plan review that an answer selects. An
// approval selects the review's approve label. A refusal selects the
// refusing option that the answer states as its choice, or the first one
// when it states none: a review can offer several refusals, such as one that
// also leaves plan mode, and the user chose one of them.
func planReviewLabel(question nativeQuestion, approve bool, choice string) (string, error) {
	approveLabel := question.Intent.Approve
	if approve {
		if choice != "" && choice != approveLabel {
			return "", fmt.Errorf("DeepSeek Harness plan review approves with %q, not %q", approveLabel, choice)
		}
		if approveLabel == "" {
			return "", fmt.Errorf("DeepSeek Harness plan review has no requested choice")
		}
		return approveLabel, nil
	}
	for _, option := range question.Options {
		if option.Label != approveLabel && (choice == "" || option.Label == choice) {
			return option.Label, nil
		}
	}
	if choice != "" {
		return "", fmt.Errorf("DeepSeek Harness plan review offers no refusal %q", choice)
	}
	return "", fmt.Errorf("DeepSeek Harness plan review has no requested choice")
}

package deepseekharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type nativeControl struct {
	event           string
	toolName        string
	agentID         string
	request         json.RawMessage
	sink            agent.ProviderServices
	payload         []byte
	published       bool
	ready           bool
	answering       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

type nativeQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Detail   string `json:"detail"`
	Options  []struct {
		Label string `json:"label"`
	} `json:"options"`
	Intent *struct {
		Kind    string `json:"kind"`
		Approve string `json:"approve"`
		CallID  string `json:"callId"`
	} `json:"intent"`
}

// storedControlRefusal validates the stored request and the answer envelope.
// An empty result means that both are valid.
func storedControlRefusal(ctx agent.ControlResponseContext) string {
	var payload struct {
		Type    string          `json:"type"`
		Event   string          `json:"event"`
		ID      string          `json:"eventId"`
		AgentID string          `json:"agentId"`
		Request json.RawMessage `json:"request"`
	}
	if json.Unmarshal(ctx.RequestPayload, &payload) != nil || payload.Type != "waterfall" || payload.ID == "" || payload.AgentID == "" || ctx.RequestID != "" && ctx.RequestID != payload.ID {
		return agent.RefusalUnreadableRequest
	}
	requestID, _, _, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok {
		return agent.RefusalNoDecision
	}
	if requestID != payload.ID {
		return agent.RefusalOtherRequest
	}
	switch payload.Event {
	case contracts.DeepseekHarnessControlEventApproval:
		var request struct {
			ToolName string `json:"toolName"`
		}
		if json.Unmarshal(payload.Request, &request) != nil || request.ToolName == "" {
			return agent.RefusalUnreadableRequest
		}
		return ""
	case contracts.DeepseekHarnessControlEventUserQuestions:
		var request struct {
			Questions []nativeQuestion `json:"questions"`
		}
		if json.Unmarshal(payload.Request, &request) != nil || len(request.Questions) == 0 {
			return agent.RefusalUnreadableRequest
		}
		ids := map[string]bool{}
		for _, question := range request.Questions {
			if question.ID == "" || question.Question == "" || ids[question.ID] {
				return agent.RefusalUnreadableRequest
			}
			ids[question.ID] = true
			labels := map[string]bool{}
			for _, option := range question.Options {
				if option.Label == "" || labels[option.Label] {
					return agent.RefusalUnreadableRequest
				}
				labels[option.Label] = true
			}
			if question.Intent != nil && question.Intent.Kind == "plan-review" && (question.Intent.CallID == "" || !labels[question.Intent.Approve]) {
				return agent.RefusalUnreadableRequest
			}
		}
		return ""
	default:
		return agent.RefusalUnreadableRequest
	}
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
	if event == contracts.DeepseekHarnessControlEventApproval {
		if tool, ok := header["toolName"].(string); ok {
			header["tool_name"] = tool
		}
		if call, ok := header["callId"].(string); ok {
			header["tool_use_id"] = call
		}
	} else {
		header["tool_name"] = contracts.DeepseekHarnessToolAskUserQuestion
		var questions struct {
			Questions []nativeQuestion `json:"questions"`
		}
		if err := json.Unmarshal(request, &questions); err != nil {
			return err
		}
		for _, question := range questions.Questions {
			if question.Intent != nil && question.Intent.Kind == "plan-review" {
				header["tool_name"] = contracts.DeepseekHarnessToolExitPlanMode
				header["tool_use_id"] = question.Intent.CallID
				// The Worker requires the declared compression. A review without plan text stores no plan.
				if plan := question.Detail; plan != "" {
					compressed, compression := msgcodec.Compress([]byte(plan))
					sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(plan))
				}
			}
		}
	}
	payload["request"] = header
	content, err := encodeJSON(payload)
	if err != nil {
		return err
	}
	toolName, _ := header["tool_name"].(string)
	control := &nativeControl{event: event, toolName: toolName, agentID: sessionID, request: append([]byte(nil), request...), sink: sink, payload: content, publicationDone: make(chan struct{})}
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
	err := control.sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: control.agentID, RequestID: id, Payload: control.payload})
	a.Mu.Lock()
	current := a.controls[id] == control
	control.published, control.ready = err == nil, true
	control.publicationOnce.Do(func() { close(control.publicationDone) })
	if err != nil && current {
		delete(a.controls, id)
	}
	automatic := current && err == nil && a.canBypassControlLocked(control)
	a.Mu.Unlock()
	if !current && err == nil {
		control.sink.CancelControlRequest(id)
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

func (a *Agent) answerControl(raw []byte) error {
	id, behavior, _, ok := agent.DecodeControlBehavior(raw)
	if !ok || id == "" || behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny {
		return fmt.Errorf("DeepSeek Harness control response is invalid")
	}
	a.Mu.Lock()
	pending, exists := a.controls[id]
	a.Mu.Unlock()
	if !exists {
		return fmt.Errorf("DeepSeek Harness control response has no pending request")
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
		if behavior == agent.ControlBehaviorAllow {
			value = "allowed-once"
		}
	} else {
		var response struct {
			Response struct {
				Response struct {
					Answers json.RawMessage `json:"answers"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return err
		}
		var questions struct {
			Questions []nativeQuestion `json:"questions"`
		}
		if err := json.Unmarshal(pending.request, &questions); err != nil {
			return err
		}
		if len(questions.Questions) == 1 && questions.Questions[0].Intent != nil && questions.Questions[0].Intent.Kind == "plan-review" {
			question := questions.Questions[0]
			label := question.Intent.Approve
			if behavior != agent.ControlBehaviorAllow {
				label = ""
				for _, option := range question.Options {
					if option.Label != question.Intent.Approve {
						label = option.Label
						break
					}
				}
			}
			if label == "" {
				return fmt.Errorf("DeepSeek Harness plan review has no requested choice")
			}
			value = map[string]any{"answers": []map[string]any{{"id": question.ID, "selected": []string{label}}}}
		} else {
			if behavior != agent.ControlBehaviorAllow {
				return a.sendControlOutcome(id, pending, map[string]any{"kind": "next"})
			}
			var answers []map[string]any
			if len(response.Response.Response.Answers) == 0 || json.Unmarshal(response.Response.Response.Answers, &answers) != nil {
				return fmt.Errorf("DeepSeek Harness question response has no answers")
			}
			value = map[string]any{"answers": answers}
		}
	}
	return a.sendControlOutcome(id, pending, map[string]any{"kind": "result", "value": value})
}

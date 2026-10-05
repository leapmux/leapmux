package deepseekharness

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type nativeControl struct {
	event   string
	agentID string
	request json.RawMessage
	sink    agent.ProviderServices
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

// storedControlRefusal reads the stored request and the answer's envelope, and
// states the reason the reader sees when either one is unreadable or mismatched.
// "" means both stand.
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
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	var header map[string]any
	if err := json.Unmarshal(request, &header); err != nil || header == nil {
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
				// The Worker decodes the plan with the compression that the call states, and it refuses an
				// unspecified one. A review that carries no plan stores none.
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
	a.Mu.Lock()
	a.controls[id] = nativeControl{event: event, agentID: sessionID, request: append([]byte(nil), request...), sink: sink}
	a.Mu.Unlock()
	if err := sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: sessionID, RequestID: id, Payload: content}); err != nil {
		a.Mu.Lock()
		delete(a.controls, id)
		a.Mu.Unlock()
		return err
	}
	return nil
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
	if !ok || id == "" {
		return fmt.Errorf("DeepSeek Harness control response is invalid")
	}
	a.Mu.Lock()
	pending, exists := a.controls[id]
	a.Mu.Unlock()
	if !exists {
		return fmt.Errorf("DeepSeek Harness control response has no pending request")
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
				if err := a.replyRemoteEvent(id, map[string]any{"kind": "next"}); err != nil {
					return err
				}
				a.Mu.Lock()
				delete(a.controls, id)
				a.Mu.Unlock()
				return nil
			}
			var answers []map[string]any
			if len(response.Response.Response.Answers) == 0 || json.Unmarshal(response.Response.Response.Answers, &answers) != nil {
				return fmt.Errorf("DeepSeek Harness question response has no answers")
			}
			value = map[string]any{"answers": answers}
		}
	}
	if err := a.replyRemoteEvent(id, map[string]any{"kind": "result", "value": value}); err != nil {
		return err
	}
	a.Mu.Lock()
	delete(a.controls, id)
	a.Mu.Unlock()
	return nil
}

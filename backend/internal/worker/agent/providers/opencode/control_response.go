package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ResolveControlResponse translates a browser response for the native control registry.
func (opencodeProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.ControlResponseResolution{}
	control, err := readOpenCodeStoredControl(ctx.RequestPayload)
	if err != nil {
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	answer := map[string]any{"request_id": ctx.RequestID}
	_, behavior, message, valid := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !valid {
		result.Refuse(agent.RefusalUnreadableAnswer)
		return result
	}
	if behavior != "" {
		switch behavior {
		case contracts.ControlAnswerBehaviorAllow:
			if control.kind != "permission" {
				result.Refuse("the form requires its field values")
				return result
			}
			answer[contracts.OpenCodeNativeAnswerFieldDecision] = contracts.OpenCodeNativePermissionDecisionOnce
		case contracts.ControlAnswerBehaviorDeny:
			if control.kind == "permission" {
				answer[contracts.OpenCodeNativeAnswerFieldDecision] = contracts.OpenCodeNativePermissionDecisionReject
				if message != "" {
					answer["message"] = message
				}
			} else {
				answer[contracts.OpenCodeNativeAnswerFieldCancel] = true
				result.FollowUp = agent.FeedbackFollowUp(message)
			}
		default:
			result.Refuse(agent.RefusalNoDecision)
			return result
		}
	} else {
		body, err := readOpenCodeBrowserControlBody(ctx.ResponseContent)
		if err != nil {
			result.Refuse(agent.RefusalUnreadableAnswer)
			return result
		}
		if control.kind == "permission" {
			var selected struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			if json.Unmarshal(body, &selected) != nil || selected.Outcome.Outcome != "selected" {
				result.Refuse(agent.RefusalNoDecision)
				return result
			}
			decision := selected.Outcome.OptionID
			if decision != contracts.OpenCodeNativePermissionDecisionOnce && decision != contracts.OpenCodeNativePermissionDecisionReject && (decision != contracts.OpenCodeNativePermissionDecisionAlways || !control.allowsAlways) {
				result.Refuse("OpenCode did not offer this permission option")
				return result
			}
			answer[contracts.OpenCodeNativeAnswerFieldDecision] = decision
		} else {
			formAnswer, err := readOpenCodeBrowserFormAnswer(body, control.externalFields)
			if err != nil {
				result.Refuse(err.Error())
				return result
			}
			if formAnswer.Answer != nil {
				answer[contracts.OpenCodeNativeAnswerFieldAnswer] = formAnswer.Answer
			}
			if formAnswer.Cancel {
				answer[contracts.OpenCodeNativeAnswerFieldCancel] = true
			}
		}
	}
	content, err := json.Marshal(answer)
	if err != nil {
		result.Refuse(agent.RefusalUnencodableReply)
		return result
	}
	if _, err := readOpenCodeControlAnswer(content, ctx.RequestID, control.kind); err != nil {
		result.Refuse(err.Error())
		return result
	}
	result.Content = content
	return result
}

type openCodeStoredControl struct {
	kind           string
	allowsAlways   bool
	externalFields []string
}

func readOpenCodeStoredControl(raw json.RawMessage) (openCodeStoredControl, error) {
	var request struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return openCodeStoredControl{}, err
	}
	switch request.Type {
	case contracts.OpenCodeNativeEventPermissionAsked:
		permission, err := readOpenCodePermission(request.Data)
		if err != nil {
			return openCodeStoredControl{}, err
		}
		return openCodeStoredControl{kind: "permission", allowsAlways: len(permission.Save) > 0}, nil
	case contracts.OpenCodeNativeEventFormCreated:
		var data struct {
			Form json.RawMessage `json:"form"`
		}
		if err := json.Unmarshal(request.Data, &data); err != nil {
			return openCodeStoredControl{}, err
		}
		form, err := readOpenCodeForm(data.Form)
		if err != nil {
			return openCodeStoredControl{}, err
		}
		control := openCodeStoredControl{kind: "form"}
		for _, field := range form.Fields {
			if field.Type == contracts.OpenCodeNativeFormFieldTypeExternal {
				control.externalFields = append(control.externalFields, *field.Key)
			}
		}
		return control, nil
	default:
		return openCodeStoredControl{}, fmt.Errorf("unsupported OpenCode control type %q", request.Type)
	}
}

// The shared ID decoder gives the nested envelope priority when both formats occur.
func readOpenCodeBrowserControlBody(raw []byte) (json.RawMessage, error) {
	var envelope struct {
		Result   json.RawMessage `json:"result"`
		Response struct {
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if strings.TrimSpace(envelope.Response.RequestID) != "" {
		return envelope.Response.Response, nil
	}
	return envelope.Result, nil
}

func readOpenCodeBrowserFormAnswer(raw json.RawMessage, externalFields []string) (openCodeFormAnswer, error) {
	var response struct {
		openCodeFormAnswer
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return openCodeFormAnswer{}, errors.New(agent.RefusalUnreadableAnswer)
	}
	if response.Action == "" {
		if response.Content != nil {
			return openCodeFormAnswer{}, errors.New("the form content has no elicitation action")
		}
		return response.openCodeFormAnswer, nil
	}
	if response.Answer != nil || response.Cancel {
		return openCodeFormAnswer{}, errors.New("the form reply mixes native and elicitation answers")
	}
	switch response.Action {
	case contracts.MCPElicitationActionAccept:
		if response.Content == nil {
			response.Content = make(map[string]json.RawMessage)
		}
		for _, field := range externalFields {
			response.Content[field] = json.RawMessage("true")
		}
		return openCodeFormAnswer{Answer: response.Content}, nil
	case contracts.MCPElicitationActionDecline, contracts.MCPElicitationActionCancel:
		return openCodeFormAnswer{Cancel: true}, nil
	default:
		return openCodeFormAnswer{}, errors.New("the form reply has an unknown elicitation action")
	}
}

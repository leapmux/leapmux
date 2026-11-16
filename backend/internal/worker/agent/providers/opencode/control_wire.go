package opencode

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

type openCodePermission struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionID"`
	Action    string    `json:"action"`
	Resources *[]string `json:"resources"`
	Save      []string  `json:"save,omitempty"`
	Source    *struct {
		Type      string `json:"type"`
		MessageID string `json:"messageID"`
		ID        string `json:"id"`
	} `json:"source,omitempty"`
}

type openCodeForm struct {
	ID        string                    `json:"id"`
	SessionID string                    `json:"sessionID"`
	Title     string                    `json:"title"`
	Metadata  json.RawMessage           `json:"metadata,omitempty"`
	Fields    []openCodeFormFieldHeader `json:"fields"`
}

type openCodeFormFieldHeader struct {
	Key  *string         `json:"key"`
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

func (field *openCodeFormFieldHeader) UnmarshalJSON(raw []byte) error {
	type header openCodeFormFieldHeader
	var decoded header
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	decoded.Raw = append(json.RawMessage(nil), raw...)
	*field = openCodeFormFieldHeader(decoded)
	return nil
}

func readOpenCodePermission(raw json.RawMessage) (openCodePermission, error) {
	var permission openCodePermission
	if err := json.Unmarshal(raw, &permission); err != nil {
		return permission, err
	}
	if err := openCodeRouteID(permission.ID); err != nil {
		return permission, fmt.Errorf("invalid OpenCode permission ID: %w", err)
	}
	if err := openCodeRouteID(permission.SessionID); err != nil {
		return permission, fmt.Errorf("invalid OpenCode permission session: %w", err)
	}
	if permission.SessionID == "global" || permission.Resources == nil {
		return permission, errors.New("the OpenCode permission requires a session and resource array")
	}
	if source := permission.Source; source != nil && (source.Type != "tool") {
		return permission, errors.New("the OpenCode permission has an invalid tool source")
	}
	return permission, nil
}

func readOpenCodeForm(raw json.RawMessage) (openCodeForm, error) {
	var form openCodeForm
	if err := json.Unmarshal(raw, &form); err != nil {
		return form, err
	}
	if err := openCodeRouteID(form.ID); err != nil {
		return form, fmt.Errorf("invalid OpenCode form ID: %w", err)
	}
	if err := openCodeRouteID(form.SessionID); err != nil {
		return form, fmt.Errorf("invalid OpenCode form session: %w", err)
	}
	if len(form.Fields) == 0 {
		return form, errors.New("the OpenCode form has no fields")
	}
	for _, field := range form.Fields {
		if field.Key == nil {
			return form, errors.New("the OpenCode form field has no key")
		}
		switch field.Type {
		case contracts.OpenCodeNativeFormFieldTypeString, contracts.OpenCodeNativeFormFieldTypeNumber, contracts.OpenCodeNativeFormFieldTypeInteger, contracts.OpenCodeNativeFormFieldTypeBoolean, contracts.OpenCodeNativeFormFieldTypeMultiselect, contracts.OpenCodeNativeFormFieldTypeExternal:
		default:
			return form, fmt.Errorf("unsupported OpenCode form field type %q", field.Type)
		}
	}
	return form, nil
}

// openCodeControlAnswer is the browser reply, addressed to the pending request.
// A form's cancellation and accepted empty answer remain distinct.
type openCodeControlAnswer struct {
	openCodeFormAnswer
	RequestID string `json:"request_id"`
	Decision  string `json:"decision,omitempty"`
	Message   string `json:"message,omitempty"`
}

type openCodeFormAnswer struct {
	Answer map[string]json.RawMessage `json:"answer,omitempty"`
	Cancel bool                       `json:"cancel,omitempty"`
}

func readOpenCodeControlAnswer(raw []byte, requestID, kind string) (openCodeControlAnswer, error) {
	var answer openCodeControlAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return answer, err
	}
	if requestID == "" || answer.RequestID != requestID {
		return answer, errors.New("the OpenCode answer addresses another control request")
	}
	switch kind {
	case "permission":
		if answer.Answer != nil || answer.Cancel {
			return answer, errors.New("a permission reply cannot answer or cancel a form")
		}
		switch answer.Decision {
		case contracts.OpenCodeNativePermissionDecisionOnce, contracts.OpenCodeNativePermissionDecisionAlways, contracts.OpenCodeNativePermissionDecisionReject:
		default:
			return answer, errors.New("the OpenCode permission decision must be once, always, or reject")
		}
	case "form":
		if answer.Decision != "" || answer.Message != "" || answer.Cancel && answer.Answer != nil {
			return answer, errors.New("the OpenCode form reply mixes incompatible answers")
		}
		if !answer.Cancel && answer.Answer == nil {
			return answer, errors.New("the OpenCode form reply requires an answer or cancellation")
		}
		for _, value := range answer.Answer {
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				return answer, err
			}
			switch typed := decoded.(type) {
			case string, float64, bool:
			case []any:
				for _, item := range typed {
					if _, ok := item.(string); !ok {
						return answer, errors.New("the OpenCode form selection requires string values")
					}
				}
			default:
				return answer, errors.New("the OpenCode form answer has an invalid value type")
			}
		}
	default:
		return answer, errors.New("the OpenCode control request has an unknown kind")
	}
	return answer, nil
}

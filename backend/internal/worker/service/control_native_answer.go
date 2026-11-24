package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// matchNativePermissionAnswer accepts the adapter response of exactly one
// offered option. The provider builds the answer with its normal encoder.
func matchNativePermissionAnswer(plugin agent.Provider, input agent.ControlAnswerInput, content []byte) (agent.ControlAnswer, *leapmuxv1.ControlDecision, error) {
	permission := input.Prompt.GetPermission()
	if permission == nil {
		return agent.ControlAnswer{}, nil, errors.New("a native response needs a permission prompt with offered choices")
	}
	submitted, ok := decodeUniqueControlObject(content)
	if !ok {
		return agent.ControlAnswer{}, nil, errors.New("the native response must be a JSON object with unique member names")
	}
	var matchedAnswer agent.ControlAnswer
	var matchedDecision *leapmuxv1.ControlDecision
	for _, option := range permission.GetOptions() {
		input.Decision = &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{
			Permission: &leapmuxv1.PermissionDecision{OptionId: option.GetOptionId()},
		}}
		answer, err := plugin.AnswerControlRequest(input)
		if err != nil || answer.PlanApproval != nil {
			continue
		}
		encoded, ok := decodeUniqueControlObject(answer.Content)
		if !ok || !reflect.DeepEqual(submitted, encoded) {
			continue
		}
		if matchedDecision != nil {
			return agent.ControlAnswer{}, nil, errors.New("the native response matches more than one offered choice. Send a decision with its option ID")
		}
		matchedDecision, matchedAnswer = input.Decision, answer
	}
	if matchedDecision == nil {
		return agent.ControlAnswer{}, nil, errors.New("the native response does not match a permission choice offered by this request")
	}
	return matchedAnswer, matchedDecision, nil
}

func decodeUniqueControlObject(content []byte) (map[string]any, bool) {
	// Validate the complete document and its nesting before walking tokens.
	// UseNumber preserves the exact spelling of every number in the packet.
	if !utf8.Valid(content) || !json.Valid(content) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	value, valid := readUniqueControlJSON(decoder)
	object, isObject := value.(map[string]any)
	return object, valid && isObject
}

func readUniqueControlJSON(decoder *json.Decoder) (any, bool) {
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	switch token {
	case json.Delim('{'):
		fields := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			member, isString := key.(string)
			if err != nil || !isString {
				return nil, false
			}
			if _, exists := fields[member]; exists {
				return nil, false
			}
			value, valid := readUniqueControlJSON(decoder)
			if !valid {
				return nil, false
			}
			fields[member] = value
		}
		_, err := decoder.Token()
		return fields, err == nil
	case json.Delim('['):
		values := make([]any, 0)
		for decoder.More() {
			value, valid := readUniqueControlJSON(decoder)
			if !valid {
				return nil, false
			}
			values = append(values, value)
		}
		_, err := decoder.Token()
		return values, err == nil
	default:
		return token, true
	}
}

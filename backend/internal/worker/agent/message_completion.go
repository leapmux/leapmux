package agent

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

type AssembledMessageKind string

const (
	AssembledMessageKindText      AssembledMessageKind = contracts.AssembledMessageKindText
	AssembledMessageKindReasoning AssembledMessageKind = contracts.AssembledMessageKindReasoning
	AssembledMessageKindPlan      AssembledMessageKind = contracts.AssembledMessageKindPlan
)

type MessageCompletion string

const (
	MessageCompletionComplete    MessageCompletion = contracts.AssembledMessageCompletionComplete
	MessageCompletionInterrupted MessageCompletion = contracts.AssembledMessageCompletionInterrupted
	MessageCompletionError       MessageCompletion = contracts.AssembledMessageCompletionError
)

func MarshalAssembledMessage(kind AssembledMessageKind, text string, completion MessageCompletion) ([]byte, error) {
	return json.Marshal(map[string]string{
		"type":       contracts.AssembledMessageType,
		"kind":       string(kind),
		"text":       text,
		"completion": string(completion),
	})
}

// AnnotateMessageCompletion adds Worker completion metadata to a provider row.
func AnnotateMessageCompletion(content []byte, completion MessageCompletion) ([]byte, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(content, &value); err != nil {
		return nil, fmt.Errorf("unmarshal provider message: %w", err)
	}
	if value == nil {
		return nil, fmt.Errorf("provider message must be a JSON object")
	}
	metadata := make(map[string]json.RawMessage)
	if current := value[contracts.AssembledMessageMetadataKey]; len(current) > 0 {
		if err := json.Unmarshal(current, &metadata); err != nil {
			return nil, fmt.Errorf("unmarshal provider completion metadata: %w", err)
		}
		if metadata == nil {
			metadata = make(map[string]json.RawMessage)
		}
	}
	encodedCompletion, err := json.Marshal(completion)
	if err != nil {
		return nil, fmt.Errorf("marshal provider completion: %w", err)
	}
	metadata["completion"] = encodedCompletion
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal provider completion metadata: %w", err)
	}
	value[contracts.AssembledMessageMetadataKey] = encodedMetadata
	annotated, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal provider message: %w", err)
	}
	return annotated, nil
}

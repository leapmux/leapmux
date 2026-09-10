package agent

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       string(kind),
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: string(completion),
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
	if current := value[contracts.AssembledMessageMetadataField]; len(current) > 0 {
		_ = json.Unmarshal(current, &metadata)
		if metadata == nil {
			metadata = make(map[string]json.RawMessage)
		}
	}
	encodedCompletion, err := json.Marshal(completion)
	if err != nil {
		return nil, fmt.Errorf("marshal provider completion: %w", err)
	}
	metadata[contracts.AssembledMessageFieldCompletion] = encodedCompletion
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal provider completion metadata: %w", err)
	}
	value[contracts.AssembledMessageMetadataField] = encodedMetadata
	annotated, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal provider message: %w", err)
	}
	return annotated, nil
}

// MessageMetadata derives the typed metadata that the message row stores.
// The database copy rebuilds from content and has no independent writer.
func MessageMetadata(content []byte) (leapmuxv1.AssembledMessageKind, leapmuxv1.MessageCompletion) {
	var value map[string]json.RawMessage
	if json.Unmarshal(content, &value) != nil {
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED,
			leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED
	}
	kind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED
	completion := leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED
	var messageType string
	_ = json.Unmarshal(value[contracts.AssembledMessageFieldType], &messageType)
	if messageType == contracts.AssembledMessageType {
		var kindToken, completionToken string
		_ = json.Unmarshal(value[contracts.AssembledMessageFieldKind], &kindToken)
		kind = assembledMessageKindFromToken(kindToken)
		_ = json.Unmarshal(value[contracts.AssembledMessageFieldCompletion], &completionToken)
		completion = messageCompletionFromToken(completionToken)
		return kind, completion
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(value[contracts.AssembledMessageMetadataField], &metadata) == nil {
		var token string
		if json.Unmarshal(metadata[contracts.AssembledMessageFieldCompletion], &token) == nil {
			completion = messageCompletionFromToken(token)
		}
	}
	return kind, completion
}

func assembledMessageKindFromToken(token string) leapmuxv1.AssembledMessageKind {
	switch token {
	case contracts.AssembledMessageKindText:
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT
	case contracts.AssembledMessageKindReasoning:
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_REASONING
	case contracts.AssembledMessageKindPlan:
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_PLAN
	default:
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED
	}
}

func messageCompletionFromToken(token string) leapmuxv1.MessageCompletion {
	switch MessageCompletion(token) {
	case MessageCompletionComplete:
		return leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE
	case MessageCompletionInterrupted:
		return leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED
	case MessageCompletionError:
		return leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_ERROR
	default:
		return leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED
	}
}

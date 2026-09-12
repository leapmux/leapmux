package agent

import (
	"encoding/json"

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

// MessageMetadata keeps worker completion outside the provider's JSON schema.
func MessageMetadata(content MessageContent) (leapmuxv1.AssembledMessageKind, leapmuxv1.MessageCompletion) {
	kind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED
	completion := messageCompletionFromToken(string(content.Completion))
	var value map[string]json.RawMessage
	if json.Unmarshal(content.Original, &value) != nil {
		return kind, completion
	}
	var messageType string
	_ = json.Unmarshal(value[contracts.AssembledMessageFieldType], &messageType)
	if messageType == contracts.AssembledMessageType {
		var kindToken, completionToken string
		_ = json.Unmarshal(value[contracts.AssembledMessageFieldKind], &kindToken)
		kind = assembledMessageKindFromToken(kindToken)
		if completion == leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED {
			_ = json.Unmarshal(value[contracts.AssembledMessageFieldCompletion], &completionToken)
			completion = messageCompletionFromToken(completionToken)
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

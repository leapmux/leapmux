package agent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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
	MessageCompletionFinished    MessageCompletion = contracts.AssembledMessageCompletionFinished
)

// MarshalAssembledMessage validates the neutral vocabulary before it encodes a row.
func MarshalAssembledMessage(kind AssembledMessageKind, text string, completion MessageCompletion) ([]byte, error) {
	if AssembledMessageKindFromToken(string(kind)) == leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED {
		return nil, fmt.Errorf("the assembled message has an unknown kind %q", kind)
	}
	if err := ValidateMessageCompletion(MessageContent{Completion: completion}); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       string(kind),
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: string(completion),
	})
}

// assembledEnvelopeFields captures the top-level fields of an assembled or
// notification-thread envelope without allocating a map for unrelated keys.
type assembledEnvelopeFields struct {
	Type       json.RawMessage `json:"type"`
	Kind       json.RawMessage `json:"kind"`
	Completion json.RawMessage `json:"completion"`
	Text       json.RawMessage `json:"text"`
}

func decodeAssembledEnvelopeFields(raw []byte) (assembledEnvelopeFields, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return assembledEnvelopeFields{}, false
	}
	var fields assembledEnvelopeFields
	if json.Unmarshal(trimmed, &fields) != nil {
		return assembledEnvelopeFields{}, false
	}
	return fields, true
}

func (f assembledEnvelopeFields) envelopeType() string {
	var messageType string
	_ = json.Unmarshal(f.Type, &messageType)
	return messageType
}

func (f assembledEnvelopeFields) metadata(explicitCompletion MessageCompletion) (leapmuxv1.AssembledMessageKind, leapmuxv1.MessageCompletion) {
	kind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED
	completion := messageCompletionFromToken(string(explicitCompletion))
	if f.envelopeType() == contracts.AssembledMessageType {
		var kindToken, completionToken string
		_ = json.Unmarshal(f.Kind, &kindToken)
		kind = AssembledMessageKindFromToken(kindToken)
		if explicitCompletion == "" {
			_ = json.Unmarshal(f.Completion, &completionToken)
			completion = messageCompletionFromToken(completionToken)
		}
	}
	return kind, completion
}

// MessageMetadata keeps worker completion outside the provider's JSON schema.
func MessageMetadata(content MessageContent) (leapmuxv1.AssembledMessageKind, leapmuxv1.MessageCompletion) {
	fields, ok := decodeAssembledEnvelopeFields(content.Original)
	if !ok {
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED, messageCompletionFromToken(string(content.Completion))
	}
	return fields.metadata(content.Completion)
}

// ValidateMessageCompletion rejects an explicit token that the neutral vocabulary does not define.
func ValidateMessageCompletion(content MessageContent) error {
	if content.Completion != "" && messageCompletionFromToken(string(content.Completion)) == leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED {
		return fmt.Errorf("the message has an unknown completion %q", content.Completion)
	}
	return nil
}

// AssembledMessageKindFromToken parses the neutral assembled-message kind token.
func AssembledMessageKindFromToken(token string) leapmuxv1.AssembledMessageKind {
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
	case MessageCompletionFinished:
		return leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_FINISHED
	default:
		return leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED
	}
}

// MessageCompletionFromProto reads the Worker's stored rendering outcome.
func MessageCompletionFromProto(value leapmuxv1.MessageCompletion) MessageCompletion {
	switch value {
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE:
		return MessageCompletionComplete
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED:
		return MessageCompletionInterrupted
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_ERROR:
		return MessageCompletionError
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_FINISHED:
		return MessageCompletionFinished
	default:
		return ""
	}
}

func IncompleteTaskStatus(completion MessageCompletion) bgtask.Status {
	if completion == MessageCompletionFinished {
		return bgtask.StatusEndedWithUnknownOutcome
	}
	if completion == MessageCompletionInterrupted {
		return bgtask.StatusStopped
	}
	if completion == MessageCompletionComplete {
		return bgtask.StatusSucceeded
	}
	return bgtask.StatusFailed
}

package dirac

import (
	"encoding/json"
	"errors"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func diracPresentationPosition(index *int64, deleted json.RawMessage) (string, error) {
	if index == nil || *index < -1 || *index > diracMaximumSafeOffset {
		return "", errors.New("the Dirac presentation has no valid conversation position")
	}
	var deletedRange []int64
	if len(deleted) != 0 {
		if err := json.Unmarshal(deleted, &deletedRange); err != nil || len(deletedRange) != 2 || deletedRange[0] < 0 || deletedRange[1] < deletedRange[0] || deletedRange[1] > diracMaximumSafeOffset {
			return "", errors.New("the Dirac presentation has an invalid deleted conversation range")
		}
	}
	raw, err := json.Marshal(struct {
		Index   int64
		Deleted []int64
	}{*index, deletedRange})
	return string(raw), err
}

// ApiConversationManager creates API status before appending the user API
// entry. Assistant presentation belongs to that following entry, while the
// stored request context retains the API status's original position.
func diracRequestPresentationPosition(index *int64, deleted json.RawMessage) (string, error) {
	if index == nil || *index < -1 || *index >= diracMaximumSafeOffset {
		return "", errors.New("the Dirac API request has no valid conversation position")
	}
	responseIndex := *index + 1
	return diracPresentationPosition(&responseIndex, deleted)
}

func diracRequestContexts(messages []json.RawMessage) (map[string]*contracts.DiracRequestContext, error) {
	contexts := make(map[string]*contracts.DiracRequestContext)
	for _, raw := range messages {
		var message struct {
			ID      string          `json:"id"`
			Index   *int64          `json:"conversationHistoryIndex"`
			Deleted json.RawMessage `json:"conversationHistoryDeletedRange"`
			Content struct {
				Type   string `json:"type"`
				Status struct {
					ID     string `json:"id"`
					Cancel string `json:"cancelReason"`
					Stop   string `json:"stopReason"`
				} `json:"status"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		if message.Content.Type != "api_status" {
			continue
		}
		if message.ID == "" || message.Content.Status.ID != "" && message.Content.Status.ID != message.ID {
			return nil, errors.New("the Dirac API status has another presentation identity")
		}
		position, err := diracRequestPresentationPosition(message.Index, message.Deleted)
		if err != nil {
			return nil, err
		}
		if _, exists := contexts[position]; exists {
			contexts[position] = nil
			continue
		}
		contexts[position] = &contracts.DiracRequestContext{MessageID: message.ID, HistoryIndex: message.Index, DeletedRange: message.Deleted, CancelReason: message.Content.Status.Cancel, StopReason: message.Content.Status.Stop}
	}
	return contexts, nil
}

func diracMessageRequestContext(record contracts.DiracTranscriptRecord) (*contracts.DiracRequestContext, error) {
	if len(record.RequestContext) == 0 {
		return nil, nil
	}
	var context *contracts.DiracRequestContext
	if err := json.Unmarshal(record.RequestContext, &context); err != nil || context == nil || context.MessageID == "" {
		return nil, errors.New("the Dirac message has invalid native request context")
	}
	message, err := decodeDiracMarkdown(record.Message)
	if err != nil {
		return nil, err
	}
	position, err := diracPresentationPosition(message.HistoryIndex, message.DeletedRange)
	if err != nil {
		return nil, err
	}
	requestPosition, err := diracRequestPresentationPosition(context.HistoryIndex, context.DeletedRange)
	if err != nil {
		return nil, err
	}
	if position != requestPosition {
		return nil, errors.New("the Dirac message request belongs to another conversation position")
	}
	return context, nil
}

func diracMessageCompletion(record contracts.DiracTranscriptRecord) (agent.MessageCompletion, error) {
	context, err := diracMessageRequestContext(record)
	if err != nil {
		return "", err
	}
	var completion agent.MessageCompletion
	if context != nil {
		switch context.CancelReason {
		case "user_cancelled":
			completion = agent.MessageCompletionInterrupted
		case "streaming_failed", "retries_exhausted":
			completion = agent.MessageCompletionError
		case "":
			if context.StopReason != "" {
				completion = agent.MessageCompletionComplete
			}
		default:
			return "", errors.New("the Dirac request has an unknown cancellation reason")
		}
	}
	if len(record.TaskletOutcome) != 0 {
		var outcome diracTaskletOutcome
		if err := json.Unmarshal(record.TaskletOutcome, &outcome); err != nil || !outcome.valid() {
			return "", errors.New("the Dirac partial message has an invalid committed tasklet outcome")
		}
		var unfinished agent.MessageCompletion
		switch outcome.Kind {
		case "interrupted", "cancelled":
			unfinished = agent.MessageCompletionInterrupted
		case "failed":
			unfinished = agent.MessageCompletionError
		default:
			return "", errors.New("a completed tasklet does not prove an unfinished message outcome")
		}
		// The request's result describes generation. A later tasklet stop
		// can interrupt its tools without changing that completed generation.
		if completion == "" {
			completion = unfinished
		}
	}
	return completion, nil
}

package dirac

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type diracMarkdownMessage struct {
	ID           string          `json:"id"`
	HistoryIndex *int64          `json:"conversationHistoryIndex"`
	DeletedRange json.RawMessage `json:"conversationHistoryDeletedRange"`
	Content      struct {
		Type      string  `json:"type"`
		Text      *string `json:"content"`
		Reasoning *bool   `json:"isReasoning"`
		Role      string  `json:"role"`
	} `json:"content"`
}

func decodeDiracMarkdown(raw []byte) (diracMarkdownMessage, error) {
	var message diracMarkdownMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return message, err
	}
	if message.ID == "" || message.Content.Type != "markdown" || message.Content.Text == nil ||
		message.Content.Role != "" && message.Content.Role != "assistant" {
		return message, errors.New("the Dirac presentation message is not owned assistant Markdown")
	}
	return message, nil
}

func diracTaskletMessageRecords(parentSessionID string, tasklet diracTaskletRecord, messages []json.RawMessage) ([]contracts.DiracTranscriptRecord, error) {
	contexts, err := diracRequestContexts(messages)
	if err != nil {
		return nil, err
	}
	records := make([]contracts.DiracTranscriptRecord, 0)
	for _, raw := range messages {
		var header struct {
			Content struct {
				Type string `json:"type"`
				Role string `json:"role"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, err
		}
		if header.Content.Type != "markdown" || header.Content.Role == "user" {
			continue
		}
		message, err := decodeDiracMarkdown(raw)
		if err != nil {
			return nil, err
		}
		record := contracts.DiracTranscriptRecord{Type: contracts.DiracRecordKindMessage, SessionID: tasklet.ConversationID, ParentSessionID: parentSessionID, TaskletID: tasklet.ID, Message: raw}
		position, err := diracPresentationPosition(message.HistoryIndex, message.DeletedRange)
		if err != nil {
			return nil, err
		}
		if context := contexts[position]; context != nil {
			record.RequestContext, err = json.Marshal(context)
			if err != nil {
				return nil, err
			}
		}
		if _, err := diracTranscriptRecordKey(record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// The native conversationHistoryIndex identifies the API message preceding
// presentation creation. IDs decide identity; these positions decide order.
func diracTaskletTranscriptRecords(parentSessionID string, tasklet diracTaskletRecord, api, presentation []json.RawMessage) ([]contracts.DiracTranscriptRecord, error) {
	tools, err := diracTaskletToolRecords(parentSessionID, tasklet, api)
	if err != nil {
		return nil, err
	}
	messages, err := diracTaskletMessageRecords(parentSessionID, tasklet, presentation)
	if err != nil {
		return nil, err
	}
	if tasklet.Outcome != nil && tasklet.Outcome.Kind != "completed" && len(tasklet.NativeOutcome) != 0 && len(api) != 0 && len(presentation) != 0 {
		fields, _, err := diracAPIMessage(api[len(api)-1])
		if err != nil {
			return nil, err
		}
		role, _ := diracStoredString(fields, "role")
		latest, err := diracStoredObject(presentation[len(presentation)-1])
		if err != nil {
			return nil, err
		}
		if role == "user" {
			// A stored assistant API entry ends model generation. The final
			// user context without that entry still owns its partial voice.
			for index := range messages {
				message, err := decodeDiracMarkdown(messages[index].Message)
				if err != nil {
					return nil, err
				}
				if message.HistoryIndex != nil && *message.HistoryIndex == int64(len(api)-1) && sameDiracNativeOutcome(message.DeletedRange, latest["conversationHistoryDeletedRange"]) {
					messages[index].TaskletOutcome = tasklet.NativeOutcome
				}
			}
		}
	}
	type positioned struct {
		record   contracts.DiracTranscriptRecord
		position int64
	}
	positions := make(map[string]int64)
	for i, raw := range api {
		_, blocks, err := diracAPIMessage(raw)
		if err != nil {
			return nil, err
		}
		for _, block := range blocks {
			var head struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				ToolID string `json:"tool_use_id"`
			}
			if err := json.Unmarshal(block, &head); err != nil {
				return nil, err
			}
			switch head.Type {
			case contracts.DiracNativeBlockToolUse:
				positions[contracts.DiracRecordRoleRequest+":"+head.ID] = int64(i) * 2
			case contracts.DiracNativeBlockToolResult:
				positions[contracts.DiracRecordRoleResult+":"+head.ToolID] = int64(i) * 2
			}
		}
	}
	ordered := make([]positioned, 0, len(tools)+len(messages))
	for _, tool := range tools {
		var declaration diracToolDeclaration
		if err := json.Unmarshal(tool.Declaration, &declaration); err != nil {
			return nil, err
		}
		position := positions[tool.Role+":"+declaration.ID]
		if tool.Role == contracts.DiracRecordRoleRetained {
			position = diracMaximumSafeOffset*2 + 2
		}
		ordered = append(ordered, positioned{tool, position})
	}
	for _, message := range messages {
		markdown, err := decodeDiracMarkdown(message.Message)
		if err != nil {
			return nil, err
		}
		if markdown.HistoryIndex == nil || *markdown.HistoryIndex < -1 || *markdown.HistoryIndex > diracMaximumSafeOffset {
			return nil, errors.New("the Dirac assistant message has no valid conversation position")
		}
		ordered = append(ordered, positioned{message, *markdown.HistoryIndex*2 + 1})
	}
	slices.SortStableFunc(ordered, func(left, right positioned) int {
		if left.position < right.position {
			return -1
		}
		if left.position > right.position {
			return 1
		}
		return 0
	})
	records := make([]contracts.DiracTranscriptRecord, 0, len(ordered))
	for _, item := range ordered {
		records = append(records, item.record)
	}
	return records, nil
}

func (writer *diracTaskletTranscript) updateMessage(stored *agent.StoredMessage, record contracts.DiracTranscriptRecord, content agent.MessageContent, span agent.SpanInfo) error {
	var original contracts.DiracTranscriptRecord
	if err := json.Unmarshal(stored.Content.Original, &original); err != nil {
		return err
	}
	key, err := diracTranscriptRecordKey(original)
	if err != nil || key != content.IdempotencyKey || stored.Content.AgentSessionID != content.AgentSessionID || stored.Content.IdempotencyKey != key {
		return errors.New("the stored Dirac presentation has another native owner")
	}
	fields := make(map[string]json.RawMessage)
	if len(stored.Content.Supplemental) != 0 {
		if err := json.Unmarshal(stored.Content.Supplemental, &fields); err != nil || fields == nil {
			return errors.New("the stored Dirac presentation view is not an object")
		}
	}
	current := original
	if raw := fields[contracts.DiracSupplementRootMessage]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		currentKey, err := diracTranscriptRecordKey(current)
		if err != nil || currentKey != key {
			return errors.New("the stored Dirac presentation view has another native owner")
		}
	}
	previousMessage, err := decodeDiracMarkdown(current.Message)
	if err != nil {
		return err
	}
	nextMessage, err := decodeDiracMarkdown(record.Message)
	if err != nil {
		return err
	}
	if previousMessage.Content.Role != nextMessage.Content.Role || (previousMessage.Content.Reasoning != nil && *previousMessage.Content.Reasoning) != (nextMessage.Content.Reasoning != nil && *nextMessage.Content.Reasoning) {
		return errors.New("the Dirac presentation view changes its native message role")
	}
	previousContext, err := diracMessageRequestContext(current)
	if err != nil {
		return err
	}
	nextContext, err := diracMessageRequestContext(record)
	if err != nil {
		return err
	}
	if previousContext != nil && (nextContext == nil || nextContext.MessageID != previousContext.MessageID || previousContext.CancelReason != "" && nextContext.CancelReason == "") {
		record.RequestContext = current.RequestContext
	}
	if len(record.TaskletOutcome) == 0 {
		record.TaskletOutcome = current.TaskletOutcome
	}
	completion, err := diracMessageCompletion(record)
	if err != nil {
		return err
	}
	currentRaw, err := json.Marshal(current)
	if err != nil {
		return err
	}
	nextRaw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if !agent.JSONCanonicalEqual(currentRaw, nextRaw) || completion != "" && completion != stored.Content.Completion {
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		fields[contracts.DiracSupplementRootMessage] = raw
		next, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		written, err := writer.EnrichMessage(agent.MessageEnrichment{Publication: content.Publication, AgentSessionID: content.AgentSessionID, Seq: stored.Seq, SpanID: span.SpanID, OriginalContent: stored.Content.Original, PreviousRevision: stored.Revision, SupplementalContent: next, Completion: completion})
		if err != nil {
			return err
		}
		if !written {
			return fmt.Errorf("the Dirac presentation revision changed before its captured update")
		}
	}
	content.WriteReceipt.RecordStoredMessage(stored.Seq)
	content.WriteReceipt.RecordStoredWrite(false)
	return nil
}

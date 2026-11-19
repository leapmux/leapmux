package dirac

import (
	"encoding/json"
	"fmt"
)

type diracAPIOperation struct {
	Offset   *int64            `json:"offset"`
	Type     string            `json:"type"`
	Message  json.RawMessage   `json:"message"`
	Messages []json.RawMessage `json:"messages"`
	Content  json.RawMessage   `json:"content"`
}

func decodeDiracAPIHistory(snapshot, baseline, operations []byte) ([]json.RawMessage, error) {
	messages := make([]json.RawMessage, 0)
	offset := int64(-1)
	appendMessage := func(raw json.RawMessage) error {
		if _, _, err := diracAPIMessage(raw); err != nil {
			return err
		}
		messages = append(messages, raw)
		return nil
	}
	if baseline != nil {
		first := true
		if len(baseline) == 0 || baseline[len(baseline)-1] != '\n' {
			return nil, fmt.Errorf("the Dirac API baseline is incomplete")
		}
		if err := walkDiracOperationLines(baseline, func(raw []byte) error {
			var record diracAPIOperation
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			if first {
				first = false
				if record.Type != "baseline" || record.Offset == nil || *record.Offset < -1 || *record.Offset > diracMaximumSafeOffset {
					return fmt.Errorf("the Dirac API baseline has no valid offset header")
				}
				offset = *record.Offset
				return nil
			}
			if record.Type != "message" {
				return fmt.Errorf("the Dirac API baseline contains another record type")
			}
			return appendMessage(record.Message)
		}); err != nil {
			return nil, err
		}
	} else if snapshot != nil {
		var values []json.RawMessage
		if err := json.Unmarshal(snapshot, &values); err != nil || values == nil {
			return nil, fmt.Errorf("the Dirac API snapshot is not a message array")
		}
		for _, raw := range values {
			if err := appendMessage(raw); err != nil {
				return nil, err
			}
		}
	}
	err := walkDiracOperationLines(operations, func(raw []byte) error {
		var operation diracAPIOperation
		if err := json.Unmarshal(raw, &operation); err != nil {
			return err
		}
		if operation.Offset == nil || *operation.Offset < 0 || *operation.Offset > diracMaximumSafeOffset {
			return fmt.Errorf("the Dirac API operation has no valid offset")
		}
		if *operation.Offset <= offset {
			return nil
		}
		replaces := operation.Type == "reset" || operation.Type == "branch"
		if *operation.Offset != offset+1 && !replaces {
			return fmt.Errorf("the Dirac API operation offset does not follow its predecessor")
		}
		switch operation.Type {
		case "reset", "branch":
			if operation.Messages == nil {
				return fmt.Errorf("the Dirac API replacement has no message array")
			}
			messages = make([]json.RawMessage, 0, len(operation.Messages))
			for _, message := range operation.Messages {
				if err := appendMessage(message); err != nil {
					return err
				}
			}
		case "append_message":
			if err := appendMessage(operation.Message); err != nil {
				return err
			}
		case "append_user_content":
			if len(messages) == 0 {
				return fmt.Errorf("the Dirac API content has no preceding user message")
			}
			fields, content, err := diracAPIMessage(messages[len(messages)-1])
			if err != nil {
				return err
			}
			role, _ := diracStoredString(fields, "role")
			if role != "user" {
				return fmt.Errorf("the Dirac API content follows another message role")
			}
			if _, err := diracStoredObject(operation.Content); err != nil {
				return err
			}
			content = append(content, operation.Content)
			fields["content"], err = json.Marshal(content)
			if err != nil {
				return err
			}
			messages[len(messages)-1], err = json.Marshal(fields)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the Dirac API operation type is unsupported")
		}
		offset = *operation.Offset
		return nil
	})
	if err != nil {
		return nil, err
	}
	return messages, nil
}

func diracAPIMessage(raw []byte) (map[string]json.RawMessage, []json.RawMessage, error) {
	fields, err := diracStoredObject(raw)
	if err != nil {
		return nil, nil, err
	}
	role, err := diracStoredString(fields, "role")
	if err != nil || role != "user" && role != "assistant" {
		return nil, nil, fmt.Errorf("the Dirac API message has an invalid role")
	}
	var text *string
	if json.Unmarshal(fields["content"], &text) == nil && text != nil {
		block, err := json.Marshal(map[string]string{"type": "text", "text": *text})
		return fields, []json.RawMessage{block}, err
	}
	var content []json.RawMessage
	if err := json.Unmarshal(fields["content"], &content); err != nil || content == nil {
		return nil, nil, fmt.Errorf("the Dirac API message has no content")
	}
	for _, block := range content {
		if _, err := diracStoredObject(block); err != nil {
			return nil, nil, err
		}
	}
	return fields, content, nil
}

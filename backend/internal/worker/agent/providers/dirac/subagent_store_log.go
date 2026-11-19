package dirac

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const diracMaximumSafeOffset = 1<<53 - 1

type diracPresentationReplay struct {
	messages []map[string]json.RawMessage
	indexes  map[string]int
	appends  map[string][]string
	offset   int64
}

type diracPresentationOperation struct {
	Offset    *int64                     `json:"offset"`
	Type      string                     `json:"type"`
	Message   json.RawMessage            `json:"message"`
	Messages  []json.RawMessage          `json:"messages"`
	ID        string                     `json:"id"`
	Patch     map[string]json.RawMessage `json:"patch"`
	Deletions []string                   `json:"deletions"`
	Text      *string                    `json:"text"`
}

// decodeDiracPresentation reconstructs the current native document. A nil
// baseline selects the native array snapshot; a present baseline supersedes it.
// Only complete operation lines are visible while the native writer appends.
func decodeDiracPresentation(snapshot, baseline, operations []byte) ([]json.RawMessage, error) {
	state := newDiracPresentationReplay()
	if baseline != nil {
		first := true
		if len(baseline) == 0 || baseline[len(baseline)-1] != '\n' {
			return nil, fmt.Errorf("the Dirac presentation baseline is incomplete")
		}
		err := walkDiracOperationLines(baseline, func(raw []byte) error {
			var record diracPresentationOperation
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			if first {
				first = false
				if record.Type != "baseline" || record.Offset == nil || *record.Offset < -1 || *record.Offset > diracMaximumSafeOffset {
					return fmt.Errorf("the Dirac presentation baseline has no valid offset header")
				}
				state.offset = *record.Offset
				return nil
			}
			if record.Type != "message" {
				return fmt.Errorf("the Dirac presentation baseline contains a non-message record")
			}
			return state.create(record.Message)
		})
		if err != nil {
			return nil, fmt.Errorf("decode the Dirac presentation baseline: %w", err)
		}
	} else if snapshot != nil {
		var messages []json.RawMessage
		if err := json.Unmarshal(snapshot, &messages); err != nil || messages == nil {
			return nil, fmt.Errorf("the Dirac presentation snapshot is not a message array")
		}
		for _, raw := range messages {
			if err := state.create(raw); err != nil {
				return nil, err
			}
		}
	}
	if err := walkDiracOperationLines(operations, func(raw []byte) error {
		var operation diracPresentationOperation
		if err := json.Unmarshal(raw, &operation); err != nil {
			return fmt.Errorf("decode the Dirac presentation operation: %w", err)
		}
		if operation.Offset == nil || *operation.Offset < 0 || *operation.Offset > diracMaximumSafeOffset {
			return fmt.Errorf("the Dirac presentation operation has no valid offset")
		}
		if *operation.Offset <= state.offset {
			return nil
		}
		if *operation.Offset != state.offset+1 && operation.Type != "reset" {
			return fmt.Errorf("the Dirac presentation operation offset %d does not follow %d", *operation.Offset, state.offset)
		}
		if err := state.apply(operation); err != nil {
			return err
		}
		state.offset = *operation.Offset
		return nil
	}); err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, 0, len(state.messages))
	for _, message := range state.messages {
		id, err := diracStoredString(message, "id")
		if err != nil {
			return nil, err
		}
		if err := state.materialize(id); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, nil
}

func newDiracPresentationReplay() *diracPresentationReplay {
	return &diracPresentationReplay{indexes: make(map[string]int), appends: make(map[string][]string), offset: -1}
}

func walkDiracOperationLines(data []byte, apply func([]byte) error) error {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			return nil
		}
		if err := apply(data[:end]); err != nil {
			return err
		}
		data = data[end+1:]
	}
	return nil
}

func diracStoredObject(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("the Dirac stored value is not an object")
	}
	return fields, nil
}

func diracStoredString(fields map[string]json.RawMessage, key string) (string, error) {
	var value *string
	if err := json.Unmarshal(fields[key], &value); err != nil || value == nil {
		return "", fmt.Errorf("the Dirac stored field %s is not a string", key)
	}
	return *value, nil
}

func (state *diracPresentationReplay) create(raw []byte) error {
	message, err := diracStoredObject(raw)
	if err != nil {
		return err
	}
	id, err := diracStoredString(message, "id")
	if err != nil || id == "" {
		return fmt.Errorf("the Dirac presentation message has no identity")
	}
	if _, exists := state.indexes[id]; exists {
		return fmt.Errorf("the Dirac presentation repeats message ID %s", id)
	}
	content, err := diracStoredObject(message["content"])
	if err != nil {
		return err
	}
	if kind, err := diracStoredString(content, "type"); err != nil || kind == "" {
		return fmt.Errorf("the Dirac presentation message has no content type")
	}
	state.indexes[id] = len(state.messages)
	state.messages = append(state.messages, message)
	return nil
}

func (state *diracPresentationReplay) apply(operation diracPresentationOperation) error {
	if operation.Type == "reset" {
		if operation.Messages == nil {
			return fmt.Errorf("the Dirac presentation reset has no message array")
		}
		next := newDiracPresentationReplay()
		for _, raw := range operation.Messages {
			if err := next.create(raw); err != nil {
				return err
			}
		}
		state.messages, state.indexes, state.appends = next.messages, next.indexes, next.appends
		return nil
	}
	if operation.Type == "create" {
		return state.create(operation.Message)
	}
	index, exists := state.indexes[operation.ID]
	if operation.ID == "" || !exists {
		return fmt.Errorf("the Dirac presentation operation has no matching message ID")
	}
	message := state.messages[index]
	if operation.Type == "delete" {
		delete(state.appends, operation.ID)
		state.messages = append(state.messages[:index], state.messages[index+1:]...)
		delete(state.indexes, operation.ID)
		for id, position := range state.indexes {
			if position > index {
				state.indexes[id] = position - 1
			}
		}
		return nil
	}
	content, err := diracStoredObject(message["content"])
	if err != nil {
		return err
	}
	kind, err := diracStoredString(content, "type")
	if err != nil {
		return err
	}
	if operation.Type == "append_markdown" || operation.Type == "append_card_body" {
		if operation.Text == nil || operation.Type == "append_markdown" && kind != "markdown" || operation.Type == "append_card_body" && kind != "card" {
			return fmt.Errorf("the Dirac presentation append has no text or targets another content type")
		}
		state.appends[operation.ID] = append(state.appends[operation.ID], *operation.Text)
		return nil
	}
	if err := state.materialize(operation.ID); err != nil {
		return err
	}
	content, err = diracStoredObject(message["content"])
	if err != nil {
		return err
	}
	if operation.Patch == nil {
		return fmt.Errorf("the Dirac presentation patch has no object")
	}
	switch operation.Type {
	case "patch_message":
		if raw, present := operation.Patch["id"]; present {
			if id, err := diracStoredString(operation.Patch, "id"); err != nil || id != operation.ID {
				return fmt.Errorf("the Dirac presentation patch changes a message identity: %s", string(raw))
			}
		}
		for key, value := range operation.Patch {
			message[key] = value
		}
		return nil
	case "patch_card", "patch_api_status":
		field, expected := "card", "card"
		if operation.Type == "patch_api_status" {
			field, expected = "status", "api_status"
		}
		if kind != expected {
			return fmt.Errorf("the Dirac presentation patch targets another content type")
		}
		target, err := diracStoredObject(content[field])
		if err != nil {
			return err
		}
		if raw, present := operation.Patch["id"]; present {
			previous, previousErr := diracStoredString(target, "id")
			incoming, incomingErr := diracStoredString(operation.Patch, "id")
			if previousErr != nil || incomingErr != nil || previous != incoming {
				return fmt.Errorf("the Dirac presentation patch changes a nested identity: %s", string(raw))
			}
		}
		for key, value := range operation.Patch {
			target[key] = value
		}
		if operation.Type == "patch_api_status" {
			for _, key := range operation.Deletions {
				if key == "id" {
					return fmt.Errorf("the Dirac presentation patch removes a status identity")
				}
				delete(target, key)
			}
		}
		content[field], err = json.Marshal(target)
		if err != nil {
			return err
		}
	case "patch_markdown":
		if kind != "markdown" {
			return fmt.Errorf("the Dirac presentation patch targets non-markdown content")
		}
		for key, value := range operation.Patch {
			if key == "type" || key == "content" {
				return fmt.Errorf("the Dirac markdown patch replaces its type or text")
			}
			content[key] = value
		}
	default:
		return fmt.Errorf("the Dirac presentation operation type %q is unsupported", operation.Type)
	}
	message["content"], err = json.Marshal(content)
	return err
}

func (state *diracPresentationReplay) materialize(id string) error {
	chunks := state.appends[id]
	if len(chunks) == 0 {
		return nil
	}
	message := state.messages[state.indexes[id]]
	content, err := diracStoredObject(message["content"])
	if err != nil {
		return err
	}
	kind, err := diracStoredString(content, "type")
	if err != nil {
		return err
	}
	target, field := content, "content"
	if kind == "card" {
		target, err = diracStoredObject(content["card"])
		if err != nil {
			return err
		}
		field = "body"
	}
	text := ""
	if _, present := target[field]; present {
		text, err = diracStoredString(target, field)
		if err != nil {
			return err
		}
	}
	target[field], err = json.Marshal(text + strings.Join(chunks, ""))
	if err != nil {
		return err
	}
	if kind == "card" {
		content["card"], err = json.Marshal(target)
		if err != nil {
			return err
		}
	}
	message["content"], err = json.Marshal(content)
	if err == nil {
		delete(state.appends, id)
	}
	return err
}

package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type geminiSession struct {
	SessionID   string          `json:"sessionId"`
	ProjectHash string          `json:"projectHash"`
	StartTime   time.Time       `json:"startTime"`
	LastUpdated time.Time       `json:"lastUpdated"`
	Kind        string          `json:"kind"`
	Summary     string          `json:"summary"`
	Messages    []geminiMessage `json:"messages"`
}

type geminiMessage struct {
	Original  json.RawMessage   `json:"-"`
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Content   json.RawMessage   `json:"content"`
	ToolCalls []json.RawMessage `json:"toolCalls"`
	Thoughts  []json.RawMessage `json:"thoughts"`
	Timestamp time.Time         `json:"timestamp"`
	Tokens    json.RawMessage   `json:"tokens"`
}

// UnmarshalJSON retains every native field beside the typed archive index.
func (message *geminiMessage) UnmarshalJSON(data []byte) error {
	type wireMessage geminiMessage
	var decoded wireMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*message = geminiMessage(decoded)
	message.Original = bytes.Clone(data)
	return nil
}

// refreshOriginal keeps a patched native record complete without deleting fields.
func (message *geminiMessage) refreshOriginal() error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message.Original, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("the Gemini message record is not an object")
	}
	if len(message.Content) > 0 {
		fields["content"] = message.Content
	}
	if message.ToolCalls != nil {
		encoded, err := json.Marshal(message.ToolCalls)
		if err != nil {
			return err
		}
		fields["toolCalls"] = encoded
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	message.Original = encoded
	return nil
}

type geminiToolIdentity struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Result json.RawMessage `json:"result"`
	Status string          `json:"status"`
}

// decodeGeminiSession applies Gemini's append-only session records in file order.
// A partial last line carries no complete native record and remains unread.
func decodeGeminiSession(data []byte) (geminiSession, error) {
	var session geminiSession
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return session, errors.New("the Gemini session is empty")
	}
	// Gemini can load a complete JSON session from versions that precede JSONL.
	if json.Valid(trimmed) {
		if err := json.Unmarshal(trimmed, &session); err != nil {
			return session, fmt.Errorf("decode the Gemini session: %w", err)
		}
		if session.SessionID != "" && session.ProjectHash != "" {
			return session, nil
		}
	}
	lines := bytes.Split(data, []byte{'\n'})
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) && index == len(lines)-1 && data[len(data)-1] != '\n' {
			break
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(line, &record); err != nil {
			return geminiSession{}, fmt.Errorf("decode Gemini record %d: %w", index+1, err)
		}
		if identity, exists := record["sessionId"]; exists {
			var metadata geminiSession
			if json.Unmarshal(identity, &metadata.SessionID) != nil || json.Unmarshal(line, &metadata) != nil {
				return geminiSession{}, errors.New("the Gemini session identity is invalid")
			}
			if session.SessionID != "" && (metadata.SessionID != session.SessionID || metadata.ProjectHash != session.ProjectHash) {
				return geminiSession{}, errors.New("the Gemini session identity changed")
			}
			if metadata.SessionID == "" || metadata.ProjectHash == "" {
				return geminiSession{}, errors.New("the Gemini session identity is empty")
			}
			metadata.Messages = session.Messages
			session = metadata
			continue
		}
		if session.SessionID == "" {
			return geminiSession{}, errors.New("the Gemini record precedes its session identity")
		}
		if raw, exists := record["$set"]; exists {
			if err := applyGeminiMetadata(&session, raw); err != nil {
				return geminiSession{}, err
			}
			continue
		}
		if raw, exists := record["$rewindTo"]; exists {
			var id string
			if json.Unmarshal(raw, &id) != nil || id == "" {
				return geminiSession{}, errors.New("the Gemini rewind identity is invalid")
			}
			end := 0
			for i, message := range session.Messages {
				if message.ID == id {
					end = i
					break
				}
			}
			session.Messages = session.Messages[:end]
			continue
		}
		if raw, exists := record["$patch"]; exists {
			if err := applyGeminiMessagePatch(&session, raw); err != nil {
				return geminiSession{}, err
			}
			continue
		}
		var message geminiMessage
		if json.Unmarshal(line, &message) != nil || message.ID == "" || message.Type == "" {
			return geminiSession{}, errors.New("the Gemini message record is invalid")
		}
		replaced := false
		for i := range session.Messages {
			if session.Messages[i].ID == message.ID {
				session.Messages[i] = message
				replaced = true
				break
			}
		}
		if !replaced {
			session.Messages = append(session.Messages, message)
		}
	}
	if session.SessionID == "" {
		return geminiSession{}, errors.New("the Gemini session identity is absent")
	}
	return session, nil
}

func applyGeminiMetadata(session *geminiSession, raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("the Gemini metadata update is invalid")
	}
	for key, target := range map[string]*string{"sessionId": &session.SessionID, "projectHash": &session.ProjectHash} {
		if value, exists := fields[key]; exists {
			var identity string
			if json.Unmarshal(value, &identity) != nil || identity != *target {
				return errors.New("the Gemini metadata changed its session identity")
			}
		}
	}
	for key, target := range map[string]any{
		"summary": &session.Summary, "kind": &session.Kind, "startTime": &session.StartTime,
		"lastUpdated": &session.LastUpdated, "messages": &session.Messages,
	} {
		if value, exists := fields[key]; exists {
			if err := json.Unmarshal(value, target); err != nil {
				return fmt.Errorf("decode Gemini metadata %s: %w", key, err)
			}
		}
	}
	return nil
}

func applyGeminiMessagePatch(session *geminiSession, raw json.RawMessage) error {
	var patch struct {
		ID        string            `json:"id"`
		Content   json.RawMessage   `json:"content"`
		ToolCalls []json.RawMessage `json:"toolCalls"`
		Updates   []json.RawMessage `json:"updates"`
		RemoveIDs []string          `json:"removeIds"`
		OrderIDs  []string          `json:"orderIds"`
	}
	if json.Unmarshal(raw, &patch) != nil {
		return errors.New("the Gemini message patch is invalid")
	}
	if patch.ID != "" {
		for i := range session.Messages {
			message := &session.Messages[i]
			if message.ID != patch.ID {
				continue
			}
			if len(patch.Content) > 0 {
				message.Content = patch.Content
			}
			for _, toolPatch := range patch.ToolCalls {
				var update geminiToolIdentity
				if json.Unmarshal(toolPatch, &update) != nil || update.ID == "" {
					return errors.New("the Gemini tool patch identity is invalid")
				}
				for j, original := range message.ToolCalls {
					var tool geminiToolIdentity
					if json.Unmarshal(original, &tool) != nil || tool.ID != update.ID {
						continue
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(original, &fields); err != nil {
						return err
					}
					if len(update.Result) > 0 {
						fields["result"] = update.Result
					}
					changed, err := json.Marshal(fields)
					if err != nil {
						return err
					}
					message.ToolCalls[j] = changed
				}
			}
			if err := message.refreshOriginal(); err != nil {
				return err
			}
		}
	}
	for _, nested := range patch.Updates {
		if err := applyGeminiMessagePatch(session, nested); err != nil {
			return err
		}
	}
	if len(patch.RemoveIDs) > 0 {
		removed := make(map[string]bool, len(patch.RemoveIDs))
		for _, id := range patch.RemoveIDs {
			removed[id] = true
		}
		kept := make([]geminiMessage, 0, len(session.Messages))
		for _, message := range session.Messages {
			if !removed[message.ID] {
				kept = append(kept, message)
			}
		}
		session.Messages = kept
	}
	if len(patch.OrderIDs) > 0 {
		positions := make(map[string]geminiMessage, len(session.Messages))
		for _, message := range session.Messages {
			positions[message.ID] = message
		}
		ordered := make([]geminiMessage, 0, len(session.Messages))
		selected := make(map[string]bool, len(patch.OrderIDs))
		for _, id := range patch.OrderIDs {
			selected[id] = true
		}
		for _, message := range session.Messages {
			if !selected[message.ID] {
				ordered = append(ordered, message)
				delete(positions, message.ID)
			}
		}
		for _, id := range patch.OrderIDs {
			if message, exists := positions[id]; exists {
				ordered = append(ordered, message)
				delete(positions, id)
			}
		}
		session.Messages = ordered
	}
	return nil
}

func geminiMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	var output strings.Builder
	for _, part := range parts {
		output.WriteString(part.Text)
	}
	return output.String()
}

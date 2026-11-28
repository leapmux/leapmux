package providerkit

import "encoding/json"

// AnthropicContent is one reading of an Anthropic content array, the block
// list that Claude Code, Qoder, CodeBuddy, Amp, Pi, and Oh My Pi carry in
// message.content. Each provider joins the texts by its own browser rule.
type AnthropicContent struct {
	// Texts holds the text of each text block in order. A text that is not a
	// string reads as empty, as the browser's block readers read it.
	Texts []string
	// HasText reports a text block, whatever its text.
	HasText bool
	// HasToolUse reports a tool_use block. A content array with one is a
	// tool call, never a reply.
	HasToolUse bool
}

// ReadAnthropicContent reads the blocks of an Anthropic content array using
// exact key matching, as JavaScript extractors do. A block that is not an
// object is skipped.
func ReadAnthropicContent(blocks []json.RawMessage) AnthropicContent {
	var content AnthropicContent
	for _, raw := range blocks {
		fields, ok := RawObject(raw)
		if !ok {
			continue
		}
		switch ObjectString(fields, "type") {
		case "tool_use":
			content.HasToolUse = true
		case "text":
			content.HasText = true
			content.Texts = append(content.Texts, ObjectString(fields, "text"))
		}
	}
	return content
}

// AnthropicMessage is a frame whose message.content is an Anthropic content
// array, as the assistant frames of those providers are.
type AnthropicMessage struct {
	Type    string `json:"type"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

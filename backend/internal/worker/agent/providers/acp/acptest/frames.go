package acptest

import "encoding/json"

// Chunk builds a streamed chunk for the specified provider session.
func Chunk(sessionID, sessionUpdate, text string) []byte {
	content, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": sessionUpdate,
				"content":       map[string]string{"type": "text", "text": text},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return content
}

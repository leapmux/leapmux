package commandcode

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

const (
	methodInitialize    = "initialize"
	methodTurnStart     = "turn/start"
	methodTurnSteer     = "turn/steer"
	methodTurnInterrupt = "turn/interrupt"
	methodSetModel      = "session/set_model"
	methodSetEffort     = "session/set_effort"
	methodSessionState  = "session/state"
	bridgeFrameType     = "leapmux_commandcode_bridge"
	bridgeSecretEnv     = "LEAPMUX_COMMANDCODE_BRIDGE_SECRET"
	protocolVersion     = 1
)

type sessionView struct {
	ID             string  `json:"id"`
	Model          string  `json:"model"`
	Effort         *string `json:"effort"`
	PermissionMode string  `json:"permissionMode"`
}

type stateResponse struct {
	ProtocolVersion int         `json:"protocolVersion"`
	Session         sessionView `json:"session"`
}

type nativeEvent struct {
	Type         string          `json:"type"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	Input        json.RawMessage `json:"input"`
	Content      json.RawMessage `json:"content"`
	Result       json.RawMessage `json:"result"`
	Partial      json.RawMessage `json:"partial"`
	Text         string          `json:"text"`
	Delta        string          `json:"delta"`
	Message      string          `json:"message"`
	HookOutput   string          `json:"hookOutput"`
	Error        json.RawMessage `json:"error"`
	Model        string          `json:"model"`
	Mode         string          `json:"mode"`
	Usage        *nativeUsage    `json:"usage"`
	Background   bool            `json:"background"`
	SubagentType string          `json:"subagentType"`
	Description  string          `json:"description"`
	Status       string          `json:"status"`
	Trigger      string          `json:"trigger"`
	Outcome      string          `json:"outcome"`
	TokensSaved  int64           `json:"tokensSaved"`
}

type nativeUsage struct {
	Input      int64 `json:"inputTokens"`
	Output     int64 `json:"outputTokens"`
	CacheRead  int64 `json:"cacheReadTokens"`
	CacheWrite int64 `json:"cacheWriteTokens"`
}

type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

func nativeText(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var text string
	for _, block := range blocks {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}

func eventFromFrame(raw []byte) (*nativeEvent, error) {
	var frame struct {
		Type  string      `json:"type"`
		Event nativeEvent `json:"event"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, err
	}
	if frame.Type != contracts.CommandCodeFrameKindEvent {
		return nil, fmt.Errorf("the Command Code frame is not an event")
	}
	return &frame.Event, nil
}

func nativeErrorText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var data struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &data) == nil {
		return data.Message
	}
	return ""
}

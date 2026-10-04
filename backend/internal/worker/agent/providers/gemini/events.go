package gemini

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Native mode text shares a message shape with model text. Only a completed
// native mode tool record or a setter reply can change the permission mode.
func (a *Agent) handleSessionUpdate(sessionID string, _ agent.ProviderServices, update json.RawMessage) bool {
	a.childMu.Lock()
	children := a.children
	a.childMu.Unlock()
	if children != nil && a.PromptActive() {
		children.observe(sessionID, update)
	}
	var message struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &message) != nil {
		return false
	}
	if message.SessionUpdate == "tool_call" || message.SessionUpdate == "tool_call_update" {
		if !a.PromptActive() {
			return false
		}
		for name, mode := range map[string]string{contracts.GeminiToolEnterPlanMode: contracts.GeminiModePlan, contracts.GeminiToolExitPlanMode: contracts.GeminiModeDefault} {
			if strings.HasPrefix(message.ToolCallID, name+"__") && len(message.ToolCallID) > len(name)+2 {
				a.modeMu.Lock()
				if a.modeCalls == nil {
					a.modeCalls = make(map[string]geminiModeCall)
				}
				a.modeCalls[sessionID+"\x00"+message.ToolCallID] = geminiModeCall{value: mode, generation: a.modeGeneration}
				a.modeMu.Unlock()
				break
			}
		}
		return false
	}
	if message.SessionUpdate != "agent_message_chunk" || message.Content.Type != "text" {
		return false
	}
	mode, found := strings.CutPrefix(message.Content.Text, "[MODE_UPDATE] ")
	if !found {
		return false
	}
	a.modeMu.Lock()
	defer a.modeMu.Unlock()
	for key, expected := range a.modeCalls {
		if strings.HasPrefix(key, sessionID+"\x00") && mode == expected.value {
			return true
		}
	}
	return false
}

func (a *Agent) observeNativeMode(sessionID string, record json.RawMessage) {
	if sessionID != a.CurrentSessionID() {
		return
	}
	var tool struct {
		geminiToolIdentity
		ResultDisplay string `json:"resultDisplay"`
	}
	if json.Unmarshal(record, &tool) != nil {
		return
	}
	var parts []struct {
		FunctionResponse struct {
			Response struct {
				Output string `json:"output"`
			} `json:"response"`
		} `json:"functionResponse"`
	}
	if json.Unmarshal(tool.Result, &parts) != nil {
		return
	}
	a.modeMu.Lock()
	defer a.modeMu.Unlock()
	key := sessionID + "\x00" + tool.ID
	mode, tracked := a.modeCalls[key]
	if tracked {
		delete(a.modeCalls, key)
	}
	if !tracked || tool.Status != "success" || mode.generation != a.modeGeneration {
		return
	}
	valid := len(parts) == 1 && tool.Name == contracts.GeminiToolEnterPlanMode && mode.value == contracts.GeminiModePlan && parts[0].FunctionResponse.Response.Output == "Switching to Plan mode."
	valid = valid || tool.Name == contracts.GeminiToolExitPlanMode && mode.value == contracts.GeminiModeDefault && strings.HasPrefix(tool.ResultDisplay, "Plan approved: ")
	if valid {
		a.ObserveCurrentMode(mode.value)
	}
}

func (a *Agent) resetNativeModes() {
	a.modeMu.Lock()
	a.modeCalls = nil
	a.modeMu.Unlock()
}

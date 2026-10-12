package gemini

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// handleSessionUpdate reads each update of a Gemini session before the base
// draws it. It returns true for an update that it consumes.
//
// Native mode text shares a message shape with model text. Only a completed
// native mode tool record or a setter reply can change the permission mode.
func (a *Agent) handleSessionUpdate(sessionID string, _ agent.ProviderServices, update json.RawMessage) bool {
	// One read serves each decision below, so a prompt that starts during this
	// call cannot change half of them.
	promptActive := a.PromptActive()
	a.childMu.Lock()
	children := a.children
	a.childMu.Unlock()
	if children != nil && promptActive {
		children.observe(sessionID, update)
	}
	// The content stays raw here: a text chunk states one content block, and a
	// tool call states an array of them.
	var message struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Content       json.RawMessage `json:"content"`
	}
	if json.Unmarshal(update, &message) != nil {
		return false
	}
	// Gemini sends conversation content outside a prompt from two emitters only,
	// both in packages/cli/src/acp/acpSession.ts:
	//
	//   - streamHistory, the replay of a session/load. Gemini does not await it
	//     before the session/load reply (upstream issue 28775), so all of the
	//     replay but its first frame arrives after the reply, while no prompt
	//     runs.
	//   - handleApprovalModeChanged, the `[MODE_UPDATE] <mode>` text of each
	//     mode change. An idle session/set_mode sends it while no prompt runs.
	//
	// Neither one is new conversation. The Worker copied the stored transcript
	// of the old tab into the resumed tab, and the setter reply applies a mode
	// change. So an idle conversation update of the current session stores
	// nothing. Without this rule, the base buffered the replayed answer, and the
	// next prompt stored the old answer and its own answer as ONE row.
	//
	// The rule reads the prompt state and is no completion boundary: Gemini
	// sends no frame that ends the replay, so nothing here knows that the replay
	// finished. A prompt that starts before the replay ends therefore receives
	// the rest of the replay, and nothing can separate the two. Only an awaited
	// replay (issue 28775) or a session/resume that sends no replay can remove
	// that case. Gemini CLI 0.62.0 and upstream main offer neither.
	if !promptActive && geminiConversationUpdate(message.SessionUpdate) && a.IsCurrentSession(sessionID) {
		return true
	}
	if message.SessionUpdate == contracts.ACPUpdateToolCall || message.SessionUpdate == contracts.ACPUpdateToolCallUpdate {
		if !promptActive {
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
	if message.SessionUpdate != contracts.ACPUpdateAgentMessageChunk {
		return false
	}
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &content) != nil || content.Type != "text" {
		return false
	}
	mode, found := strings.CutPrefix(content.Text, "[MODE_UPDATE] ")
	if !found {
		return false
	}
	a.modeMu.Lock()
	defer a.modeMu.Unlock()
	// A mode change that LeapMux requested sends this text before its reply,
	// also while a prompt runs.
	if a.takeModeEchoLocked(sessionID, mode) {
		return true
	}
	for key, expected := range a.modeCalls {
		if strings.HasPrefix(key, sessionID+"\x00") && mode == expected.value {
			return true
		}
	}
	return false
}

// geminiConversationUpdate reports whether an update of this type is
// conversation content that a turn draws. A user_message_chunk is not in the
// set, because the base already draws none.
func geminiConversationUpdate(updateType string) bool {
	switch updateType {
	case contracts.ACPUpdateAgentMessageChunk, contracts.ACPUpdateAgentThoughtChunk,
		contracts.ACPUpdateToolCall, contracts.ACPUpdateToolCallUpdate, contracts.ACPUpdatePlan:
		return true
	default:
		return false
	}
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

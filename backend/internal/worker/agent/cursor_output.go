package agent

import (
	"encoding/json"
	"log/slog"
	"strings"
)

const (
	CursorMethodAskQuestion   = "cursor/ask_question"
	CursorMethodCreatePlan    = "cursor/create_plan"
	cursorMethodUpdateTodos   = "cursor/update_todos"
	cursorMethodTask          = "cursor/task"
	cursorMethodGenerateImage = "cursor/generate_image"
)

func (a *CursorCLIAgent) handleConfigOptionUpdate(update json.RawMessage) {
	options := parseACPConfigOptions(update)
	if len(options) == 0 {
		return
	}

	a.mu.Lock()
	mode := syncACPConfigOptions(&a.model, &a.permissionMode, &a.availableModels, &a.availableModes, options, func(configID, value string) string {
		if configID == "model" {
			return normalizeCursorModelID(value)
		}
		return value
	})
	a.mu.Unlock()

	if mode != "" {
		a.sink.UpdatePermissionMode(mode)
	}
}

func (a *CursorCLIAgent) handleExtraMethod(line *parsedLine) bool {
	if !strings.HasPrefix(line.Method, "cursor/") {
		return false
	}

	idRaw, requestID, ok := ExtractJSONRPCID(line.Raw)
	if !ok {
		return true
	}

	switch line.Method {
	case CursorMethodAskQuestion:
		a.handleAskQuestionRequest(line.Raw, requestID)
		return true
	case CursorMethodCreatePlan:
		a.handleCreatePlanRequest(line.Raw, requestID)
		return true
	case cursorMethodUpdateTodos, cursorMethodTask, cursorMethodGenerateImage:
		if err := a.sendResponse(idRaw, map[string]interface{}{}); err != nil {
			slog.Warn("cursor extension ack failed", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
		return true
	default:
		if err := a.sendErrorResponse(idRaw, -32601, "Method not supported: "+line.Method); err != nil {
			slog.Warn("cursor extension method-not-found failed", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
		return true
	}
}

func (a *CursorCLIAgent) handleAskQuestionRequest(raw []byte, requestID string) {
	payload, ok := a.buildAskQuestionPayload(raw)
	if !ok {
		return
	}
	a.sink.PersistControlRequest(requestID, payload)
	a.sink.BroadcastControlRequest(requestID, payload)
}

func (a *CursorCLIAgent) buildAskQuestionPayload(raw []byte) ([]byte, bool) {
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("cursor ask_question unmarshal failed", "agent_id", a.agentID, "error", err)
		return nil, false
	}

	params, _ := payload["params"].(map[string]interface{})
	rawQuestions, _ := params["questions"].([]interface{})
	questions := make([]map[string]interface{}, 0, len(rawQuestions))
	for _, item := range rawQuestions {
		q, _ := item.(map[string]interface{})
		if q == nil {
			continue
		}
		rawOptions, _ := q["options"].([]interface{})
		options := make([]map[string]interface{}, 0, len(rawOptions))
		for _, rawOption := range rawOptions {
			option, _ := rawOption.(map[string]interface{})
			if option == nil {
				continue
			}
			mapped := map[string]interface{}{}
			if id, _ := option["id"].(string); id != "" {
				mapped["id"] = id
			}
			if label, _ := option["label"].(string); label != "" {
				mapped["label"] = label
			}
			options = append(options, mapped)
		}

		mapped := map[string]interface{}{
			"question": q["prompt"],
			"options":  options,
		}
		if id, _ := q["id"].(string); id != "" {
			mapped["id"] = id
		}
		if prompt, _ := q["prompt"].(string); prompt != "" {
			mapped["header"] = prompt
		}
		if allowMultiple, ok := q["allowMultiple"].(bool); ok {
			mapped["multiSelect"] = allowMultiple
		}
		questions = append(questions, mapped)
	}

	payload["request"] = map[string]interface{}{
		"tool_name": ToolNameAskUserQuestion,
		"input": map[string]interface{}{
			"questions": questions,
		},
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("cursor ask_question marshal failed", "agent_id", a.agentID, "error", err)
		return nil, false
	}
	return encoded, true
}

func (a *CursorCLIAgent) handleCreatePlanRequest(raw []byte, requestID string) {
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("cursor create_plan unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	payload["type"] = "cursor.create_plan"
	encoded, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("cursor create_plan marshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	a.sink.PersistControlRequest(requestID, encoded)
	a.sink.BroadcastControlRequest(requestID, encoded)
}

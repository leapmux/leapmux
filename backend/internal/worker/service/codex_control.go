package service

import (
	"encoding/json"
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func codexControlResponseRequestID(content []byte) string {
	var rpc struct {
		ID *json.Number `json:"id"`
	}
	if err := json.Unmarshal(content, &rpc); err == nil && rpc.ID != nil {
		return rpc.ID.String()
	}

	var cr struct {
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &cr); err == nil {
		return cr.Response.RequestID
	}

	return ""
}

func codexUserInputAnswersText(requestPayload, responseContent []byte) string {
	var req struct {
		Params struct {
			Questions []struct {
				ID     string `json:"id"`
				Header string `json:"header"`
			} `json:"questions"`
		} `json:"params"`
	}
	var resp struct {
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if json.Unmarshal(requestPayload, &req) != nil || json.Unmarshal(responseContent, &resp) != nil {
		return ""
	}

	if len(resp.Result.Answers) == 0 {
		return ""
	}

	labels := make(map[string]string, len(req.Params.Questions))
	order := make([]string, 0, len(req.Params.Questions))
	for _, q := range req.Params.Questions {
		key := strings.TrimSpace(q.ID)
		if key == "" {
			key = strings.TrimSpace(q.Header)
		}
		if key == "" {
			continue
		}
		label := strings.TrimSpace(q.Header)
		if label == "" {
			label = key
		}
		labels[key] = label
		order = append(order, key)
	}

	lines := make([]string, 0, len(resp.Result.Answers))
	seen := make(map[string]bool, len(resp.Result.Answers))
	appendLine := func(key string) {
		answer, ok := resp.Result.Answers[key]
		if !ok || seen[key] {
			return
		}
		parts := make([]string, 0, len(answer.Answers))
		for _, entry := range answer.Answers {
			if text := strings.TrimSpace(entry); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return
		}
		lines = append(lines, fmt.Sprintf("%s: %s", labels[key], strings.Join(parts, ", ")))
		seen[key] = true
	}

	for _, key := range order {
		appendLine(key)
	}
	for key := range resp.Result.Answers {
		if seen[key] {
			continue
		}
		if _, ok := labels[key]; !ok {
			labels[key] = key
		}
		appendLine(key)
	}

	return strings.Join(lines, "\n")
}

func codexFeedbackMessageText(responseContent []byte) string {
	var cr struct {
		Response struct {
			Response struct {
				Message string `json:"message"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(responseContent, &cr); err != nil {
		return ""
	}
	message := strings.TrimSpace(cr.Response.Response.Message)
	if message == "" || message == "Rejected by user." {
		return ""
	}
	return message
}

func (svc *Context) codexControlResponseDisplayText(agentID string, provider leapmuxv1.AgentProvider, content []byte) string {
	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		// handled below
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:
		return opencodeControlResponseDisplayText(content)
	default:
		return ""
	}

	reqID := codexControlResponseRequestID(content)
	if reqID == "" {
		return ""
	}

	cr, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	if err != nil {
		return ""
	}

	var payload struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(cr.Payload, &payload) != nil {
		return ""
	}

	if payload.Method == "item/tool/requestUserInput" {
		return codexUserInputAnswersText(cr.Payload, content)
	}

	return codexFeedbackMessageText(content)
}

// opencodeControlResponseDisplayText extracts a human-readable display text
// from an OpenCode permission response (e.g. "Allow once", "Reject").
func opencodeControlResponseDisplayText(content []byte) string {
	var resp struct {
		Result struct {
			Outcome struct {
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal(content, &resp) != nil {
		return ""
	}

	switch resp.Result.Outcome.OptionID {
	case "once":
		return "Allow once"
	case "always":
		return "Always allow"
	case "reject":
		return "Reject"
	default:
		return resp.Result.Outcome.OptionID
	}
}

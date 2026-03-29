package service

import (
	"encoding/json"
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func controlResponseRequestID(content []byte) string {
	var rpc struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(content, &rpc); err == nil && len(rpc.ID) > 0 && string(rpc.ID) != "null" {
		var str string
		if json.Unmarshal(rpc.ID, &str) == nil {
			return str
		}
		return strings.TrimSpace(string(rpc.ID))
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

func opencodeQuestionAnswersText(requestPayload, responseContent []byte) string {
	var req struct {
		Properties struct {
			Questions []struct {
				Header   string `json:"header"`
				Question string `json:"question"`
			} `json:"questions"`
		} `json:"properties"`
	}
	var resp struct {
		Result struct {
			Answers [][]string `json:"answers"`
		} `json:"result"`
	}
	if json.Unmarshal(requestPayload, &req) != nil || json.Unmarshal(responseContent, &resp) != nil {
		return ""
	}

	lines := make([]string, 0, len(resp.Result.Answers))
	for i, answers := range resp.Result.Answers {
		if len(answers) == 0 {
			continue
		}
		parts := make([]string, 0, len(answers))
		for _, answer := range answers {
			if text := strings.TrimSpace(answer); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			continue
		}

		label := fmt.Sprintf("Question %d", i+1)
		if i < len(req.Properties.Questions) {
			if header := strings.TrimSpace(req.Properties.Questions[i].Header); header != "" {
				label = header
			} else if question := strings.TrimSpace(req.Properties.Questions[i].Question); question != "" {
				label = question
			}
		}
		lines = append(lines, fmt.Sprintf("%s: %s", label, strings.Join(parts, ", ")))
	}
	return strings.Join(lines, "\n")
}

func (svc *Context) controlResponseDisplayText(agentID string, provider leapmuxv1.AgentProvider, content []byte) string {
	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		// handled below
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:
		// handled below
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI:
		// handled below
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_COPILOT_CLI:
		// handled below
	default:
		return ""
	}

	reqID := controlResponseRequestID(content)
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
		Type   string `json:"type"`
	}
	if json.Unmarshal(cr.Payload, &payload) != nil {
		return ""
	}

	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		if payload.Method == "item/tool/requestUserInput" {
			return codexUserInputAnswersText(cr.Payload, content)
		}
		return codexFeedbackMessageText(content)
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:
		if payload.Type == "question.asked" {
			return opencodeQuestionAnswersText(cr.Payload, content)
		}
		return acpPermissionResponseDisplayText(cr.Payload, content)
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_COPILOT_CLI:
		return acpPermissionResponseDisplayText(cr.Payload, content)
	default:
		return ""
	}
}

// acpPermissionResponseDisplayText extracts a human-readable display text from
// an ACP permission response by matching the selected optionId against the
// original request payload's option list.
func acpPermissionResponseDisplayText(requestPayload, responseContent []byte) string {
	var req struct {
		Params struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Name     string `json:"name"`
			} `json:"options"`
		} `json:"params"`
	}
	var resp struct {
		Result struct {
			Outcome struct {
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal(requestPayload, &req) != nil || json.Unmarshal(responseContent, &resp) != nil {
		return ""
	}

	optionID := strings.TrimSpace(resp.Result.Outcome.OptionID)
	if optionID == "" {
		return ""
	}
	for _, option := range req.Params.Options {
		if strings.TrimSpace(option.OptionID) == optionID {
			if name := strings.TrimSpace(option.Name); name != "" {
				return name
			}
			break
		}
	}

	switch optionID {
	case "once", "proceed_once":
		return "Allow once"
	case "always", "proceed_always":
		return "Always allow"
	case "reject", "cancel":
		return "Reject"
	default:
		return optionID
	}
}

package service

import (
	"encoding/json"
	"html"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/microcosm-cc/bluemonday"
)

// trackPlanModeToolUse inspects an assistant message for EnterPlanMode or
// ExitPlanMode tool_use blocks and records the tool_use_id for later matching
// against the tool_result confirmation.
func (h *OutputHandler) trackPlanModeToolUse(content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}
	for _, block := range msg.Message.Content {
		if block.Type != "tool_use" || block.ID == "" {
			continue
		}
		switch block.Name {
		case "EnterPlanMode":
			h.planModeToolUse.Store(block.ID, "plan")
		case "ExitPlanMode":
			h.planModeToolUse.Store(block.ID, "default")
		}
	}
}

// trackPlanFilePath inspects an assistant message for Write or Edit tool_use
// blocks whose file_path targets the agent's ~/.claude/plans/ directory,
// and persists the plan file path and compressed plan content to the DB.
func (h *OutputHandler) trackPlanFilePath(agentID string, content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					FilePath  string `json:"file_path"`
					Content   string `json:"content"`
					OldString string `json:"old_string"`
					NewString string `json:"new_string"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}

	for _, block := range msg.Message.Content {
		if block.Type != "tool_use" {
			continue
		}
		if block.Name != "Write" && block.Name != "Edit" {
			continue
		}
		filePath := block.Input.FilePath
		if filePath == "" {
			continue
		}

		agent, err := h.queries.GetAgentByID(bgCtx(), agentID)
		if err != nil || agent.HomeDir == "" {
			continue
		}

		planDir := agent.HomeDir + "/.claude/plans/"
		if !strings.HasPrefix(filePath, planDir) {
			continue
		}

		// Resolve plan content.
		// For Write, use the content from the tool_use input (available
		// immediately, before the tool executes and writes to disk).
		// For Edit, read from disk and apply the substitution.
		var planContentStr string
		if block.Name == "Write" && block.Input.Content != "" {
			planContentStr = block.Input.Content
		} else {
			data, readErr := os.ReadFile(filePath)
			if readErr == nil && len(data) > 0 {
				if block.Name == "Edit" {
					planContentStr = strings.Replace(string(data), block.Input.OldString, block.Input.NewString, 1)
				} else {
					planContentStr = string(data)
				}
			}
		}

		// Compress content and extract title.
		var compressed []byte
		var compression leapmuxv1.ContentCompression
		if planContentStr != "" {
			compressed, compression = msgcodec.Compress([]byte(planContentStr))
		}
		newPlanTitle := extractPlanTitle(planContentStr)
		// Preserve existing plan_title when the new content yields no title.
		if newPlanTitle == "" {
			newPlanTitle = agent.PlanTitle
		}

		// Persist plan file path, content, and title in a single UPDATE.
		// If the title changed and auto-rename applies, also update the
		// agent's display title atomically.
		shouldAutoRename := newPlanTitle != "" &&
			newPlanTitle != agent.Title &&
			(agent.Title == agent.PlanTitle ||
				regexp.MustCompile(`^Agent \d+$`).MatchString(agent.Title))
		if shouldAutoRename {
			if err := h.queries.UpdateAgentPlanAndTitle(bgCtx(), db.UpdateAgentPlanAndTitleParams{
				PlanFilePath:           filePath,
				PlanContent:            compressed,
				PlanContentCompression: compression,
				PlanTitle:              newPlanTitle,
				Title:                  newPlanTitle,
				ID:                     agentID,
			}); err != nil {
				slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
			} else {
				h.BroadcastNotification(agentID, map[string]interface{}{
					"type":  "agent_renamed",
					"title": newPlanTitle,
				})
			}
		} else {
			if err := h.queries.UpdateAgentPlan(bgCtx(), db.UpdateAgentPlanParams{
				PlanFilePath:           filePath,
				PlanContent:            compressed,
				PlanContentCompression: compression,
				PlanTitle:              newPlanTitle,
				ID:                     agentID,
			}); err != nil {
				slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
			}
		}

		// Only track the first matching plan file per message.
		return
	}
}

// detectPlanModeFromToolResult inspects a user message (tool_result) for
// confirmation of a previously tracked EnterPlanMode or ExitPlanMode tool_use.
// When a match is found, it updates the permission mode and broadcasts.
func (h *OutputHandler) detectPlanModeFromToolResult(agentID string, content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
		ToolUseResult *struct {
			Message  string `json:"message"`
			Plan     string `json:"plan"`
			FilePath string `json:"filePath"`
		} `json:"tool_use_result"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}

	for _, block := range msg.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}

		targetModeVal, ok := h.planModeToolUse.LoadAndDelete(block.ToolUseID)
		if !ok {
			continue
		}
		targetMode := targetModeVal.(string)

		resultText := ""
		if msg.ToolUseResult != nil {
			resultText = msg.ToolUseResult.Message
		}

		resultLower := strings.ToLower(resultText)
		confirmed := false
		if targetMode == "plan" && strings.Contains(resultLower, "entered plan mode") {
			confirmed = true
		} else if targetMode == "default" && strings.Contains(resultLower, "approved your plan") {
			confirmed = true
		}

		if confirmed {
			slog.Info("plan mode change confirmed via tool_result",
				"agent_id", agentID,
				"tool_use_id", block.ToolUseID,
				"mode", targetMode)

			// Fetch agent before updating to capture old mode.
			dbAgent, fetchErr := h.queries.GetAgentByID(bgCtx(), agentID)
			oldMode := ""
			if fetchErr == nil {
				oldMode = dbAgent.PermissionMode
			}

			_ = h.queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
				PermissionMode: targetMode,
				ID:             agentID,
			})

			// Broadcast statusChange so frontends update their permission mode display.
			if fetchErr == nil {
				sc := &leapmuxv1.AgentStatusChange{
					AgentId:        agentID,
					Status:         leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
					AgentSessionId: dbAgent.AgentSessionID,
					WorkerOnline:   true,
					PermissionMode: targetMode,
					Model:          dbAgent.Model,
					Effort:         dbAgent.Effort,
				}
				populateGitFileStatus(sc, dbAgent.WorkingDir)
				h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
					AgentId: agentID,
					Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
				})
			}

			// Broadcast settings_changed notification for the chat view.
			if oldMode != "" && oldMode != targetMode {
				h.BroadcastNotification(agentID, map[string]interface{}{
					"type": "settings_changed",
					"changes": map[string]interface{}{
						"permissionMode": map[string]string{"old": oldMode, "new": targetMode},
					},
				})
			}
		} else {
			truncated := resultText
			if len(truncated) > 64 {
				truncated = truncated[:64]
			}
			slog.Debug("plan mode tool_result did not contain expected confirmation",
				"agent_id", agentID,
				"tool_use_id", block.ToolUseID,
				"expected_mode", targetMode,
				"result_text", truncated)
		}
	}
}

// --- Plan title extraction ---

var (
	reHeading       = regexp.MustCompile(`^#{1,6}\s+`)
	reBold          = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	reItalic        = regexp.MustCompile(`\*(.+?)\*|_(.+?)_`)
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
	reInlineCode    = regexp.MustCompile("`(.+?)`")
	reImageLink     = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink          = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reWikiLink      = regexp.MustCompile(`\[\[(.+?)\]\]`)

	htmlPolicy = bluemonday.StrictPolicy()
)

// extractPlanTitle extracts a human-readable title from markdown plan content.
// It returns the first meaningful line, stripped of markdown formatting.
func extractPlanTitle(content string) string {
	// Skip YAML frontmatter.
	if strings.HasPrefix(content, "---\n") {
		if idx := strings.Index(content[4:], "\n---\n"); idx >= 0 {
			content = content[4+idx+5:]
		} else if strings.HasPrefix(content[4:], "---\n") {
			content = content[8:]
		}
	}

	// Find first non-empty line.
	var line string
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return ""
	}

	// Strip heading markers.
	line = reHeading.ReplaceAllString(line, "")

	// Strip markdown inline formatting.
	line = reBold.ReplaceAllString(line, "${1}${2}")
	line = reItalic.ReplaceAllString(line, "${1}${2}")
	line = reStrikethrough.ReplaceAllString(line, "${1}")
	line = reInlineCode.ReplaceAllString(line, "${1}")
	line = reImageLink.ReplaceAllString(line, "${1}")
	line = reLink.ReplaceAllString(line, "${1}")
	line = reWikiLink.ReplaceAllString(line, "${1}")

	// Strip HTML tags.
	line = htmlPolicy.Sanitize(line)

	// Decode HTML entities.
	line = html.UnescapeString(line)

	// Clean up whitespace and control characters.
	line = strings.TrimSpace(line)
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)

	// Truncate to 128 characters.
	if len([]rune(line)) > 128 {
		line = string([]rune(line)[:128])
	}

	return line
}

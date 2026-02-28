package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// clearAgentContext clears the agent's context. If the agent is active and
// its worker is connected, it is stopped and marked for a fresh restart
// (without a session ID); the context_cleared notification is broadcast
// after the restart completes. Otherwise (agent not active or worker
// disconnected), the session ID is cleared immediately and the notification
// is broadcast right away.
func (s *AgentService) clearAgentContext(ctx context.Context, agent *db.Agent, ws *db.Workspace) {
	s.resetUsageSnapshot(agent.ID)
	s.lastAgentStatus.Delete(agent.ID)

	// If the agent is active and the worker is connected, stop it and
	// let the restart cycle handle the rest.
	if agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		if conn := s.workerMgr.Get(agent.WorkerID); conn != nil {
			s.restartPending.Store(agent.ID, &RestartOptions{
				ClearSession: true,
			})
			_ = conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_AgentStop{
					AgentStop: &leapmuxv1.AgentStopRequest{
						WorkspaceId: agent.WorkspaceID,
						AgentId:     agent.ID,
					},
				},
			})
			return
		}
	}

	// Agent is not running or worker is disconnected — clear session ID
	// directly and notify.
	if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "",
		ID:             agent.ID,
	}); err != nil {
		slog.Warn("failed to clear agent session ID", "agent_id", agent.ID, "error", err)
	}
	s.broadcastNotification(ctx, agent.ID, map[string]interface{}{
		"type": "context_cleared",
	})
}

// buildPlanExecMessage constructs the synthetic user message sent to the agent
// after a context-clearing restart for plan execution. It includes the plan
// file path (when available) so the agent can re-read the plan if its context
// is later compressed.
func buildPlanExecMessage(planFilePath, planContent string) string {
	msg := "Execute the following plan:\n\n---\n\n" + planContent
	if planFilePath != "" {
		msg += "\n\n---\n\nThe above plan has been written to " + planFilePath + " — re-read it if needed."
	}
	return msg
}

// clearAgentContextForPlanExecution clears the agent's context and queues a
// synthetic user message containing the plan content to be sent after restart.
func (s *AgentService) clearAgentContextForPlanExecution(ctx context.Context, agent *db.Agent, ws *db.Workspace, planContent string) {
	s.resetUsageSnapshot(agent.ID)
	s.lastAgentStatus.Delete(agent.ID)

	opts := &RestartOptions{
		ClearSession:         true,
		SyntheticUserMessage: buildPlanExecMessage(agent.PlanFilePath, planContent),
		PlanExec:             true,
		PlanFilePath:         agent.PlanFilePath,
	}

	// If the agent is active and the worker is connected, stop it and
	// let the restart cycle handle the rest.
	if agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		if conn := s.workerMgr.Get(agent.WorkerID); conn != nil {
			s.restartPending.Store(agent.ID, opts)
			_ = conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_AgentStop{
					AgentStop: &leapmuxv1.AgentStopRequest{
						WorkspaceId: agent.WorkspaceID,
						AgentId:     agent.ID,
					},
				},
			})
			return
		}
	}

	// Agent is not running or worker is disconnected — clear session ID
	// directly and notify.
	if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "",
		ID:             agent.ID,
	}); err != nil {
		slog.Warn("failed to clear agent session ID", "agent_id", agent.ID, "error", err)
	}
	s.broadcastNotification(ctx, agent.ID, map[string]interface{}{
		"type": "context_cleared",
	})
	s.broadcastNotification(ctx, agent.ID, map[string]interface{}{
		"type":            "plan_execution",
		"context_cleared": true,
		"plan_file_path":  opts.PlanFilePath,
	})
}

// sendSyntheticUserMessage persists a hidden user message (not displayed in
// chat) and delivers it to the worker. Used to inject plan content after a
// context-clearing restart.
func (s *AgentService) sendSyntheticUserMessage(ctx context.Context, agentID string, content string) error {
	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Build hidden message content.
	contentJSON, _ := json.Marshal(map[string]interface{}{
		"content": content,
		"hidden":  true,
	})
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	msgID := id.Generate()
	now := time.Now()

	seq, err := s.queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           "",
		CreatedAt:          now,
	})
	if err != nil {
		return fmt.Errorf("persist synthetic message: %w", err)
	}

	// Broadcast to watchers (frontend checks hidden flag and skips rendering).
	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})

	// Deliver to worker.
	msgCtx, msgCancel := context.WithTimeout(ctx, s.timeoutCfg.APITimeout())
	defer msgCancel()

	agentNotFound, deliveryErr := s.deliverMessageToWorker(msgCtx, agent.WorkerID, agent.WorkspaceID, agentID, content)
	if agentNotFound {
		slog.Warn("synthetic message: agent not found on worker", "agent_id", agentID)
		return fmt.Errorf("agent not found on worker")
	}
	if deliveryErr != nil {
		s.setDeliveryError(ctx, agentID, msgID, deliveryErr.Error())
		return deliveryErr
	}

	return nil
}

// trackPlanModeToolUse inspects an assistant message for EnterPlanMode or
// ExitPlanMode tool_use blocks and records the tool_use_id for later matching
// against the tool_result confirmation.
func (s *AgentService) trackPlanModeToolUse(content []byte) {
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
			s.planModeToolUse.Store(block.ID, "plan")
		case "ExitPlanMode":
			s.planModeToolUse.Store(block.ID, "default")
		}
	}
}

// trackPlanFilePath inspects an assistant message for Write or Edit tool_use
// blocks whose file_path targets the agent's ~/.claude/plans/ directory,
// and persists the plan file path and compressed plan content to the DB.
func (s *AgentService) trackPlanFilePath(ctx context.Context, agentID string, content []byte) {
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

		agent, err := s.queries.GetAgentByID(ctx, agentID)
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
			if err := s.queries.UpdateAgentPlanAndTitle(ctx, db.UpdateAgentPlanAndTitleParams{
				PlanFilePath:           filePath,
				PlanContent:            compressed,
				PlanContentCompression: compression,
				PlanTitle:              newPlanTitle,
				Title:                  newPlanTitle,
				ID:                     agentID,
			}); err != nil {
				slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
			} else {
				s.broadcastNotification(ctx, agentID, map[string]interface{}{
					"type":  "agent_renamed",
					"title": newPlanTitle,
				})
			}
		} else {
			if err := s.queries.UpdateAgentPlan(ctx, db.UpdateAgentPlanParams{
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
// When a match is found, it calls setAgentPermissionMode to update the DB and
// broadcast a notification. It also intercepts ExitPlanMode tool_results that
// have a pending plan execution (set by SendControlResponse on approval).
func (s *AgentService) detectPlanModeFromToolResult(ctx context.Context, agentID string, content []byte) {
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

		// Check for plan execution pending (takes priority over planModeToolUse).
		if val, ok := s.planExecPending.LoadAndDelete(block.ToolUseID); ok {
			config := val.(*PlanExecConfig)
			close(config.Done) // cancel the timeout goroutine

			// Parse plan content from tool_use_result.
			planContent, filePath := extractPlanFromToolUseResult(msg.ToolUseResult)

			// Fallback 1: DB-persisted compressed plan content.
			if planContent == "" {
				if agent, err := s.queries.GetAgentByID(ctx, agentID); err == nil {
					if len(agent.PlanContent) > 0 {
						if decompressed, err := msgcodec.Decompress(agent.PlanContent, agent.PlanContentCompression); err == nil {
							planContent = string(decompressed)
						}
					}
					if planContent == "" && filePath == "" {
						filePath = agent.PlanFilePath
					}
				}
			}

			// Fallback 2: Read plan file from disk.
			if planContent == "" && filePath != "" {
				if data, err := os.ReadFile(filePath); err == nil && len(data) > 0 {
					planContent = string(data)
				}
			}

			if planContent != "" {
				s.initiatePlanExecRestart(ctx, agentID, planContent)
			} else {
				slog.Warn("plan execution: no plan content found, continuing with retained context",
					"agent_id", agentID, "tool_use_id", block.ToolUseID)
				planFilePath := filePath
				if planFilePath == "" {
					if a, err := s.queries.GetAgentByID(ctx, agentID); err == nil {
						planFilePath = a.PlanFilePath
					}
				}
				s.broadcastNotification(ctx, agentID, map[string]interface{}{
					"type":            "plan_execution",
					"context_cleared": false,
					"plan_file_path":  planFilePath,
				})
			}
			continue
		}

		targetModeVal, ok := s.planModeToolUse.LoadAndDelete(block.ToolUseID)
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
			s.setAgentPermissionMode(ctx, agentID, targetMode)
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

// extractPlanFromToolUseResult extracts plan content and file path from the
// ExitPlanMode tool_use_result.
func extractPlanFromToolUseResult(result *struct {
	Message  string `json:"message"`
	Plan     string `json:"plan"`
	FilePath string `json:"filePath"`
}) (plan, filePath string) {
	if result == nil {
		return "", ""
	}
	return result.Plan, result.FilePath
}

// initiatePlanExecRestart looks up the agent and workspace, then initiates a
// context-clearing restart with the plan content as a synthetic user message.
func (s *AgentService) initiatePlanExecRestart(ctx context.Context, agentID string, planContent string) {
	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		slog.Error("plan exec restart: get agent", "agent_id", agentID, "error", err)
		return
	}
	ws, err := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	if err != nil {
		slog.Error("plan exec restart: get workspace", "agent_id", agentID, "error", err)
		return
	}
	s.clearAgentContextForPlanExecution(ctx, &agent, &ws, planContent)
}

package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// agentShell returns the resolved default shell path for agent options.
func (svc *Context) agentShell() string {
	return terminal.ResolveDefaultShell()
}

// agentLoginShell returns whether the agent should use interactive+login shell flags.
func (svc *Context) agentLoginShell() bool {
	return svc.UseLoginShell
}

// registerAgentHandlers registers all agent-related inner RPC handlers.
func registerAgentHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("OpenAgent", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.OpenAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := id.Generate()

		workingDir := expandTilde(r.GetWorkingDir())
		if workingDir == "" {
			workingDir = svc.HomeDir
		}

		// Apply git-mode options (create-worktree, checkout-branch, etc.).
		gm, gmErr := svc.applyGitMode(workingDir, &r)
		if gmErr != nil {
			slog.Error("failed to apply git mode for agent", "error", gmErr)
			sendInternalError(sender, gmErr.Error())
			return
		}
		workingDir = gm.WorkingDir
		worktreeID := gm.WorktreeID

		// Resolve default model based on agent provider.
		agentProvider := r.GetAgentProvider()
		if agentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			agentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
		}
		model := modelOrDefault(r.GetModel(), agentProvider)

		// Ensure the channel knows about this workspace so that
		// subsequent WatchEvents calls can access the agent.
		svc.Channels.AddAccessibleWorkspaceID(sender.ChannelID(), r.GetWorkspaceId())

		// Create the agent record in the database.
		if err := svc.Queries.CreateAgent(bgCtx(), db.CreateAgentParams{
			ID:                 agentID,
			WorkspaceID:        r.GetWorkspaceId(),
			WorkingDir:         workingDir,
			HomeDir:            svc.HomeDir,
			Title:              r.GetTitle(),
			Model:              model,
			SystemPrompt:       r.GetSystemPrompt(),
			Effort:             r.GetEffort(),
			CodexSandboxPolicy: r.GetCodexSandboxPolicy(),
			AgentProvider:      agentProvider,
		}); err != nil {
			slog.Error("failed to create agent", "error", err)
			sendInternalError(sender, "failed to create agent")
			return
		}

		// Start the agent process.
		agentOpts := agent.Options{
			AgentID:            agentID,
			Model:              model,
			Effort:             r.GetEffort(),
			WorkingDir:         workingDir,
			ResumeSessionID:    r.GetAgentSessionId(),
			CodexSandboxPolicy: r.GetCodexSandboxPolicy(),
			StartupTimeout:     svc.agentStartupTimeout(),
			Shell:              svc.agentShell(),
			LoginShell:         svc.agentLoginShell(),
			HomeDir:            svc.HomeDir,
			AgentProvider:      agentProvider,
		}

		sink := svc.Output.NewSink(agentID, agentProvider)

		confirmedMode, err := svc.Agents.StartAgent(bgCtx(), agentOpts, sink)
		if err != nil {
			slog.Error("failed to start agent", "agent_id", agentID, "error", err)
			// Mark the agent as closed since the process failed to start.
			_ = svc.Queries.CloseAgent(bgCtx(), agentID)
			sendInternalError(sender, "failed to start agent: "+err.Error())
			return
		}

		slog.Info("agent started", "agent_id", agentID, "model", model, "permission_mode", confirmedMode)

		// Persist the confirmed permission mode.
		if confirmedMode != "" {
			_ = svc.Queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
				PermissionMode: confirmedMode,
				ID:             agentID,
			})
		}

		// Register the agent tab with the worktree.
		svc.registerTabForWorktree(worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)

		// Fetch the created agent for the response.
		dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
		if err != nil {
			slog.Error("failed to fetch created agent", "error", err)
			sendInternalError(sender, "failed to fetch created agent")
			return
		}

		permissionMode := confirmedMode
		if permissionMode == "" {
			permissionMode = dbAgent.PermissionMode
		}

		sendProtoResponse(sender, &leapmuxv1.OpenAgentResponse{
			Agent: agentToProto(&dbAgent, permissionMode, svc.WorkerID, true, gitutil.GetGitStatus(dbAgent.WorkingDir), svc.Agents.AvailableModels(agentID, dbAgent.AgentProvider), svc.Agents.AvailableOptionGroups(dbAgent.AgentProvider)),
		})
	})

	d.Register("CloseAgent", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()

		// Stop the agent process.
		svc.Agents.StopAgent(agentID)

		// Mark the agent as closed in the database.
		if err := svc.Queries.CloseAgent(bgCtx(), agentID); err != nil {
			slog.Error("failed to close agent in DB", "agent_id", agentID, "error", err)
			sendInternalError(sender, "failed to close agent")
			return
		}

		// Handle worktree cleanup.
		cleanup := svc.unregisterTabAndCleanup(leapmuxv1.TabType_TAB_TYPE_AGENT, agentID, r.GetWorktreeAction())
		sendProtoResponse(sender, &leapmuxv1.CloseAgentResponse{
			WorktreeCleanupPending: cleanup.NeedsConfirmation,
			WorktreePath:           cleanup.WorktreePath,
			WorktreeId:             cleanup.WorktreeID,
		})
	})

	d.Register("SendAgentMessage", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendAgentMessageRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		content := r.GetContent()

		// Control requests (set_permission_mode, interrupt) are sent as raw
		// input to Claude Code's stdin — not persisted as chat messages.
		if isControlRequest(content) {
			svc.handleControlRequestMessage(agentID, content)
			sendProtoResponse(sender, &leapmuxv1.SendAgentMessageResponse{})
			return
		}

		// Reject user messages shorter than 2 characters (after trimming
		// whitespace) to avoid 400 errors from the Anthropic API.
		trimmed := strings.TrimSpace(content)
		if utf8.RuneCountInString(trimmed) < 2 {
			sendInvalidArgument(sender, "message must be at least 2 characters")
			return
		}

		dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
		if err != nil {
			sendInternalError(sender, "agent not found")
			return
		}

		messageID := id.Generate()
		now := time.Now().UTC()

		// Wrap user content in the standard threadWrapper envelope so the
		// frontend can parse it consistently. The inner format is a plain
		// object with a "content" string field (no "type"), which the
		// frontend classifies as user_content and renders as markdown.
		innerJSON, _ := json.Marshal(map[string]string{"content": content})
		wrapped := wrapContent(innerJSON)
		compressed, compressionType := msgcodec.Compress(wrapped)

		// Persist the user message.
		seq, err := svc.Queries.CreateMessage(bgCtx(), db.CreateMessageParams{
			ID:                 messageID,
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
			Content:            compressed,
			ContentCompression: compressionType,
			ThreadID:           "",
			AgentProvider:      dbAgent.AgentProvider,
			CreatedAt:          now,
		})
		if err != nil {
			slog.Error("failed to persist message", "agent_id", agentID, "error", err)
			sendInternalError(sender, "failed to persist message")
			return
		}

		// Check for leapmux-level slash commands (e.g. /clear) that
		// Claude Code does not handle natively.
		isSlashClear := trimmed == "/clear" || trimmed == "/reset"

		// Attempt to send the message to the agent process (unless it's
		// a command that leapmux handles itself).
		var deliveryError string
		if isSlashClear {
			// /clear: restart the agent with a fresh context.
			svc.handleClearContext(agentID)
		} else if !svc.Agents.HasAgent(agentID) {
			// Agent is not running — try to auto-start it (e.g. after worker restart).
			if startErr := svc.ensureAgentRunning(agentID); startErr != nil {
				deliveryError = "agent is not running"
			} else if sendErr := svc.Agents.SendInput(agentID, content); sendErr != nil {
				slog.Error("failed to send input to agent after auto-start", "agent_id", agentID, "error", sendErr)
				deliveryError = sendErr.Error()
			}
		} else if sendErr := svc.Agents.SendInput(agentID, content); sendErr != nil {
			slog.Error("failed to send input to agent", "agent_id", agentID, "error", sendErr)
			deliveryError = sendErr.Error()
		}
		if deliveryError != "" {
			_ = svc.Queries.SetMessageDeliveryError(bgCtx(), db.SetMessageDeliveryErrorParams{
				DeliveryError: deliveryError,
				ID:            messageID,
				AgentID:       agentID,
			})
		}

		sendProtoResponse(sender, &leapmuxv1.SendAgentMessageResponse{})

		// Broadcast the user message to all watchers so it appears in
		// every connected frontend's chat view.
		userMsg := &leapmuxv1.AgentChatMessage{
			Id:                 messageID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
			Content:            compressed,
			ContentCompression: compressionType,
			Seq:                seq,
			DeliveryError:      deliveryError,
			AgentProvider:      dbAgent.AgentProvider,
			CreatedAt:          timefmt.Format(now),
		}
		svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event: &leapmuxv1.AgentEvent_AgentMessage{
				AgentMessage: userMsg,
			},
		})

		// Broadcast delivery error separately (frontend uses both events).
		if deliveryError != "" {
			svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_MessageError{
					MessageError: &leapmuxv1.AgentMessageError{
						AgentId:   agentID,
						MessageId: messageID,
						Error:     deliveryError,
					},
				},
			})
		}
	})

	d.Register("ListAgents", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListAgentsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		tabIDs := r.GetTabIds()
		if len(tabIDs) == 0 {
			sendProtoResponse(sender, &leapmuxv1.ListAgentsResponse{})
			return
		}

		agents, err := svc.Queries.ListAgentsByIDs(bgCtx(), tabIDs)
		if err != nil {
			slog.Error("failed to list agents", "tab_ids", tabIDs, "error", err)
			sendInternalError(sender, "failed to list agents")
			return
		}

		// Filter by access control: only return agents in accessible workspaces.
		var accessibleWsIDs map[string]bool
		if chID := sender.ChannelID(); chID != "" {
			accessibleWsIDs = svc.Channels.AccessibleWorkspaceIDs(chID)
		}

		// Compute git status concurrently for all agents.
		gitStatuses := make([]*gitutil.GitStatus, len(agents))
		var wg sync.WaitGroup
		for i := range agents {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				gitStatuses[idx] = gitutil.GetGitStatus(agents[idx].WorkingDir)
			}(i)
		}
		wg.Wait()

		protoAgents := make([]*leapmuxv1.AgentInfo, 0, len(agents))
		for i := range agents {
			if accessibleWsIDs != nil && !accessibleWsIDs[agents[i].WorkspaceID] {
				continue
			}
			protoAgents = append(protoAgents, agentToProto(&agents[i], agents[i].PermissionMode, svc.WorkerID, svc.Agents.HasAgent(agents[i].ID), gitStatuses[i], svc.Agents.AvailableModels(agents[i].ID, agents[i].AgentProvider), svc.Agents.AvailableOptionGroups(agents[i].AgentProvider)))
		}

		sendProtoResponse(sender, &leapmuxv1.ListAgentsResponse{
			Agents: protoAgents,
		})
	})

	d.Register("ListAgentMessages", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListAgentMessagesRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		limit := int64(r.GetLimit())
		if limit <= 0 || limit > 50 {
			limit = 50
		}

		agentID := r.GetAgentId()

		// Return empty for closed agents.
		if agentRow, err := svc.Queries.GetAgentByID(bgCtx(), agentID); err == nil && agentRow.ClosedAt.Valid {
			sendProtoResponse(sender, &leapmuxv1.ListAgentMessagesResponse{})
			return
		}

		afterSeq := r.GetAfterSeq()
		beforeSeq := r.GetBeforeSeq()

		var dbMessages []db.Message
		var queryErr error

		if beforeSeq > 0 {
			// BACKWARD: fetch messages with seq < before_seq, returned in descending order.
			dbMessages, queryErr = svc.Queries.ListMessagesByAgentIDReverse(bgCtx(), db.ListMessagesByAgentIDReverseParams{
				AgentID: agentID,
				Seq:     beforeSeq,
				Limit:   limit + 1, // Fetch one extra to determine has_more.
			})
		} else if afterSeq > 0 {
			// FORWARD: fetch messages with seq > after_seq in ascending order.
			dbMessages, queryErr = svc.Queries.ListMessagesByAgentID(bgCtx(), db.ListMessagesByAgentIDParams{
				AgentID: agentID,
				Seq:     afterSeq,
				Limit:   limit + 1,
			})
		} else {
			// LATEST: fetch the most recent messages.
			dbMessages, queryErr = svc.Queries.ListLatestMessagesByAgentID(bgCtx(), db.ListLatestMessagesByAgentIDParams{
				AgentID: agentID,
				Limit:   limit + 1,
			})
		}

		if queryErr != nil {
			slog.Error("failed to list messages", "agent_id", agentID, "error", queryErr)
			sendInternalError(sender, "failed to list messages")
			return
		}

		hasMore := int64(len(dbMessages)) > limit
		if hasMore {
			dbMessages = dbMessages[:limit]
		}

		// For BACKWARD and LATEST queries, results come in descending order;
		// reverse them so the response is always in ascending seq order.
		if beforeSeq > 0 || (afterSeq == 0 && beforeSeq == 0) {
			for i, j := 0, len(dbMessages)-1; i < j; i, j = i+1, j-1 {
				dbMessages[i], dbMessages[j] = dbMessages[j], dbMessages[i]
			}
		}

		protoMessages := make([]*leapmuxv1.AgentChatMessage, 0, len(dbMessages))
		for i := range dbMessages {
			protoMessages = append(protoMessages, messageToProto(&dbMessages[i]))
		}

		sendProtoResponse(sender, &leapmuxv1.ListAgentMessagesResponse{
			Messages: protoMessages,
			HasMore:  hasMore,
		})
	})

	d.Register("RenameAgent", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.RenameAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		if _, err := svc.Queries.RenameAgent(bgCtx(), db.RenameAgentParams{
			Title: r.GetTitle(),
			ID:    r.GetAgentId(),
		}); err != nil {
			slog.Error("failed to rename agent", "agent_id", r.GetAgentId(), "error", err)
			sendInternalError(sender, "failed to rename agent")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.RenameAgentResponse{})
	})

	d.Register("DeleteAgentMessage", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.DeleteAgentMessageRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		if err := svc.Queries.DeleteMessageByAgentAndID(bgCtx(), db.DeleteMessageByAgentAndIDParams{
			AgentID: r.GetAgentId(),
			ID:      r.GetMessageId(),
		}); err != nil {
			slog.Error("failed to delete message", "agent_id", r.GetAgentId(), "message_id", r.GetMessageId(), "error", err)
			sendInternalError(sender, "failed to delete message")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.DeleteAgentMessageResponse{})

		// Broadcast deletion to all watchers.
		svc.Watchers.BroadcastAgentEvent(r.GetAgentId(), &leapmuxv1.AgentEvent{
			AgentId: r.GetAgentId(),
			Event: &leapmuxv1.AgentEvent_MessageDeleted{
				MessageDeleted: &leapmuxv1.AgentMessageDeleted{
					AgentId:   r.GetAgentId(),
					MessageId: r.GetMessageId(),
				},
			},
		})
	})

	d.Register("UpdateAgentSettings", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.UpdateAgentSettingsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()

		// Fetch current agent to get existing values for unchanged fields.
		dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
		if err != nil {
			slog.Error("failed to fetch agent for settings update", "agent_id", agentID, "error", err)
			sendNotFoundError(sender, "agent not found")
			return
		}

		newModel := r.GetModel()
		if newModel == "" {
			newModel = dbAgent.Model
		}
		newEffort := r.GetEffort()
		if newEffort == "" {
			newEffort = dbAgent.Effort
		}
		newPermissionMode := r.GetPermissionMode()
		if newPermissionMode == "" {
			newPermissionMode = dbAgent.PermissionMode
		}
		newCodexSandboxPolicy := r.GetCodexSandboxPolicy()
		if newCodexSandboxPolicy == "" {
			newCodexSandboxPolicy = dbAgent.CodexSandboxPolicy
		}

		// Update the DB.
		if err := svc.Queries.UpdateAgentModelAndEffort(bgCtx(), db.UpdateAgentModelAndEffortParams{
			Model:  newModel,
			Effort: newEffort,
			ID:     agentID,
		}); err != nil {
			slog.Error("failed to update agent settings", "agent_id", agentID, "error", err)
			sendInternalError(sender, "failed to update agent settings")
			return
		}
		if newPermissionMode != dbAgent.PermissionMode {
			_ = svc.Queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
				PermissionMode: newPermissionMode,
				ID:             agentID,
			})
		}
		if newCodexSandboxPolicy != dbAgent.CodexSandboxPolicy {
			_ = svc.Queries.SetAgentCodexSandboxPolicy(bgCtx(), db.SetAgentCodexSandboxPolicyParams{
				CodexSandboxPolicy: newCodexSandboxPolicy,
				ID:                 agentID,
			})
		}

		// If the agent is currently running, try a live update first.
		// Providers that support it (e.g. Codex) apply settings to the
		// next turn without a restart. Providers that don't (e.g. Claude
		// Code) return false and we fall back to stop+restart.
		if svc.Agents.HasAgent(agentID) {
			updated := svc.Agents.UpdateSettings(agentID, &leapmuxv1.UpdateAgentSettingsRequest{
				Model:              newModel,
				Effort:             newEffort,
				PermissionMode:     newPermissionMode,
				CodexSandboxPolicy: newCodexSandboxPolicy,
			})

			if !updated {
				svc.Agents.StopAndWaitAgent(agentID)

				// Only resume the session if user messages have actually been
				// exchanged. The agent process assigns a session ID during the
				// initialize handshake, but no server-side conversation exists
				// until the user sends a message. Resuming with a session ID
				// that has no conversation causes "No conversation found" errors.
				resumeSessionID := ""
				if dbAgent.AgentSessionID != "" {
					hasMessages, err := svc.Queries.HasUserMessages(bgCtx(), agentID)
					if err == nil && hasMessages != 0 {
						resumeSessionID = dbAgent.AgentSessionID
					}
				}

				agentOpts := agent.Options{
					AgentID:            agentID,
					Model:              newModel,
					Effort:             newEffort,
					WorkingDir:         dbAgent.WorkingDir,
					ResumeSessionID:    resumeSessionID,
					PermissionMode:     newPermissionMode,
					CodexSandboxPolicy: newCodexSandboxPolicy,
					StartupTimeout:     svc.agentStartupTimeout(),
					Shell:              svc.agentShell(),
					LoginShell:         svc.agentLoginShell(),
					HomeDir:            svc.HomeDir,
					AgentProvider:      dbAgent.AgentProvider,
				}

				sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)

				if _, err := svc.Agents.StartAgent(bgCtx(), agentOpts, sink); err != nil {
					slog.Error("failed to restart agent with new settings",
						"agent_id", agentID, "error", err)
					// Clear stale session ID so ensureAgentRunning won't try
					// to resume a non-existent session on the next message.
					_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
						AgentSessionID: "",
						ID:             agentID,
					})
					svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
						"type":  "agent_error",
						"error": "Failed to restart agent with new settings: " + err.Error(),
					})
				} else {
					slog.Info("agent restarted with new settings",
						"agent_id", agentID, "model", newModel, "effort", newEffort)
				}
			}
		}

		// Broadcast settings_changed notification for the chat view.
		// Include display labels so the frontend can show human-readable names
		// without maintaining its own label maps.
		modelLabel, effortLabel := svc.settingsDisplayLabels(agentID, dbAgent.AgentProvider)
		changes := map[string]interface{}{}
		if dbAgent.Model != newModel {
			oldID := modelOrDefault(dbAgent.Model, dbAgent.AgentProvider)
			changes["model"] = map[string]string{
				"old": oldID, "new": newModel,
				"oldLabel": modelLabel(oldID), "newLabel": modelLabel(newModel),
			}
		}
		if dbAgent.Effort != newEffort {
			oldID := effortOrDefault(dbAgent.Effort, dbAgent.AgentProvider)
			changes["effort"] = map[string]string{
				"old": oldID, "new": newEffort,
				"oldLabel": effortLabel(oldID), "newLabel": effortLabel(newEffort),
			}
		}
		if dbAgent.PermissionMode != newPermissionMode {
			changes["permissionMode"] = map[string]string{
				"old": dbAgent.PermissionMode, "new": newPermissionMode,
				"oldLabel": permissionModeLabel(dbAgent.PermissionMode, dbAgent.AgentProvider), "newLabel": permissionModeLabel(newPermissionMode, dbAgent.AgentProvider),
			}
		}
		if dbAgent.CodexSandboxPolicy != newCodexSandboxPolicy {
			changes["codexSandboxPolicy"] = map[string]string{
				"old": dbAgent.CodexSandboxPolicy, "new": newCodexSandboxPolicy,
				"oldLabel": optionLabel("codexSandboxPolicy", dbAgent.CodexSandboxPolicy, dbAgent.AgentProvider), "newLabel": optionLabel("codexSandboxPolicy", newCodexSandboxPolicy, dbAgent.AgentProvider),
			}
		}
		if len(changes) > 0 {
			svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
				"type":    "settings_changed",
				"changes": changes,
			})
		}

		sendProtoResponse(sender, &leapmuxv1.UpdateAgentSettingsResponse{})
	})

	d.Register("SendControlResponse", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendControlResponseRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		content := r.GetContent()

		// Detect plan mode changes from the control response before
		// forwarding to the agent. This mirrors the main-branch Hub logic
		// that updated permission mode based on EnterPlanMode/ExitPlanMode
		// approval or rejection.
		svc.handleControlResponsePlanMode(agentID, content)

		if err := svc.Agents.SendRawInput(agentID, content); err != nil {
			slog.Error("failed to send control response to agent",
				"agent_id", agentID, "error", err)
			sendNotFoundError(sender, "agent not found or not running")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.SendControlResponseResponse{})
	})

	// WatchEvents registers the channel as a watcher for agent/terminal events.
	// It replays messages since afterSeq, sends a statusChange marker,
	// replays pending control requests, then streams live events.
	// Access control: only agents/terminals in workspaces accessible to the
	// user (via the channel's accessible_workspace_ids) are watched.
	d.Register("WatchEvents", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.WatchEventsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		channelID := sender.ChannelID()
		allowedWorkspaces := svc.Channels.AccessibleWorkspaceIDs(channelID)

		// Create an EventWatcher for this stream.
		watcher := &EventWatcher{
			ChannelID: channelID,
			Sender:    sender,
		}

		// Filter agents by access control and register watchers FIRST
		// so no broadcasts are missed during the replay phase.
		var verifiedAgents []*leapmuxv1.WatchAgentEntry
		var rejectedAgentIDs []string
		for _, agentEntry := range r.GetAgents() {
			agentRow, err := svc.Queries.GetAgentByID(bgCtx(), agentEntry.GetAgentId())
			if err != nil || !allowedWorkspaces[agentRow.WorkspaceID] || agentRow.ClosedAt.Valid {
				rejectedAgentIDs = append(rejectedAgentIDs, agentEntry.GetAgentId())
				continue
			}
			svc.Watchers.WatchAgent(agentEntry.GetAgentId(), watcher)
			verifiedAgents = append(verifiedAgents, agentEntry)
		}

		// Filter terminals by access control and register watchers.
		var verifiedTerminalIDs []string
		var rejectedTerminalIDs []string
		for _, termID := range r.GetTerminalIds() {
			termRow, err := svc.Queries.GetTerminal(bgCtx(), termID)
			if err != nil || !allowedWorkspaces[termRow.WorkspaceID] || termRow.ClosedAt.Valid {
				rejectedTerminalIDs = append(rejectedTerminalIDs, termID)
				continue
			}
			svc.Watchers.WatchTerminal(termID, watcher)
			verifiedTerminalIDs = append(verifiedTerminalIDs, termID)
		}

		// Log any rejected entities for diagnostics.
		if len(rejectedAgentIDs) > 0 || len(rejectedTerminalIDs) > 0 {
			slog.Warn("WatchEvents: some requested entities not accessible",
				"rejected_agents", rejectedAgentIDs,
				"rejected_terminals", rejectedTerminalIDs,
				"verified_agents", len(verifiedAgents),
				"verified_terminals", len(verifiedTerminalIDs))
		}

		// If ALL requested entities were rejected, send a stream error
		// so the frontend can retry. We use SendStream (not SendError)
		// because the frontend dispatches stream correlation IDs to
		// streamListeners, not pendingRequests.
		if len(verifiedAgents) == 0 && len(verifiedTerminalIDs) == 0 {
			_ = sender.SendStream(&leapmuxv1.InnerStreamMessage{
				IsError:      true,
				ErrorCode:    5, // NOT_FOUND
				ErrorMessage: fmt.Sprintf("agents %v and/or terminals %v not found or not accessible", rejectedAgentIDs, rejectedTerminalIDs),
			})
			return
		}

		// Process each verified agent entry: replay messages, send status.
		for _, agentEntry := range verifiedAgents {
			agentID := agentEntry.GetAgentId()
			afterSeq := agentEntry.GetAfterSeq()

			// Replay messages with seq > afterSeq (up to 50).
			if afterSeq >= 0 {
				messages, err := svc.Queries.ListMessagesByAgentID(bgCtx(), db.ListMessagesByAgentIDParams{
					AgentID: agentID,
					Seq:     afterSeq,
					Limit:   50,
				})
				if err != nil {
					slog.Error("failed to list messages for replay", "agent_id", agentID, "error", err)
				} else {
					for i := range messages {
						broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
							Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
								AgentEvent: &leapmuxv1.AgentEvent{
									AgentId: agentID,
									Event: &leapmuxv1.AgentEvent_AgentMessage{
										AgentMessage: messageToProto(&messages[i]),
									},
								},
							},
						})
					}
				}
			}

			// Send a statusChange marker (signals end of message replay).
			dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
			if err != nil {
				slog.Error("failed to fetch agent for status", "agent_id", agentID, "error", err)
			} else {
				status := leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE
				if svc.Agents.HasAgent(agentID) {
					status = leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
				}
				sc := &leapmuxv1.AgentStatusChange{
					AgentId:        agentID,
					Status:         status,
					AgentSessionId: dbAgent.AgentSessionID,
					WorkerOnline:   true,
					PermissionMode: dbAgent.PermissionMode,
					Model:          modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
					Effort:         dbAgent.Effort,
					GitStatus:      gitStatusToProto(gitutil.GetGitStatus(dbAgent.WorkingDir)),
					AgentProvider:  dbAgent.AgentProvider,
				}
				broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: &leapmuxv1.AgentEvent{
							AgentId: agentID,
							Event: &leapmuxv1.AgentEvent_StatusChange{
								StatusChange: sc,
							},
						},
					},
				})
			}

			// Replay pending control requests.
			controlReqs, err := svc.Queries.ListControlRequestsByAgentID(bgCtx(), agentID)
			if err != nil {
				slog.Error("failed to list control requests for replay", "agent_id", agentID, "error", err)
			} else {
				for _, cr := range controlReqs {
					broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
						Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
							AgentEvent: &leapmuxv1.AgentEvent{
								AgentId: agentID,
								Event: &leapmuxv1.AgentEvent_ControlRequest{
									ControlRequest: &leapmuxv1.AgentControlRequest{
										AgentId:   agentID,
										RequestId: cr.RequestID,
										Payload:   cr.Payload,
									},
								},
							},
						},
					})
				}
			}

			// Send catch-up complete sentinel so the client knows replay
			// for this agent is done and can transition to live phase.
			broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
				Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
					AgentEvent: &leapmuxv1.AgentEvent{
						AgentId: agentID,
						Event: &leapmuxv1.AgentEvent_CatchUpComplete{
							CatchUpComplete: &leapmuxv1.CatchUpComplete{},
						},
					},
				},
			})
		}

		// Send initial screen snapshot for each verified terminal.
		for _, termID := range verifiedTerminalIDs {
			if screen := svc.Terminals.ScreenSnapshot(termID); len(screen) > 0 {
				broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
						TerminalEvent: &leapmuxv1.TerminalEvent{
							TerminalId: termID,
							Event: &leapmuxv1.TerminalEvent_Data{
								Data: &leapmuxv1.TerminalData{
									Data:       screen,
									IsSnapshot: true,
								},
							},
						},
					},
				})
			}
		}

		// Stream stays open — events will be pushed via watcher.Sender.SendStream().
		// The handler returns immediately; cleanup happens when the channel closes.
	})
}

// agentToProto converts a DB Agent to a proto AgentInfo.
func agentToProto(a *db.Agent, permissionMode, workerID string, isRunning bool, gs *gitutil.GitStatus, availableModels []*leapmuxv1.AvailableModel, availableOptionGroups []*leapmuxv1.AvailableOptionGroup) *leapmuxv1.AgentInfo {
	status := leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE
	if isRunning {
		status = leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
	}
	info := &leapmuxv1.AgentInfo{
		Id:                    a.ID,
		WorkspaceId:           a.WorkspaceID,
		Title:                 a.Title,
		Model:                 modelOrDefault(a.Model, a.AgentProvider),
		Status:                status,
		WorkingDir:            a.WorkingDir,
		PermissionMode:        permissionMode,
		Effort:                a.Effort,
		AgentSessionId:        a.AgentSessionID,
		HomeDir:               a.HomeDir,
		WorkerId:              workerID,
		CreatedAt:             timefmt.Format(a.CreatedAt),
		GitStatus:             gitStatusToProto(gs),
		AgentProvider:         a.AgentProvider,
		AvailableModels:       availableModels,
		AvailableOptionGroups: availableOptionGroups,
		CodexSandboxPolicy:    a.CodexSandboxPolicy,
	}

	if a.ClosedAt.Valid {
		info.ClosedAt = timefmt.Format(a.ClosedAt.Time)
	}

	return info
}

// gitStatusToProto converts a gitutil.GitStatus to a proto AgentGitStatus.
// Returns nil if gs is nil.
func gitStatusToProto(gs *gitutil.GitStatus) *leapmuxv1.AgentGitStatus {
	if gs == nil {
		return nil
	}
	return &leapmuxv1.AgentGitStatus{
		Branch:      gs.Branch,
		Ahead:       int32(gs.Ahead),
		Behind:      int32(gs.Behind),
		Conflicted:  gs.Conflicted,
		Stashed:     gs.Stashed,
		Deleted:     gs.Deleted,
		Renamed:     gs.Renamed,
		Modified:    gs.Modified,
		TypeChanged: gs.TypeChanged,
		Added:       gs.Added,
		Untracked:   gs.Untracked,
		OriginUrl:   gs.OriginURL,
	}
}

// handleClearContext implements the /clear command by restarting the agent
// without resuming the previous session, giving it a fresh context window.
func (svc *Context) handleClearContext(agentID string) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("clear context: failed to fetch agent", "agent_id", agentID, "error", err)
		return
	}

	// Stop the running agent and wait for it to fully exit so that
	// StartAgent below doesn't fail with "agent already running".
	svc.Agents.StopAndWaitAgent(agentID)

	// Restart the agent with a fresh context.
	// Don't clear agentSessionId before starting — the frontend uses it for
	// isWatchable. On success, handleSystemInit will overwrite it with the
	// new session ID. On failure, clear it so ensureAgentRunning won't try
	// to resume a stale session.
	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	if _, err := svc.Agents.StartAgent(bgCtx(), agent.Options{
		AgentID:            agentID,
		Model:              modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:             dbAgent.Effort,
		WorkingDir:         dbAgent.WorkingDir,
		PermissionMode:     dbAgent.PermissionMode,
		CodexSandboxPolicy: dbAgent.CodexSandboxPolicy,
		StartupTimeout:     svc.agentStartupTimeout(),
		Shell:              svc.agentShell(),
		LoginShell:         svc.agentLoginShell(),
		HomeDir:            svc.HomeDir,
		AgentProvider:      dbAgent.AgentProvider,
	}, sink); err != nil {
		slog.Error("clear context: failed to restart agent", "agent_id", agentID, "error", err)
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: "",
			ID:             agentID,
		})
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":  "agent_error",
			"error": "Failed to restart agent after clearing context: " + err.Error(),
		})
	} else {
		slog.Info("clear context: agent restarted successfully", "agent_id", agentID)
	}

	// Broadcast a context_cleared notification.
	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": "context_cleared",
	})
}

// ensureAgentRunning starts the agent process if it is not already running.
// It fetches the agent configuration from the DB and resumes the session
// if a session ID is stored (e.g. after worker restart).
func (svc *Context) ensureAgentRunning(agentID string) error {
	if svc.Agents.HasAgent(agentID) {
		return nil
	}

	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("ensureAgentRunning: failed to fetch agent", "agent_id", agentID, "error", err)
		return fmt.Errorf("agent not found: %w", err)
	}

	// Only resume the session if user messages have actually been exchanged.
	// The agent process assigns a session ID during startup, but no
	// server-side conversation may exist until a message is sent.
	resumeSessionID := ""
	if dbAgent.AgentSessionID != "" {
		hasMessages, err := svc.Queries.HasUserMessages(bgCtx(), agentID)
		if err == nil && hasMessages != 0 {
			resumeSessionID = dbAgent.AgentSessionID
		}
	}

	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	if _, err := svc.Agents.StartAgent(bgCtx(), agent.Options{
		AgentID:            agentID,
		Model:              modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:             dbAgent.Effort,
		WorkingDir:         dbAgent.WorkingDir,
		ResumeSessionID:    resumeSessionID,
		PermissionMode:     dbAgent.PermissionMode,
		CodexSandboxPolicy: dbAgent.CodexSandboxPolicy,
		StartupTimeout:     svc.agentStartupTimeout(),
		Shell:              svc.agentShell(),
		LoginShell:         svc.agentLoginShell(),
		HomeDir:            svc.HomeDir,
		AgentProvider:      dbAgent.AgentProvider,
	}, sink); err != nil {
		slog.Error("ensureAgentRunning: failed to start agent", "agent_id", agentID, "error", err)
		return err
	}

	slog.Info("ensureAgentRunning: agent started", "agent_id", agentID)
	return nil
}

// handleControlRequestMessage handles a control_request JSON message sent
// as a user message. Control requests (set_permission_mode, interrupt, etc.)
// are forwarded as raw input to the agent's stdin, not wrapped in a user
// message envelope.
func (svc *Context) handleControlRequestMessage(agentID, content string) {
	// If agent is not running, handle special cases locally.
	if !svc.Agents.HasAgent(agentID) {
		if mode, ok := parseSetPermissionMode(content); ok {
			svc.setAgentPermissionMode(agentID, mode)
			return
		}
		if isInterruptRequest(content) {
			// Agent is already gone — nothing to interrupt.
			return
		}
		// Other control requests need the agent running.
		if err := svc.ensureAgentRunning(agentID); err != nil {
			slog.Error("failed to start agent for control request", "agent_id", agentID, "error", err)
			return
		}
	}

	// Send as raw input to the agent's stdin.
	if err := svc.Agents.SendRawInput(agentID, []byte(content)); err != nil {
		slog.Error("failed to send control request to agent", "agent_id", agentID, "error", err)
	}
}

// setAgentPermissionMode updates the agent's permission mode in the DB
// and broadcasts a statusChange + settings_changed notification.
func (svc *Context) setAgentPermissionMode(agentID, mode string) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("set permission mode: agent not found", "agent_id", agentID, "error", err)
		return
	}

	oldMode := dbAgent.PermissionMode
	if err := svc.Queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
		PermissionMode: mode,
		ID:             agentID,
	}); err != nil {
		slog.Error("set permission mode: DB update failed", "agent_id", agentID, "error", err)
		return
	}

	// Broadcast statusChange so frontends update their settings display.
	svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: &leapmuxv1.AgentStatusChange{
				AgentId:        agentID,
				Status:         leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED,
				AgentSessionId: dbAgent.AgentSessionID,
				WorkerOnline:   true,
				PermissionMode: mode,
				Model:          modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
				Effort:         dbAgent.Effort,
				GitStatus:      gitStatusToProto(gitutil.GetGitStatus(dbAgent.WorkingDir)),
				AgentProvider:  dbAgent.AgentProvider,
			},
		},
	})

	// Broadcast settings_changed notification for the chat view.
	if oldMode != "" && oldMode != mode {
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type": "settings_changed",
			"changes": map[string]interface{}{
				"permissionMode": map[string]string{
					"old": oldMode, "new": mode,
					"oldLabel": permissionModeLabel(oldMode, dbAgent.AgentProvider), "newLabel": permissionModeLabel(mode, dbAgent.AgentProvider),
				},
			},
		})
	}
}

// handleControlResponsePlanMode detects plan mode changes from control
// responses. When the frontend approves/rejects an EnterPlanMode or
// ExitPlanMode control request, this updates the permission mode and
// initiates plan execution as needed.
func (svc *Context) handleControlResponsePlanMode(agentID string, content []byte) {
	var crPayload struct {
		PermissionMode string `json:"permissionMode"`
		Response       struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &crPayload); err != nil {
		return
	}

	reqID := crPayload.Response.RequestID
	if reqID == "" {
		return
	}

	// Look up the original control request to get the tool_name.
	cr, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	if err != nil {
		return
	}

	var crBody struct {
		Request struct {
			ToolName  string `json:"tool_name"`
			ToolUseID string `json:"tool_use_id"`
		} `json:"request"`
	}
	if json.Unmarshal(cr.Payload, &crBody) != nil {
		return
	}
	toolName := crBody.Request.ToolName
	toolUseID := crBody.Request.ToolUseID

	// Look up the agent's provider for message persistence.
	dbAgent, dbErr := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if dbErr != nil {
		return
	}

	// Persist a display message for the control response.
	action := "approved"
	if crPayload.Response.Response.Behavior == "deny" {
		action = "rejected"
	}
	displayContent := map[string]interface{}{
		"isSynthetic": true,
		"controlResponse": map[string]string{
			"action":  action,
			"comment": crPayload.Response.Response.Message,
		},
	}
	displayJSON, _ := json.Marshal(displayContent)
	merged := false
	if toolUseID != "" {
		merged = svc.Output.mergeIntoThread(agentID, dbAgent.AgentProvider, toolUseID, displayJSON)
	}
	if !merged {
		if err := svc.Output.persistAndBroadcast(agentID, dbAgent.AgentProvider, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, displayJSON, ""); err != nil {
			slog.Warn("failed to persist control response notification", "agent_id", agentID, "error", err)
		}
	}

	// Detect plan mode changes from control responses (agent-initiated).
	if crPayload.Response.Response.Behavior == "allow" {
		switch toolName {
		case "EnterPlanMode":
			svc.setAgentPermissionMode(agentID, "plan")
		case "ExitPlanMode":
			// Determine target permission mode from control_response.
			targetMode := "acceptEdits"
			if crPayload.PermissionMode != "" {
				targetMode = crPayload.PermissionMode
			}
			svc.setAgentPermissionMode(agentID, targetMode)

			// Remove the planModeToolUse entry so detectPlanModeFromToolResult
			// does not override the mode we just set.
			if toolUseID != "" {
				svc.Output.planModeToolUse.Delete(toolUseID)
			}

			// Initiate plan execution: stop agent, clear context, restart
			// with plan content as a synthetic user message.
			go svc.initiatePlanExecution(agentID, targetMode)
		}
	}

	// Delete the answered control request.
	_ = svc.Queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
}

// initiatePlanExecution stops the agent, clears its context, and restarts
// it with the plan content as a synthetic user message. This enables
// "plan mode" where the agent executes the approved plan with fresh context.
func (svc *Context) initiatePlanExecution(agentID string, targetMode string) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("plan exec: failed to fetch agent", "agent_id", agentID, "error", err)
		return
	}

	// Read plan content from DB (compressed).
	var planContent string
	if len(dbAgent.PlanContent) > 0 {
		if decompressed, dErr := msgcodec.Decompress(dbAgent.PlanContent, dbAgent.PlanContentCompression); dErr == nil {
			planContent = string(decompressed)
		}
	}

	// Fallback: read plan file from disk.
	if planContent == "" && dbAgent.PlanFilePath != "" {
		if data, readErr := os.ReadFile(dbAgent.PlanFilePath); readErr == nil && len(data) > 0 {
			planContent = string(data)
		}
	}

	if planContent == "" {
		slog.Warn("plan exec: no plan content found, broadcasting notification without restart",
			"agent_id", agentID)
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":           "plan_execution",
			"plan_file_path": dbAgent.PlanFilePath,
		})
		return
	}

	// Wait for the control_response to be delivered to the agent before
	// stopping it. The agent needs to process the approval and output its
	// tool_result before we kill it.
	time.Sleep(2 * time.Second)

	// Stop the running agent and wait for it to fully exit so that
	// StartAgent below doesn't fail with "agent already running".
	svc.Agents.StopAndWaitAgent(agentID)

	// Broadcast context_cleared and plan_execution as separate notifications.
	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": "context_cleared",
	})
	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type":           "plan_execution",
		"plan_file_path": dbAgent.PlanFilePath,
	})

	// Restart agent with plan content.
	// Don't clear agentSessionId before starting — the frontend uses it for
	// isWatchable. On success, handleSystemInit will overwrite it with the
	// new session ID. On failure, clear it so ensureAgentRunning won't try
	// to resume a stale session.
	planMsg := "Execute the following plan:\n\n---\n\n" + planContent
	if dbAgent.PlanFilePath != "" {
		planMsg += "\n\n---\n\nThe above plan has been written to " + dbAgent.PlanFilePath + " — re-read it if needed."
	}

	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	if _, err := svc.Agents.StartAgent(bgCtx(), agent.Options{
		AgentID:            agentID,
		Model:              modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:             dbAgent.Effort,
		WorkingDir:         dbAgent.WorkingDir,
		PermissionMode:     targetMode,
		CodexSandboxPolicy: dbAgent.CodexSandboxPolicy,
		StartupTimeout:     svc.agentStartupTimeout(),
		Shell:              svc.agentShell(),
		LoginShell:         svc.agentLoginShell(),
		HomeDir:            svc.HomeDir,
		AgentProvider:      dbAgent.AgentProvider,
	}, sink); err != nil {
		slog.Error("plan exec: failed to restart agent", "agent_id", agentID, "error", err)
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: "",
			ID:             agentID,
		})
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":  "agent_error",
			"error": "Failed to restart agent for plan execution: " + err.Error(),
		})
		return
	}

	slog.Info("plan exec: agent restarted successfully", "agent_id", agentID)

	// Send plan content as user message.
	if err := svc.Agents.SendInput(agentID, planMsg); err != nil {
		slog.Error("plan exec: failed to send plan content", "agent_id", agentID, "error", err)
	}
}

// isControlRequest checks if the content is a control_request JSON message
// (e.g. set_permission_mode, interrupt). These are sent as raw input to
// Claude Code's stdin and not persisted as chat messages.
func isControlRequest(content string) bool {
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Type == "control_request"
}

// parseSetPermissionMode checks if a control_request is a set_permission_mode
// request and returns the requested mode. Returns ("", false) if not a match.
func parseSetPermissionMode(content string) (string, bool) {
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
			Mode    string `json:"mode"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return "", false
	}
	if msg.Request.Subtype != "set_permission_mode" || msg.Request.Mode == "" {
		return "", false
	}
	return msg.Request.Mode, true
}

// isInterruptRequest checks if a control_request has subtype "interrupt".
func isInterruptRequest(content string) bool {
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Request.Subtype == "interrupt"
}

// broadcastWatchEvent sends a WatchEventsResponse as a stream message.
func broadcastWatchEvent(sender *channel.Sender, resp *leapmuxv1.WatchEventsResponse) {
	slog.Debug("stream payload", "payload", protojson.Format(resp))
	payload, err := proto.Marshal(resp)
	if err != nil {
		slog.Error("failed to marshal WatchEventsResponse", "error", err)
		return
	}
	_ = sender.SendStream(&leapmuxv1.InnerStreamMessage{
		Payload: payload,
	})
}

// messageToProto converts a DB Message to a proto AgentChatMessage.
func messageToProto(m *db.Message) *leapmuxv1.AgentChatMessage {
	msg := &leapmuxv1.AgentChatMessage{
		Id:                 m.ID,
		Role:               leapmuxv1.MessageRole(m.Role),
		Content:            m.Content,
		Seq:                m.Seq,
		DeliveryError:      m.DeliveryError,
		ContentCompression: leapmuxv1.ContentCompression(m.ContentCompression),
		AgentProvider:      m.AgentProvider,
		CreatedAt:          timefmt.Format(m.CreatedAt),
	}

	if m.UpdatedAt.Valid {
		msg.UpdatedAt = timefmt.Format(m.UpdatedAt.Time)
	}

	return msg
}

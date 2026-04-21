package service

import (
	"context"
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
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/validate"
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

		if r.GetWorkspaceId() == "" {
			sendInvalidArgument(sender, "workspace_id is required")
			return
		}
		if !svc.requireAccessibleWorkspace(sender, r.GetWorkspaceId()) {
			return
		}

		if err := validate.ValidateSessionID(r.GetAgentSessionId()); err != nil {
			sendInvalidArgument(sender, err.Error())
			return
		}

		agentID := id.Generate()
		agent.TraceStartupPhase(agentID, "handler_begin")

		workingDir := expandTilde(r.GetWorkingDir())
		if workingDir == "" {
			workingDir = svc.HomeDir
		}

		// Apply git-mode options (create-worktree, checkout-branch, etc.).
		// This is kept on the sync path so git errors (dirty tree, unknown
		// branch, etc.) surface as immediate RPC errors the caller can react
		// to with a dialog.
		gm, gmErr := svc.applyGitMode(workingDir, &r)
		if gmErr != nil {
			slog.Error("failed to apply git mode for agent", "error", gmErr)
			sendInternalError(sender, gmErr.Error())
			return
		}
		// Rolled back iff the sync prologue fails before handing ownership
		// to the startup goroutine. Once the goroutine is running it owns
		// rollback on its own failure path.
		syncSucceeded := false
		defer func() {
			if !syncSucceeded {
				svc.rollbackGitMode(gm)
			}
		}()
		workingDir = gm.WorkingDir
		worktreeID := gm.WorktreeID

		// Resolve default model based on agent provider.
		agentProvider := r.GetAgentProvider()
		if agentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			agentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
		}
		model := modelOrDefault(r.GetModel(), agentProvider)
		effort := effortOrDefault(r.GetEffort(), model, agentProvider)
		extraSettings := resolveCodexExtras(mergeExtraSettings(nil, r.GetExtraSettings()), agentProvider)

		// Track whether this agent was created via session resume.
		resumed := ptrconv.BoolToInt64(r.GetAgentSessionId() != "")

		agent.TraceStartupPhase(agentID, "gitmode_applied")

		// Create the agent record in the database.
		if err := svc.createAgentRecord(bgCtx(), db.CreateAgentParams{
			ID:            agentID,
			WorkspaceID:   r.GetWorkspaceId(),
			WorkingDir:    workingDir,
			HomeDir:       svc.HomeDir,
			Title:         r.GetTitle(),
			Model:         model,
			SystemPrompt:  r.GetSystemPrompt(),
			Effort:        effort,
			ExtraSettings: marshalExtraSettings(extraSettings),
			AgentProvider: agentProvider,
			Resumed:       resumed,
		}); err != nil {
			slog.Error("failed to create agent", "error", err)
			sendInternalError(sender, "failed to create agent")
			return
		}

		// Register before startup so CloseAgent during startup still
		// unregisters correctly.
		svc.registerTabForWorktree(worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)

		// Subprocess-dependent fields on the returned Agent are filled in
		// later via an AgentStatusChange{status=ACTIVE} event.
		dbAgent, err := svc.getAgentByID(bgCtx(), agentID)
		if err != nil {
			slog.Error("failed to fetch created agent", "error", err)
			sendInternalError(sender, "failed to fetch created agent")
			return
		}

		startupCtx, cancel := context.WithCancel(context.Background())
		svc.AgentStartup.begin(agentID, cancel)

		// Broadcast STARTING before returning so any already-subscribed
		// watcher sees the transition (the frontend just created the tab
		// and its WatchEvents stream is already live).
		svc.broadcastAgentStartupStatus(&dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, "")

		agentOpts := agent.Options{
			AgentID:         agentID,
			Model:           model,
			Effort:          effort,
			WorkingDir:      workingDir,
			ResumeSessionID: r.GetAgentSessionId(),
			ExtraSettings:   extraSettings,
			StartupTimeout:  svc.agentStartupTimeout(),
			APITimeout:      svc.agentAPITimeout(),
			Shell:           svc.agentShell(),
			LoginShell:      svc.agentLoginShell(),
			HomeDir:         svc.HomeDir,
			AgentProvider:   agentProvider,
		}

		syncSucceeded = true
		agent.TraceStartupPhase(agentID, "before_response")
		sendProtoResponse(sender, &leapmuxv1.OpenAgentResponse{
			Agent: svc.agentToProto(&dbAgent, dbAgent.PermissionMode, false, gitutil.GetGitStatus(dbAgent.WorkingDir), svc.Agents.AvailableModels(agentID, dbAgent.AgentProvider), svc.Agents.AvailableOptionGroups(agentID, dbAgent.AgentProvider)),
		})
		agent.TraceStartupPhase(agentID, "response_sent")

		// Kick off subprocess startup in the background.
		go svc.runAgentStartup(startupCtx, agentID, gm, agentOpts, model, effort, extraSettings)
	})

	d.Register("CloseAgent", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		if _, ok := svc.requireAccessibleAgent(sender, agentID); !ok {
			return
		}

		// Cancel any in-flight startup so the goroutine aborts the
		// subprocess handshake cleanly.
		svc.AgentStartup.cancelAndClear(agentID)

		// Stop the agent process.
		svc.Agents.StopAgent(agentID)

		// Clean up per-agent output handler state.
		svc.Output.CleanupAgent(agentID)

		// Mark the agent as closed in the database.
		if err := svc.Queries.CloseAgent(bgCtx(), agentID); err != nil {
			slog.Error("failed to close agent in DB", "agent_id", agentID, "error", err)
			sendInternalError(sender, "failed to close agent")
			return
		}

		svc.unregisterTabAndCleanup(leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
		sendProtoResponse(sender, &leapmuxv1.CloseAgentResponse{})
	})

	d.Register("SendAgentMessage", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendAgentMessageRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		dbAgent, ok := svc.requireAccessibleAgent(sender, agentID)
		if !ok {
			return
		}

		// Reject sends only on permanent startup failure — STARTING
		// messages are queued on the frontend and dispatched on the
		// status transition to ACTIVE. A STARTING-state send gate on
		// the server would race with the ACTIVE broadcast that fires
		// from the output sink before runAgentStartup's bookkeeping
		// completes; ensureAgentRunning already restarts crashed
		// subprocesses on demand. Also reject when the persisted
		// startup_error is set (covers worker restart: the in-memory
		// registry was wiped but the DB remembers the failure).
		if status, _, ok := svc.AgentStartup.status(agentID); ok && status == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
			sendFailedPrecondition(sender, "agent failed to start; open a new agent")
			return
		}
		if dbAgent.StartupError != "" && !svc.Agents.HasAgent(agentID) {
			sendFailedPrecondition(sender, "agent failed to start; open a new agent")
			return
		}

		content := r.GetContent()
		attachments := r.GetAttachments()

		// Validate text: at least 1 character when no attachments,
		// or allow empty text when attachments are present.
		trimmed := strings.TrimSpace(content)
		if len(attachments) == 0 && utf8.RuneCountInString(trimmed) < 1 {
			sendInvalidArgument(sender, "message must be at least 1 character")
			return
		}

		// Validate total attachment size (max 10 MB).
		const maxAttachmentSize = 10 * 1024 * 1024
		var totalSize int
		for _, a := range attachments {
			totalSize += len(a.GetData())
		}
		if totalSize > maxAttachmentSize {
			sendInvalidArgument(sender, "total attachment size exceeds 10 MB")
			return
		}

		// Pre-resolve the resume session ID BEFORE persisting the user
		// message. HasUserMessages must run before the current message is
		// written; otherwise the just-persisted message is counted as a
		// prior conversation and --resume is used for a session that never
		// had any messages (e.g. after an app restart on an idle tab).
		resumeSessionID := svc.resolveResumeSessionID(agentID, dbAgent.AgentSessionID, dbAgent.Resumed)

		attachments, err := agent.NormalizeAttachmentsForProvider(
			leapmuxv1.AgentProvider(dbAgent.AgentProvider),
			attachments,
		)
		if err != nil {
			sendInvalidArgument(sender, err.Error())
			return
		}

		messageID := id.Generate()
		now := time.Now().UTC()

		// Store user content as a plain JSON object with a "content" field,
		// which the frontend classifies as user_content and renders as markdown.
		// When attachments are present, include their metadata (filename + mime_type)
		// but not the raw binary data (too large for DB storage).
		var innerJSON []byte
		if len(attachments) > 0 {
			type attachmentMeta struct {
				Filename string `json:"filename"`
				MimeType string `json:"mime_type"`
			}
			meta := make([]attachmentMeta, len(attachments))
			for i, a := range attachments {
				meta[i] = attachmentMeta{Filename: a.GetFilename(), MimeType: a.GetMimeType()}
			}
			innerJSON, err = json.Marshal(map[string]interface{}{"content": content, "attachments": meta})
			if err != nil {
				slog.Warn("user message marshal failed", "agent_id", agentID, "error", err)
			}
		} else {
			innerJSON, err = json.Marshal(map[string]string{"content": content})
			if err != nil {
				slog.Warn("user message marshal failed", "agent_id", agentID, "error", err)
			}
		}
		compressed, compressionType := msgcodec.Compress(innerJSON)

		// Persist the user message.
		seq, err := svc.Queries.CreateMessage(bgCtx(), db.CreateMessageParams{
			ID:                 messageID,
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
			Content:            compressed,
			ContentCompression: compressionType,
			Depth:              0,
			SpanID:             "",
			ParentSpanID:       "",
			SpanLines:          "[]",
			SpanColor:          0,
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
		isSlashClear := trimmed == "/clear" || trimmed == "/reset" || trimmed == "/new"

		userMsg := &leapmuxv1.AgentChatMessage{
			Id:                 messageID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
			Content:            compressed,
			ContentCompression: compressionType,
			Seq:                seq,
			AgentProvider:      dbAgent.AgentProvider,
			CreatedAt:          timefmt.Format(now),
		}

		// For /clear, broadcast the user message before restarting so live
		// watchers never see context_cleared ahead of the triggering command.
		if isSlashClear {
			svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_AgentMessage{
					AgentMessage: userMsg,
				},
			})
		}

		// Attempt to send the message to the agent process (unless it's
		// a command that leapmux handles itself).
		var deliveryError string
		if isSlashClear {
			// /clear: restart the agent with a fresh context.
			svc.handleClearContext(agentID)
		} else if !svc.Agents.HasAgent(agentID) {
			// Agent is not running — try to auto-start it (e.g. after worker restart).
			if startErr := svc.ensureAgentRunning(agentID, &resumeSessionID); startErr != nil {
				deliveryError = "agent is not running"
			} else if sendErr := svc.Agents.SendInput(agentID, content, attachments); sendErr != nil {
				slog.Error("failed to send input to agent after auto-start", "agent_id", agentID, "error", sendErr)
				deliveryError = sendErr.Error()
			}
		} else if sendErr := svc.Agents.SendInput(agentID, content, attachments); sendErr != nil {
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
		if !isSlashClear {
			userMsg.DeliveryError = deliveryError
			svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_AgentMessage{
					AgentMessage: userMsg,
				},
			})
		}

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

	d.Register("SendAgentRawMessage", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendAgentRawMessageRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		dbAgent, ok := svc.requireAccessibleAgent(sender, agentID)
		if !ok {
			return
		}
		content := r.GetContent()
		if dbAgent.AgentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX && isInterruptRequest(content) {
			svc.persistSyntheticUserMessage(agentID, dbAgent.AgentProvider, "[Request interrupted by user]")
		}

		svc.handleControlRequestMessage(agentID, content)
		sendProtoResponse(sender, &leapmuxv1.SendAgentRawMessageResponse{})
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
		gitStatuses := make([]*leapmuxv1.AgentGitStatus, len(agents))
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
			// Preload cached models/option groups from DB for inactive agents
			// so AvailableModels/AvailableOptionGroups return persisted data.
			if !svc.Agents.HasAgent(agents[i].ID) {
				svc.Agents.PreloadCache(
					agents[i].ID,
					unmarshalAvailableModels(agents[i].AvailableModels),
					unmarshalAvailableOptionGroups(agents[i].AvailableOptionGroups),
				)
			}
			protoAgents = append(protoAgents, svc.agentToProto(&agents[i], agents[i].PermissionMode, svc.Agents.HasAgent(agents[i].ID), gitStatuses[i], svc.Agents.AvailableModels(agents[i].ID, agents[i].AgentProvider), svc.Agents.AvailableOptionGroups(agents[i].ID, agents[i].AgentProvider)))
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
		agentRow, ok := svc.requireAccessibleAgent(sender, agentID)
		if !ok {
			return
		}

		// Return empty for closed agents.
		if agentRow.ClosedAt.Valid {
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

		agentID := r.GetAgentId()
		if _, ok := svc.requireAccessibleAgent(sender, agentID); !ok {
			return
		}

		if _, err := svc.Queries.RenameAgent(bgCtx(), db.RenameAgentParams{
			Title: r.GetTitle(),
			ID:    agentID,
		}); err != nil {
			slog.Error("failed to rename agent", "agent_id", agentID, "error", err)
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

		agentID := r.GetAgentId()
		messageID := r.GetMessageId()
		if _, ok := svc.requireAccessibleAgent(sender, agentID); !ok {
			return
		}

		if err := svc.Queries.DeleteMessageByAgentAndID(bgCtx(), db.DeleteMessageByAgentAndIDParams{
			AgentID: agentID,
			ID:      messageID,
		}); err != nil {
			slog.Error("failed to delete message", "agent_id", agentID, "message_id", messageID, "error", err)
			sendInternalError(sender, "failed to delete message")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.DeleteAgentMessageResponse{})

		// Broadcast deletion to all watchers.
		svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event: &leapmuxv1.AgentEvent_MessageDeleted{
				MessageDeleted: &leapmuxv1.AgentMessageDeleted{
					AgentId:   agentID,
					MessageId: messageID,
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
		dbAgent, ok := svc.requireAccessibleAgent(sender, agentID)
		if !ok {
			return
		}

		s := r.GetSettings()
		newModel := s.GetModel()
		if newModel == "" {
			newModel = dbAgent.Model
		}
		newEffort := s.GetEffort()
		if newEffort == "" {
			newEffort = dbAgent.Effort
		}
		newPermissionMode := s.GetPermissionMode()
		if newPermissionMode == "" {
			newPermissionMode = dbAgent.PermissionMode
		}
		oldExtraSettings := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
		newExtraSettings := resolveCodexExtras(mergeExtraSettings(oldExtraSettings, s.GetExtraSettings()), dbAgent.AgentProvider)

		// Update the DB.
		if err := svc.Queries.UpdateAgentAllSettings(bgCtx(), db.UpdateAgentAllSettingsParams{
			Model:          newModel,
			Effort:         newEffort,
			PermissionMode: newPermissionMode,
			ExtraSettings:  marshalExtraSettings(newExtraSettings),
			ID:             agentID,
		}); err != nil {
			slog.Error("failed to update agent settings", "agent_id", agentID, "error", err)
			sendInternalError(sender, "failed to update agent settings")
			return
		}

		// If the agent is currently running, try a live update first.
		// Providers that support it (e.g. Codex) apply settings to the
		// next turn without a restart. Providers that don't (e.g. Claude
		// Code) return false and we fall back to stop+restart.
		if svc.Agents.HasAgent(agentID) {
			updated := svc.Agents.UpdateSettings(agentID, &leapmuxv1.AgentSettings{
				Model:          newModel,
				Effort:         newEffort,
				PermissionMode: newPermissionMode,
				ExtraSettings:  newExtraSettings,
			})

			if !updated {
				resumeSessionID := svc.resolveResumeSessionID(agentID, dbAgent.AgentSessionID, dbAgent.Resumed)

				agentOpts := agent.Options{
					AgentID:         agentID,
					Model:           newModel,
					Effort:          newEffort,
					WorkingDir:      dbAgent.WorkingDir,
					ResumeSessionID: resumeSessionID,
					PermissionMode:  newPermissionMode,
					ExtraSettings:   newExtraSettings,
					StartupTimeout:  svc.agentStartupTimeout(),
					APITimeout:      svc.agentAPITimeout(),
					Shell:           svc.agentShell(),
					LoginShell:      svc.agentLoginShell(),
					HomeDir:         svc.HomeDir,
					AgentProvider:   dbAgent.AgentProvider,
				}

				sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)

				confirmedSettings, err := svc.Agents.RestartAgent(bgCtx(), agentOpts, sink)
				if err != nil {
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
					if err := svc.persistConfirmedAgentSettings(agentID, dbAgent.AgentProvider, newModel, newEffort, newPermissionMode, newExtraSettings, confirmedSettings); err != nil {
						slog.Warn("failed to persist confirmed settings after restart", "agent_id", agentID, "error", err)
					}
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
			oldID := effortOrDefault(dbAgent.Effort, dbAgent.Model, dbAgent.AgentProvider)
			changes["effort"] = map[string]string{
				"old": oldID, "new": newEffort,
				"oldLabel": effortLabel(oldID), "newLabel": effortLabel(newEffort),
			}
		}
		if dbAgent.PermissionMode != newPermissionMode {
			changes[agent.OptionGroupKeyPermissionMode] = map[string]string{
				"old": dbAgent.PermissionMode, "new": newPermissionMode,
				"oldLabel": svc.permissionModeLabel(agentID, dbAgent.PermissionMode, dbAgent.AgentProvider), "newLabel": svc.permissionModeLabel(agentID, newPermissionMode, dbAgent.AgentProvider),
			}
		}
		for _, key := range sortedExtraSettingKeys(oldExtraSettings, newExtraSettings) {
			oldVal, newVal := oldExtraSettings[key], newExtraSettings[key]
			if oldVal != newVal {
				changes[key] = map[string]string{
					"old": oldVal, "new": newVal,
					"label":    svc.optionGroupLabel(agentID, key, dbAgent.AgentProvider),
					"oldLabel": svc.optionLabel(agentID, key, oldVal, dbAgent.AgentProvider),
					"newLabel": svc.optionLabel(agentID, key, newVal, dbAgent.AgentProvider),
				}
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
		dbAgent, ok := svc.requireAccessibleAgent(sender, agentID)
		if !ok {
			return
		}
		content := r.GetContent()

		if svc.handleCodexPlanModePromptResponse(agentID, content) {
			sendProtoResponse(sender, &leapmuxv1.SendControlResponseResponse{})
			return
		}

		var displayText string
		content, displayText = svc.normalizeProviderControlResponse(agentID, dbAgent.AgentProvider, content)
		if displayText == "" {
			displayText = svc.controlResponseDisplayText(agentID, dbAgent.AgentProvider, content)
		}

		// Detect plan mode changes from the control response before
		// forwarding to the agent. This mirrors the main-branch Hub logic
		// that updated permission mode based on EnterPlanMode/ExitPlanMode
		// approval or rejection.
		skipSend := svc.handleControlResponsePlanMode(agentID, content)

		svc.persistSyntheticUserMessage(agentID, dbAgent.AgentProvider, displayText)

		if !skipSend {
			if err := svc.Agents.SendRawInput(agentID, content); err != nil {
				slog.Error("failed to send control response to agent",
					"agent_id", agentID, "error", err)
				sendNotFoundError(sender, "agent not found or not running")
				return
			}
		}

		// Delete the resolved control request from the DB so it is not
		// replayed on reconnect.  Extract the request ID from the response
		// content — Claude Code uses response.request_id, OpenCode/ACP uses
		// the JSON-RPC id field.
		if reqID := extractControlResponseRequestID(content); reqID != "" {
			sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
			sink.DeleteControlRequest(reqID)
			sink.BroadcastControlCancel(reqID)
		}

		sendProtoResponse(sender, &leapmuxv1.SendControlResponseResponse{})
	})

	d.Register("ListAvailableProviders", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListAvailableProvidersRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), svc.agentStartupTimeout())
		defer cancel()
		providers := agent.ListAvailableProviders(ctx, svc.agentShell(), svc.agentLoginShell())
		sendProtoResponse(sender, &leapmuxv1.ListAvailableProvidersResponse{
			Providers: providers,
		})
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
				// Preload cached models/option groups from DB for inactive agents.
				if !svc.Agents.HasAgent(agentID) {
					svc.Agents.PreloadCache(
						agentID,
						unmarshalAvailableModels(dbAgent.AvailableModels),
						unmarshalAvailableOptionGroups(dbAgent.AvailableOptionGroups),
					)
				}
				status, startupError := svc.deriveAgentStatus(&dbAgent, svc.Agents.HasAgent(agentID))
				broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: &leapmuxv1.AgentEvent{
							AgentId: agentID,
							Event: &leapmuxv1.AgentEvent_StatusChange{
								StatusChange: svc.buildAgentStatusChange(&dbAgent, status, startupError),
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

			// Replay current startup status so a subscriber that joins
			// after READY / STARTUP_FAILED was broadcast still converges
			// (the prior pure-broadcast design lost events for any
			// watcher that attached after the one-shot fire). Fetch the
			// DB row so a failure that predates a worker restart still
			// surfaces via the persisted startup_error column.
			status := leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
			var startupError string
			if termRow, err := svc.Queries.GetTerminal(bgCtx(), termID); err == nil {
				status, startupError = svc.deriveTerminalStatus(&termRow)
			} else if sup, errStr, ok := svc.TerminalStartup.status(termID); ok {
				status = sup
				startupError = errStr
			}
			broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
				Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
					TerminalEvent: &leapmuxv1.TerminalEvent{
						TerminalId: termID,
						Event: &leapmuxv1.TerminalEvent_StatusChange{
							StatusChange: buildTerminalStatusChange(termID, status, startupError),
						},
					},
				},
			})
		}

		// Stream stays open — events will be pushed via watcher.Sender.SendStream().
		// The handler returns immediately; cleanup happens when the channel closes.
	})
}

// deriveAgentStatus computes (status, startupError) for an agent, in
// priority order:
//  1. runtime Manager — if the agent is currently running, ACTIVE wins.
//  2. in-memory startup registry — STARTING / STARTUP_FAILED while a
//     startup is in flight or has just failed.
//  3. persisted startup_error column — surfaces a prior failure across
//     worker restarts (the in-memory registry is wiped on restart).
//  4. INACTIVE otherwise.
func (svc *Context) deriveAgentStatus(a *db.Agent, isRunning bool) (leapmuxv1.AgentStatus, string) {
	if isRunning {
		return leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, ""
	}
	if sup, errStr, ok := svc.AgentStartup.status(a.ID); ok {
		return sup, errStr
	}
	if a.StartupError != "" {
		return leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, a.StartupError
	}
	return leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, ""
}

// agentToProto converts a DB Agent to a proto AgentInfo. Status and
// startup_error are derived via deriveAgentStatus.
func (svc *Context) agentToProto(a *db.Agent, permissionMode string, isRunning bool, gs *leapmuxv1.AgentGitStatus, availableModels []*leapmuxv1.AvailableModel, availableOptionGroups []*leapmuxv1.AvailableOptionGroup) *leapmuxv1.AgentInfo {
	status, startupError := svc.deriveAgentStatus(a, isRunning)
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
		WorkerId:              svc.WorkerID,
		CreatedAt:             timefmt.Format(a.CreatedAt),
		GitStatus:             gs,
		AgentProvider:         a.AgentProvider,
		AvailableModels:       availableModels,
		AvailableOptionGroups: availableOptionGroups,
		ExtraSettings:         loadExtraSettings(a.ExtraSettings, a.AgentProvider),
		StartupError:          startupError,
	}

	if a.ClosedAt.Valid {
		info.ClosedAt = timefmt.Format(a.ClosedAt.Time)
	}

	return info
}

// runAgentStartup is the async body of OpenAgent: it spawns the subprocess,
// runs the initialize handshake, then reports success/failure via
// broadcastAgentStartupStatus and cleans up on failure.
func (svc *Context) runAgentStartup(ctx context.Context, agentID string, gm gitModeResult, agentOpts agent.Options, model, effort string, extraSettings map[string]string) {
	sink := svc.Output.NewSink(agentID, agentOpts.AgentProvider)

	agent.TraceStartupPhase(agentID, "before_start_agent")
	confirmedSettings, startErr := svc.startAgent(ctx, agentOpts, sink)
	agent.TraceStartupPhase(agentID, "after_start_agent")

	// After startAgent returns, re-read the DB row to see whether the tab
	// was closed during startup (CloseAgent sets closed_at). If so, stop
	// the just-started process and skip the success broadcast.
	dbAgent, fetchErr := svc.getAgentByID(bgCtx(), agentID)
	if fetchErr == nil && dbAgent.ClosedAt.Valid {
		if startErr == nil {
			svc.Agents.StopAgent(agentID)
		}
		svc.AgentStartup.succeed(agentID)
		svc.rollbackGitMode(gm)
		return
	}

	if startErr != nil {
		errMsg := startErr.Error()
		slog.Error("failed to start agent", "agent_id", agentID, "error", errMsg)
		// Keep the agent row open so the in-tab error UI is reachable
		// across page refreshes. The user dismisses the dead tab via
		// the tab bar's close action, which calls CloseAgent and
		// cleans up the worktree + in-flight startup.
		svc.rollbackGitMode(gm)
		// Persist the error on the agent row so the startup panel
		// survives a worker restart (the in-memory registry is wiped
		// on restart; the DB column is the source of truth).
		if err := svc.Queries.SetAgentStartupError(bgCtx(), db.SetAgentStartupErrorParams{
			StartupError: errMsg,
			ID:           agentID,
		}); err != nil {
			slog.Warn("failed to persist agent startup error", "agent_id", agentID, "error", err)
		}
		if fetchErr == nil {
			svc.broadcastAgentStartupStatus(&dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, errMsg)
		} else {
			// DB row unreadable — emit a minimal status change anyway so
			// the frontend can transition out of STARTING.
			svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_StatusChange{
					StatusChange: &leapmuxv1.AgentStatusChange{
						AgentId:      agentID,
						Status:       leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED,
						WorkerOnline: true,
						StartupError: errMsg,
					},
				},
			})
		}
		// Mark the registry last so waitForStartupFailure in tests is a
		// reliable "failure-handling goroutine finished" signal — all
		// rollback, DB, and broadcast work completes before this point.
		svc.AgentStartup.fail(agentID, errMsg)
		return
	}

	// Clear the startup registry entry *before* persistConfirmedAgentSettings
	// so that any SendAgentMessage racing against the early ACTIVE
	// broadcast (emitted from the output sink when the first init message
	// arrives inside startAgent) is not rejected by the SendAgentMessage
	// startup-gate. The subprocess is up and ready for input at this
	// point; settings persistence is a best-effort DB write.
	svc.AgentStartup.succeed(agentID)
	// Clear any persisted startup_error from a prior failed attempt so
	// the startup panel doesn't resurrect after a worker restart.
	if dbAgent.StartupError != "" {
		if err := svc.Queries.SetAgentStartupError(bgCtx(), db.SetAgentStartupErrorParams{
			StartupError: "",
			ID:           agentID,
		}); err != nil {
			slog.Warn("failed to clear agent startup error", "agent_id", agentID, "error", err)
		}
	}

	if err := svc.persistConfirmedAgentSettings(agentID, agentOpts.AgentProvider, model, agentOpts.Effort, agentOpts.PermissionMode, extraSettings, confirmedSettings); err != nil {
		slog.Warn("failed to persist confirmed agent settings", "agent_id", agentID, "error", err)
	}

	slog.Info("agent started", "agent_id", agentID, "model", model, "permission_mode", confirmedSettings.GetPermissionMode())

	// Re-fetch dbAgent so the broadcast carries the persisted settings.
	activeDbAgent, err := svc.getAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to re-fetch agent for active broadcast", "agent_id", agentID, "error", err)
		activeDbAgent = dbAgent
	}

	svc.broadcastAgentStartupStatus(&activeDbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, "")
}

// buildAgentStatusChange constructs an AgentStatusChange proto for the
// given agent/status pair. Shared by the WatchEvents catch-up replay and
// the broadcast path so both surface the same fields (the
// subprocess-dependent catalogs are only attached when status=ACTIVE).
func (svc *Context) buildAgentStatusChange(dbAgent *db.Agent, status leapmuxv1.AgentStatus, startupError string) *leapmuxv1.AgentStatusChange {
	sc := &leapmuxv1.AgentStatusChange{
		AgentId:        dbAgent.ID,
		Status:         status,
		AgentSessionId: dbAgent.AgentSessionID,
		WorkerOnline:   true,
		PermissionMode: dbAgent.PermissionMode,
		Model:          modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:         dbAgent.Effort,
		GitStatus:      gitutil.GetGitStatus(dbAgent.WorkingDir),
		AgentProvider:  dbAgent.AgentProvider,
		ExtraSettings:  loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider),
		StartupError:   startupError,
	}
	if status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		sc.AvailableModels = svc.Agents.AvailableModels(dbAgent.ID, dbAgent.AgentProvider)
		sc.AvailableOptionGroups = svc.Agents.AvailableOptionGroups(dbAgent.ID, dbAgent.AgentProvider)
	}
	return sc
}

// broadcastAgentStartupStatus fans out an AgentStatusChange event to all
// subscribers. Used by the OpenAgent startup goroutine.
func (svc *Context) broadcastAgentStartupStatus(dbAgent *db.Agent, status leapmuxv1.AgentStatus, startupError string) {
	svc.Watchers.BroadcastAgentEvent(dbAgent.ID, &leapmuxv1.AgentEvent{
		AgentId: dbAgent.ID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: svc.buildAgentStatusChange(dbAgent, status, startupError)},
	})
}

func (svc *Context) persistConfirmedAgentSettings(agentID string, provider leapmuxv1.AgentProvider, model, effort, permissionMode string, extraSettings map[string]string, confirmed *leapmuxv1.AgentSettings) error {
	confirmedModel := model
	if confirmed != nil && confirmed.GetModel() != "" {
		confirmedModel = confirmed.GetModel()
	}
	confirmedEffort := effort
	if confirmed != nil && confirmed.GetEffort() != "" {
		confirmedEffort = confirmed.GetEffort()
	}
	confirmedPermissionMode := permissionMode
	if confirmed != nil && confirmed.GetPermissionMode() != "" {
		confirmedPermissionMode = confirmed.GetPermissionMode()
	}
	confirmedExtraSettings := mergeExtraSettings(extraSettings, nil)
	if confirmed != nil {
		confirmedExtraSettings = mergeExtraSettings(confirmedExtraSettings, confirmed.GetExtraSettings())
	}
	confirmedExtraSettings = resolveCodexExtras(confirmedExtraSettings, provider)

	if err := svc.Queries.UpdateAgentAllSettings(bgCtx(), db.UpdateAgentAllSettingsParams{
		Model:          confirmedModel,
		Effort:         confirmedEffort,
		PermissionMode: confirmedPermissionMode,
		ExtraSettings:  marshalExtraSettings(confirmedExtraSettings),
		ID:             agentID,
	}); err != nil {
		return err
	}

	// Persist available models and option groups so they survive backend restarts.
	models := svc.Agents.AvailableModels(agentID, provider)
	groups := svc.Agents.AvailableOptionGroups(agentID, provider)
	return svc.Queries.UpdateAgentAvailableSettings(bgCtx(), db.UpdateAgentAvailableSettingsParams{
		AvailableModels:       marshalAvailableModels(models),
		AvailableOptionGroups: marshalAvailableOptionGroups(groups),
		ID:                    agentID,
	})
}

// handleClearContext implements the /clear command by restarting the agent
// without resuming the previous session, giving it a fresh context window.
func (svc *Context) handleClearContext(agentID string) {
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()

	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("clear context: failed to fetch agent", "agent_id", agentID, "error", err)
		return
	}

	// Stop the running agent and wait for it to fully exit so that
	// StartAgent below doesn't fail with "agent already running".
	svc.Agents.StopAndWaitAgent(agentID)

	// Clear span tracking state from the previous session.
	svc.Output.ResetSpanTracker(agentID)

	// Restart the agent with a fresh context.
	// Don't clear agentSessionId before starting — the frontend uses it for
	// isWatchable. On success, handleSystemInit will overwrite it with the
	// new session ID. On failure, clear it so ensureAgentRunning won't try
	// to resume a stale session.
	model := modelOrDefault(dbAgent.Model, dbAgent.AgentProvider)
	extraSettings := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	confirmedSettings, err := svc.startAgent(bgCtx(), agent.Options{
		AgentID:        agentID,
		Model:          model,
		Effort:         dbAgent.Effort,
		WorkingDir:     dbAgent.WorkingDir,
		PermissionMode: dbAgent.PermissionMode,
		ExtraSettings:  extraSettings,
		StartupTimeout: svc.agentStartupTimeout(),
		APITimeout:     svc.agentAPITimeout(),
		Shell:          svc.agentShell(),
		LoginShell:     svc.agentLoginShell(),
		HomeDir:        svc.HomeDir,
		AgentProvider:  dbAgent.AgentProvider,
	}, sink)
	if err != nil {
		slog.Error("clear context: failed to restart agent", "agent_id", agentID, "error", err)
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: "",
			ID:             agentID,
		})
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":  "agent_error",
			"error": "Failed to restart agent after clearing context: " + err.Error(),
		})
		return
	}
	if err := svc.persistConfirmedAgentSettings(agentID, dbAgent.AgentProvider, model, dbAgent.Effort, dbAgent.PermissionMode, extraSettings, confirmedSettings); err != nil {
		slog.Warn("clear context: failed to persist confirmed settings", "agent_id", agentID, "error", err)
	}
	slog.Info("clear context: agent restarted successfully", "agent_id", agentID)

	// Only broadcast context_cleared once the new agent is up; on failure
	// the agent_error notification above stands on its own so clients do
	// not see a "cleared" UI state for an agent that is actually down.
	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": "context_cleared",
	})
}

// resolveResumeSessionID returns the session ID to resume if the agent was
// originally resumed or user messages have been exchanged, or empty string
// otherwise. The agent assigns a session ID during startup, but no conversation
// exists until the user actually sends a message — resuming without messages
// causes errors. When the agent was created via resume (resumed != 0), the
// conversation lives in Claude Code's session storage so the HasUserMessages
// check is skipped.
func (svc *Context) resolveResumeSessionID(agentID, currentSessionID string, resumed int64) string {
	if currentSessionID == "" {
		return ""
	}
	if resumed != 0 {
		return currentSessionID
	}
	hasMessages, err := svc.Queries.HasUserMessages(bgCtx(), agentID)
	if err == nil && hasMessages != 0 {
		return currentSessionID
	}
	return ""
}

// ensureAgentRunning starts the agent process if it is not already running.
// It fetches the agent configuration from the DB and resumes the session
// if a session ID is stored (e.g. after worker restart).
//
// When the caller has already resolved the resume session ID (e.g. before
// persisting a user message that would skew the HasUserMessages check),
// pass it via preResolvedResumeSessionID. Pass nil to let this function
// resolve it from the DB.
func (svc *Context) ensureAgentRunning(agentID string, preResolvedResumeSessionID *string) error {
	if svc.Agents.HasAgent(agentID) {
		return nil
	}

	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("ensureAgentRunning: failed to fetch agent", "agent_id", agentID, "error", err)
		return fmt.Errorf("agent not found: %w", err)
	}

	var resumeSessionID string
	if preResolvedResumeSessionID != nil {
		resumeSessionID = *preResolvedResumeSessionID
	} else {
		resumeSessionID = svc.resolveResumeSessionID(agentID, dbAgent.AgentSessionID, dbAgent.Resumed)
	}
	model := modelOrDefault(dbAgent.Model, dbAgent.AgentProvider)
	extraSettings := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)

	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	confirmedSettings, err := svc.Agents.StartAgent(bgCtx(), agent.Options{
		AgentID:         agentID,
		Model:           model,
		Effort:          dbAgent.Effort,
		WorkingDir:      dbAgent.WorkingDir,
		ResumeSessionID: resumeSessionID,
		PermissionMode:  dbAgent.PermissionMode,
		ExtraSettings:   extraSettings,
		StartupTimeout:  svc.agentStartupTimeout(),
		APITimeout:      svc.agentAPITimeout(),
		Shell:           svc.agentShell(),
		LoginShell:      svc.agentLoginShell(),
		HomeDir:         svc.HomeDir,
		AgentProvider:   dbAgent.AgentProvider,
	}, sink)
	if err != nil {
		slog.Error("ensureAgentRunning: failed to start agent", "agent_id", agentID, "error", err)
		return err
	}
	if err := svc.persistConfirmedAgentSettings(agentID, dbAgent.AgentProvider, model, dbAgent.Effort, dbAgent.PermissionMode, extraSettings, confirmedSettings); err != nil {
		slog.Warn("ensureAgentRunning: failed to persist confirmed settings", "agent_id", agentID, "error", err)
	}

	slog.Info("ensureAgentRunning: agent started", "agent_id", agentID)
	return nil
}

// handleControlRequestMessage handles raw provider control input
// (e.g. Claude control_request JSON or Codex JSON-RPC interrupt).
// These payloads are forwarded directly to the agent's stdin and are not
// wrapped in a user message envelope or persisted as chat messages.
func (svc *Context) handleControlRequestMessage(agentID, content string) {
	// Persist set_permission_mode to the DB eagerly so that /clear
	// (which reads the DB) always sees the latest mode. Some providers
	// (e.g. Claude Code) don't echo the mode back in their
	// control_response, so relying on the output handler alone would
	// leave the DB stale.
	mode, isSetMode := parseSetPermissionMode(content)
	if isSetMode {
		svc.setAgentPermissionMode(agentID, mode)
	}

	// If agent is not running, handle special cases locally.
	if !svc.Agents.HasAgent(agentID) {
		if isSetMode {
			return
		}
		if isInterruptRequest(content) {
			// Agent is already gone — nothing to interrupt.
			return
		}
		// Other control requests need the agent running.
		if err := svc.ensureAgentRunning(agentID, nil); err != nil {
			slog.Error("failed to start agent for control request", "agent_id", agentID, "error", err)
			return
		}
	}

	// Send as raw input to the agent's stdin.
	if err := svc.Agents.SendRawInput(agentID, []byte(content)); err != nil {
		slog.Error("failed to send control request to agent", "agent_id", agentID, "error", err)
	}
}

func (svc *Context) persistSyntheticUserMessage(agentID string, provider leapmuxv1.AgentProvider, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}

	innerJSON, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		slog.Warn("synthetic user message: marshal failed", "agent_id", agentID, "error", err)
		return
	}
	if err := svc.Output.persistAndBroadcast(agentID, provider, leapmuxv1.MessageRole_MESSAGE_ROLE_USER, innerJSON, agent.SpanInfo{}, nil); err != nil {
		slog.Error("synthetic user message: failed to persist message", "agent_id", agentID, "error", err)
	}
}

// broadcastSettingsStatusChange broadcasts an AgentStatusChange event
// so frontends update their settings display. If extras is non-nil, the
// ExtraSettings field is included in the broadcast.
func (svc *Context) broadcastSettingsStatusChange(dbAgent db.Agent, extras map[string]string) {
	sc := &leapmuxv1.AgentStatusChange{
		AgentId:        dbAgent.ID,
		Status:         leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED,
		AgentSessionId: dbAgent.AgentSessionID,
		WorkerOnline:   true,
		PermissionMode: dbAgent.PermissionMode,
		Model:          modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:         dbAgent.Effort,
		GitStatus:      gitutil.GetGitStatus(dbAgent.WorkingDir),
		AgentProvider:  dbAgent.AgentProvider,
		ExtraSettings:  extras,
	}
	svc.Watchers.BroadcastAgentEvent(dbAgent.ID, &leapmuxv1.AgentEvent{
		AgentId: dbAgent.ID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// setAgentPermissionMode updates the agent's permission mode in the DB
// and broadcasts a statusChange + settings_changed notification.
func (svc *Context) setAgentPermissionMode(agentID, mode string) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("set permission mode: agent not found", "agent_id", agentID, "error", err)
		return
	}
	svc.setAgentPermissionModeWithAgent(dbAgent, mode)
}

func (svc *Context) setAgentPermissionModeWithAgent(dbAgent db.Agent, mode string) db.Agent {
	agentID := dbAgent.ID
	oldMode := dbAgent.PermissionMode
	if oldMode == mode {
		return dbAgent
	}
	if err := svc.Queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
		PermissionMode: mode,
		ID:             agentID,
	}); err != nil {
		slog.Error("set permission mode: DB update failed", "agent_id", agentID, "error", err)
		return dbAgent
	}

	dbAgent.PermissionMode = mode

	svc.broadcastSettingsStatusChange(dbAgent, nil)

	if oldMode != "" {
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type": "settings_changed",
			"changes": map[string]interface{}{
				agent.OptionGroupKeyPermissionMode: map[string]string{
					"old": oldMode, "new": mode,
					"oldLabel": svc.permissionModeLabel(agentID, oldMode, dbAgent.AgentProvider), "newLabel": svc.permissionModeLabel(agentID, mode, dbAgent.AgentProvider),
				},
			},
		})
	}

	return dbAgent
}

// setAgentCollaborationMode updates the agent's collaboration mode
// in the DB and broadcasts a statusChange + settings_changed notification.
func (svc *Context) setAgentCollaborationMode(agentID, mode string) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("set Codex collaboration mode: agent not found", "agent_id", agentID, "error", err)
		return
	}
	svc.setAgentCollaborationModeWithAgent(dbAgent, mode)
}

func (svc *Context) setAgentCollaborationModeWithAgent(dbAgent db.Agent, mode string) db.Agent {
	agentID := dbAgent.ID
	extras := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
	oldMode := extras[agent.CodexExtraCollaborationMode]
	if oldMode == mode {
		return dbAgent
	}
	extras[agent.CodexExtraCollaborationMode] = mode
	newExtraSettings := marshalExtraSettings(extras)
	if err := svc.Queries.SetAgentExtraSettings(bgCtx(), db.SetAgentExtraSettingsParams{
		ExtraSettings: newExtraSettings,
		ID:            agentID,
	}); err != nil {
		slog.Error("set Codex collaboration mode: DB update failed", "agent_id", agentID, "error", err)
		return dbAgent
	}

	dbAgent.ExtraSettings = newExtraSettings

	if svc.Agents.HasAgent(agentID) {
		svc.Agents.UpdateSettings(agentID, &leapmuxv1.AgentSettings{ExtraSettings: map[string]string{
			agent.CodexExtraCollaborationMode: mode,
		}})
	}

	svc.broadcastSettingsStatusChange(dbAgent, extras)

	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			agent.CodexExtraCollaborationMode: map[string]string{
				"old": oldMode, "new": mode,
				"label":    svc.optionGroupLabel(agentID, agent.CodexExtraCollaborationMode, dbAgent.AgentProvider),
				"oldLabel": svc.optionLabel(agentID, agent.CodexExtraCollaborationMode, oldMode, dbAgent.AgentProvider), "newLabel": svc.optionLabel(agentID, agent.CodexExtraCollaborationMode, mode, dbAgent.AgentProvider),
			},
		},
	})

	return dbAgent
}

// setCodexBypassExtrasWithAgent sets the Codex-specific extra settings for
// bypass mode (full network access and no sandbox restrictions).
func (svc *Context) setCodexBypassExtrasWithAgent(dbAgent db.Agent) db.Agent {
	agentID := dbAgent.ID
	extras := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
	changes := map[string]interface{}{}

	setExtra := func(key, newVal string) {
		oldVal := extras[key]
		if oldVal == newVal {
			return
		}
		extras[key] = newVal
		changes[key] = map[string]string{
			"old": oldVal, "new": newVal,
			"label":    svc.optionGroupLabel(agentID, key, dbAgent.AgentProvider),
			"oldLabel": svc.optionLabel(agentID, key, oldVal, dbAgent.AgentProvider),
			"newLabel": svc.optionLabel(agentID, key, newVal, dbAgent.AgentProvider),
		}
	}
	setExtra(agent.CodexExtraNetworkAccess, agent.CodexNetworkEnabled)
	setExtra(agent.CodexExtraSandboxPolicy, agent.CodexSandboxDangerFullAccess)

	if len(changes) == 0 {
		return dbAgent
	}

	newExtraSettings := marshalExtraSettings(extras)
	if err := svc.Queries.SetAgentExtraSettings(bgCtx(), db.SetAgentExtraSettingsParams{
		ExtraSettings: newExtraSettings,
		ID:            agentID,
	}); err != nil {
		slog.Error("set Codex bypass extras: DB update failed", "agent_id", agentID, "error", err)
		return dbAgent
	}

	dbAgent.ExtraSettings = newExtraSettings

	if svc.Agents.HasAgent(agentID) {
		svc.Agents.UpdateSettings(agentID, &leapmuxv1.AgentSettings{ExtraSettings: map[string]string{
			agent.CodexExtraNetworkAccess: extras[agent.CodexExtraNetworkAccess],
			agent.CodexExtraSandboxPolicy: extras[agent.CodexExtraSandboxPolicy],
		}})
	}

	svc.broadcastSettingsStatusChange(dbAgent, extras)

	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type":    "settings_changed",
		"changes": changes,
	})

	return dbAgent
}

// sendSyntheticUserMessage persists and broadcasts a user message, then sends
// it to the agent process if possible. This is used for local plan-mode flows
// that originate from a UI prompt rather than a frontend SendAgentMessage RPC.
func (svc *Context) sendSyntheticUserMessage(agentID, content string) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("synthetic user message: agent not found", "agent_id", agentID, "error", err)
		return
	}

	// Pre-resolve the resume session ID before persisting (same reason
	// as in SendAgentMessage — see comment there).
	resumeSessionID := svc.resolveResumeSessionID(agentID, dbAgent.AgentSessionID, dbAgent.Resumed)

	messageID := id.Generate()
	now := time.Now().UTC()
	innerJSON, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		slog.Warn("synthetic user message: marshal failed", "agent_id", agentID, "error", err)
		return
	}
	compressed, compressionType := msgcodec.Compress(innerJSON)

	seq, err := svc.Queries.CreateMessage(bgCtx(), db.CreateMessageParams{
		ID:                 messageID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Depth:              0,
		SpanID:             "",
		ParentSpanID:       "",
		SpanLines:          "[]",
		SpanColor:          0,
		AgentProvider:      dbAgent.AgentProvider,
		CreatedAt:          now,
	})
	if err != nil {
		slog.Error("synthetic user message: failed to persist message", "agent_id", agentID, "error", err)
		return
	}

	deliveryError := ""
	if !svc.Agents.HasAgent(agentID) {
		if startErr := svc.ensureAgentRunning(agentID, &resumeSessionID); startErr != nil {
			deliveryError = "agent is not running"
		} else if sendErr := svc.Agents.SendInput(agentID, content, nil); sendErr != nil {
			slog.Error("synthetic user message: failed to send after auto-start", "agent_id", agentID, "error", sendErr)
			deliveryError = sendErr.Error()
		}
	} else if sendErr := svc.Agents.SendInput(agentID, content, nil); sendErr != nil {
		slog.Error("synthetic user message: failed to send input", "agent_id", agentID, "error", sendErr)
		deliveryError = sendErr.Error()
	}
	if deliveryError != "" {
		_ = svc.Queries.SetMessageDeliveryError(bgCtx(), db.SetMessageDeliveryErrorParams{
			DeliveryError: deliveryError,
			ID:            messageID,
			AgentID:       agentID,
		})
	}

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
}

func (svc *Context) handleCodexPlanModePromptResponse(agentID string, content []byte) bool {
	var crPayload struct {
		PermissionMode string `json:"permissionMode"`
		ClearContext   bool   `json:"clearContext"`
		Response       struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &crPayload); err != nil {
		return false
	}

	reqID := crPayload.Response.RequestID
	if reqID == "" {
		return false
	}

	cr, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	if err != nil {
		return false
	}

	var crBody struct {
		Request struct {
			ToolName string `json:"tool_name"`
		} `json:"request"`
	}
	if err := json.Unmarshal(cr.Payload, &crBody); err != nil {
		slog.Warn("codex plan mode prompt unmarshal failed", "agent_id", agentID, "error", err)
		return false
	}
	if crBody.Request.ToolName != agent.ToolNameCodexPlanModePrompt {
		return false
	}

	_ = svc.Queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_ControlCancel{
			ControlCancel: &leapmuxv1.AgentControlCancelRequest{
				AgentId:   agentID,
				RequestId: reqID,
			},
		},
	})

	switch crPayload.Response.Response.Behavior {
	case agent.ControlBehaviorAllow:
		dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
		if err != nil {
			slog.Error("codex plan mode prompt: agent not found", "agent_id", agentID, "error", err)
			return false
		}

		dbAgent = svc.setAgentCollaborationModeWithAgent(dbAgent, agent.CodexCollaborationDefault)

		if crPayload.PermissionMode != "" {
			dbAgent = svc.setAgentPermissionModeWithAgent(dbAgent, crPayload.PermissionMode)
			svc.setCodexBypassExtrasWithAgent(dbAgent)
		}

		if crPayload.ClearContext {
			targetMode := crPayload.PermissionMode
			if targetMode == "" {
				targetMode = agent.PermissionModeDefault
			}
			go svc.initiatePlanExecution(agentID, targetMode)
		} else {
			svc.sendSyntheticUserMessage(agentID, "Implement the plan.")
		}
	case agent.ControlBehaviorDeny:
		if msg := strings.TrimSpace(crPayload.Response.Response.Message); msg != "" && msg != "Rejected by user." {
			svc.sendSyntheticUserMessage(agentID, msg)
		}
	}

	return true
}

// normalizeProviderControlResponse transforms provider-specific control
// responses into the wire format expected by the agent process.  It returns
// the (possibly transformed) content and, when the transform already computed
// the display text, a non-empty displayText so the caller can skip a second
// DB lookup in controlResponseDisplayText.
func (svc *Context) normalizeProviderControlResponse(agentID string, provider leapmuxv1.AgentProvider, content []byte) (normalized []byte, displayText string) {
	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:
		if transformed, text, ok := svc.transformCursorControlResponse(agentID, content); ok {
			return transformed, text
		}
	}
	return content, ""
}

func (svc *Context) transformCursorControlResponse(agentID string, content []byte) ([]byte, string, bool) {
	var crPayload struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &crPayload); err != nil {
		return nil, "", false
	}

	reqID := strings.TrimSpace(crPayload.Response.RequestID)
	if reqID == "" {
		return nil, "", false
	}

	cr, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	if err != nil {
		return nil, "", false
	}

	var req struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(cr.Payload, &req); err != nil {
		slog.Warn("cursor control response unmarshal method failed", "agent_id", agentID, "error", err)
		return nil, "", false
	}
	if req.Method != agent.CursorMethodCreatePlan {
		return nil, "", false
	}

	idRaw, _, ok := agent.ExtractJSONRPCID(cr.Payload)
	if !ok {
		return nil, "", false
	}

	outcomeBody := map[string]interface{}{
		"outcome": "accepted",
	}
	if crPayload.Response.Response.Behavior == agent.ControlBehaviorDeny {
		outcomeBody["outcome"] = "rejected"
		if reason := strings.TrimSpace(crPayload.Response.Response.Message); reason != "" && reason != "Rejected by user." {
			outcomeBody["reason"] = reason
		}
	}

	encoded, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idRaw),
		"result":  map[string]interface{}{"outcome": outcomeBody},
	})
	if err != nil {
		return nil, "", false
	}
	return encoded, cursorCreatePlanResponseDisplayText(encoded), true
}

// extractControlResponseRequestID extracts the control request ID from a
// control response's raw JSON content.  It supports both Claude Code format
// (response.request_id) and OpenCode/ACP JSON-RPC format (top-level id).
func extractControlResponseRequestID(content []byte) string {
	var parsed struct {
		// Claude Code format
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
		// OpenCode / ACP JSON-RPC format (id can be number or string)
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(content, &parsed); err != nil {
		slog.Warn("extract control response request ID unmarshal failed", "error", err)
		return ""
	}
	if parsed.Response.RequestID != "" {
		return parsed.Response.RequestID
	}
	// Try JSON-RPC id: strip quotes for string, use raw for number.
	if len(parsed.ID) > 0 && string(parsed.ID) != "null" {
		var s string
		if json.Unmarshal(parsed.ID, &s) == nil {
			return s
		}
		return strings.TrimSpace(string(parsed.ID))
	}
	return ""
}

// handleControlResponsePlanMode detects plan mode changes from control
// responses. When the frontend approves/rejects an EnterPlanMode or
// ExitPlanMode control request, this updates the permission mode and
// initiates plan execution as needed. Returns true when the caller
// should skip sending the response to the agent (clearContext path).
func (svc *Context) handleControlResponsePlanMode(agentID string, content []byte) bool {
	var crPayload struct {
		PermissionMode string `json:"permissionMode"`
		ClearContext   bool   `json:"clearContext"`
		Response       struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &crPayload); err != nil {
		return false
	}

	reqID := crPayload.Response.RequestID
	if reqID == "" {
		return false
	}

	// Look up the original control request to get the tool_name.
	cr, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	if err != nil {
		return false
	}

	var crBody struct {
		Request struct {
			ToolName  string `json:"tool_name"`
			ToolUseID string `json:"tool_use_id"`
		} `json:"request"`
	}
	if err := json.Unmarshal(cr.Payload, &crBody); err != nil {
		slog.Warn("codex cancel request unmarshal failed", "agent_id", agentID, "error", err)
		return false
	}
	toolName := crBody.Request.ToolName
	toolUseID := crBody.Request.ToolUseID

	// Look up the agent's provider for message persistence.
	dbAgent, dbErr := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if dbErr != nil {
		return false
	}

	// Persist a display message for the control response.
	// Skip for AskUserQuestion — the tool_result already shows the user's answers.
	// Skip for ExitPlanMode — the tool_result already shows approval/feedback.
	if toolName != agent.ToolNameAskUserQuestion && toolName != agent.ToolNameExitPlanMode {
		action := "approved"
		if crPayload.Response.Response.Behavior == agent.ControlBehaviorDeny {
			action = "rejected"
		}
		displayContent := map[string]interface{}{
			"isSynthetic": true,
			"controlResponse": map[string]string{
				"action":  action,
				"comment": crPayload.Response.Response.Message,
			},
		}
		displayJSON, marshalErr := json.Marshal(displayContent)
		if marshalErr != nil {
			slog.Warn("marshal control response notification", "agent_id", agentID, "error", marshalErr)
		} else if err := svc.Output.persistAndBroadcast(agentID, dbAgent.AgentProvider, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, displayJSON, agent.SpanInfo{}, nil); err != nil {
			slog.Warn("failed to persist control response notification", "agent_id", agentID, "error", err)
		}
	}

	// Detect plan mode changes from control responses (agent-initiated).
	skipSend := false
	if crPayload.Response.Response.Behavior == agent.ControlBehaviorAllow {
		switch toolName {
		case agent.ToolNameEnterPlanMode:
			svc.setAgentPermissionModeWithAgent(dbAgent, agent.PermissionModePlan)
		case agent.ToolNameExitPlanMode:
			// Determine target permission mode from control_response.
			targetMode := agent.PermissionModeAcceptEdits
			if crPayload.PermissionMode != "" {
				targetMode = crPayload.PermissionMode
			}
			svc.setAgentPermissionModeWithAgent(dbAgent, targetMode)

			// Remove the planModeToolUse entry so detectPlanModeFromToolResult
			// does not override the mode we just set.
			if toolUseID != "" {
				svc.Output.planModeToolUse.Delete(toolUseID)
			}

			if crPayload.ClearContext {
				// When clearing context, don't send the approval to the
				// agent — we're about to stop it anyway. This avoids
				// the race where the agent acts on the approval before
				// initiatePlanExecution kills it.
				go svc.initiatePlanExecution(agentID, targetMode)
				skipSend = true
			}
			// When !clearContext, the agent continues in current context.
		}
	}

	// Delete the answered control request.
	_ = svc.Queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})

	return skipSend
}

// initiatePlanExecution clears the agent's context and sends the plan as a
// user message. For providers that support in-place context clearing (Codex),
// it sends a new thread/start on the running process. For others (Claude Code),
// it stops and restarts the agent process entirely.
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

	planMsg := "Execute the following plan:\n\n---\n\n" + planContent
	if dbAgent.PlanFilePath != "" {
		planMsg += "\n\n---\n\nThe above plan has been written to " + dbAgent.PlanFilePath + " — re-read it if needed."
	}

	// Try in-place context clearing first (e.g. Codex thread/start on
	// the running process). Fall back to full restart if not supported.
	if newSessionID, ok := svc.Agents.ClearContext(agentID); ok {
		slog.Info("plan exec: context cleared in-place", "agent_id", agentID, "session_id", newSessionID)

		// Update the session ID in the DB.
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: newSessionID,
			ID:             agentID,
		})

		// Clear span tracking and broadcast notifications.
		svc.Output.ResetSpanTracker(agentID)
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type": "context_cleared",
		})
		svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":           "plan_execution",
			"plan_file_path": dbAgent.PlanFilePath,
		})
	} else {
		// Full restart path (Claude Code and other providers).
		svc.initiatePlanExecutionRestart(agentID, targetMode, dbAgent, planMsg)
	}

	// Send plan content as user message and persist it for the frontend.
	if err := svc.Agents.SendInput(agentID, planMsg, nil); err != nil {
		slog.Error("plan exec: failed to send plan content", "agent_id", agentID, "error", err)
	}

	// Persist the plan execution message so the frontend can render it as
	// a collapsible "Execute plan" bubble.
	innerJSON, err := json.Marshal(map[string]interface{}{
		"content":       planMsg,
		"planExecution": true,
	})
	if err != nil {
		slog.Warn("plan exec: marshal plan execution message", "agent_id", agentID, "error", err)
		return
	}
	if err := svc.Output.persistAndBroadcast(agentID, dbAgent.AgentProvider, leapmuxv1.MessageRole_MESSAGE_ROLE_USER, innerJSON, agent.SpanInfo{}, nil); err != nil {
		slog.Warn("plan exec: failed to persist plan execution message", "agent_id", agentID, "error", err)
	}
}

// initiatePlanExecutionRestart performs a full stop-and-restart to clear
// context for providers that don't support in-place clearing (e.g. Claude Code).
func (svc *Context) initiatePlanExecutionRestart(agentID, targetMode string, dbAgent db.Agent, planMsg string) {
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()

	// DiscardOutput before stop so shutdown noise ("stream closed") does not
	// land in the persisted chat history.
	svc.Agents.DiscardOutputAndStopAgent(agentID)

	// Clear span tracking state from the previous session.
	svc.Output.ResetSpanTracker(agentID)

	// Broadcast context_cleared and plan_execution as separate notifications.
	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": "context_cleared",
	})
	svc.Output.BroadcastNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type":           "plan_execution",
		"plan_file_path": dbAgent.PlanFilePath,
	})

	// Restart agent with plan content.
	model := modelOrDefault(dbAgent.Model, dbAgent.AgentProvider)
	extraSettings := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	confirmedSettings, err := svc.Agents.StartAgent(bgCtx(), agent.Options{
		AgentID:        agentID,
		Model:          model,
		Effort:         dbAgent.Effort,
		WorkingDir:     dbAgent.WorkingDir,
		PermissionMode: targetMode,
		ExtraSettings:  extraSettings,
		StartupTimeout: svc.agentStartupTimeout(),
		APITimeout:     svc.agentAPITimeout(),
		Shell:          svc.agentShell(),
		LoginShell:     svc.agentLoginShell(),
		HomeDir:        svc.HomeDir,
		AgentProvider:  dbAgent.AgentProvider,
	}, sink)
	if err != nil {
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
	if err := svc.persistConfirmedAgentSettings(agentID, dbAgent.AgentProvider, model, dbAgent.Effort, targetMode, extraSettings, confirmedSettings); err != nil {
		slog.Warn("plan exec: failed to persist confirmed settings", "agent_id", agentID, "error", err)
	}

	slog.Info("plan exec: agent restarted successfully", "agent_id", agentID)
}

// parseSetPermissionMode checks if a control_request is a set_permission_mode
// request and returns the requested mode. Returns ("", false) if not a match.
func parseSetPermissionMode(content string) (string, bool) {
	if !strings.Contains(content, "set_permission_mode") {
		return "", false
	}
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

// isInterruptRequest checks whether the raw payload is an interrupt request
// for a supported provider format.
func isInterruptRequest(content string) bool {
	var msg struct {
		Method  string `json:"method"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Request.Subtype == "interrupt" || msg.Method == "turn/interrupt" || msg.Method == "session/cancel" || msg.Method == "cancel"
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
	return &leapmuxv1.AgentChatMessage{
		Id:                 m.ID,
		Role:               leapmuxv1.MessageRole(m.Role),
		Content:            m.Content,
		Seq:                m.Seq,
		DeliveryError:      m.DeliveryError,
		ContentCompression: leapmuxv1.ContentCompression(m.ContentCompression),
		AgentProvider:      m.AgentProvider,
		CreatedAt:          timefmt.Format(m.CreatedAt),
		Depth:              int32(m.Depth),
		SpanId:             m.SpanID,
		ParentSpanId:       m.ParentSpanID,
		SpanType:           m.SpanType,
		SpanColor:          int32(m.SpanColor),
		SpanLines:          m.SpanLines,
	}
}

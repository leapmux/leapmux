package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/grpc/codes"
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
	d.Register("OpenAgent", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		title, err := sanitizeOptionalTitle(r.GetTitle())
		if err != nil {
			sendInvalidArgument(sender, err.Error())
			return
		}
		// Empty title means "you pick one". Default to a random
		// "Agent <Name>" from the shared pool so CLI-spawned agents
		// match the format UI-spawned ones get. Collisions are
		// allowed (cosmetic; the user can rename either tab).
		if title == "" {
			title = pickAgentTitle()
		}

		agentID := id.Generate()
		agent.TraceStartupPhase(agentID, "handler_begin")

		workingDir := expandTilde(r.GetWorkingDir())
		if workingDir == "" {
			workingDir = svc.HomeDir
		}

		// Validate git-mode options on the sync path so bad input (invalid
		// branch name, non-existent base branch, worktree path collision,
		// etc.) fails the RPC with InvalidArgument before we mutate any
		// state. The actual mutation happens inside runAgentStartup.
		plan, gmErr := svc.validateGitMode(ctx, workingDir, &r)
		if gmErr != nil {
			sendValidationError(sender, gmErr)
			return
		}

		// Resolve default model based on agent provider.
		agentProvider := r.GetAgentProvider()
		if agentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			agentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
		}
		// Resolve the initial option selections: the client's requested values
		// (model/effort/permissionMode/provider options), filled with provider
		// defaults for any missing well-known and provider-specific ids.
		requested := mergeOptions(nil, r.GetOptions())
		options := resolveProviderDefaults(requested, agentProvider)
		if options[agent.OptionIDPermissionMode] == "" {
			options[agent.OptionIDPermissionMode] = agent.PermissionModeOrDefault(agentProvider, "")
		}
		// Reject a spawn whose EXPLICITLY-requested permission mode isn't valid for the provider, so a
		// typo'd --permission-mode fails fast with a clear error instead of reaching the provider and
		// dying at startup (Claude fails startup on a bad set_permission_mode). Model and effort are
		// NOT validated here: every provider discovers its model catalog (and effort tiers) from the
		// running CLI/daemon, seeding only a static fallback, so a value valid in the live catalog but
		// absent from the seed would be wrongly rejected -- the running session validates those.
		if err := agent.ValidateLaunchOptions(agentProvider, requested); err != nil {
			sendInvalidArgument(sender, err.Error())
			return
		}

		// Track whether this agent was created via session resume.
		resumed := ptrconv.BoolToInt64(r.GetAgentSessionId() != "")

		agent.TraceStartupPhase(agentID, "gitmode_validated")

		// Persist the agent row + read it back under a fresh background
		// context: the DB write must survive a mid-RPC disconnect so a
		// retry from the same client doesn't observe a half-created agent
		// (the validation phase above is the only step that should
		// fail-fast on disconnect). The actual worktree mutation happens
		// later inside runAgentStartup, which uses its own startupCtx.
		if err := svc.createAgentRecord(bgCtx(), db.CreateAgentParams{
			ID:            agentID,
			WorkspaceID:   r.GetWorkspaceId(),
			WorkingDir:    plan.PlannedWorkingDir,
			HomeDir:       svc.HomeDir,
			Title:         title,
			Options:       marshalOptions(options),
			AgentProvider: agentProvider,
			Resumed:       resumed,
		}); err != nil {
			slog.Error("failed to create agent", "error", err)
			sendInternalError(sender, "failed to create agent")
			return
		}

		dbAgent, err := svc.getAgentByID(bgCtx(), agentID)
		if err != nil {
			slog.Error("failed to fetch created agent", "error", err)
			sendInternalError(sender, "failed to fetch created agent")
			return
		}

		startupCtx, cancel := context.WithCancel(context.Background())
		svc.AgentStartup.begin(agentID, cancel)

		remoteEnvs := svc.spawnRemoteIPC("agent", agentID, "", svc.registerAgentCleanup, func() ([]string, func(), error) {
			return svc.RemoteIPC.AgentSpawning(AgentSpawnInfo{
				UserID:        userID,
				OrgID:         r.GetOrgId(),
				WorkspaceID:   r.GetWorkspaceId(),
				WorkerID:      svc.WorkerID,
				TabID:         agentID,
				WorkingDir:    plan.PlannedWorkingDir,
				AgentProvider: agentlabels.CLIAlias(agentProvider),
			})
		})

		agentOpts := agent.Options{
			AgentID:         agentID,
			WorkingDir:      plan.PlannedWorkingDir,
			ResumeSessionID: r.GetAgentSessionId(),
			Options:         options,
			StartupTimeout:  svc.agentStartupTimeout(),
			APITimeout:      svc.agentAPITimeout(),
			Shell:           svc.agentShell(),
			LoginShell:      svc.agentLoginShell(),
			HomeDir:         svc.HomeDir,
			AgentProvider:   agentProvider,
			ExtraEnv:        remoteEnvs,
		}

		agent.TraceStartupPhase(agentID, "before_response")
		sendProtoResponse(sender, &leapmuxv1.OpenAgentResponse{
			Agent: svc.agentToProto(&dbAgent, false, nil),
		})
		agent.TraceStartupPhase(agentID, "response_sent")

		// Kick off subprocess startup in the background.
		go svc.runAgentStartup(startupCtx, dbAgent, plan, agentOpts)
	})

	// CloseAgent backgrounds the entire close flow (subprocess stop, DB
	// close, optional worktree removal) so the work survives a mid-RPC
	// disconnect from the client that initiated the close. The dispatcher
	// ctx is intentionally not threaded — using it would cancel the
	// cleanup partway through if the user clicked away.
	d.RegisterTracked("CloseAgent", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		if _, ok := svc.requireAccessibleAgent(sender, agentID); !ok {
			return
		}

		// Tracked via dispatcher RegisterTracked above so a concurrent
		// Shutdown drains the close flow (stop → DB close → unregister
		// → optional worktree remove) before tearing down the DB pool.
		// The frontend fires this RPC fire-and-forget after removing
		// the tab from the UI. The AgentStartup goroutine's trailing
		// rollback work is tracked separately by
		// AgentStartup.WaitForInFlight and drained in Shutdown.
		result := svc.closeTabCommon(
			leapmuxv1.TabType_TAB_TYPE_AGENT,
			agentID,
			r.GetWorktreeAction(),
			func() {
				svc.AgentStartup.cancelAndClear(agentID)
				svc.Agents.StopAgent(agentID)
				svc.Output.ClearAgentRuntimeState(agentID)
				svc.runAgentCleanup(agentID)
			},
			func() error { return svc.Queries.CloseAgent(bgCtx(), agentID) },
		)
		sendProtoResponse(sender, &leapmuxv1.CloseAgentResponse{Result: result})
	})

	// SendAgentMessage persists the user message, forwards it to the agent
	// subprocess, and broadcasts it to every connected watcher. The
	// dispatcher ctx is intentionally not threaded — the persist + forward
	// + broadcast must complete even if the originating client disconnects
	// a millisecond after firing the RPC, otherwise *other* watchers in
	// the same workspace would silently miss the message.
	d.Register("SendAgentMessage", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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
		if status, _, _, ok := svc.AgentStartup.status(agentID); ok && status == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
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

		// Capture currently-active spans so the user message renders with
		// passthrough vertical bars instead of breaking the column.
		spanLines := svc.Output.snapshotPassthroughSpanLines(agentID)

		// Persist the user message.
		seq, err := createMessageRow(bgCtx(), svc.Queries, db.CreateMessageParams{
			ID:                 messageID,
			AgentID:            agentID,
			Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:            compressed,
			ContentCompression: compressionType,
			Depth:              0,
			SpanID:             "",
			ParentSpanID:       "",
			SpanLines:          spanLines,
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
			Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:            compressed,
			ContentCompression: compressionType,
			Seq:                seq,
			AgentProvider:      dbAgent.AgentProvider,
			CreatedAt:          timefmt.Format(now),
			Depth:              0,
			SpanLines:          spanLines,
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

	// SendAgentRawMessage forwards a provider-shaped raw message (Codex
	// interrupt frames etc.) to the agent subprocess. The forward + any
	// synthetic-message persistence must complete past a client
	// disconnect; dispatcher ctx is intentionally not threaded.
	d.Register("SendAgentRawMessage", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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
		if dbAgent.AgentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX && agent.IsInterruptRequest(dbAgent.AgentProvider, content) {
			svc.persistSyntheticUserMessage(agentID, dbAgent.AgentProvider, "[Request interrupted by user]")
		}

		svc.handleControlRequestMessage(agentID, dbAgent.AgentProvider, content)
		sendProtoResponse(sender, &leapmuxv1.SendAgentRawMessageResponse{})
	})

	// ListAgents is a synchronous read-only handler: the response shape is
	// the only side effect, so the inbound dispatcher ctx is threaded
	// through the DB and git probes. A mid-call client disconnect cancels
	// the remaining work instead of wasting subprocess forks against
	// BatchGetGitStatus.
	d.Register("ListAgents", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		agents, err := svc.Queries.ListAgentsByIDs(ctx, tabIDs)
		if err != nil {
			slog.Error("failed to list agents", "tab_ids", tabIDs, "error", err)
			sendInternalError(sender, "failed to list agents")
			return
		}

		// Filter by access control: only return agents in accessible
		// workspaces. AuthorizerForSender abstracts over E2EE channels and
		// local-IPC streams (which have no channel id but carry a token
		// scope registered at request entry).
		accessibleWsIDs := svc.AuthorizerForSender(sender).AccessibleSet()

		workingDirs := make([]string, len(agents))
		for i := range agents {
			workingDirs[i] = agents[i].WorkingDir
		}
		gitStatuses := gitutil.BatchGetGitStatus(ctx, workingDirs)

		protoAgents := make([]*leapmuxv1.AgentInfo, 0, len(agents))
		for i := range agents {
			if accessibleWsIDs != nil && !accessibleWsIDs[agents[i].WorkspaceID] {
				continue
			}
			hasAgent := svc.Agents.HasAgent(agents[i].ID)
			// agentToProto -> optionGroupsForAgent -> optionGroupsView already preloads the
			// cached option-group catalog from the DB for an inactive agent (and decodes
			// option_groups exactly once), so no separate PreloadCache is needed here -- a
			// second one would decode and re-seed every closed agent's catalog redundantly.
			protoAgents = append(protoAgents, svc.agentToProto(&agents[i], hasAgent, gitStatuses[i]))
		}

		sendProtoResponse(sender, &leapmuxv1.ListAgentsResponse{
			Agents: protoAgents,
		})
	})

	// ListAgentMessages is a synchronous read-only paginated handler: the
	// response shape is the only side effect, so the inbound dispatcher
	// ctx is threaded through every DB read. A mid-call client disconnect
	// cancels the remaining page query instead of wasting DB load.
	d.Register("ListAgentMessages", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		// Only ship the to-do list on the initial "latest" page — scroll
		// pagination requests don't need to re-fetch it (the client
		// already has the authoritative snapshot from cold start and
		// receives live mutations via AgentTodosChanged broadcasts).
		var todoItems []todoevents.Item
		if afterSeq == 0 && beforeSeq == 0 {
			items, todoErr := svc.Output.LoadTodos(ctx, agentID)
			if todoErr != nil {
				slog.Warn("failed to load agent_todos", "agent_id", agentID, "error", todoErr)
			}
			todoItems = items
		}

		var dbMessages []db.Message
		var queryErr error

		if beforeSeq > 0 {
			// BACKWARD: fetch messages with seq < before_seq, returned in descending order.
			dbMessages, queryErr = svc.Queries.ListMessagesByAgentIDReverse(ctx, db.ListMessagesByAgentIDReverseParams{
				AgentID: agentID,
				Seq:     beforeSeq,
				Limit:   limit + 1, // Fetch one extra to determine has_more.
			})
		} else if afterSeq > 0 {
			// FORWARD: fetch messages with seq > after_seq in ascending order.
			dbMessages, queryErr = svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
				AgentID: agentID,
				Seq:     afterSeq,
				Limit:   limit + 1,
			})
		} else {
			// LATEST: fetch the most recent messages.
			dbMessages, queryErr = svc.Queries.ListLatestMessagesByAgentID(ctx, db.ListLatestMessagesByAgentIDParams{
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
			Todos:    todoevents.ItemsToProto(todoItems),
		})
	})

	// RenameAgent persists the new title and broadcasts a TabRenamed event
	// to other clients in the same workspace. The DB write + broadcast
	// must complete past a client disconnect (otherwise sibling clients
	// would miss the rename); dispatcher ctx is intentionally not threaded.
	d.Register("RenameAgent", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.RenameAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		agentID := r.GetAgentId()
		dbAgent, ok := svc.requireAccessibleAgent(sender, agentID)
		if !ok {
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

		// Broadcast over the worker-private E2EE bus so other clients of
		// the same workspace can update their tab title without the hub
		// ever seeing the new title string.
		if svc.PrivateEvents != nil {
			svc.PrivateEvents.PublishTabRenamed(
				dbAgent.WorkspaceID, agentID, leapmuxv1.TabType_TAB_TYPE_AGENT,
				r.GetTitle(), sender.ChannelID(),
			)
		}

		sendProtoResponse(sender, &leapmuxv1.RenameAgentResponse{})
	})

	// DeleteAgentMessage removes the row and broadcasts a MessageDeleted
	// event to every watcher. The DB write + broadcast must complete past
	// a client disconnect; dispatcher ctx is intentionally not threaded.
	d.Register("DeleteAgentMessage", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

	// UpdateAgentSettings persists the new settings and (for providers
	// that need it) restarts the agent subprocess. Both must complete past
	// a client disconnect, otherwise the agent ends up in a half-applied
	// state mismatched with the persisted row. Dispatcher ctx is
	// intentionally not threaded.
	d.Register("UpdateAgentSettings", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		provider := dbAgent.AgentProvider
		oldOptions := loadOptions(dbAgent.Options, provider)
		newOptions := svc.sanitizeIncomingOptions(agentID, provider, oldOptions, r.GetSettings().GetOptions())

		// Optimistic DB write of the requested options; corrected below to the values
		// the session actually confirms (settledOptions). Persist only the axes this edit
		// changes, via compare-and-swap, so a concurrent server-initiated PersistSettingsRefresh
		// (no shared lock) can't be clobbered by a stale full-map blob and vice versa.
		optimistic, _, err := casPersistAgentOptions(bgCtx(), svc.Queries, agentID, dbAgent.Options,
			optionsChangeDelta(oldOptions, newOptions))
		if err != nil {
			slog.Error("failed to update agent settings", "agent_id", agentID, "error", err)
			sendInternalError(sender, "failed to update agent settings")
			return
		}
		// Refresh the in-memory row to the blob we just persisted so applySettingsLive's
		// corrective CAS starts from the current row rather than the pre-write snapshot --
		// otherwise its first compare-and-swap is guaranteed to miss (the row already moved)
		// and burns an extra re-read before converging.
		dbAgent.Options = optimistic

		// settledOptions is the option map the session actually settled on -- the
		// requested newOptions overlaid with whatever the running provider confirmed,
		// then filled with provider defaults (confirmedOptions). The provider's
		// confirmation can differ from the request on ANY axis:
		//   - effort: selecting ultracode without the workflows entitlement lands on
		//     xhigh; selecting Auto (or a model switch, which resets effort to Auto)
		//     relaunches without --effort and the CLI resolves Auto to a concrete level.
		//   - model: the account-default sentinel ("default") resolves to a concrete
		//     model the session reports back.
		//   - options: an ACP reasoning_effort the server downgraded, a Codex
		//     sandbox/service_tier it adjusted.
		// Reporting the settled (not requested) values is INTENTIONAL -- the
		// notification, the persisted row, and the RPC reply all state what the session
		// is actually running. settledOptions drives all three so they can't disagree.
		// For an offline edit or a failed restart no agent confirms anything, so it
		// stays equal to the requested newOptions.
		settledOptions := newOptions
		if svc.Agents.HasAgent(agentID) {
			// Try a live update first; fall back to a full restart for changes the
			// provider can't apply in place (e.g. Claude Code switching effort to auto).
			if settled, applied := svc.applySettingsLive(dbAgent, newOptions); applied {
				settledOptions = settled
			} else {
				settledOptions = svc.applySettingsViaRestart(dbAgent, newOptions)
			}
		}

		// Broadcast settings_changed notification for the chat view, diffing the
		// stored options against the settled ones (every axis corrected to the value
		// the session actually confirmed).
		changes := svc.buildSettingsChanges(&dbAgent, oldOptions, settledOptions, sortedOptionKeys(oldOptions, settledOptions), true)
		if len(changes) > 0 {
			svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
				"type":    agent.NotificationTypeSettingsChanged,
				"changes": changes,
			})
		}

		// Return the settled options so the client reconciles its optimistic state
		// against the values this RPC confirmed -- not a separately-broadcast catalog,
		// which would depend on cross-channel ordering (the broadcast arriving before
		// this reply).
		sendProtoResponse(sender, &leapmuxv1.UpdateAgentSettingsResponse{ConfirmedOptions: settledOptions})
	})

	// SendControlResponse forwards the user's allow/deny on a tool-use
	// request to the agent subprocess. The forward must reach the agent
	// even if the originating client window closed (the agent process is
	// blocked waiting for it); dispatcher ctx is intentionally not threaded.
	d.Register("SendControlResponse", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		// Drop the prompt before forwarding the response. Claude can emit the
		// follow-up control_request immediately after it reads the denial; if
		// the old request is still pending client-side, the revised request can
		// be hidden behind stale dedup state.
		reqID := extractControlResponseRequestID(content)
		if reqID != "" {
			sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
			sink.DeleteControlRequest(reqID)
			sink.BroadcastControlCancel(reqID)
		}

		svc.persistSyntheticUserMessage(agentID, dbAgent.AgentProvider, displayText)

		if !skipSend {
			if err := svc.Agents.SendRawInput(agentID, content); err != nil {
				slog.Error("failed to send control response to agent",
					"agent_id", agentID, "error", err)
				sendNotFoundError(sender, "agent not found or not running")
				return
			}
		}

		sendProtoResponse(sender, &leapmuxv1.SendControlResponseResponse{})
	})

	// InterruptAgent sends a signal to the agent subprocess; the signal
	// delivery must happen even if the requesting client disconnects mid-
	// RPC. Dispatcher ctx is intentionally not threaded.
	d.Register("InterruptAgent", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.InterruptAgentRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		agentID := r.GetAgentId()
		if _, ok := svc.requireAccessibleAgent(sender, agentID); !ok {
			return
		}
		if err := svc.Agents.Interrupt(agentID); err != nil {
			slog.Warn("interrupt failed", "agent_id", agentID, "error", err)
			sendNotFoundError(sender, "agent not found or not running")
			return
		}
		sendProtoResponse(sender, &leapmuxv1.InterruptAgentResponse{})
	})

	// WatchWorkspacePrivateEvents streams worker-private workspace events
	// (TabRenamed, FileTabPathRegistered, FileTabPathRevoked) over the
	// existing E2EE channel. The bootstrap-replay sends one
	// FileTabPathRegistered per row in worker_file_tabs for the
	// requested workspace before any live events.
	// SnapshotAndSubscribe drives the stream lifetime off the sender's
	// channel directly, not the dispatcher ctx (which only covers the
	// initial subscribe call). The bgCtx() passed in is the snapshot
	// cursor's context, intentionally background so a slow snapshot
	// doesn't get cancelled by the RPC dispatcher unwinding after the
	// subscribe returns. Dispatcher ctx is intentionally not threaded.
	d.Register("WatchWorkspacePrivateEvents", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.WatchWorkspacePrivateEventsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		workspaceID := r.GetWorkspaceId()
		if workspaceID == "" {
			sendInvalidArgument(sender, "workspace_id is required")
			return
		}
		if !svc.requireAccessibleWorkspace(sender, workspaceID) {
			return
		}
		if svc.PrivateEvents == nil {
			return
		}
		_ = svc.PrivateEvents.SnapshotAndSubscribe(
			bgCtx(),
			workspaceID,
			func(wsID string) []*leapmuxv1.WorkspacePrivateEvent {
				if svc.FileTabPaths == nil {
					return nil
				}
				snapshot, err := svc.FileTabPaths.SnapshotForWorkspace(bgCtx(), wsID)
				if err != nil {
					return nil
				}
				return snapshot
			},
			func(evt *leapmuxv1.WorkspacePrivateEvent) error {
				data, err := proto.Marshal(evt)
				if err != nil {
					return err
				}
				return sender.SendStream(&leapmuxv1.InnerStreamMessage{Payload: data})
			},
		)
	})

	// RegisterFileTabPath writes the (tab_id → path) registry row. The
	// write must survive a client disconnect, otherwise a subsequent
	// GetFileTabPath from a sibling client would see a stale "not found".
	// Dispatcher ctx is intentionally not threaded.
	d.Register("RegisterFileTabPath", func(_ context.Context, _ string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.RegisterFileTabPathRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if r.GetTabId() == "" || r.GetOrgId() == "" || r.GetWorkspaceId() == "" || r.GetFilePath() == "" {
			sendInvalidArgument(sender, "tab_id, org_id, workspace_id, file_path are required")
			return
		}
		if !svc.requireAccessibleWorkspace(sender, r.GetWorkspaceId()) {
			return
		}
		if svc.FileTabPaths == nil {
			sendInternalError(sender, "file tab path store unavailable")
			return
		}
		if err := svc.FileTabPaths.Register(bgCtx(), RegisterFileTabPathParams{
			OrgID: r.GetOrgId(), TabID: r.GetTabId(),
			WorkspaceID: r.GetWorkspaceId(), FilePath: r.GetFilePath(),
		}); err != nil {
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, &leapmuxv1.RegisterFileTabPathResponse{})
	})

	// GetFileTabPath is a synchronous read-only handler: the response is
	// the only side effect, so the inbound dispatcher ctx is threaded
	// through the store lookup to fail-fast on disconnect.
	d.Register("GetFileTabPath", func(ctx context.Context, _ string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.GetFileTabPathRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if r.GetTabId() == "" || r.GetOrgId() == "" {
			sendInvalidArgument(sender, "tab_id and org_id are required")
			return
		}
		if svc.FileTabPaths == nil {
			sendInternalError(sender, "file tab path store unavailable")
			return
		}
		wsID, path, err := svc.FileTabPaths.Get(ctx, r.GetOrgId(), r.GetTabId())
		if err != nil {
			if errors.Is(err, ErrFileTabPathNotFound) {
				sendNotFoundError(sender, "file tab path not found")
				return
			}
			sendInternalError(sender, err.Error())
			return
		}
		if !svc.requireAccessibleWorkspace(sender, wsID) {
			return
		}
		sendProtoResponse(sender, &leapmuxv1.GetFileTabPathResponse{
			WorkspaceId: wsID,
			FilePath:    path,
		})
	})

	// RevokeFileTabPath deletes the (tab_id → path) row and runs the
	// shared closeTabCommon flow so the worktree-tab link (and any
	// resulting `git worktree remove` when the user picked Delete in
	// the last-tab dialog) is handled identically to CloseAgent /
	// CloseTerminal. The write must survive a client disconnect for the
	// same reason as the register handler — otherwise a stale row would
	// survive past the user's intended revocation. Dispatcher ctx is
	// intentionally not threaded.
	d.RegisterTracked("RevokeFileTabPath", func(_ context.Context, _ string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.RevokeFileTabPathRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if r.GetTabId() == "" || r.GetOrgId() == "" {
			sendInvalidArgument(sender, "tab_id and org_id are required")
			return
		}
		if svc.FileTabPaths == nil {
			sendInternalError(sender, "file tab path store unavailable")
			return
		}
		// Workspace auth check uses the tab's currently-stored
		// workspace_id (rollback path: the CRDT tab may not exist yet).
		wsID, _, getErr := svc.FileTabPaths.Get(bgCtx(), r.GetOrgId(), r.GetTabId())
		if getErr != nil {
			if errors.Is(getErr, ErrFileTabPathNotFound) {
				// Already revoked — idempotent success.
				sendProtoResponse(sender, &leapmuxv1.RevokeFileTabPathResponse{})
				return
			}
			sendInternalError(sender, getErr.Error())
			return
		}
		if !svc.requireAccessibleWorkspace(sender, wsID) {
			return
		}
		// Drive the shared closeTabCommon flow so the worktree-tab link
		// (and any user-requested `git worktree remove`) is handled
		// identically to CloseAgent / CloseTerminal.
		result := svc.closeFileTabCommon(r.GetOrgId(), r.GetTabId(), r.GetWorktreeAction())
		sendProtoResponse(sender, &leapmuxv1.RevokeFileTabPathResponse{Result: result})
	})

	// RelocateFileTabPath moves the (tab_id → path) row across workspaces
	// after a cross-workspace tab move. The write must survive a client
	// disconnect — otherwise the destination workspace would observe a
	// missing path row even though the CRDT moved the tab. Dispatcher ctx
	// is intentionally not threaded.
	d.Register("RelocateFileTabPath", func(_ context.Context, _ string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.RelocateFileTabPathRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if r.GetTabId() == "" || r.GetOrgId() == "" || r.GetNewWorkspaceId() == "" {
			sendInvalidArgument(sender, "tab_id, org_id, new_workspace_id are required")
			return
		}
		if svc.FileTabPaths == nil {
			sendInternalError(sender, "file tab path store unavailable")
			return
		}
		// Auth: caller must have access to BOTH the current and the
		// destination workspaces (mirrors the CRDT's cross-workspace
		// move auth rule).
		wsID, _, err := svc.FileTabPaths.Get(bgCtx(), r.GetOrgId(), r.GetTabId())
		if err != nil {
			if errors.Is(err, ErrFileTabPathNotFound) {
				sendNotFoundError(sender, "file tab path not found")
				return
			}
			sendInternalError(sender, err.Error())
			return
		}
		if !svc.requireAccessibleWorkspace(sender, wsID) {
			return
		}
		if !svc.requireAccessibleWorkspace(sender, r.GetNewWorkspaceId()) {
			return
		}
		if err := svc.FileTabPaths.Relocate(bgCtx(), r.GetOrgId(), r.GetTabId(), r.GetNewWorkspaceId()); err != nil {
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, &leapmuxv1.RelocateFileTabPathResponse{})
	})

	d.Register("ListAvailableProviders", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListAvailableProvidersRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		// Discovery probes run in parallel (one goroutine per provider), so
		// this deadline is effectively a per-probe wall-clock cap. Uses the
		// API timeout rather than startup timeout: probing binary presence
		// should never take as long as the MCP-heavy agent handshake.
		// Derived from the inbound ctx so a dialog dismissal cancels every
		// probe in flight.
		ctx, cancel := context.WithTimeout(ctx, svc.agentAPITimeout())
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
	//
	// Dispatcher ctx is intentionally not threaded: the handler returns
	// after registering watchers + completing the synchronous replay, but
	// the live-event stream survives indefinitely via the EventWatcher.
	// Using the dispatcher ctx for the replay's bootstrap reads would
	// risk cancelling them when the handler unwinds before the bg
	// goroutines finish writing to the stream.
	d.Register("WatchEvents", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.WatchEventsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		auth := svc.AuthorizerForSender(sender)
		channelID := auth.SubscriberID()
		allowedWorkspaces := auth.AccessibleSet()

		// Create an EventWatcher for this stream.
		watcher := &EventWatcher{
			ChannelID: channelID,
			Sender:    sender,
		}

		// Filter agents by access control and register watchers FIRST
		// so no broadcasts are missed during the replay phase. Retain
		// the fetched rows so the replay loop below doesn't have to
		// re-fetch them. A single batched SELECT replaces N GetAgentByID
		// round trips on page refresh; ListAgentsByIDs filters closed_at
		// IS NULL, so closed rows fall into the "not returned" branch and
		// land in rejectedAgentIDs with the same semantics as before.
		requestedAgentIDs := make([]string, 0, len(r.GetAgents()))
		for _, agentEntry := range r.GetAgents() {
			requestedAgentIDs = append(requestedAgentIDs, agentEntry.GetAgentId())
		}
		agentRowsByID := make(map[string]db.Agent, len(requestedAgentIDs))
		if len(requestedAgentIDs) > 0 {
			rows, err := svc.Queries.ListAgentsByIDs(bgCtx(), requestedAgentIDs)
			if err != nil {
				slog.Warn("WatchEvents: ListAgentsByIDs failed", "error", err)
			}
			for _, row := range rows {
				agentRowsByID[row.ID] = row
			}
		}
		var verifiedAgents []*leapmuxv1.WatchAgentEntry
		var verifiedAgentRows []db.Agent
		var rejectedAgentIDs []string
		for _, agentEntry := range r.GetAgents() {
			agentID := agentEntry.GetAgentId()
			agentRow, ok := agentRowsByID[agentID]
			if !ok || !allowedWorkspaces[agentRow.WorkspaceID] {
				rejectedAgentIDs = append(rejectedAgentIDs, agentID)
				continue
			}
			svc.Watchers.WatchAgent(agentID, watcher)
			verifiedAgents = append(verifiedAgents, agentEntry)
			verifiedAgentRows = append(verifiedAgentRows, agentRow)
		}

		// Filter terminals by access control and register watchers. Same
		// batched-lookup rationale as the agent loop above.
		requestedTerminals := r.GetTerminals()
		requestedTerminalIDs := make([]string, 0, len(requestedTerminals))
		afterOffsetByID := make(map[string]int64, len(requestedTerminals))
		for _, entry := range requestedTerminals {
			requestedTerminalIDs = append(requestedTerminalIDs, entry.GetTerminalId())
			afterOffsetByID[entry.GetTerminalId()] = entry.GetAfterOffset()
		}
		termRowsByID := make(map[string]db.Terminal, len(requestedTerminalIDs))
		if len(requestedTerminalIDs) > 0 {
			rows, err := svc.Queries.ListTerminalsByIDs(bgCtx(), requestedTerminalIDs)
			if err != nil {
				slog.Warn("WatchEvents: ListTerminalsByIDs failed", "error", err)
			}
			for _, row := range rows {
				termRowsByID[row.ID] = row
			}
		}
		var verifiedTerminalIDs []string
		var verifiedTerminalRows []db.Terminal
		var rejectedTerminalIDs []string
		for _, termID := range requestedTerminalIDs {
			termRow, ok := termRowsByID[termID]
			if !ok || !allowedWorkspaces[termRow.WorkspaceID] {
				rejectedTerminalIDs = append(rejectedTerminalIDs, termID)
				continue
			}
			svc.Watchers.WatchTerminal(termID, watcher)
			verifiedTerminalIDs = append(verifiedTerminalIDs, termID)
			verifiedTerminalRows = append(verifiedTerminalRows, termRow)
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
				ErrorCode:    int32(codes.NotFound),
				ErrorMessage: fmt.Sprintf("agents %v and/or terminals %v not found or not accessible", rejectedAgentIDs, rejectedTerminalIDs),
			})
			return
		}

		// Compute git statuses in a single deduplicated batch so the
		// per-agent replay loop below doesn't serialize N git shell-outs
		// on page refresh (and multiple tabs on the same repo share one
		// call). The DB rows are already in verifiedAgentRows from the
		// access-control loop above.
		replayDirs := make([]string, len(verifiedAgentRows))
		for i, row := range verifiedAgentRows {
			replayDirs[i] = row.WorkingDir
		}
		replayGitStatuses := gitutil.BatchGetGitStatus(bgCtx(), replayDirs)

		// Process each verified agent entry: replay messages, send status.
		for i, agentEntry := range verifiedAgents {
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
					for j := range messages {
						broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
							Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
								AgentEvent: &leapmuxv1.AgentEvent{
									AgentId: agentID,
									Event: &leapmuxv1.AgentEvent_AgentMessage{
										AgentMessage: messageToProto(&messages[j]),
									},
								},
							},
						})
					}
				}
			}

			// Send a statusChange marker (signals end of message replay).
			dbAgent := verifiedAgentRows[i]
			hasAgent := svc.Agents.HasAgent(agentID)
			// Preload the cached option-group catalog from DB for inactive agents.
			if !hasAgent {
				svc.Agents.PreloadCache(agentID, parseOptionGroups(dbAgent.OptionGroups))
			}
			status, startupError, startupMessage := svc.deriveAgentStatus(&dbAgent, hasAgent)
			var statusChange *leapmuxv1.AgentStatusChange
			switch status {
			case leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:
				statusChange = buildAgentStartingStatus(&dbAgent, startupMessage, replayGitStatuses[i])
			case leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED:
				statusChange = buildAgentFailedStatus(&dbAgent, startupError, replayGitStatuses[i])
			case leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE:
				statusChange = svc.buildAgentActiveStatus(&dbAgent, replayGitStatuses[i])
			default:
				statusChange = buildAgentInactiveStatus(&dbAgent, replayGitStatuses[i])
			}
			broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
				Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
					AgentEvent: &leapmuxv1.AgentEvent{
						AgentId: agentID,
						Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: statusChange},
					},
				},
			})

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

		// Send the minimum screen bytes each verified terminal needs to
		// catch up to the current PTY state. The frontend's after_offset
		// tells us how far it has already processed; SnapshotSince
		// returns an incremental delta when the offset is inside the
		// retained ring, or a full-state snapshot (with is_snapshot=true)
		// when the subscriber has fallen behind or is cold-subscribing.
		// Caller is-caught-up returns (nil, _, false) and no event is sent.
		for i, termID := range verifiedTerminalIDs {
			afterOffset := afterOffsetByID[termID]
			data, endOffset, isSnapshot := svc.Terminals.ScreenSnapshotSince(termID, afterOffset)
			if len(data) > 0 {
				broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
						TerminalEvent: &leapmuxv1.TerminalEvent{
							TerminalId: termID,
							Event: &leapmuxv1.TerminalEvent_Data{
								Data: &leapmuxv1.TerminalData{
									Data:       data,
									IsSnapshot: isSnapshot,
									EndOffset:  endOffset,
								},
							},
						},
					},
				})
			}

			// Replay current startup status so a subscriber that joins
			// after READY / STARTUP_FAILED was broadcast still converges
			// (the prior pure-broadcast design lost events for any
			// watcher that attached after the one-shot fire). Use the
			// row from the access-control loop so a failure that predates
			// a worker restart still surfaces via the persisted
			// startup_error column.
			termRow := verifiedTerminalRows[i]
			status, startupError, startupMessage := svc.deriveTerminalStatus(&termRow)
			var termStatusChange *leapmuxv1.TerminalStatusChange
			switch status {
			case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING:
				termStatusChange = buildTerminalStartingStatus(termID, startupMessage, nil)
			case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
				termStatusChange = buildTerminalFailedStatus(termID, startupError)
			default:
				termStatusChange = buildTerminalReadyStatus(termID)
			}
			broadcastWatchEvent(sender, &leapmuxv1.WatchEventsResponse{
				Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
					TerminalEvent: &leapmuxv1.TerminalEvent{
						TerminalId: termID,
						Event:      &leapmuxv1.TerminalEvent_StatusChange{StatusChange: termStatusChange},
					},
				},
			})
		}

		// Stream stays open — events will be pushed via watcher.Sender.SendStream().
		// The handler returns immediately; cleanup happens when the channel closes.
	})
}

// deriveAgentStatus computes (status, startupError, startupMessage) for
// an agent, in priority order:
//  1. runtime Manager — if the agent is currently running, ACTIVE wins.
//  2. in-memory startup registry — STARTING / STARTUP_FAILED while a
//     startup is in flight or has just failed. The current phase
//     message is surfaced so a WatchEvents subscriber that arrived
//     after the initial STARTING broadcast still sees the right label.
//  3. persisted startup_error column — surfaces a prior failure across
//     worker restarts (the in-memory registry is wiped on restart).
//  4. INACTIVE otherwise.
func (svc *Context) deriveAgentStatus(a *db.Agent, isRunning bool) (status leapmuxv1.AgentStatus, startupError, startupMessage string) {
	if isRunning {
		return leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, "", ""
	}
	if sup, errStr, msg, ok := svc.AgentStartup.status(a.ID); ok {
		return sup, errStr, msg
	}
	if a.StartupError != "" {
		return leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, a.StartupError, ""
	}
	return leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, "", ""
}

// agentToProto converts a DB Agent to a proto AgentInfo. Status,
// startup_error, and startup_message are derived via deriveAgentStatus.
func (svc *Context) agentToProto(a *db.Agent, isRunning bool, gs *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentInfo {
	status, startupError, startupMessage := svc.deriveAgentStatus(a, isRunning)
	info := &leapmuxv1.AgentInfo{
		Id:             a.ID,
		WorkspaceId:    a.WorkspaceID,
		Title:          a.Title,
		Status:         status,
		WorkingDir:     a.WorkingDir,
		AgentSessionId: a.AgentSessionID,
		HomeDir:        a.HomeDir,
		WorkerId:       svc.WorkerID,
		CreatedAt:      timefmt.Format(a.CreatedAt),
		GitStatus:      gs,
		AgentProvider:  a.AgentProvider,
		OptionGroups:   svc.optionGroupsForAgent(a),
		StartupError:   startupError,
		StartupMessage: startupMessage,
	}

	if a.ClosedAt.Valid {
		info.ClosedAt = timefmt.Format(a.ClosedAt.Time)
	}

	return info
}

// runAgentStartup is the async body of OpenAgent: it executes the git-mode
// plan, spawns the subprocess, runs the initialize handshake, and reports
// success/failure via the per-status broadcastAgent{Starting,Failed,Active}
// helpers. Phases 0–2 run serially so the user sees a phased progress
// label ("Creating worktree…" → "Checking Git status…" → "Starting
// {provider}…") rather than overlapping noise.
func (svc *Context) runAgentStartup(ctx context.Context, dbAgent db.Agent, plan gitModePlan, agentOpts agent.Options) {
	defer svc.AgentStartup.finish()
	agentID := agentOpts.AgentID
	sink := svc.Output.NewSink(agentID, agentOpts.AgentProvider)

	// Phase 0: execute the git-mode mutation (worktree add, branch create,
	// checkout). Validation already ran synchronously in OpenAgent; what
	// runs here is only the potentially slow shell-outs.
	gm, gmErr := svc.runAgentPhase0(ctx, &dbAgent, plan)
	if gmErr != nil {
		svc.failAgentStartup(&dbAgent, gm, gmErr, nil)
		return
	}
	// Link the tab to its worktree now that we know the worktree id, unless a
	// CloseAgent already landed during startup (see
	// registerTabForWorktreeUnlessClosed for the strand-leak rationale). The
	// close-during-startup detection after startAgent rolls back a worktree
	// this startup created; skipping the link covers the pre-existing-worktree
	// case too.
	agentClosedDuringStartup := false
	if latest, fetchErr := svc.getAgentByID(bgCtx(), agentID); fetchErr == nil {
		agentClosedDuringStartup = latest.ClosedAt.Valid
	}
	svc.registerTabForWorktreeUnlessClosed(gm.WorktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID, agentClosedDuringStartup)
	if gm.WorkingDir != "" {
		agentOpts.WorkingDir = gm.WorkingDir
	}

	// Phase 1: compute gitStatus here rather than in the sync prologue —
	// the git shell-out would otherwise block the OpenAgent RPC. Record
	// each phase label in the registry *before* broadcasting so a
	// WatchEvents subscriber that attaches mid-phase reads the current
	// label via catch-up replay rather than seeing a generic fallback.
	phase1Msg := "Checking Git status…"
	svc.AgentStartup.setMessage(agentID, phase1Msg)
	svc.broadcastAgentStarting(&dbAgent, phase1Msg, nil)
	gitStatus := gitutil.GetGitStatus(ctx, agentOpts.WorkingDir)
	// initialOpts captures the launch-time settings. applyDBSettingsToAgentOptions
	// (called below) assigns a fresh Options map to agentOpts, so this
	// snapshot stays valid as long as no caller mutates agentOpts.Options
	// in place between here and the final settings handoff.
	initialOpts := agentOpts

	// OpenAgent returns before this goroutine starts the subprocess, so a
	// user can change settings while the agent is still in STARTING.
	// Re-read here so changes made during phase 0/1 affect startup itself.
	if latest, err := svc.getAgentByID(bgCtx(), agentID); err == nil {
		dbAgent = latest
		agentOpts = applyDBSettingsToAgentOptions(agentOpts, &dbAgent)
	} else {
		slog.Warn("agent startup: failed to refresh settings before start", "agent_id", agentID, "error", err)
	}

	// Phase 2: spawn the subprocess and run the init handshake.
	phase2Msg := agentStartupLabel("Starting", agentOpts.AgentProvider)
	svc.AgentStartup.setMessage(agentID, phase2Msg)
	svc.broadcastAgentStarting(&dbAgent, phase2Msg, gitStatus)
	agent.TraceStartupPhase(agentID, "before_start_agent")
	startedOpts := agentOpts
	confirmedSettings, startErr := svc.startAgent(ctx, agentOpts, sink)
	agent.TraceStartupPhase(agentID, "after_start_agent")

	// Re-read to detect whether CloseAgent landed during startup
	// (closed_at gets set) and to see the latest StartupError before
	// we potentially overwrite it.
	latest, fetchErr := svc.getAgentByID(bgCtx(), agentID)
	if fetchErr == nil {
		dbAgent = latest
	}
	if fetchErr == nil && dbAgent.ClosedAt.Valid {
		if startErr == nil {
			svc.Agents.StopAgent(agentID)
		}
		svc.AgentStartup.succeed(agentID)
		svc.rollbackGitMode(gm)
		return
	}

	if startErr != nil {
		slog.Error("failed to start agent", "agent_id", agentID, "error", startErr)
		svc.failAgentStartup(&dbAgent, gm, startErr, gitStatus)
		return
	}

	// Clear the startup registry entry *before* persistConfirmedAgentSettings
	// so that any SendAgentMessage racing against the early ACTIVE
	// broadcast (emitted from the output sink when the first init message
	// arrives inside startAgent) is not rejected by the SendAgentMessage
	// startup-gate. The subprocess is up and ready for input at this
	// point; settings persistence is a best-effort DB write.
	svc.AgentStartup.succeed(agentID)
	if dbAgent.StartupError != "" {
		svc.persistAgentStartupError(agentID, "")
	}

	unlockFinalSettingsHandoff := svc.Agents.LockAgent(agentID)
	// Released explicitly before any relaunch below (RestartAgent re-acquires
	// this same non-reentrant lock); the guard keeps the deferred release a
	// safety net for the panic path without double-unlocking.
	handoffUnlocked := false
	releaseHandoff := func() {
		if !handoffUnlocked {
			handoffUnlocked = true
			unlockFinalSettingsHandoff()
		}
	}
	defer releaseHandoff()

	// A settings update can also land while startAgent is blocked in the
	// provider handshake, before Manager.HasAgent can accept live updates.
	// Re-read at the final handoff, then use an atomic preserving UPDATE so
	// late control requests cannot be overwritten by confirmed startup
	// defaults while ACTIVE is being persisted.
	if latest, err := svc.getAgentByID(bgCtx(), agentID); err == nil {
		dbAgent = latest
	} else {
		slog.Warn("agent startup: failed to refresh settings before active persist", "agent_id", agentID, "error", err)
	}
	latestOpts, confirmedForPersist := resolveConfirmedStartupSettings(startedOpts, initialOpts, confirmedSettings, &dbAgent)

	activeDbAgent, err := svc.persistConfirmedAgentSettingsPreservingStartedSettings(agentID, dbAgent.Options, latestOpts, confirmedForPersist, dbAgent.OptionGroups)
	if err != nil {
		slog.Warn("failed to persist confirmed agent settings", "agent_id", agentID, "error", err)
		activeDbAgent = dbAgent
	}

	// Apply startup-time changes after the final DB handoff. For Claude Code
	// this means set_permission_mode is sent after the initialize/startup
	// sequence has fully settled, while the ACTIVE broadcast still carries
	// the preserved DB value. A change the provider can't apply live (e.g. a
	// model switch made during startup, which resets effort to auto and so
	// needs a relaunch) returns false; we relaunch below so the switch takes
	// effect rather than being silently dropped.
	relaunch := false
	if !maps.Equal(initialOpts.Options, latestOpts.Options) {
		if !svc.Agents.UpdateSettings(agentID, latestOpts.Options) {
			relaunch = true
		}
	}
	releaseHandoff()

	if relaunch {
		activeDbAgent = svc.relaunchForStartupSettingsChange(agentID, dbAgent.AgentProvider, latestOpts, activeDbAgent)
	}

	activeOptions := loadOptions(activeDbAgent.Options, activeDbAgent.AgentProvider)
	slog.Info("agent started",
		"agent_id", agentID,
		"model", activeOptions[agent.OptionIDModel],
		"permission_mode", activeOptions[agent.OptionIDPermissionMode])

	svc.broadcastAgentActive(&activeDbAgent, gitStatus)
	if latest, err := svc.getAgentByID(bgCtx(), agentID); err == nil && !maps.Equal(loadOptions(activeDbAgent.Options, activeDbAgent.AgentProvider), loadOptions(latest.Options, latest.AgentProvider)) {
		svc.broadcastSettingsStatusChange(latest)
	} else if err != nil {
		slog.Warn("agent startup: failed to reconcile settings after active broadcast", "agent_id", agentID, "error", err)
	}
}

// relaunchForStartupSettingsChange restarts the agent with opts after a settings
// change that landed during the startup window required a relaunch (the live
// update could not apply it -- e.g. a model switch resets effort to auto, which
// the CLI only honors on a fresh launch). Without this the change is written to
// the DB but never applied to the running process, leaving the agent on its
// launch settings. Returns the refreshed db row, or fallback when the relaunch
// or its persistence fails. Must be called with the per-agent lifecycle lock
// released: RestartAgent acquires it itself.
func (svc *Context) relaunchForStartupSettingsChange(agentID string, provider leapmuxv1.AgentProvider, opts agent.Options, fallback db.Agent) db.Agent {
	slog.Info("agent startup: relaunching to apply settings changed during startup",
		"agent_id", agentID, "model", opts.Model(), "effort", opts.Effort())
	sink := svc.Output.NewSink(agentID, provider)
	confirmed, err := svc.Agents.RestartAgent(bgCtx(), opts, sink)
	if err != nil {
		slog.Error("agent startup: failed to relaunch for startup-time settings change",
			"agent_id", agentID, "error", err)
		return fallback
	}
	// No orphan reconciliation on the startup path: persist the launch opts overlaid with the
	// confirmed values + provider defaults, keeping every launch axis (a complete-snapshot
	// reconcile is the apply paths' job, not startup's).
	active, err := svc.persistConfirmedStartupSettings(agentID, provider, opts.Options, confirmed)
	if err != nil {
		slog.Warn("agent startup: failed to persist confirmed settings after relaunch",
			"agent_id", agentID, "error", err)
		return fallback
	}
	return active
}

func applyDBSettingsToAgentOptions(opts agent.Options, dbAgent *db.Agent) agent.Options {
	o := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
	if o[agent.OptionIDPermissionMode] == "" {
		o[agent.OptionIDPermissionMode] = agent.PermissionModeOrDefault(dbAgent.AgentProvider, "")
	}
	opts.Options = o
	return opts
}

// confirmedSettingsPreservingStartupChanges drops, from the confirmed option
// map, any option the user changed while startup was finishing (initial !=
// latest), so the confirmed-settings persist can't overwrite a late edit with a
// startup-time default. Returns nil for a nil confirmed map.
func confirmedSettingsPreservingStartupChanges(confirmed OptionMap, initial, latest agent.Options) OptionMap {
	if confirmed == nil {
		return nil
	}
	out := confirmed.Clone()
	initialOpts, latestOpts := initial.Options, latest.Options
	for _, key := range sortedOptionKeys(initialOpts, latestOpts) {
		if initialOpts[key] != latestOpts[key] {
			delete(out, key)
		}
	}
	return out
}

// resolveConfirmedStartupSettings derives the two values the startup handoff persists from the
// (already-refreshed) DB row: latestOpts -- the launch options overlaid with the row's stored
// settings (so a setting changed mid-startup is carried) -- and confirmedForPersist -- the
// provider's confirmed blob with any such mid-startup edit dropped so it can't be overwritten
// by a startup-time default. Pure: it reads dbAgent but performs no I/O, so the caller owns the
// re-read that refreshes dbAgent first.
func resolveConfirmedStartupSettings(startedOpts, initialOpts agent.Options, confirmedSettings map[string]string, dbAgent *db.Agent) (latestOpts agent.Options, confirmedForPersist OptionMap) {
	latestOpts = applyDBSettingsToAgentOptions(startedOpts, dbAgent)
	confirmedForPersist = confirmedSettingsPreservingStartupChanges(confirmedSettings, initialOpts, latestOpts)
	return latestOpts, confirmedForPersist
}

// baseAgentStatusChange fills the fields that are identical across every
// AgentStatusChange broadcast regardless of status. Per-status builders
// layer status-specific fields (startupMessage, startupError, available
// catalogs) on top.
// baseAgentStatusChange omits OptionGroups: a STARTING/FAILED/INACTIVE broadcast
// must not overwrite the frontend's last-known catalog (empty = don't update).
// The ACTIVE and settings-refresh paths attach the catalog explicitly.
func baseAgentStatusChange(dbAgent *db.Agent, status leapmuxv1.AgentStatus, gitStatus *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentStatusChange {
	return &leapmuxv1.AgentStatusChange{
		AgentId:        dbAgent.ID,
		Status:         status,
		AgentSessionId: dbAgent.AgentSessionID,
		WorkerOnline:   true,
		GitStatus:      gitStatus,
		AgentProvider:  dbAgent.AgentProvider,
	}
}

// buildAgentStartingStatus builds a STARTING AgentStatusChange carrying
// the current phase label. gitStatus is populated once phase 1 has
// finished computing it; earlier phases pass nil.
func buildAgentStartingStatus(dbAgent *db.Agent, message string, gitStatus *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentStatusChange {
	sc := baseAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, gitStatus)
	sc.StartupMessage = message
	return sc
}

// buildAgentFailedStatus builds a STARTUP_FAILED AgentStatusChange. The
// gitStatus is attached when phase 1 completed before the failure so the
// frontend can show branch info alongside the error.
func buildAgentFailedStatus(dbAgent *db.Agent, errMsg string, gitStatus *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentStatusChange {
	sc := baseAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, gitStatus)
	sc.StartupError = errMsg
	return sc
}

// buildAgentActiveStatus builds an ACTIVE AgentStatusChange including the
// provider-reported model / option-group catalogs. The catalogs are
// deliberately only attached on ACTIVE so a STARTING or FAILED broadcast
// does not overwrite the frontend's last-known catalog with an empty
// slice.
func (svc *Context) buildAgentActiveStatus(dbAgent *db.Agent, gitStatus *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentStatusChange {
	sc := baseAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, gitStatus)
	sc.OptionGroups = svc.optionGroupsForAgent(dbAgent)
	return sc
}

// buildAgentInactiveStatus builds an INACTIVE AgentStatusChange. Used by
// WatchEvents replay (when the agent is neither running nor starting up
// and has no persisted startup_error, where deriveAgentStatus would
// otherwise return STARTUP_FAILED) and by broadcastAgentInactive to
// revert a transient STARTING after an auto-start failure.
func buildAgentInactiveStatus(dbAgent *db.Agent, gitStatus *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentStatusChange {
	return baseAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, gitStatus)
}

// agentStartupLabel renders the user-visible "<verb> <provider>…" phase
// label shown beneath the chat startup banner during cold-start and
// restart of an agent subprocess. Verb is typically "Starting" or
// "Restarting".
func agentStartupLabel(verb string, provider leapmuxv1.AgentProvider) string {
	return verb + " " + agentlabels.DisplayName(provider) + "…"
}

// broadcastStatusChange fans out a single AgentStatusChange to all subscribers,
// wrapping it in the AgentEvent envelope. The lifecycle/settings broadcasters below
// share this so the envelope construction lives in one place.
func (svc *Context) broadcastStatusChange(agentID string, sc *leapmuxv1.AgentStatusChange) {
	svc.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// broadcastAgentStarting fans out a STARTING AgentStatusChange to all
// subscribers. Used by the OpenAgent startup goroutine for each phase
// label transition.
func (svc *Context) broadcastAgentStarting(dbAgent *db.Agent, message string, gitStatus *leapmuxv1.AgentGitStatus) {
	svc.broadcastStatusChange(dbAgent.ID, buildAgentStartingStatus(dbAgent, message, gitStatus))
}

// broadcastAgentFailed fans out a STARTUP_FAILED AgentStatusChange.
func (svc *Context) broadcastAgentFailed(dbAgent *db.Agent, errMsg string, gitStatus *leapmuxv1.AgentGitStatus) {
	svc.broadcastStatusChange(dbAgent.ID, buildAgentFailedStatus(dbAgent, errMsg, gitStatus))
}

// broadcastAgentActive fans out an ACTIVE AgentStatusChange.
func (svc *Context) broadcastAgentActive(dbAgent *db.Agent, gitStatus *leapmuxv1.AgentGitStatus) {
	svc.broadcastStatusChange(dbAgent.ID, svc.buildAgentActiveStatus(dbAgent, gitStatus))
}

// broadcastAgentInactive fans out an INACTIVE AgentStatusChange. Used to
// clear a transient STARTING spinner when an auto-start attempt
// (ensureAgentRunning) fails — the failure surfaces to the user as a
// per-message delivery_error rather than a permanent STARTUP_FAILED, so
// the agent stays retryable on the next send.
func (svc *Context) broadcastAgentInactive(dbAgent *db.Agent) {
	svc.broadcastStatusChange(dbAgent.ID, buildAgentInactiveStatus(dbAgent, nil))
}

// runAgentPhase0 broadcasts the per-mode label and executes the git-mode
// mutation. Returns the result (with rollback metadata populated iff a
// mutation partially succeeded before failing) and any error.
func (svc *Context) runAgentPhase0(ctx context.Context, dbAgent *db.Agent, plan gitModePlan) (gitModeResult, error) {
	return svc.runStartupPhase0(ctx, plan, svc.agentStartupCallbacks(dbAgent, nil))
}

// failAgentStartup is the common tail for every failure after the sync
// prologue: rolls back any partial git-mode mutation, persists the
// error, broadcasts STARTUP_FAILED, and marks the registry failed. The
// shared `failStartup` enforces the ordering (DB before broadcast
// before registry) so observers see a durable terminal state.
func (svc *Context) failAgentStartup(dbAgent *db.Agent, gm gitModeResult, cause error, gitStatus *leapmuxv1.AgentGitStatus) {
	svc.failStartup(gm, cause, svc.agentStartupCallbacks(dbAgent, gitStatus))
}

// persistAgentStartupError writes (or clears when errMsg is "") the
// agents.startup_error column so the startup panel survives a worker
// restart that wipes the in-memory registry.
func (svc *Context) persistAgentStartupError(agentID, errMsg string) {
	if err := svc.Queries.SetAgentStartupError(bgCtx(), db.SetAgentStartupErrorParams{
		StartupError: errMsg,
		ID:           agentID,
	}); err != nil {
		action := "persist"
		if errMsg == "" {
			action = "clear"
		}
		slog.Warn("failed to "+action+" agent startup error", "agent_id", agentID, "error", err)
	}
}

// confirmedOptions overlays the values the provider actually confirmed
// (CurrentOptions, captured after a live update or restart) onto the requested
// base options, then fills provider defaults. A nil/empty confirmed map (offline
// edit or failed restart) yields the base unchanged.
func confirmedOptions(provider leapmuxv1.AgentProvider, base, confirmed OptionMap) OptionMap {
	final := base.Clone()
	for k, v := range confirmed {
		if v != "" {
			final[k] = v
		}
	}
	return resolveProviderDefaults(final, provider)
}

// surfacedOptions is a COMPLETE snapshot of every axis a running session currently surfaces (its
// post-start CurrentOptions), as opposed to a sparse confirmation blob. reconcileOrphanedOptions /
// settleConfirmedOptions consume it to decide which persisted axes the session no longer surfaces;
// the named type makes the "must be complete" precondition a compile-time barrier (a caller has to
// write an explicit surfacedOptions(...) conversion, asserting completeness) rather than a comment a
// sparse-blob caller can silently ignore.
type surfacedOptions OptionMap

// reconcileOrphanedOptions drops from opts any axis the running agent's COMPLETE confirmed
// snapshot (surfaced) no longer carries -- e.g. an ACP effort/reasoning_effort the relaunched
// model dropped, or an option applyStartupOptions re-pushed and the server rejected.
// Leaving such a value persisted is the [E12] three-way disagreement (the row advertises a
// value the session isn't running). Two kinds are kept: the model axis (always live), and the
// provider's persisted-only options (Pi's pi_provider), which are persisted by design but never
// surfaced -- so their absence from `surfaced` is expected, not orphaning.
//
// `surfaced` is the surfacedOptions type precisely because it MUST be a complete snapshot of every
// axis the agent currently surfaces, NOT a sparse confirmation blob -- otherwise a legitimately-
// present-but-unconfirmed axis would be wrongly dropped. Callers without such a snapshot (the
// sparse-confirm startup-preserve path) cannot reach this without an explicit conversion.
func reconcileOrphanedOptions(provider leapmuxv1.AgentProvider, opts OptionMap, surfaced surfacedOptions) OptionMap {
	persistedOnly := agent.PersistedOnlyOptionIDs(provider)
	out := opts.Clone()
	for k := range out {
		if k == agent.OptionIDModel || persistedOnly[k] {
			continue
		}
		if surfaced[k] == "" {
			delete(out, k)
		}
	}
	return out
}

// settleConfirmedOptions overlays the provider-confirmed values onto requested, fills provider
// defaults, THEN drops any axis the running session does not surface -- reconcile runs LAST so a
// provider default resolveProviderDefaults fills for an axis the session dropped is removed rather
// than resurrected. confirmedOptions alone would re-stamp such a default (e.g. a future provider
// whose option default the session can drop), defeating reconcileOrphanedOptions when it ran
// first. `confirmed` MUST be the running session's COMPLETE CurrentOptions snapshot (see
// reconcileOrphanedOptions) -- the live-apply and restart paths both capture it that way.
func settleConfirmedOptions(provider leapmuxv1.AgentProvider, requested OptionMap, confirmed surfacedOptions) OptionMap {
	return reconcileOrphanedOptions(provider, confirmedOptions(provider, requested, OptionMap(confirmed)), confirmed)
}

// reportModelChange decides whether the settings_changed notification should carry
// a model change: true when the prior stored model (oldModel) and the model the
// session settled on (settledModel) differ after normalizing into the provider's
// alias space, so a model that merely re-spelled (e.g. stored "claude-opus-4-8" vs
// settled "opus" on an effort-only edit) is NOT reported as a change.
//
// The account-default sentinel resolving to its concrete model ("default" ->
// "sonnet") IS reported: it is a real, user-visible transition that the settings
// panel (built from the same settled model in AgentInfo) shows too, so chat and
// panel agree. This deliberately also announces the resolution on a new tab's first
// edit -- there is no signal distinguishing that from a session that was stuck on an
// unresolved "default" and only now resolved (the stuck resolution is itself a first
// resolution), and announcing the concrete model is informative rather than noise. A
// sentinel that has NOT resolved stays "default" on both sides and so compares equal
// -- no spurious change.
func reportModelChange(provider leapmuxv1.AgentProvider, oldModel, settledModel string) bool {
	return agent.NormalizeModelID(provider, oldModel) != agent.NormalizeModelID(provider, settledModel)
}

// optionChangeEntry is the settings_changed payload for one changed option group: the value
// ids (old/new) and their human-readable labels, plus the group's own label. It marshals to
// the {old,new,oldLabel,newLabel,label} JSON shape the chat-view notification renderer reads
// (see frontend notificationRenderers). Using a typed struct rather than a bare
// map[string]string makes a misspelled key a compile error here instead of a silently-absent
// field in the UI, and documents the wire shape in one place that every emitter shares.
type optionChangeEntry struct {
	Old        string `json:"old"`
	New        string `json:"new"`
	OldLabel   string `json:"oldLabel"`
	NewLabel   string `json:"newLabel"`
	GroupLabel string `json:"label"`
}

// optionGroupChangeEntry builds the settings_changed entry a notification carries for one
// changed option group. valueLabel resolves the option value labels; groupLabel is the
// human-readable group name (e.g. "Output Style").
func optionGroupChangeEntry(oldID, newID string, valueLabel func(string) string, groupLabel string) optionChangeEntry {
	return optionChangeEntry{
		Old:        oldID,
		New:        newID,
		OldLabel:   valueLabel(oldID),
		NewLabel:   valueLabel(newID),
		GroupLabel: groupLabel,
	}
}

// acceptExposedOptions filters `incoming` to the axes the provider actually exposes, so a key it
// can't apply never persists a phantom AND emits a misleading settings_changed notification for a
// change the agent's UpdateSettings silently ignores. The CLI can send an arbitrary `agent set
// --set key=value` (or a `--permission-mode` against a primary-agent provider, or `--effort`
// against Cursor, which bakes effort into the model id) -- all foreign axes that must be dropped
// rather than stored.
//
// An axis is valid when it is one of the provider's KNOWN ids (the static allowlist: model, the
// secondary permission-mode/primary-agent axis, effort where the provider has one, and the
// provider's declared options -- Codex's sandbox/network/..., Pi's pi_provider, the ACP server
// config options) OR is present in `catalog` (a server-driven config option the running agent has
// actually surfaced, accepted even before it is added to the static allowlist). The two together
// make the catalog the authoritative key set; the frontend only ever sends catalog-exposed groups,
// so only CLI mis-targets are stripped. catalog is built by the caller for the model the edit
// settles on, so an option the NEW model exposes isn't rejected on the same edit that selects it.
// Filtering into a fresh map leaves the caller's request (the decoded proto message) untouched.
func (svc *Context) acceptExposedOptions(agentID string, provider leapmuxv1.AgentProvider, incoming OptionMap, catalog []*leapmuxv1.AvailableOptionGroup) OptionMap {
	known := agent.KnownOptionIDs(provider)
	accepted := make(OptionMap, len(incoming))
	for axis, value := range incoming {
		// An empty value on the edit path is NOT a clear. Every option is a select whose
		// values are non-empty, the frontend only ever sends a concrete catalog selection, and
		// mergeOptions below treats an empty value as a DELETE -- so a stray CLI
		// `agent set --option key=` (no value) would otherwise destructively wipe the
		// persisted option rather than being the no-op the user intended. Skip it. Provider-
		// driven option clears flow through the separate refresh path (acpRefreshMap), not here.
		if value == "" {
			slog.Warn("ignoring empty option value on settings edit (not a clear)",
				"agent_id", agentID, "option_id", axis, "provider", provider.String())
			continue
		}
		if known[axis] || optionids.GroupByID(catalog, axis) != nil {
			accepted[axis] = value
			continue
		}
		slog.Warn("ignoring option not exposed by the provider",
			"agent_id", agentID, "option_id", axis, "provider", provider.String())
	}
	return accepted
}

// resetEffortToAutoIfUnsupported resets newOptions' effort to EffortAuto, in place, when it
// wouldn't be valid for the model the edit settles on -- for a provider that owns a model-dependent
// effort catalog (Claude/Codex/Pi):
//   - on a model switch, also when the client sent NO effort (explicitEffort == "") -- so the new
//     model picks its own default rather than silently inheriting the previous model's tier;
//   - whether or not the model switched, when the effort that WOULD persist -- whether explicitly
//     sent (CLI `--effort xhigh`) OR inherited from the stored row -- is not a tier the settled
//     model offers. Validating the MERGED value (not just the sent one) also catches a stale stored
//     effort the unchanged model no longer offers because the live catalog narrowed mid-session
//     (e.g. an entitlement was revoked); leaving it would persist an unsupported tier and surface a
//     misleading effort in the settings_changed notification until a relaunch clamps it.
//
// EffortAuto is offered by every model, so resetting to it is always valid. An ACP provider's
// effort is a server-driven axis independent of the model (or it has none, like Cursor), so this is
// a no-op for them. Compares NORMALIZED model ids, not the raw strings: a model merely re-spelled
// into the same normalized id (a CLI alias, or the account-default sentinel resolving to its
// concrete id) is not a real switch and must not reset the user's effort -- mirroring
// reportModelChange's normalized comparison used for the settings_changed notification.
func resetEffortToAutoIfUnsupported(provider leapmuxv1.AgentProvider, newOptions OptionMap, catalog []*leapmuxv1.AvailableOptionGroup, oldModel, newModel, explicitEffort string) {
	if !agent.ProviderManagesEffort(provider) {
		return
	}
	switched := agent.NormalizeModelID(provider, newOptions[agent.OptionIDModel]) != agent.NormalizeModelID(provider, oldModel)
	merged := newOptions[agent.OptionIDEffort]
	// The merged-effort reset (second clause) fires only when the settled model is one the catalog
	// actually describes (ModelEffortKnown): an effort can only be judged unsupported against a model
	// whose effort set is known. A model ABSENT from the catalog -- e.g. a tier valid in the running
	// provider's live catalog but missing from a stopped agent's static seed -- is left for the
	// running session to validate, so a CLI `agent set --model <new> --effort xhigh` on a stopped
	// agent doesn't silently clobber a valid effort to auto. Mirrors ValidateLaunchOptions's
	// deliberate non-validation of model/effort against the seed.
	if (switched && explicitEffort == "") ||
		(merged != "" && agent.ModelEffortKnown(catalog, provider, newModel) && !agent.EffortSupportedByModel(catalog, provider, newModel, merged)) {
		newOptions[agent.OptionIDEffort] = agent.EffortAuto
	}
}

// sanitizeIncomingOptions turns a raw UpdateAgentSettings options map into the full,
// validated option set to persist and apply: it drops axes the provider can't apply,
// merges the rest over the agent's current options, resets effort on a model switch for
// catalog-effort providers, and fills the provider's permission-mode and other defaults.
func (svc *Context) sanitizeIncomingOptions(agentID string, provider leapmuxv1.AgentProvider, oldOptions, incoming OptionMap) OptionMap {
	oldModel := oldOptions[agent.OptionIDModel]
	// The model the edit settles on drives both the catalog the axis filter validates against and
	// the model the effort reset checks: for a running agent OptionGroups ignores the model arg
	// (it returns the live catalog), so this only changes the offline/static-fallback path --
	// exactly where a CLI `agent set --model X --set someOption=...` targets a stopped agent.
	newModel := oldModel
	if m, ok := incoming[agent.OptionIDModel]; ok && m != "" {
		newModel = m
	}
	catalog := svc.Agents.OptionGroups(agentID, provider, newModel)

	accepted := svc.acceptExposedOptions(agentID, provider, incoming, catalog)
	newOptions := mergeOptions(oldOptions, accepted)
	resetEffortToAutoIfUnsupported(provider, newOptions, catalog, oldModel, newModel, accepted[agent.OptionIDEffort])

	// Stamp the provider's default permission mode only when it actually has one.
	// Providers with no permission-mode axis (OpenCode/Kilo primary-agent, Reasonix
	// model-only) return "" here, and writing an empty key would surface it in the RPC
	// reply's ConfirmedOptions even though marshalOptions drops it from the row.
	if newOptions[agent.OptionIDPermissionMode] == "" {
		if def := agent.PermissionModeOrDefault(provider, ""); def != "" {
			newOptions[agent.OptionIDPermissionMode] = def
		}
	}
	return resolveProviderDefaults(newOptions, provider)
}

// optionsChangeDelta returns the minimal set of axes the edit changes: every key whose
// value differs between `from` and `to`, with the NEW value (an empty value for a key
// `to` drops, which the wire merge then deletes). Feeding this delta -- rather than the
// full `to` map -- to casPersistAgentOptions lets a settings edit and a concurrent
// server-initiated refresh converge: the edit writes only what it touched, so a key the
// refresh added (and the edit's stale snapshot lacks) is preserved instead of clobbered.
func optionsChangeDelta(from, to OptionMap) OptionMap {
	delta := OptionMap{}
	for k, v := range to {
		if from[k] != v {
			delta[k] = v
		}
	}
	for k := range from {
		if _, ok := to[k]; !ok {
			delta[k] = ""
		}
	}
	return delta
}

// pushAndReadConfirmed applies `applied` to the running agent and reads back its COMPLETE
// confirmed option snapshot, holding the per-agent lifecycle lock across BOTH so a concurrent
// UpdateAgentSettings for the same agent can't land between them and make the caller read ITS
// confirmation instead. It returns:
//   - confirmed: the running session's CurrentOptions, or nil when the process exited between the
//     in-memory accept and the readback -- the exit-cleanup goroutine deletes the manager entry
//     under the manager mutex, NOT this lifecycle lock, so CurrentOptions returns nil. A running
//     agent always returns a non-nil map (empty at most), so nil unambiguously means "gone"; a
//     caller must NOT treat it as confirmation (the dead session settled nothing).
//   - appliedLive: what UpdateSettings reported -- false means the provider can't apply this change
//     live (e.g. Claude effort->auto) and the caller should relaunch.
//
// Shared by applySettingsLive (which gates on appliedLive and relaunches) and applyOptionChanges
// (which only overlays when confirmed != nil), so the hold-lock-across-push-and-readback contract
// lives in one place.
func (svc *Context) pushAndReadConfirmed(agentID string, applied OptionMap) (confirmed OptionMap, appliedLive bool) {
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()
	appliedLive = svc.Agents.UpdateSettings(agentID, applied)
	return svc.Agents.CurrentOptions(agentID), appliedLive
}

// applySettingsLive attempts to apply newOptions to a running agent without a restart.
// Providers apply what they can without a restart (Codex applies to the next turn; Claude
// Code applies model/effort/permission changes via apply_flag_settings) and return true;
// they return false only for changes they can't apply live -- e.g. Claude Code switching
// effort back to auto, which needs a relaunch without --effort -- and the caller then
// restarts. Returns the settled options (the request overlaid with what the provider
// confirmed) and whether the change was applied live.
func (svc *Context) applySettingsLive(dbAgent db.Agent, newOptions OptionMap) (OptionMap, bool) {
	agentID, provider := dbAgent.ID, dbAgent.AgentProvider
	confirmed, appliedLive := svc.pushAndReadConfirmed(agentID, newOptions)
	// Not applied live (provider needs a relaunch), or the process exited mid-apply (nil
	// snapshot -- see pushAndReadConfirmed): either way, overlaying nothing would persist/broadcast
	// the un-clamped optimistic REQUEST as if the session had confirmed it, so report not-applied
	// and let the caller restart and confirm against a fresh session instead.
	if !appliedLive || confirmed == nil {
		return newOptions, false
	}
	// `confirmed` is the running session's COMPLETE CurrentOptions snapshot, so settle the
	// request against it -- overlay the confirmed values + provider defaults, THEN reconcile away
	// any axis it no longer surfaces (the same [E12] guard the restart path applies). Without the
	// reconcile, an ACP option the server accepted the write for but then dropped from its
	// configOptions (it no longer applies) would stay persisted/broadcast as a value the live
	// session isn't running. settleConfirmedOptions runs the reconcile LAST so a provider default
	// the overlay re-fills for a now-unsurfaced axis is dropped rather than resurrected; it keeps
	// the always-live model axis and the provider's persisted-only axes.
	settledOptions := settleConfirmedOptions(provider, newOptions, surfacedOptions(confirmed))

	// Persist EVERY axis the provider confirmed, not just model/effort: the optimistic
	// write stored the REQUESTED values, so a clamp the provider applied to any axis (a
	// re-spelled model, an effort downgrade, an ACP reasoning_effort the server lowered, a
	// Codex sandbox/service_tier it adjusted) would otherwise be lost from the DB row --
	// and the AgentInfo broadcast built from it -- even though the running agent applied it.
	// Diff against newOptions (what the optimistic write left on the row), NOT oldOptions, so
	// the delta also DELETES an axis the reconcile dropped that this very edit added -- a base
	// of oldOptions would miss it (the orphan isn't in oldOptions). Persist via compare-and-swap
	// on only those axes: two concurrent UpdateAgentSettings each merge their own delta, and a
	// server-initiated PersistSettingsRefresh holding no lifecycle lock can't be clobbered --
	// the CAS converges rather than letting a stale full-map write win.
	if delta := optionsChangeDelta(newOptions, settledOptions); len(delta) > 0 {
		if _, _, err := casPersistAgentOptions(bgCtx(), svc.Queries, agentID, dbAgent.Options, delta); err != nil {
			slog.Warn("failed to persist settled options after live update", "agent_id", agentID, "error", err)
		}
	}
	return settledOptions, true
}

// applySettingsViaRestart stops and restarts the agent on newOptions (for a change the
// provider couldn't apply live), persists the confirmed settings, and returns the settled
// options -- the request overlaid with what the relaunched session confirmed, or the
// unchanged request when the restart fails.
func (svc *Context) applySettingsViaRestart(dbAgent db.Agent, newOptions OptionMap) OptionMap {
	agentID, provider := dbAgent.ID, dbAgent.AgentProvider
	resumeSessionID := svc.resolveResumeSessionID(agentID, dbAgent.AgentSessionID, dbAgent.Resumed)

	agentOpts := agent.Options{
		AgentID:         agentID,
		WorkingDir:      dbAgent.WorkingDir,
		ResumeSessionID: resumeSessionID,
		Options:         newOptions,
		StartupTimeout:  svc.agentStartupTimeout(),
		APITimeout:      svc.agentAPITimeout(),
		Shell:           svc.agentShell(),
		LoginShell:      svc.agentLoginShell(),
		HomeDir:         svc.HomeDir,
		AgentProvider:   provider,
	}

	sink := svc.Output.NewSink(agentID, provider)

	confirmedOpts, err := svc.Agents.RestartAgent(bgCtx(), agentOpts, sink)
	if err != nil {
		slog.Error("failed to restart agent with new settings", "agent_id", agentID, "error", err)
		// Clear stale session ID so ensureAgentRunning won't try to resume a
		// non-existent session on the next message.
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: "",
			ID:             agentID,
		})
		svc.Output.PersistLeapMuxNotification(agentID, provider, map[string]interface{}{
			"type":  agent.NotificationTypeAgentError,
			"error": "Failed to restart agent with new settings: " + err.Error(),
		})
		return newOptions
	}
	// confirmedOpts is the relaunched agent's COMPLETE surfaced snapshot (CurrentOptions), so
	// settle the request against it -- overlay the confirmed values + provider defaults, THEN
	// reconcile away any axis the new session no longer surfaces (an option the new model dropped,
	// or one applyStartupOptions re-pushed and the server rejected), instead of leaving the row
	// advertising a value the session isn't running ([E12]). The reconcile runs LAST so a provider
	// default the overlay re-fills for a now-unsurfaced axis is dropped rather than resurrected.
	settled := settleConfirmedOptions(provider, newOptions, surfacedOptions(confirmedOpts))
	// Pass the pre-settle newOptions as `stored` (what the optimistic write left on the row,
	// carrying the orphaned axes) so the persisted delta DELETES the axes the relaunched session
	// no longer surfaces, while `settled` is the option set we want to keep.
	if _, err := svc.persistConfirmedAgentSettings(agentID, provider, newOptions, settled); err != nil {
		slog.Warn("failed to persist confirmed settings after restart", "agent_id", agentID, "error", err)
	}
	slog.Info("agent restarted with new settings",
		"agent_id", agentID, "model", settled[agent.OptionIDModel], "effort", settled[agent.OptionIDEffort])
	return settled
}

// buildSettingsChanges assembles the settings_changed "changes" map for the chat view: one
// {old,new,oldLabel,newLabel,label} entry per axis in `keys` whose value actually changed between
// the prior options (oldOptions) and the settled values (newOptions). Display labels are resolved
// here against the agent's option-group catalog so the frontend needs no label maps of its own.
// When notifyFirstSet is false, an axis whose prior value was empty (a first set) is skipped.
// Returns an empty map when nothing reports.
//
// Shared by the model-settle path (full union of keys, first sets announced) and the live
// option-change path (only the applied keys, first sets gated by the caller's spec), so the
// catalog-resolution rules below -- the SETTLED-model catalog and the row-catalog fallback -- are
// applied once and can't drift between the two notification emitters.
func (svc *Context) buildSettingsChanges(
	dbAgent *db.Agent,
	oldOptions, newOptions OptionMap,
	keys []string,
	notifyFirstSet bool,
) map[string]interface{} {
	agentID, provider := dbAgent.ID, dbAgent.AgentProvider
	// Build the catalog for the SETTLED model, not the provider default: an offline edit
	// has no running agent, so OptionGroups falls back to the static catalog -- built with
	// an empty model arg it enumerates only the provider-default model's effort tiers and
	// leaks the raw id of an effort the new model introduces. Passing the new model rebuilds
	// the effort group for it (matching persistConfirmedAgentSettings / optionGroupsView).
	liveGroups := svc.Agents.OptionGroups(agentID, provider, newOptions[agent.OptionIDModel])
	// A model switch relaunches the agent onto a different model whose rebuilt
	// catalog may no longer list a value we need to name: the model the session
	// switched away from (Claude Code hides standard-context Opus behind "default",
	// so the resolved "opus[1m]" is listed only while it is the active model), or an
	// effort tier the new model doesn't offer (e.g. "xhigh" after Opus->Sonnet).
	// Resolving such a value against the live catalog alone leaks its raw bracketed
	// id; fall back to the catalog persisted on the agent row, captured while those
	// pre-change selections were still current.
	prevGroups := parseOptionGroups(dbAgent.OptionGroups)
	changes := map[string]interface{}{}
	for _, key := range keys {
		oldVal, newVal := oldOptions[key], newOptions[key]
		if oldVal == newVal {
			continue
		}
		// The model axis has a special "report" rule: a value that merely
		// re-spelled into the same normalized model isn't a user-visible change,
		// while the account-default sentinel resolving to a concrete model is.
		if key == agent.OptionIDModel && !reportModelChange(provider, oldVal, newVal) {
			continue
		}
		// An axis whose settled value is empty is no longer in effect: a model switch dropped it
		// (reconcileOrphanedOptions removes an axis the relaunched session no longer surfaces, e.g.
		// effort after switching to a model without an effort axis), or it was cleared. There is no
		// new value to name, so emitting "Label (old -> )" would render a dangling arrow with a
		// blank target. Omit it -- the axis simply disappears from the picker. (oldVal == newVal ==
		// "" already returned above, so this fires only for a real removal, oldVal != "".)
		if newVal == "" {
			continue
		}
		if oldVal == "" {
			// A first set whose value is just the axis's own DEFAULT being materialized is not a
			// user-visible change -- skip it on either path. This bites permissionMode in
			// particular: resolveProviderDefaults does NOT stamp it into oldOptions (only
			// sanitizeIncomingOptions does, into the settled map), so the first settings edit on a
			// fresh agent reads as ""->default and would otherwise announce a spurious
			// "Permission Mode (default)" the user never chose. Compared against the SETTLED-model
			// catalog's DefaultValue (the same liveGroups used for labels); a first set to a
			// NON-default value is a real user choice, still announced when the caller opts in.
			if def := optionids.GroupByID(liveGroups, key).GetDefaultValue(); def != "" && newVal == def {
				continue
			}
			// A first set (no prior value) is otherwise noise on the live option-change path (an
			// axis the agent settles for the first time), so it is announced only when asked.
			if !notifyFirstSet {
				continue
			}
		}
		changes[key] = optionGroupChangeEntry(
			oldVal, newVal,
			func(v string) string { return resolveOptionValueLabel(liveGroups, prevGroups, key, v) },
			// Reuse the already-fetched liveGroups rather than re-resolving the catalog
			// per key (svc.optionGroupLabel would rebuild it every iteration).
			optionGroupLabelInGroups(liveGroups, key),
		)
	}
	return changes
}

// persistConfirmedAgentSettings persists the confirmed option values and the
// provider-reported option-group catalog after a (re)start, returning the refreshed row
// for the ACTIVE broadcast.
//
//   - stored: the option map currently on the row that this confirmation revises -- the options
//     the agent was (re)launched with. Used as the CAS base; the delta against `final` is what
//     this confirmation changes (a provider clamp, or an "" delete for an axis the relaunch dropped).
//   - final:  the option set to settle the row on. The CALLER builds it -- confirmedOptions for the
//     startup paths (which keep launch axes the session may not surface), settleConfirmedOptions for
//     the live/restart apply paths (which reconcile a now-unsurfaced axis away). Recomputing it here
//     would force ONE confirm-vs-settle policy on both kinds of caller; lifting it out keeps that
//     policy with the caller that knows whether it holds a complete CurrentOptions snapshot.
//
// The options column is written via compare-and-swap on only the DELTA between `stored`
// (with provider defaults, i.e. the row as written at launch) and the settled `final` -- the
// provider's clamps plus any axis `final` dropped relative to `stored` (an "" delete).
// CAS-merging the delta, rather than the blind full-map write this used before, means a
// server-initiated PersistSettingsRefresh landing in the post-relaunch window -- the
// relaunched reader goroutine holds no lifecycle lock -- can't be clobbered: its key isn't in
// our delta, so the merge preserves it. The catalog is recomputed wholesale on every
// confirmation, and written via a compare-and-swap against the catalog on the row when this
// began (read into `prior` below): a richer catalog a running ACP provider discovered
// concurrently -- persisted via SetAgentOptionGroups on the unsynchronized reader path -- is
// kept rather than clobbered by this (possibly narrower) handoff catalog, the synchronous
// mirror of the async variant's expected_option_groups guard. The row is then re-read once for
// the broadcast (the writes have no single RETURNING row to hand back).
func (svc *Context) persistConfirmedAgentSettings(agentID string, provider leapmuxv1.AgentProvider, stored, final OptionMap) (db.Agent, error) {
	base := resolveProviderDefaults(stored, provider)
	// Snapshot the catalog on the row BEFORE the write so the option_groups CAS can tell it apart
	// from a concurrently-discovered one (a richer catalog a running provider persisted in between).
	prior, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return db.Agent{}, err
	}
	// Persist the options DELTA and the catalog in ONE atomic statement so a concurrent
	// PersistSettingsRefresh can't land between two separate writes and strand the row with this
	// handoff's options beside a foreign catalog (the two move together-or-neither). The catalog is
	// CAS-guarded against prior.OptionGroups -- the snapshot on the row when this handoff read it.
	expectedCatalog, catalog := svc.confirmedCatalogOrSkip(agentID, provider, final, prior.OptionGroups, "confirmed-settings")
	return casPersistConfirmedSettings(bgCtx(), svc.Queries, agentID, marshalOptions(base), optionsChangeDelta(base, final), expectedCatalog, catalog)
}

// persistConfirmedStartupSettings is the startup-path spelling of persistConfirmedAgentSettings:
// it settles the row on the launch options overlaid with the provider-confirmed values + defaults
// (confirmedOptions), keeping every launch axis the session may not surface. The "base == launch
// options" policy lives here in ONE place so the several startup-confirmation sites can't re-spell
// it and drift -- they pass the launch option map and the confirmed snapshot and nothing else.
func (svc *Context) persistConfirmedStartupSettings(agentID string, provider leapmuxv1.AgentProvider, launch, confirmed OptionMap) (db.Agent, error) {
	return svc.persistConfirmedAgentSettings(agentID, provider, launch, confirmedOptions(provider, launch, confirmed))
}

// confirmedCatalogFor marshals the provider-reported option-group catalog for the CONFIRMED
// model (final[model]) -- not the launch model: for an account-default Claude agent the launch
// model is the sentinel "default", whose static fallback (used when the agent is momentarily
// unregistered) enumerates the provider-default model's effort tiers rather than the resolved
// model's. Shared by both confirmed-settings persist paths so the model-resolution rule lives
// in one place. Returns the marshal error rather than a truncated catalog (see marshalOptionGroups);
// callers skip the catalog write on error and keep the prior catalog.
func (svc *Context) confirmedCatalogFor(agentID string, provider leapmuxv1.AgentProvider, final OptionMap) (string, error) {
	return marshalOptionGroups(svc.Agents.OptionGroups(agentID, provider, final[agent.OptionIDModel]))
}

// confirmedCatalogOrSkip marshals the provider-reported catalog for the confirmed model (via
// confirmedCatalogFor) and returns the (expectedCatalog, catalog) CAS pair for a confirmed-settings
// write. On a marshal failure it logs with logCtx and returns ("", "") so the catalog CAS is a
// no-op -- the options still persist while the stored catalog is left intact rather than overwritten
// with a truncated one (the next live push re-persists a full catalog). Shared by the synchronous
// (persistConfirmedAgentSettings) and async (persistConfirmedAgentSettingsPreservingStartedSettings)
// confirmed-settings paths so the marshal-fail-skip rule lives in one place. `expected` is the
// catalog the caller read on the row -- returned as the CAS expectation when the marshal succeeds.
func (svc *Context) confirmedCatalogOrSkip(agentID string, provider leapmuxv1.AgentProvider, final OptionMap, expected, logCtx string) (expectedCatalog, catalog string) {
	catalog, err := svc.confirmedCatalogFor(agentID, provider, final)
	if err != nil {
		slog.Warn("skipping "+logCtx+" catalog write; catalog marshal failed",
			"agent_id", agentID, "error", err)
		return "", ""
	}
	return expected, catalog
}

// persistConfirmedAgentSettingsPreservingStartedSettings is the async startup variant. It
// compare-and-swaps the confirmed options onto the row only while the options column still equals
// `expectedOptions` -- the raw column read at the handoff, the snapshot `latest` was loaded from.
// `latest` already incorporates any setting the user changed during startup, so the confirmed blob
// (provider resolutions overlaid on `latest`) both applies those resolutions and preserves the
// user's edits; a change that raced in AFTER the handoff read leaves the column != expectedOptions
// and is left intact. The provider is taken from latest.AgentProvider.
func (svc *Context) persistConfirmedAgentSettingsPreservingStartedSettings(agentID, expectedOptions string, latest agent.Options, confirmed map[string]string, expectedOptionGroups string) (db.Agent, error) {
	provider := latest.AgentProvider
	final := confirmedOptions(provider, latest.Options, confirmed)
	// The CAS guard must compare against the row's CURRENT serialized options, canonicalized the
	// same way every write produces the column (marshalOptions sorts keys and drops empties).
	// `expectedOptions` is the raw options column read at the handoff. Recomputing
	// resolveProviderDefaults(latest.Options) here instead would be WRONG: the column is not always a
	// fixed point of that resolution -- a settings refresh that CLEARS a default-valued axis mid-
	// startup (an empty-value delete in the options delta) leaves the column without that key, while
	// resolveProviderDefaults re-fills it. The recomputed expectation would then never match the
	// column, the options CASE would silently take ELSE, and the WHOLE confirmed blob (including the
	// model resolution) would be discarded with no error. Guarding on the row's own canonical form
	// makes the CAS land whenever no concurrent writer actually moved the row. (Using `started`
	// instead would discard the confirmed blob whenever the user changed any axis mid-startup.)
	expected := marshalOptions(parseOptions(expectedOptions))

	expectedOptionGroups, catalog := svc.confirmedCatalogOrSkip(agentID, provider, final, expectedOptionGroups, "startup-confirmed")
	return svc.Queries.UpdateAgentConfirmedSettingsPreservingStartedSettings(bgCtx(), db.UpdateAgentConfirmedSettingsPreservingStartedSettingsParams{
		ExpectedOptions:  expected,
		ConfirmedOptions: marshalOptions(final),
		// The catalog (confirmedCatalogFor) is CAS-guarded against expectedOptionGroups (the
		// catalog on the row when this handoff read it) so a richer catalog a running provider
		// discovered after the handoff -- persisted via SetAgentOptionGroups on a separate,
		// unsynchronized path -- isn't clobbered by this one.
		ExpectedOptionGroups: expectedOptionGroups,
		OptionGroups:         catalog,
		ID:                   agentID,
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

	// Broadcast STARTING so frontends gate the thinking indicator and
	// startup panel correctly while the process is bouncing. Without this,
	// an agent whose status was non-ACTIVE before /clear (e.g. INACTIVE
	// after a worker restart that killed the process) shows no progress
	// affordance until context_cleared lands — by which point the indicator
	// is suppressed again because the chat history ends in a turn boundary.
	startingMsg := agentStartupLabel("Restarting", dbAgent.AgentProvider)
	svc.broadcastAgentStarting(&dbAgent, startingMsg, nil)

	// Stop the running agent and wait for it to fully exit so that
	// StartAgent below doesn't fail with "agent already running".
	svc.Agents.StopAndWaitAgent(agentID)

	svc.Output.ClearAgentRuntimeState(agentID)

	// Clear span tracking state from the previous session.
	svc.Output.ResetSpanTracker(agentID)

	// Restart the agent with a fresh context.
	// Don't clear agentSessionId before starting — the frontend uses it for
	// isWatchable. On success, handleSystemInit will overwrite it with the
	// new session ID. On failure, clear it so ensureAgentRunning won't try
	// to resume a stale session.
	launchOptions := applyDBSettingsToAgentOptions(agent.Options{
		AgentID:        agentID,
		WorkingDir:     dbAgent.WorkingDir,
		StartupTimeout: svc.agentStartupTimeout(),
		APITimeout:     svc.agentAPITimeout(),
		Shell:          svc.agentShell(),
		LoginShell:     svc.agentLoginShell(),
		HomeDir:        svc.HomeDir,
		AgentProvider:  dbAgent.AgentProvider,
	}, &dbAgent)
	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	confirmedSettings, err := svc.startAgent(bgCtx(), launchOptions, sink)
	if err != nil {
		slog.Error("clear context: failed to restart agent", "agent_id", agentID, "error", err)
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: "",
			ID:             agentID,
		})
		// Persist the error and broadcast STARTUP_FAILED so the frontend
		// transitions out of the STARTING state we entered above; otherwise
		// the startup panel would stay stuck on the "Restarting…" label.
		errMsg := err.Error()
		svc.persistAgentStartupError(agentID, errMsg)
		svc.broadcastAgentFailed(&dbAgent, errMsg, nil)
		svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":  agent.NotificationTypeAgentError,
			"error": "Failed to restart agent after clearing context: " + errMsg,
		})
		return
	}
	activeDbAgent, err := svc.persistConfirmedStartupSettings(agentID, dbAgent.AgentProvider, launchOptions.Options, confirmedSettings)
	if err != nil {
		slog.Warn("clear context: failed to persist confirmed settings", "agent_id", agentID, "error", err)
		activeDbAgent = dbAgent
	}
	slog.Info("clear context: agent restarted successfully", "agent_id", agentID)

	// Persist context_cleared before broadcasting ACTIVE so the frontend
	// receives the notification while the startup banner is still showing,
	// and the banner is replaced atomically by the new message instead of
	// disappearing into a brief empty gap. broadcastAgentActive is an
	// in-memory fan-out (microseconds), while PersistLeapMuxNotification
	// runs a DB write before broadcasting (5–50ms) — sending ACTIVE first
	// would let the banner clear before the message lands, producing the
	// flicker the ordering avoids. On failure the agent_error /
	// STARTUP_FAILED pair above stands on its own so clients do not see a
	// "cleared" UI state for an agent that is down.
	svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": agent.NotificationTypeContextCleared,
	})

	// Broadcast ACTIVE explicitly so the frontend leaves STARTING even if
	// the OutputSink's init handshake didn't (or hasn't yet) emitted its
	// own ACTIVE broadcast. broadcastAgentActive carries the fresh model
	// catalogs that the catch-up path also relies on.
	svc.broadcastAgentActive(&activeDbAgent, nil)
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
	if err == nil && hasMessages {
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

	// Serialize this cold-start against any concurrent auto-start or restart for the same
	// agent. The HasAgent check above and startAgent below otherwise straddle no lock, so two
	// concurrent sends to a cold agent (SendAgentMessage / a synthetic message / a control
	// request) would both pass the check and spawn duplicate subprocesses -- the second
	// overwriting (and orphaning) the first in the manager's agent map. LockAgent is the same
	// per-agent lifecycle mutex restart/clear use (see RestartAgent); re-check HasAgent under
	// it (double-checked locking) so a start that won the race is observed rather than repeated.
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()
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
	// Broadcast STARTING so the chat startup banner appears beneath any
	// just-typed messages while the cold subprocess spins up. Symmetric
	// with handleClearContext and runAgentStartup; without this, the
	// auto-start path (cold subprocess after worker/desktop restart) is
	// silent — the bubble pulses but no progress affordance is shown.
	svc.broadcastAgentStarting(&dbAgent, agentStartupLabel("Starting", dbAgent.AgentProvider), nil)

	launchOptions := applyDBSettingsToAgentOptions(agent.Options{
		AgentID:         agentID,
		WorkingDir:      dbAgent.WorkingDir,
		ResumeSessionID: resumeSessionID,
		StartupTimeout:  svc.agentStartupTimeout(),
		APITimeout:      svc.agentAPITimeout(),
		Shell:           svc.agentShell(),
		LoginShell:      svc.agentLoginShell(),
		HomeDir:         svc.HomeDir,
		AgentProvider:   dbAgent.AgentProvider,
	}, &dbAgent)
	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	confirmedSettings, err := svc.startAgent(bgCtx(), launchOptions, sink)
	if err != nil {
		slog.Error("ensureAgentRunning: failed to start agent", "agent_id", agentID, "error", err)
		// Revert the STARTING broadcast so the spinner clears. Caller
		// surfaces the failure as a per-message delivery_error; we don't
		// broadcast STARTUP_FAILED here because that would make the
		// agent permanently unusable until the user opens a new one,
		// while the existing design keeps it retryable on the next send.
		svc.broadcastAgentInactive(&dbAgent)
		return err
	}
	if _, err := svc.persistConfirmedStartupSettings(agentID, dbAgent.AgentProvider, launchOptions.Options, confirmedSettings); err != nil {
		slog.Warn("ensureAgentRunning: failed to persist confirmed settings", "agent_id", agentID, "error", err)
	}

	slog.Info("ensureAgentRunning: agent started", "agent_id", agentID)
	return nil
}

// handleControlRequestMessage handles raw provider control input
// (e.g. Claude control_request JSON or Codex JSON-RPC interrupt).
// These payloads are forwarded directly to the agent's stdin and are not
// wrapped in a user message envelope or persisted as chat messages.
func (svc *Context) handleControlRequestMessage(agentID string, provider leapmuxv1.AgentProvider, content string) {
	// Persist set_permission_mode to the DB eagerly so that /clear
	// (which reads the DB) always sees the latest mode. Some providers
	// (e.g. Claude Code) don't echo the mode back in their
	// control_response, so relying on the output handler alone would
	// leave the DB stale.
	mode, isSetMode := parseSetPermissionMode(content)
	if isSetMode {
		unlock := svc.Agents.LockAgent(agentID)
		defer unlock()

		svc.setAgentPermissionMode(agentID, mode)

		if !svc.Agents.HasAgent(agentID) {
			return
		}

		if err := svc.Agents.SendRawInput(agentID, []byte(content)); err != nil {
			slog.Error("failed to send control request to agent", "agent_id", agentID, "error", err)
		}
		return
	}

	// If agent is not running, handle special cases locally.
	if !svc.Agents.HasAgent(agentID) {
		if agent.IsInterruptRequest(provider, content) {
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
	if err := svc.Output.persistAndBroadcast(agentID, provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, innerJSON, agent.SpanInfo{}, nil); err != nil {
		slog.Error("synthetic user message: failed to persist message", "agent_id", agentID, "error", err)
	}
}

// broadcastSettingsStatusChange broadcasts an UNSPECIFIED-status AgentStatusChange
// carrying the refreshed option-group catalog, so frontends update their settings
// display in place without a status transition. Shares the base field set with the
// lifecycle status builders (baseAgentStatusChange) and attaches OptionGroups like the
// ACTIVE path, so a future status-change field is wired here too.
func (svc *Context) broadcastSettingsStatusChange(dbAgent db.Agent) {
	sc := baseAgentStatusChange(&dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, gitutil.GetGitStatus(bgCtx(), dbAgent.WorkingDir))
	sc.OptionGroups = svc.optionGroupsForAgent(&dbAgent)
	svc.broadcastStatusChange(dbAgent.ID, sc)
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

// applyOptionsSpec tunes how applyOptionChanges treats a set of option changes.
type applyOptionsSpec struct {
	// live pushes the changed values to a running agent via UpdateSettings. The
	// permission-mode path leaves it false: it records a mode the agent already
	// switched to itself (via a control response), so re-pushing would be redundant.
	live bool
	// notifyFirstSet, when false, suppresses the settings_changed notification for a
	// change whose prior value was empty (a first set) -- permission mode shouldn't
	// announce its initial default.
	notifyFirstSet bool
}

// applyOptionChanges diffs `wanted` (id->new value) against the agent's stored options,
// persists the changed axes, optionally pushes them to a running agent, broadcasts the
// refreshed catalog, and emits a settings_changed notification. It returns dbAgent with
// its Options updated (unchanged on a no-op or a DB error). Shared by the permission-mode,
// collaboration-mode, and Codex-bypass setters so their persist/broadcast/notify sequence
// can't drift.
func (svc *Context) applyOptionChanges(dbAgent db.Agent, wanted OptionMap, spec applyOptionsSpec) db.Agent {
	agentID := dbAgent.ID
	opts := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
	applied := OptionMap{}
	oldVals := OptionMap{}
	for id, newVal := range wanted {
		oldVal := opts[id]
		if oldVal == newVal {
			continue
		}
		oldVals[id] = oldVal
		opts[id] = newVal
		applied[id] = newVal
	}
	if len(applied) == 0 {
		return dbAgent
	}

	// For a live change, push to the running agent and read back the values it confirmed (a
	// provider may clamp/normalize an axis -- e.g. Codex re-reads model/effort/approval/sandbox
	// via config/read; collaboration_mode and network_access are per-turn params, not config
	// keys, so config/read never echoes them and they keep the pushed value), overlaying them so
	// the row we persist, the catalog we broadcast, AND the chat notification all reflect the
	// CONFIRMED values rather than the optimistic request. The push + readback stay under the
	// lifecycle lock so a concurrent UpdateAgentSettings for the same agent can't interleave and
	// make us read its confirmation instead of ours.
	provider := dbAgent.AgentProvider
	if spec.live && svc.Agents.HasAgent(agentID) {
		// Push the change and read back the confirmed snapshot under the lifecycle lock (see
		// pushAndReadConfirmed). Overlay the provider's confirmed values only while the session is
		// still live; when it's gone (nil snapshot), keep the REQUESTED values -- the next launch
		// confirms/clamps them -- rather than treating a nil snapshot as confirmation and
		// persisting/broadcasting the optimistic values as if the dead session had settled them.
		if confirmed, _ := svc.pushAndReadConfirmed(agentID, applied); confirmed != nil {
			opts = confirmedOptions(provider, opts, confirmed)
		}
	}

	// Persist ONLY the axes we changed (with their provider-confirmed value), via a
	// compare-and-swap, so a concurrent server-initiated PersistSettingsRefresh -- which holds
	// no lifecycle lock -- can neither lose our keys nor have its keys clobbered by a stale
	// full-map blob. The lifecycle lock above does not serialize against that reader-goroutine
	// writer, so the blind full-map write this path used before could drop a key the refresh
	// had just merged in.
	delta := make(OptionMap, len(applied))
	for id := range applied {
		delta[id] = opts[id]
	}
	settled, _, err := casPersistAgentOptions(bgCtx(), svc.Queries, agentID, dbAgent.Options, delta)
	if err != nil {
		slog.Error("apply option changes: DB update failed", "agent_id", agentID, "options", applied, "error", err)
		return dbAgent
	}
	dbAgent.Options = settled

	svc.broadcastSettingsStatusChange(dbAgent)

	// Build the settings_changed notification from the CONFIRMED values (opts, after the live
	// readback), diffing each applied axis against its prior value, so a clamp the provider applied
	// is announced as the settled value -- matching the row and broadcast catalog. buildSettingsChanges
	// resolves labels against the SETTLED-model catalog (avoiding the empty-model effort-id leak) and
	// honors spec.notifyFirstSet, the same emitter the model-settle path uses.
	changes := svc.buildSettingsChanges(&dbAgent, oldVals, opts, sortedOptionKeys(applied), spec.notifyFirstSet)
	if len(changes) > 0 {
		svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":    agent.NotificationTypeSettingsChanged,
			"changes": changes,
		})
	}

	return dbAgent
}

func (svc *Context) setAgentPermissionModeWithAgent(dbAgent db.Agent, mode string) db.Agent {
	return svc.applyOptionChanges(dbAgent,
		map[string]string{agent.OptionIDPermissionMode: mode},
		applyOptionsSpec{live: false, notifyFirstSet: false})
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

	// Capture currently-active spans so the user message renders with
	// passthrough vertical bars instead of breaking the column.
	spanLines := svc.Output.snapshotPassthroughSpanLines(agentID)

	seq, err := createMessageRow(bgCtx(), svc.Queries, db.CreateMessageParams{
		ID:                 messageID,
		AgentID:            agentID,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Depth:              0,
		SpanID:             "",
		ParentSpanID:       "",
		SpanLines:          spanLines,
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
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		DeliveryError:      deliveryError,
		AgentProvider:      dbAgent.AgentProvider,
		CreatedAt:          timefmt.Format(now),
		Depth:              0,
		SpanLines:          spanLines,
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

// controlResponsePlanModePayload is the decoded shape of a plan-mode control
// response: the approve/reject envelope ({request_id, response:{behavior, message}})
// plus the optional permission-mode switch and context-clear the frontend attaches.
// Shared by the two plan-mode handlers (the Codex prompt-response path and the common
// control-response path) so the wire shape is defined once and can't drift between them.
type controlResponsePlanModePayload struct {
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

func (svc *Context) handleCodexPlanModePromptResponse(agentID string, content []byte) bool {
	var crPayload controlResponsePlanModePayload
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

		// Settle Codex's collaboration axis back to its default mode (applied live, notify on
		// first set).
		dbAgent = svc.applyOptionChanges(dbAgent,
			map[string]string{agent.CodexOptionCollaborationMode: agent.CodexCollaborationDefault},
			applyOptionsSpec{live: true, notifyFirstSet: true})

		if crPayload.PermissionMode != "" {
			dbAgent = svc.setAgentPermissionModeWithAgent(dbAgent, crPayload.PermissionMode)
			// Grant Codex bypass options for the approved mode: full network access and no
			// sandbox restrictions (applied live, notify on first set).
			svc.applyOptionChanges(dbAgent, map[string]string{
				agent.CodexOptionNetworkAccess: agent.CodexNetworkEnabled,
				agent.CodexOptionSandboxPolicy: agent.CodexSandboxDangerFullAccess,
			}, applyOptionsSpec{live: true, notifyFirstSet: true})
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
	var crPayload controlResponsePlanModePayload
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
		slog.Warn("control request payload unmarshal failed", "agent_id", agentID, "error", err)
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
		} else if err := svc.Output.persistAndBroadcast(agentID, dbAgent.AgentProvider, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, displayJSON, agent.SpanInfo{}, nil); err != nil {
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

	// Read plan content from disk. The agents row carries the path; the
	// file is the sole source of truth for plan content.
	var planContent string
	if dbAgent.PlanFilePath != "" {
		if data, readErr := os.ReadFile(dbAgent.PlanFilePath); readErr == nil && len(data) > 0 {
			planContent = string(data)
		}
	}

	if planContent == "" {
		slog.Warn("plan exec: no plan content found, broadcasting notification without restart",
			"agent_id", agentID)
		svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":           agent.NotificationTypePlanExecution,
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
		svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type": agent.NotificationTypeContextCleared,
		})
		svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":           agent.NotificationTypePlanExecution,
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
	if err := svc.Output.persistAndBroadcast(agentID, dbAgent.AgentProvider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, innerJSON, agent.SpanInfo{}, nil); err != nil {
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

	svc.Output.ClearAgentRuntimeState(agentID)

	// Clear span tracking state from the previous session.
	svc.Output.ResetSpanTracker(agentID)

	// Broadcast context_cleared and plan_execution as separate notifications.
	svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type": agent.NotificationTypeContextCleared,
	})
	svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
		"type":           agent.NotificationTypePlanExecution,
		"plan_file_path": dbAgent.PlanFilePath,
	})

	// Restart agent with plan content. Use svc.startAgent — the
	// test-injectable wrapper that forwards to svc.Agents.StartAgent in
	// production — so unit tests can stub the restart out.
	launchOptions := applyDBSettingsToAgentOptions(agent.Options{
		AgentID:        agentID,
		WorkingDir:     dbAgent.WorkingDir,
		StartupTimeout: svc.agentStartupTimeout(),
		APITimeout:     svc.agentAPITimeout(),
		Shell:          svc.agentShell(),
		LoginShell:     svc.agentLoginShell(),
		HomeDir:        svc.HomeDir,
		AgentProvider:  dbAgent.AgentProvider,
	}, &dbAgent)
	// Plan execution forces the target permission mode (e.g. acceptEdits).
	// applyDBSettingsToAgentOptions populated a fresh Options map, so writing the
	// key here is safe (no shared aliasing).
	launchOptions.Options[agent.OptionIDPermissionMode] = targetMode
	sink := svc.Output.NewSink(agentID, dbAgent.AgentProvider)
	confirmedSettings, err := svc.startAgent(bgCtx(), launchOptions, sink)
	if err != nil {
		slog.Error("plan exec: failed to restart agent", "agent_id", agentID, "error", err)
		_ = svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: "",
			ID:             agentID,
		})
		svc.Output.PersistLeapMuxNotification(agentID, dbAgent.AgentProvider, map[string]interface{}{
			"type":  agent.NotificationTypeAgentError,
			"error": "Failed to restart agent for plan execution: " + err.Error(),
		})
		return
	}
	if _, err := svc.persistConfirmedStartupSettings(agentID, dbAgent.AgentProvider, launchOptions.Options, confirmedSettings); err != nil {
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
		Source:             m.Source,
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

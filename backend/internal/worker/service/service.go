// Package service implements the Worker-side business logic for E2EE channel
// requests. Each service registers its handlers with the inner RPC dispatcher,
// which routes decrypted InnerRpcRequests from the Frontend to the appropriate
// handler function.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SendFunc sends a ConnectRequest message to the Hub.
type SendFunc func(msg *leapmuxv1.ConnectRequest) error

// Context holds shared dependencies for all service implementations.
type Context struct {
	DB                  *sql.DB
	Queries             *db.Queries
	Agents              *agent.Manager
	Terminals           *terminal.Manager
	Channels            *channel.Manager // E2EE channel manager (for workspace access lookups)
	HomeDir             string
	DataDir             string
	WorkerID            string                    // This worker's ID (set after registration)
	Name                string                    // Worker display name (from LEAPMUX_WORKER_NAME, defaults to hostname)
	Send                SendFunc                  // Forwards messages to the Hub via WebSocket
	Watchers            *WatcherManager           // Fan-out manager for event broadcasting
	Output              *OutputHandler            // Agent output NDJSON processor
	AgentStartupTimeout time.Duration             // Timeout for agent startup handshake (default: 30s)
	APITimeout          time.Duration             // Timeout for JSON-RPC requests (default: 10s)
	UseLoginShell       bool                      // Wrap claude invocation in user's login shell
	WakeLock            *wakelock.ActivityTracker // Keep-awake tracker (nil = disabled)
	RegisteredBy        string                    // User ID who registered this worker (for tunnel authorization)

	startAgentFn        func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error)
	startTerminalFn     func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error
	createAgentRecordFn func(context.Context, db.CreateAgentParams) error
	getAgentByIDFn      func(context.Context, string) (db.Agent, error)

	// Startup tracks in-flight agent/terminal startups — the window
	// between OpenAgent/OpenTerminal returning and the subprocess being
	// ready. See startupstate.go.
	Startup *startupRegistry
}

// agentStartupTimeout returns the configured agent startup timeout,
// or 30s if not set.
func (svc *Context) agentStartupTimeout() time.Duration {
	if svc.AgentStartupTimeout > 0 {
		return svc.AgentStartupTimeout
	}
	return time.Duration(config.DefaultAgentStartupTimeoutSeconds) * time.Second
}

// agentAPITimeout returns the configured API timeout, or the default if not set.
func (svc *Context) agentAPITimeout() time.Duration {
	if svc.APITimeout > 0 {
		return svc.APITimeout
	}
	return agent.DefaultAPITimeout
}

// NewContext creates a new service context with all dependencies.
func NewContext(sqlDB *sql.DB, agents *agent.Manager, terminals *terminal.Manager, homeDir, dataDir string, wl *wakelock.ActivityTracker) *Context {
	queries := db.New(sqlDB)
	watchers := NewWatcherManager()
	output := NewOutputHandler(queries, watchers, agents, wl)
	output.DataDir = dataDir
	svc := &Context{
		DB:        sqlDB,
		Queries:   queries,
		Agents:    agents,
		Terminals: terminals,
		HomeDir:   homeDir,
		DataDir:   dataDir,
		Watchers:  watchers,
		Output:    output,
		WakeLock:  wl,
		Startup:   newStartupRegistry(),
	}
	svc.startAgentFn = svc.Agents.StartAgent
	svc.startTerminalFn = svc.Terminals.StartTerminal
	svc.createAgentRecordFn = svc.Queries.CreateAgent
	svc.getAgentByIDFn = svc.Queries.GetAgentByID
	return svc
}

func (svc *Context) startAgent(ctx context.Context, opts agent.Options, sink agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
	if svc.startAgentFn != nil {
		return svc.startAgentFn(ctx, opts, sink)
	}
	return svc.Agents.StartAgent(ctx, opts, sink)
}

func (svc *Context) startTerminal(opts terminal.Options, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
	if svc.startTerminalFn != nil {
		return svc.startTerminalFn(opts, outputFn, exitFn)
	}
	return svc.Terminals.StartTerminal(opts, outputFn, exitFn)
}

func (svc *Context) createAgentRecord(ctx context.Context, params db.CreateAgentParams) error {
	if svc.createAgentRecordFn != nil {
		return svc.createAgentRecordFn(ctx, params)
	}
	return svc.Queries.CreateAgent(ctx, params)
}

func (svc *Context) getAgentByID(ctx context.Context, agentID string) (db.Agent, error) {
	if svc.getAgentByIDFn != nil {
		return svc.getAgentByIDFn(ctx, agentID)
	}
	return svc.Queries.GetAgentByID(ctx, agentID)
}

// Init performs one-time startup tasks such as clearing stale agent state
// left over from a previous Worker process.
//
// Init panics if required fields (Channels, Send) have not been set.
// These fields are not part of NewContext because they depend on
// components that are created separately (e.g. the channel manager).
func (svc *Context) Init() {
	// Validate required fields that are set after NewContext.
	if svc.Channels == nil {
		panic("service.Context.Init: Channels must be set before calling Init")
	}
	if svc.Send == nil {
		panic("service.Context.Init: Send must be set before calling Init")
	}

	// Wire auto-continue so OutputHandler can send synthetic user messages.
	svc.Output.SetSendMessageFunc(svc.sendSyntheticUserMessage)
	svc.Output.restoreAutoContinueSchedules()

	// No need to deactivate agents/terminals on startup — status is now
	// derived from runtime state (HasAgent/HasTerminal), not from the DB.
}

// Shutdown persists in-memory terminal state to the database so it
// survives a worker restart. Call this before stopping the terminal
// manager (which clears in-memory state).
func (svc *Context) Shutdown() {
	for _, tid := range svc.Terminals.ListTerminalIDs() {
		svc.appendTerminalDisconnectNotice(tid)

		// Try to get a full snapshot (metadata + screen). If the screen
		// is empty (e.g. terminal was killed before rendering), fall back
		// to metadata-only so the title and other fields are still saved.
		snap, ok := svc.Terminals.SnapshotTerminal(tid)
		if ok {
			if err := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
				ID:            tid,
				WorkspaceID:   snap.WorkspaceID,
				WorkingDir:    snap.WorkingDir,
				ShellStartDir: snap.ShellStartDir,
				Title:         snap.Title,
				Cols:          int64(snap.Cols),
				Rows:          int64(snap.Rows),
				Screen:        appendTerminalDisconnectNotice(snap.Screen),
			}); err != nil {
				slog.Error("failed to save terminal on shutdown", "terminal_id", tid, "error", err)
			}
			continue
		}

		// No screen available — still persist metadata (title, etc.)
		// so it survives the restart.
		meta, hasMeta := svc.Terminals.GetMeta(tid)
		if !hasMeta {
			continue
		}
		if err := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
			ID:            tid,
			WorkspaceID:   meta.WorkspaceID,
			WorkingDir:    meta.WorkingDir,
			ShellStartDir: meta.ShellStartDir,
			Title:         meta.Title,
			Cols:          int64(meta.Cols),
			Rows:          int64(meta.Rows),
			Screen:        appendTerminalDisconnectNotice(nil),
		}); err != nil {
			slog.Error("failed to save terminal metadata on shutdown", "terminal_id", tid, "error", err)
		}
	}
}

var terminalDisconnectNotice = []byte("\r\n\r\n[Connection to the terminal was lost.]\r\n")

func appendTerminalDisconnectNotice(screen []byte) []byte {
	if bytes.HasSuffix(screen, terminalDisconnectNotice) {
		out := make([]byte, len(screen))
		copy(out, screen)
		return out
	}

	out := make([]byte, 0, len(screen)+len(terminalDisconnectNotice))
	out = append(out, screen...)
	out = append(out, terminalDisconnectNotice...)
	return out
}

func (svc *Context) appendTerminalDisconnectNotice(terminalID string) {
	screen := svc.Terminals.ScreenSnapshot(terminalID)
	if bytes.HasSuffix(screen, terminalDisconnectNotice) {
		return
	}
	_ = svc.Terminals.AppendOutput(terminalID, terminalDisconnectNotice)
}

// RegisterAll registers all service handlers with the dispatcher.
func RegisterAll(d *channel.Dispatcher, svc *Context) {
	registerFileHandlers(d, svc)
	registerGitHandlers(d, svc)
	registerTerminalHandlers(d, svc)
	registerAgentHandlers(d, svc)
	registerCleanupHandlers(d, svc)
	registerTabMoveHandlers(d, svc)
	registerSysInfoHandlers(d, svc)
	registerTunnelHandlers(d, svc)
}

// modelOrDefault returns the model if non-empty, otherwise the provider's
// default model from the agent registry (which checks env vars and the
// registered default model list).
func modelOrDefault(model string, provider leapmuxv1.AgentProvider) string {
	if model != "" {
		return model
	}
	return agent.DefaultModel(provider)
}

// effortOrDefault returns the effort if non-empty, otherwise the default
// effort for the given model (falling back to the provider's default model
// if the given model has no configured default).
func effortOrDefault(effort, model string, provider leapmuxv1.AgentProvider) string {
	if effort != "" {
		return effort
	}
	return agent.DefaultEffortForModel(provider, model)
}

// settingsDisplayLabels returns lookup functions for model and effort display
// names using the agent's AvailableModels data. If the agent is not running or
// has no model list, the lookup functions return the raw ID as-is.
func (svc *Context) settingsDisplayLabels(agentID string, provider leapmuxv1.AgentProvider) (modelLabel, effortLabel func(string) string) {
	models := svc.Agents.AvailableModels(agentID, provider)

	// Build model ID → displayName map.
	modelMap := make(map[string]string, len(models))
	effortMap := make(map[string]string, len(models)*4)
	for _, m := range models {
		if m.DisplayName != "" {
			modelMap[m.Id] = m.DisplayName
		}
		for _, e := range m.SupportedEfforts {
			if e.Name != "" {
				effortMap[e.Id] = e.Name
			}
		}
	}

	modelLabel = func(id string) string {
		if label, ok := modelMap[id]; ok {
			return label
		}
		return id
	}
	effortLabel = func(id string) string {
		if label, ok := effortMap[id]; ok {
			return label
		}
		return id
	}
	return
}

// permissionModeLabel returns a human-readable label for a permission mode ID
// by looking up the "permissionMode" option group for the running agent when
// available, then falling back to the provider registry.
func (svc *Context) permissionModeLabel(agentID, mode string, provider leapmuxv1.AgentProvider) string {
	return svc.optionLabel(agentID, agent.OptionGroupKeyPermissionMode, mode, provider)
}

func (svc *Context) optionGroupLabel(agentID, key string, provider leapmuxv1.AgentProvider) string {
	for _, group := range svc.Agents.AvailableOptionGroups(agentID, provider) {
		if group.Key == key {
			if group.Label != "" {
				return group.Label
			}
			return key
		}
	}
	return key
}

// optionLabel looks up a human-readable label for an option value from the
// runtime option groups when the agent is running, falling back to the raw
// value if not found.
func (svc *Context) optionLabel(agentID, key, value string, provider leapmuxv1.AgentProvider) string {
	for _, group := range svc.Agents.AvailableOptionGroups(agentID, provider) {
		if group.Key == key {
			for _, opt := range group.Options {
				if opt.Id == value {
					return opt.Name
				}
			}
			return value
		}
	}
	return value
}

// sendProtoResponse is a helper that serializes a proto response and sends it.
func sendProtoResponse(sender *channel.Sender, msg proto.Message) {
	slog.Debug("response payload", "payload", protojson.Format(msg))
	data, err := proto.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal response", "error", err)
		_ = sender.SendError(13, "internal: marshal response") // INTERNAL
		return
	}
	_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{
		Payload: data,
	})
}

// unmarshalRequest is a helper that deserializes an InnerRpcRequest payload.
func unmarshalRequest(req *leapmuxv1.InnerRpcRequest, msg proto.Message) error {
	if err := proto.Unmarshal(req.GetPayload(), msg); err != nil {
		return err
	}
	slog.Debug("request payload",
		"method", req.GetMethod(),
		"payload", protojson.Format(msg),
	)
	return nil
}

// sendInternalError sends an INTERNAL (13) error response.
func sendInternalError(sender *channel.Sender, msg string) {
	_ = sender.SendError(13, msg)
}

// sendNotFoundError sends a NOT_FOUND (5) error response.
func sendNotFoundError(sender *channel.Sender, msg string) {
	_ = sender.SendError(5, msg)
}

// sendPermissionDenied sends a PERMISSION_DENIED (7) error response.
func sendPermissionDenied(sender *channel.Sender, msg string) {
	_ = sender.SendError(7, msg)
}

// sendInvalidArgument sends an INVALID_ARGUMENT (3) error response.
func sendInvalidArgument(sender *channel.Sender, msg string) {
	_ = sender.SendError(3, msg)
}

// sendFailedPrecondition sends a FAILED_PRECONDITION (9) error response.
// Used when the request is valid but the target is not in a state that
// permits the operation (e.g. sending a message to an agent that is
// still starting up).
func sendFailedPrecondition(sender *channel.Sender, msg string) {
	_ = sender.SendError(9, msg)
}

// requireAccessibleWorkspace verifies the workspace_id is accessible on the
// sender's channel. Sends PERMISSION_DENIED and returns false when the channel
// has no context or the workspace is not in its accessible set (populated at
// channel handshake by the hub's list of workspaces the user owns). The
// caller is responsible for rejecting empty workspace_id up front.
func (svc *Context) requireAccessibleWorkspace(sender *channel.Sender, workspaceID string) bool {
	channelID := sender.ChannelID()
	if channelID == "" {
		sendPermissionDenied(sender, "workspace is not accessible")
		return false
	}
	if !svc.Channels.AccessibleWorkspaceIDs(channelID)[workspaceID] {
		sendPermissionDenied(sender, "workspace is not accessible")
		return false
	}
	return true
}

// requireAccessibleAgent looks up the agent and verifies its workspace is
// accessible on the sender's channel. Sends the appropriate error response
// and returns ok=false on empty id, missing row, db error, or denial. The
// returned Agent is the freshly-loaded row so callers can reuse it.
func (svc *Context) requireAccessibleAgent(sender *channel.Sender, agentID string) (db.Agent, bool) {
	if agentID == "" {
		sendInvalidArgument(sender, "agent_id is required")
		return db.Agent{}, false
	}
	agentRow, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			sendNotFoundError(sender, "agent not found")
			return db.Agent{}, false
		}
		slog.Error("failed to load agent for access check", "agent_id", agentID, "error", err)
		sendInternalError(sender, "failed to load agent")
		return db.Agent{}, false
	}
	if !svc.requireAccessibleWorkspace(sender, agentRow.WorkspaceID) {
		return db.Agent{}, false
	}
	return agentRow, true
}

// requireAccessibleTerminal looks up the terminal and verifies its workspace
// is accessible on the sender's channel. Mirror of requireAccessibleAgent.
func (svc *Context) requireAccessibleTerminal(sender *channel.Sender, terminalID string) (db.Terminal, bool) {
	if terminalID == "" {
		sendInvalidArgument(sender, "terminal_id is required")
		return db.Terminal{}, false
	}
	termRow, err := svc.Queries.GetTerminal(bgCtx(), terminalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			sendNotFoundError(sender, "terminal not found")
			return db.Terminal{}, false
		}
		slog.Error("failed to load terminal for access check", "terminal_id", terminalID, "error", err)
		sendInternalError(sender, "failed to load terminal")
		return db.Terminal{}, false
	}
	if !svc.requireAccessibleWorkspace(sender, termRow.WorkspaceID) {
		return db.Terminal{}, false
	}
	return termRow, true
}

// expandTilde expands a leading "~" or "~/" in a path to the user's home
// directory. Other forms (e.g. "~user/", "~~") are left unchanged.
func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// bgCtx returns a background context for database operations.
func bgCtx() context.Context {
	return context.Background()
}

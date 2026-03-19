// Package service implements the Worker-side business logic for E2EE channel
// requests. Each service registers its handlers with the inner RPC dispatcher,
// which routes decrypted InnerRpcRequests from the Frontend to the appropriate
// handler function.
package service

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
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
	WorkerID            string          // This worker's ID (set after registration)
	Name                string          // Worker display name (from LEAPMUX_WORKER_NAME, defaults to hostname)
	Version             string          // Build-time version string
	Send                SendFunc        // Forwards messages to the Hub via WebSocket
	Watchers            *WatcherManager // Fan-out manager for event broadcasting
	Output              *OutputHandler  // Agent output NDJSON processor
	AgentStartupTimeout time.Duration   // Timeout for agent startup handshake (default: 30s)
	UseLoginShell       bool            // Wrap claude invocation in user's login shell
}

// agentStartupTimeout returns the configured agent startup timeout,
// or 30s if not set.
func (svc *Context) agentStartupTimeout() time.Duration {
	if svc.AgentStartupTimeout > 0 {
		return svc.AgentStartupTimeout
	}
	return 30 * time.Second
}

// NewContext creates a new service context with all dependencies.
func NewContext(sqlDB *sql.DB, agents *agent.Manager, terminals *terminal.Manager, homeDir, dataDir string) *Context {
	queries := db.New(sqlDB)
	watchers := NewWatcherManager()
	output := NewOutputHandler(queries, watchers, agents)
	return &Context{
		DB:        sqlDB,
		Queries:   queries,
		Agents:    agents,
		Terminals: terminals,
		HomeDir:   homeDir,
		DataDir:   dataDir,
		Watchers:  watchers,
		Output:    output,
	}
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

	// No need to deactivate agents/terminals on startup — status is now
	// derived from runtime state (HasAgent/HasTerminal), not from the DB.
}

// Shutdown persists in-memory terminal state to the database so it
// survives a worker restart. Call this before stopping the terminal
// manager (which clears in-memory state).
func (svc *Context) Shutdown() {
	for _, tid := range svc.Terminals.ListTerminalIDs() {
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
				Screen:        snap.Screen,
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
			Screen:        []byte{},
		}); err != nil {
			slog.Error("failed to save terminal metadata on shutdown", "terminal_id", tid, "error", err)
		}
	}
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
}

// modelOrDefault returns the given model, or falls back to the
// LEAPMUX_DEFAULT_CLAUDE_MODEL environment variable, or "opus" if unset.
// This is used for Claude Code agents. For provider-aware defaults,
// use modelOrDefaultForProvider.
func modelOrDefault(model string) string {
	if model != "" {
		return model
	}
	if env := os.Getenv("LEAPMUX_DEFAULT_CLAUDE_MODEL"); env != "" {
		return env
	}
	return "opus"
}

// modelOrDefaultForProvider returns the given model, or falls back to a
// provider-specific default from environment variables.
func modelOrDefaultForProvider(model string, provider leapmuxv1.AgentProvider) string {
	if model != "" {
		return model
	}
	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		if env := os.Getenv("LEAPMUX_DEFAULT_CODEX_MODEL"); env != "" {
			return env
		}
		return "gpt-5.4"
	default:
		return modelOrDefault(model)
	}
}

// effortOrDefault returns the given effort, or falls back to the
// LEAPMUX_DEFAULT_EFFORT environment variable, or "high" if unset.
func effortOrDefault(effort string) string {
	if effort != "" {
		return effort
	}
	if env := os.Getenv("LEAPMUX_DEFAULT_EFFORT"); env != "" {
		return env
	}
	return "high"
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

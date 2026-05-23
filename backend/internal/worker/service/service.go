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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/grpc/codes"
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
	AgentStartupTimeout time.Duration             // Timeout for agent startup handshake (default: 5m)
	APITimeout          time.Duration             // Timeout for JSON-RPC requests (default: 10s)
	UseLoginShell       bool                      // Wrap claude invocation in user's login shell
	WakeLock            *wakelock.ActivityTracker // Keep-awake tracker (nil = disabled)
	RegisteredBy        string                    // User ID who registered this worker (for tunnel authorization)

	startAgentFn        func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error)
	startTerminalFn     func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error
	createAgentRecordFn func(context.Context, db.CreateAgentParams) error
	getAgentByIDFn      func(context.Context, string) (db.Agent, error)

	// AgentStartup / TerminalStartup track in-flight startups — the
	// window between OpenAgent/OpenTerminal returning and the subprocess
	// being ready. See startupstate.go.
	AgentStartup    *startupRegistry[leapmuxv1.AgentStatus]
	TerminalStartup *startupRegistry[leapmuxv1.TerminalStatus]

	// PrivateEvents is the worker-local pub/sub for E2EE-only events
	// (TabRenamed, FileTabPathRegistered, FileTabPathRevoked). Always
	// non-nil after NewContext.
	PrivateEvents *PrivateEventsBus

	// FileTabPaths persists (org_id, tab_id) -> (workspace_id,
	// file_path) for FILE-typed tabs. Always non-nil after NewContext.
	// The hub never sees these rows; clients fetch paths over E2EE.
	FileTabPaths *FileTabPathStore

	// RemoteIPC supplies per-agent local-IPC servers for the
	// `leapmux remote` CLI. Nil disables remote control (env vars are
	// not injected and no socket is created).
	RemoteIPC RemoteIPCFactory

	// Cleanup tracks in-flight close handlers so Shutdown() can wait for
	// them to finish before DB/data-dir teardown. Close handlers must
	// Add(1) at entry and defer Done() so Wait() in Shutdown observes
	// them even if a handler panics.
	Cleanup sync.WaitGroup

	// agentCleanups / terminalCleanups hold per-tab cleanup callbacks
	// registered by spawn*RemoteIPC and fired on close (or before a
	// restart mints a new token). Same shape, two embeddings keep the
	// terminal-vs-agent intent at the call site.
	agentCleanups    cleanupRegistry
	terminalCleanups cleanupRegistry

	// localAuthorizers maps synthetic local-IPC stream ids to their
	// LocalIPCAuthorizer. The router populates the entry around each
	// dispatch so requireAccessibleWorkspace can answer access checks
	// for callers that don't have an E2EE channel.
	localAuthorizers sync.Map
}

// cleanupRegistry holds id → cleanup callbacks under a single mutex.
// Used twice on Context (agentCleanups, terminalCleanups) so the two
// tab kinds keep distinct namespaces.
type cleanupRegistry struct {
	mu sync.Mutex
	m  map[string]func()
}

func (r *cleanupRegistry) register(id string, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]func(){}
	}
	r.m[id] = fn
}

func (r *cleanupRegistry) run(id string) {
	r.mu.Lock()
	fn, ok := r.m[id]
	delete(r.m, id)
	r.mu.Unlock()
	if ok && fn != nil {
		fn()
	}
}

func (svc *Context) registerAgentCleanup(agentID string, cleanupFn func()) {
	svc.agentCleanups.register(agentID, cleanupFn)
}

func (svc *Context) runAgentCleanup(agentID string) {
	svc.agentCleanups.run(agentID)
}

func (svc *Context) registerTerminalCleanup(terminalID string, cleanupFn func()) {
	svc.terminalCleanups.register(terminalID, cleanupFn)
}

func (svc *Context) runTerminalCleanup(terminalID string) {
	svc.terminalCleanups.run(terminalID)
}

// spawnRemoteIPC mints LEAPMUX_REMOTE_* env vars and registers the
// matching cleanup so a later close (or pre-restart re-mint) retires
// the token. kind is the user-facing tab type ("agent" or "terminal")
// — embedded in the log message and the slog id field. phase is an
// optional correlation tag ("open" / "restart" for terminals, empty
// for agents). Returns nil envs when RemoteIPC is disabled or the
// factory errors; in both cases the tab still spawns, just without
// remote control. The factory call is supplied as a closure so the
// generic helper doesn't have to know about the AgentSpawnInfo /
// TerminalSpawnInfo type split.
func (svc *Context) spawnRemoteIPC(
	kind, tabID, phase string,
	register func(string, func()),
	call func() ([]string, func(), error),
) []string {
	if svc.RemoteIPC == nil {
		return nil
	}
	envs, cleanup, err := call()
	if err != nil {
		attrs := []any{kind + "_id", tabID, "error", err}
		if phase != "" {
			attrs = append(attrs, "phase", phase)
		}
		slog.Warn("remote IPC factory failed; "+kind+" will start without remote control", attrs...)
		return nil
	}
	if cleanup != nil {
		register(tabID, cleanup)
	}
	return envs
}

// agentStartupTimeout returns the configured agent startup timeout,
// or the default if not set.
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
	output := NewOutputHandler(sqlDB, queries, watchers, agents, wl)
	output.DataDir = dataDir
	svc := &Context{
		DB:              sqlDB,
		Queries:         queries,
		Agents:          agents,
		Terminals:       terminals,
		HomeDir:         homeDir,
		DataDir:         dataDir,
		Watchers:        watchers,
		Output:          output,
		WakeLock:        wl,
		AgentStartup:    newAgentStartupRegistry(),
		TerminalStartup: newTerminalStartupRegistry(),
		PrivateEvents:   NewPrivateEventsBus(),
	}
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, svc.PrivateEvents)
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

func (svc *Context) startTerminal(ctx context.Context, opts terminal.Options, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
	if svc.startTerminalFn != nil {
		return svc.startTerminalFn(ctx, opts, outputFn, exitFn)
	}
	return svc.Terminals.StartTerminal(ctx, opts, outputFn, exitFn)
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
// manager (which clears in-memory state). Callers must have already
// stopped dispatching new OpenAgent/OpenTerminal/CloseAgent/CloseTerminal
// requests; otherwise the Wait calls below can race with a fresh Add.
func (svc *Context) Shutdown() {
	// Drain any goroutines spawned by OpenAgent/OpenTerminal so their
	// trailing DB writes and filesystem work land before the caller
	// closes the DB or removes data directories.
	svc.AgentStartup.WaitForInFlight()
	svc.TerminalStartup.WaitForInFlight()
	// Also drain in-flight close handlers. The frontend fires close RPCs
	// as fire-and-forget, so a close can still be running on the
	// dispatcher goroutine while the worker receives a deregister/SIGTERM.
	svc.Cleanup.Wait()

	for _, tid := range svc.Terminals.ListTerminalIDs() {
		// Already-exited terminals were persisted with their real exit
		// code by makeTerminalExitFn; don't clobber it with the shutdown
		// sentinel.
		if svc.Terminals.IsExited(tid) {
			continue
		}
		svc.persistTerminalOnExit(tid, exitCodeUnknown)
	}
}

// exitCodeUnknown is the sentinel used when the worker never observed
// the child's exit (forced Shutdown: the worker is tearing the child
// down without waiting for its exit code). Renders as the "Worker
// disconnected" wording rather than a literal "?" because in this path
// we *know* the worker killed the child — the exit code is unobserved,
// not unknown.
const exitCodeUnknown = -1

// terminalExitedNoticeSuffix is the constant trailing portion of every
// formatted notice. Idempotency checks use HasSuffix against this so a
// repeat call on a screen that already ends with any prior notice is a
// no-op. Both the "Terminal process exited (N)" and "Worker
// disconnected" notices end with this suffix so one check covers both.
var terminalExitedNoticeSuffix = []byte(" - Press Enter to restart]\r\n")

// formatTerminalExitedNotice renders the per-exit notice. The
// exitCodeUnknown sentinel produces the worker-disconnected wording;
// any other value produces the standard "Terminal process exited (N)"
// wording.
func formatTerminalExitedNotice(exitCode int) []byte {
	if exitCode == exitCodeUnknown {
		return []byte("\r\n\r\n[Worker disconnected - Press Enter to restart]\r\n")
	}
	return []byte("\r\n\r\n[Terminal process exited (" + strconv.Itoa(exitCode) + ") - Press Enter to restart]\r\n")
}

// persistTerminalOnExit injects the exit notice into the live terminal's
// screen buffer (so any subscriber sees it via the next broadcast) and
// writes the resulting row to the DB so it survives a worker restart.
// Single snapshot pass — the snapshot taken up front feeds both the
// idempotency check (skip AppendOutput when the notice is already there)
// and the DB-bound screen, avoiding the redundant RLock + HasSuffix that
// a separate appendTerminalExitedNotice + persist sequence would do.
// Falls back to metadata-only when no screen exists (e.g. terminal was
// killed before rendering) so title/dims are still saved. Returns false
// when neither snapshot nor meta is available — the row stays untouched.
//
// Always writes exitCode into the exit_code column EXCEPT when called
// with exitCodeUnknown on a screen that already carries the exit-notice
// suffix: that means the exit handler raced ahead and persisted the
// real exit code, so we skip to avoid clobbering it with the
// "worker disconnected" sentinel. The non-shutdown caller path (the
// exit handler itself) never passes exitCodeUnknown.
func (svc *Context) persistTerminalOnExit(tid string, exitCode int) bool {
	var (
		src    terminal.TerminalMeta
		screen []byte
		hasRow bool
	)
	if snap, ok := svc.Terminals.SnapshotTerminal(tid); ok {
		src, screen, hasRow = snap.TerminalMeta, snap.Screen, true
	} else if meta, ok := svc.Terminals.GetMeta(tid); ok {
		src, hasRow = meta, true
	}
	if !hasRow {
		return false
	}
	hasNotice := bytes.HasSuffix(screen, terminalExitedNoticeSuffix)
	if exitCode == exitCodeUnknown && hasNotice {
		// Shutdown lost the race to the exit handler — its persist already
		// landed a real exit code, leave the row alone.
		return true
	}
	if !hasNotice {
		notice := formatTerminalExitedNotice(exitCode)
		_ = svc.Terminals.AppendOutput(tid, notice)
		// SnapshotTerminal returns a freshly-allocated slice (tailBytesLocked),
		// so append can extend it directly without aliasing the manager's buffer.
		screen = append(screen, notice...)
	}
	// Shell column is INSERT-only (see UpsertTerminal SQL): UPDATE
	// preserves whatever was written at OpenTerminal time, so passing
	// the zero value here is a no-op on the existing row.
	params := db.UpsertTerminalParams{
		ID:            tid,
		WorkspaceID:   src.WorkspaceID,
		WorkingDir:    src.WorkingDir,
		HomeDir:       svc.HomeDir,
		ShellStartDir: src.ShellStartDir,
		Title:         src.Title,
		Cols:          int64(src.Cols),
		Rows:          int64(src.Rows),
		Screen:        screen,
		ExitCode:      int64(exitCode),
	}
	if err := svc.Queries.UpsertTerminal(bgCtx(), params); err != nil {
		slog.Error("failed to save terminal on exit", "terminal_id", tid, "error", err)
		return false
	}
	return true
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

// effortOrDefault returns the effort if non-empty, otherwise the provider's
// LEAPMUX_*_DEFAULT_EFFORT env var override, otherwise agent.EffortAuto.
// The sentinel tells the CLI layer to omit the --effort flag (Claude) or
// the reasoning_effort field (Codex) so the agent binary picks its own
// default.
func effortOrDefault(effort string, provider leapmuxv1.AgentProvider) string {
	if effort != "" {
		return effort
	}
	if env := agent.EffortEnvOverride(provider); env != "" {
		return env
	}
	return agent.EffortAuto
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
		_ = sender.SendError(int32(codes.Internal), "internal: marshal response")
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

// sendInternalError sends an Internal error response.
func sendInternalError(sender *channel.Sender, msg string) {
	_ = sender.SendError(int32(codes.Internal), msg)
}

// sendNotFoundError sends a NotFound error response.
func sendNotFoundError(sender *channel.Sender, msg string) {
	_ = sender.SendError(int32(codes.NotFound), msg)
}

// sendPermissionDenied sends a PermissionDenied error response.
func sendPermissionDenied(sender *channel.Sender, msg string) {
	_ = sender.SendError(int32(codes.PermissionDenied), msg)
}

// sendInvalidArgument sends an InvalidArgument error response.
func sendInvalidArgument(sender *channel.Sender, msg string) {
	_ = sender.SendError(int32(codes.InvalidArgument), msg)
}

// sendFailedPrecondition sends a FailedPrecondition error response.
// Used when the request is valid but the target is not in a state that
// permits the operation (e.g. sending a message to an agent that is
// still starting up).
func sendFailedPrecondition(sender *channel.Sender, msg string) {
	_ = sender.SendError(int32(codes.FailedPrecondition), msg)
}

// sendValidationError routes a validation failure to the most accurate
// gRPC code: context cancellation/timeout (client disconnect, hub-side
// deadline) maps to Canceled / DeadlineExceeded; everything else is the
// caller's bad input → InvalidArgument. Without the split, a client
// disconnect during a slow `git worktree list` validation surfaced as
// "invalid_argument: context canceled", which is misleading both for
// the user-facing toast and for any client-side telemetry that filters
// by code.
func sendValidationError(sender *channel.Sender, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		_ = sender.SendError(int32(codes.Canceled), err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		_ = sender.SendError(int32(codes.DeadlineExceeded), err.Error())
	default:
		sendInvalidArgument(sender, err.Error())
	}
}

// requireAccessibleWorkspace verifies the workspace_id is accessible on the
// sender's channel. Sends PERMISSION_DENIED and returns false when the channel
// has no context or the workspace is not in its accessible set (populated at
// channel handshake by the hub's list of workspaces the user owns). The
// caller is responsible for rejecting empty workspace_id up front.
//
// Authorization is delegated to AuthorizerForSender so both E2EE channels
// (channelmgr-backed) and local-IPC callers (registered LocalIPCAuthorizer)
// take the same code path. Callers that need the authorizer for follow-up
// checks (list filters, watcher subscriber ids) should use
// AuthorizerForSender directly.
func (svc *Context) requireAccessibleWorkspace(sender *channel.Sender, workspaceID string) bool {
	auth := svc.AuthorizerForSender(sender)
	if !auth.IsAccessible(workspaceID) {
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
	return requireAccessibleRow(
		svc, sender, agentID, "agent",
		svc.Queries.GetAgentByID,
		func(a db.Agent) string { return a.WorkspaceID },
	)
}

// requireAccessibleTerminal looks up the terminal and verifies its workspace
// is accessible on the sender's channel. Mirror of requireAccessibleAgent.
func (svc *Context) requireAccessibleTerminal(sender *channel.Sender, terminalID string) (db.Terminal, bool) {
	return requireAccessibleRow(
		svc, sender, terminalID, "terminal",
		svc.Queries.GetTerminal,
		func(t db.Terminal) string { return t.WorkspaceID },
	)
}

// requireAccessibleTerminalForRestart is the narrow-query variant used
// by the RestartTerminal handler: returns metadata + length(screen)
// without loading the screen BLOB. See GetTerminalForRestart for why.
func (svc *Context) requireAccessibleTerminalForRestart(sender *channel.Sender, terminalID string) (db.GetTerminalForRestartRow, bool) {
	return requireAccessibleRow(
		svc, sender, terminalID, "terminal",
		svc.Queries.GetTerminalForRestart,
		func(t db.GetTerminalForRestartRow) string { return t.WorkspaceID },
	)
}

// requireAccessibleRow factors the ACL + error-mapping shell shared by
// every "load a row by id, then check workspace access" helper. kind is
// the user-facing entity label embedded in error messages ("agent",
// "terminal"); fetch is the sqlc query; workspaceID extracts the row's
// workspace id for the access check.
func requireAccessibleRow[T any](
	svc *Context,
	sender *channel.Sender,
	id, kind string,
	fetch func(context.Context, string) (T, error),
	workspaceID func(T) string,
) (T, bool) {
	var zero T
	if id == "" {
		sendInvalidArgument(sender, kind+"_id is required")
		return zero, false
	}
	row, err := fetch(bgCtx(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			sendNotFoundError(sender, kind+" not found")
			return zero, false
		}
		slog.Error("failed to load "+kind+" for access check", kind+"_id", id, "error", err)
		sendInternalError(sender, "failed to load "+kind)
		return zero, false
	}
	if !svc.requireAccessibleWorkspace(sender, workspaceID(row)) {
		return zero, false
	}
	return row, true
}

// sanitizeOptionalTitle normalizes an OpenAgent/OpenTerminal title. An empty
// title is allowed (falls back to "Agent N"/"Terminal N" assignments
// downstream); a non-empty title goes through SanitizeName, which caps
// length at 128 chars and strips control characters + the set known to
// cause trouble in downstream string templating.
func sanitizeOptionalTitle(title string) (string, error) {
	if title == "" {
		return "", nil
	}
	sanitized, err := validate.SanitizeName(title)
	if err != nil {
		return "", fmt.Errorf("invalid title: %w", err)
	}
	return sanitized, nil
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

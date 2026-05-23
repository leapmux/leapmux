package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// pendingResizeWaitCap bounds how long runTerminalStartup blocks waiting
// for the frontend's first ResizeTerminal before spawning the shell. A
// typical wait returns in a few tens of ms; the cap is a safety valve
// for a wedged or unusually slow frontend.
const pendingResizeWaitCap = 500 * time.Millisecond

// terminalStartingLabel returns the "Starting <shell>…" label used for the
// sync prologue broadcast and the phase-1 re-broadcast once git status is
// in hand. Kept in one place so both call sites stay in lockstep.
func terminalStartingLabel(shell string) string {
	return "Starting " + filepath.Base(shell) + "…"
}

// beginTerminalStartup registers a fresh startup for terminalID with a
// cancellable background context, seeds the "Starting <shell>…" message
// on the registry, and broadcasts STARTING to watchers. gs is the git
// status to attach; both current callers pass nil and let the async
// goroutine re-broadcast once git status returns (the frontend keeps
// existing git fields when STARTING arrives without them, so a nil
// first broadcast is non-clobbering). Returns the ctx the caller
// passes into runTerminalStartup / runTerminalRestart.
func (svc *Context) beginTerminalStartup(terminalID, shell string, gs *leapmuxv1.AgentGitStatus) context.Context {
	startupCtx, cancel := context.WithCancel(context.Background())
	svc.TerminalStartup.begin(terminalID, cancel)
	msg := terminalStartingLabel(shell)
	svc.TerminalStartup.setMessage(terminalID, msg)
	svc.broadcastTerminalStarting(terminalID, msg, gs)
	return startupCtx
}

// registerTerminalHandlers registers all terminal-related RPC handlers.
func registerTerminalHandlers(d *channel.Dispatcher, svc *Context) {
	// OpenTerminal starts a new PTY terminal session.
	d.Register("OpenTerminal", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.OpenTerminalRequest
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

		cols := r.GetCols()
		if cols == 0 {
			cols = 80
		}
		rows := r.GetRows()
		if rows == 0 {
			rows = 25
		}

		// Resolve the default shell here (not inside terminal.Start) so
		// the startup-panel label reflects the actual binary, e.g.
		// "Starting zsh…" rather than a generic "Starting terminal…"
		// fallback when the client passes shell="".
		shell := r.GetShell()
		if shell == "" {
			shell = terminal.ResolveDefaultShell()
		}
		shellStartDir := expandTilde(r.GetShellStartDir())
		workingDir := expandTilde(r.GetWorkingDir())
		if workingDir == "" {
			workingDir = svc.HomeDir
		}

		// Validate git-mode options on the sync path so bad input fails
		// the RPC with InvalidArgument before we create any DB row. The
		// actual mutation happens inside runTerminalStartup.
		plan, gmErr := svc.validateGitMode(ctx, workingDir, &r)
		if gmErr != nil {
			sendValidationError(sender, gmErr)
			return
		}

		terminalID := id.Generate()

		outputFn := svc.makeTerminalOutputFn(terminalID)
		exitFn := svc.makeTerminalExitFn()

		// Persist the initial terminal record using the planned working
		// dir, so tab sync and post-refresh reads see the eventual path
		// even before git-mode execution creates the worktree.
		// Default a random "Terminal <Name>" title here so all spawn
		// paths (UI + CLI) get a name from one pool, picked one place.
		// OpenTerminalRequest has no title field by design — the
		// frontend used to pick client-side and call UpdateTerminalTitle
		// afterward; now it just reads `title` from this response.
		terminalTitle := pickTerminalTitle()
		if upsertErr := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
			ID:            terminalID,
			WorkspaceID:   workspaceID,
			WorkingDir:    plan.PlannedWorkingDir,
			HomeDir:       svc.HomeDir,
			ShellStartDir: shellStartDir,
			Shell:         shell,
			Title:         terminalTitle,
			Cols:          int64(cols),
			Rows:          int64(rows),
			Screen:        []byte{},
		}); upsertErr != nil {
			slog.Error("failed to persist terminal record", "terminal_id", terminalID, "error", upsertErr)
			sendInternalError(sender, "failed to persist terminal")
			return
		}

		// Register the startup in the registry with a cancel ctx so
		// CloseTerminal during phase 0 aborts executeGitMode, and seed
		// the STARTING broadcast with the provider label. Phase 0 will
		// overwrite the message with a mode-specific label (e.g.
		// `Creating worktree "feature/x"…`) before mutation begins. gs
		// is nil here because the post-mutation working dir isn't
		// known yet; phase 1 re-broadcasts with the real value.
		startupCtx := svc.beginTerminalStartup(terminalID, shell, nil)

		sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{
			TerminalId: terminalID,
			Title:      terminalTitle,
		})

		// Kick off git-mode execution + PTY spawn in the background.
		// The RemoteIPC mint happens inside runTerminalStartup so an
		// unusually slow factory doesn't stretch the synchronous RPC
		// latency the user sees.
		spawnInfo := TerminalSpawnInfo{
			UserID:      userID,
			OrgID:       r.GetOrgId(),
			WorkspaceID: workspaceID,
			WorkerID:    svc.WorkerID,
			TabID:       terminalID,
			WorkingDir:  plan.PlannedWorkingDir,
		}
		go svc.runTerminalStartup(startupCtx, terminal.Options{
			ID:            terminalID,
			WorkspaceID:   workspaceID,
			Shell:         shell,
			WorkingDir:    plan.PlannedWorkingDir,
			ShellStartDir: shellStartDir,
			Cols:          uint16(cols),
			Rows:          uint16(rows),
		}, spawnInfo, plan, outputFn, exitFn)
	})

	// RestartTerminal respawns the shell process for a terminal whose
	// previous PTY has exited. Reuses the tab's working_dir / shell /
	// shell_start_dir and mints fresh LEAPMUX_REMOTE_* env vars. The
	// existing screen buffer (including the "[Terminal process exited
	// (N) - Press Enter to restart]" notice) is preserved so the new
	// shell's prompt lands directly below the notice.
	d.Register("RestartTerminal", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.RestartTerminalRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		terminalID := r.GetTerminalId()
		dbTerm, ok := svc.requireAccessibleTerminalForRestart(sender, terminalID)
		if !ok {
			return
		}

		// Reject overlapping restarts: a previous startup hasn't broadcast
		// READY/FAILED yet (could be the original OpenTerminal still in
		// flight, or a back-to-back restart).
		if _, _, _, inFlight := svc.TerminalStartup.status(terminalID); inFlight {
			sendFailedPrecondition(sender, "terminal startup in progress")
			return
		}
		// Reject synchronously if the PTY is still alive so the user sees
		// FailedPrecondition rather than waiting for an async
		// STARTUP_FAILED broadcast from the spawn goroutine. The TOCTOU
		// between this check and Manager.RestartTerminal's own check is
		// benign: a PTY that exits between calls yields a false reject
		// that retrying Enter resolves.
		if svc.Terminals.IsRunning(terminalID) {
			sendFailedPrecondition(sender, terminal.ErrTerminalStillRunning.Error())
			return
		}

		cols := r.GetCols()
		if cols == 0 {
			cols = uint32(dbTerm.Cols)
		}
		rows := r.GetRows()
		if rows == 0 {
			rows = uint32(dbTerm.Rows)
		}

		// No default-shell fallback — an empty value here means the
		// OpenTerminal path that wrote the row skipped its own
		// ResolveDefaultShell() call, which is a real bug we'd rather
		// surface as a clear STARTUP_FAILED than mask by silently
		// swapping in a different shell.
		shell := dbTerm.Shell
		if shell == "" {
			sendFailedPrecondition(sender, "terminal has no shell to restart")
			return
		}

		// Seed STARTING without git status — the goroutine fetches and
		// re-broadcasts with branch/origin once it lands. Mirrors
		// runTerminalStartup's phase-1 pattern so the RPC round-trip
		// doesn't block on a slow `git status` against a large worktree.
		startupCtx := svc.beginTerminalStartup(terminalID, shell, nil)

		sendProtoResponse(sender, &leapmuxv1.RestartTerminalResponse{})

		outputFn := svc.makeTerminalOutputFn(terminalID)
		exitFn := svc.makeTerminalExitFn()
		// fallbackOffset seeds the cumulative byte counter only when no
		// in-memory ScreenBuffer is around (post-worker-restart). For the
		// common case it's ignored — Manager.RestartTerminal carries the
		// live buffer's counter through Respawn.
		var fallbackOffset int64
		if dbTerm.ScreenLength.Valid {
			fallbackOffset = dbTerm.ScreenLength.Int64
		}
		spawnInfo := TerminalSpawnInfo{
			UserID:      userID,
			OrgID:       r.GetOrgId(),
			WorkspaceID: dbTerm.WorkspaceID,
			WorkerID:    svc.WorkerID,
			TabID:       terminalID,
			WorkingDir:  dbTerm.WorkingDir,
		}
		go svc.runTerminalRestart(startupCtx, terminal.Options{
			ID:            terminalID,
			WorkspaceID:   dbTerm.WorkspaceID,
			Shell:         shell,
			WorkingDir:    dbTerm.WorkingDir,
			ShellStartDir: dbTerm.ShellStartDir,
			Cols:          uint16(cols),
			Rows:          uint16(rows),
		}, spawnInfo, fallbackOffset, outputFn, exitFn)
	})

	// CloseTerminal stops and removes a terminal session.
	d.RegisterTracked("CloseTerminal", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseTerminalRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		if _, ok := svc.requireAccessibleTerminal(sender, terminalID); !ok {
			return
		}

		// Tracked via dispatcher RegisterTracked above so Shutdown
		// drains the close flow (stop → DB close → unregister →
		// optional worktree remove) before tearing down the DB pool.
		// The frontend fires this RPC fire-and-forget after removing
		// the tab from the UI. The TerminalStartup goroutine's
		// trailing rollback work is tracked separately by
		// TerminalStartup.WaitForInFlight and drained in Shutdown.
		result := svc.closeTabCommon(
			leapmuxv1.TabType_TAB_TYPE_TERMINAL,
			terminalID,
			r.GetWorktreeAction(),
			func() {
				svc.TerminalStartup.cancelAndClear(terminalID)
				svc.Terminals.RemoveTerminal(terminalID)
				svc.runTerminalCleanup(terminalID)
			},
			func() error { return svc.Queries.CloseTerminal(bgCtx(), terminalID) },
		)
		sendProtoResponse(sender, &leapmuxv1.CloseTerminalResponse{Result: result})
	})

	// SendInput sends input data to a terminal.
	d.Register("SendInput", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendInputRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		if _, ok := svc.requireAccessibleTerminal(sender, terminalID); !ok {
			return
		}

		if svc.WakeLock != nil {
			svc.WakeLock.RecordActivity()
		}

		if err := svc.Terminals.SendInput(terminalID, r.GetData()); err != nil {
			slog.Error("failed to send input", "terminal_id", terminalID, "error", err)
			sendInternalError(sender, fmt.Sprintf("send input: %v", err))
			return
		}

		sendProtoResponse(sender, &leapmuxv1.SendInputResponse{})
	})

	// ResizeTerminal changes a terminal's dimensions.
	d.Register("ResizeTerminal", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ResizeTerminalRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		if _, ok := svc.requireAccessibleTerminal(sender, terminalID); !ok {
			return
		}

		cols := r.GetCols()
		rows := r.GetRows()
		if cols == 0 || rows == 0 {
			sendInvalidArgument(sender, "cols and rows must be > 0")
			return
		}

		err := svc.Terminals.Resize(terminalID, uint16(cols), uint16(rows))
		switch {
		case err == nil:
			// Drop any dims stashed during STARTING — the resize just
			// landed on the real PTY, so the post-startup apply in
			// runTerminalStartup must not overwrite it with older dims.
			svc.TerminalStartup.clearPendingResize(terminalID)
		case errors.Is(err, terminal.ErrTerminalNotFound):
			// Async startup: the tab exists but the PTY isn't in the
			// Manager yet. Stash the latest dims and ack success so the
			// frontend's first fit() isn't silently dropped — vim/nvim
			// would otherwise see the placeholder 80x24 from the
			// OpenTerminal request on its first TIOCGWINSZ query.
			if !svc.TerminalStartup.setPendingResize(terminalID, uint16(cols), uint16(rows)) {
				// Benign TOCTOU: the PTY exited between the frontend's
				// status check and this RPC arriving. The frontend gates
				// EXITED/DISCONNECTED/STARTUP_FAILED, so reaching here
				// means READY at check time and gone now — no PTY to
				// resize, but not actionable either.
				slog.Debug("resize on missing terminal", "terminal_id", terminalID, "error", err)
				sendInternalError(sender, fmt.Sprintf("resize: %v", err))
				return
			}
		default:
			slog.Error("failed to resize terminal", "terminal_id", terminalID, "error", err)
			sendInternalError(sender, fmt.Sprintf("resize: %v", err))
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ResizeTerminalResponse{})
	})

	// UpdateTerminalTitle updates a terminal's title in both the in-memory
	// manager and the database. The frontend throttles calls at 500ms
	// intervals (kept short so a title set right before shell exit reaches
	// the worker before the close handler persists meta to DB).
	d.Register("UpdateTerminalTitle", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.UpdateTerminalTitleRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		dbTerm, ok := svc.requireAccessibleTerminal(sender, terminalID)
		if !ok {
			return
		}

		title := r.GetTitle()
		svc.Terminals.UpdateTitle(terminalID, title)
		screen := dbTerm.Screen
		if screen == nil {
			screen = []byte{}
		}

		// Persist to DB so it survives restarts.
		if err := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
			ID:            dbTerm.ID,
			WorkspaceID:   dbTerm.WorkspaceID,
			WorkingDir:    dbTerm.WorkingDir,
			HomeDir:       dbTerm.HomeDir,
			ShellStartDir: dbTerm.ShellStartDir,
			Shell:         dbTerm.Shell,
			Title:         title,
			Cols:          dbTerm.Cols,
			Rows:          dbTerm.Rows,
			Screen:        screen,
			ExitCode:      dbTerm.ExitCode,
			ClosedAt:      dbTerm.ClosedAt,
		}); err != nil {
			slog.Error("failed to update terminal title", "terminal_id", terminalID, "error", err)
			sendInternalError(sender, "failed to update terminal title")
			return
		}

		if svc.PrivateEvents != nil {
			svc.PrivateEvents.PublishTabRenamed(
				dbTerm.WorkspaceID, terminalID, leapmuxv1.TabType_TAB_TYPE_TERMINAL,
				title, sender.ChannelID(),
			)
		}

		sendProtoResponse(sender, &leapmuxv1.UpdateTerminalTitleResponse{})
	})

	// ListTerminals returns all terminal tabs for a workspace.
	// Uses the in-memory terminal manager for running terminals and falls
	// back to saved terminal records for terminals that have already exited
	// and been removed from the manager.
	d.Register("ListTerminals", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListTerminalsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		tabIDs := r.GetTabIds()
		if len(tabIDs) == 0 {
			sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{})
			return
		}

		// Filter by access control: only return terminals in accessible
		// workspaces. AuthorizerForSender abstracts over E2EE channels and
		// local-IPC streams (which have no channel id but carry a token
		// scope registered at request entry).
		accessibleWsIDs := svc.AuthorizerForSender(sender).AccessibleSet()

		// Collect from the in-memory manager and DB-only rows, recording
		// each terminal's resolved git directory (see gitutil.ResolveGitDir)
		// so BatchGetGitStatus can dedupe across terminals that share a repo.
		entries := svc.Terminals.ListByIDs(tabIDs)
		seen := make(map[string]bool, len(entries))
		var terminals []*leapmuxv1.TerminalInfo
		var gitDirs []string
		for _, e := range entries {
			if accessibleWsIDs != nil && !accessibleWsIDs[e.Meta.WorkspaceID] {
				continue
			}
			seen[e.ID] = true
			ti := &leapmuxv1.TerminalInfo{
				TerminalId:      e.ID,
				Cols:            e.Meta.Cols,
				Rows:            e.Meta.Rows,
				Screen:          e.Screen,
				ScreenEndOffset: e.ScreenEndOffset,
				Exited:          e.Exited,
				WorkingDir:      e.Meta.WorkingDir,
				ShellStartDir:   e.Meta.ShellStartDir,
				Title:           e.Meta.Title,
				Status:          leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
			}
			if sup, errStr, msg, ok := svc.TerminalStartup.status(e.ID); ok {
				ti.Status = sup
				ti.StartupError = errStr
				ti.StartupMessage = msg
			}
			terminals = append(terminals, ti)
			gitDirs = append(gitDirs, gitutil.ResolveGitDir(e.Meta.ShellStartDir, e.Meta.WorkingDir))
		}

		dbTerminals, err := svc.Queries.ListTerminalsByIDs(ctx, tabIDs)
		if err != nil {
			slog.Error("failed to list terminals from DB", "error", err)
		} else {
			for _, ts := range dbTerminals {
				if seen[ts.ID] {
					continue
				}
				if accessibleWsIDs != nil && !accessibleWsIDs[ts.WorkspaceID] {
					continue
				}
				status, startupError, startupMessage := svc.deriveTerminalStatus(&ts)
				// DB-persisted screen is just the bytes; the backend has no
				// live ring for this terminal (PTY exited or worker
				// restarted), so the "end offset" equals the screen
				// length. The client's after_offset will be the same
				// value, and WatchEvents will return nothing for a dead
				// terminal — correct, since there are no new bytes.
				ti := &leapmuxv1.TerminalInfo{
					TerminalId:      ts.ID,
					Cols:            uint32(ts.Cols),
					Rows:            uint32(ts.Rows),
					Screen:          ts.Screen,
					ScreenEndOffset: int64(len(ts.Screen)),
					Exited:          !svc.Terminals.HasTerminal(ts.ID),
					WorkingDir:      ts.WorkingDir,
					ShellStartDir:   ts.ShellStartDir,
					Title:           ts.Title,
					Status:          status,
					StartupError:    startupError,
					StartupMessage:  startupMessage,
				}
				terminals = append(terminals, ti)
				gitDirs = append(gitDirs, gitutil.ResolveGitDir(ts.ShellStartDir, ts.WorkingDir))
			}
		}

		gitStatuses := gitutil.BatchGetGitStatus(ctx, gitDirs)
		for i, gs := range gitStatuses {
			if gs != nil {
				terminals[i].GitBranch = gs.Branch
				terminals[i].GitOriginUrl = gs.OriginUrl
				terminals[i].GitToplevel = gs.Toplevel
				terminals[i].GitIsWorktree = gs.IsWorktree
			}
		}

		sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{
			Terminals: terminals,
		})
	})

	// ListAvailableShells returns the shells installed on this worker.
	d.Register("ListAvailableShells", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListAvailableShellsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		shells, defaultShell := terminal.ListAvailableShells()
		sendProtoResponse(sender, &leapmuxv1.ListAvailableShellsResponse{
			Shells:       shells,
			DefaultShell: defaultShell,
		})
	})
}

// runTerminalStartup is the async body of OpenTerminal: it executes the
// git-mode plan, spawns the PTY, and reports READY or STARTUP_FAILED to the
// frontend. On failure it rolls back any partial git-mode side effects.
//
// spawnInfo carries the data needed to mint the LEAPMUX_REMOTE_* token.
// The mint runs inside this goroutine (rather than synchronously, before
// sendProtoResponse) so an unusually slow RemoteIPC factory doesn't
// stretch the RPC latency the user sees.
func (svc *Context) runTerminalStartup(ctx context.Context, opts terminal.Options, spawnInfo TerminalSpawnInfo, plan gitModePlan, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler) {
	defer svc.TerminalStartup.finish()
	terminalID := opts.ID

	// Mint the remote-IPC token before phase 0 so the cleanup is in the
	// map by the time any concurrent CloseTerminal calls runTerminalCleanup.
	// Ownership lives with this goroutine until we broadcast READY; until
	// then, the deferred cleanup retires the token on every error path so
	// a close that lost the register-vs-cleanup race doesn't leak it.
	// When no token was minted (RemoteIPC disabled or factory failed),
	// nothing was registered, so the defer skips the mutex roundtrip.
	opts.ExtraEnv = svc.spawnRemoteIPC("terminal", terminalID, "open", svc.registerTerminalCleanup, func() ([]string, func(), error) {
		return svc.RemoteIPC.TerminalSpawning(spawnInfo)
	})
	ownsIPCToken := opts.ExtraEnv != nil
	defer func() {
		if ownsIPCToken {
			svc.runTerminalCleanup(terminalID)
		}
	}()

	// Phase 0: execute git-mode mutation (worktree add, branch create,
	// checkout). Validation already ran synchronously.
	gm, gmErr := svc.runTerminalPhase0(ctx, terminalID, plan)
	if gmErr != nil {
		svc.failTerminalStartup(terminalID, gm, gmErr)
		return
	}
	svc.registerTabForWorktree(gm.WorktreeID, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)
	if gm.WorkingDir != "" {
		opts.WorkingDir = gm.WorkingDir
	}

	// Phase 1: compute git status from the final working dir (may be a
	// freshly-created worktree). The resulting branch/origin/toplevel travel
	// on the "Starting <shell>…" broadcast so the frontend can populate the
	// tab's gitBranch / gitOriginUrl / gitToplevel without an extra round-trip.
	gs := gitutil.GetGitStatus(ctx, gitutil.ResolveGitDir(opts.ShellStartDir, opts.WorkingDir))
	shellMsg := terminalStartingLabel(opts.Shell)
	svc.TerminalStartup.setMessage(terminalID, shellMsg)
	svc.broadcastTerminalStarting(terminalID, shellMsg, gs)

	// Wait for the frontend's first ResizeTerminal to arrive so the shell
	// is spawned at the final size rather than being SIGWINCH'd to it
	// after StartTerminal returns — some shells emit artifacts on a
	// mid-startup resize. If the cap elapses, the post-spawn apply below
	// still lands the dims on the running PTY.
	if cols, rows, ok := svc.TerminalStartup.waitForPendingResize(terminalID, pendingResizeWaitCap); ok {
		opts.Cols = cols
		opts.Rows = rows
	}

	startErr := svc.startTerminal(ctx, opts, outputFn, exitFn)

	// Post-spawn fetch: closed_at detects a CloseTerminal that landed
	// during the PTY spawn (must neither broadcast READY nor leave a
	// running PTY behind), and title absorbs the value the frontend may
	// have persisted between OpenTerminal returning and StartTerminal
	// registering in-memory metadata. Single narrow query — avoids
	// re-reading the screen BLOB the handler entry already fetched.
	postSpawn, postSpawnErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID)
	if postSpawnErr == nil && postSpawn.ClosedAt.Valid {
		if startErr == nil {
			svc.Terminals.RemoveTerminal(terminalID)
		}
		svc.TerminalStartup.succeed(terminalID)
		svc.rollbackGitMode(gm)
		return
	}

	if startErr != nil {
		slog.Error("failed to start terminal", "terminal_id", terminalID, "error", startErr)
		svc.failTerminalStartup(terminalID, gm, startErr)
		return
	}

	if postSpawnErr == nil && postSpawn.Title != "" {
		svc.Terminals.UpdateTitle(terminalID, postSpawn.Title)
	}

	// Apply any ResizeTerminal that arrived after the pre-spawn wait
	// above (e.g. the frontend's fit() was unusually slow, or a second
	// resize has since landed). The PTY is already the correct size for
	// the pre-wait case; this handler covers the rare late-arriving dims.
	if cols, rows, ok := svc.TerminalStartup.takePendingResize(terminalID); ok {
		if err := svc.Terminals.Resize(terminalID, cols, rows); err != nil {
			slog.Warn("apply pending resize after startup", "terminal_id", terminalID, "error", err)
		}
	}

	// Spawn succeeded and no close-race; hand cleanup ownership to the
	// eventual CloseTerminal handler.
	ownsIPCToken = false
	svc.succeedTerminalStartup(terminalID)
}

// runTerminalRestart is the async body of RestartTerminal: it spawns a
// new PTY through Manager.RestartTerminal and broadcasts READY or
// STARTUP_FAILED depending on the outcome. The handler seeded STARTING
// without git status; this goroutine re-broadcasts STARTING with the
// branch/origin once `git status` returns. No git-mode rollback path —
// restart never mutates worktrees. spawnInfo + the previous-token
// release run inside this goroutine so a slow RemoteIPC factory
// doesn't stretch the RPC latency the user sees.
func (svc *Context) runTerminalRestart(
	ctx context.Context,
	opts terminal.Options,
	spawnInfo TerminalSpawnInfo,
	fallbackOffset int64,
	outputFn terminal.OutputHandler,
	exitFn terminal.ExitHandler,
) {
	defer svc.TerminalStartup.finish()
	terminalID := opts.ID

	// Release the previous spawn's token before minting a fresh one.
	// The explicit call retires the *old* token; the deferred cleanup
	// retires the *new* token if the spawn never reaches READY. When
	// no new token was minted (RemoteIPC disabled / factory failed),
	// the defer skips the mutex roundtrip.
	svc.runTerminalCleanup(terminalID)
	opts.ExtraEnv = svc.spawnRemoteIPC("terminal", terminalID, "restart", svc.registerTerminalCleanup, func() ([]string, func(), error) {
		return svc.RemoteIPC.TerminalSpawning(spawnInfo)
	})
	ownsIPCToken := opts.ExtraEnv != nil
	defer func() {
		if ownsIPCToken {
			svc.runTerminalCleanup(terminalID)
		}
	}()

	// Phase 1: fetch git status off the RPC goroutine and re-broadcast
	// STARTING with branch/origin attached. Working dir is stable across
	// restart (no git-mode mutation), so a single re-broadcast is enough.
	gs := gitutil.GetGitStatus(ctx, gitutil.ResolveGitDir(opts.ShellStartDir, opts.WorkingDir))
	shellMsg := terminalStartingLabel(opts.Shell)
	svc.TerminalStartup.setMessage(terminalID, shellMsg)
	svc.broadcastTerminalStarting(terminalID, shellMsg, gs)

	startErr := svc.Terminals.RestartTerminal(ctx, opts, fallbackOffset, outputFn, exitFn)

	// Detect a CloseTerminal that landed during the PTY spawn: if
	// closed_at is set we must neither broadcast READY nor leave a
	// freshly-spawned PTY behind. Re-fetching is best-effort — a
	// CloseTerminal whose DB write hasn't committed yet will be caught
	// by its own RemoveTerminal call instead. The title column from the
	// same row is unused on the restart path (titles don't change
	// across restart), so it's read-and-discarded.
	if postSpawn, fetchErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID); fetchErr == nil && postSpawn.ClosedAt.Valid {
		if startErr == nil {
			svc.Terminals.RemoveTerminal(terminalID)
		}
		svc.TerminalStartup.succeed(terminalID)
		return
	}

	if startErr != nil {
		slog.Error("failed to restart terminal", "terminal_id", terminalID, "error", startErr)
		// No git-mode mutation in the restart path — pass a zero result so
		// failTerminalStartup skips the rollback branch.
		svc.failTerminalStartup(terminalID, gitModeResult{}, startErr)
		return
	}

	ownsIPCToken = false
	svc.succeedTerminalStartup(terminalID)
}

// succeedTerminalStartup is the shared READY tail for runTerminalStartup
// and runTerminalRestart: clear the persisted startup_error, broadcast
// READY, and mark the registry succeeded last so observers see a durable
// terminal state.
func (svc *Context) succeedTerminalStartup(terminalID string) {
	svc.persistTerminalStartupError(terminalID, "")
	svc.broadcastTerminalReady(terminalID)
	svc.TerminalStartup.succeed(terminalID)
}

// runTerminalPhase0 broadcasts the per-mode label and executes the
// git-mode mutation.
func (svc *Context) runTerminalPhase0(ctx context.Context, terminalID string, plan gitModePlan) (gitModeResult, error) {
	return svc.runStartupPhase0(ctx, plan, svc.terminalStartupCallbacks(terminalID))
}

// failTerminalStartup is the common tail for every failure after the sync
// prologue: rolls back any partial git-mode mutation, persists the
// error, broadcasts STARTUP_FAILED, and marks the registry failed. The
// shared `failStartup` enforces the ordering (DB before broadcast
// before registry) so observers see a durable terminal state.
func (svc *Context) failTerminalStartup(terminalID string, gm gitModeResult, cause error) {
	svc.failStartup(gm, cause, svc.terminalStartupCallbacks(terminalID))
}

// persistTerminalStartupError writes (or clears when errMsg is "") the
// terminals.startup_error column so the startup panel survives a worker
// restart that wipes the in-memory registry.
func (svc *Context) persistTerminalStartupError(terminalID, errMsg string) {
	if err := svc.Queries.SetTerminalStartupError(bgCtx(), db.SetTerminalStartupErrorParams{
		StartupError: errMsg,
		ID:           terminalID,
	}); err != nil {
		action := "persist"
		if errMsg == "" {
			action = "clear"
		}
		slog.Warn("failed to "+action+" terminal startup error", "terminal_id", terminalID, "error", err)
	}
}

// buildTerminalStartingStatus builds a STARTING TerminalStatusChange
// carrying the current phase label. gs is nil for phases before git
// status has been computed (phase 0 mode labels, rollback labels, the
// seed broadcast from registerTerminalHandlers) and non-nil once
// runTerminalStartup's phase 1 has run `git status` on the final dir.
func buildTerminalStartingStatus(terminalID, message string, gs *leapmuxv1.AgentGitStatus) *leapmuxv1.TerminalStatusChange {
	sc := &leapmuxv1.TerminalStatusChange{
		TerminalId:     terminalID,
		Status:         leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		StartupMessage: message,
	}
	if gs != nil {
		sc.GitBranch = gs.GetBranch()
		sc.GitOriginUrl = gs.GetOriginUrl()
		sc.GitToplevel = gs.GetToplevel()
		sc.GitIsWorktree = gs.GetIsWorktree()
	}
	return sc
}

// buildTerminalFailedStatus builds a STARTUP_FAILED TerminalStatusChange
// carrying the error message.
func buildTerminalFailedStatus(terminalID, errMsg string) *leapmuxv1.TerminalStatusChange {
	return &leapmuxv1.TerminalStatusChange{
		TerminalId:   terminalID,
		Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
		StartupError: errMsg,
	}
}

// buildTerminalReadyStatus builds a READY TerminalStatusChange.
func buildTerminalReadyStatus(terminalID string) *leapmuxv1.TerminalStatusChange {
	return &leapmuxv1.TerminalStatusChange{
		TerminalId: terminalID,
		Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
	}
}

// deriveTerminalStatus computes (status, startupError, startupMessage)
// for a terminal, in priority order:
//  1. in-memory startup registry — STARTING / STARTUP_FAILED while a
//     startup is in flight or has just failed. The current phase
//     message is surfaced so a WatchEvents subscriber that arrived
//     after the initial STARTING broadcast still sees the right label.
//  2. persisted startup_error column — surfaces a prior failure across
//     worker restarts (the in-memory registry is wiped on restart).
//  3. READY otherwise (the caller uses `Exited` to distinguish a
//     running terminal from an exited one).
func (svc *Context) deriveTerminalStatus(t *db.Terminal) (status leapmuxv1.TerminalStatus, startupError, startupMessage string) {
	if sup, errStr, msg, ok := svc.TerminalStartup.status(t.ID); ok {
		return sup, errStr, msg
	}
	if t.StartupError != "" {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, t.StartupError, ""
	}
	return leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, "", ""
}

// broadcastTerminalStarting fans out a STARTING TerminalStatusChange.
// Used by runTerminalStartup for each phase label transition; gs is
// non-nil only once phase 1 has computed git status.
func (svc *Context) broadcastTerminalStarting(terminalID, message string, gs *leapmuxv1.AgentGitStatus) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalStartingStatus(terminalID, message, gs),
		},
	})
}

// broadcastTerminalFailed fans out a STARTUP_FAILED TerminalStatusChange.
func (svc *Context) broadcastTerminalFailed(terminalID, errMsg string) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalFailedStatus(terminalID, errMsg),
		},
	})
}

// broadcastTerminalReady fans out a READY TerminalStatusChange.
func (svc *Context) broadcastTerminalReady(terminalID string) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalReadyStatus(terminalID),
		},
	})
}

// makeTerminalOutputFn builds the OutputHandler closure that broadcasts
// data events to subscribers and pings the wake lock.
func (svc *Context) makeTerminalOutputFn(terminalID string) terminal.OutputHandler {
	return func(data []byte, endOffset int64) {
		if svc.WakeLock != nil {
			svc.WakeLock.RecordActivity()
		}
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Data{
				Data: &leapmuxv1.TerminalData{
					Data:      data,
					EndOffset: endOffset,
				},
			},
		})
	}
}

// makeTerminalExitFn builds the ExitHandler that runs when the shell
// process exits: append the "Press Enter to restart" notice, persist
// the final screen + metadata to the DB so a worker restart still finds
// an exited row, and broadcast TerminalClosed. Does not set closed_at —
// only explicit user close does that.
func (svc *Context) makeTerminalExitFn() terminal.ExitHandler {
	return func(tid string, exitCode int) {
		svc.persistTerminalOnExit(tid, exitCode)
		svc.Watchers.BroadcastTerminalEvent(tid, &leapmuxv1.TerminalEvent{
			TerminalId: tid,
			Event: &leapmuxv1.TerminalEvent_Closed{
				Closed: &leapmuxv1.TerminalClosed{
					ExitCode: int32(exitCode),
				},
			},
		})
	}
}

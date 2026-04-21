package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// terminalStartingLabel returns the "Starting <shell>…" label used for the
// sync prologue broadcast and the phase-1 re-broadcast once git status is
// in hand. Kept in one place so both call sites stay in lockstep.
func terminalStartingLabel(shell string) string {
	return "Starting " + filepath.Base(shell) + "…"
}

// registerTerminalHandlers registers all terminal-related RPC handlers.
func registerTerminalHandlers(d *channel.Dispatcher, svc *Context) {
	// OpenTerminal starts a new PTY terminal session.
	d.Register("OpenTerminal", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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
		plan, gmErr := svc.validateGitMode(workingDir, &r)
		if gmErr != nil {
			sendInvalidArgument(sender, gmErr.Error())
			return
		}

		terminalID := id.Generate()

		outputFn := func(data []byte) {
			if svc.WakeLock != nil {
				svc.WakeLock.RecordActivity()
			}
			svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
				TerminalId: terminalID,
				Event: &leapmuxv1.TerminalEvent_Data{
					Data: &leapmuxv1.TerminalData{
						Data: data,
					},
				},
			})
		}
		exitFn := func(tid string, exitCode int) {
			svc.appendTerminalDisconnectNotice(tid)

			// Persist the screen buffer and mark as inactive before
			// broadcasting the close event, so it can be restored if the
			// frontend reconnects later. Do not set closed_at here —
			// only explicit user close sets closed_at.
			if snap, ok := svc.Terminals.SnapshotTerminal(tid); ok {
				if err := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
					ID:            tid,
					WorkspaceID:   snap.WorkspaceID,
					WorkingDir:    plan.PlannedWorkingDir,
					HomeDir:       svc.HomeDir,
					ShellStartDir: shellStartDir,
					Title:         snap.Title,
					Cols:          int64(snap.Cols),
					Rows:          int64(snap.Rows),
					Screen:        appendTerminalDisconnectNotice(snap.Screen),
					ExitCode:      int64(exitCode),
				}); err != nil {
					slog.Error("failed to save terminal on exit", "terminal_id", tid, "error", err)
				}
			} else if meta, hasMeta := svc.Terminals.GetMeta(tid); hasMeta {
				// No screen available — still persist metadata (title, etc.)
				if err := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
					ID:            tid,
					WorkspaceID:   meta.WorkspaceID,
					WorkingDir:    plan.PlannedWorkingDir,
					HomeDir:       svc.HomeDir,
					ShellStartDir: shellStartDir,
					Title:         meta.Title,
					Cols:          int64(meta.Cols),
					Rows:          int64(meta.Rows),
					Screen:        appendTerminalDisconnectNotice(nil),
					ExitCode:      int64(exitCode),
				}); err != nil {
					slog.Error("failed to save terminal metadata on exit", "terminal_id", tid, "error", err)
				}
			}

			svc.Watchers.BroadcastTerminalEvent(tid, &leapmuxv1.TerminalEvent{
				TerminalId: tid,
				Event: &leapmuxv1.TerminalEvent_Closed{
					Closed: &leapmuxv1.TerminalClosed{
						ExitCode: int32(exitCode),
					},
				},
			})
		}

		// Persist the initial terminal record using the planned working
		// dir, so tab sync and post-refresh reads see the eventual path
		// even before git-mode execution creates the worktree.
		if upsertErr := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
			ID:            terminalID,
			WorkspaceID:   workspaceID,
			WorkingDir:    plan.PlannedWorkingDir,
			HomeDir:       svc.HomeDir,
			ShellStartDir: shellStartDir,
			Cols:          int64(cols),
			Rows:          int64(rows),
			Screen:        []byte{},
		}); upsertErr != nil {
			slog.Error("failed to persist terminal record", "terminal_id", terminalID, "error", upsertErr)
			sendInternalError(sender, "failed to persist terminal")
			return
		}

		// Register the startup in the registry with a cancel ctx so
		// CloseTerminal during phase 0 aborts executeGitMode.
		startupCtx, cancel := context.WithCancel(context.Background())
		svc.TerminalStartup.begin(terminalID, cancel)

		// Seed a STARTING broadcast with the provider label. Phase 0
		// will overwrite the message with a mode-specific label (e.g.
		// `Creating worktree "feature/x"…`) before mutation begins.
		startupMessage := terminalStartingLabel(shell)
		svc.TerminalStartup.setMessage(terminalID, startupMessage)
		svc.broadcastTerminalStarting(terminalID, startupMessage, "", "")

		sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{
			TerminalId: terminalID,
		})

		// Kick off git-mode execution + PTY spawn in the background.
		go svc.runTerminalStartup(startupCtx, terminal.Options{
			ID:            terminalID,
			WorkspaceID:   workspaceID,
			Shell:         shell,
			WorkingDir:    plan.PlannedWorkingDir,
			ShellStartDir: shellStartDir,
			Cols:          uint16(cols),
			Rows:          uint16(rows),
		}, plan, outputFn, exitFn)
	})

	// CloseTerminal stops and removes a terminal session.
	d.Register("CloseTerminal", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseTerminalRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		if _, ok := svc.requireAccessibleTerminal(sender, terminalID); !ok {
			return
		}

		// Clear any in-flight startup entry.
		svc.TerminalStartup.cancelAndClear(terminalID)

		svc.Terminals.RemoveTerminal(terminalID)

		// Soft-delete the terminal record.
		_ = svc.Queries.CloseTerminal(bgCtx(), terminalID)

		svc.unregisterTabAndCleanup(leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)
		sendProtoResponse(sender, &leapmuxv1.CloseTerminalResponse{})
	})

	// SendInput sends input data to a terminal.
	d.Register("SendInput", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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
	d.Register("ResizeTerminal", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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
				slog.Error("failed to resize terminal", "terminal_id", terminalID, "error", err)
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
	// manager and the database. The frontend debounces calls at 10s intervals.
	d.Register("UpdateTerminalTitle", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		// Persist to DB so it survives restarts.
		_ = svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
			ID:            dbTerm.ID,
			WorkspaceID:   dbTerm.WorkspaceID,
			WorkingDir:    dbTerm.WorkingDir,
			HomeDir:       dbTerm.HomeDir,
			ShellStartDir: dbTerm.ShellStartDir,
			Title:         title,
			Cols:          dbTerm.Cols,
			Rows:          dbTerm.Rows,
			Screen:        dbTerm.Screen,
			ExitCode:      dbTerm.ExitCode,
			ClosedAt:      dbTerm.ClosedAt,
		})

		sendProtoResponse(sender, &leapmuxv1.UpdateTerminalTitleResponse{})
	})

	// ListTerminals returns all terminal tabs for a workspace.
	// Uses the in-memory terminal manager for running terminals and falls
	// back to saved terminal records for terminals that have already exited
	// and been removed from the manager.
	d.Register("ListTerminals", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		// Filter by access control: only return terminals in accessible workspaces.
		var accessibleWsIDs map[string]bool
		if chID := sender.ChannelID(); chID != "" {
			accessibleWsIDs = svc.Channels.AccessibleWorkspaceIDs(chID)
		}

		// Collect from the in-memory manager and DB-only rows, recording
		// each terminal's resolved git directory (ShellStartDir, falling
		// back to WorkingDir) so BatchGetGitStatus can dedupe across
		// terminals that share a repo.
		entries := svc.Terminals.ListByIDs(tabIDs)
		seen := make(map[string]bool, len(entries))
		var terminals []*leapmuxv1.TerminalInfo
		var gitDirs []string
		resolveGitDir := func(shellStartDir, workingDir string) string {
			if shellStartDir != "" {
				return shellStartDir
			}
			return workingDir
		}
		for _, e := range entries {
			if accessibleWsIDs != nil && !accessibleWsIDs[e.Meta.WorkspaceID] {
				continue
			}
			seen[e.ID] = true
			ti := &leapmuxv1.TerminalInfo{
				TerminalId:    e.ID,
				Cols:          e.Meta.Cols,
				Rows:          e.Meta.Rows,
				Screen:        e.Screen,
				Exited:        e.Exited,
				WorkingDir:    e.Meta.WorkingDir,
				ShellStartDir: e.Meta.ShellStartDir,
				Title:         e.Meta.Title,
				Status:        leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
			}
			if sup, errStr, msg, ok := svc.TerminalStartup.status(e.ID); ok {
				ti.Status = sup
				ti.StartupError = errStr
				ti.StartupMessage = msg
			}
			terminals = append(terminals, ti)
			gitDirs = append(gitDirs, resolveGitDir(e.Meta.ShellStartDir, e.Meta.WorkingDir))
		}

		dbTerminals, err := svc.Queries.ListTerminalsByIDs(bgCtx(), tabIDs)
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
				ti := &leapmuxv1.TerminalInfo{
					TerminalId:     ts.ID,
					Cols:           uint32(ts.Cols),
					Rows:           uint32(ts.Rows),
					Screen:         ts.Screen,
					Exited:         !svc.Terminals.HasTerminal(ts.ID),
					WorkingDir:     ts.WorkingDir,
					ShellStartDir:  ts.ShellStartDir,
					Title:          ts.Title,
					Status:         status,
					StartupError:   startupError,
					StartupMessage: startupMessage,
				}
				terminals = append(terminals, ti)
				gitDirs = append(gitDirs, resolveGitDir(ts.ShellStartDir, ts.WorkingDir))
			}
		}

		gitStatuses := gitutil.BatchGetGitStatus(bgCtx(), gitDirs)
		for i, gs := range gitStatuses {
			if gs != nil {
				terminals[i].GitBranch = gs.Branch
				terminals[i].GitOriginUrl = gs.OriginUrl
			}
		}

		sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{
			Terminals: terminals,
		})
	})

	// ListAvailableShells returns the shells installed on this worker.
	d.Register("ListAvailableShells", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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
func (svc *Context) runTerminalStartup(ctx context.Context, opts terminal.Options, plan gitModePlan, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler) {
	defer svc.TerminalStartup.finish()
	terminalID := opts.ID

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
	// freshly-created worktree). The resulting branch/origin travels on
	// the "Starting <shell>…" broadcast so the frontend can populate the
	// tab's gitBranch / gitOriginUrl without an extra round-trip.
	gitDir := opts.ShellStartDir
	if gitDir == "" {
		gitDir = opts.WorkingDir
	}
	var gitBranch, gitOriginURL string
	if gs := gitutil.GetGitStatus(ctx, gitDir); gs != nil {
		gitBranch = gs.Branch
		gitOriginURL = gs.OriginUrl
	}
	shellMsg := terminalStartingLabel(opts.Shell)
	svc.TerminalStartup.setMessage(terminalID, shellMsg)
	svc.broadcastTerminalStarting(terminalID, shellMsg, gitBranch, gitOriginURL)

	startErr := svc.startTerminal(ctx, opts, outputFn, exitFn)

	// Re-read the row so we can detect whether CloseTerminal landed
	// during the PTY spawn: if closed_at is set, the user already asked
	// to close the tab and runTerminalStartup must neither broadcast
	// READY nor leave a running PTY behind. Mirror of the post-spawn
	// close-detection in runAgentStartup.
	if dbTerm, fetchErr := svc.Queries.GetTerminal(bgCtx(), terminalID); fetchErr == nil && dbTerm.ClosedAt.Valid {
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

	// Apply any ResizeTerminal that arrived during the PTY-spawn window.
	// The PTY was created with the placeholder cols/rows from the
	// OpenTerminal request; the frontend's first fit() fires long before
	// the Manager holds the terminal, so its RPC is stashed in the
	// startup registry and drained here.
	if cols, rows, ok := svc.TerminalStartup.takePendingResize(terminalID); ok {
		if err := svc.Terminals.Resize(terminalID, cols, rows); err != nil {
			slog.Warn("apply pending resize after startup", "terminal_id", terminalID, "error", err)
		}
	}

	svc.persistTerminalStartupError(terminalID, "")
	svc.broadcastTerminalReady(terminalID)
	svc.TerminalStartup.succeed(terminalID)
}

// runTerminalPhase0 broadcasts the per-mode label and executes the
// git-mode mutation.
func (svc *Context) runTerminalPhase0(ctx context.Context, terminalID string, plan gitModePlan) (gitModeResult, error) {
	if label := plan.PhaseLabel(); label != "" {
		svc.TerminalStartup.setMessage(terminalID, label)
		svc.broadcastTerminalStarting(terminalID, label, "", "")
	}
	return svc.executeGitMode(ctx, plan)
}

// failTerminalStartup is the common tail for every failure after the sync
// prologue: optionally show a rollback label, roll back any partial
// git-mode mutation, persist the error, broadcast STARTUP_FAILED, and mark
// the registry failed last so observers see a durable terminal state.
func (svc *Context) failTerminalStartup(terminalID string, gm gitModeResult, cause error) {
	if gm.Rollback.HasPartialMutation() {
		if label := rollbackLabelFromRollback(gm.Rollback); label != "" {
			svc.TerminalStartup.setMessage(terminalID, label)
			svc.broadcastTerminalStarting(terminalID, label, "", "")
		}
		svc.rollbackGitMode(gm)
	}
	errMsg := cause.Error()
	svc.persistTerminalStartupError(terminalID, errMsg)
	svc.broadcastTerminalFailed(terminalID, errMsg)
	svc.TerminalStartup.fail(terminalID, errMsg)
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
// carrying the current phase label. gitBranch and gitOriginURL are
// populated once phase 1 finishes computing them; earlier phases pass
// empty strings.
func buildTerminalStartingStatus(terminalID, message, gitBranch, gitOriginURL string) *leapmuxv1.TerminalStatusChange {
	return &leapmuxv1.TerminalStatusChange{
		TerminalId:     terminalID,
		Status:         leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		StartupMessage: message,
		GitBranch:      gitBranch,
		GitOriginUrl:   gitOriginURL,
	}
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
// Used by runTerminalStartup for each phase label transition; gitBranch
// and gitOriginURL are passed once phase 1 has computed them.
func (svc *Context) broadcastTerminalStarting(terminalID, message, gitBranch, gitOriginURL string) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalStartingStatus(terminalID, message, gitBranch, gitOriginURL),
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

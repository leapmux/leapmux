package service

import (
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

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
			rows = 24
		}

		shell := r.GetShell()
		shellStartDir := expandTilde(r.GetShellStartDir())
		workingDir := expandTilde(r.GetWorkingDir())
		if workingDir == "" {
			workingDir = svc.HomeDir
		}

		// Apply git-mode options (create-worktree, checkout-branch, etc.).
		// Kept sync so git errors surface as immediate RPC errors.
		gm, gmErr := svc.applyGitMode(workingDir, &r)
		if gmErr != nil {
			slog.Error("failed to apply git mode for terminal", "error", gmErr)
			sendInternalError(sender, gmErr.Error())
			return
		}
		syncSucceeded := false
		defer func() {
			if !syncSucceeded {
				svc.rollbackGitMode(gm)
			}
		}()
		workingDir = gm.WorkingDir
		worktreeID := gm.WorktreeID

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
					WorkingDir:    workingDir,
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
					WorkingDir:    workingDir,
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

		// Persist the initial terminal record so tab sync works
		// even before the terminal starts up.
		if upsertErr := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
			ID:            terminalID,
			WorkspaceID:   workspaceID,
			WorkingDir:    workingDir,
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

		// Register the terminal tab with the worktree.
		svc.registerTabForWorktree(worktreeID, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)

		// Register the startup in the registry (no cancel plumbing for
		// terminals — PTY spawn is synchronous and fast, but we still
		// report STARTING/READY/STARTUP_FAILED so the frontend can gate
		// xterm mount and show a loader in the interim).
		svc.Startup.begin(terminalID, startupKindTerminal, func() {})

		// Broadcast STARTING so the frontend watcher sees the initial state.
		svc.broadcastTerminalStartupStatus(terminalID, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, "")

		resp := &leapmuxv1.OpenTerminalResponse{
			TerminalId: terminalID,
		}
		gitDir := shellStartDir
		if gitDir == "" {
			gitDir = workingDir
		}
		if gs := gitutil.GetGitStatus(gitDir); gs != nil {
			resp.GitBranch = gs.Branch
			resp.GitOriginUrl = gs.OriginUrl
		}
		syncSucceeded = true
		sendProtoResponse(sender, resp)

		// Kick off PTY spawn in the background.
		go svc.runTerminalStartup(terminalID, workspaceID, shell, workingDir, shellStartDir, cols, rows, worktreeID, gm, outputFn, exitFn)
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
		svc.Startup.cancelAndClear(terminalID)

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

		if err := svc.Terminals.Resize(terminalID, uint16(cols), uint16(rows)); err != nil {
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

		// Collect running/exited terminals from the in-memory manager.
		entries := svc.Terminals.ListByIDs(tabIDs)
		seen := make(map[string]bool, len(entries))
		var terminals []*leapmuxv1.TerminalInfo
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
			if sup, errStr, ok := svc.Startup.terminalStatus(e.ID); ok {
				ti.Status = sup
				ti.StartupError = errStr
			}
			populateTerminalGitInfo(ti, e.Meta.ShellStartDir)
			terminals = append(terminals, ti)
		}

		// Also include terminals from the DB that have been removed from
		// the manager (e.g. exited + cleaned up) but still have saved data.
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
				ti := &leapmuxv1.TerminalInfo{
					TerminalId:    ts.ID,
					Cols:          uint32(ts.Cols),
					Rows:          uint32(ts.Rows),
					Screen:        ts.Screen,
					Exited:        !svc.Terminals.HasTerminal(ts.ID),
					WorkingDir:    ts.WorkingDir,
					ShellStartDir: ts.ShellStartDir,
					Title:         ts.Title,
					Status:        leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
				}
				if sup, errStr, ok := svc.Startup.terminalStatus(ts.ID); ok {
					ti.Status = sup
					ti.StartupError = errStr
				}
				populateTerminalGitInfo(ti, ts.ShellStartDir)
				terminals = append(terminals, ti)
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

// runTerminalStartup is the async body of OpenTerminal: it spawns the PTY
// and reports READY or STARTUP_FAILED to the frontend. On failure it also
// rolls back any git-mode side effects and removes the DB row.
func (svc *Context) runTerminalStartup(terminalID, workspaceID, shell, workingDir, shellStartDir string, cols, rows uint32, worktreeID string, gm gitModeResult, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler) {
	_ = worktreeID // worktreeID is already registered on the sync path; kept in signature for future diagnostics
	err := svc.startTerminal(terminal.Options{
		ID:            terminalID,
		WorkspaceID:   workspaceID,
		Shell:         shell,
		WorkingDir:    workingDir,
		ShellStartDir: shellStartDir,
		Cols:          uint16(cols),
		Rows:          uint16(rows),
	}, outputFn, exitFn)
	if err != nil {
		errMsg := err.Error()
		slog.Error("failed to start terminal", "terminal_id", terminalID, "error", errMsg)
		// Keep the terminal row open so the in-tab error UI remains
		// reachable across page refreshes; the user dismisses via the
		// Close-tab button which calls CloseTerminal. Rollback git-mode
		// changes immediately though (worktree creation can be expensive).
		svc.rollbackGitMode(gm)
		svc.Startup.fail(terminalID, errMsg)
		svc.broadcastTerminalStartupStatus(terminalID, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, errMsg)
		return
	}
	svc.Startup.succeed(terminalID)
	svc.broadcastTerminalStartupStatus(terminalID, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, "")
}

// broadcastTerminalStartupStatus emits a TerminalStatusChange event.
func (svc *Context) broadcastTerminalStartupStatus(terminalID string, status leapmuxv1.TerminalStatus, startupError string) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: &leapmuxv1.TerminalStatusChange{
				TerminalId:   terminalID,
				Status:       status,
				StartupError: startupError,
			},
		},
	})
}

// populateTerminalGitInfo fills in the git branch and origin URL fields on a TerminalInfo
// from the terminal's shell start directory. Best-effort: fields are left empty if the
// directory is not inside a git repo.
func populateTerminalGitInfo(ti *leapmuxv1.TerminalInfo, shellStartDir string) {
	dir := shellStartDir
	if dir == "" {
		dir = ti.WorkingDir
	}
	if dir == "" {
		return
	}
	gs := gitutil.GetGitStatus(dir)
	if gs != nil {
		ti.GitBranch = gs.Branch
		ti.GitOriginUrl = gs.OriginUrl
	}
}

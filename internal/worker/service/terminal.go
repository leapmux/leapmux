package service

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
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

		// Create a worktree if requested.
		var worktreeID string
		if r.GetCreateWorktree() {
			finalDir, wtID, wtErr := svc.createWorktreeIfRequested(
				workingDir, true, r.GetWorktreeBranch(),
			)
			if wtErr != nil {
				slog.Error("failed to create worktree for terminal",
					"error", wtErr)
				sendInternalError(sender, "failed to create worktree: "+wtErr.Error())
				return
			}
			workingDir = finalDir
			worktreeID = wtID
		}

		terminalID := id.Generate()

		outputFn := func(data []byte) {
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
					Cols:          int64(snap.Cols),
					Rows:          int64(snap.Rows),
					Screen:        snap.Screen,
					ExitCode:      int64(exitCode),
				}); err != nil {
					slog.Error("failed to save terminal on exit", "terminal_id", tid, "error", err)
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

		err := svc.Terminals.StartTerminal(terminal.Options{
			ID:            terminalID,
			WorkspaceID:   workspaceID,
			Shell:         shell,
			WorkingDir:    workingDir,
			ShellStartDir: shellStartDir,
			Cols:          uint16(cols),
			Rows:          uint16(rows),
		}, outputFn, exitFn)
		if err != nil {
			slog.Error("failed to start terminal", "error", err)
			sendInternalError(sender, "failed to start terminal")
			return
		}

		// Persist the initial terminal record so tab sync works
		// even before the terminal exits.
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
		}

		// Register the terminal tab with the worktree.
		svc.registerTabForWorktree(worktreeID, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)

		sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{
			TerminalId: terminalID,
		})
	})

	// CloseTerminal stops and removes a terminal session.
	d.Register("CloseTerminal", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseTerminalRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		if terminalID == "" {
			sendInvalidArgument(sender, "terminal_id is required")
			return
		}

		svc.Terminals.RemoveTerminal(terminalID)

		// Soft-delete the terminal record.
		_ = svc.Queries.CloseTerminal(bgCtx(), terminalID)

		// Handle worktree cleanup.
		cleanup := svc.unregisterTabAndCleanup(leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)
		sendProtoResponse(sender, &leapmuxv1.CloseTerminalResponse{
			WorktreeCleanupPending: cleanup.NeedsConfirmation,
			WorktreePath:           cleanup.WorktreePath,
			WorktreeId:             cleanup.WorktreeID,
		})
	})

	// SendInput sends input data to a terminal.
	d.Register("SendInput", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendInputRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		terminalID := r.GetTerminalId()
		if terminalID == "" {
			sendInvalidArgument(sender, "terminal_id is required")
			return
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
		if terminalID == "" {
			sendInvalidArgument(sender, "terminal_id is required")
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

		workspaceID := r.GetWorkspaceId()
		if workspaceID == "" {
			sendInvalidArgument(sender, "workspace_id is required")
			return
		}

		// Collect running/exited terminals from the in-memory manager.
		entries := svc.Terminals.ListByWorkspace(workspaceID)
		seen := make(map[string]bool, len(entries))
		var terminals []*leapmuxv1.TerminalInfo
		for _, e := range entries {
			seen[e.ID] = true
			terminals = append(terminals, &leapmuxv1.TerminalInfo{
				TerminalId:    e.ID,
				Cols:          e.Meta.Cols,
				Rows:          e.Meta.Rows,
				Screen:        e.Screen,
				Exited:        e.Exited,
				WorkingDir:    e.Meta.WorkingDir,
				ShellStartDir: e.Meta.ShellStartDir,
			})
		}

		// Also include terminals from the DB that have been removed from
		// the manager (e.g. exited + cleaned up) but still have saved data.
		dbTerminals, err := svc.Queries.ListTerminalsByWorkspace(bgCtx(), workspaceID)
		if err != nil {
			slog.Error("failed to list terminals from DB", "error", err)
		} else {
			for _, ts := range dbTerminals {
				if seen[ts.ID] {
					continue
				}
				terminals = append(terminals, &leapmuxv1.TerminalInfo{
					TerminalId:    ts.ID,
					Cols:          uint32(ts.Cols),
					Rows:          uint32(ts.Rows),
					Screen:        ts.Screen,
					Exited:        !svc.Terminals.HasTerminal(ts.ID),
					WorkingDir:    ts.WorkingDir,
					ShellStartDir: ts.ShellStartDir,
				})
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

		candidates := []string{
			"/bin/bash",
			"/bin/zsh",
			"/bin/sh",
			"/usr/bin/fish",
			"/bin/fish",
		}

		var shells []string
		for _, path := range candidates {
			if _, err := os.Stat(path); err == nil {
				shells = append(shells, path)
			}
		}

		defaultShell := os.Getenv("SHELL")
		if defaultShell == "" {
			defaultShell = "/bin/sh"
		}

		// Ensure default shell is in the list.
		found := false
		for _, s := range shells {
			if s == defaultShell {
				found = true
				break
			}
		}
		if !found {
			if _, err := os.Stat(defaultShell); err == nil {
				shells = append(shells, defaultShell)
			}
		}

		sendProtoResponse(sender, &leapmuxv1.ListAvailableShellsResponse{
			Shells:       shells,
			DefaultShell: filepath.Base(defaultShell),
		})
	})
}

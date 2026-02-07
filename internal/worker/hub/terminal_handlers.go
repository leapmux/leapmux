package hub

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

func (c *Client) handleTerminalStart(requestID string, req *leapmuxv1.TerminalStartRequest) {
	terminalID := req.GetTerminalId()
	workingDir := req.GetWorkingDir()

	resolvedDir, err := resolveWorkingDir(workingDir)
	if err != nil {
		slog.Error("failed to resolve working directory", "terminal_id", terminalID, "working_dir", workingDir, "error", err)
		_ = c.Send(&leapmuxv1.ConnectRequest{
			RequestId: requestID,
			Payload: &leapmuxv1.ConnectRequest_TerminalStarted{
				TerminalStarted: &leapmuxv1.TerminalStarted{
					TerminalId: terminalID,
					Error:      err.Error(),
				},
			},
		})
		return
	}

	slog.Info("starting terminal", "terminal_id", terminalID, "working_dir", resolvedDir)

	outputFn := func(data []byte) {
		// NOTE: Do NOT set RequestId here. TerminalOutput is fire-and-forget,
		// not a request-response pair.
		_ = c.Send(&leapmuxv1.ConnectRequest{
			Payload: &leapmuxv1.ConnectRequest_TerminalOutput{
				TerminalOutput: &leapmuxv1.TerminalOutput{
					TerminalId: terminalID,
					Data:       data,
				},
			},
		})
	}

	exitFn := func(id string, exitCode int) {
		slog.Info("sending terminal exited", "terminal_id", id, "exit_code", exitCode)
		_ = c.Send(&leapmuxv1.ConnectRequest{
			Payload: &leapmuxv1.ConnectRequest_TerminalExited{
				TerminalExited: &leapmuxv1.TerminalExited{
					TerminalId: id,
					ExitCode:   int32(exitCode),
				},
			},
		})
	}

	err = c.terminals.StartTerminal(terminal.Options{
		ID:         terminalID,
		Shell:      req.GetShell(),
		WorkingDir: resolvedDir,
		Cols:       uint16(req.GetCols()),
		Rows:       uint16(req.GetRows()),
	}, outputFn, exitFn)

	if err != nil {
		slog.Error("failed to start terminal", "terminal_id", terminalID, "error", err)
		_ = c.Send(&leapmuxv1.ConnectRequest{
			RequestId: requestID,
			Payload: &leapmuxv1.ConnectRequest_TerminalStarted{
				TerminalStarted: &leapmuxv1.TerminalStarted{
					TerminalId: terminalID,
					Error:      err.Error(),
				},
			},
		})
		return
	}

	c.mu.Lock()
	c.terminalWorkspaces[terminalID] = terminalMeta{
		workspaceID: req.GetWorkspaceId(),
		cols:        uint32(req.GetCols()),
		rows:        uint32(req.GetRows()),
	}
	c.mu.Unlock()

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_TerminalStarted{
			TerminalStarted: &leapmuxv1.TerminalStarted{
				TerminalId:         terminalID,
				ResolvedWorkingDir: resolvedDir,
			},
		},
	})
}

func (c *Client) handleTerminalInput(req *leapmuxv1.TerminalInput) {
	if err := c.terminals.SendInput(req.GetTerminalId(), req.GetData()); err != nil {
		slog.Warn("terminal input failed", "terminal_id", req.GetTerminalId(), "error", err)
	}
}

func (c *Client) handleTerminalResize(req *leapmuxv1.TerminalResizeRequest) {
	if err := c.terminals.Resize(req.GetTerminalId(), uint16(req.GetCols()), uint16(req.GetRows())); err != nil {
		slog.Warn("terminal resize failed", "terminal_id", req.GetTerminalId(), "error", err)
	}
}

func (c *Client) handleTerminalStop(req *leapmuxv1.TerminalStopRequest) {
	slog.Info("stopping terminal", "terminal_id", req.GetTerminalId())
	c.terminals.RemoveTerminal(req.GetTerminalId())

	c.mu.Lock()
	delete(c.terminalWorkspaces, req.GetTerminalId())
	c.mu.Unlock()
}

func (c *Client) handleTerminalList(requestID string, req *leapmuxv1.TerminalListRequest) {
	workspaceID := req.GetWorkspaceId()
	var entries []*leapmuxv1.TerminalListEntry

	c.mu.Lock()
	seen := make(map[string]bool)
	// Live terminals take precedence over saved ones.
	for termID, meta := range c.terminalWorkspaces {
		if meta.workspaceID != workspaceID {
			continue
		}
		if c.terminals.HasTerminal(termID) {
			seen[termID] = true
			entries = append(entries, &leapmuxv1.TerminalListEntry{
				TerminalId: termID,
				Cols:       meta.cols,
				Rows:       meta.rows,
				Screen:     c.terminals.ScreenSnapshot(termID),
				Exited:     c.terminals.IsExited(termID),
			})
		}
	}
	// Include saved terminals that don't have a live counterpart.
	for termID, st := range c.savedTerminals {
		if st.WorkspaceID != workspaceID || seen[termID] {
			continue
		}
		entries = append(entries, &leapmuxv1.TerminalListEntry{
			TerminalId: termID,
			Cols:       st.Cols,
			Rows:       st.Rows,
			Screen:     st.Screen,
			Exited:     true,
		})
	}
	c.mu.Unlock()

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_TerminalListResp{
			TerminalListResp: &leapmuxv1.TerminalListResponse{
				Terminals: entries,
			},
		},
	})
}

func (c *Client) handleShellList(requestID string) {
	shells, defaultShell := terminal.ListAvailableShells()
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ShellListResp{
			ShellListResp: &leapmuxv1.ShellListResponse{
				Shells:       shells,
				DefaultShell: defaultShell,
			},
		},
	})
}

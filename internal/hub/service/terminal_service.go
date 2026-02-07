package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/terminalmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// terminalInfo holds the routing info for a single terminal.
type terminalInfo struct {
	workspaceID string
	workerID    string
}

// TerminalService implements the TerminalServiceHandler interface.
// Terminals are stateless on the Hub -- the Worker owns the lifecycle.
// The Hub verifies workspace ownership and routes I/O.
type TerminalService struct {
	queries        *db.Queries
	workerMgr      *workermgr.Manager
	terminalMgr    *terminalmgr.Manager
	pending        *workermgr.PendingRequests
	worktreeHelper *WorktreeHelper

	mu             sync.RWMutex
	terminalRoutes map[string]terminalInfo // terminalID -> routing info
}

// NewTerminalService creates a new TerminalService.
func NewTerminalService(q *db.Queries, bm *workermgr.Manager, tm *terminalmgr.Manager, pr *workermgr.PendingRequests, wh *WorktreeHelper) *TerminalService {
	return &TerminalService{
		queries:        q,
		workerMgr:      bm,
		terminalMgr:    tm,
		pending:        pr,
		worktreeHelper: wh,
		terminalRoutes: make(map[string]terminalInfo),
	}
}

// verifyWorkspaceOwnership checks that the workspace exists in the given org and the user can see it.
func (s *TerminalService) verifyWorkspaceOwnership(ctx context.Context, user *auth.UserInfo, orgID, workspaceID string) (*db.Workspace, error) {
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace_id is required"))
	}

	return getVisibleWorkspace(ctx, s.queries, user, orgID, workspaceID)
}

// getTerminalConn looks up the worker for a tracked terminal and returns its connection.
func (s *TerminalService) getTerminalConn(terminalID string) (*workermgr.Conn, error) {
	s.mu.RLock()
	info, ok := s.terminalRoutes[terminalID]
	s.mu.RUnlock()

	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("terminal not tracked"))
	}

	conn := s.workerMgr.Get(info.workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}

	return conn, nil
}

func (s *TerminalService) OpenTerminal(
	ctx context.Context,
	req *connect.Request[leapmuxv1.OpenTerminalRequest],
) (*connect.Response[leapmuxv1.OpenTerminalResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.verifyWorkspaceOwnership(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}
	if s.workerMgr.IsDeregistering(workerID) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is being deregistered"))
	}

	// Create worktree if requested (before terminal start so failures are clean).
	workingDir := req.Msg.GetWorkingDir()
	var worktreeID string
	if req.Msg.GetCreateWorktree() {
		var wtErr error
		workingDir, worktreeID, wtErr = s.worktreeHelper.CreateWorktreeIfRequested(
			ctx, workerID, workingDir, true, req.Msg.GetWorktreeBranch(),
		)
		if wtErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("create worktree: %w", wtErr))
		}
	}

	terminalID := id.Generate()
	cols := req.Msg.GetCols()
	rows := req.Msg.GetRows()
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	// Tell the worker to start the terminal and wait for a response.
	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_TerminalStart{
			TerminalStart: &leapmuxv1.TerminalStartRequest{
				TerminalId:  terminalID,
				Cols:        cols,
				Rows:        rows,
				WorkingDir:  workingDir,
				Shell:       req.Msg.GetShell(),
				WorkspaceId: req.Msg.GetWorkspaceId(),
			},
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send terminal start: %w", err))
	}

	// Check if the worker returned an error (e.g. invalid working directory).
	if errMsg := resp.GetTerminalStarted().GetError(); errMsg != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", errMsg))
	}

	s.trackTerminal(terminalID, req.Msg.GetWorkspaceId(), workerID)

	// Register tab for worktree tracking (after successful terminal start).
	s.worktreeHelper.RegisterTabForWorktree(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)

	return connect.NewResponse(&leapmuxv1.OpenTerminalResponse{
		TerminalId: terminalID,
	}), nil
}

func (s *TerminalService) CloseTerminal(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CloseTerminalRequest],
) (*connect.Response[leapmuxv1.CloseTerminalResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.verifyWorkspaceOwnership(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	terminalID := req.Msg.GetTerminalId()

	conn, err := s.getTerminalConn(terminalID)
	if err != nil {
		return nil, err
	}

	// Tell the worker to stop the terminal.
	_ = conn.Send(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_TerminalStop{
			TerminalStop: &leapmuxv1.TerminalStopRequest{
				TerminalId: terminalID,
			},
		},
	})

	s.untrackTerminal(terminalID)

	// Remove the persisted tab so it doesn't reappear on page reload.
	if err := s.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
		WorkspaceID: req.Msg.GetWorkspaceId(),
		TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabID:       terminalID,
	}); err != nil {
		slog.Warn("failed to delete workspace tab", "terminal_id", terminalID, "workspace_id", req.Msg.GetWorkspaceId(), "error", err)
	}

	// Notify watchers.
	s.terminalMgr.Broadcast(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_Closed{
			Closed: &leapmuxv1.TerminalClosed{},
		},
	})

	// Handle worktree cleanup.
	result := s.worktreeHelper.UnregisterTabAndCleanup(ctx, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)
	resp := &leapmuxv1.CloseTerminalResponse{}
	if result.NeedsConfirmation {
		resp.WorktreeCleanupPending = true
		resp.WorktreePath = result.WorktreePath
		resp.WorktreeId = result.WorktreeID
	}

	return connect.NewResponse(resp), nil
}

// CloseTerminalInternal closes a terminal without RPC auth checks.
// Used by WorkspaceService when cleaning up tiles.
func (s *TerminalService) CloseTerminalInternal(ctx context.Context, conn *workermgr.Conn, terminalID string) {
	if conn != nil {
		_ = conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_TerminalStop{
				TerminalStop: &leapmuxv1.TerminalStopRequest{
					TerminalId: terminalID,
				},
			},
		})
	}

	s.untrackTerminal(terminalID)

	s.terminalMgr.Broadcast(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_Closed{
			Closed: &leapmuxv1.TerminalClosed{},
		},
	})

	// Best-effort worktree cleanup (no user confirmation in internal close).
	s.worktreeHelper.UnregisterTabBestEffort(ctx, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)
}

func (s *TerminalService) SendInput(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SendInputRequest],
) (*connect.Response[leapmuxv1.SendInputResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.verifyWorkspaceOwnership(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	conn, err := s.getTerminalConn(req.Msg.GetTerminalId())
	if err != nil {
		return nil, err
	}

	if err := conn.Send(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_TerminalInput{
			TerminalInput: &leapmuxv1.TerminalInput{
				TerminalId: req.Msg.GetTerminalId(),
				Data:       req.Msg.GetData(),
			},
		},
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send input: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.SendInputResponse{}), nil
}

func (s *TerminalService) ResizeTerminal(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ResizeTerminalRequest],
) (*connect.Response[leapmuxv1.ResizeTerminalResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.verifyWorkspaceOwnership(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	terminalID := req.Msg.GetTerminalId()
	conn, err := s.getTerminalConn(terminalID)
	if err != nil {
		return nil, err
	}
	if err := conn.Send(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_TerminalResize{
			TerminalResize: &leapmuxv1.TerminalResizeRequest{
				TerminalId: terminalID,
				Cols:       req.Msg.GetCols(),
				Rows:       req.Msg.GetRows(),
			},
		},
	}); err != nil {
		slog.Warn("terminal resize send failed", "terminal_id", terminalID, "error", err)
	}

	return connect.NewResponse(&leapmuxv1.ResizeTerminalResponse{}), nil
}

func (s *TerminalService) ListTerminals(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListTerminalsRequest],
) (*connect.Response[leapmuxv1.ListTerminalsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.verifyWorkspaceOwnership(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	// Collect unique worker IDs for this workspace's terminals.
	s.mu.RLock()
	workerIDs := make(map[string]struct{})
	for _, info := range s.terminalRoutes {
		if info.workspaceID == req.Msg.GetWorkspaceId() {
			workerIDs[info.workerID] = struct{}{}
		}
	}
	s.mu.RUnlock()

	var allTerminals []*leapmuxv1.TerminalInfo
	for workerID := range workerIDs {
		conn := s.workerMgr.Get(workerID)
		if conn == nil {
			continue
		}

		resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_TerminalList{
				TerminalList: &leapmuxv1.TerminalListRequest{
					WorkspaceId: req.Msg.GetWorkspaceId(),
				},
			},
		})
		if err != nil {
			continue
		}

		termResp := resp.GetTerminalListResp()
		if termResp != nil {
			for _, t := range termResp.GetTerminals() {
				allTerminals = append(allTerminals, &leapmuxv1.TerminalInfo{
					TerminalId: t.GetTerminalId(),
					Cols:       t.GetCols(),
					Rows:       t.GetRows(),
					Screen:     t.GetScreen(),
					Exited:     t.GetExited(),
				})
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.ListTerminalsResponse{
		Terminals: allTerminals,
	}), nil
}

func (s *TerminalService) ListAvailableShells(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListAvailableShellsRequest],
) (*connect.Response[leapmuxv1.ListAvailableShellsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.verifyWorkspaceOwnership(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}

	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ShellList{
			ShellList: &leapmuxv1.ShellListRequest{
				WorkspaceId: req.Msg.GetWorkspaceId(),
			},
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	shellResp := resp.GetShellListResp()
	if shellResp == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response type"))
	}

	return connect.NewResponse(&leapmuxv1.ListAvailableShellsResponse{
		Shells:       shellResp.GetShells(),
		DefaultShell: shellResp.GetDefaultShell(),
	}), nil
}

// HandleTerminalOutput routes terminal output from a worker to watchers.
func (s *TerminalService) HandleTerminalOutput(output *leapmuxv1.TerminalOutput) {
	s.terminalMgr.Broadcast(output.GetTerminalId(), &leapmuxv1.TerminalEvent{
		TerminalId: output.GetTerminalId(),
		Event: &leapmuxv1.TerminalEvent_Data{
			Data: &leapmuxv1.TerminalData{
				Data: output.GetData(),
			},
		},
	})
}

// HandleTerminalExited handles a terminal exit event from a worker.
func (s *TerminalService) HandleTerminalExited(exited *leapmuxv1.TerminalExited) {
	s.untrackTerminal(exited.GetTerminalId())
	s.terminalMgr.Broadcast(exited.GetTerminalId(), &leapmuxv1.TerminalEvent{
		TerminalId: exited.GetTerminalId(),
		Event: &leapmuxv1.TerminalEvent_Closed{
			Closed: &leapmuxv1.TerminalClosed{
				ExitCode: exited.GetExitCode(),
			},
		},
	})
}

func (s *TerminalService) trackTerminal(terminalID, workspaceID, workerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminalRoutes[terminalID] = terminalInfo{workspaceID: workspaceID, workerID: workerID}
}

// TrackTerminal registers a terminal in the routing table. Exported for testing.
func (s *TerminalService) TrackTerminal(terminalID, workspaceID, workerID string) {
	s.trackTerminal(terminalID, workspaceID, workerID)
}

func (s *TerminalService) untrackTerminal(terminalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.terminalRoutes, terminalID)
}

// CleanupTerminalsByWorkspaces broadcasts a closed event for all terminals
// belonging to the given workspace IDs. Called when a worker disconnects.
func (s *TerminalService) CleanupTerminalsByWorkspaces(workspaceIDs []string) {
	wsSet := make(map[string]bool, len(workspaceIDs))
	for _, id := range workspaceIDs {
		wsSet[id] = true
	}

	s.mu.Lock()
	var toClose []string
	for termID, info := range s.terminalRoutes {
		if wsSet[info.workspaceID] {
			toClose = append(toClose, termID)
		}
	}
	for _, termID := range toClose {
		delete(s.terminalRoutes, termID)
	}
	s.mu.Unlock()

	for _, termID := range toClose {
		s.terminalMgr.Broadcast(termID, &leapmuxv1.TerminalEvent{
			TerminalId: termID,
			Event: &leapmuxv1.TerminalEvent_Closed{
				Closed: &leapmuxv1.TerminalClosed{},
			},
		})
	}
}

// CleanupTerminalsByWorker removes all terminals on a specific worker and
// broadcasts a closed event for each. Called when a worker disconnects.
func (s *TerminalService) CleanupTerminalsByWorker(workerID string) {
	s.mu.Lock()
	var toClose []string
	for termID, info := range s.terminalRoutes {
		if info.workerID == workerID {
			toClose = append(toClose, termID)
		}
	}
	for _, termID := range toClose {
		delete(s.terminalRoutes, termID)
	}
	s.mu.Unlock()

	for _, termID := range toClose {
		s.terminalMgr.Broadcast(termID, &leapmuxv1.TerminalEvent{
			TerminalId: termID,
			Event: &leapmuxv1.TerminalEvent_Closed{
				Closed: &leapmuxv1.TerminalClosed{},
			},
		})
	}
}

// GetTerminalWorkerID returns the worker ID for a tracked terminal, or
// an empty string if the terminal is not tracked.
func (s *TerminalService) GetTerminalWorkerID(terminalID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if info, ok := s.terminalRoutes[terminalID]; ok {
		return info.workerID
	}
	return ""
}

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

func (c *Client) handleAgentStart(ctx context.Context, requestID string, req *leapmuxv1.AgentStartRequest) {
	agentID := req.GetAgentId()
	workspaceID := req.GetWorkspaceId()
	model := req.GetModel()
	workingDir := req.GetWorkingDir()

	if model == "" {
		model = "haiku"
	}

	resolvedDir, err := resolveWorkingDir(workingDir)
	if err != nil {
		slog.Error("failed to resolve working directory", "agent_id", agentID, "working_dir", workingDir, "error", err)
		_ = c.Send(&leapmuxv1.ConnectRequest{
			RequestId: requestID,
			Payload: &leapmuxv1.ConnectRequest_AgentStarted{
				AgentStarted: &leapmuxv1.AgentStarted{
					AgentId: agentID,
					Error:   err.Error(),
				},
			},
		})
		return
	}

	action := "starting agent"
	if req.GetAgentSessionId() != "" {
		action = "resuming agent"
	}
	slog.Info(action,
		"agent_id", agentID,
		"workspace_id", workspaceID,
		"model", model,
		"effort", req.GetEffort(),
		"permission_mode", req.GetPermissionMode(),
		"resume_session_id", req.GetAgentSessionId(),
		"working_dir", resolvedDir,
	)

	outputFn := func(line []byte) {
		// Forward each NDJSON line to the Hub as agent output.
		// NOTE: Do NOT set RequestId here. AgentOutput is fire-and-forget,
		// not a request-response pair. Setting the same requestID as
		// AgentStartRequest causes a race condition where pending.Complete
		// in the hub's receive loop consumes the first AgentOutput message
		// instead of routing it to HandleAgentOutput.
		_ = c.Send(&leapmuxv1.ConnectRequest{
			Payload: &leapmuxv1.ConnectRequest_AgentOutput{
				AgentOutput: &leapmuxv1.AgentOutput{
					AgentId:     agentID,
					WorkspaceId: workspaceID,
					Content:     line,
				},
			},
		})

		// On turn-end (result message), detect and report git status.
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &envelope) == nil && envelope.Type == "result" {
			if gs := gitutil.GetGitStatus(resolvedDir); gs != nil {
				_ = c.Send(&leapmuxv1.ConnectRequest{
					Payload: &leapmuxv1.ConnectRequest_AgentGitInfo{
						AgentGitInfo: &leapmuxv1.AgentGitInfo{
							AgentId:     agentID,
							WorkspaceId: workspaceID,
							GitStatus:   gitStatusToProto(gs),
						},
					},
				})
			}
		}
	}

	confirmedMode, err := c.agents.StartAgent(ctx, agent.Options{
		AgentID:         agentID,
		Model:           model,
		Effort:          req.GetEffort(),
		WorkingDir:      resolvedDir,
		ResumeSessionID: req.GetAgentSessionId(),
		PermissionMode:  req.GetPermissionMode(),
	}, outputFn)

	if err != nil {
		slog.Error("failed to start agent", "agent_id", agentID, "error", err)
		_ = c.Send(&leapmuxv1.ConnectRequest{
			RequestId: requestID,
			Payload: &leapmuxv1.ConnectRequest_AgentStarted{
				AgentStarted: &leapmuxv1.AgentStarted{
					AgentId: agentID,
					Error:   err.Error(),
				},
			},
		})
		return
	}

	c.mu.Lock()
	c.agentWorkspaces[agentID] = workspaceID
	c.mu.Unlock()

	// Notify Hub that the agent started. The session ID is not available yet
	// because Claude Code with --input-format stream-json does not produce
	// any output until it receives stdin input. The session ID will be
	// extracted from the init message in HandleAgentOutput on the hub side.
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_AgentStarted{
			AgentStarted: &leapmuxv1.AgentStarted{
				AgentId:            agentID,
				WorkspaceId:        workspaceID,
				ResolvedWorkingDir: resolvedDir,
				PermissionMode:     confirmedMode,
				Effort:             req.GetEffort(),
				GitStatus:          gitStatusToProto(gitutil.GetGitStatus(resolvedDir)),
			},
		},
	})
}

func (c *Client) handleAgentInput(requestID string, req *leapmuxv1.AgentInput) {
	agentID := req.GetAgentId()
	err := c.agents.SendInput(agentID, string(req.GetContent()))

	ack := &leapmuxv1.AgentInputAck{AgentId: agentID}
	if err != nil {
		slog.Warn("agent input failed", "agent_id", agentID, "error", err)
		ack.ErrorReason = err.Error()
		if errors.Is(err, agent.ErrAgentNotFound) {
			ack.Error = leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_AGENT_NOT_FOUND
		} else {
			ack.Error = leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_INTERNAL
		}
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_AgentInputAck{
			AgentInputAck: ack,
		},
	})
}

func (c *Client) handleAgentRawInput(requestID string, req *leapmuxv1.AgentRawInput) {
	agentID := req.GetAgentId()
	err := c.agents.SendRawInput(agentID, req.GetContent())

	ack := &leapmuxv1.AgentInputAck{AgentId: agentID}
	if err != nil {
		slog.Warn("agent raw input failed", "agent_id", agentID, "error", err)
		ack.ErrorReason = err.Error()
		if errors.Is(err, agent.ErrAgentNotFound) {
			ack.Error = leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_AGENT_NOT_FOUND
		} else {
			ack.Error = leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_INTERNAL
		}
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_AgentInputAck{
			AgentInputAck: ack,
		},
	})
}

func (c *Client) handleAgentStop(req *leapmuxv1.AgentStopRequest) {
	slog.Info("stopping agent", "agent_id", req.GetAgentId())
	if c.agents.StopAgent(req.GetAgentId()) {
		// agentWorkspaces cleanup and AgentStopped notification are handled
		// by the exit handler passed to agent.NewManager.
		return
	}

	// Agent already exited — send AgentStopped so the hub can complete
	// any pending restart cycle (e.g. /clear or settings change).
	slog.Info("agent already stopped", "agent_id", req.GetAgentId())
	_ = c.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_AgentStopped{
			AgentStopped: &leapmuxv1.AgentStopped{
				AgentId:     req.GetAgentId(),
				WorkspaceId: req.GetWorkspaceId(),
			},
		},
	})
}

// gitStatusToProto converts a gitutil.GitStatus to the protobuf AgentGitStatus.
// Returns nil if the input is nil.
func gitStatusToProto(gs *gitutil.GitStatus) *leapmuxv1.AgentGitStatus {
	if gs == nil {
		return nil
	}
	return &leapmuxv1.AgentGitStatus{
		Branch:      gs.Branch,
		Ahead:       int32(gs.Ahead),
		Behind:      int32(gs.Behind),
		Conflicted:  gs.Conflicted,
		Stashed:     gs.Stashed,
		Deleted:     gs.Deleted,
		Renamed:     gs.Renamed,
		Modified:    gs.Modified,
		TypeChanged: gs.TypeChanged,
		Added:       gs.Added,
		Untracked:   gs.Untracked,
	}
}

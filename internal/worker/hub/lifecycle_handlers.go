package hub

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func (c *Client) handleDeregister(requestID string, _ *leapmuxv1.DeregisterNotification) {
	slog.Info("received deregistration notification from hub")

	// Stop all agents and terminals.
	c.agents.StopAll()
	c.terminals.StopAll()

	// Send ack.
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_DeregisterAck{
			DeregisterAck: &leapmuxv1.DeregisterAck{},
		},
	})

	// Trigger graceful shutdown.
	if c.OnDeregister != nil {
		c.OnDeregister()
	}
}

func (c *Client) handleHubShuttingDown(msg *leapmuxv1.HubShuttingDownNotification) {
	delay := msg.GetRetryDelaySeconds()
	slog.Info("hub is shutting down, will delay reconnect", "retry_delay_seconds", delay)
	c.hubRetryDelay.Store(int64(delay))
}

func (c *Client) handleTerminateWorkspaces(requestID string, req *leapmuxv1.TerminateWorkspacesNotification) {
	workspaceIDs := req.GetWorkspaceIds()
	slog.Info("received terminate workspaces notification", "workspace_ids", workspaceIDs, "reason", req.GetReason())

	targetWorkspaces := make(map[string]bool, len(workspaceIDs))
	for _, id := range workspaceIDs {
		targetWorkspaces[id] = true
	}

	// Stop agents belonging to the target workspaces.
	c.mu.Lock()
	var agentsToStop []string
	for agentID, wsID := range c.agentWorkspaces {
		if targetWorkspaces[wsID] {
			agentsToStop = append(agentsToStop, agentID)
		}
	}
	for _, agentID := range agentsToStop {
		delete(c.agentWorkspaces, agentID)
	}

	// Stop terminals belonging to the target workspaces.
	var terminalsToStop []string
	for termID, meta := range c.terminalWorkspaces {
		if targetWorkspaces[meta.workspaceID] {
			terminalsToStop = append(terminalsToStop, termID)
		}
	}
	for _, termID := range terminalsToStop {
		delete(c.terminalWorkspaces, termID)
	}
	c.mu.Unlock()

	for _, agentID := range agentsToStop {
		c.agents.StopAgent(agentID)
	}
	for _, termID := range terminalsToStop {
		c.terminals.RemoveTerminal(termID)
	}

	// Send ack with the workspace IDs we processed.
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_TerminateWorkspacesAck{
			TerminateWorkspacesAck: &leapmuxv1.TerminateWorkspacesAck{
				WorkspaceIds: workspaceIDs,
			},
		},
	})
}

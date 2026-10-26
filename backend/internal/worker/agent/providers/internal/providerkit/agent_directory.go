package providerkit

import (
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
)

// RemoveAgentDir removes a stopped agent's private directory. dir may be nil.
// Cleanup failures are logged because neither a stop nor a failed start has a
// caller that can act on them. The next worker's sweep retries the removal.
// Providers decide when their processes and other resources no longer need it.
func RemoveAgentDir(providerID, agentID string, dir *agentdir.Dir) {
	if err := dir.Close(); err != nil {
		slog.Warn("remove the agent directory", "provider", providerID, "agent_id", agentID, "error", err)
	}
}

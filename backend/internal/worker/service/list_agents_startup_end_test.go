//go:build unix

package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// listAgentsDuringGit runs a ListAgents call for one agent and holds it inside
// its git status. It calls change while git runs, then releases git and returns
// the status of the reply.
//
// The reply leaves the Worker after git ends. A browser takes the reply as the
// state of the agent, so the status must describe the agent when the reply is
// built. A status that the call read before git is older than every event that
// the Worker broadcast during git, and a browser that applied such an event
// then takes the older reply over it.
func listAgentsDuringGit(t *testing.T, d *channel.Dispatcher, agentID string, change func()) leapmuxv1.AgentStatus {
	t.Helper()
	gitStarted, gitRelease := installBlockingGit(t)
	w := newTestWriter()
	listed := make(chan struct{})
	go func() {
		defer close(listed)
		dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, w)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(gitStarted); return err == nil }, inputQueueWait, 10*time.Millisecond,
		"ListAgents must reach the git status of its row")

	change()
	require.NoError(t, os.WriteFile(gitRelease, nil, 0o600))
	<-listed

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	return resp.GetAgents()[0].GetStatus()
}

// startRunningTestProcess registers a process for agentID in the Manager, as the
// end of a startup does.
func startRunningTestProcess(ctx context.Context, t *testing.T, svc *Service, agentID string, provider leapmuxv1.AgentProvider) {
	t.Helper()
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{AgentID: agentID, AgentProvider: provider, WorkingDir: t.TempDir()},
		svc.Output.NewSink(agentID, provider),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			return newRunningTestAgent(), nil
		})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
}

// A startup ends in this order: the Manager registers the process, then the
// startup registry drops its entry, then the Worker broadcasts ACTIVE. A startup
// that ends while ListAgents runs git leaves the agent ACTIVE when the reply is
// built.
//
// STARTING was a true answer at the start of the call, and INACTIVE never held.
// The reply must still say ACTIVE. A browser that opened its watch before the
// startup ended applied the ACTIVE broadcast first, and it takes the STARTING
// reply over it. Nothing sends ACTIVE again, so the tab shows "Starting" and
// holds the file tree back for as long as the page lives.
func TestListAgents_ReportsActiveWhenTheStartupEndsWhileGitRuns(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "agent-ending-startup"
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		AgentProvider: provider, ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
	}))
	handle := svc.AgentStartup.begin(id, func() {})
	require.NotNil(t, handle)

	status := listAgentsDuringGit(t, d, id, func() {
		// The startup ends now, in the order of runAgentStartup.
		startRunningTestProcess(ctx, t, svc, id, provider)
		svc.AgentStartup.succeed(id, handle)
		svc.AgentStartup.finishEntry(handle)
	})

	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, status,
		"an agent whose startup ended during the call is ACTIVE in the reply")
}

// A startup that is still running when git ends is STARTING in the reply. The
// late status read must not lose the startup that the registry holds.
func TestListAgents_ReportsStartingWhenTheStartupStillRunsAfterGit(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "agent-running-startup"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
	}))
	handle := svc.AgentStartup.begin(id, func() {})
	require.NotNil(t, handle)
	t.Cleanup(func() { svc.AgentStartup.abandon(handle) })

	status := listAgentsDuringGit(t, d, id, func() {})

	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, status,
		"an agent whose startup still runs after git is STARTING in the reply")
}

// The mirror of the first case. A process that stops while ListAgents runs git
// leaves the agent INACTIVE when the reply is built. A browser that applied the
// INACTIVE broadcast first takes an ACTIVE reply over it, and it then offers
// input to an agent that has no process.
func TestListAgents_ReportsInactiveWhenTheProcessStopsWhileGitRuns(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "agent-stopping-process"
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		AgentProvider: provider, ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
	}))
	startRunningTestProcess(ctx, t, svc, id, provider)

	status := listAgentsDuringGit(t, d, id, func() {
		svc.Agents.StopAndWaitAgent(id)
	})

	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, status,
		"an agent whose process stopped during the call is INACTIVE in the reply")
}

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
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// A startup ends in this order: the Manager registers the process, then the
// startup registry drops its entry. ListAgents reads the registry and the Manager
// at two moments, and it runs git between its first read and the status that it
// reports. A startup that ends in that gap left no entry for the late read and no
// process for the early one, so ListAgents reported INACTIVE for an agent that
// had just become ACTIVE. A caller that polls ListAgents until the status leaves
// STARTING, as the E2E helpers do, took that reply for the verdict of the startup.
//
// Both STARTING and ACTIVE are true answers: each one held at some moment of the
// call. INACTIVE never held.
func TestListAgents_NeverReportsInactiveWhenTheStartupEndsWhileGitRuns(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, w := setupTestService(t)
	const id = "agent-ending-startup"
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		AgentProvider: provider, ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
	}))
	handle := svc.AgentStartup.begin(id, func() {})
	require.NotNil(t, handle)
	gitStarted, gitRelease := installBlockingGit(t)

	listed := make(chan struct{})
	go func() {
		defer close(listed)
		dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{id}}, w)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(gitStarted); return err == nil }, inputQueueWait, 10*time.Millisecond,
		"ListAgents must reach the git status of its row")

	// The startup ends now, in the order of runAgentStartup.
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{AgentID: id, AgentProvider: provider, WorkingDir: t.TempDir()},
		svc.Output.NewSink(id, provider),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			return newRunningTestAgent(), nil
		})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(id) })
	svc.AgentStartup.succeed(id, handle)
	svc.AgentStartup.finishEntry(handle)
	require.NoError(t, os.WriteFile(gitRelease, nil, 0o600))
	<-listed

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	assert.Contains(t,
		[]leapmuxv1.AgentStatus{leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		resp.GetAgents()[0].GetStatus(),
		"an agent whose startup ended during the call is STARTING or ACTIVE, never INACTIVE")
}

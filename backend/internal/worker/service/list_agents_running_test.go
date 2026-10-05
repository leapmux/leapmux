//go:build unix

package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// runningTestAgent is an agent whose process runs until Stop. The Manager drops an
// agent whose Wait returns, and agenttest.IdleAgent returns from Wait at once.
type runningTestAgent struct {
	agenttest.IdleAgent
	stopOnce sync.Once
	stopped  chan struct{}
}

func newRunningTestAgent() *runningTestAgent { return &runningTestAgent{stopped: make(chan struct{})} }

func (a *runningTestAgent) Stop() { a.stopOnce.Do(func() { close(a.stopped) }) }

func (a *runningTestAgent) Wait() error {
	<-a.stopped
	return nil
}

func (a *runningTestAgent) IsStopped() bool {
	select {
	case <-a.stopped:
		return true
	default:
		return false
	}
}

// installBlockingGit puts a `git` on PATH that records its start and then waits for a
// release file. A git that answers at once would close the window between the rows
// that ListAgents reads and the process check that follows, so a test could not
// place an event inside it. The caller must not run in parallel: PATH is process state.
func installBlockingGit(t *testing.T) (started, release string) {
	t.Helper()
	dir := t.TempDir()
	started = filepath.Join(dir, "git-started")
	release = filepath.Join(dir, "git-release")
	script := "#!/bin/sh\n: > '" + started + "'\nwhile [ ! -e '" + release + "' ]; do sleep 0.01; done\nexit 128\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return started, release
}

// A process that runs has stored its native session ID, because startup stores the ID
// before the Manager registers the process. ListAgents reads the rows, then runs git
// for each working directory, then asks whether the process runs. A startup that
// completes in the middle of that work left a row from before it, beside a process
// from after it: an ACTIVE agent with no session ID. A picker that reopens a session
// compares the ID of that row with the stored one, and failed on the empty ID.
func TestListAgents_AnActiveAgentStatesTheSessionThatItsStartupStored(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, w := setupTestService(t)
	const id = "agent-starting"
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		AgentProvider: provider, ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
	}))
	gitStarted, gitRelease := installBlockingGit(t)

	listed := make(chan struct{})
	go func() {
		defer close(listed)
		dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{id}}, w)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(gitStarted); return err == nil }, inputQueueWait, 10*time.Millisecond,
		"ListAgents must reach the git status of its row")

	// The startup finishes now: the session ID reaches the row, then the process registers.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: id, AgentSessionID: "session-1"}))
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{AgentID: id, AgentProvider: provider, WorkingDir: t.TempDir()},
		svc.Output.NewSink(id, provider),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			return newRunningTestAgent(), nil
		})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(id) })
	require.NoError(t, os.WriteFile(gitRelease, nil, 0o600))
	<-listed

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	info := resp.GetAgents()[0]
	if info.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		require.Equal(t, "session-1", info.GetAgentSessionId(), "an ACTIVE agent must state the session that its startup stored")
	}
}

//go:build unix

package claude

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

func TestClaudeStartupExhaustionPeer(*testing.T) { agenttest.RunInheritedWriterPeer() }

func TestClaudeStartupKeepsCandidateExhaustionAfterAnEarlyTransportError(t *testing.T) {
	profile := agenttest.InstallStartupExhaustionProfile(t, "TestClaudeStartupExhaustionPeer", true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	outcome := make(chan error, 1)
	go func() {
		started, err := Start(ctx, agent.Options{
			AgentID: "startup-exhaustion", HomeDir: dir, WorkingDir: dir, Shell: profile.Shell,
			StartupTimeout: 30 * time.Second,
		}, agent.NewProviderServices(&agenttest.Sink{}))
		if started != nil {
			started.Stop()
			_ = started.Wait()
		}
		outcome <- err
	}()

	profile.FinishSelection(t)
	request, err := os.ReadFile(profile.Initialize)
	require.NoError(t, err)
	assert.Contains(t, string(request), `"subtype":"initialize"`)
	// The initialize exchange still waits for a reply because the inherited
	// writer holds stdout. Cancel the caller after the shell has exited.
	cancel()
	select {
	case err := <-outcome:
		require.ErrorIs(t, err, launch.ErrNoCandidate)
		assert.ErrorContains(t, err, "(claude)")
		assert.ErrorContains(t, err, "provider absent diagnostic")
		assert.NotContains(t, err.Error(), "__LEAPMUX_EXHAUSTED_")
		var status *exec.ExitError
		require.ErrorAs(t, err, &status)
		assert.Equal(t, 127, status.ExitCode())
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("startup did not return after the bounded output drain")
	}
}

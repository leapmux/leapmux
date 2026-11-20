package cmd

import (
	"bytes"
	"testing"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitWorktreeLookupDisplaysCheckoutFields(t *testing.T) {
	result := gitWorktreeLookup{
		GitWorktreeRow: GitWorktreeRow{Path: "/repo/checkout", Branch: "feature/selected", Locked: true, LockReason: "selected for review"},
		WorkerID:       "worker-a", RepoRoot: "/repo/main",
	}
	for _, format := range []cli.Format{cli.FormatText, cli.FormatJSON} {
		var out bytes.Buffer
		printer := cli.Printer{Out: &out, Format: format}
		require.NoError(t, printer.Result(result))
		for _, field := range []string{"/repo/checkout", "feature/selected", "selected for review", "worker-a", "/repo/main"} {
			assert.Contains(t, out.String(), field)
		}
	}
}

func TestGitWorktreeBranchRequiresTheSelectedWorkersDirectory(t *testing.T) {
	hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
	dispatcher := &creationPublicationDispatcher{}
	startSpawnIPC(t, hub, dispatcher)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.WorkingDirVariable, "/local/repo")
	outcome := runControl(t, "git", "worktree", "get", "branch:topic", "--worker", "worker-B")
	failure := outcome.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, failure.Message, "requires --dir on the selected worker")
	assert.Empty(t, dispatcher.recorded())
}

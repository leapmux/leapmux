package opencodetest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertPinsTheQuestionToolFlag asserts the one environment variable that makes
// the whole question bridge reachable: start must launch the daemon with key set
// to 1, exactly once. install puts a fake daemon on PATH that records its
// environment in the file it is given.
//
// Both daemons register their question tool only when the client is one of a small
// allowed list or when this flag is set, and `<cli> acp` assigns its own client name
// at the top of the handler -- so the flag is the only route. Without it the agent
// never asks, no `question.asked` is ever published, and every route below this file
// is dead code. A live OpenCode session under `.tmp/probe` confirms both halves: with
// the flag the daemon calls `question` and the reply ends the turn, and without it no
// question tool is offered at all.
//
// The value is PINNED, so the test poisons the inherited environment with the value
// that would turn the tool off. A default would let that value through.
func AssertPinsTheQuestionToolFlag(t *testing.T, key string, install func(t *testing.T, envFile string), start agent.StartFunc, agentID string) {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "env")
	// The value that would turn the tool OFF, which a default would inherit.
	t.Setenv(key, "0")
	install(t, envFile)

	provider, err := start(context.Background(), agent.Options{
		AgentID:    agentID,
		WorkingDir: t.TempDir(),
		Shell:      testutil.TestShell(),
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() {
		provider.Stop()
		_ = provider.Wait()
	})

	raw, err := os.ReadFile(envFile)
	require.NoError(t, err, "the launcher must have recorded its environment")
	// Only the assignments of THIS key. The whole environment is hundreds of
	// entries, and a failure that prints all of them hides the one that matters.
	var seen []string
	for _, entry := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(entry, key+"=") {
			seen = append(seen, entry)
		}
	}
	assert.Equal(t, []string{key + "=1"}, seen,
		"the daemon must receive the flag exactly once, turned on")
}

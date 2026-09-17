//go:build unix

// The spawn test below installs a fake CLI on PATH through the shared unix-only
// helpers, so it lives beside them rather than in opencode_questions_test.go.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenCodeFamilyPinsTheQuestionToolFlag asserts the one environment variable
// that makes the whole question bridge reachable.
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
func TestOpenCodeFamilyPinsTheQuestionToolFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		install func(*testing.T, string)
		start   func(context.Context, Options, ProviderServices) (Agent, error)
		agentID string
	}{
		{
			name:    "opencode",
			key:     openCodeQuestionToolEnv,
			install: func(t *testing.T, envFile string) { installFakeOpenCodeACP(t, "", envFile) },
			start:   StartOpenCode,
			agentID: "opencode-question-flag",
		},
		{
			name:    "kilo",
			key:     kiloQuestionToolEnv,
			install: func(t *testing.T, envFile string) { installFakeKiloACP(t, "", envFile) },
			start:   StartKilo,
			agentID: "kilo-question-flag",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), "env")
			// The value that would turn the tool OFF, which a default would inherit.
			t.Setenv(tc.key, "0")
			tc.install(t, envFile)

			provider, err := tc.start(context.Background(), Options{
				AgentID:    tc.agentID,
				WorkingDir: t.TempDir(),
				Shell:      testutil.TestShell(),
			}, &testSink{})
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
				if strings.HasPrefix(entry, tc.key+"=") {
					seen = append(seen, entry)
				}
			}
			assert.Equal(t, []string{tc.key + "=1"}, seen,
				"the daemon must receive the flag exactly once, turned on")
		})
	}
}

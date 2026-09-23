package claude

import (
	"context"
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// spawnMockClaudeAgent spawns os.Args[0] with -test.run=testRun as a mock
// Claude process, wires its stdin/stdout into an Agent, and starts
// the output reader. extraEnv is appended to os.Environ(). Callers populate
// any additional agent fields on the returned instance before use.
func spawnMockClaudeAgent(ctx context.Context, testRun string, extraEnv []string, opts agent.Options, sink agent.ProviderServices) (*Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run="+testRun, "--")
	cmd.Env = append(os.Environ(), extraEnv...)
	return StartOverCommand(ctx, cancel, cmd, opts, sink)
}

// claudeModelsByID indexes converted available models by their ID for
// order-independent assertions. It lives here, outside the unix-only
// agent_test.go, so tests that run on every platform (e.g. the live-settings
// control-response tests) can use it without dragging in unix build tags.
func claudeModelsByID(models []*agent.ModelInfo) map[string]*agent.ModelInfo {
	byID := make(map[string]*agent.ModelInfo, len(models))
	for _, m := range models {
		byID[m.Id] = m
	}
	return byID
}

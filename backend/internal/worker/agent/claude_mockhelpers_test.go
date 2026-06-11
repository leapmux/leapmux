package agent

import (
	"context"
	"os"
	"os/exec"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// spawnMockClaudeAgent spawns os.Args[0] with -test.run=testRun as a mock
// Claude process, wires its stdin/stdout into a ClaudeCodeAgent, and starts
// the output reader. extraEnv is appended to os.Environ(). Callers populate
// any additional agent fields on the returned instance before use.
func spawnMockClaudeAgent(ctx context.Context, testRun string, extraEnv []string, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run="+testRun, "--")
	cmd.Env = append(os.Environ(), extraEnv...)
	return wireClaudeMockAgent(ctx, cancel, cmd, opts, sink)
}

// claudeModelsByID indexes converted available models by their ID for
// order-independent assertions. It lives here, outside the unix-only
// claude_test.go, so tests that run on every platform (e.g. the live-settings
// control-response tests) can use it without dragging in unix build tags.
func claudeModelsByID(models []*leapmuxv1.AvailableModel) map[string]*leapmuxv1.AvailableModel {
	byID := make(map[string]*leapmuxv1.AvailableModel, len(models))
	for _, m := range models {
		byID[m.Id] = m
	}
	return byID
}

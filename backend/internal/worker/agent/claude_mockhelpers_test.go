package agent

import (
	"context"
	"os"
	"os/exec"
)

// spawnMockClaudeAgent spawns os.Args[0] with -test.run=testRun as a mock
// Claude process, wires its stdin/stdout into a ClaudeCodeAgent, and starts
// the output reader. extraEnv is appended to os.Environ(). Callers populate
// any additional agent fields on the returned instance before use.
func spawnMockClaudeAgent(ctx context.Context, testRun string, extraEnv []string, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run="+testRun, "--")
	cmd.Env = append(os.Environ(), extraEnv...)
	return wireClaudeMockAgent(ctx, cancel, cmd, opts, sink, "")
}

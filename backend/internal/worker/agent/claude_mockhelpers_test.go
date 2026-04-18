package agent

import (
	"bufio"
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
	cmd.Dir = opts.WorkingDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stderr = nil

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     opts.AgentID,
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
			apiTimeout:  opts.apiTimeout(),
		},
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	return a, nil
}

package agent

import (
	"bufio"
	"context"
	"os"
	"os/exec"
)

// MockStartAgent registers a mock agent process in the manager for testing.
// The mock process is a simple echo process that reads stdin and writes to
// stdout. It does not implement the Claude Code protocol.
//
// This is intended for use in tests outside the agent package that need a
// running agent registered in the manager (e.g. to make HasAgent return true).
func (m *Manager) MockStartAgent(ctx context.Context, opts Options, sink OutputSink) (string, error) {
	return m.startAgentWith(ctx, opts, sink, mockStartForTest)
}

// mockStartForTest spawns a test helper process (simple cat) instead of the
// real claude binary. Unlike the internal mockStart used in agent_test.go,
// this does not depend on TestHelperProcess and uses a plain "cat" command.
func mockStartForTest(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, "cat")
	cmd.Dir = opts.WorkingDir
	cmd.Env = os.Environ()

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
			stderrDone:  make(chan struct{}),
			processDone: make(chan struct{}),
		},
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		homeDir:        opts.HomeDir,
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone) // no stderr pipe in mock

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	return a, nil
}

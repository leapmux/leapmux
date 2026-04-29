package agent

import (
	"bufio"
	"context"
	"os"
	"os/exec"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// MockStartAgent registers a mock agent process in the manager for testing.
// The mock process is a simple echo process that reads stdin and writes to
// stdout. It does not implement the Claude Code protocol.
//
// This is intended for use in tests outside the agent package that need a
// running agent registered in the manager (e.g. to make HasAgent return true).
func (m *Manager) MockStartAgent(ctx context.Context, opts Options, sink OutputSink) (*leapmuxv1.AgentSettings, error) {
	return m.startAgentWith(ctx, opts, sink, mockStartForTest)
}

// mockStartForTest spawns a plain "cat" process and wires it up as a
// ClaudeCodeAgent. Unlike the in-package spawnMockClaudeAgent (which runs
// TestHelperProcess to simulate the Claude Code protocol), this helper is
// for external consumers that only need a running agent in the manager.
func mockStartForTest(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "cat")
	cmd.Env = os.Environ()
	return wireClaudeMockAgent(ctx, cancel, cmd, opts, sink)
}

// wireClaudeMockAgent takes a cmd pre-bound to ctx via exec.CommandContext
// and builds a ClaudeCodeAgent around it: wires stdin/stdout, starts the
// process, and spawns the output reader loop. Callers pass the matching
// cancel so cmd.Start failures can release the context.
func wireClaudeMockAgent(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
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
			stderrDone:  make(chan struct{}),
			processDone: make(chan struct{}),
			apiTimeout:  opts.apiTimeout(),
		},
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		homeDir:        opts.HomeDir,
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

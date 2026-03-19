package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// maxStderrSize is the maximum amount of stderr to buffer.
const maxStderrSize = 1 << 20 // 1MB

// processBase contains the shared process lifecycle state and methods
// used by both Agent (Claude Code) and CodexAgent (Codex).
type processBase struct {
	agentID string
	stdin   io.WriteCloser

	cmd         *exec.Cmd
	ctx         context.Context
	cancel      func()
	processDone chan struct{}
	waitErr     error

	stderrBuf  bytes.Buffer
	stderrMu   sync.Mutex
	stderrDone chan struct{}

	mu      sync.Mutex
	stopped bool
}

// SendRawInput writes raw bytes directly to the process's stdin without
// wrapping. Ensures a trailing newline.
func (p *processBase) SendRawInput(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return fmt.Errorf("agent is stopped")
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}

	if _, err := p.stdin.Write(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}

	return nil
}

// Stop terminates the process gracefully. It closes stdin and gives the
// process a short grace period before cancelling the context (which sends
// SIGTERM, then SIGKILL after WaitDelay).
func (p *processBase) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()

	_ = p.stdin.Close()

	select {
	case <-p.processDone:
		return
	case <-time.After(3 * time.Second):
		p.cancel()
	}

	<-p.processDone
}

// IsStopped returns true if the process was intentionally stopped via Stop().
func (p *processBase) IsStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// Wait blocks until the process exits and returns its exit error.
func (p *processBase) Wait() error {
	<-p.processDone
	return p.waitErr
}

// AgentID returns the unique identifier for this agent.
func (p *processBase) AgentID() string {
	return p.agentID
}

// Stderr returns the captured stderr output. It waits for the stderr
// goroutine to finish draining the pipe (up to 3 seconds).
func (p *processBase) Stderr() string {
	select {
	case <-p.stderrDone:
	case <-time.After(3 * time.Second):
	}
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	return p.stderrBuf.String()
}

// processExitError returns a descriptive error for a process that exited
// unexpectedly. It includes the exit code when available.
func (p *processBase) processExitError() error {
	if p.waitErr != nil {
		if exitErr, ok := p.waitErr.(*exec.ExitError); ok {
			return fmt.Errorf("agent process exited with code %d", exitErr.ExitCode())
		}
	}
	return fmt.Errorf("agent process exited unexpectedly")
}

// formatStartupError returns a descriptive error including stderr for
// frontend diagnostics. The optional preambleOutput is included when
// non-empty (used by Claude Code's shell wrapper).
func (p *processBase) formatStartupError(phase string, err error, preambleOutput string) error {
	parts := []string{fmt.Sprintf("%s: %s", phase, err)}
	if stderr := strings.TrimSpace(p.Stderr()); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if preamble := strings.TrimSpace(preambleOutput); preamble != "" {
		parts = append(parts, "shell preamble: "+preamble)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// drainStderr starts a goroutine that reads from the given reader into
// the stderr buffer, capped at maxStderrSize. It closes stderrDone when
// the reader is exhausted. The lock is only held for individual writes,
// not the entire drain loop.
func (p *processBase) drainStderr(r io.Reader) {
	go func() {
		defer close(p.stderrDone)
		buf := make([]byte, 32*1024)
		var total int64
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				p.stderrMu.Lock()
				if total < maxStderrSize {
					limit := int64(n)
					if total+limit > maxStderrSize {
						limit = maxStderrSize - total
					}
					p.stderrBuf.Write(buf[:limit])
					total += limit
				}
				p.stderrMu.Unlock()
			}
			if readErr != nil {
				if readErr != io.EOF {
					slog.Debug("stderr drain error", "agent_id", p.agentID, "error", readErr)
				}
				break
			}
		}
	}()
}

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxStderrSize is the maximum amount of stderr to buffer.
const maxStderrSize = 1 << 20 // 1MB

// processBase contains the shared process lifecycle state and methods.
// ClaudeCodeAgent embeds it directly; ACP agents (GeminiCLIAgent, OpenCodeAgent)
// and CodexAgent embed it via jsonrpcBase which adds JSON-RPC request plumbing.
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

	mu             sync.Mutex
	stopped        bool
	discardOutput  bool

	// Preamble handling (from shell wrapper).
	preambleDelimiter  string            // if set, skipPreamble skips lines until this delimiter
	preambleMetaPrefix string            // prefix for metadata lines (before delimiter)
	preambleMeta       map[string]string // parsed key=value metadata from preamble
	preambleOutput     []string          // captured preamble lines (before delimiter)

	turnToolUses int // number of tool uses in the current turn
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

// ClearContext is a no-op for providers that don't support in-place context
// clearing. Providers that support it (e.g. Codex) override this method.
func (p *processBase) ClearContext() (string, bool) { return "", false }

// DiscardOutput marks the process so that the readOutput loop silently
// drops all remaining lines. Use this before stopping an agent that will
// be restarted (e.g. plan execution) to avoid persisting spurious error
// messages from closed streams.
func (p *processBase) DiscardOutput() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discardOutput = true
}

func (p *processBase) isDiscardingOutput() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discardOutput
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

// formatStartupError returns a descriptive error including stderr and
// preamble output for frontend diagnostics.
func (p *processBase) formatStartupError(phase string, err error) error {
	parts := []string{fmt.Sprintf("%s: %s", phase, err)}
	if stderr := strings.TrimSpace(p.Stderr()); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if preamble := strings.TrimSpace(p.PreambleOutput()); preamble != "" {
		parts = append(parts, "shell preamble: "+preamble)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// skipPreamble reads lines from the scanner until the preamble delimiter is
// found. Shell login preamble (motd, .zshrc output, etc.) appears before the
// agent's JSONL stream. Metadata lines (key=value pairs prefixed with
// preambleMetaPrefix) are parsed and stored; other preamble lines are captured
// for diagnostics. This is a no-op if preambleDelimiter is empty.
func (p *processBase) skipPreamble(scanner *bufio.Scanner) {
	if p.preambleDelimiter == "" {
		return
	}
	delimBytes := []byte(p.preambleDelimiter)
	metaPrefixBytes := []byte(p.preambleMetaPrefix)
	const maxPreambleLines = 50
	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)
		if bytes.Equal(trimmed, delimBytes) {
			break
		}
		if len(metaPrefixBytes) > 0 && bytes.HasPrefix(trimmed, metaPrefixBytes) {
			kv := string(trimmed[len(metaPrefixBytes):])
			if eqIdx := strings.IndexByte(kv, '='); eqIdx >= 0 {
				p.preambleMeta[kv[:eqIdx]] = kv[eqIdx+1:]
			}
			continue
		}
		if len(p.preambleOutput) < maxPreambleLines {
			p.preambleOutput = append(p.preambleOutput, string(line))
		}
	}
}

// PreambleOutput returns the captured stdout preamble lines (before the
// delimiter) when running under a login shell wrapper.
func (p *processBase) PreambleOutput() string {
	if len(p.preambleOutput) == 0 {
		return ""
	}
	return strings.Join(p.preambleOutput, "\n")
}

// setupProcessPipes configures the command's cancel/wait behavior and opens
// stdin, stdout, and stderr pipes. On error it calls cancel() and returns.
func setupProcessPipes(cmd *exec.Cmd, cancel func()) (stdin io.WriteCloser, stdout, stderr io.ReadCloser, err error) {
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdin, err = cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err = cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err = cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}

	return stdin, stdout, stderr, nil
}

// parsedLine holds the JSON-parsed superset of all agent output envelope
// fields. Raw is the original bytes; the typed fields are populated by a
// single json.Unmarshal in readOutput so downstream consumers never need to
// re-parse the envelope.
type parsedLine struct {
	Raw    []byte
	ID     *json.Number    `json:"id"`
	Method string          `json:"method"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// parseLine creates a parsedLine from raw bytes. Used by HandleOutput methods
// that accept []byte (e.g. for tests) to bridge into the single-parse pipeline.
func parseLine(content []byte) *parsedLine {
	line := &parsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		slog.Warn("parse line unmarshal failed", "error", err)
	}
	return line
}

// outputInterceptor is a function that inspects a parsed line before it is
// forwarded to HandleOutput. If it returns true, the line is consumed (not
// forwarded).
type outputInterceptor func(line *parsedLine) bool

// outputHandler processes a single parsed output line from the agent process.
type outputHandler func(line *parsedLine)

// readOutput reads JSONL lines from stdout, JSON-parses them once into a
// parsedLine, optionally intercepts responses, then forwards remaining lines
// to the output handler.
func (p *processBase) readOutput(scanner *bufio.Scanner, intercept outputInterceptor, handle outputHandler) {
	p.skipPreamble(scanner)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		parsed := &parsedLine{Raw: lineCopy}
		if err := json.Unmarshal(lineCopy, parsed); err != nil {
			slog.Warn("invalid agent output JSON", "agent_id", p.agentID, "error", err)
			continue
		}

		// When marked for output discard (e.g. plan execution restart),
		// drop remaining lines to avoid persisting spurious error messages
		// from closed streams.
		if p.isDiscardingOutput() {
			continue
		}

		if intercept(parsed) {
			continue
		}

		handle(parsed)
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("agent stdout read error",
			"agent_id", p.agentID,
			"error", err,
		)
	}

	p.waitErr = p.cmd.Wait()
	close(p.processDone)
}

// enrichWithToolUses injects num_tool_uses into a JSON message so the
// frontend can determine whether the turn involved tool use. Returns the
// original content unchanged if enrichment fails.
func (p *processBase) enrichWithToolUses(content []byte) []byte {
	p.mu.Lock()
	numToolUses := p.turnToolUses
	p.mu.Unlock()

	enriched := make(map[string]json.RawMessage)
	if err := json.Unmarshal(content, &enriched); err != nil {
		return content
	}
	b, err := json.Marshal(numToolUses)
	if err != nil {
		return content
	}
	enriched["num_tool_uses"] = b
	out, err := json.Marshal(enriched)
	if err != nil {
		return content
	}
	return out
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

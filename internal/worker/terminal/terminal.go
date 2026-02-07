package terminal

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

const screenBufferSize = 100 * 1024 // 100KB ring buffer for screen restore

// ScreenBuffer is a thread-safe ring buffer that stores recent PTY output.
type ScreenBuffer struct {
	mu   sync.Mutex
	buf  []byte
	pos  int
	full bool
}

// NewScreenBuffer creates a new screen buffer.
func NewScreenBuffer() *ScreenBuffer {
	return &ScreenBuffer{buf: make([]byte, screenBufferSize)}
}

// Write appends data to the ring buffer.
func (sb *ScreenBuffer) Write(data []byte) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for len(data) > 0 {
		n := copy(sb.buf[sb.pos:], data)
		data = data[n:]
		sb.pos += n
		if sb.pos >= len(sb.buf) {
			sb.pos = 0
			sb.full = true
		}
	}
}

// Snapshot returns a copy of the buffered data in chronological order.
func (sb *ScreenBuffer) Snapshot() []byte {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if !sb.full {
		out := make([]byte, sb.pos)
		copy(out, sb.buf[:sb.pos])
		return out
	}

	out := make([]byte, len(sb.buf))
	n := copy(out, sb.buf[sb.pos:])
	copy(out[n:], sb.buf[:sb.pos])
	return out
}

// OutputHandler is called for each chunk of output from the PTY.
type OutputHandler func(data []byte)

// Terminal manages a single PTY session.
type Terminal struct {
	id        string
	cmd       *exec.Cmd
	ptmx      *os.File
	outputFn  OutputHandler
	screenBuf *ScreenBuffer
	mu        sync.Mutex
	stopped   bool
	exitCode  int
	exitCh    chan struct{}
}

// Options configures a new Terminal.
type Options struct {
	ID         string
	Shell      string
	WorkingDir string
	Cols       uint16
	Rows       uint16
}

// Start creates a new PTY terminal session.
func Start(opts Options, outputFn OutputHandler) (*Terminal, error) {
	shell := opts.Shell
	if shell == "" {
		shell = resolveDefaultShell()
	}

	cmd := exec.Command(shell)
	cmd.Dir = opts.WorkingDir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
	)

	winSize := &pty.Winsize{
		Cols: opts.Cols,
		Rows: opts.Rows,
	}
	if winSize.Cols == 0 {
		winSize.Cols = 80
	}
	if winSize.Rows == 0 {
		winSize.Rows = 24
	}

	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	screenBuf := NewScreenBuffer()
	wrappedOutput := func(data []byte) {
		screenBuf.Write(data)
		outputFn(data)
	}

	t := &Terminal{
		id:        opts.ID,
		cmd:       cmd,
		ptmx:      ptmx,
		outputFn:  wrappedOutput,
		screenBuf: screenBuf,
		exitCh:    make(chan struct{}),
	}

	go t.readOutput()
	go t.waitForExit()

	slog.Info("terminal started",
		"terminal_id", opts.ID,
		"shell", shell,
		"pid", cmd.Process.Pid,
	)

	return t, nil
}

// SendInput writes data to the PTY.
func (t *Terminal) SendInput(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return fmt.Errorf("terminal is stopped")
	}

	_, err := t.ptmx.Write(data)
	return err
}

// Resize changes the terminal dimensions.
func (t *Terminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return fmt.Errorf("terminal is stopped")
	}

	return pty.Setsize(t.ptmx, &pty.Winsize{
		Cols: cols,
		Rows: rows,
	})
}

// Stop terminates the terminal session.
func (t *Terminal) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}
	t.stopped = true

	_ = t.ptmx.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}

// Wait blocks until the terminal process exits.
func (t *Terminal) Wait() int {
	<-t.exitCh
	return t.exitCode
}

// IsExited returns true if the terminal process has exited.
func (t *Terminal) IsExited() bool {
	select {
	case <-t.exitCh:
		return true
	default:
		return false
	}
}

// ID returns the terminal's ID.
func (t *Terminal) ID() string {
	return t.id
}

// ScreenSnapshot returns the recent PTY output for screen restore.
func (t *Terminal) ScreenSnapshot() []byte {
	return t.screenBuf.Snapshot()
}

func (t *Terminal) readOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			t.outputFn(data)
		}
		if err != nil {
			if err != io.EOF {
				slog.Debug("terminal read error",
					"terminal_id", t.id,
					"error", err,
				)
			}
			return
		}
	}
}

func (t *Terminal) waitForExit() {
	err := t.cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.exitCode = exitErr.ExitCode()
		} else {
			t.exitCode = -1
		}
	}
	close(t.exitCh)

	slog.Info("terminal exited",
		"terminal_id", t.id,
		"exit_code", t.exitCode,
	)
}

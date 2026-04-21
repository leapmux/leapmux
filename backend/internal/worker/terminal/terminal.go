package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/leapmux/leapmux/util/procutil"
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
	cmd       *pty.Cmd
	ptmx      pty.Pty
	jobObject *procutil.JobObject
	outputFn  OutputHandler
	screenBuf *ScreenBuffer
	mu        sync.Mutex
	stopped   bool
	exitCode  int
	exitCh    chan struct{}
}

// Options configures a new Terminal.
type Options struct {
	ID            string
	WorkspaceID   string
	Shell         string
	WorkingDir    string
	ShellStartDir string
	Cols          uint16
	Rows          uint16
}

// Start creates a new PTY terminal session. The supplied context
// governs the spawn itself: if it is already cancelled Start returns
// ctx.Err() without forking, and a later cancellation sends the child
// process the usual exec.CommandContext kill signal. The caller still
// owns the long-running Terminal — its lifetime is independent of ctx
// once Start returns successfully.
func Start(ctx context.Context, opts Options, outputFn OutputHandler) (*Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	shell := opts.Shell
	if shell == "" {
		shell = ResolveDefaultShell()
	}

	args := LoginShellArgs(shell)

	ptmx, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("new pty: %w", err)
	}

	cmd := ptmx.CommandContext(ctx, shell, args...)
	cmd.Dir = opts.WorkingDir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
	)
	// No procutil.HideConsoleWindow here: on Windows, CREATE_NO_WINDOW is
	// incompatible with ConPTY — the pseudo console already serves as the
	// child's console, and the flag would leave it with none.

	cols, rows := opts.Cols, opts.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 25
	}
	if err := ptmx.Resize(int(cols), int(rows)); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("resize pty: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("start pty: %w", err)
	}

	// Put the shell and its descendants into a kill group so closing the
	// tab reaps the whole tree, not just the direct shell. Failure is
	// non-fatal: the terminal still works, it just loses the tree-kill
	// guarantee for this session.
	jobObject, err := procutil.AssignPID(cmd.Process.Pid)
	if err != nil {
		slog.Warn("terminal attach job object failed",
			"terminal_id", opts.ID,
			"error", err,
		)
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
		jobObject: jobObject,
		outputFn:  wrappedOutput,
		screenBuf: screenBuf,
		exitCh:    make(chan struct{}),
	}

	go t.readOutput()
	go t.waitForExit()

	slog.Info("terminal started",
		"terminal_id", opts.ID,
		"shell", shell,
		"args", args,
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

	return t.ptmx.Resize(int(cols), int(rows))
}

// Stop terminates the terminal session and every process spawned beneath
// the shell. Closing the PTY master triggers the kernel's normal hang-up
// flow; Terminate then reaps anything still alive in the shell's kill
// group (JobObject on Windows, process-group SIGHUP+SIGKILL on Unix).
func (t *Terminal) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}
	t.stopped = true

	_ = t.ptmx.Close()
	if err := t.jobObject.Terminate(); err != nil {
		slog.Debug("terminal job object terminate failed",
			"terminal_id", t.id,
			"error", err,
		)
	}
	if t.jobObject == nil && t.cmd.Process != nil {
		// Fallback when AssignPID failed at startup; better than leaking
		// the direct shell process even if we lose the tree guarantee.
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

// AppendOutput injects synthetic output into the terminal stream and screen
// buffer without writing to the PTY. This is used for system notices that
// should be restorable like normal terminal output.
func (t *Terminal) AppendOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	t.outputFn(data)
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
			if !errors.Is(err, io.EOF) {
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
	if closeErr := t.jobObject.Close(); closeErr != nil {
		slog.Debug("terminal job object close failed",
			"terminal_id", t.id,
			"error", closeErr,
		)
	}
	close(t.exitCh)

	slog.Info("terminal exited",
		"terminal_id", t.id,
		"exit_code", t.exitCode,
	)
}

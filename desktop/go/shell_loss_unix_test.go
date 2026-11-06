//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The desktop shell owns the sidecar's stdio. When the shell dies, all three
// of its pipe ends close at once: the sidecar reads EOF on stdin and writes
// into pipes with no reader. The write side must report EPIPE, not raise a
// fatal SIGPIPE, or the sidecar dies mid-drain and loses the Hub's runtime
// lease. These tests reproduce that loss through the real main entry point.

// sidecarTimeout is the generous ceiling for a whole test. Only a failure
// path waits for it.
const sidecarTimeout = 60 * time.Second

// sidecarSession is a running sidecar whose stdio the test owns, the way the
// shell owns it.
type sidecarSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	// stderrM collects the drained stderr. Reads race the drain goroutine, so
	// every access takes the mutex.
	stderrM syncBuffer
	// exitMu guards the single observation of the child's exit. Several
	// waiters (waitStderr, hammerSignal, waitExit, cleanup) poll it, so it
	// must not live in a channel whose one buffered value the first waiter
	// consumes.
	exitMu   sync.Mutex
	exitErr  error
	exitSeen bool
	exitNow  chan struct{}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startSidecarSession starts the real main through runMainEnv with three pipe
// pairs, and drains stderr into the session's buffer until the test closes
// the read end. The caller must run setUniqueSoloLocalListen and
// locallistentest.SandboxHome first: a plain temporary HOME exceeds the
// macOS socket path limit.
func startSidecarSession(t *testing.T) *sidecarSession {
	t.Helper()
	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)
	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)
	stderrR, stderrW, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	require.NoError(t, cmd.Start())
	// The child holds its inherited copies; drop the parent's, so a close of
	// the test-side end reaches the child alone.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	s := &sidecarSession{
		cmd:     cmd,
		stdin:   stdinW,
		stdout:  stdoutR,
		stderr:  stderrR,
		exitNow: make(chan struct{}),
	}
	go func() {
		_, _ = io.Copy(&s.stderrM, stderrR)
	}()
	go func() {
		err := cmd.Wait()
		s.exitMu.Lock()
		s.exitErr, s.exitSeen = err, true
		close(s.exitNow)
		s.exitMu.Unlock()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if _, ok := s.exitResult(sidecarTimeout); !ok {
			t.Errorf("the sidecar outlived the test")
		}
	})
	return s
}

// exitResult returns the child's exit error once it has exited. It reports
// false while the child is still running or did not exit within deadline.
// Reading it consumes nothing: every waiter may call it.
func (s *sidecarSession) exitResult(deadline time.Duration) (error, bool) {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		s.exitMu.Lock()
		seen, err := s.exitSeen, s.exitErr
		s.exitMu.Unlock()
		if seen {
			return err, true
		}
		select {
		case <-s.exitNow:
		case <-timer.C:
			return nil, false
		}
	}
}

func (s *sidecarSession) writeFrame(t *testing.T, frame *desktoppb.Frame) {
	t.Helper()
	require.NoError(t, WriteFrame(s.stdin, frame))
}

// readResponse reads frames until a response with the given id arrives,
// passing event frames on the way.
func (s *sidecarSession) readResponse(t *testing.T, id uint64) *desktoppb.Response {
	t.Helper()
	timer := time.NewTimer(sidecarTimeout)
	defer timer.Stop()
	reader := bufio.NewReader(s.stdout)
	type readResult struct {
		frame *desktoppb.Frame
		err   error
	}
	for {
		results := make(chan readResult, 1)
		go func() {
			frame, err := ReadFrame(reader)
			results <- readResult{frame, err}
		}()
		var frame *desktoppb.Frame
		select {
		case result := <-results:
			require.NoError(t, result.err)
			frame = result.frame
		case <-timer.C:
			t.Fatalf("no response %d arrived\nstderr:\n%s", id, s.stderrM.String())
		}
		if response := frame.GetResponse(); response != nil && response.GetId() == id {
			return response
		}
	}
}

// dropAsDeadShell closes all three of the test's pipe ends at once, the way a
// dying shell does: the sidecar then reads EOF on stdin and writes into pipes
// with no reader. The stderr drain stops at the close, so nothing logged
// after this point reaches stderrM.
func (s *sidecarSession) dropAsDeadShell() {
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	_ = s.stderr.Close()
}

// waitExit waits for the sidecar to exit, and fails the test on a signal.
func (s *sidecarSession) waitExit(t *testing.T) {
	t.Helper()
	err, ok := s.exitResult(sidecarTimeout)
	if !ok {
		t.Fatalf("the sidecar did not exit\nstderr:\n%s", s.stderrM.String())
	}
	require.NoError(t, err, "the sidecar must exit 0, not die by signal\nstderr:\n%s", s.stderrM.String())
}

func connectSoloFrame(id uint64) *desktoppb.Frame {
	return &desktoppb.Frame{Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
		Id:     id,
		Method: &desktoppb.Request_ConnectSolo{ConnectSolo: &desktoppb.ConnectSoloRequest{}},
	}}}
}

// waitStderr waits until the drained stderr holds line. The pipes stay open,
// so the drain goroutine keeps collecting. The sidecar can log the line and
// exit in the same poll window, so the exit observation never consumes
// anything (exitResult) and every wake re-checks the buffer first.
func (s *sidecarSession) waitStderr(t *testing.T, line string) {
	t.Helper()
	timer := time.NewTimer(sidecarTimeout)
	defer timer.Stop()
	for {
		if strings.Contains(s.stderrM.String(), line) {
			return
		}
		if err, ok := s.exitResult(0); ok {
			t.Fatalf("the sidecar exited before it logged %q: %v\nstderr:\n%s", line, err, s.stderrM.String())
		}
		select {
		case <-timer.C:
			t.Fatalf("the sidecar never logged %q\nstderr:\n%s", line, s.stderrM.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A dying shell closes every pipe at once. The sidecar must still run its
// whole drain -- whose log writes now report EPIPE -- and exit 0.
func TestSidecarFinishesShutdownAfterTheShellDisappears(t *testing.T) {
	s := startSoloSidecar(t)

	s.dropAsDeadShell()
	s.waitExit(t)
}

// The shell gives up on a slow quit: the Shutdown frame goes out, and the
// pipes close before the reply. The sidecar must still finish the drain and
// exit 0.
func TestSidecarFinishesShutdownWhenTheShellLeavesDuringADrain(t *testing.T) {
	s := startSoloSidecar(t)

	s.writeFrame(t, &desktoppb.Frame{Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
		Id:     2,
		Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
	}}})
	s.dropAsDeadShell()
	s.waitExit(t)
}

// startSoloSidecar is the common setup of the sidecar tests: a sidecar whose
// solo session is up and whose pipes stay open.
func startSoloSidecar(t *testing.T) *sidecarSession {
	t.Helper()
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)
	s := startSidecarSession(t)
	s.writeFrame(t, connectSoloFrame(1))
	response := s.readResponse(t, 1)
	require.Empty(t, response.GetError(), "ConnectSolo must succeed\nstderr:\n%s", s.stderrM.String())
	return s
}

// hammerSignal sends sig in a tight loop until a delivery succeeds or the
// process exits. With the registration kept for the whole process life every
// delivery is dropped, so successful deliveries cannot harm the drain. Without
// it, the first signal restores the default disposition, and a delivery inside
// the drain -- the whole window between the restore and the exit -- kills the
// process, which is the failure this loop exists to catch.
func (s *sidecarSession) hammerSignal(t *testing.T, sig os.Signal) {
	t.Helper()
	for deliveries := 0; deliveries < 200; deliveries++ {
		err := s.cmd.Process.Signal(sig)
		if err == nil {
			continue
		}
		// The process is gone (reaped or exiting): no further delivery can
		// reach it.
		if _, ok := s.exitResult(sidecarTimeout); ok {
			return
		}
		t.Fatalf("the sidecar refused every signal and never exited: %v", err)
	}
}

// A signal that arrives while a drain runs must do nothing: the registration
// is never stopped, so a later SIGTERM is dropped instead of killing a process
// that is releasing the lease. The wait for "connected to hub" keeps the
// timing out of the race: the in-process worker of a just-started solo is
// still migrating its database, and a stop before that line cancels the
// startup before the state-drain phase ever begins.
func TestSidecarIgnoresASignalDuringShutdown(t *testing.T) {
	s := startSoloSidecar(t)
	s.waitStderr(t, "connected to hub")

	require.NoError(t, s.cmd.Process.Signal(syscall.SIGTERM))
	s.waitStderr(t, "draining worker state for graceful shutdown")
	s.hammerSignal(t, syscall.SIGTERM)
	s.waitExit(t)
	assert.Contains(t, s.stderrM.String(), "released the Hub runtime lease",
		"the drain must finish and release the lease before the exit")
}

// A signal during a shutdown REQUEST takes the app.ctx.Done() path, which
// every normal quit takes: the request cancels app.ctx, the signal watcher
// bows out, and the SIGTERM must not force-terminate the drain.
func TestSidecarIgnoresASignalDuringAShutdownRequest(t *testing.T) {
	s := startSoloSidecar(t)
	s.waitStderr(t, "connected to hub")

	s.writeFrame(t, &desktoppb.Frame{Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
		Id:     2,
		Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
	}}})
	s.waitStderr(t, "draining worker state for graceful shutdown")
	s.hammerSignal(t, syscall.SIGTERM)
	s.waitExit(t)
	assert.Contains(t, s.stderrM.String(), "released the Hub runtime lease",
		"the drain must finish and release the lease before the exit")
}

//go:build !windows

package terminal

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// The orderly-shutdown sweep for terminals: HangUpAll hangs up every shell,
// escalates only for one that is still alive after its grace, and joins every
// exit goroutine. The hang-up must be the SIGNAL and not the close: on macOS
// creack/pty opens a blocking master, so a close alone delivers no hang-up and
// every shell would reach the escalation that kills nohup jobs.

// hangUpShellTimeout is the generous ceiling for one wait on a real shell.
// Only a failure path waits for it.
const hangUpShellTimeout = 30 * time.Second

// hangUpGrace is the per-shell escalation grace the tests pass.
const hangUpGrace = 2 * time.Second

type blockedTeardownPTY struct {
	pty.UnixPty
	beforeWrite func()
	beforeClose func()
}

func (p *blockedTeardownPTY) Write(data []byte) (int, error) {
	if p.beforeWrite != nil {
		p.beforeWrite()
	}
	return p.UnixPty.Write(data)
}

func (p *blockedTeardownPTY) Close() error {
	if p.beforeClose != nil {
		p.beforeClose()
	}
	return p.UnixPty.Close()
}

func TestTerminalTerminationSurvivesBlockedPTY(t *testing.T) {
	for _, operation := range []string{"hang-up", "stop"} {
		for _, blockedOperation := range []string{"write", "close"} {
			t.Run(operation+"/"+blockedOperation, func(t *testing.T) {
				t.Parallel()
				clock := testutil.NewQuartzMock(t)
				m := NewManager(WithClock(clock))
				readHeld := make(chan struct{})
				readRelease := make(chan struct{})
				ioEntered := make(chan struct{})
				ioRelease := make(chan struct{})
				releaseRead := sync.OnceFunc(func() { close(readRelease) })
				releaseIO := sync.OnceFunc(func() { close(ioRelease) })
				var inputDone, terminationDone chan struct{}
				// Trap cleanup runs before this join, so a failed timer
				// assertion cannot strand teardown inside the trap.
				t.Cleanup(func() {
					releaseRead()
					releaseIO()
					m.StopTerminal("blocked")
					m.WaitForExit("blocked")
					if inputDone != nil {
						<-inputDone
					}
					if terminationDone != nil {
						<-terminationDone
					}
				})
				newTimer := clock.Trap().NewTimer(terminalHangUpTimerTag)
				t.Cleanup(newTimer.Close)
				var output testutil.LogBuffer
				var readOnce sync.Once
				const marker = "LMX_TEARDOWN_READY"
				require.NoError(t, m.StartTerminal(t.Context(), Options{
					ID: "blocked", Shell: testutil.TestShell(), WorkingDir: t.TempDir(),
				}, func(data []byte, _ int64, _ []Signal) {
					_, _ = output.Write(data)
					if strings.Contains(output.String(), marker) {
						readOnce.Do(func() {
							close(readHeld)
							<-readRelease
						})
					}
				}, nil))
				readyCommand, err := testutil.TestShellOutputCommand(marker)
				require.NoError(t, err)
				require.NoError(t, m.SendInput("blocked", []byte("trap '' HUP; "+readyCommand+"; while :; do sleep 500; done"+testutil.TestShellEnter())))
				ctx, cancel := context.WithTimeout(t.Context(), hangUpShellTimeout)
				defer cancel()
				select {
				case <-readHeld:
				case <-ctx.Done():
					t.Fatal("the shell never installed its hang-up trap")
				}
				term := hangUpTerminal(m, "blocked")
				require.NotNil(t, term)
				nativePTY, ok := term.ptmx.(pty.UnixPty)
				require.True(t, ok)
				blocked := &blockedTeardownPTY{UnixPty: nativePTY}
				block := sync.OnceFunc(func() {
					close(ioEntered)
					<-ioRelease
				})
				if blockedOperation == "write" {
					blocked.beforeWrite = block
				} else {
					blocked.beforeClose = block
				}
				// The output callback holds the reader between PTY calls.
				// The live shell cannot reach exit teardown before this swap.
				term.ptmx = blocked
				releaseRead()
				if blockedOperation == "write" {
					inputDone = make(chan struct{})
					go func() {
						defer close(inputDone)
						// Forced termination may fail this admitted write.
						_ = m.SendInput("blocked", []byte("held input"))
					}()
					select {
					case <-ioEntered:
					case <-ctx.Done():
						t.Fatal("the input write never reached its hold")
					}
				}
				terminationDone = make(chan struct{})
				var stragglers []string
				go func() {
					defer close(terminationDone)
					if operation == "hang-up" {
						stragglers = m.HangUpAll(hangUpGrace)
					} else {
						term.Stop()
					}
				}()
				if operation == "hang-up" {
					newTimer.MustWait(ctx).MustRelease(ctx)
					clock.Advance(hangUpGrace).MustWait(ctx)
				}
				select {
				case <-term.exitCh:
				case <-ctx.Done():
					t.Fatal("PTY teardown prevented forced process termination")
				}
				releaseIO()
				select {
				case <-terminationDone:
				case <-ctx.Done():
					t.Fatal("termination did not join the released PTY operation")
				}
				assert.Empty(t, stragglers)
			})
		}
	}
}

// startHangUpTerminal starts a shell in the manager under id, with an exit
// handler that reports through handlerRan. command, when not empty, is sent to
// the shell after StartTerminal returns. The output buffer records readiness.
func startHangUpTerminal(t *testing.T, m *Manager, id string, handlerRan chan string, command string) *testutil.LogBuffer {
	t.Helper()
	output := &testutil.LogBuffer{}
	err := m.StartTerminal(context.Background(), Options{
		ID:         id,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func(data []byte, _ int64, _ []Signal) { _, _ = output.Write(data) }, func(terminalID string, _ int) {
		if handlerRan != nil {
			handlerRan <- terminalID
		}
	})
	require.NoError(t, err, "StartTerminal(%s)", id)
	if command != "" {
		require.NoError(t, m.SendInput(id, []byte(command+testutil.TestShellEnter())), "SendInput(%s)", id)
	}
	return output
}

// hangUpTerminal returns the live terminal of id. Caller holds no lock.
func hangUpTerminal(m *Manager, id string) *Terminal {
	m.mu.RLock()
	defer m.mu.RUnlock()
	term, _ := m.currentTerminalLocked(id)
	return term
}

// waitForHandler waits for the exit handler of one terminal.
func waitForHandler(t *testing.T, handlerRan chan string, want string) {
	t.Helper()
	select {
	case got := <-handlerRan:
		require.Equal(t, want, got)
	case <-time.After(hangUpShellTimeout):
		t.Fatalf("the exit handler of %s never ran", want)
	}
}

// A live idle shell ends on the hang-up alone: the handler runs before the
// return, and no escalation ran -- HangUp claims no `stopped`. With a
// close-only HangUp the shell survives, and this parks on the unadvanced mock
// clock on macOS, where a close defers the real close behind the parked read.
func TestManager_HangUpAllEndsAShell(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	m := NewManager(WithClock(clock))
	handlerRan := make(chan string, 1)
	startHangUpTerminal(t, m, "plain", handlerRan, "")

	ids := m.HangUpAll(hangUpGrace)

	assert.Nil(t, ids, "a shell that dies on the hang-up is no straggler")
	waitForHandler(t, handlerRan, "plain")
	term := hangUpTerminal(m, "plain")
	require.NotNil(t, term)
	assert.True(t, term.IsExited())
	assert.False(t, term.stopped, "the escalation cannot satisfy this test")
}

// A nohup job survives the sweep: the hang-up reaches the shell, the shell
// exits, and the escalation that would kill the job never runs.
func TestManager_HangUpAllLeavesANohupJobAlive(t *testing.T) {
	m := NewManager()
	pidFile := t.TempDir() + "/nohup.pid"
	handlerRan := make(chan string, 1)
	startHangUpTerminal(t, m, "nohup", handlerRan,
		"nohup sh -c 'echo $$ > "+pidFile+"; exec sleep 1000' >/dev/null 2>&1 &")
	pid := waitForShellJobPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	ids := m.HangUpAll(hangUpGrace)

	assert.Nil(t, ids)
	waitForHandler(t, handlerRan, "nohup")
	// The job's parent is gone; give the re-parenting a moment before the
	// liveness read, so the assertion cannot pass on a not-yet-reaped zombie.
	assert.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) == nil
	}, hangUpShellTimeout, 10*time.Millisecond,
		"the nohup job must survive the sweep")
}

// A foreground job dies with the shell: the hang-up ends the session's
// controlling terminal, and the kernel hangs up the foreground group with it.
// The sweep joins the shell and not the job.
func TestManager_HangUpAllEndsAForegroundJob(t *testing.T) {
	m := NewManager()
	pidFile := t.TempDir() + "/fg.pid"
	handlerRan := make(chan string, 1)
	startHangUpTerminal(t, m, "fg", handlerRan,
		"sh -c 'echo $$ > "+pidFile+"; exec sleep 1000'")
	pid := waitForShellJobPID(t, pidFile)

	ids := m.HangUpAll(hangUpGrace)

	assert.Nil(t, ids)
	waitForHandler(t, handlerRan, "fg")
	assert.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) != nil
	}, hangUpShellTimeout, 10*time.Millisecond,
		"the foreground job must die with the shell")
}

// A shell that ignores the hang-up meets the escalation at the grace: the
// mock clock advances, Stop claims the terminal, and the sweep reports no
// straggler.
func TestManager_HangUpAllEscalatesForAShellThatIgnoresHangUp(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, terminalHangUpTimerTag)
	m := NewManager(WithClock(clock))
	handlerRan := make(chan string, 1)
	startHangUpIgnoringTerminal(t, m, "stubborn", handlerRan)

	done := make(chan []string, 1)
	go func() { done <- m.HangUpAll(hangUpGrace) }()

	// The advance must not run before the sweep armed its grace timer, so the
	// trap holds the arm, the release completes it, and the advance then finds
	// the event scheduled.
	newTimer.MustWait(t.Context()).MustRelease(t.Context())
	testutil.AdvanceAndAwaitStop(t, t.Context(), clock, hangUpGrace, stopTimer)

	select {
	case ids := <-done:
		assert.Nil(t, ids, "the escalation must end the stubborn shell")
	case <-time.After(hangUpShellTimeout):
		t.Fatal("HangUpAll did not return after the escalation")
	}
	waitForHandler(t, handlerRan, "stubborn")
	term := hangUpTerminal(m, "stubborn")
	require.NotNil(t, term)
	assert.True(t, term.IsExited())
	assert.True(t, term.stopped, "the escalation must have claimed the terminal")
}

// One stubborn shell must not delay another's sweep: the plain shell's exit
// handler runs before the clock advances, while the stubborn one is still
// waiting out its grace.
func TestManager_HangUpAllDoesNotLetOneShellDelayAnother(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, terminalHangUpTimerTag)
	m := NewManager(WithClock(clock))
	handlerRan := make(chan string, 2)
	startHangUpIgnoringTerminal(t, m, "stubborn", handlerRan)
	startHangUpTerminal(t, m, "plain", handlerRan, "")

	done := make(chan []string, 1)
	go func() { done <- m.HangUpAll(hangUpGrace) }()

	// Each shell's sweep goroutine arms one grace timer; release both arms so
	// the events are scheduled.
	newTimer.MustWait(t.Context()).MustRelease(t.Context())
	newTimer.MustWait(t.Context()).MustRelease(t.Context())

	// The plain shell's handler runs on the hang-up alone, on the unadvanced
	// clock. Its exit path stops the timer it armed, and the trap holds
	// that stop.
	waitForHandler(t, handlerRan, "plain")
	stopTimer.MustWait(t.Context()).MustRelease(t.Context())
	select {
	case ids := <-done:
		t.Fatalf("HangUpAll returned before the clock advanced: %v", ids)
	default:
	}

	testutil.AdvanceAndAwaitStop(t, t.Context(), clock, hangUpGrace, stopTimer)
	select {
	case ids := <-done:
		assert.Nil(t, ids)
	case <-time.After(hangUpShellTimeout):
		t.Fatal("HangUpAll did not return after the escalation")
	}
	waitForHandler(t, handlerRan, "stubborn")
}

// An exit handler that parks past the join budget is abandoned, and its id is
// what the sweep reports. The budget is real time, so this test spends it.
func TestManager_HangUpAllReturnsAStraggler(t *testing.T) {
	m := NewManager()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	err := m.StartTerminal(context.Background(), Options{
		ID:         "straggler",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {}, func(string, int) {
		<-release
	})
	require.NoError(t, err)

	done := make(chan []string, 1)
	go func() { done <- m.HangUpAll(hangUpGrace) }()

	select {
	case ids := <-done:
		assert.Equal(t, []string{"straggler"}, ids,
			"the parked exit handler must surface as the id the sweep abandoned")
	case <-time.After(hangUpGrace + hangUpJoinGrace + hangUpShellTimeout):
		t.Fatal("HangUpAll did not give up at its budget")
	}
}

// RemoveTerminal removes the current registration. The draining registrations
// retain its exit goroutine: a handler parked
// past its tab's removal still holds the sweep until it returns.
func TestManager_HangUpAllJoinsARemovedTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager()
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseFn)
		err := m.StartTerminal(context.Background(), Options{
			ID:         "removed",
			Shell:      testutil.TestShell(),
			WorkingDir: t.TempDir(),
		}, func([]byte, int64, []Signal) {}, func(string, int) {
			close(entered)
			<-release
		})
		require.NoError(t, err)
		term := hangUpTerminal(m, "removed")
		m.RemoveTerminal("removed")
		<-entered
		<-term.readDoneCh
		<-term.childSideClosedCh

		done := make(chan []string, 1)
		go func() { done <- m.HangUpAll(hangUpGrace) }()

		synctest.Wait()
		select {
		case ids := <-done:
			t.Fatalf("HangUpAll returned while the removed terminal's handler was parked: %v", ids)
		default:
		}

		releaseFn()
		select {
		case <-done:
		case <-time.After(hangUpShellTimeout):
			t.Fatal("HangUpAll did not return after the release")
		}

	})
}

// A restart replaces the exit handler of the old incarnation. The old
// registration is no longer current, so the sweep joins its draining
// registration too.
func TestManager_HangUpAllJoinsAReplacedExitHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager()
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseFn)
		err := m.StartTerminal(context.Background(), Options{
			ID:         "replaced",
			Shell:      testutil.TestShell(),
			WorkingDir: t.TempDir(),
		}, func([]byte, int64, []Signal) {}, func(string, int) {
			close(entered)
			<-release
		})
		require.NoError(t, err)
		oldTerm := hangUpTerminal(m, "replaced")
		require.NotNil(t, oldTerm)
		// The restart refuses a running terminal, and the old shell's exit is
		// what runs its exit goroutine into the parked handler.
		require.NoError(t, m.SendInput("replaced", []byte("exit"+testutil.TestShellEnter())))
		<-entered
		<-oldTerm.readDoneCh
		<-oldTerm.childSideClosedCh

		// The restart replaces the PTY while the old exit goroutine is parked in
		// the handler above, and installs a fresh one.
		require.NoError(t, m.RestartTerminal(context.Background(), Options{
			ID:         "replaced",
			Shell:      testutil.TestShell(),
			WorkingDir: t.TempDir(),
			Cols:       80,
			Rows:       24,
		}, 0, func([]byte, int64, []Signal) {}, nil))

		replacement := hangUpTerminal(m, "replaced")
		m.StopTerminal("replaced")
		m.WaitForExit("replaced")
		<-replacement.readDoneCh
		<-replacement.childSideClosedCh

		done := make(chan []string, 1)
		go func() { done <- m.HangUpAll(hangUpGrace) }()

		synctest.Wait()
		select {
		case ids := <-done:
			t.Fatalf("HangUpAll returned while the replaced terminal's old handler was parked: %v", ids)
		default:
		}

		releaseFn()
		select {
		case <-done:
		case <-time.After(hangUpShellTimeout):
			t.Fatal("HangUpAll did not return after the release")
		}

		assert.Same(t, replacement, hangUpTerminal(m, "replaced"))
		meta, ok := m.GetMeta("replaced")
		require.True(t, ok)
		assert.Equal(t, uint32(80), meta.Cols)
		assert.True(t, m.ExitHandlerFinished("replaced"))

	})
}

func TestManager_HangUpAllWithNoTerminals(t *testing.T) {
	m := NewManager()

	assert.Nil(t, m.HangUpAll(hangUpGrace), "no terminal runs, so HangUpAll returns at once")
}

// An exited terminal takes no part in the sweep: the escalation must not run
// for a shell that already ended, which is what keeps it from killing the
// nohup jobs a hang-up left alive.
func TestManager_HangUpAllSkipsAnExitedTerminal(t *testing.T) {
	m := NewManager()
	startHangUpTerminal(t, m, "gone", nil, "exit")
	deadline := time.Now().Add(hangUpShellTimeout)
	for {
		term := hangUpTerminal(m, "gone")
		if term != nil && term.IsExited() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the shell never exited on its own")
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.Nil(t, m.HangUpAll(hangUpGrace))
	term := hangUpTerminal(m, "gone")
	require.NotNil(t, term)
	assert.False(t, term.stopped, "an exited terminal must not be escalated")
}

// HangUp leaves the escalation to the caller: a shell that ignored the hang-up
// ends when Stop follows it, and Stop claims `stopped`.
func TestTerminal_StopAfterHangUpEndsAStubbornShell(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	m := NewManager(WithClock(clock))
	startHangUpIgnoringTerminal(t, m, "stubborn", nil)
	term := hangUpTerminal(m, "stubborn")
	require.NotNil(t, term)

	term.HangUp()
	assert.False(t, term.stopped, "HangUp claims no stopped")
	assert.False(t, term.IsExited(), "the stubborn shell must survive the hang-up")

	term.Stop()

	assert.True(t, term.stopped, "Stop claims stopped")
	assert.Eventually(t, term.IsExited, hangUpShellTimeout, 10*time.Millisecond,
		"the escalation must end the stubborn shell")
}

func waitForShellJobPID(t *testing.T, path string) int {
	t.Helper()
	var contents string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		contents = strings.TrimSpace(string(data))
		return err == nil && contents != ""
	}, hangUpShellTimeout, 10*time.Millisecond, "the shell job did not write its PID to %s", path)
	pid, err := strconv.Atoi(contents)
	require.NoError(t, err, "the shell job wrote an invalid PID to %s", path)
	require.Positive(t, pid)
	return pid
}

func startHangUpIgnoringTerminal(t *testing.T, m *Manager, id string, handlerRan chan string) {
	t.Helper()
	const marker = "LMX_HANGUP_TRAP_READY"
	emit, err := testutil.TestShellOutputCommand(marker)
	require.NoError(t, err)
	output := startHangUpTerminal(t, m, id, handlerRan, "trap '' HUP; "+emit+"; while :; do sleep 500; done")
	require.Eventually(t, func() bool { return strings.Contains(output.String(), marker) }, hangUpShellTimeout, 10*time.Millisecond,
		"the shell did not install its hang-up trap")
}

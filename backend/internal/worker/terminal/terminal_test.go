package terminal

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminal_StartAndStop(t *testing.T) {
	var mu sync.Mutex
	var output []byte

	term, err := Start(context.Background(), Options{
		ID:         "test-1",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, _ int64, _ []Signal) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	})
	require.NoError(t, err, "Start")

	// Send a command.
	require.NoError(t, term.SendInput([]byte("echo hello"+testutil.TestShellEnter())), "SendInput")

	// Wait for output.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "hello")
	}, "expected output to contain 'hello'")

	// Stop the terminal.
	term.Stop()
	exitCode := term.Wait()
	t.Logf("exit code: %d", exitCode)

	// Double stop is safe.
	term.Stop()
}

// When the desktop app runs as a Linux AppImage, the runtime exports
// ARGV0 (= AppImage filename), and zsh interprets that env var as the
// argv[0] to use when execing every external command (AppImageKit#852).
// Terminal tabs spawn the user's login shell directly; if ARGV0 leaks
// through, mise's shim sees argv[0] = the AppImage filename and bails
// with "<file>.AppImage is not a valid shim" (jdx/mise#3537) — the same
// failure mode commit 5d430a3 fixed for agent shells. Scrub APPIMAGE,
// APPDIR, ARGV0, and OWD whenever APPIMAGE is set on the parent.
func TestTerminal_AppImage_ScrubsEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("AppImage is a Linux runtime; POSIX-only test")
	}

	t.Setenv("APPIMAGE", "/path/to/leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_xxxxxx")
	t.Setenv("ARGV0", "leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("OWD", "/home/user")

	var mu sync.Mutex
	var output []byte

	term, err := Start(context.Background(), Options{
		ID:         "test-appimage-scrub",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, _ int64, _ []Signal) {
		mu.Lock()
		defer mu.Unlock()
		output = append(output, data...)
	})
	require.NoError(t, err, "Start")
	defer func() {
		term.Stop()
		term.Wait()
	}()

	// Sentinel-bracket the value so we can disambiguate the echoed line
	// from any prompt / banner / set-x output the shell may emit. An
	// interactive PTY echoes the raw input *and* the shell's readline
	// prints it back, both with the literal `${ARGV0}` text. Only the
	// executed output produces a `<S>…<E>` block free of `$`, so wait
	// for that to appear before reading the last block.
	require.NoError(t, term.SendInput([]byte("echo \"<S>${ARGV0}<E>\""+testutil.TestShellEnter())), "SendInput")

	executedBlock := func(s string) (between string, ok bool) {
		rest := s
		for {
			i := strings.Index(rest, "<S>")
			if i == -1 {
				return between, ok
			}
			rest = rest[i+len("<S>"):]
			j := strings.Index(rest, "<E>")
			if j == -1 {
				return between, ok
			}
			candidate := rest[:j]
			if !strings.Contains(candidate, "$") {
				between = candidate
				ok = true
			}
			rest = rest[j+len("<E>"):]
		}
	}

	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		_, ok := executedBlock(string(output))
		return ok
	}, "expected executed echo output (a <S>…<E> block without `$`)")

	mu.Lock()
	got := string(output)
	mu.Unlock()
	// After the scrub: ARGV0 is unset → echo prints "<S><E>".
	// Without the scrub: ARGV0 leaks → echo prints "<S>leapmux-…AppImage<E>".
	between, ok := executedBlock(got)
	require.True(t, ok, "no executed-output sentinel in PTY buffer: %q", got)
	assert.Empty(t, between, "ARGV0 should be scrubbed inside an AppImage launch; leaked %q", between)
}

func TestTerminal_Resize(t *testing.T) {
	term, err := Start(context.Background(), Options{
		ID:         "test-resize",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func([]byte, int64, []Signal) {})
	require.NoError(t, err, "Start")
	defer func() {
		term.Stop()
		term.Wait()
	}()

	assert.NoError(t, term.Resize(120, 40), "Resize")
}

func TestTerminal_SendInputAfterStop(t *testing.T) {
	term, err := Start(context.Background(), Options{
		ID:         "test-stopped",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {})
	require.NoError(t, err, "Start")

	term.Stop()
	term.Wait()

	assert.Error(t, term.SendInput([]byte("echo fail"+testutil.TestShellEnter())), "expected error sending input after stop")
}

func TestTerminal_IsExited(t *testing.T) {
	term, err := Start(context.Background(), Options{
		ID:         "test-exited",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {})
	require.NoError(t, err, "Start")

	assert.False(t, term.IsExited(), "expected IsExited = false before stop")

	term.Stop()
	term.Wait()

	assert.True(t, term.IsExited(), "expected IsExited = true after stop")
}

func TestManager_StartAndRemove(t *testing.T) {
	m := NewManager()

	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-1",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {}, nil)
	require.NoError(t, err, "StartTerminal")

	assert.True(t, m.HasTerminal("tm-1"), "expected HasTerminal = true")

	// Duplicate should fail.
	err = m.StartTerminal(context.Background(), Options{
		ID:         "tm-1",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {}, nil)
	assert.Error(t, err, "expected error for duplicate terminal")

	// StopTerminal keeps it in the map (exited but not removed).
	m.StopTerminal("tm-1")
	testutil.AssertEventually(t, func() bool {
		return m.IsExited("tm-1")
	}, "expected IsExited = true after StopTerminal")

	assert.True(t, m.HasTerminal("tm-1"), "expected HasTerminal = true after StopTerminal (kept in map)")

	// RemoveTerminal removes from the map.
	m.RemoveTerminal("tm-1")

	assert.False(t, m.HasTerminal("tm-1"), "expected HasTerminal = false after RemoveTerminal")
}

func TestManager_ExitedTerminalRejectsInput(t *testing.T) {
	m := NewManager()

	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-exit",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {}, nil)
	require.NoError(t, err, "StartTerminal")

	// Stop and wait for exit.
	m.StopTerminal("tm-exit")
	testutil.AssertEventually(t, func() bool {
		return m.IsExited("tm-exit")
	}, "expected terminal to exit")

	// Input should fail on exited terminal.
	assert.Error(t, m.SendInput("tm-exit", []byte("test")), "expected error sending input to exited terminal")

	// Resize should also fail.
	assert.Error(t, m.Resize("tm-exit", 80, 24), "expected error resizing exited terminal")

	m.RemoveTerminal("tm-exit")
}

func TestManager_ExitNotification(t *testing.T) {
	m := NewManager()
	exitCh := make(chan struct{})
	var gotID string
	var gotCode int

	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-notify",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, func([]byte, int64, []Signal) {}, func(id string, code int) {
		gotID = id
		gotCode = code
		close(exitCh)
	})
	require.NoError(t, err, "StartTerminal")

	m.StopTerminal("tm-notify")

	select {
	case <-exitCh:
	case <-time.After(5 * time.Second):
		require.Fail(t, "timed out waiting for exit notification")
	}

	assert.Equal(t, "tm-notify", gotID, "exit notification ID")
	// Exit code from kill is typically -1 or non-zero.
	t.Logf("exit code: %d", gotCode)

	m.RemoveTerminal("tm-notify")
}

// TestInstallTerminal_ExitHandlerWaitsForTheReaderToDrain pins the order the
// exit handler depends on. That handler persists the terminal's final screen,
// and the child being reaped says nothing about the reader: readOutput runs on
// its own goroutine, and readDoneCh is the signal that it stopped writing. A
// handler that runs first snapshots a screen the reader can still grow, and a
// natural exit persists only once, so the row keeps the truncated copy.
//
// The two signals are driven by hand rather than by a real shell. Through a
// PTY the reader wins this race on any unloaded machine -- waitForExit polls
// for the process group's death before it closes exitCh, which hands the reader
// all the time it needs -- so a shell-driven version of this test passes
// whether the wait is there or not. That is exactly how the loss reached CI:
// visible only on a runner slow enough to lose the race.
func TestInstallTerminal_ExitHandlerWaitsForTheReaderToDrain(t *testing.T) {
	m := NewManager()
	const id = "tm-exit-order"

	// No PTY: only the two channels installTerminal orders against.
	term := &Terminal{
		id:         id,
		exitCh:     make(chan struct{}),
		readDoneCh: make(chan struct{}),
		screenBuf:  NewScreenBuffer(),
	}
	ran := make(chan struct{})
	m.installTerminal(id, term, func(string, int) { close(ran) },
		func(TerminalMeta) TerminalMeta { return TerminalMeta{} })

	// The child is reaped, and the reader has not finished.
	close(term.exitCh)
	select {
	case <-ran:
		require.Fail(t, "the exit handler ran while the reader could still write the screen it persists")
	case <-time.After(100 * time.Millisecond):
		// The window only bounds a "has not happened yet" claim, so a slow
		// machine can only make this case weaker, never make it fail wrongly.
		// Without the wait the handler runs at once and this fails every run.
	}

	close(term.readDoneCh)
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the exit handler never ran after the reader drained")
	}
	m.WaitForExit(id)
}

func TestManager_StopAll(t *testing.T) {
	m := NewManager()

	for _, id := range []string{"a", "b"} {
		err := m.StartTerminal(context.Background(), Options{
			ID:         id,
			Shell:      testutil.TestShell(),
			WorkingDir: t.TempDir(),
		}, func([]byte, int64, []Signal) {}, nil)
		require.NoError(t, err, "StartTerminal(%s)", id)
	}

	m.StopAll()

	for _, id := range []string{"a", "b"} {
		id := id
		testutil.AssertEventually(t, func() bool {
			return !m.HasTerminal(id)
		}, "HasTerminal(%s) = true after StopAll", id)
	}
}

func TestManager_StopUnknown(t *testing.T) {
	m := NewManager()
	// Should not panic.
	m.StopTerminal("nonexistent")
	m.RemoveTerminal("nonexistent")
}

func TestManager_SendInput_UnknownTerminal(t *testing.T) {
	m := NewManager()
	err := m.SendInput("nonexistent", []byte("hello"))
	assert.Error(t, err, "expected error for unknown terminal")
	assert.ErrorIs(t, err, ErrTerminalNotFound, "error should wrap ErrTerminalNotFound")
}

func TestManager_Resize_UnknownTerminal(t *testing.T) {
	m := NewManager()
	err := m.Resize("nonexistent", 80, 24)
	assert.Error(t, err, "expected error for unknown terminal")
	assert.ErrorIs(t, err, ErrTerminalNotFound, "error should wrap ErrTerminalNotFound")
}

func TestManager_Resize_SameDimensions(t *testing.T) {
	m := NewManager()
	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-resize-noop",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func([]byte, int64, []Signal) {}, nil)
	require.NoError(t, err)
	defer m.StopAll()

	// Resize to same dimensions should be a no-op (no spurious SIGWINCH).
	assert.NoError(t, m.Resize("tm-resize-noop", 80, 24))

	// Resize to different dimensions should succeed.
	assert.NoError(t, m.Resize("tm-resize-noop", 120, 40))

	// Resize to same (new) dimensions should be a no-op again.
	assert.NoError(t, m.Resize("tm-resize-noop", 120, 40))
}

func TestManager_ScreenSnapshotSince(t *testing.T) {
	m := NewManager()
	var mu sync.Mutex
	var output []byte

	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-snap",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, _ int64, _ []Signal) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	}, nil)
	require.NoError(t, err, "StartTerminal")
	require.True(t, m.AppendOutput("tm-snap", []byte(conPTYStyleModeBytes)))

	// Send a command to produce output.
	require.NoError(t, m.SendInput("tm-snap", []byte("echo snapshot_test"+testutil.TestShellEnter())), "SendInput")

	// Wait for the output to arrive.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "snapshot_test")
	}, "expected output to contain 'snapshot_test'")

	// Verify a cold subscribe (afterOffset=0) returns the retained bytes
	// and a matching offset. The shell stays live, so assert only what a
	// single atomic read of the buffer states — a second read could see
	// more bytes.
	snap, offset, isSnap := m.ScreenSnapshotSince("tm-snap", 0)
	assert.NotEmpty(t, snap, "expected non-empty screen snapshot")
	assert.Contains(t, string(snap), "snapshot_test", "snapshot should contain the echoed text")
	assert.True(t, isSnap, "afterOffset 0 is a forced resync, not an incremental delta")
	require.Less(t, offset, int64(screenBufferSize), "test output must stay inside the ring")
	// The snapshot is the tracker's synthesized mode prefix followed by
	// the retained PTY bytes, and the offset counts those PTY bytes
	// alone. So the snapshot is strictly LONGER than the offset once any
	// sticky mode is set — cmd.exe sets a window title and hides the
	// cursor at startup, /bin/sh sets neither, and the injection above
	// makes the case identical on both. Equating len(snap) with the
	// offset asserts an empty prefix, which is what broke on Windows;
	// inflating the offset by the prefix breaks this too, in the other
	// direction.
	assert.Greater(t, int64(len(snap)), offset,
		"the injected mode bytes must produce a restore prefix on top of the counted bytes")

	m.StopTerminal("tm-snap")
	testutil.AssertEventually(t, func() bool {
		return m.IsExited("tm-snap")
	}, "expected terminal to exit")
	m.RemoveTerminal("tm-snap")
}

func TestManager_ScreenSnapshotSince_UnknownTerminal(t *testing.T) {
	m := NewManager()
	snap, offset, isSnap := m.ScreenSnapshotSince("nonexistent", 0)
	assert.Nil(t, snap, "expected nil snapshot for unknown terminal")
	assert.Zero(t, offset, "expected zero offset for unknown terminal")
	assert.False(t, isSnap)
}

// TestManager_SnapshotAfterExit: after the shell exits, the Terminal
// stays in the Manager until RemoveTerminal is called, so snapshots must
// continue to return the final screen state + offset. The exit notice
// is appended to the buffer on exit by the service layer, and
// subscribers that reconnect need those bytes.
func TestManager_SnapshotAfterExit(t *testing.T) {
	m := NewManager()
	var mu sync.Mutex
	var output []byte
	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-exit",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, _ int64, _ []Signal) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	}, nil)
	require.NoError(t, err)

	require.NoError(t, m.SendInput("tm-exit", []byte("echo before_exit"+testutil.TestShellEnter())))
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "before_exit")
	}, "expected output before exit")

	// Freeze the PTY, THEN inject the mode bytes. After the freeze the
	// test goroutine is the only writer, so the handler receives the
	// injection last and `output` holds the byte stream in buffer order.
	// Injecting while the shell still runs would leave that order to a
	// race: the ring write and the handler call are not one atomic step.
	testutil.FreezeTerminalOutput(t, m, "tm-exit")
	require.True(t, m.IsExited("tm-exit"), "expected terminal to exit")
	require.True(t, m.AppendOutput("tm-exit", []byte(conPTYStyleModeBytes)))

	mu.Lock()
	seen := append([]byte(nil), output...)
	mu.Unlock()

	// Snapshot must still work between exit and RemoveTerminal; this is
	// the window where a reconnecting client asks for the final screen.
	snap, offset, _ := m.ScreenSnapshotSince("tm-exit", 0)
	assert.NotEmpty(t, snap, "post-exit snapshot must be retained until RemoveTerminal")
	assert.Contains(t, string(snap), "before_exit")
	require.Less(t, offset, int64(screenBufferSize), "test output must stay inside the ring")
	// Nothing writes any more, so the counts are exact: the offset counts
	// every byte of the stream — the shell's output and the injection —
	// and the snapshot puts only the synthesized restore prefix in front
	// of them.
	assert.Equal(t, int64(len(seen)), offset,
		"offset counts stream bytes only, never the synthesized mode prefix")
	assert.True(t, bytes.HasSuffix(snap, seen),
		"snapshot must end with every retained stream byte")
	assert.Greater(t, int64(len(snap)), offset,
		"the injected mode bytes must produce a restore prefix on top of the counted bytes")

	// SnapshotSince(offset) must also return empty (caught up) even after exit.
	data, endOffset, isSnap := m.ScreenSnapshotSince("tm-exit", offset)
	assert.Empty(t, data)
	assert.Equal(t, offset, endOffset)
	assert.False(t, isSnap)

	m.RemoveTerminal("tm-exit")

	// After removal, the entry is gone and snapshots return zero-value.
	gone, goneOffset, _ := m.ScreenSnapshotSince("tm-exit", 0)
	assert.Nil(t, gone)
	assert.Zero(t, goneOffset)
}

// TestManager_AppendOutput_AdvancesOffset: synthetic output injected via
// AppendOutput (used for the "terminal disconnected" notice when the
// shell exits) must advance the cumulative byte counter so subscribers
// deltaing past the exit see the notice in their incremental catch-up.
func TestManager_AppendOutput_AdvancesOffset(t *testing.T) {
	m := NewManager()
	err := m.StartTerminal(context.Background(), Options{
		ID:         "tm-append",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, _ int64, _ []Signal) {}, nil)
	require.NoError(t, err)
	testutil.RegisterTerminalCleanup(t, m, "tm-append")

	// Wait for the shell's initial prompt to *finish* writing before
	// capturing baseline. `/bin/sh -i -l` (and pwsh, zsh, etc.) emit
	// the prompt as several PTY chunks — hostname, working dir, $PS1,
	// trailing space — and on a loaded test host the gap between the
	// first chunk landing and the last one can exceed the
	// AssertEventually poll interval. The previous baseline-capture
	// triggered on the first non-zero offset, so any chunk arriving
	// after baseline but before AppendOutput inflated the delta by
	// the prompt remainder and tripped the assertion below.
	//
	// Probing two samples 50ms apart and only accepting when they
	// agree pins baseline to a quiescent terminal; an interactive
	// shell only writes again in response to stdin we never send.
	var baseline int64
	testutil.AssertEventually(t, func() bool {
		_, off1, _ := m.ScreenSnapshotSince("tm-append", 0)
		if off1 == 0 {
			return false
		}
		time.Sleep(50 * time.Millisecond)
		_, off2, _ := m.ScreenSnapshotSince("tm-append", 0)
		if off1 != off2 {
			return false
		}
		baseline = off2
		return true
	}, "shell prompt settled")

	notice := []byte("\r\n[terminal disconnected]\r\n")
	require.True(t, m.AppendOutput("tm-append", notice))

	_, afterAppend, _ := m.ScreenSnapshotSince("tm-append", 0)
	assert.Equal(t, baseline+int64(len(notice)), afterAppend,
		"AppendOutput must advance the cumulative byte counter by len(data)")

	// A subscriber at `baseline` must receive exactly the appended bytes
	// as an incremental delta — the catch-up contract for the disconnect
	// notice.
	data, endOffset, isSnap := m.ScreenSnapshotSince("tm-append", baseline)
	assert.Equal(t, notice, data)
	assert.Equal(t, afterAppend, endOffset)
	assert.False(t, isSnap)
}

// TestManager_ScreenSnapshotSince_ModePrefixOnFallenBehind: the manager
// wrapper must propagate the mode-restore prefix produced inside the
// ScreenBuffer when a subscriber has fallen out of the retained ring.
// Tests the seam between Manager and Terminal — easy to break by
// swapping the ScreenSnapshotSince implementation.
func TestManager_ScreenSnapshotSince_ModePrefixOnFallenBehind(t *testing.T) {
	m := newTestManagerWithTerminal(t, Options{
		ID:         "tm-prefix",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	})
	pushAltScreenPastRing(t, m, "tm-prefix")

	data, _, isSnap := m.ScreenSnapshotSince("tm-prefix", 0)
	require.True(t, isSnap, "fallen-behind subscriber must get a snapshot")
	require.GreaterOrEqual(t, len(data), len("\x1b[?1049h"))
	assert.Equal(t, []byte("\x1b[?1049h"), data[:len("\x1b[?1049h")],
		"manager.ScreenSnapshotSince must propagate the alt-screen restore prefix")
}

// TestManager_ScreenSnapshot_PrefixesPersistedScreen: ScreenSnapshot is
// the entry point for the DB-persistence and ListTerminals paths. The
// prefix MUST appear here too, otherwise alt-screen state is lost
// across worker restarts even when the bug-fix landed for resubscribe.
func TestManager_ScreenSnapshot_PrefixesPersistedScreen(t *testing.T) {
	m := newTestManagerWithTerminal(t, Options{
		ID:         "tm-snapshot",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	})
	pushAltScreenPastRing(t, m, "tm-snapshot")

	snap, ok := m.SnapshotTerminal("tm-snapshot")
	require.True(t, ok)
	require.GreaterOrEqual(t, len(snap.Screen), len("\x1b[?1049h"))
	assert.Equal(t, []byte("\x1b[?1049h"), snap.Screen[:len("\x1b[?1049h")],
		"SnapshotTerminal must include the mode-restore prefix so DB-persisted screens survive worker restarts")
}

// newTestManagerWithTerminal starts a Manager + one terminal and wires
// up the standard cleanup so callers don't repeat the StartTerminal +
// t.Cleanup boilerplate. The Options are passed through verbatim (so
// callers can vary Cols / Rows).
func newTestManagerWithTerminal(t *testing.T, opts Options) *Manager {
	t.Helper()
	id := opts.ID
	m := NewManager()
	err := m.StartTerminal(context.Background(), opts, func(data []byte, _ int64, _ []Signal) {}, nil)
	require.NoError(t, err)
	testutil.RegisterTerminalCleanup(t, m, id)
	return m
}

// pushAltScreenPastRing injects the alt-screen toggle and then enough
// filler bytes to overwrite the retained ring, so a subsequent snapshot
// must reach for the modeTracker prefix to recover alt-screen state.
// AppendOutput goes through the same Write path as live PTY output, so
// the tracker observes the toggle exactly as it would in production.
func pushAltScreenPastRing(t *testing.T, m *Manager, id string) {
	t.Helper()
	require.True(t, m.AppendOutput(id, []byte("\x1b[?1049h")))
	require.True(t, m.AppendOutput(id, ringOverflowFiller()))
}

// TestTerminal_AppendOutputCannotOvertakeLiveOutput reproduces the pair
// of writers the shutdown sweep creates: broadcastTerminalsDisconnected
// appends the disconnect notice to terminals whose PTY it does NOT stop,
// so that AppendOutput runs while the reader goroutine still delivers
// shell output. Both reach the handler through wrappedOutput, and a
// watcher drops any chunk whose range sits below the offset it already
// holds — so an out-of-order delivery loses those bytes from the screen
// until the next full resync.
func TestTerminal_AppendOutputCannotOvertakeLiveOutput(t *testing.T) {
	notice := []byte("\r\n\r\n[Worker disconnected]\r\n")
	var (
		mu          sync.Mutex
		last        int64
		wrong       []int64
		shellChunks int
	)
	term, err := Start(context.Background(), Options{
		ID:         "t-order",
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, endOffset int64, _ []Signal) {
		mu.Lock()
		defer mu.Unlock()
		if !bytes.Equal(data, notice) {
			shellChunks++
		}
		if endOffset < last {
			wrong = append(wrong, endOffset)
			return
		}
		last = endOffset
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
		<-term.readDoneCh
	})

	// Flood the PTY the way a build or a `tail -f` would, then inject the
	// notices while the reader delivers it. Both the start and the end of
	// the injection are tied to the reader's own progress, so the overlap
	// that this test is about does not depend on the scheduler: the
	// injectors keep going until several more PTY chunks have landed
	// among them. 200000 lines outlast that by far.
	require.NoError(t, term.SendInput([]byte("seq 1 200000"+testutil.TestShellEnter())))
	chunksSeen := func() int {
		mu.Lock()
		defer mu.Unlock()
		return shellChunks
	}
	testutil.AssertEventually(t, func() bool { return chunksSeen() > 0 },
		"expected the shell to start producing output")

	const injectors, maxPerInjector, overlapChunks = 8, 500, 5
	target := chunksSeen() + overlapChunks
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < injectors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < maxPerInjector; n++ {
				select {
				case <-stop:
					return
				default:
				}
				term.AppendOutput(notice)
			}
		}()
	}
	testutil.AssertEventually(t, func() bool { return chunksSeen() >= target },
		"expected PTY chunks to land while the notices were going in")
	close(stop)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, wrong,
		"a watcher would receive these offsets after a higher one and drop the bytes")
}

// conPTYStyleModeBytes is what cmd.exe under ConPTY writes soon after it
// starts: a window-title OSC and a cursor hide. A test that injects them
// leaves the modeTracker non-default, so its snapshots carry a restore
// prefix on every platform, exactly as they do on a Windows runner. The
// POSIX test shell (/bin/sh) sets no sticky mode at all, so without the
// injection a Linux or macOS run only ever sees the empty-prefix case
// and cannot catch a regression in the prefix-versus-offset arithmetic.
const conPTYStyleModeBytes = "\x1b]0;leapmux test\x07\x1b[?25l"

func TestManager_IsExited_UnknownTerminal(t *testing.T) {
	m := NewManager()
	assert.False(t, m.IsExited("nonexistent"), "expected IsExited = false for unknown terminal")
}

func TestManager_HasTerminal_UnknownTerminal(t *testing.T) {
	m := NewManager()
	assert.False(t, m.HasTerminal("nonexistent"), "expected HasTerminal = false for unknown terminal")
}

// newTestDB creates an in-memory SQLite database with migrations applied.
func newTestDB(t *testing.T) *db.Queries {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err, "open in-memory DB")
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB), "migrate DB")
	return db.New(sqlDB)
}

func TestSnapshotTerminal(t *testing.T) {
	m := NewManager()
	var mu sync.Mutex
	var output []byte

	termID := "tm-snap-single"

	err := m.StartTerminal(context.Background(), Options{
		ID:         termID,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte, _ int64, _ []Signal) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	}, nil)
	require.NoError(t, err, "StartTerminal")

	require.NoError(t, m.SendInput(termID, []byte("echo snapshot_single"+testutil.TestShellEnter())), "SendInput")
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "snapshot_single")
	}, "expected output to contain 'snapshot_single'")

	snap, ok := m.SnapshotTerminal(termID)
	require.True(t, ok, "SnapshotTerminal should return ok=true")
	assert.Equal(t, uint32(80), snap.Cols)
	assert.Equal(t, uint32(24), snap.Rows)
	assert.Contains(t, string(snap.Screen), "snapshot_single")

	// Unknown terminal returns ok=false.
	_, ok = m.SnapshotTerminal("nonexistent")
	assert.False(t, ok, "SnapshotTerminal should return ok=false for unknown terminal")

	m.StopAll()
}

func TestUpsertAndGetTerminal(t *testing.T) {
	ctx := context.Background()
	queries := newTestDB(t)

	termID := "tm-db"

	// Upsert a terminal (workspace_id is plain TEXT, no FK needed).
	screenData := []byte("hello terminal")
	require.NoError(t, queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         termID,
		WorkingDir: "/tmp",
		HomeDir:    "/home/test",
		Cols:       120,
		Rows:       40,
		Screen:     screenData,
	}), "UpsertTerminal")

	// Get it back.
	row, err := queries.GetTerminal(ctx, termID)
	require.NoError(t, err, "GetTerminal")
	assert.Equal(t, termID, row.ID)
	assert.Equal(t, "/tmp", row.WorkingDir)
	assert.Equal(t, "/home/test", row.HomeDir)
	assert.Equal(t, int64(120), row.Cols)
	assert.Equal(t, int64(40), row.Rows)
	assert.Equal(t, screenData, row.Screen)

	// Soft-delete and verify closed_at is set.
	require.NoError(t, closeErr(queries.CloseTerminal(ctx, termID)), "CloseTerminal")
	row, err = queries.GetTerminal(ctx, termID)
	require.NoError(t, err, "GetTerminal after close")
	assert.True(t, row.ClosedAt.Valid, "closed_at should be set")
}

func TestUpdateTitle(t *testing.T) {
	m := NewManager()

	termID := "tm-title"
	err := m.StartTerminal(context.Background(), Options{
		ID:         termID,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func([]byte, int64, []Signal) {}, nil)
	require.NoError(t, err)

	// Initially empty title via ListByIDs.
	entries := m.ListByIDs([]string{termID})
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].Meta.Title)

	// Update title.
	assert.True(t, m.UpdateTitle(termID, "my terminal"))
	entries = m.ListByIDs([]string{termID})
	require.Len(t, entries, 1)
	assert.Equal(t, "my terminal", entries[0].Meta.Title)

	// Unknown terminal returns false.
	assert.False(t, m.UpdateTitle("nonexistent", "nope"))

	// THE CLEAN LIVES HERE, so no writer can put a control character or a
	// bidirectional override in a tab label. The rename RPC cleaned its own
	// title and the post-spawn absorb did not, and the manager's title is what
	// ListTerminals reports while a terminal is live.
	assert.True(t, m.UpdateTitle(termID, "safe\u202ereversed\x00"))
	entries = m.ListByIDs([]string{termID})
	require.Len(t, entries, 1)
	assert.Equal(t, "safereversed", entries[0].Meta.Title)

	// A title that cleans to nothing leaves the stored one alone, the same
	// answer the rename RPC gives: writing "" leaves the tab with no name.
	assert.False(t, m.UpdateTitle(termID, "  \u200b\ufeff  "))
	entries = m.ListByIDs([]string{termID})
	require.Len(t, entries, 1)
	assert.Equal(t, "safereversed", entries[0].Meta.Title, "an empty clean must not blank the tab")

	m.StopAll()
}

func TestUpsertTerminalTitle(t *testing.T) {
	ctx := context.Background()
	queries := newTestDB(t)

	termID := "tm-title-db"
	require.NoError(t, queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         termID,
		WorkingDir: "/tmp",
		Title:      "Terminal 1",
		Cols:       80,
		Rows:       24,
		Screen:     []byte{},
	}))

	row, err := queries.GetTerminal(ctx, termID)
	require.NoError(t, err)
	assert.Equal(t, "Terminal 1", row.Title)

	// Update title via upsert.
	require.NoError(t, queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         termID,
		WorkingDir: "/tmp",
		Title:      "My Shell",
		Cols:       80,
		Rows:       24,
		Screen:     []byte{},
	}))

	row, err = queries.GetTerminal(ctx, termID)
	require.NoError(t, err)
	assert.Equal(t, "My Shell", row.Title)
}

// resetShellCache swaps in a fresh sync.OnceValues so ListAvailableShells
// recomputes after env-var mutations.
func resetShellCache() {
	resolveShells = newShellsResolver()
}

func TestListAvailableShells_ReturnsAtLeastOne(t *testing.T) {
	resetShellCache()
	shells, _ := ListAvailableShells()
	assert.NotEmpty(t, shells, "expected at least one shell to be found")
}

func TestListAvailableShells_DefaultShellSet(t *testing.T) {
	resetShellCache()
	_, defaultShell := ListAvailableShells()
	assert.NotEmpty(t, defaultShell, "expected default shell to be non-empty")
}

func TestListAvailableShells_DefaultShellFirst(t *testing.T) {
	resetShellCache()
	shells, defaultShell := ListAvailableShells()
	require.NotEmpty(t, shells, "expected at least one shell")
	assert.Equal(t, defaultShell, shells[0], "default shell should be the first entry")
}

func TestListAvailableShells_DefaultShellFirst_NonStandardPath(t *testing.T) {
	// Simulate $SHELL pointing to a path not found by LookPath (e.g.
	// /bin/zsh vs /opt/homebrew/bin/zsh).
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", "/usr/local/fake/zsh")
	resetShellCache()
	shells, defaultShell := ListAvailableShells()
	assert.Equal(t, "/usr/local/fake/zsh", defaultShell)
	require.NotEmpty(t, shells)
	assert.Equal(t, "/usr/local/fake/zsh", shells[0], "non-standard default shell should be first")
}

func TestListAvailableShells_NoDuplicateDefaultShell(t *testing.T) {
	resetShellCache()
	shells, defaultShell := ListAvailableShells()
	count := 0
	for _, s := range shells {
		if s == defaultShell {
			count++
		}
	}
	assert.Equal(t, 1, count, "default shell should appear exactly once")
}

func TestListAvailableShells_Cached(t *testing.T) {
	resetShellCache()
	shells1, default1 := ListAvailableShells()
	shells2, default2 := ListAvailableShells()
	assert.Equal(t, shells1, shells2, "cached shells should be identical")
	assert.Equal(t, default1, default2, "cached default shell should be identical")
}

func TestDetectDefaultShell(t *testing.T) {
	shell := detectDefaultShell()
	assert.NotEmpty(t, shell, "detectDefaultShell should return a non-empty string")
	assert.True(t, filepath.IsAbs(shell), "detectDefaultShell should return an absolute path, got %q", shell)
}

func TestResolveDefaultShell_LeapMuxEnvInvalidBareName(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "nonexistent-shell-xyz")
	t.Setenv("SHELL", "/bin/fallback-shell")
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.Equal(t, "/bin/fallback-shell", shell, "should fall back to $SHELL when LEAPMUX_DEFAULT_SHELL is unresolvable")
}

func TestResolveDefaultShell_FallsBackWhenEnvUnset(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", "")
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.NotEmpty(t, shell, "ResolveDefaultShell should return a shell even without $SHELL")
	assert.True(t, filepath.IsAbs(shell), "ResolveDefaultShell should return an absolute path, got %q", shell)
}

func TestResolveShellEnv_Empty(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "")
	assert.Equal(t, "", resolveShellEnv("TEST_SHELL_ENV"))
}

func TestResolveShellEnv_BareNameNotFound(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "nonexistent-shell-xyz")
	assert.Equal(t, "", resolveShellEnv("TEST_SHELL_ENV"))
}

// TestRestart_PreservesScreenBufferOffset exercises the live-exit path:
// the previous *Terminal struct is still in the manager when restart
// runs, so the new spawn reuses its *ScreenBuffer (same pointer) and the
// cumulative byte counter continues across the PTY boundary.
func TestRestart_PreservesScreenBufferOffset(t *testing.T) {
	m := NewManager()
	const id = "tm-restart-preserve"
	opts := Options{
		ID:         id,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}

	require.NoError(t, m.StartTerminal(context.Background(), opts, func([]byte, int64, []Signal) {}, nil), "StartTerminal")

	require.NoError(t, m.SendInput(id, []byte("echo first"+testutil.TestShellEnter())))
	testutil.AssertEventually(t, func() bool {
		s, _ := m.SnapshotTerminal(id)
		return strings.Contains(string(s.Screen), "first")
	}, "expected first command output")

	// Tear down the shell to mirror the post-exit state, then snapshot the
	// pre-restart cumulative offset (the value the frontend has cached as
	// its resume cursor).
	m.StopTerminal(id)
	testutil.AssertEventually(t, func() bool { return m.IsExited(id) }, "exit")
	preTerm := m.terminals[id]
	_, preTotal, _ := m.ScreenSnapshotSince(id, 0)
	require.Greater(t, preTotal, int64(0), "should have written some bytes pre-restart")

	require.NoError(t, m.RestartTerminal(context.Background(), opts, 0, func([]byte, int64, []Signal) {}, nil), "RestartTerminal")
	testutil.RegisterTerminalCleanup(t, m, id)

	newTerm := m.terminals[id]
	require.NotSame(t, preTerm, newTerm, "manager should swap in a fresh *Terminal on restart")
	// Observable contract: cumulative offset doesn't regress across the
	// restart, and the retained bytes from the prior session remain
	// visible. Together these prove the ScreenBuffer was carried over —
	// without relying on a private accessor.
	postScreen, postTotal, _ := m.ScreenSnapshotSince(id, 0)
	assert.GreaterOrEqual(t, postTotal, preTotal,
		"cumulative byte counter must not regress across restart")
	assert.Contains(t, string(postScreen), "first",
		"retained ring contents from the prior session must survive the restart")
}

// TestRestart_NewBufferWithFallbackOffset exercises the worker-restart
// path: the prior *Terminal is gone (e.g. removed during Shutdown), so
// RestartTerminal must create a new ScreenBuffer seeded with the
// fallback offset so the frontend's cached lastOffset stays consistent.
func TestRestart_NewBufferWithFallbackOffset(t *testing.T) {
	m := NewManager()
	const id = "tm-restart-fallback"
	const fallback = int64(4096)
	opts := Options{
		ID:         id,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}

	require.NoError(t, m.RestartTerminal(context.Background(), opts, fallback, func([]byte, int64, []Signal) {}, nil), "RestartTerminal")
	testutil.RegisterTerminalCleanup(t, m, id)

	newTerm := m.terminals[id]
	require.NotNil(t, newTerm, "manager should have a *Terminal after restart")
	_, total, _ := m.ScreenSnapshotSince(id, 0)
	assert.GreaterOrEqual(t, total, fallback,
		"new screen buffer must start at >= fallback offset")
}

// TestRestart_FailsIfStillRunning rejects restarts on a live terminal:
// the previous shell hasn't exited, so accepting the restart would
// orphan it (or worse, hand two PTYs the same id).
func TestRestart_FailsIfStillRunning(t *testing.T) {
	m := NewManager()
	const id = "tm-restart-running"
	opts := Options{
		ID:         id,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}
	require.NoError(t, m.StartTerminal(context.Background(), opts, func([]byte, int64, []Signal) {}, nil), "StartTerminal")
	testutil.RegisterTerminalCleanup(t, m, id)

	original := m.terminals[id]
	err := m.RestartTerminal(context.Background(), opts, 0, func([]byte, int64, []Signal) {}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still running")
	assert.Same(t, original, m.terminals[id], "failed restart must not swap out the live *Terminal")
}

// closeErr discards the affected-row count from an :execresult tab close and
// returns just the error, so tests that only need the side effect can keep using
// require.NoError. CloseAgent / CloseTerminal report that count because
// closeTabCommon uses it to tell a live close from a re-close of an
// already-closed row.
func closeErr(_ sql.Result, err error) error { return err }

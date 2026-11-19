package terminal

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/terminput"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// newInitialCommandTerminal builds a terminal on clock whose PTY records its
// writes. No process runs: a test plays the shell, by writing the shell's
// output to the screen buffer and by closing exitCh for its exit.
//
// The shell ends when the test ends, so a wait that the test left pending
// returns before the synctest bubble checks that its goroutines ended.
func newInitialCommandTerminal(t *testing.T, clock quartz.Clock) (*Terminal, *recordingPTY) {
	t.Helper()
	rec := &recordingPTY{}
	term := &Terminal{id: "term-1", ptmx: rec, clock: clock, screenBuf: NewScreenBuffer(), exitCh: make(chan struct{})}
	t.Cleanup(func() {
		if !term.IsExited() {
			close(term.exitCh)
		}
	})
	return term, rec
}

// bracketedCommand is the one write that the command "make test" becomes in
// a shell that turned bracketed paste on.
const bracketedCommand = "\x1b[200~make test\x1b[201~\r"

// assertTimerReleased asserts that the wait for the shell stopped its timer.
func assertTimerReleased(t *testing.T, clock *quartz.Mock) {
	t.Helper()
	_, armed := clock.Peek()
	assert.False(t, armed, "the wait for the shell stops its timer when it ends")
}

func TestStartInitialCommand_WritesNothingBeforeTheShellIsReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		term, rec := newInitialCommandTerminal(t, clock)
		term.startInitialCommand("make test")

		// What a shell prints while its startup files run: a banner, other
		// modes, a reset of bracketed paste, and the ANSI mode 2004, which is
		// not the DEC private mode.
		term.screenBuf.Write([]byte("Last login: today\r\n\x1b[?1h\x1b[?2004l\x1b[2004h$ "))
		clock.Advance(initialCommandFallback - time.Millisecond).MustWait(t.Context())
		synctest.Wait()
		assert.Empty(t, rec.recorded(), "a shell that is not ready gets nothing")
	})
}

func TestStartInitialCommand_WritesOnceWhenTheShellTurnsBracketedPasteOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		term, rec := newInitialCommandTerminal(t, clock)
		term.startInitialCommand("make test")

		term.screenBuf.Write([]byte("$ \x1b[?2004h"))
		synctest.Wait()
		assert.Equal(t, []string{bracketedCommand}, rec.recorded(),
			"one write holds the command as a paste and the Enter key")
		assertTimerReleased(t, clock)

		// The line editor turns bracketed paste on again for each later line.
		term.screenBuf.Write([]byte("\x1b[?2004l\r\nok\r\n$ \x1b[?2004h"))
		synctest.Wait()
		assert.Equal(t, []string{bracketedCommand}, rec.recorded(), "the command runs once")
	})
}

// The DECSET that marks the ready shell can arrive in two reads, or beside
// another mode in one sequence.
func TestStartInitialCommand_ReadsTheModeAsTheTrackerReadsIt(t *testing.T) {
	for name, chunks := range map[string][]string{
		"a sequence split across two reads": {"$ \x1b[?20", "04h"},
		"two modes in one sequence":         {"\x1b[?1;2004h"},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := testutil.NewQuartzMock(t)
				term, rec := newInitialCommandTerminal(t, clock)
				term.startInitialCommand("make test")

				for i, chunk := range chunks {
					synctest.Wait()
					require.Emptyf(t, rec.recorded(), "nothing before chunk %d", i)
					term.screenBuf.Write([]byte(chunk))
				}
				synctest.Wait()
				require.Len(t, rec.recorded(), 1)
				assert.Contains(t, rec.recorded()[0], "\x1b[200~make test\x1b[201~", "the command arrives as a bracketed paste")
			})
		})
	}
}

// A shell whose line editor never turns bracketed paste on gets the command
// after the fallback wait, as plain text and Enter.
func TestStartInitialCommand_WritesAfterTheFallbackWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		term, rec := newInitialCommandTerminal(t, clock)
		term.startInitialCommand("make test")

		wait, armed := clock.Peek()
		require.True(t, armed, "the fallback timer exists when startInitialCommand returns")
		assert.Equal(t, initialCommandFallback, wait)
		clock.Advance(wait).MustWait(t.Context())
		synctest.Wait()
		assert.Equal(t, []string{"make test\r"}, rec.recorded())
		assertTimerReleased(t, clock)

		// A line editor that turns bracketed paste on later writes nothing more.
		term.screenBuf.Write([]byte("\x1b[?2004h"))
		synctest.Wait()
		assert.Equal(t, []string{"make test\r"}, rec.recorded(), "the command runs once")
	})
}

// A restart hands the screen buffer of the previous shell to the next one. A
// bracketed paste that the previous shell left on does not make the new shell
// ready: only a DECSET 2004 after the start does.
func TestStartInitialCommand_IgnoresAModeThatWasOnBeforeTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		term, rec := newInitialCommandTerminal(t, clock)
		term.screenBuf.Write([]byte("\x1b[?2004h"))
		require.True(t, term.screenBuf.InputModes().BracketedPaste, "precondition: the mode is on")

		term.startInitialCommand("make test")
		synctest.Wait()
		assert.Empty(t, rec.recorded(), "the mode of the previous shell does not count")

		term.screenBuf.Write([]byte("$ \x1b[?2004h"))
		synctest.Wait()
		assert.Equal(t, []string{bracketedCommand}, rec.recorded())
	})
}

func TestStartInitialCommand_WritesNothingToAShellThatEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		term, rec := newInitialCommandTerminal(t, clock)
		term.startInitialCommand("make test")

		close(term.exitCh)
		synctest.Wait()
		assertTimerReleased(t, clock)
		term.screenBuf.Write([]byte("\x1b[?2004h"))
		synctest.Wait()
		assert.Empty(t, rec.recorded(), "a shell that ended before it was ready gets nothing")
	})
}

// A close of the tab stops the terminal, and the shell can turn bracketed
// paste on before the stop ends it.
func TestStartInitialCommand_WritesNothingToAStoppedTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		term, rec := newInitialCommandTerminal(t, clock)
		term.startInitialCommand("make test")

		term.mu.Lock()
		term.stopped = true
		term.mu.Unlock()
		term.screenBuf.Write([]byte("\x1b[?2004h"))
		synctest.Wait()
		assert.Empty(t, rec.recorded())
		assertTimerReleased(t, clock)
	})
}

// The initial command reaches the PTY as the bytes that `terminal run` sends
// through SendInput: the command as a paste, then "enter", for the modes that
// the program enabled.
func TestCommandLineInput_EncodesAsTerminalRunSendsIt(t *testing.T) {
	enter, err := terminput.ParseKeys([]string{"enter"})
	require.NoError(t, err)
	for _, command := range []string{"make test", "printf 'a\\n'\nmake", "echo \x1b[201~ end"} {
		for _, modes := range []terminput.Modes{{}, {BracketedPaste: true}, {ApplicationCursorKeys: true, BracketedPaste: true}} {
			run := terminput.Input{Data: []byte(command), Paste: true, Keys: enter}
			assert.Equalf(t, run.Encode(modes), commandLineInput(command).Encode(modes), "command %q in modes %+v", command, modes)
		}
	}
	assert.Equal(t, "\x1b[200~a\rb\x1b[201~\r", string(commandLineInput("a\nb").Encode(terminput.Modes{BracketedPaste: true})))
}

func TestScreenBufferNextBracketedPasteSet(t *testing.T) {
	sb := NewScreenBuffer()
	first := sb.nextBracketedPasteSet()
	assert.Equal(t, first, sb.nextBracketedPasteSet(), "every caller before the sequence shares one channel")

	sb.Write([]byte("\x1b[?2004l\x1b[?1h text"))
	select {
	case <-first:
		require.FailNow(t, "a reset and another mode do not close the channel")
	default:
	}

	sb.Write([]byte("\x1b[?2004h"))
	select {
	case <-first:
	default:
		require.FailNow(t, "DECSET 2004 closes the channel")
	}

	next := sb.nextBracketedPasteSet()
	assert.NotEqual(t, first, next, "a caller after the sequence waits for the next one")
	select {
	case <-next:
		require.FailNow(t, "the mode that is on already does not close the next channel")
	default:
	}
	sb.Write([]byte("\x1b[?2004h"))
	select {
	case <-next:
	default:
		require.FailNow(t, "the next DECSET 2004 closes the next channel")
	}

	// A DECSET 2004 while nothing waits leaves no channel behind.
	sb.Write([]byte("\x1b[?2004h"))
	sb.mu.Lock()
	assert.Nil(t, sb.bracketedPasteSet, "no channel exists while nothing waits")
	sb.mu.Unlock()
}

func TestManagerShellStatus(t *testing.T) {
	m := NewManager()
	assert.Equal(t, ShellStatus{}, m.ShellStatus("no-such-terminal"))

	term := &Terminal{id: "term-1", screenBuf: NewScreenBuffer(), exitCh: make(chan struct{})}
	m.current["term-1"] = &terminalRegistration{id: "term-1", terminal: term, done: make(chan struct{})}
	assert.Equal(t, ShellStatus{Held: true, Running: true}, m.ShellStatus("term-1"))

	term.exitCode = 3
	close(term.exitCh)
	assert.Equal(t, ShellStatus{Held: true, ExitCode: 3}, m.ShellStatus("term-1"))

	signaled := &Terminal{id: "term-2", screenBuf: NewScreenBuffer(), exitCh: make(chan struct{}), exitCode: -1}
	close(signaled.exitCh)
	m.current["term-2"] = &terminalRegistration{id: "term-2", terminal: signaled, done: make(chan struct{})}
	assert.Equal(t, ShellStatus{Held: true, ExitCode: -1}, m.ShellStatus("term-2"), "a shell that a signal ended reports -1")
}

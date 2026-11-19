package terminal

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/terminput"
)

// initialCommandFallback is how long a new shell has to turn bracketed paste
// on before the terminal sends its initial command anyway.
//
// It serves the shells whose line editor never turns bracketed paste on: sh,
// dash, a bash older than 5.1, cmd.exe and PowerShell. The PTY holds the
// command until such a shell reads its input, so a shell that is still
// starting after this wait reads the command once it starts to read. A longer
// wait only delays the command in those shells. A shell that turns bracketed
// paste on gets the command at once, whatever this value is.
const initialCommandFallback = 2 * time.Second

// initialCommandTimerTag identifies the timer of initialCommandFallback to a
// test clock.
const initialCommandTimerTag = "terminal-initial-command"

// enterKey is the key that ends a command line, as `leapmux control terminal
// run` presses it.
var enterKey = mustParseKey("enter")

// mustParseKey parses a key name that this package states as a constant, so a
// failure is a programming error.
func mustParseKey(name string) terminput.Key {
	k, err := terminput.ParseKey(name)
	if err != nil {
		panic(fmt.Sprintf("terminal: key %q: %v", name, err))
	}
	return k
}

// commandLineInput is the input of one command line: the command as a paste,
// then the Enter key. `leapmux control terminal run` sends the same input
// through a SendInputRequest with paste set and the key "enter".
func commandLineInput(command string) terminput.Input {
	return terminput.Input{Data: []byte(command), Paste: true, Keys: []terminput.Key{enterKey}}
}

// startInitialCommand sends command to the shell once the shell is ready, in
// one write, from a goroutine of its own:
//
//   - The shell is ready when the output next turns bracketed paste on. zsh,
//     fish and bash 5.1 or later do that each time their line editor starts
//     to read a command line, so the command reaches the line editor and not
//     a program that the shell's startup files run.
//   - A shell that does not turn it on within initialCommandFallback gets the
//     command then.
//   - A shell that ends first gets nothing, and so does a terminal that Stop
//     ended.
//
// The command arrives as a paste, so a line editor that turned bracketed
// paste on inserts it as it is: an abbreviation or an automatic closing
// bracket cannot change it.
//
// The wait for the mode and the timer exist when this returns. The caller
// starts the reader after it, so no DECSET 2004 can pass before the wait
// exists, and a test with a mock clock finds the timer armed.
func (t *Terminal) startInitialCommand(command string) {
	ready := t.screenBuf.nextBracketedPasteSet()
	fallback := t.clock.NewTimer(initialCommandFallback, initialCommandTimerTag)
	go func() {
		defer fallback.Stop(initialCommandTimerTag)
		select {
		case <-ready:
		case <-fallback.C:
		case <-t.exitCh:
			return
		}
		// A Stop that a close of the tab started does not close exitCh until
		// the shell is gone. Writing to a stopped terminal would only report
		// that it stopped.
		if t.isStopped() || t.IsExited() {
			return
		}
		if err := t.sendInput(commandLineInput(command)); err != nil {
			slog.Warn("failed to send the initial command of a terminal", "terminal_id", t.id, "error", err)
		}
	}()
}

package cmd

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// terminalWaitLong is the help of `terminal wait`.
const terminalWaitLong = `Wait until a terminal meets one condition, then print what met it:

  - --exit waits until the shell of the terminal ends, and prints its exit
    code.
  - --text waits until a line of the terminal holds the text.
  - --regex waits until a line of the terminal matches the regular
    expression, in RE2 syntax.

The CLI replays the output of the terminal through the emulator of "terminal
read", so a line is the text that the terminal shows: an escape sequence, or
a text that arrives in two parts, cannot break a match. The match is per
line. A line that the terminal wraps at its width counts as one line, and it
loses its trailing blanks, as "terminal read" prints it, so ^ and $ match at
its ends. The command prints the line that matched. When several lines match
at once, it prints the lowest one.

The text that the screen shows when the wait begins counts, so a wait for a
text that the terminal already shows ends at once. Each line that appears
later counts too, even one that scrolls off the screen before the CLI reads
it. The wait follows size changes and applies them in order after reconnect.
Use --scrollback to include the retained history from before the wait.
To wait for the output of a new command only, use the default screen scope
and clear the screen first, or wait for a line that the command text does not
show. In the second example, the echoed command line holds "echo DONE".
The line of output matches '^DONE$'.

The command exits 0 when the condition holds. It exits 6 when the wait ended
without it: --timeout passed, the shell ended before the terminal showed the
text, or the shell failed to start. --json prints the status of the terminal
in each case, and the exit code of the shell once it ended.

--exit ends at once for a shell that ended before the wait began, and prints
its exit code, which the Worker keeps. When the Worker holds no record of the
code, because it was killed while the shell ran, the command prints no exit
code, says so on stderr, and still exits 0. --exit refuses the terminal that
runs the command, because its shell cannot end while the wait runs in it.

"leapmux control events --kind terminal" prints the bells, the titles, the
notifications, the progress reports and the exits of terminals as they
happen.`

// newTerminalWaitLeaf builds `terminal wait`.
func newTerminalWaitLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "wait <terminal>",
		Short: "Wait until a terminal shows a text, or until its shell ends",
		Long:  terminalWaitLong,
		Examples: []cli.Example{
			{Comment: "Wait until the shell ends, and keep its exit code.", Command: "CODE=$(leapmux control terminal wait 4cbb3f34 --exit)"},
			{Comment: "Run the tests, then wait for the line that the last command prints.", Command: "leapmux control terminal run 4cbb3f34 'make test; echo DONE' && leapmux control terminal wait 4cbb3f34 --regex '^DONE$' --timeout 10m"},
			{Comment: "Wait until a server reports that it listens, for 2 minutes at most.", Command: "leapmux control terminal wait 4cbb3f34 --text 'Listening on' --timeout 2m"},
			{Comment: "Look for a completed build in the terminal's retained output.", Command: "leapmux control terminal wait 4cbb3f34 --text 'Build complete' --scrollback"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("exit", false, "wait until the shell of the terminal ends, and print its exit code")
			fs.String("text", "", "wait until a line of the terminal holds this text")
			fs.String("regex", "", "wait until a line of the terminal matches this regular expression, in RE2 syntax")
			fs.Bool("scrollback", false, "also search the retained scrollback from before the wait")
			fs.Duration("timeout", 0, "stop waiting after this long, such as 90s or 10m; 0 waits without a limit")
		},
		Complete: completeTargets,
		Run:      runTerminalWait,
	})
}

// terminalWaitSpec states one wait for a terminal.
type terminalWaitSpec struct {
	// exit waits for the shell to end. A wait for text sets match instead.
	exit bool
	// match reports whether one line of the terminal meets --text or
	// --regex.
	match func(line string) bool
	// scrollback includes existing history in the text search.
	scrollback bool
	// condition states the text of --text or --regex for a notice, such as
	// `the text "PASS"`.
	condition string
	// timeout ends the wait after this long. Zero waits without a limit.
	timeout time.Duration
}

// terminalWaitSpecOf reads the flags of `terminal wait`, and refuses a
// command line that states no condition, or two, before anything is sent.
func terminalWaitSpecOf(inv *cli.Invocation) (terminalWaitSpec, error) {
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return terminalWaitSpec{}, err
	}
	spec := terminalWaitSpec{exit: boolFlag(inv, "exit"), timeout: timeout, scrollback: boolFlag(inv, "scrollback")}
	hasText, hasRegex := inv.Changed("text"), inv.Changed("regex")
	switch {
	case hasText && hasRegex:
		return terminalWaitSpec{}, cli.Usagef("--text and --regex exclude each other").
			WithHint("pass one of them")
	case spec.exit && (hasText || hasRegex):
		return terminalWaitSpec{}, cli.Usagef("--exit takes no --text and no --regex").
			WithHint("a wait for text already ends with exit status 6 when the shell ends first")
	case spec.scrollback && !hasText && !hasRegex:
		return terminalWaitSpec{}, cli.Usagef("--scrollback needs --text or --regex")
	case hasText:
		text := stringFlag(inv, "text")
		spec.match, err = textMatcher(text)
		spec.condition = fmt.Sprintf("the text %q", text)
	case hasRegex:
		pattern := stringFlag(inv, "regex")
		spec.match, err = regexMatcher(pattern)
		spec.condition = fmt.Sprintf("a line that matches %q", pattern)
	case !spec.exit:
		return terminalWaitSpec{}, cli.Usagef("missing the condition to wait for").
			WithHint("pass --exit, --text <text> or --regex <pattern>")
	}
	if err != nil {
		return terminalWaitSpec{}, err
	}
	return spec, nil
}

// textMatcher matches a line that holds text. A text with a line break
// cannot match, because a match is per line.
func textMatcher(text string) (func(string) bool, error) {
	if text == "" {
		return nil, cli.Usagef("--text is empty")
	}
	if strings.ContainsAny(text, "\r\n") {
		return nil, cli.Usagef("--text holds a line break, but a match is per line").
			WithHint("wait for one line of the text, or use --regex")
	}
	return func(line string) bool { return strings.Contains(line, text) }, nil
}

// regexMatcher matches a line that pattern matches. A pattern that matches
// an empty line matches every line, so the wait would end at once whatever
// the terminal shows.
func regexMatcher(pattern string) (func(string) bool, error) {
	if pattern == "" {
		return nil, cli.Usagef("--regex is empty")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, cli.Usagef("--regex %q does not compile: %v", pattern, err).
			WithHint("the syntax is RE2, which Go's regexp package reads")
	}
	if re.MatchString("") {
		return nil, cli.Usagef("--regex %q matches an empty line, so it matches every line at once", pattern).
			WithHint("make the pattern need at least one character, such as '^DONE$'")
	}
	return re.MatchString, nil
}

// terminalStatusUnknown is the status of a terminal that the wait did not
// observe.
const terminalStatusUnknown = "unknown"

// terminalWaitResult is the result of `terminal wait`.
type terminalWaitResult struct {
	ID string `json:"id"`
	// Status is the last status of the shell that the wait observed: a token
	// of TerminalStatus, such as "ready" or "exited", or "unknown".
	Status string `json:"status"`
	// Reached reports whether the condition held.
	Reached bool `json:"reached"`
	// ExitCode is the exit code of the shell, when the wait saw the shell
	// end with one. A shell that a signal ended reports -1.
	ExitCode *int32 `json:"exit_code,omitempty"`
	// Line is the line of the terminal that matched --text or --regex.
	Line string `json:"line,omitempty"`
	// StartupError states why the shell failed to start.
	StartupError string `json:"startup_error,omitempty"`
	// exit selects the text form: the exit code for --exit, and the line
	// otherwise.
	exit bool
}

// RenderText prints the exit code of a wait for exit, or the line that met
// a wait for text, and nothing when it has neither.
func (r terminalWaitResult) RenderText(w io.Writer) error {
	var err error
	switch {
	case r.exit && r.ExitCode != nil:
		_, err = fmt.Fprintln(w, *r.ExitCode)
	case !r.exit && r.Reached:
		_, err = fmt.Fprintln(w, r.Line)
	}
	return err
}

// terminalWaitOutcome is how a wait for a terminal ended.
type terminalWaitOutcome int

const (
	// terminalWaitHeld is a wait whose condition held.
	terminalWaitHeld terminalWaitOutcome = iota + 1
	// terminalWaitTimedOut is a wait that its timeout ended.
	terminalWaitTimedOut
	// terminalWaitShellEnded is a wait for text whose shell ended before the
	// terminal showed the text.
	terminalWaitShellEnded
	// terminalWaitStartupFailed is a wait whose shell failed to start, so it
	// never runs.
	terminalWaitStartupFailed
)

// terminalWaitTimerTag identifies the timeout of a wait to a test clock.
const terminalWaitTimerTag = "terminal-wait-timeout"

// terminalWaiter holds what one wait observed, and decides when it ends.
type terminalWaiter struct {
	spec   terminalWaitSpec
	result terminalWaitResult
}

func newTerminalWaiter(id string, spec terminalWaitSpec) *terminalWaiter {
	return &terminalWaiter{spec: spec, result: terminalWaitResult{ID: id, Status: terminalStatusUnknown, exit: spec.exit}}
}

// deadline starts the timeout of the wait. The channel is nil for a wait
// without a limit. The function stops the timer.
func (w *terminalWaiter) deadline(clock quartz.Clock) (<-chan time.Time, func()) {
	if w.spec.timeout <= 0 {
		return nil, func() {}
	}
	timer := clock.NewTimer(w.spec.timeout, terminalWaitTimerTag)
	return timer.C, func() { timer.Stop(terminalWaitTimerTag) }
}

// record takes a read of the terminal. A read states the exit code of a shell
// that ended, unless the Worker holds no record of the exit. done reports that
// the wait ended.
func (w *terminalWaiter) record(info *leapmuxv1.TerminalInfo) (terminalWaitOutcome, bool) {
	if info.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED {
		return w.shellEnded(exitCodeOf(info))
	}
	return w.status(info.GetStatus(), info.GetStartupError())
}

// status takes a status of the shell from a status change. done reports that
// the wait ended.
func (w *terminalWaiter) status(status leapmuxv1.TerminalStatus, startupError string) (terminalWaitOutcome, bool) {
	if status == leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED {
		return 0, false
	}
	w.result.Status = enumToken(status)
	switch status {
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
		// A status change states no exit code. A wait for exit therefore reads
		// the record of the terminal instead of passing EXITED here (see
		// awaitShellExit), and a wait for text ends without the code.
		return w.shellEnded(nil)
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
		w.result.StartupError = startupError
		return terminalWaitStartupFailed, true
	default:
		return 0, false
	}
}

// event takes one event of the terminal other than its bytes, which only a
// wait for text reads.
func (w *terminalWaiter) event(te *leapmuxv1.TerminalEvent) (terminalWaitOutcome, bool) {
	switch e := te.GetEvent().(type) {
	case *leapmuxv1.TerminalEvent_Closed:
		// An unset code is an exit that the Worker holds no record of, not a
		// clean exit.
		return w.shellEnded(e.Closed.ExitCode)
	case *leapmuxv1.TerminalEvent_StatusChange:
		return w.status(e.StatusChange.GetStatus(), e.StatusChange.GetStartupError())
	default:
		return 0, false
	}
}

// shellEnded takes the end of the shell, with its exit code when the wait
// saw it. It meets a wait for exit, and ends a wait for text without its
// condition.
func (w *terminalWaiter) shellEnded(code *int32) (terminalWaitOutcome, bool) {
	w.result.Status = enumToken(leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED)
	if code != nil {
		w.result.ExitCode = code
	}
	if w.spec.exit {
		w.result.Reached = true
		return terminalWaitHeld, true
	}
	return terminalWaitShellEnded, true
}

// screenChanged checks the screen after it changed, and ends a wait for
// text when a line matches.
func (w *terminalWaiter) screenChanged(screen *screenWatch) (terminalWaitOutcome, bool, error) {
	line, ok, err := screen.check()
	if err != nil || !ok {
		return 0, false, err
	}
	w.result.Line, w.result.Reached = line, true
	return terminalWaitHeld, true, nil
}

// output feeds one chunk of the terminal to the screen, and checks it. A
// chunk that replaces the whole state resets the screen first.
func (w *terminalWaiter) output(screen *screenWatch, data *leapmuxv1.TerminalData) (terminalWaitOutcome, bool, error) {
	var outcome terminalWaitOutcome
	var done bool
	err := screen.consumeScreenOutput(data, func() (bool, error) {
		var checkErr error
		outcome, done, checkErr = w.screenChanged(screen)
		return done, checkErr
	})
	if done || err != nil {
		return outcome, done, err
	}
	return w.screenChanged(screen)
}

// runTerminalWait waits until the terminal meets the condition of the
// command line. It prints the result whether the condition held or not, and
// exits 6 when it did not.
func runTerminalWait(inv *cli.Invocation) (any, error) {
	spec, err := terminalWaitSpecOf(inv)
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	t, info, err := terminalWaitTarget(s, inv.Arg("terminal"), spec)
	if err != nil {
		return nil, err
	}
	var result terminalWaitResult
	var outcome terminalWaitOutcome
	if spec.exit {
		result, outcome, err = waitForShellExit(s, t, info, spec)
	} else {
		result, outcome, err = waitForScreenText(s, t, info, spec)
	}
	if err != nil {
		return nil, err
	}
	if outcome != terminalWaitHeld {
		return nil, terminalWaitEnded(inv.Printer, result, outcome, spec)
	}
	// Every path to an ended shell reads a record or a TerminalClosed, and both
	// state the exit code whenever the Worker holds a record of it. Only a
	// Worker that was killed while the shell ran holds none.
	if spec.exit && result.ExitCode == nil {
		inv.Printer.Notice("the Worker holds no record of the exit code of the shell of terminal %s, because it did not see the shell end, so no exit code prints", t.ID)
	}
	return result, nil
}

// terminalWaitTarget resolves the terminal and reads its record. A wait for
// the exit of the calling terminal is refused: the wait runs in that shell,
// so the shell cannot end while it runs.
func terminalWaitTarget(s *session, input string, spec terminalWaitSpec) (tabTarget, *leapmuxv1.TerminalInfo, error) {
	bounded := s.bounded()
	defer bounded.Close()
	t, err := bounded.resolveTab(resolve.KindTerminal, input, resolve.Need{WorkerID: true})
	if err != nil {
		return tabTarget{}, nil, err
	}
	if spec.exit && (t.Self || t.ID == bounded.listing().SelfTabID(resolve.KindTerminal)) {
		return tabTarget{}, nil, cli.Usagef("terminal %s is the calling terminal, whose shell cannot end while the wait runs in it", t.ID).
			WithHint("wait from another tab, or for another terminal")
	}
	info, err := bounded.terminalInfo(t)
	if err != nil {
		return tabTarget{}, nil, err
	}
	return t, info, nil
}

// waitForShellExit waits until the shell of t ends. info is the record of
// the terminal when the wait began.
//
// It watches the terminal in notify mode, which carries the exit and the
// status changes but no bytes. A notify watch replays nothing, so after the
// Worker acknowledges each subscription, the wait reads the record of the
// terminal again: a shell that ended before the watch, or during a gap
// between two subscriptions, then still ends the wait, with the exit code
// that the record states.
func waitForShellExit(s *session, t tabTarget, info *leapmuxv1.TerminalInfo, spec terminalWaitSpec) (terminalWaitResult, terminalWaitOutcome, error) {
	w := newTerminalWaiter(t.ID, spec)
	if outcome, done := w.record(info); done {
		return w.result, outcome, nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	out := make(chan feedOutput, 16)
	feed := newWorkerFeed(t.WorkerID, workerFeedSourceOf(s.c, t.WorkerID), out)
	feed.setInterest(watchInterest{terminals: []string{t.ID}})
	feed.start(ctx, &wg)
	readStatus := func() (*leapmuxv1.TerminalInfo, error) {
		bounded := s.bounded()
		defer bounded.Close()
		return bounded.terminalInfo(t)
	}
	return awaitShellExit(ctx, quartz.NewReal(), out, readStatus, w)
}

// terminalStatusRead is the result of one read of the terminal's status.
type terminalStatusRead struct {
	info *leapmuxv1.TerminalInfo
	err  error
}

// awaitShellExit reads the notify watch of the terminal until its shell
// ends, the timeout passes, or the watch fails. readStatus reads the record
// of the terminal. The wait runs it after each first acknowledgement of a
// subscription, and after a status change to EXITED.
func awaitShellExit(ctx context.Context, clock quartz.Clock, feed <-chan feedOutput, readStatus func() (*leapmuxv1.TerminalInfo, error), w *terminalWaiter) (terminalWaitResult, terminalWaitOutcome, error) {
	deadline, stop := w.deadline(clock)
	defer stop()
	reads := make(chan terminalStatusRead, 1)
	// readRecord reads the record of the terminal on a goroutine of its own.
	// The read waits on the Worker, and the events of the watch keep arriving
	// meanwhile. An exit is final, so the order of the read and an event does
	// not matter.
	readRecord := func() {
		go func() {
			info, err := readStatus()
			select {
			case reads <- terminalStatusRead{info: info, err: err}:
			case <-ctx.Done():
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return w.result, 0, context.Cause(ctx)
		case <-deadline:
			return w.result, terminalWaitTimedOut, nil
		case read := <-reads:
			if read.err != nil {
				return w.result, 0, read.err
			}
			if outcome, done := w.record(read.info); done {
				return w.result, outcome, nil
			}
		case out := <-feed:
			switch out.kind {
			case feedLevels:
				if out.first {
					readRecord()
				}
			case feedTerminalEvent:
				// A status change to EXITED states no exit code, and the
				// TerminalClosed of the same exit can arrive after it. The
				// record states the code, so the wait reads it instead of
				// ending without one.
				if out.terminal.GetStatusChange().GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED {
					readRecord()
					continue
				}
				if outcome, done := w.event(out.terminal); done {
					return w.result, outcome, nil
				}
			case feedRejected:
				if !out.rejectedTerminal {
					continue
				}
				if err := terminalRejection(out.rejection); err != nil {
					return w.result, 0, err
				}
			case feedRefused:
				return w.result, 0, out.err
			case feedDown, feedAgentEvent:
				// The watch subscribes again by itself, and the status read
				// after its next acknowledgement covers an exit in the gap.
			}
		}
	}
}

// waitForScreenText waits until a line of the terminal t matches. info is
// the record of the terminal when the wait began: its size, the bytes that
// replay its screen, and the offset where the watch of its output resumes.
func waitForScreenText(s *session, t tabTarget, info *leapmuxv1.TerminalInfo, spec terminalWaitSpec) (terminalWaitResult, terminalWaitOutcome, error) {
	w := newTerminalWaiter(t.ID, spec)
	screen, err := newScreenWatch(info, spec.match, spec.scrollback)
	if err != nil {
		return w.result, 0, err
	}
	defer screen.dispose()
	terminalOutcome, terminalEnded := w.record(info)
	// The screen when the wait begins counts. A shell that already ended
	// shows nothing new, so the wait for text ends without its condition.
	if outcome, done, err := w.screenChanged(screen); err != nil || done {
		return w.result, outcome, err
	}
	if terminalEnded {
		return w.result, terminalOutcome, nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	items := make(chan screenFeedItem, 16)
	startScreenFeed(ctx, &wg, workerScreenFeedSource(s.c, t.WorkerID), t.ID, screen.position, items)
	return awaitScreenText(ctx, quartz.NewReal(), items, screen, w)
}

// awaitScreenText feeds the output of the terminal to screen until a line
// matches, the shell ends, the timeout passes, or the watch fails.
func awaitScreenText(ctx context.Context, clock quartz.Clock, items <-chan screenFeedItem, screen *screenWatch, w *terminalWaiter) (terminalWaitResult, terminalWaitOutcome, error) {
	deadline, stop := w.deadline(clock)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return w.result, 0, context.Cause(ctx)
		case <-deadline:
			return w.result, terminalWaitTimedOut, nil
		case item := <-items:
			if item.kind != screenFeedEvent {
				return w.result, 0, item.err
			}
			var outcome terminalWaitOutcome
			var done bool
			var err error
			if data := item.event.GetData(); data != nil {
				outcome, done, err = w.output(screen, data)
			} else if geometry := item.event.GetScreenGeometry(); geometry != nil {
				if err = screen.applyLiveScreenGeometry(geometry); err == nil {
					outcome, done, err = w.screenChanged(screen)
				}
			} else {
				outcome, done = w.event(item.event)
			}
			if err != nil || done {
				return w.result, outcome, err
			}
		}
	}
}

// terminalWaitEnded reports a wait whose condition did not hold: it prints
// the result, says why on stderr, and returns exit status 6. The result
// prints in each case, so a script reads the status of the terminal either
// way.
func terminalWaitEnded(p *cli.Printer, result terminalWaitResult, outcome terminalWaitOutcome, spec terminalWaitSpec) error {
	if err := p.Result(result); err != nil {
		return err
	}
	switch outcome {
	case terminalWaitTimedOut:
		if spec.exit {
			p.Notice("the wait ended after %s, before the shell of terminal %s ended", spec.timeout, result.ID)
		} else {
			p.Notice("the wait ended after %s, before terminal %s showed %s", spec.timeout, result.ID, spec.condition)
		}
	case terminalWaitShellEnded:
		if result.ExitCode != nil {
			p.Notice("the shell of terminal %s exited with code %d before the terminal showed %s", result.ID, *result.ExitCode, spec.condition)
		} else {
			p.Notice("the shell of terminal %s ended before the terminal showed %s", result.ID, spec.condition)
		}
	case terminalWaitStartupFailed:
		if result.StartupError != "" {
			p.Notice("the shell of terminal %s failed to start: %s", result.ID, result.StartupError)
		} else {
			p.Notice("the shell of terminal %s failed to start", result.ID)
		}
	case terminalWaitHeld:
		// A wait whose condition held does not end here.
	}
	return &cli.ExitStatus{Code: cli.ExitWaitEnded}
}

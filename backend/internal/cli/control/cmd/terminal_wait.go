package cmd

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

// terminalWaitLong is the help of `terminal wait`.
const terminalWaitLong = `Wait until a terminal meets one condition, then print what met it:

  - --exit waits until the shell of the terminal ends, and prints its exit
    code.
  - --text waits until a line of the terminal holds the text.
  - --regex waits until a line of the terminal matches the regular
    expression, in RE2 syntax.
  - --change waits until the visible screen text changes (event-driven over
    the live Worker output stream, with no polling), and prints the updated
    screen. Pass --after-offset with screen_end_offset from a prior
    "terminal read --json" or "terminal wait --change --json" to return
    immediately if the terminal already produced output since that offset.

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

"leapmux events --kind terminal" prints the bells, the titles, the
notifications, the progress reports and the exits of terminals as they
happen.`

// newTerminalWaitLeaf builds `terminal wait`.
func newTerminalWaitLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "wait <terminal>",
		Short: "Wait until a terminal shows a text, or until its shell ends",
		Long:  terminalWaitLong,
		Examples: []cli.Example{
			{Comment: "Wait until the shell ends, and keep its exit code.", Command: "CODE=$(leapmux terminal wait 4cbb3f34 --exit)"},
			{Comment: "Run the tests, then wait for the line that the last command prints.", Command: "leapmux terminal run 4cbb3f34 'make test; echo DONE' && leapmux terminal wait 4cbb3f34 --regex '^DONE$' --timeout 10m"},
			{Comment: "Wait until a server reports that it listens, for 2 minutes at most.", Command: "leapmux terminal wait 4cbb3f34 --text 'Listening on' --timeout 2m"},
			{Comment: "Look for a completed build in the terminal's retained output.", Command: "leapmux terminal wait 4cbb3f34 --text 'Build complete' --scrollback"},
		},
		Flags: func(fs *pflag.FlagSet) {
			addTerminalWaitConditionFlags(fs, true)
			for _, name := range terminalWaitFlagNames(true) {
				cli.SetFlagGroup(fs, name, "Wait")
			}
			addTargetWorkspaceFlag(fs)
			cli.SetFlagGroup(fs, "workspace", "Scope")
		},
		Complete: completeTargets,
		Run:      runTerminalWait,
	})
}

// terminalWaitFlagNames returns the wait condition flag names registered by
// addTerminalWaitConditionFlags.
func terminalWaitFlagNames(includeAfterOffset bool) []string {
	if includeAfterOffset {
		return []string{"exit", "text", "regex", "change", "after-offset", "scrollback", "timeout"}
	}
	return []string{"exit", "text", "regex", "change", "scrollback", "timeout"}
}

// addTerminalWaitConditionFlags registers --exit, --text, --regex, --change,
// --scrollback, and --timeout, plus --after-offset when includeAfterOffset is
// true.
func addTerminalWaitConditionFlags(fs *pflag.FlagSet, includeAfterOffset bool) {
	fs.Bool("exit", false, "wait until the shell of the terminal ends, and print its exit code")
	fs.String("text", "", "wait until a line of the terminal holds this text")
	fs.String("regex", "", "wait until a line of the terminal matches this regular expression, in RE2 syntax")
	fs.Bool("change", false, "wait until the visible screen text changes, and print the updated screen")
	if includeAfterOffset {
		fs.Int64("after-offset", 0, "with --change, return immediately if screen_end_offset already exceeds this byte offset")
	}
	fs.Bool("scrollback", false, "also search the retained scrollback from before the wait")
	fs.Duration("timeout", 0, "stop waiting after this long, such as 90s or 10m; 0 waits without a limit")
}

// optionalTerminalWaitSpec parses --wait and its condition flags for commands
// where waiting is optional (`terminal open`, `terminal run`).
func optionalTerminalWaitSpec(inv *cli.Invocation, includeAfterOffset bool) (wait bool, spec terminalWaitSpec, err error) {
	if !boolFlag(inv, "wait") {
		for _, name := range terminalWaitFlagNames(includeAfterOffset) {
			if inv.Changed(name) {
				return false, terminalWaitSpec{}, cli.Usagef("--%s needs --wait", name)
			}
		}
		return false, terminalWaitSpec{}, nil
	}
	spec, err = terminalWaitSpecOf(inv)
	if err != nil {
		return false, terminalWaitSpec{}, err
	}
	return true, spec, nil
}

// refuseSelfTerminalExitWait refuses --exit when target is the calling
// terminal, because its shell cannot end while the wait runs in it.
func refuseSelfTerminalExitWait(target tabTarget, selfID string, spec terminalWaitSpec) error {
	if spec.mode == terminalWaitExit && (target.Self || target.ID == selfID) {
		return cli.Usagef("terminal %s is the calling terminal, whose shell cannot end while the wait runs in it", target.ID).
			WithHint("wait from another tab, or for another terminal")
	}
	return nil
}

// terminalWaitMode selects what condition a terminal wait evaluates.
type terminalWaitMode uint8

const (
	terminalWaitText terminalWaitMode = iota
	terminalWaitExit
	terminalWaitChange
)

// terminalWaitSpec states one wait for a terminal.
type terminalWaitSpec struct {
	// mode selects whether the wait checks for matching text, shell exit, or a
	// visible screen change.
	mode terminalWaitMode
	// preOpenBaseline treats the baseline before opening a new terminal as an
	// empty screen, so `terminal open --wait --change` returns immediately when
	// the new terminal already printed visible text before terminalInfo ran.
	preOpenBaseline bool
	// hasAfterOffset reports whether --after-offset was supplied with --change.
	hasAfterOffset bool
	// afterOffset is the byte offset from a prior read/wait.
	afterOffset int64
	// match reports whether one line of the terminal meets --text or
	// --regex.
	match func(line string) bool
	// scrollback includes existing history in the text search.
	scrollback bool
	// condition states the text of --text, --regex, or --change for a notice,
	// such as `the text "PASS"`.
	condition string
	// timeout ends the wait after this long. Zero waits without a limit.
	timeout time.Duration
	// requestID and retryCommand carry retry metadata from `terminal run --wait`.
	requestID    string
	retryCommand string
}

// terminalWaitSpecOf reads the flags of `terminal wait`, and refuses a
// command line that states no condition, or two, before anything is sent.
func terminalWaitSpecOf(inv *cli.Invocation) (terminalWaitSpec, error) {
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return terminalWaitSpec{}, err
	}
	hasExit := boolFlag(inv, "exit")
	hasChange := boolFlag(inv, "change")
	hasText, hasRegex := inv.Changed("text"), inv.Changed("regex")
	spec := terminalWaitSpec{
		hasAfterOffset: inv.Changed("after-offset"),
		timeout:        timeout,
		scrollback:     boolFlag(inv, "scrollback"),
	}
	if spec.hasAfterOffset {
		v, err := inv.Command.Flags().GetInt64("after-offset")
		if err != nil {
			return terminalWaitSpec{}, cli.Usagef("invalid --after-offset: %v", err)
		}
		if v < 0 {
			return terminalWaitSpec{}, cli.Usagef("--after-offset must be 0 or greater; got %d", v)
		}
		spec.afterOffset = v
	}
	modeCount := 0
	for _, set := range []bool{hasExit, hasChange, hasText, hasRegex} {
		if set {
			modeCount++
		}
	}
	switch {
	case spec.hasAfterOffset && !hasChange:
		return terminalWaitSpec{}, cli.Usagef("--after-offset needs --change")
	case hasExit && (hasText || hasRegex):
		return terminalWaitSpec{}, cli.Usagef("--exit takes no --text and no --regex").
			WithHint("a wait for text already ends with exit status 6 when the shell ends first")
	case modeCount > 1:
		return terminalWaitSpec{}, cli.Usagef("--exit, --text, --regex, and --change exclude each other").
			WithHint("pass one wait condition")
	case spec.scrollback && !hasText && !hasRegex:
		return terminalWaitSpec{}, cli.Usagef("--scrollback needs --text or --regex")
	case hasText:
		spec.mode = terminalWaitText
		text := stringFlag(inv, "text")
		spec.match, err = textMatcher(text)
		spec.condition = fmt.Sprintf("the text %q", text)
	case hasRegex:
		spec.mode = terminalWaitText
		pattern := stringFlag(inv, "regex")
		spec.match, err = regexMatcher(pattern)
		spec.condition = fmt.Sprintf("a line that matches %q", pattern)
	case hasChange:
		spec.mode = terminalWaitChange
		spec.condition = "a screen change"
	case hasExit:
		spec.mode = terminalWaitExit
	default:
		return terminalWaitSpec{}, cli.Usagef("missing the condition to wait for").
			WithHint("pass --exit, --text <text>, --regex <pattern>, or --change")
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
	ID        string `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	// Status is the last status of the shell that the wait observed: a token
	// of TerminalStatus, such as "ready" or "exited", or "unknown".
	Status string `json:"status"`
	// Reached reports whether the condition held.
	Reached bool `json:"reached"`
	// Changed reports whether visible screen text changed for --change.
	Changed bool `json:"changed,omitempty"`
	// ScreenEndOffset is the byte offset of the terminal stream at completion
	// (pass to --after-offset on the next `terminal wait --change`).
	ScreenEndOffset int64 `json:"screen_end_offset,omitempty"`
	// Screen holds the visible screen text for --change. RenderText sanitizes
	// it before writing to a terminal.
	Screen string `json:"screen,omitempty"`
	// ExitCode is the exit code of the shell, when the wait saw the shell
	// end with one. A shell that a signal ended reports -1.
	ExitCode *int32 `json:"exit_code,omitempty"`
	// Line is the line of the terminal that matched --text or --regex.
	Line string `json:"line,omitempty"`
	// StartupError states why the shell failed to start.
	StartupError string `json:"startup_error,omitempty"`
	// Receipt carries recovery metadata when a keyed wait times out.
	Receipt *cli.RetryReceipt `json:"receipt,omitempty"`
	// mode selects the text form.
	mode terminalWaitMode
}

// RenderText prints the exit code of a wait for exit, the updated screen for
// --change, or the line that met a wait for text, and nothing otherwise.
func (r terminalWaitResult) RenderText(w io.Writer) error {
	var err error
	switch r.mode {
	case terminalWaitExit:
		if r.ExitCode != nil {
			_, err = fmt.Fprintln(w, *r.ExitCode)
		}
	case terminalWaitChange:
		if r.Reached && r.Screen != "" {
			_, err = fmt.Fprintln(w, cli.SanitizeTerminalText(r.Screen))
		}
	case terminalWaitText:
		if r.Reached {
			_, err = fmt.Fprintln(w, cli.SanitizeTerminalText(r.Line))
		}
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
	return &terminalWaiter{
		spec: spec,
		result: terminalWaitResult{
			ID:        id,
			RequestID: spec.requestID,
			Status:    terminalStatusUnknown,
			mode:      spec.mode,
		},
	}
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
	return w.status(info.GetStatus(), info.GetStartupError(), exitCodeOf(info))
}

// status takes a status of the shell from a status change or terminal record.
// done reports that the wait ended.
func (w *terminalWaiter) status(status leapmuxv1.TerminalStatus, startupError string, exitCode *int32) (terminalWaitOutcome, bool) {
	if status == leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED {
		return 0, false
	}
	w.result.Status = enumToken(status)
	switch status {
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
		return w.shellEnded(exitCode)
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
		return w.status(e.StatusChange.GetStatus(), e.StatusChange.GetStartupError(), e.StatusChange.ExitCode)
	default:
		return 0, false
	}
}

// shellEnded takes the end of the shell, with its exit code when the wait
// saw it. It meets a wait for exit, and ends a wait for text or screen change
// without its condition.
func (w *terminalWaiter) shellEnded(code *int32) (terminalWaitOutcome, bool) {
	w.result.Status = enumToken(leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED)
	if code != nil {
		v := *code
		w.result.ExitCode = &v
	}
	if w.spec.mode == terminalWaitExit {
		w.result.Reached = true
		return terminalWaitHeld, true
	}
	return terminalWaitShellEnded, true
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
	return waitTerminalWithInfo(inv, s, t, info, spec)
}

// waitTerminalForTarget reads the terminal's current info on a time-limited
// call and runs the wait condition against it (used by `terminal open --wait`).
func waitTerminalForTarget(inv *cli.Invocation, s *session, t tabTarget, spec terminalWaitSpec) (any, error) {
	callSession := s.timed()
	info, err := callSession.terminalInfo(t)
	callSession.Close()
	if err != nil {
		return nil, err
	}
	return waitTerminalWithInfo(inv, s, t, info, spec)
}

// readTerminalInfo fetches the TerminalInfo for terminalID using call.
func readTerminalInfo(ctx context.Context, call func(context.Context, string, proto.Message, proto.Message) error, terminalID string) (*leapmuxv1.TerminalInfo, error) {
	var resp leapmuxv1.ListTerminalsResponse
	if err := call(ctx, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, &resp); err != nil {
		return nil, err
	}
	for _, info := range resp.GetTerminals() {
		if info.GetTerminalId() == terminalID {
			return info, nil
		}
	}
	return nil, cli.Newf(cli.CodeNotFound, "the worker has no terminal %s", terminalID)
}

func waitTerminalWithInfo(inv *cli.Invocation, s *session, t tabTarget, info *leapmuxv1.TerminalInfo, spec terminalWaitSpec) (any, error) {
	spinMsg := fmt.Sprintf("waiting for terminal %s", cli.ShortID(t.ID))
	if spec.mode == terminalWaitExit {
		spinMsg += " to exit"
	} else if spec.condition != "" {
		spinMsg += " (" + spec.condition + ")"
	}
	spin := cli.StartSpinner(inv.Printer, spinMsg)
	defer spin.Stop()
	var result terminalWaitResult
	var outcome terminalWaitOutcome
	var err error
	if spec.mode == terminalWaitExit {
		result, outcome, err = waitForShellExit(s, t, info, spec)
	} else {
		result, outcome, err = waitForScreen(s, t, info, spec)
	}
	spin.Stop()
	if err != nil {
		return nil, err
	}
	if outcome != terminalWaitHeld {
		return nil, terminalWaitEnded(inv.Printer, result, outcome, spec)
	}
	// Every path to an ended shell reads a record or a TerminalClosed, and both
	// state the exit code whenever the Worker holds a record of it. Only a
	// Worker that was killed while the shell ran holds none.
	if spec.mode == terminalWaitExit && result.ExitCode == nil {
		_ = inv.Printer.Notice("the Worker holds no record of the exit code of the shell of terminal %s, because it did not see the shell end, so no exit code prints", t.ID)
	}
	return result, nil
}

// terminalWaitTarget resolves the terminal and reads its record. A wait for
// the exit of the calling terminal is refused: the wait runs in that shell,
// so the shell cannot end while it runs.
func terminalWaitTarget(s *session, input string, spec terminalWaitSpec) (tabTarget, *leapmuxv1.TerminalInfo, error) {
	callSession := s.timed()
	defer callSession.Close()
	t, err := callSession.resolveTab(resolve.KindTerminal, input, resolve.Need{WorkerID: true})
	if err != nil {
		return tabTarget{}, nil, err
	}
	if err := refuseSelfTerminalExitWait(t, callSession.listing().SelfTabID(resolve.KindTerminal), spec); err != nil {
		return tabTarget{}, nil, err
	}
	info, err := callSession.terminalInfo(t)
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
// terminal again over the active watch transport: a shell that ended before
// the watch, or during a gap between two subscriptions, then still ends the
// wait, with the exit code that the record states.
func waitForShellExit(s *session, t tabTarget, info *leapmuxv1.TerminalInfo, spec terminalWaitSpec) (terminalWaitResult, terminalWaitOutcome, error) {
	w := newTerminalWaiter(t.ID, spec)
	if outcome, done := w.record(info); done {
		return w.result, outcome, nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	var activeTransport atomic.Pointer[workerWatchTransport]
	source := workerFeedSource{
		openTransport: func(openCtx context.Context) (streamevents.Transport, func(), error) {
			wt, err := newWorkerWatchTransport(openCtx, s.c, t.WorkerID)
			if err != nil {
				return nil, nil, &codedRPCError{Code: "channel_open_failed", Cause: err}
			}
			activeTransport.Store(wt)
			release := func() {
				activeTransport.CompareAndSwap(wt, nil)
				wt.close()
			}
			return wt.transport, release, nil
		},
		clock: quartz.NewReal(),
	}
	out := make(chan feedOutput, 16)
	feed := newWorkerFeed(t.WorkerID, source, out)
	feed.setInterest(watchInterest{terminals: []string{t.ID}})
	feed.start(ctx, &wg)
	readStatus := func(readCtx context.Context) (*leapmuxv1.TerminalInfo, error) {
		callCtx, cancelCall := rpcDeadline(readCtx)
		defer cancelCall()
		call := func(c context.Context, method string, in, out proto.Message) error {
			if wt := activeTransport.Load(); wt != nil {
				return wt.Call(c, method, in, out)
			}
			return callInnerRPC(c, s.c, t.WorkerID, method, in, out)
		}
		return readTerminalInfo(callCtx, call, t.ID)
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
func awaitShellExit(ctx context.Context, clock quartz.Clock, feed <-chan feedOutput, readStatus func(context.Context) (*leapmuxv1.TerminalInfo, error), w *terminalWaiter) (terminalWaitResult, terminalWaitOutcome, error) {
	deadline, stop := w.deadline(clock)
	defer stop()
	readCtx, cancelReads := context.WithCancel(ctx)
	readTrigger := make(chan struct{}, 1)
	reads := make(chan terminalStatusRead, 1)
	var readWG sync.WaitGroup
	defer readWG.Wait()
	defer cancelReads()
	// Serialize status reads on one tracked goroutine derived from ctx so
	// watch events keep arriving while a read waits on the Worker and no
	// goroutine outlives awaitShellExit.
	readWG.Go(func() {
		for {
			select {
			case <-readCtx.Done():
				return
			case <-readTrigger:
			}
			info, err := readStatus(readCtx)
			select {
			case reads <- terminalStatusRead{info: info, err: err}:
			case <-readCtx.Done():
				return
			}
		}
	})
	readRecord := func() {
		select {
		case readTrigger <- struct{}{}:
		default:
		}
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
				// When a status change to EXITED carries exit_code, finish
				// immediately without an extra ListTerminals round trip. Only
				// fall back to reading the record when exit_code is absent.
				if sc := out.terminal.GetStatusChange(); sc != nil &&
					sc.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED &&
					sc.ExitCode == nil {
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

// screenTextCheck returns a check callback that matches lines on watch and
// records the matching line in w.result.
func screenTextCheck(watch *screenWatch, w *terminalWaiter) func() (bool, error) {
	return func() (bool, error) {
		line, ok, err := watch.check()
		if err != nil || !ok {
			return false, err
		}
		w.result.Line, w.result.Reached = line, true
		return true, nil
	}
}

// screenChangeCheck returns a check callback that compares the visible screen
// of em against initialLines and records the updated screen in w.result.
func screenChangeCheck(em *screenEmulator, w *terminalWaiter, initialLines []string) func() (bool, error) {
	return func() (bool, error) {
		w.result.ScreenEndOffset = em.position.offset
		cur, changed, err := em.diffVisibleScreen(initialLines)
		if err != nil || !changed {
			return false, err
		}
		w.result.Screen = cur
		w.result.Reached = true
		w.result.Changed = true
		return true, nil
	}
}

// waitForScreen waits until terminal t meets a screen condition: a matching
// line for --text/--regex, or a visible screen change for --change. info is
// the record of the terminal when the wait began: its size, the bytes that
// replay its screen, and the offset where the watch of its output resumes.
func waitForScreen(s *session, t tabTarget, info *leapmuxv1.TerminalInfo, spec terminalWaitSpec) (terminalWaitResult, terminalWaitOutcome, error) {
	w := newTerminalWaiter(t.ID, spec)
	var em *screenEmulator
	var check func() (bool, error)
	switch spec.mode {
	case terminalWaitChange:
		var err error
		em, err = newScreenEmulator(info)
		if err != nil {
			return w.result, 0, err
		}
		defer em.dispose()
		terminalOutcome, terminalEnded := w.record(info)
		initialLines, err := em.visibleLines()
		if err != nil {
			return w.result, 0, err
		}
		w.result.ScreenEndOffset = em.position.offset
		w.result.Screen = strings.Join(initialLines, "\n")
		if spec.preOpenBaseline && len(initialLines) > 0 ||
			!spec.preOpenBaseline && spec.hasAfterOffset && em.position.offset > spec.afterOffset {
			w.result.Reached = true
			w.result.Changed = true
			return w.result, terminalWaitHeld, nil
		}
		if terminalEnded {
			return w.result, terminalOutcome, nil
		}
		check = screenChangeCheck(em, w, initialLines)
	case terminalWaitText:
		watch, err := newScreenWatch(info, spec.match, spec.scrollback)
		if err != nil {
			return w.result, 0, err
		}
		defer watch.dispose()
		em = &watch.screenEmulator
		check = screenTextCheck(watch, w)
		terminalOutcome, terminalEnded := w.record(info)
		// The screen when the wait begins counts. A shell that already ended
		// shows nothing new, so the wait for text ends without its condition.
		if matched, err := check(); err != nil {
			return w.result, 0, err
		} else if matched {
			return w.result, terminalWaitHeld, nil
		}
		if terminalEnded {
			return w.result, terminalOutcome, nil
		}
	default:
		return w.result, 0, cli.Newf(cli.CodeInternal, "unexpected screen wait mode %d for terminal %s", spec.mode, t.ID)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	items := make(chan screenFeedItem, 16)
	startScreenFeed(ctx, &wg, workerScreenFeedSource(s.c, t.WorkerID), t.ID, em.position, items)
	return awaitScreen(ctx, quartz.NewReal(), items, em, w, check)
}

// awaitScreen feeds the output of the terminal to em until check reports that
// the condition held, the shell ends, the timeout passes, or the watch fails.
func awaitScreen(ctx context.Context, clock quartz.Clock, items <-chan screenFeedItem, em *screenEmulator, w *terminalWaiter, check func() (bool, error)) (terminalWaitResult, terminalWaitOutcome, error) {
	deadline, stop := w.deadline(clock)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return w.result, 0, context.Cause(ctx)
		case <-deadline:
			return w.result, terminalWaitTimedOut, nil
		case item := <-items:
			if item.event == nil {
				return w.result, 0, screenFeedError(w.result.ID, item)
			}
			var outcome terminalWaitOutcome
			var done bool
			var err error
			if data := item.event.GetData(); data != nil {
				err = em.consumeScreenOutput(data, func() (bool, error) {
					matched, checkErr := check()
					if matched {
						outcome, done = terminalWaitHeld, true
					}
					return matched, checkErr
				})
			} else if geometry := item.event.GetScreenGeometry(); geometry != nil {
				if err = em.applyLiveScreenGeometry(geometry); err == nil {
					var matched bool
					matched, err = check()
					if matched {
						outcome, done = terminalWaitHeld, true
					}
				}
			} else {
				outcome, done = w.event(item.event)
			}
			if err != nil {
				return w.result, 0, err
			}
			if done {
				return w.result, outcome, nil
			}
		}
	}
}

// screenFeedError maps a non-event screenFeedItem to the error that
// `terminal wait` reports.
func screenFeedError(terminalID string, item screenFeedItem) error {
	if item.rejected != nil {
		return terminalRejection(item.rejected)
	}
	if item.overflow {
		return cli.Newf(cli.CodeUnavailable,
			"terminal %s produced too much screen output before replay finished", terminalID).
			WithExitCode(cli.ExitWaitEnded)
	}
	return item.err
}

// terminalRejection is the error that one rejected terminal of an
// acknowledgement becomes. It is nil for a rejection that the subscription
// retries by itself.
//
// A terminal that the Worker no longer holds closed during the wait, so the
// wait ended without its condition: the error exits 6, as `agent send --wait`
// does for an agent that closed.
func terminalRejection(rejection *leapmuxv1.WatchRejection) error {
	id := rejection.GetEntityId()
	disp, err := classifyWatchRejection("terminal", id, rejection.GetReason())
	switch disp {
	case watchRejectionRetry:
		return nil
	case watchRejectionNotFound:
		return cli.Newf(cli.CodeNotFound, "terminal %s closed during the wait", id).
			WithExitCode(cli.ExitWaitEnded)
	default:
		return err
	}
}

// terminalWaitEnded reports a wait whose condition did not hold: it prints
// the result, says why on stderr, and returns exit status 6. The result
// prints in each case, so a script reads the status of the terminal either
// way.
func terminalWaitEnded(p *cli.Printer, result terminalWaitResult, outcome terminalWaitOutcome, spec terminalWaitSpec) error {
	if outcome == terminalWaitTimedOut && spec.requestID != "" {
		result.Receipt = &cli.RetryReceipt{
			RequestID:    spec.requestID,
			Disposition:  dispositionInProgress,
			RetryCommand: spec.retryCommand,
		}
	}
	if err := p.Result(result); err != nil {
		return err
	}
	switch outcome {
	case terminalWaitTimedOut:
		if spec.mode == terminalWaitExit {
			_ = p.Notice("the wait ended after %s, before the shell of terminal %s ended", spec.timeout, result.ID)
		} else {
			_ = p.Notice("the wait ended after %s, before terminal %s showed %s", spec.timeout, result.ID, spec.condition)
		}
	case terminalWaitShellEnded:
		if result.ExitCode != nil {
			_ = p.Notice("the shell of terminal %s exited with code %d before the terminal showed %s", result.ID, *result.ExitCode, spec.condition)
		} else {
			_ = p.Notice("the shell of terminal %s ended before the terminal showed %s", result.ID, spec.condition)
		}
	case terminalWaitStartupFailed:
		if result.StartupError != "" {
			_ = p.Notice("the shell of terminal %s failed to start: %s", result.ID, result.StartupError)
		} else {
			_ = p.Notice("the shell of terminal %s failed to start", result.ID)
		}
	case terminalWaitHeld:
		// A wait whose condition held does not end here.
	}
	if result.Receipt != nil {
		_ = p.Notice("disposition: %s (request_id: %s)", result.Receipt.Disposition, result.Receipt.RequestID)
		if result.Receipt.RetryCommand != "" {
			_ = p.Notice("safe retry: %s", result.Receipt.RetryCommand)
		}
	}
	return &cli.ExitStatus{Code: cli.ExitWaitEnded}
}

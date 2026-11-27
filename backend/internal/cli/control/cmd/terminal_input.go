package cmd

import (
	"strings"
	"unicode/utf8"

	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/terminput"
)

// helpWidth is the width at which a help text breaks its lines.
const helpWidth = 78

// terminalSendLong returns the help of `terminal send`. It reads the key
// names from terminput, which the Worker reads to encode them, so the help
// cannot drift from either.
func terminalSendLong() string {
	keys := wrapWords("--key presses a key after the text. Repeat it to press several keys in "+
		"order. The Worker encodes each key for the modes that the program in the "+
		`terminal enabled, as the browser does, so "up" moves the cursor in a shell and `+
		"in a full-screen program alike. A modifier on a cursor, editing or function "+
		"key takes xterm's encoding, such as ESC [1;5D for ctrl-left. Key names: "+
		terminput.Vocabulary()+".", helpWidth)
	return `Type text into a terminal, then press keys. The text reaches the shell as it
is, with no Enter added: add --key enter, or use "terminal run". The text is
the second argument, "-" to read it from stdin, or --file. A text that starts
with "-" needs "--" before it.

` + keys + `

--paste sends the text as the browser sends a paste: each line ending becomes
a carriage return, and when the program enabled bracketed paste, the text
arrives between the paste markers, so the program inserts it instead of
running each line. A paste end marker inside the text is removed, so the text
cannot end its own paste.

The text and the keys travel in one request, so no other input can arrive
between them.`
}

// wrapWords joins the words of text into lines of at most width runes. A word
// longer than width stands on a line of its own.
func wrapWords(text string, width int) string {
	var sb strings.Builder
	lineLen := 0
	for _, word := range strings.Fields(text) {
		n := utf8.RuneCountInString(word)
		switch {
		case lineLen == 0:
		case lineLen+1+n > width:
			sb.WriteByte('\n')
			lineLen = 0
		default:
			sb.WriteByte(' ')
			lineLen++
		}
		sb.WriteString(word)
		lineLen += n
	}
	return sb.String()
}

// addTerminalSendFlags registers the flags of `terminal send`.
func addTerminalSendFlags(fs *pflag.FlagSet) {
	fs.String("file", "", `read the bytes from this file ("-" reads stdin)`)
	fs.StringArray("key", nil, "press this key after the text, such as enter, ctrl-c or up; repeat it to press several keys in order")
	fs.Bool("paste", false, "send the text as a paste, between bracketed-paste markers when the program enabled them")
}

// terminalSendResult is the result of `terminal send`.
type terminalSendResult struct {
	ID string `json:"id"`
	// TextBytes counts the bytes of the text, before a paste changes them.
	TextBytes int  `json:"text_bytes"`
	Paste     bool `json:"paste,omitempty"`
	// Keys lists the keys pressed after the text, by their canonical names.
	Keys []string `json:"keys,omitempty"`
}

// terminalSendPayload reads and validates the text, --file, --key, and --paste
// inputs shared by `terminal send` and `terminal quake send`.
func terminalSendPayload(inv *cli.Invocation) (raw []byte, keys []string, paste bool, err error) {
	raw, hasText, err := optionalInputBytes(inv, "text")
	if err != nil {
		return nil, nil, false, err
	}
	keys, err = terminalKeyNames(inv)
	if err != nil {
		return nil, nil, false, err
	}
	paste = boolFlag(inv, "paste")
	switch {
	case !hasText && len(keys) == 0:
		return nil, nil, false, cli.Usagef("missing <text>").
			WithHint(`give the text as an argument, as "-" to read stdin, or with --file, or press a key with --key`)
	case paste && !hasText:
		return nil, nil, false, cli.Usagef("--paste needs a text").
			WithHint(`give the text as an argument, as "-" to read stdin, or with --file`)
	}
	return raw, keys, paste, nil
}

// runTerminalSend writes the text and then the keys to a terminal's PTY in
// one request. It refuses a command line that sends nothing, so a script
// cannot send an empty write by accident.
func runTerminalSend(inv *cli.Invocation) (any, error) {
	text, keys, paste, err := terminalSendPayload(inv)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	req := &leapmuxv1.SendInputRequest{TerminalId: got.ID, Data: text, Paste: paste, Keys: keys}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil); err != nil {
		return nil, err
	}
	return terminalSendResult{ID: got.ID, TextBytes: len(text), Paste: paste, Keys: keys}, nil
}

// terminalRunLong is the help of `terminal run`.
const terminalRunLong = `Run a command in a terminal: send the command line and press Enter, in one
request, so no other input can arrive between them. The command is the second
argument, "-" to read it from stdin, or --file. The line endings at its end are
dropped, because the command presses Enter itself.

The command line arrives as a paste, as "terminal send --paste" sends it. A
line editor that enabled bracketed paste therefore inserts it as it is: an
abbreviation, an automatic closing bracket or a vi key binding cannot change
it. A program without bracketed paste reads it as typed text. To type the
command key by key in any case, run
"terminal send <terminal> <command> --key enter".

By default the command returns as soon as the input is written. Pass --wait
with --exit, --text, --regex, or --change to wait until that condition holds,
or read output later with "terminal read".`

// terminalRunResult is the result of `terminal run`.
type terminalRunResult struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	RequestID string `json:"request_id,omitempty"`
}

// runTerminalRun sends a command line as a paste, then the Enter key, and
// optionally waits until --exit, --text, --regex, or --change holds.
func runTerminalRun(inv *cli.Invocation) (any, error) {
	requestID, explicitRequestID, err := ensureRequestID(inv)
	if err != nil {
		return nil, err
	}
	wait, waitSpec, err := optionalTerminalWaitSpec(inv, true)
	if err != nil {
		return nil, err
	}
	command, err := readRunCommand(inv)
	if err != nil {
		return nil, err
	}
	receiptFor := func(targetID string) retryReceipt {
		return retryReceipt{
			RequestID:    requestID,
			Explicit:     explicitRequestID,
			RetryCommand: buildTerminalRunRetryCmd(inv, targetID, command, wait, requestID),
		}
	}
	open := openSession
	if wait {
		open = openStreamSession
	}
	s, err := open(inv)
	if err != nil {
		return nil, wrapRetryReceipt(err, retryStageBeforeSend, receiptFor(inv.Arg("terminal")))
	}
	defer s.Close()
	callSession := s
	closeCall := func() {}
	if wait {
		callSession = s.bounded()
		closeCall = callSession.Close
	}
	got, err := callSession.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		closeCall()
		return nil, wrapRetryReceipt(err, retryStageBeforeSend, receiptFor(inv.Arg("terminal")))
	}
	if wait {
		if err := refuseSelfTerminalExitWait(got, callSession.listing().SelfTabID(resolve.KindTerminal), waitSpec); err != nil {
			closeCall()
			return nil, err
		}
	}
	if err := maybePreflightWorker(callSession.ctx, s.c, got.WorkerID); err != nil {
		closeCall()
		return nil, wrapRetryReceipt(err, retryStageBeforeSend, receiptFor(got.ID))
	}
	req := &leapmuxv1.SendInputRequest{
		TerminalId: got.ID,
		Data:       []byte(command),
		Paste:      true,
		Keys:       []string{"enter"},
		RequestId:  requestID,
	}
	var baselineInfo *leapmuxv1.TerminalInfo
	stage := retryStageBeforeSend
	err = withWorkerChannel(callSession.ctx, s.c, got.WorkerID, func(w workerCall) error {
		if wait {
			// Capture the baseline screen and stream offset before SendInput on
			// the same E2EE channel so --change and fast-scrolling output from
			// the command cannot race ahead of the wait baseline.
			var infoErr error
			baselineInfo, infoErr = readTerminalInfo(callSession.ctx, w.Call, got.ID)
			if infoErr != nil {
				return infoErr
			}
		}
		stage = retryStageDuringSend
		return w.Call(callSession.ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil)
	})
	closeCall()
	if err != nil {
		return nil, wrapRetryReceipt(err, stage, receiptFor(got.ID))
	}
	if !wait {
		return terminalRunResult{ID: got.ID, Command: command, RequestID: requestID}, nil
	}
	waitSpec.requestID = requestID
	waitSpec.retryCommand = buildTerminalRunRetryCmd(inv, got.ID, command, true, requestID)
	res, err := waitTerminalWithInfo(inv, s, got, baselineInfo, waitSpec)
	if err != nil {
		return nil, wrapRetryReceipt(err, retryStageDuringWait, receiptFor(got.ID))
	}
	return res, nil
}

// readRunCommand reads the command line of `terminal run` from the positional
// argument, stdin ("-"), or --file before any connection is opened.
func readRunCommand(inv *cli.Invocation) (string, error) {
	hasArg := inv.HasArg("command")
	if inv.Changed("file") {
		if hasArg {
			return "", cli.Usagef("give the command as an argument or with --file, not both")
		}
		data, err := readFileOrStdin(inv.Stdin, stringFlag(inv, "file"), "--file")
		if err != nil {
			return "", err
		}
		text := strings.TrimRight(string(data), "\r\n")
		if strings.TrimSpace(text) == "" {
			return "", cli.Usagef("--file is empty")
		}
		return text, nil
	}
	if !hasArg {
		return "", cli.Usagef("missing <command>").
			WithHint(`give it as an argument, as "-" to read stdin, or with --file`)
	}
	return readCommandLine(inv, inv.Arg("command"), "the command", !argAfterDash(inv, "command"))
}

// readCommandLine reads a command from text, or from stdin when allowStdin is
// true and text is "-". It drops trailing line endings and refuses an empty
// command. name identifies the source in the refusal, such as "--command".
func readCommandLine(inv *cli.Invocation, text, name string, allowStdin bool) (string, error) {
	if allowStdin && text == "-" {
		data, err := readStdinGuarded(inv.Stdin)
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return "", cli.Usagef("%s is empty", name)
	}
	return text, nil
}

// initialCommandOf reads --command for the new shell when the caller supplied it.
func initialCommandOf(inv *cli.Invocation) (string, error) {
	if !inv.Changed("command") {
		return "", nil
	}
	return readCommandLine(inv, stringFlag(inv, "command"), "--command", true)
}

// argAfterDash reports whether the positional argument argName appeared after
// "--" on the command line.
func argAfterDash(inv *cli.Invocation, argName string) bool {
	if inv == nil || inv.Command == nil {
		return false
	}
	dash := inv.Command.ArgsLenAtDash()
	if dash < 0 {
		return false
	}
	for i, spec := range cli.ArgsOf(inv.Command) {
		if spec.Name == argName {
			return i >= dash
		}
	}
	return false
}

// terminalKeyNames reads the --key values and returns their canonical names.
// An unknown name fails here, before the command sends anything, with the
// vocabulary that the Worker accepts.
func terminalKeyNames(inv *cli.Invocation) ([]string, error) {
	keys, err := terminput.ParseKeys(inv.StringArray("key"))
	if err != nil {
		return nil, cli.Usagef("%v", err).WithHint("key names: %s", terminput.Vocabulary())
	}
	if len(keys) == 0 {
		return nil, nil
	}
	canonical := make([]string, len(keys))
	for i, k := range keys {
		canonical[i] = k.String()
	}
	return canonical, nil
}

// optionalInputBytes reads bytes that reach a PTY as they are: the argument
// argName, "-" for stdin (unless preceded by "--"), or --file. It keeps
// whitespace, reports false when the command line gives no text at all, and
// refuses an empty text.
func optionalInputBytes(inv *cli.Invocation, argName string) ([]byte, bool, error) {
	hasArg := inv.HasArg(argName)
	var data []byte
	switch {
	case inv.Changed("file") && hasArg:
		return nil, false, cli.Usagef("give the text as an argument or with --file, not both")
	case inv.Changed("file"):
		var err error
		data, err = readFileOrStdin(inv.Stdin, stringFlag(inv, "file"), "--file")
		if err != nil {
			return nil, false, err
		}
	case !hasArg:
		return nil, false, nil
	case inv.Arg(argName) == "-" && !argAfterDash(inv, argName):
		var err error
		data, err = readStdinGuarded(inv.Stdin)
		if err != nil {
			return nil, false, err
		}
	default:
		data = []byte(inv.Arg(argName))
	}
	if len(data) == 0 {
		return nil, false, cli.Usagef("the text is empty")
	}
	return data, true, nil
}

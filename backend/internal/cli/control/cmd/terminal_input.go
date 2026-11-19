package cmd

import (
	"io"
	"os"
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

// runTerminalSend writes the text and then the keys to a terminal's PTY in
// one request. It refuses a command line that sends nothing, so a script
// cannot send an empty write by accident.
func runTerminalSend(inv *cli.Invocation) (any, error) {
	text, hasText, err := optionalInputBytes(inv, "text")
	if err != nil {
		return nil, err
	}
	keys, err := terminalKeyNames(inv)
	if err != nil {
		return nil, err
	}
	paste := boolFlag(inv, "paste")
	switch {
	case !hasText && len(keys) == 0:
		return nil, cli.Usagef("missing <text>").
			WithHint(`give the text as an argument, as "-" to read stdin, or with --file, or press a key with --key`)
	case paste && !hasText:
		return nil, cli.Usagef("--paste needs a text").
			WithHint(`give the text as an argument, as "-" to read stdin, or with --file`)
	}
	id, err := sendTerminalInput(inv, &leapmuxv1.SendInputRequest{Data: text, Paste: paste, Keys: keys})
	if err != nil {
		return nil, err
	}
	return terminalSendResult{ID: id, TextBytes: len(text), Paste: paste, Keys: keys}, nil
}

// terminalRunLong is the help of `terminal run`.
const terminalRunLong = `Run a command in a terminal: send the command line and press Enter, in one
request, so no other input can arrive between them. The command is the second
argument, or "-" to read it from stdin. The line endings at its end are
dropped, because the command presses Enter itself.

The command line arrives as a paste, as "terminal send --paste" sends it. A
line editor that enabled bracketed paste therefore inserts it as it is: an
abbreviation, an automatic closing bracket or a vi key binding cannot change
it. A program without bracketed paste reads it as typed text. To type the
command key by key in any case, run
"terminal send <terminal> <command> --key enter".

The command does not wait for the program to finish. Read its output with
"terminal read".`

// terminalRunResult is the result of `terminal run`.
type terminalRunResult struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

// runTerminalRun sends a command line as a paste, then the Enter key.
func runTerminalRun(inv *cli.Invocation) (any, error) {
	command := inv.Arg("command")
	if command == "-" {
		data, err := io.ReadAll(inv.Stdin)
		if err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "read stdin")
		}
		command = string(data)
	}
	command = strings.TrimRight(command, "\r\n")
	if strings.TrimSpace(command) == "" {
		return nil, cli.Usagef("the command is empty")
	}
	id, err := sendTerminalInput(inv, &leapmuxv1.SendInputRequest{Data: []byte(command), Paste: true, Keys: []string{"enter"}})
	if err != nil {
		return nil, err
	}
	return terminalRunResult{ID: id, Command: command}, nil
}

// sendTerminalInput sends req to the terminal that the command line selects,
// and returns the id of the terminal.
func sendTerminalInput(inv *cli.Invocation, req *leapmuxv1.SendInputRequest) (string, error) {
	s, err := openSession(inv)
	if err != nil {
		return "", err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		return "", err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return "", err
	}
	req.TerminalId = got.ID
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil); err != nil {
		return "", err
	}
	return got.ID, nil
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
// argName, "-" for stdin, or --file. It keeps whitespace, reports false when
// the command line gives no text at all, and refuses an empty text.
func optionalInputBytes(inv *cli.Invocation, argName string) ([]byte, bool, error) {
	hasArg := inv.HasArg(argName)
	var data []byte
	switch {
	case inv.Changed("file") && hasArg:
		return nil, false, cli.Usagef("give the text as an argument or with --file, not both")
	case inv.Changed("file"):
		path := stringFlag(inv, "file")
		var err error
		if path == "-" {
			data, err = io.ReadAll(inv.Stdin)
		} else {
			data, err = os.ReadFile(path)
		}
		if err != nil {
			return nil, false, cli.Wrap(cli.CodeUsage, err, "read --file")
		}
	case !hasArg:
		return nil, false, nil
	case inv.Arg(argName) == "-":
		var err error
		data, err = io.ReadAll(inv.Stdin)
		if err != nil {
			return nil, false, cli.Wrap(cli.CodeInternal, err, "read stdin")
		}
	default:
		data = []byte(inv.Arg(argName))
	}
	if len(data) == 0 {
		return nil, false, cli.Usagef("the text is empty")
	}
	return data, true, nil
}

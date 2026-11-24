package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/terminput"
)

// requireOneInput asserts that the Worker received exactly the request want.
func requireOneInput(t *testing.T, w *terminalWorker, want *leapmuxv1.SendInputRequest, why ...string) {
	t.Helper()
	inputs := w.recordedInputs()
	require.Len(t, inputs, 1)
	require.True(t, proto.Equal(want, inputs[0]), "%s\nwant %s\ngot  %s", strings.Join(why, " "), protojson.Format(want), protojson.Format(inputs[0]))
}

func TestTerminalSendOverIPC_SendsTheTextAndTheKeysInOneRequest(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{}
	startTerminalWorker(t, w, id)

	data := runControl(t, "terminal", "send", id, "make", "--key", "Enter", "--key", "CTRL-c", "--key", ",").Data(t)
	assert.Equal(t, map[string]any{"id": id, "text_bytes": 4.0, "keys": []any{"enter", "ctrl-c", ","}}, data)
	requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("make"), Keys: []string{"enter", "ctrl-c", ","}})
}

func TestTerminalSendOverIPC_SendsKeysWithoutText(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{}
	startTerminalWorker(t, w, id)

	data := runControl(t, "terminal", "send", id, "--key", "down", "--key", "enter").Data(t)
	assert.Equal(t, map[string]any{"id": id, "text_bytes": 0.0, "keys": []any{"down", "enter"}}, data)
	requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Keys: []string{"down", "enter"}})
}

func TestTerminalSendOverIPC_SendsAPaste(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{}
	startTerminalWorker(t, w, id)

	o := runControlIn(t, "def f():\n    return 1\n", "terminal", "send", id, "-", "--paste", "--json")
	data := o.Data(t)
	assert.Equal(t, map[string]any{"id": id, "text_bytes": 22.0, "paste": true}, data)
	requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("def f():\n    return 1\n"), Paste: true},
		"the CLI sends the text as it is, and the Worker encodes the paste")
}

// --file sends the bytes of a file as they are, an escape sequence and bytes
// that are no UTF-8 included, and "--file -" reads them from stdin. Either
// combines with --paste and --key, in one request.
func TestTerminalSendOverIPC_SendsTheBytesOfAFile(t *testing.T) {
	keys := "\x1b[A\x00\xff\r\n"
	path := filepath.Join(t.TempDir(), "keys.bin")
	require.NoError(t, os.WriteFile(path, []byte(keys), 0o600))

	t.Run("a file", func(t *testing.T) {
		id := fullTestID("term-1")
		w := &terminalWorker{}
		startTerminalWorker(t, w, id)

		data := runControl(t, "terminal", "send", id, "--file", path, "--key", "enter").Data(t)
		assert.Equal(t, map[string]any{"id": id, "text_bytes": float64(len(keys)), "keys": []any{"enter"}}, data)
		requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte(keys), Keys: []string{"enter"}})
	})
	t.Run("stdin as a paste", func(t *testing.T) {
		id := fullTestID("term-1")
		w := &terminalWorker{}
		startTerminalWorker(t, w, id)

		data := runControlIn(t, "def f():\n", "terminal", "send", id, "--file", "-", "--paste", "--json").Data(t)
		assert.Equal(t, map[string]any{"id": id, "text_bytes": 9.0, "paste": true}, data)
		requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("def f():\n"), Paste: true})
	})
}

// A command line that sends nothing, or a key that the Worker cannot encode,
// fails as a usage error before any request reaches the Worker.
func TestTerminalSendOverIPC_Refusals(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{}
	startTerminalWorker(t, w, id)

	for name, tc := range map[string]struct {
		args []string
		want string
		hint string
	}{
		"no text and no key":     {nil, "missing <text>", "--key"},
		"a paste with no text":   {[]string{"--paste", "--key", "enter"}, "--paste needs a text", ""},
		"an unknown key":         {[]string{"x", "--key", "ctl-c"}, `unknown key "ctl-c"`, "key names: enter, tab"},
		"a key with no encoding": {[]string{"--key", "up", "--key", "ctrl-enter"}, "ctrl applies only to a letter", "key names:"},
		"an empty key":           {[]string{"--key", ""}, "a key name is empty", "key names:"},
		"an empty key and text":  {[]string{"x", "--key", ""}, "a key name is empty", "key names:"},
		"an empty text":          {[]string{""}, "the text is empty", ""},
		"a text and a file":      {[]string{"x", "--file", "keys.bin"}, "not both", ""},
		"a missing file":         {[]string{"--file", filepath.Join(t.TempDir(), "missing.bin")}, "read --file", ""},
		"an empty stdin":         {[]string{"-"}, "the text is empty", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := runControl(t, append([]string{"terminal", "send", id}, tc.args...)...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.want)
			if tc.hint != "" {
				require.NotEmpty(t, f.Hints)
				assert.Contains(t, f.Hints[0], tc.hint)
			}
		})
	}
	assert.Empty(t, w.recordedInputs(), "no refused command line reached the Worker")
}

// A refusal of the Worker reaches the caller with the Worker's words.
func TestTerminalSendOverIPC_TheWorkerRefuses(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{refuseInput: `unknown key "hyper-x"`}
	startTerminalWorker(t, w, id)

	f := runControl(t, "terminal", "send", id, "x", "--key", "enter").requireFailure(t, cli.CodeInvalidArgument)
	assert.Contains(t, f.Message, `unknown key "hyper-x"`)
}

func TestTerminalRunOverIPC_SendsThePasteAndEnterInOneRequest(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{}
	startTerminalWorker(t, w, id)

	data := runControl(t, "terminal", "run", id, "make test").Data(t)
	reqID, _ := data["request_id"].(string)
	require.NotEmpty(t, reqID)
	assert.Equal(t, map[string]any{"id": id, "command": "make test", "request_id": reqID}, data)
	requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("make test"), Paste: true, Keys: []string{"enter"}, RequestId: reqID})

	o := runControlText(t, "terminal", "run", id, "ls")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "id: "+id+"  command: ls  request_id: ")
}

// The line endings at the end of the command go, because run presses Enter
// itself. The line endings inside it stay, and the Worker's paste turns them
// into carriage returns.
func TestTerminalRunOverIPC_DropsTheTrailingLineEndings(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin string
		arg   string
		want  string
	}{
		"a line from echo":         {"go test ./...\n", "-", "go test ./..."},
		"CRLF line endings":        {"dir\r\n\r\n", "-", "dir"},
		"lines inside the command": {"", "cd /tmp\nls\n", "cd /tmp\nls"},
		"leading blanks stay":      {"", "  ls", "  ls"},
	} {
		t.Run(name, func(t *testing.T) {
			id := fullTestID("term-1")
			w := &terminalWorker{}
			startTerminalWorker(t, w, id)

			o := runControlIn(t, tc.stdin, "terminal", "run", id, tc.arg, "--json")
			data := o.Data(t)
			assert.Equal(t, tc.want, data["command"])
			reqID, _ := data["request_id"].(string)
			require.NotEmpty(t, reqID)
			requireOneInput(t, w, &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte(tc.want), Paste: true, Keys: []string{"enter"}, RequestId: reqID})
		})
	}
}

func TestTerminalRunOverIPC_RefusesAnEmptyCommand(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{}
	startTerminalWorker(t, w, id)

	for name, tc := range map[string]struct{ stdin, arg string }{
		"an empty argument":   {"", ""},
		"blanks":              {"", " \t "},
		"line endings only":   {"", "\r\n\n"},
		"an empty stdin":      {"", "-"},
		"a stdin of newlines": {"\n\n", "-"},
	} {
		t.Run(name, func(t *testing.T) {
			f := runControlIn(t, tc.stdin, "terminal", "run", id, tc.arg, "--json").requireFailure(t, cli.CodeUsage)
			assert.Equal(t, "the command is empty", f.Message)
		})
	}
	f := runControl(t, "terminal", "run", id).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "missing <command>")
	assert.Empty(t, w.recordedInputs(), "no refused command line reached the Worker")
}

// Both commands change the state of a terminal, so `leapmux commands` marks
// them, and neither defaults its target to the caller's own terminal.
func TestTerminalInputCommands_Mutate(t *testing.T) {
	group := newTerminalCommand()
	for _, name := range []string{"send", "run"} {
		cmd, _, err := group.Find([]string{name})
		require.NoError(t, err)
		assert.Truef(t, cli.IsMutating(cmd), "terminal %s changes state", name)
		specs := cli.ArgsOf(cmd)
		require.NotEmpty(t, specs)
		assert.Equal(t, "terminal", specs[0].Name)
		assert.True(t, specs[0].Required)
	}
}

// The help of `terminal send` lists every key name that the Worker accepts,
// and no line runs past the width of the other help texts.
func TestTerminalSendLong(t *testing.T) {
	long := terminalSendLong()
	flat := strings.Join(strings.Fields(long), " ")
	assert.Contains(t, flat, strings.Join(strings.Fields(terminput.Vocabulary()), " "))
	for _, name := range []string{"pagedown", "f1 to f12", "shift-tab", "ctrl-c"} {
		assert.Contains(t, flat, name)
	}
	for _, line := range strings.Split(long, "\n") {
		assert.LessOrEqualf(t, utf8.RuneCountInString(line), helpWidth, "a line of the help is too long: %q", line)
	}
}

func TestWrapWords(t *testing.T) {
	assert.Equal(t, "", wrapWords("", 10))
	assert.Equal(t, "", wrapWords("   ", 10))
	assert.Equal(t, "one two", wrapWords("one  two", 10))
	assert.Equal(t, "one two\nthree", wrapWords("one two three", 10), "a word that would pass the width starts a line")
	assert.Equal(t, "one two\nthree", wrapWords("one two\nthree", 7), "a line exactly as wide as the width fits")
	assert.Equal(t, "a\nverylongword\nb", wrapWords("a verylongword b", 5), "a word longer than the width stands alone")
	assert.Equal(t, "日本 語", wrapWords("日本 語", 4), "the width counts runes, not bytes")
}

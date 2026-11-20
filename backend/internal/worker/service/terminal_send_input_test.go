package service

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// runShellCommand sends line to a live terminal the way `terminal run` does:
// the line as a paste, then the Enter key, in one request. It waits until the
// shell printed marker, which the line prints.
func runShellCommand(t *testing.T, d testRPCDispatcher, terminalID, marker string) *testResponseWriter {
	t.Helper()
	line, err := testutil.TestShellOutputCommand(marker)
	require.NoError(t, err)
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte(line),
		Paste:      true,
		Keys:       []string{"Enter"},
	}, w)
	return w
}

// screenContains reports whether the retained screen of a terminal holds
// needle.
func screenContains(t *testing.T, svc *Service, terminalID, needle string) bool {
	t.Helper()
	screen, _, _ := svc.Terminals.ScreenSnapshotSince(terminalID, 0)
	return bytes.Contains(screen, []byte(needle))
}

// The handler writes the text and then the keys that the request lists, so a
// command with the Enter key runs without a newline in its text.
func TestSendInput_PressesTheKeysAfterTheText(t *testing.T) {
	svc, d, _ := setupTestService(t)
	startTestTerminal(t, svc, t.Context(), "term-keys")

	w := runShellCommand(t, d, "term-keys", "keys_after_text")
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-keys", "keys_after_text")
	}, "the Enter key after the text runs the command")
}

// An unknown key name refuses the whole request before the handler writes
// anything: the text of the refused request never reaches the shell.
func TestSendInput_AnUnknownKeyWritesNothing(t *testing.T) {
	svc, d, _ := setupTestService(t)
	startTestTerminal(t, svc, t.Context(), "term-refuse")

	for name, keys := range map[string][]string{
		"an unknown name":         {"enter", "entr"},
		"a modifier with no code": {"ctrl-enter"},
		"an empty name":           {""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
				TerminalId: "term-refuse",
				Data:       []byte("refused_input"),
				Keys:       keys,
			}, w)
			require.Len(t, w.errors, 1)
			assert.EqualValues(t, codes.InvalidArgument, w.errors[0].code)
			assert.Empty(t, w.responses)
		})
	}

	// The shell echoes its input in order, so a refused text that reached it
	// would show before the output of the command that follows.
	w := runShellCommand(t, d, "term-refuse", "after_refusals")
	require.Empty(t, w.errors)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-refuse", "after_refusals")
	}, "the command after the refusals runs")
	assert.False(t, screenContains(t, svc, "term-refuse", "refused_input"), "a refused request wrote its text")
}

// The handler sends the data of a paste request as a paste: the Worker encodes
// it for the bracketed paste that the program in the terminal enabled, and
// presses the keys after it, in one write. The shell of the terminal is a
// script that turns bracketed paste on, reads the raw bytes that reach the
// PTY, and prints them in hex.
func TestSendInput_EncodesAPasteForTheModesOfTheProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the recording shell is a POSIX script that puts the terminal in raw mode with stty")
	}
	svc, d, _ := setupTestService(t)
	const want = "\x1b[200~a\rb\x1b[201~\r"
	shell := filepath.Join(t.TempDir(), "recording-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\n"+
		"stty raw -echo\n"+
		"printf '\\033[?2004h<READY>\\n'\n"+
		fmt.Sprintf("printf '<GOT>%%s</GOT>\\n' \"$(dd bs=1 count=%d 2>/dev/null | od -An -tx1 | tr -d ' \\n')\"\n", len(want))+
		"exec cat >/dev/null\n")
	workingDir := t.TempDir()
	require.NoError(t, svc.Terminals.StartTerminal(t.Context(), terminal.Options{
		ID: "term-paste", Shell: shell, WorkingDir: workingDir, Cols: 80, Rows: 24,
	}, func(terminal.Output) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, "term-paste")
	require.NoError(t, svc.Queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
		ID: "term-paste", WorkingDir: workingDir, Cols: 80, Rows: 24, Screen: []byte{},
	}))
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-paste", "<READY>")
	}, "the shell turns bracketed paste on and waits for input")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: "term-paste",
		Data:       []byte("a\nb"),
		Paste:      true,
		Keys:       []string{"enter"},
	}, w)
	require.Empty(t, w.errors)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-paste", "<GOT>"+hex.EncodeToString([]byte(want))+"</GOT>")
	}, "the paste arrives between the markers, with CR for its line ending, and Enter follows it")
}

// The refusal names the key, so the caller knows which one to correct.
func TestSendInput_TheRefusalNamesTheKey(t *testing.T) {
	svc, d, _ := setupTestService(t)
	startTestTerminal(t, svc, t.Context(), "term-name")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: "term-name",
		Keys:       []string{"up", "pgup"},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, `"pgup"`)
}

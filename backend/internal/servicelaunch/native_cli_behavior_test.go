package servicelaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

const nativeCLITestSecret = "synthetic-private-value"

func TestNativeCLITranscriptMatchesFragmentedPrompts(t *testing.T) {
	prompt := "\x1b[32m" + nativePasswordStart + "fixture-account" + nativePasswordEnd + "\x1b[0m"
	for split := 0; split <= len(prompt); split++ {
		transcript := &nativeCLITranscript{account: "fixture-account"}
		first, err := transcript.consume([]byte(prompt[:split]))
		require.NoError(t, err)
		second, err := transcript.consume([]byte(prompt[split:]))
		require.NoError(t, err)
		assert.NotEqual(t, first, second, "the complete prompt must be observed exactly once")
		_, finishErr := transcript.finish()
		require.NoError(t, finishErr)
	}
}

func TestNativeCLITranscriptRefusesUnsafeInput(t *testing.T) {
	prompt := nativePasswordStart + "fixture-account" + nativePasswordEnd
	for _, test := range []struct{ name, text string }{
		{"wrong account", nativePasswordStart + "another-account" + nativePasswordEnd},
		{"duplicate prompt", prompt + prompt},
		{"output limit", strings.Repeat("x", nativeCLITotalBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			transcript := &nativeCLITranscript{account: "fixture-account"}
			_, err := transcript.consume([]byte(test.text))
			require.Error(t, err)
			_, finishErr := transcript.finish()
			require.NoError(t, finishErr)
		})
	}
	for _, secret := range [][]byte{nil, {}, []byte("line\nline"), []byte("line\rline"), []byte("nul\x00byte"), bytes.Repeat([]byte{'x'}, nativeCLISecretMax+1)} {
		transcript := &nativeCLITranscript{}
		require.Error(t, transcript.protect(secret))
		_, finishErr := transcript.finish()
		require.NoError(t, finishErr)
	}
}

func TestNativeCLITranscriptRedactsAndDetectsEcho(t *testing.T) {
	secret := []byte("synthetic-private-value")
	transcript := &nativeCLITranscript{}
	_, err := transcript.consume([]byte("safe diagnostic before input"))
	require.NoError(t, err)
	require.NoError(t, transcript.protect(secret))
	_, err = transcript.consume(secret[:7])
	require.NoError(t, err)
	_, err = transcript.consume(secret[7:])
	require.ErrorContains(t, err, "echoed protected input")
	transcript.fail(err)
	output, failure := transcript.finish()
	require.Error(t, failure)
	assert.NotContains(t, output, string(secret))
	assert.NotContains(t, failure.Error(), string(secret))
	assert.Equal(t, make([]byte, len(secret)), transcript.secret)
}

func TestHelperNativeCLI(t *testing.T) {
	mode := os.Getenv("LEAPMUX_TEST_NATIVE_CLI")
	if mode == "" {
		return
	}
	exit := func(code int) { os.Exit(code) }
	switch mode {
	case "success":
		if _, err := fmt.Fprintln(os.Stdout, "native CLI completed"); err != nil {
			exit(4)
		}
	case "failure":
		if _, err := fmt.Fprint(os.Stdout, "partial native CLI diagnostic"); err != nil {
			exit(4)
		}
		exit(7)
	case "wait":
		var input [1]byte
		if _, err := os.Stdin.Read(input[:]); err != nil {
			exit(4)
		}
	case "password", "echo":
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			exit(4)
		}
		if _, err := fmt.Fprint(os.Stdout, "\x1b[32mPassword for fixture-account (LeapMux does not store it): \x1b[0m"); err != nil {
			exit(4)
		}
		secret, err := readNativeCLIHelperPassword(os.Stdin)
		if err != nil {
			exit(4)
		}
		if err := term.Restore(int(os.Stdin.Fd()), state); err != nil {
			clear(secret)
			exit(4)
		}
		correct := bytes.Equal(secret, []byte(nativeCLITestSecret))
		if mode == "echo" {
			if _, err := os.Stdout.Write(secret); err != nil {
				clear(secret)
				exit(4)
			}
		}
		clear(secret)
		if !correct {
			exit(8)
		}
	case "wrong account":
		if _, err := fmt.Fprint(os.Stdout, nativePasswordStart+"another-account"+nativePasswordEnd); err != nil {
			exit(4)
		}
	case "duplicate":
		prompt := nativePasswordStart + "fixture-account" + nativePasswordEnd
		if _, err := fmt.Fprint(os.Stdout, prompt+prompt); err != nil {
			exit(4)
		}
	case "overflow":
		if _, err := fmt.Fprint(os.Stdout, strings.Repeat("x", nativeCLITotalBytes+1)); err != nil {
			exit(4)
		}
	default:
		exit(9)
	}
	exit(0)
}

func nativeCLIHelperCommand(t *testing.T, mode string) nativeCLICommand {
	t.Helper()
	return nativeCLICommand{
		program: os.Args[0], args: []string{"-test.run=^TestHelperNativeCLI$"}, directory: t.TempDir(),
		environment: append(os.Environ(), "LEAPMUX_TEST_NATIVE_CLI="+mode),
	}
}

func TestNativeCLICommandRunsAndRetainsFailureDiagnostics(t *testing.T) {
	result := runNativeCLICommand(nativeCLIProcessDeadline(t), nativeCLIHelperCommand(t, "success"))
	require.NoError(t, result.err)
	assert.Contains(t, result.output, "native CLI completed")
	result = runNativeCLICommand(nativeCLIProcessDeadline(t), nativeCLIHelperCommand(t, "failure"))
	require.Error(t, result.err)
	assert.Contains(t, result.output, "partial native CLI diagnostic")
	command := nativeCLIHelperCommand(t, "success")
	command.program = filepath.Join(t.TempDir(), "absent-program")
	result = runNativeCLICommand(nativeCLIProcessDeadline(t), command)
	require.Error(t, result.err)
}

func TestNativeCLICommandUsesProtectedInputAndClearsItsBuffer(t *testing.T) {
	for _, mode := range []string{"password", "echo"} {
		t.Run(mode, func(t *testing.T) {
			secret := []byte("synthetic-private-value")
			command := nativeCLIHelperCommand(t, mode)
			command.account = "fixture-account"
			command.password = func(context.Context) ([]byte, error) { return secret, nil }
			result := runNativeCLICommand(nativeCLIProcessDeadline(t), command)
			if mode == "echo" {
				require.ErrorContains(t, result.err, "echoed protected input")
			} else {
				require.NoError(t, result.err)
			}
			assert.NotContains(t, result.output, "synthetic-private-value")
			assert.Equal(t, make([]byte, len(secret)), secret)
		})
	}
}

func TestNativeCLICommandCancellationAndCredentialFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(nativeCLIProcessDeadline(t))
	cancel()
	result := runNativeCLICommand(ctx, nativeCLIHelperCommand(t, "wait"))
	require.ErrorIs(t, result.err, context.Canceled)
	ctx, cancel = context.WithCancel(nativeCLIProcessDeadline(t))
	defer cancel()
	command := nativeCLIHelperCommand(t, "password")
	command.account = "fixture-account"
	partial := []byte("partial-private-credential")
	command.password = func(context.Context) ([]byte, error) { cancel(); return partial, errors.New("private fixture detail") }
	result = runNativeCLICommand(ctx, command)
	require.ErrorIs(t, result.err, context.Canceled)
	assert.NotContains(t, result.err.Error(), "private fixture detail")
	assert.Equal(t, make([]byte, len(partial)), partial)
}

func TestNativeCLICommandRejectsUnexpectedOutput(t *testing.T) {
	for _, test := range []struct{ mode, failure string }{
		{"wrong account", "unexpected account"},
		{"duplicate", "more than once"},
		{"overflow", "output limit"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			command := nativeCLIHelperCommand(t, test.mode)
			command.account = "fixture-account"
			command.password = func(context.Context) ([]byte, error) { return []byte("synthetic-private-value"), nil }
			result := runNativeCLICommand(nativeCLIProcessDeadline(t), command)
			require.ErrorContains(t, result.err, test.failure)
			assert.LessOrEqual(t, len(result.output), nativeCLITailBytes)
		})
	}
}

func readNativeCLIHelperPassword(input io.Reader) ([]byte, error) {
	// The helper establishes raw mode before its ready prompt. Keep that mode
	// while reading: changing to canonical mode afterward can leave prequeued
	// input waiting for another line-ending event on macOS.
	ending := nativeCLIEnter()
	frame := make([]byte, len(nativeCLITestSecret)+len(ending))
	if _, err := io.ReadFull(input, frame); err != nil {
		clear(frame)
		return nil, err
	}
	if !bytes.Equal(frame[len(nativeCLITestSecret):], ending) {
		clear(frame)
		return nil, errors.New("the native CLI helper received an incomplete input frame")
	}
	clear(frame[len(nativeCLITestSecret):])
	return frame[:len(nativeCLITestSecret)], nil
}

func nativeCLIProcessDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNativeCLIHelperValidatesInputFrames(t *testing.T) {
	frame := append([]byte(nativeCLITestSecret), nativeCLIEnter()...)
	defer clear(frame)
	secret, err := readNativeCLIHelperPassword(iotest.OneByteReader(bytes.NewReader(frame)))
	require.NoError(t, err)
	assert.Equal(t, nativeCLITestSecret, string(secret))
	assert.Equal(t, make([]byte, len(nativeCLIEnter())), secret[len(secret):cap(secret)])
	clear(secret)
	for _, data := range [][]byte{nil, frame[:len(frame)-1], append([]byte(nativeCLITestSecret), '!')} {
		value, err := readNativeCLIHelperPassword(bytes.NewReader(data))
		require.Error(t, err)
		assert.Nil(t, value)
		assert.NotContains(t, err.Error(), nativeCLITestSecret)
	}
	_, err = readNativeCLIHelperPassword(iotest.ErrReader(assert.AnError))
	require.ErrorIs(t, err, assert.AnError)
}

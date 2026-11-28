//go:build !windows

package control

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"

	"github.com/leapmux/leapmux/internal/cli"
)

// An interrupt ends the hidden prompt with the cause of the context, and the
// prompt turns the echo back on: the read itself cannot stop, so without the
// restore the shell would go on without an echo after the command exits.
func TestPromptPassword_AnInterruptRestoresTheEcho(t *testing.T) {
	t.Setenv(noPromptEnv, "")
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() {
		// The read that the prompt started goes on after the interrupt, as
		// it does in a process that is about to exit. A newline ends it, so
		// the pty closes; a close under a pending read blocks.
		_, _ = ptmx.Write([]byte("\n"))
		_ = tty.Close()
		_ = ptmx.Close()
	})
	fd := int(tty.Fd())
	before, err := term.GetState(fd)
	require.NoError(t, err)

	errOut := &syncBuffer{}
	cause := errors.New("interrupted by the test")
	ctx, cancel := context.WithCancelCause(context.Background())
	type answer struct {
		password string
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		password, err := PromptPassword(ctx, "Password: ", tty, errOut)
		done <- answer{password, err}
	}()
	// The read turns the echo off. Interrupt only after that, as a person
	// who sees the prompt does.
	require.Eventually(t, func() bool {
		state, err := term.GetState(fd)
		return err == nil && !reflect.DeepEqual(before, state)
	}, 30*time.Second, time.Millisecond, "the prompt never turned the echo off")
	cancel(cause)

	a := <-done
	require.ErrorIs(t, a.err, cause)
	assert.Empty(t, a.password)
	assert.Contains(t, errOut.String(), "Password: ")
	after, err := term.GetState(fd)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the prompt must restore the terminal it found")
}

func TestPromptPassword_ReadsHiddenInputFromInjectedTerminal(t *testing.T) {
	t.Setenv(noPromptEnv, "")
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tty.Close()
		_ = ptmx.Close()
	})
	fd := int(tty.Fd())
	before, err := term.GetState(fd)
	require.NoError(t, err)

	errOut := &syncBuffer{}
	type answer struct {
		password string
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		pw, readErr := PromptPassword(t.Context(), "Enter password: ", tty, errOut)
		done <- answer{pw, readErr}
	}()

	// Wait for ReadPassword to disable echo before typing the secret.
	require.Eventually(t, func() bool {
		state, stateErr := term.GetState(fd)
		return stateErr == nil && !reflect.DeepEqual(before, state)
	}, 30*time.Second, time.Millisecond, "the prompt never turned the echo off")

	_, err = ptmx.Write([]byte("s3cret-pass\n"))
	require.NoError(t, err)

	select {
	case a := <-done:
		require.NoError(t, a.err)
		assert.Equal(t, "s3cret-pass", a.password)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for PromptPassword to return")
	}

	assert.Equal(t, "\x1b]133;P\x07Enter password: \n", errOut.String(),
		"prompt must write OSC 133;P, prompt text, and trailing newline to errOut without echoing the password")
	after, err := term.GetState(fd)
	require.NoError(t, err)
	assert.Equal(t, before, after, "terminal state must be restored after reading")
}

func TestRequirePassword_UsesInjectedTerminalAndRespectsNoPromptEnv(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tty.Close()
		_ = ptmx.Close()
	})

	t.Run("noPromptEnv refuses even when injected input is a terminal", func(t *testing.T) {
		t.Setenv(noPromptEnv, "1")
		errOut := &syncBuffer{}
		got, reqErr := RequirePassword(t.Context(), "", "Password: ", tty, errOut)
		assert.Empty(t, got)
		require.Error(t, reqErr)
		assert.Equal(t, cli.CodeUsage, cli.FromError(reqErr).Code)
		assert.Contains(t, reqErr.Error(), noPromptEnv)
		assert.Empty(t, errOut.String())
	})

	t.Run("empty flag value prompts on injected terminal", func(t *testing.T) {
		t.Setenv(noPromptEnv, "")
		fd := int(tty.Fd())
		before, stateErr := term.GetState(fd)
		require.NoError(t, stateErr)

		errOut := &syncBuffer{}
		type answer struct {
			password string
			err      error
		}
		done := make(chan answer, 1)
		go func() {
			pw, reqErr := RequirePassword(t.Context(), "", "Pass: ", tty, errOut)
			done <- answer{pw, reqErr}
		}()

		require.Eventually(t, func() bool {
			state, err := term.GetState(fd)
			return err == nil && !reflect.DeepEqual(before, state)
		}, 30*time.Second, time.Millisecond)

		_, writeErr := ptmx.Write([]byte("typed-pw\n"))
		require.NoError(t, writeErr)

		select {
		case a := <-done:
			require.NoError(t, a.err)
			assert.Equal(t, "typed-pw", a.password)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for RequirePassword")
		}
		assert.Equal(t, "\x1b]133;P\x07Pass: \n", errOut.String())
	})
}

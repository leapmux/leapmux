//go:build !windows

package control

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// An interrupt ends the hidden prompt with the cause of the context, and the
// prompt turns the echo back on: the read itself cannot stop, so without the
// restore the shell would go on without an echo after the command exits.
func TestPromptPassword_AnInterruptRestoresTheEcho(t *testing.T) {
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
	stdin := os.Stdin
	os.Stdin = tty
	t.Cleanup(func() { os.Stdin = stdin })
	fd := int(tty.Fd())
	before, err := term.GetState(fd)
	require.NoError(t, err)

	cause := errors.New("interrupted by the test")
	ctx, cancel := context.WithCancelCause(context.Background())
	type answer struct {
		password string
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		password, err := PromptPassword(ctx, "Password: ")
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
	after, err := term.GetState(fd)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the prompt must restore the terminal it found")
}

//go:build !windows

package servicelaunch

import (
	"bytes"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"

	pty "github.com/aymanbagabas/go-pty"
)

func closeNativeCLIChild(terminal pty.Pty) error {
	if unixTerminal, ok := terminal.(pty.UnixPty); ok {
		return unixTerminal.Slave().Close()
	}
	return errors.New("the native CLI PTY has no Unix child endpoint")
}

func closeNativeCLIPipes(terminal pty.Pty) error {
	if unixTerminal, ok := terminal.(pty.UnixPty); ok {
		return unixTerminal.Master().Close()
	}
	return terminal.Close()
}

func nativeCLIEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

func nativeCLIEnter() []byte { return []byte{'\n'} }

func TestNativeCLIHelperReadsPrequeuedRawInput(t *testing.T) {
	terminal, err := pty.New()
	require.NoError(t, err)
	unixTerminal, ok := terminal.(pty.UnixPty)
	require.True(t, ok)
	t.Cleanup(func() { _ = unixTerminal.Slave().Close(); _ = unixTerminal.Master().Close() })
	_, err = term.MakeRaw(int(unixTerminal.Slave().Fd()))
	require.NoError(t, err)
	secret := []byte("synthetic-private-value")
	response := append(bytes.Clone(secret), nativeCLIEnter()...)
	written, err := terminal.Write(response)
	require.NoError(t, err)
	require.Equal(t, len(response), written)
	clear(response)
	done := make(chan struct{})
	var input []byte
	var readErr error
	go func() { input, readErr = readNativeCLIHelperPassword(unixTerminal.Slave()); close(done) }()
	select {
	case <-done:
	case <-nativeCLIProcessDeadline(t).Done():
		// Complete a canonical read before closing its descriptor; close can
		// wait for a native read that still owns the terminal endpoint.
		_, writeErr := terminal.Write(nativeCLIEnter())
		require.NoError(t, writeErr)
		<-done
		t.Fatal("protected input queued in raw mode required another line-ending event")
	}
	require.NoError(t, readErr)
	assert.Equal(t, secret, input)
	clear(input)
}

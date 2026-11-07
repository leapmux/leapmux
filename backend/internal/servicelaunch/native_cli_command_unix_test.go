//go:build !windows

package servicelaunch

import (
	"errors"
	"io"
	"os"
	"syscall"

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

//go:build windows

package servicelaunch

import (
	"errors"
	"io"
	"os"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

func closeNativeCLIChild(terminal pty.Pty) error {
	if console, ok := terminal.(pty.ConPty); ok {
		windows.ClosePseudoConsole(windows.Handle(console.Fd()))
		return nil
	}
	return errors.New("the native CLI PTY has no Windows child endpoint")
}

func closeNativeCLIPipes(terminal pty.Pty) error {
	if console, ok := terminal.(pty.ConPty); ok {
		return errors.Join(console.InputPipe().Close(), console.OutputPipe().Close())
	}
	return terminal.Close()
}

func nativeCLIEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA)
}

func nativeCLIEnter() []byte { return []byte{'\r'} }

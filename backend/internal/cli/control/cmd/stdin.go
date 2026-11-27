package cmd

import (
	"io"
	"os"

	"golang.org/x/term"

	"github.com/leapmux/leapmux/internal/cli"
)

// isTerminalReader reports whether r is a character device / interactive TTY.
func isTerminalReader(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// isTerminalWriter reports whether w is a character device / interactive TTY.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// readStdinGuarded drains r before opening any network connection and fails
// fast when stdin is an interactive TTY instead of hanging silently waiting
// for EOF.
func readStdinGuarded(r io.Reader) ([]byte, error) {
	if isTerminalReader(r) {
		return nil, cli.Usagef("refusing to read from an interactive terminal on stdin").
			WithHint("pipe input into the command, pass text directly, or use --file <path>")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, cli.Wrap(cli.CodeInternal, err, "read stdin")
	}
	return data, nil
}

// readFileOrStdin reads bytes from stdin when path is "-", or from the file at
// path otherwise. File read failures are wrapped as usage errors that include
// flagName (for example "--file").
func readFileOrStdin(stdin io.Reader, path, flagName string) ([]byte, error) {
	if path == "-" {
		return readStdinGuarded(stdin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "read "+flagName)
	}
	return data, nil
}

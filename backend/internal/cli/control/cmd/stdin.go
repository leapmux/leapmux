package cmd

import (
	"io"
	"os"

	"github.com/leapmux/leapmux/internal/cli"
)

// isTerminalIO reports whether v is an *os.File attached to an interactive
// terminal.
func isTerminalIO(v any) bool {
	f, ok := v.(*os.File)
	return ok && cli.IsTerminalFile(f)
}

// readStdinGuarded drains r before opening any network connection and fails
// fast when stdin is an interactive TTY instead of hanging silently waiting
// for EOF.
func readStdinGuarded(r io.Reader) ([]byte, error) {
	if isTerminalIO(r) {
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

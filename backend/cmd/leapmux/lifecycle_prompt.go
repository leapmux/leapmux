package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// The interactive configuration flow: the startup choice, the preview
// explanation, and the y/N consent. y and yes accept, case
// insensitively; empty input, end of input, and a read error decline.
// A noninteractive command that needs missing information fails with
// the exact options it needs instead.

// say writes one prompt line; a failed write surfaces at the read
// that follows, where the answer cannot arrive either.
func say(out io.Writer, text string) {
	_, _ = fmt.Fprint(out, text)
}

// promptStartupChoice shows the startup choice. Login startup is the
// default for a new configuration.
func promptStartupChoice(out io.Writer, in io.Reader) (lifecycle.StartupKind, error) {
	say(out, "\nWhen should LeapMux start automatically?\n\n")
	say(out, "  1. When you log in\n")
	say(out, "     Runs as your account.\n")
	say(out, "     Stops after your last applicable login session ends.\n\n")
	say(out, "  2. When the computer starts\n")
	say(out, "     Runs as your account even while you are logged out.\n\n")
	say(out, "Choose [1]: ")

	line, err := readTrimmedLine(in)
	if err != nil {
		// End of input declines: a noninteractive caller must pass
		// --startup and --yes instead of reading a closed stdin.
		return "", fmt.Errorf("no startup mode chosen; pass --startup=login or --startup=boot with --yes")
	}
	switch line {
	case "", "1":
		return lifecycle.StartupLogin, nil
	case "2":
		return lifecycle.StartupBoot, nil
	default:
		return "", fmt.Errorf("%q is not a startup choice; answer 1 or 2", line)
	}
}

// promptApply asks for the y/N consent after the preview.
func promptApply(out io.Writer, in io.Reader) (bool, error) {
	say(out, "\nApply this configuration? [y/N]: ")
	line, err := readTrimmedLine(in)
	if err != nil {
		// End of input declines the configuration.
		return false, nil
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	default:
		// Empty input declines too.
		return false, nil
	}
}

// readTrimmedLine reads one trimmed line.
func readTrimmedLine(in io.Reader) (string, error) {
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

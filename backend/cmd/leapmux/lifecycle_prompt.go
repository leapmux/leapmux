package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/leapmux/leapmux/lifecycle"
)

// The interactive configuration flow: the startup choice, the preview
// explanation, and the y/N consent. y and yes accept, case
// insensitively; empty input, end of input, and a read error decline.
// A noninteractive command that needs missing information fails with
// the exact options it needs instead.

// writeSetupText requires the complete explanation before setup can proceed.
func writeSetupText(out io.Writer, text string) error {
	written, err := io.WriteString(out, text)
	if err == nil && written < len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("display the startup configuration: %w", err)
	}
	return nil
}

// promptStartupChoice shows the startup choice. Login startup is the
// default for a new configuration.
func promptStartupChoice(out io.Writer, in io.Reader) (lifecycle.StartupKind, error) {
	if err := writeSetupText(out, "\nWhen should LeapMux start automatically?\n\n"+
		"  1. When you log in\n"+
		"     Runs as your account.\n"+
		"     Stops after your last applicable login session ends.\n\n"+
		"  2. When the computer starts\n"+
		"     Runs as your account even while you are logged out.\n\n"+
		"Choose [1]: "); err != nil {
		return "", err
	}

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
	if err := writeSetupText(out, "\nApply this configuration? [y/N]: "); err != nil {
		return false, err
	}
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
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// requireInteractiveInput refuses missing setup options before reading a pipe.
func requireInteractiveInput(missingOptions ...string) error {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	return fmt.Errorf("noninteractive setup requires %s", strings.Join(missingOptions, " and "))
}

package control

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// PromptPassword reads a password from the terminal without echoing.
// It emits OSC 133;P to signal password input to terminals that support
// it (e.g. Ghostty), enabling credential detection features.
//
// A non-terminal stdin is REFUSED rather than read: a password piped in is
// a password in a shell history, a script, or a process listing.
func PromptPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !isatty.IsTerminal(uintptr(fd)) && !isatty.IsCygwinTerminal(uintptr(fd)) {
		return "", fmt.Errorf("--password is required (stdin is not a terminal)")
	}

	fmt.Fprint(os.Stderr, "\x1b]133;P\x07")
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

// RequirePassword returns the password from the flag if set, otherwise
// prompts interactively.
//
// Shared by the offline `recover` verbs and the online `control admin user
// create`: an RPC surface is no reason to make an operator type a password
// on the command line, where the shell history and the process table both
// record it. The prompt happens locally, before the request is built.
func RequirePassword(pw string, prompt string) (string, error) {
	if pw != "" {
		return pw, nil
	}
	return PromptPassword(prompt)
}

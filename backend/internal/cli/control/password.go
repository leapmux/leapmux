package control

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// isInteractive reports whether a person drives this process, by testing
// whether stdin is a terminal.
//
// STDIN is the signal even for a prompt that writes to stderr and reads
// nothing back, such as the browser step-up. It is the stream a person's
// terminal owns whatever the command does with its output, so a redirect of
// stdout alone does not change the answer.
func isInteractive() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// PromptPassword reads a password from the terminal without echoing.
// It emits OSC 133;P to signal password input to terminals that support
// it (e.g. Ghostty), enabling credential detection features.
//
// PromptPassword REFUSES a process that may not prompt, rather than reading
// from it: a password piped in is a password in a shell history, a script, or a
// process listing. Both refusals go through promptsAllowed, so one variable
// answers for every prompt this CLI can open -- a caller that states "I am a
// script" must not then be asked to type a secret.
func PromptPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !promptsAllowed() {
		return "", fmt.Errorf("--password is required (%s)", noPromptReason())
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
// record it. The prompt happens locally, before this code builds the request.
func RequirePassword(pw string, prompt string) (string, error) {
	if pw != "" {
		return pw, nil
	}
	return PromptPassword(prompt)
}

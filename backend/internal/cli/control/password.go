package control

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"golang.org/x/term"

	"github.com/leapmux/leapmux/internal/cli"
)

// isInteractive reports whether a person drives this process, by testing
// whether stdin is a terminal.
//
// STDIN is the signal even for a prompt that writes to stderr and reads
// nothing back, such as the browser step-up. It is the stream a person's
// terminal owns whatever the command does with its output, so a redirect of
// stdout alone does not change the answer.
func isInteractive() bool {
	return cli.IsTerminalFile(os.Stdin)
}

// PromptPassword reads a password from in (defaulting to os.Stdin) without
// echoing, writing the prompt and terminal escape to errOut (defaulting to
// os.Stderr). It emits OSC 133;P to signal password input to terminals that
// support it (e.g. Ghostty), enabling credential detection features.
//
// PromptPassword REFUSES a process that may not prompt, rather than reading
// from it: a password piped in is a password in a shell history, a script, or a
// process listing. Both refusals go through promptsAllowedFor, so one variable
// answers for every prompt this CLI can open -- a caller that states "I am a
// script" must not then be asked to type a secret.
//
// An end of ctx, such as an interrupt, ends the prompt with the cause of ctx.
// The terminal reads with its echo off until the read returns, and the read
// cannot stop, so the prompt turns the echo back on itself first.
func PromptPassword(ctx context.Context, prompt string, in *os.File, errOut io.Writer) (string, error) {
	if ctx.Err() != nil {
		return "", context.Cause(ctx)
	}
	if in == nil {
		in = os.Stdin
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	if !promptsAllowedFor(in) {
		return "", cli.Usagef("--password is required (%s)", noPromptReason())
	}
	fd := int(in.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("read the terminal state: %w", err)
	}

	_, _ = fmt.Fprint(errOut, "\x1b]133;P\x07")
	_, _ = fmt.Fprint(errOut, prompt)
	type answer struct {
		password []byte
		err      error
	}
	var canceled atomic.Bool
	done := make(chan answer, 1)
	go func() {
		if canceled.Load() {
			return
		}
		pw, err := term.ReadPassword(fd)
		if canceled.Load() {
			_ = term.Restore(fd, state)
		}
		done <- answer{pw, err}
	}()
	select {
	case a := <-done:
		_, _ = fmt.Fprintln(errOut) // newline after hidden input
		if a.err != nil {
			return "", fmt.Errorf("read password: %w", a.err)
		}
		return string(a.password), nil
	case <-ctx.Done():
		canceled.Store(true)
		_ = term.Restore(fd, state)
		_, _ = fmt.Fprintln(errOut)
		return "", context.Cause(ctx)
	}
}

// RequirePassword returns the password from the flag if set, otherwise
// prompts interactively on in (defaulting to os.Stdin) and errOut (defaulting
// to os.Stderr).
//
// Shared by the offline `recover` verbs and the online `admin user
// create`: an RPC surface is no reason to make an operator type a password
// on the command line, where the shell history and the process table both
// record it. The prompt happens locally, before this code builds the request.
func RequirePassword(ctx context.Context, pw, prompt string, in *os.File, errOut io.Writer) (string, error) {
	if pw != "" {
		return pw, nil
	}
	return PromptPassword(ctx, prompt, in, errOut)
}

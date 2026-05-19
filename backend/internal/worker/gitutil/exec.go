package gitutil

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// runGit invokes a git subprocess via NewGitCmd in `dir` and captures
// stdout and stderr unconditionally. Callers that only consume one
// stream pick it through {@link Output} / {@link OutputStderr} below;
// callers that need both take the buffers off this function directly.
func runGit(ctx context.Context, dir string, args ...string) (stdout, stderr bytes.Buffer, err error) {
	cmd := NewGitCmd(ctx, append([]string{"-C", dir}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout, stderr, err
}

// wrapWithStderr threads captured stderr into the returned error so
// debug logs and bubbled error messages carry git's own diagnostic
// instead of a bare `exit status N`. Returns the original err when
// stderr is empty so the wrap is a no-op for ctx-cancellation /
// signal-kill paths where stderr never opened.
func wrapWithStderr(err error, stderr bytes.Buffer) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

// Output runs a git command in `dir` and returns its stdout as a string.
// Use Bytes when the consumer wants byte-oriented parsing (NUL split,
// raw blob content); use this when the consumer treats the output as
// text (TrimSpace + Split, single ref name, etc.).
//
// On failure the returned error carries the trimmed stderr — callers
// that log the error get git's actual diagnostic ("fatal: ambiguous
// argument 'HEAD'") instead of an opaque `exit status 128`.
func Output(ctx context.Context, dir string, args ...string) (string, error) {
	stdout, stderr, err := runGit(ctx, dir, args...)
	if err != nil {
		return "", wrapWithStderr(err, stderr)
	}
	return stdout.String(), nil
}

// Bytes runs a git command in `dir` and returns its stdout as a byte
// slice. Prefer this for `-z`-delimited output (NUL bytes break utf-8
// scans) and for raw blob reads that downstream code may write to disk.
//
// On failure the returned error carries the trimmed stderr — see Output.
func Bytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	stdout, stderr, err := runGit(ctx, dir, args...)
	if err != nil {
		return nil, wrapWithStderr(err, stderr)
	}
	return stdout.Bytes(), nil
}

// OutputStderr runs a git command in `dir` and returns its stderr as a
// string. Used by mutating commands (checkout / commit / push) whose
// failure message lives on stderr — callers surface it as the
// user-visible error text.
func OutputStderr(ctx context.Context, dir string, args ...string) (string, error) {
	_, stderr, err := runGit(ctx, dir, args...)
	return stderr.String(), err
}

// Run runs a git command in `dir` and returns an error if it fails.
// Used by mutating commands that don't need the output (worktree
// add/remove, branch -D after checkout) — the call sites just need to
// know whether the operation succeeded.
//
// On failure the returned error carries the trimmed stderr — see Output.
func Run(ctx context.Context, dir string, args ...string) error {
	_, stderr, err := runGit(ctx, dir, args...)
	if err != nil {
		return wrapWithStderr(err, stderr)
	}
	return nil
}

package gitutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/leapmux/leapmux/util/procutil"
)

// runGit invokes a git subprocess via NewGitCmd in `dir` and captures
// stdout and stderr unconditionally. Callers that only consume one
// stream pick it through {@link Output} / {@link OutputStderr} below;
// callers that need both take the buffers off this function directly.
func runGit(ctx context.Context, dir string, args ...string) (stdout, stderr bytes.Buffer, err error) {
	return captureGitCommand(ctx, append([]string{"-C", dir}, args...)...)
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

// CommandError keeps command failure separate from ownership cleanup failure.
// A native absence status is meaningful only when cleanup also succeeded.
type CommandError struct {
	RunError     error
	CleanupError error
}

func (e *CommandError) Error() string {
	if e == nil {
		return "git command failed"
	}
	joined := errors.Join(e.RunError, e.CleanupError)
	if joined == nil {
		return "git command failed"
	}
	return joined.Error()
}
func (e *CommandError) Unwrap() []error {
	if e == nil {
		return nil
	}
	var result []error
	if e.RunError != nil {
		result = append(result, e.RunError)
	}
	if e.CleanupError != nil {
		result = append(result, e.CleanupError)
	}
	return result
}

func runGitCommand(cmd *exec.Cmd) (runErr, cleanupErr error) {
	owner := procutil.PrepareProcess(cmd)
	runErr = owner.Start()
	if runErr == nil {
		runErr = owner.Wait()
	}
	cleanupErr = owner.Close()
	return runErr, cleanupErr
}

func finishGitCommand(ctx context.Context, runErr, cleanupErr error, stderr []byte) error {
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		exitErr.Stderr = bytes.Clone(stderr)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		runErr = errors.Join(ctxErr, runErr)
	}
	if runErr != nil || cleanupErr != nil {
		return &CommandError{RunError: runErr, CleanupError: cleanupErr}
	}
	return nil
}

func captureGitCommand(ctx context.Context, args ...string) (stdout, stderr bytes.Buffer, err error) {
	cmd := NewGitCmd(ctx, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr, cleanupErr := runGitCommand(cmd)
	return stdout, stderr, finishGitCommand(ctx, runErr, cleanupErr, stderr.Bytes())
}

// Capture returns complete captured streams, including partial bytes on failure.
func Capture(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error) {
	out, diagnostic, err := runGit(ctx, dir, args...)
	if err != nil {
		err = wrapWithStderr(err, diagnostic)
	}
	return out.Bytes(), diagnostic.Bytes(), err
}

// DiffBytes disables repository executable helpers and preserves raw output.
func DiffBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return Bytes(ctx, dir, append([]string{"diff", "--no-ext-diff", "--no-textconv"}, args...)...)
}

// DiffToWriter streams raw git diff output into stdout with repository
// diff.external and textconv helpers disabled.
func DiffToWriter(ctx context.Context, dir string, stdout io.Writer, args ...string) (stderr []byte, err error) {
	full := make([]string, 0, len(args)+5)
	full = append(full, "-C", dir, "diff", "--no-ext-diff", "--no-textconv")
	full = append(full, args...)
	cmd := NewGitCmd(ctx, full...)
	var stderrBuf bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderrBuf
	runErr, cleanupErr := runGitCommand(cmd)
	return stderrBuf.Bytes(), finishGitCommand(ctx, runErr, cleanupErr, stderrBuf.Bytes())
}

// RunCombinedWithStdin runs git in dir with stdin and returns combined stdout
// and stderr along with any CommandError.
func RunCombinedWithStdin(ctx context.Context, dir string, stdin io.Reader, args ...string) (combined []byte, err error) {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-C", dir)
	full = append(full, args...)
	cmd := NewGitCmd(ctx, full...)
	cmd.Stdin = stdin
	var combinedBuf bytes.Buffer
	cmd.Stdout = &combinedBuf
	cmd.Stderr = &combinedBuf
	runErr, cleanupErr := runGitCommand(cmd)
	return combinedBuf.Bytes(), finishGitCommand(ctx, runErr, cleanupErr, combinedBuf.Bytes())
}

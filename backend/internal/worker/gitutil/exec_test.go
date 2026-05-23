package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExec_Output_HappyPath(t *testing.T) {
	dir := initRepo(t)
	out, err := Output(context.Background(), dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(out))
}

func TestExec_Output_FailurePropagatesExitError(t *testing.T) {
	dir := t.TempDir() // Not a git repo.
	_, err := Output(context.Background(), dir, "rev-parse", "HEAD")
	require.Error(t, err)
	// Surface as *exec.ExitError so callers can distinguish exit-code
	// signals (HasRefs relies on this).
	var exitErr *exec.ExitError
	assert.ErrorAs(t, err, &exitErr)
}

func TestExec_Bytes_PreservesNULDelimitedOutput(t *testing.T) {
	// `git config -z --get-regexp` separates entries with NUL bytes.
	// Output (string) would still work for the parse, but Bytes is the
	// idiomatic accessor for `-z` callers. Pin that it returns raw bytes
	// without trimming.
	dir := initRepo(t)
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.email", "first@test.com").Run())

	out, err := Bytes(context.Background(), dir, "config", "-z", "--get-regexp", "^user\\.")
	require.NoError(t, err)
	assert.Contains(t, string(out), "user.email\nfirst@test.com\x00")
}

func TestExec_OutputStderr_CapturesMutatingFailureMessage(t *testing.T) {
	// Mutating commands route their error message to stderr; OutputStderr
	// is what gives the dialog its banner copy. Verify on a deliberately
	// failing checkout.
	dir := initRepo(t)
	stderr, err := OutputStderr(context.Background(), dir, "checkout", "nope-no-such-branch")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(stderr), "did not match any file")
}

func TestExec_Run_ReturnsNilOnSuccess(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, Run(context.Background(), dir, "rev-parse", "HEAD"))
}

func TestExec_Run_ReturnsErrorOnFailure(t *testing.T) {
	dir := t.TempDir() // Not a git repo.
	assert.Error(t, Run(context.Background(), dir, "rev-parse", "HEAD"))
}

func TestExec_Output_RespectsContextCancellation(t *testing.T) {
	dir := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so the subprocess never starts.
	_, err := Output(ctx, dir, "rev-parse", "HEAD")
	require.Error(t, err)
}

// TestNewGitCmd_PinsCLocale locks in the LC_ALL=C invariant. Without it,
// a worker running under a localized locale (ja_JP.UTF-8, fr_FR.UTF-8)
// emits translated messages — `git diff --shortstat HEAD` would print
// "3 fichiers modifiés, 12 insertions(+)" instead of "3 files changed,
// 12 insertions(+)", silently zeroing the regex parsers in
// parseDiffShortstat. The env vars also stack: LANG / LC_MESSAGES from
// the operator's environment must NOT override the LC_ALL pin (LC_ALL
// takes precedence per POSIX), so we just verify the pin is present.
func TestNewGitCmd_PinsCLocale(t *testing.T) {
	cmd := NewGitCmd(context.Background(), "status")
	hasLCAll := false
	for _, e := range cmd.Env {
		if e == "LC_ALL=C" {
			hasLCAll = true
			break
		}
	}
	assert.True(t, hasLCAll, "NewGitCmd must pin LC_ALL=C so locale-localized git output can't break the worker's regex parsers")
}

// TestNewGitCmd_ShortstatEmitsEnglishWording proves the end-to-end
// consequence of the LC_ALL pin: `git diff --shortstat HEAD` emits
// English output ("insertion", "deletion") that parseDiffShortstat's
// regexes match. Without the pin, a worker running under a localized
// locale would emit translated strings and the parser would silently
// return (0, 0). We can't flip the test process's own locale (the
// child inherits via NewGitCmd before the parent can override), so
// the test verifies the wording is present on a staged change.
func TestNewGitCmd_ShortstatEmitsEnglishWording(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\n"), 0o644))
	// Stage the change so `diff --shortstat HEAD` includes it.
	require.NoError(t, exec.Command("git", "-C", dir, "add", "f.txt").Run())

	out, err := Output(context.Background(), dir, "diff", "--shortstat", "HEAD")
	require.NoError(t, err)
	// Match the regex parseDiffShortstat uses.
	assert.Contains(t, out, "insertion",
		"NewGitCmd output must hit the English 'insertion' wording the parser matches")
}

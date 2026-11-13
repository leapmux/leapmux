package gitutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureKeepsNativeDiagnosticsAndCancellation(t *testing.T) {
	_, diagnostic, err := Capture(t.Context(), initRepo(t), "config", "--get-regexp", "[")
	require.Error(t, err)
	var native *exec.ExitError
	require.ErrorAs(t, err, &native)
	assert.Equal(t, diagnostic, native.Stderr)
	require.NotEmpty(t, native.Stderr)
	var commandError *CommandError
	require.ErrorAs(t, err, &commandError)
	assert.Nil(t, commandError.CleanupError)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = Capture(ctx, initRepo(t), "rev-parse", "HEAD")
	require.ErrorIs(t, err, context.Canceled)
}

func TestCommandErrorRetainsBothCauses(t *testing.T) {
	runFailure, cleanupFailure := errors.New("run failed"), errors.New("cleanup failed")
	err := &CommandError{RunError: runFailure, CleanupError: cleanupFailure}
	require.ErrorIs(t, err, runFailure)
	require.ErrorIs(t, err, cleanupFailure)
	assert.Contains(t, err.Error(), "cleanup failed")
}

func TestCapturePreservesRawConfigBytes(t *testing.T) {
	repo := initRepo(t)
	require.NoError(t, Run(t.Context(), repo, "config", "leapmux.raw", "one\ntwo \t"))
	output, diagnostic, err := Capture(t.Context(), repo, "config", "-z", "--get-regexp", "^leapmux\\.raw$")
	require.NoError(t, err)
	assert.Equal(t, []byte("leapmux.raw\none\ntwo \t\x00"), output)
	assert.Empty(t, diagnostic)
	output, diagnostic, err = Capture(t.Context(), repo, "config", "-z", "--get-regexp", "^missing$")
	require.Error(t, err)
	assert.Empty(t, output)
	assert.Empty(t, diagnostic)
}

func TestRepositoryNULFraming(t *testing.T) {
	output := []byte("worktree /bare\x00bare\x00\x00worktree /checkout\nwith-space \x00HEAD abc\x00branch refs/heads/feature/x\x00locked reason\nwith-space \x00\x00")
	entries, err := parseWorktrees(output)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.True(t, entries[0].Bare)
	assert.True(t, entries[0].IsMain)
	assert.Equal(t, "/checkout\nwith-space ", entries[1].Path)
	assert.Equal(t, "feature/x", entries[1].Branch)
	assert.Equal(t, "reason\nwith-space ", entries[1].LockReason)
	assert.True(t, entries[1].Locked)
	for _, malformed := range [][]byte{nil, []byte("branch refs/heads/main\x00"), []byte("worktree \x00")} {
		_, err := parseWorktrees(malformed)
		require.Error(t, err)
	}
	_, err = (Repository{GitDir: "relative"}).Output(t.Context(), "for-each-ref")
	require.Error(t, err)
}

func TestDiffBytesPreservesPatchWhitespace(t *testing.T) {
	repo := initRepo(t)
	path := filepath.Join(repo, "tracked.txt")
	require.NoError(t, os.WriteFile(path, []byte("before\n"), 0o600))
	require.NoError(t, Run(t.Context(), repo, "add", "tracked.txt"))
	require.NoError(t, os.WriteFile(path, []byte("after \t\n"), 0o600))
	output, err := DiffBytes(t.Context(), repo, "--", "tracked.txt")
	require.NoError(t, err)
	assert.Contains(t, string(output), "+after \t\n")
	nul, err := DiffBytes(t.Context(), repo, "--numstat", "-z")
	require.NoError(t, err)
	assert.Equal(t, []byte("1\t1\ttracked.txt\x00"), nul)
	_, err = DiffBytes(t.Context(), filepath.Join(repo, "missing"), "--numstat")
	require.Error(t, err)
	var native *exec.ExitError
	assert.ErrorAs(t, err, &native)
}

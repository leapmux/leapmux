//go:build unix

package gitutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffBytesDisablesExternalDiffAndTextconv(t *testing.T) {
	// Inherited GIT_EXTERNAL_DIFF, GIT_CONFIG_PARAMETERS, and GIT_CONFIG_COUNT
	// take precedence over repo diff.external and diff.*.textconv; t.Setenv
	// alone is insufficient because GIT_EXTERNAL_DIFF="" makes Git fail to
	// execute an empty helper path, so unset each key while keeping cleanup.
	for _, key := range []string{"GIT_EXTERNAL_DIFF", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"} {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}

	repo := initRepo(t)
	tracked := filepath.Join(repo, "tracked.txt")
	require.NoError(t, os.WriteFile(tracked, []byte("before\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("tracked.txt diff=leapmux-test\n"), 0o600))
	require.NoError(t, Run(t.Context(), repo, "add", "tracked.txt", ".gitattributes"))
	require.NoError(t, Run(t.Context(), repo, "commit", "--no-gpg-sign", "-m", "Add the baseline for the diff"))
	require.NoError(t, os.WriteFile(tracked, []byte("after \t\n"), 0o600))
	// Compare the staged blobs. The positive controls must have a diff even
	// when Git is configured to trust the working tree's index entry.
	require.NoError(t, Run(t.Context(), repo, "add", "--renormalize", "--", "tracked.txt"))
	helperDir := filepath.Join(t.TempDir(), "helper dir 'q' $x (1)")
	require.NoError(t, os.MkdirAll(helperDir, 0o700))
	marker := filepath.Join(helperDir, "executed")
	program := filepath.Join(helperDir, "diff-helper")
	quotedProgram := shellquote.POSIX.Quote(program)
	t.Setenv("LEAPMUX_TEST_DIFF_MARKER", marker)
	testutil.WriteExecutable(t, program, "#!/bin/sh\nprintf ran > \"$LEAPMUX_TEST_DIFF_MARKER\"\nprintf converted\n")
	require.NoError(t, Run(t.Context(), repo, "config", "diff.external", quotedProgram))
	_, err := Bytes(t.Context(), repo, "diff", "--cached", "--no-textconv")
	require.NoError(t, err)
	_, err = os.Stat(marker)
	require.NoError(t, err, "the positive control must execute the external helper")
	require.NoError(t, os.Remove(marker))
	require.NoError(t, Run(t.Context(), repo, "config", "diff.leapmux-test.textconv", quotedProgram))
	_, err = Bytes(t.Context(), repo, "diff", "--cached", "--no-ext-diff")
	require.NoError(t, err)
	_, err = os.Stat(marker)
	require.NoError(t, err, "the positive control must execute textconv")
	require.NoError(t, os.Remove(marker))

	envMarker := filepath.Join(helperDir, "env-executed")
	envProgram := filepath.Join(helperDir, "env-diff-helper")
	t.Setenv("LEAPMUX_TEST_ENV_DIFF_MARKER", envMarker)
	testutil.WriteExecutable(t, envProgram, "#!/bin/sh\nprintf ran > \"$LEAPMUX_TEST_ENV_DIFF_MARKER\"\nprintf env-converted\n")
	t.Setenv("GIT_EXTERNAL_DIFF", shellquote.POSIX.Quote(envProgram))
	_, err = Bytes(t.Context(), repo, "diff", "--cached", "--no-textconv")
	require.NoError(t, err)
	_, err = os.Stat(envMarker)
	require.NoError(t, err, "the positive control must execute GIT_EXTERNAL_DIFF")
	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Remove(envMarker))

	output, err := DiffBytes(t.Context(), repo, "--cached")
	require.NoError(t, err)
	assert.Contains(t, string(output), "+after \t\n")
	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(envMarker)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestPathQueriesKeepCompleteUnixPaths(t *testing.T) {
	for _, component := range []string{"main\nrepo", "repo \t\n", `literal\backslash`} {
		t.Run(component, func(t *testing.T) {
			original := initRepo(t)
			repo := filepath.Join(t.TempDir(), component)
			require.NoError(t, os.Rename(original, repo))
			paths, err := QueryWorktreePaths(t.Context(), repo)
			require.NoError(t, err)
			assert.Equal(t, pathutil.Canonicalize(repo), paths.TopLevel)
			assert.Equal(t, pathutil.Canonicalize(filepath.Join(repo, ".git")), paths.CommonDir)
			assert.Equal(t, paths.GitDir, paths.CommonDir)
			info := GetToplevelInfo(t.Context(), repo)
			assert.Equal(t, paths.TopLevel, info.Toplevel)
			assert.False(t, info.IsWorktree)
			linked := filepath.Join(t.TempDir(), "linked \n")
			require.NoError(t, Run(t.Context(), repo, "worktree", "add", "-b", "linked", linked))
			paths, err = QueryWorktreePaths(t.Context(), linked)
			require.NoError(t, err)
			assert.Equal(t, pathutil.Canonicalize(linked), paths.TopLevel)
			assert.Equal(t, pathutil.Canonicalize(filepath.Join(repo, ".git")), paths.CommonDir)
			assert.False(t, pathutil.SamePath(paths.GitDir, paths.CommonDir))
		})
	}
}

func TestPathQueryRequiresOnlyOneFramingLF(t *testing.T) {
	helperDir := t.TempDir()
	testutil.WriteExecutable(t, filepath.Join(helperDir, "git"), "#!/bin/sh\nprintf '%s' \"$LEAPMUX_TEST_RAW_PATH\"\n")
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, test := range []struct {
		output, want string
		fails        bool
	}{
		{"", "", true}, {"/unframed", "", true}, {"/path \t\n", "/path \t", false}, {"/path\n\n", "/path\n", false}, {"/path\r\n", "/path\r", false},
	} {
		t.Setenv("LEAPMUX_TEST_RAW_PATH", test.output)
		path, err := QueryToplevel(t.Context(), helperDir)
		if test.fails {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			assert.Equal(t, test.want, path)
		}
	}
}

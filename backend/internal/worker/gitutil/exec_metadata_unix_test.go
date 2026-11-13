//go:build unix

package gitutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffBytesDisablesExternalDiffAndTextconv(t *testing.T) {
	repo := initRepo(t)
	tracked := filepath.Join(repo, "tracked.txt")
	require.NoError(t, os.WriteFile(tracked, []byte("before\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("tracked.txt diff=leapmux-test\n"), 0o600))
	require.NoError(t, Run(t.Context(), repo, "add", "tracked.txt", ".gitattributes"))
	require.NoError(t, os.WriteFile(tracked, []byte("after \t\n"), 0o600))
	helperDir := t.TempDir()
	marker := filepath.Join(helperDir, "executed")
	program := filepath.Join(helperDir, "diff-helper")
	t.Setenv("LEAPMUX_TEST_DIFF_MARKER", marker)
	testutil.WriteExecutable(t, program, "#!/bin/sh\nprintf ran > \"$LEAPMUX_TEST_DIFF_MARKER\"\nprintf converted\n")
	require.NoError(t, Run(t.Context(), repo, "config", "diff.external", program))
	_, err := Bytes(t.Context(), repo, "diff", "--no-textconv")
	require.NoError(t, err)
	_, err = os.Stat(marker)
	require.NoError(t, err, "the positive control must execute the external helper")
	require.NoError(t, os.Remove(marker))
	require.NoError(t, Run(t.Context(), repo, "config", "diff.leapmux-test.textconv", program))
	_, err = Bytes(t.Context(), repo, "diff", "--no-ext-diff")
	require.NoError(t, err)
	_, err = os.Stat(marker)
	require.NoError(t, err, "the positive control must execute textconv")
	require.NoError(t, os.Remove(marker))
	output, err := DiffBytes(t.Context(), repo)
	require.NoError(t, err)
	assert.Contains(t, string(output), "+after \t\n")
	_, err = os.Stat(marker)
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

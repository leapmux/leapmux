//go:build unix

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func installGitFailureProgram(t *testing.T, mode string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	directory := t.TempDir()
	program := filepath.Join(directory, "git")
	testutil.WriteExecutable(t, program, `#!/bin/sh
case "$LEAPMUX_TEST_GIT_FAILURE" in
config)
    case " $* " in
        *" config -z --get-regexp "*) printf 'config read refused\n' >&2; exit 1 ;;
    esac ;;
symref)
    case " $* " in
        *" symbolic-ref --quiet HEAD "*) printf 'symbolic-ref read refused\n' >&2; exit 128 ;;
    esac ;;
esac
exec "$LEAPMUX_TEST_NATIVE_GIT" "$@"
`)
	t.Setenv("LEAPMUX_TEST_NATIVE_GIT", realGit)
	t.Setenv("LEAPMUX_TEST_GIT_FAILURE", mode)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCheckoutRefusesConfigOnlyFailure(t *testing.T) {
	repo := initRepo(t)
	run(t, repo, "git", "branch", "feature")
	run(t, repo, "git", "update-ref", "refs/remotes/upstream/feature", "HEAD")
	run(t, repo, "git", "config", "branch.feature.remote", "origin")
	before := strings.TrimSpace(run(t, repo, "git", "symbolic-ref", "HEAD"))
	installGitFailureProgram(t, "config")
	err := checkoutBranchInDir(t.Context(), repo, "upstream/feature")
	require.ErrorContains(t, err, "config read refused")
	assert.Equal(t, before, strings.TrimSpace(run(t, repo, "git", "symbolic-ref", "HEAD")))
	_, err = pushStatusForPath(t.Context(), repo, "main")
	require.ErrorContains(t, err, "config read refused")
}

func TestUnbornSymbolicRefFailureIsNotSuccess(t *testing.T) {
	repo := testutil.NewEmptyGitRepo(t)
	installGitFailureProgram(t, "symref")
	info, err := queryGitPathInfo(t.Context(), repo)
	require.ErrorContains(t, err, "symbolic-ref read refused")
	assert.Nil(t, info)
	assert.NotErrorIs(t, err, errNotGitRepo)
}

func TestBlobLookupKeepsWhitespaceRootAndSymlink(t *testing.T) {
	original := initRepo(t)
	repo := filepath.Join(t.TempDir(), "repository ")
	require.NoError(t, os.Rename(original, repo))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked"), []byte("head\n"), 0o600))
	require.NoError(t, os.Symlink("tracked", filepath.Join(repo, "link")))
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "tracked symlink")
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(repo, alias))
	// The requested alias has no trailing whitespace. The discovered root does.
	for _, ref := range []leapmuxv1.GitFileRef{leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD, leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED} {
		_, dispatcher, writer := setupTestService(t)
		dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, &leapmuxv1.ReadGitFileRequest{Path: filepath.Join(alias, "link"), Ref: ref}, writer)
		require.Empty(t, writer.errors)
		require.Len(t, writer.responses, 1)
		var response leapmuxv1.ReadGitFileResponse
		require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
		require.True(t, response.Exists)
		assert.Equal(t, "tracked", string(response.Content))
	}
}

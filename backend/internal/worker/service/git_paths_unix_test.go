//go:build unix

package service

import (
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestReadGitFile_DirectoryAliasesPreserveFinalSymlinkBlobs(t *testing.T) {
	t.Parallel()
	dir := canonicalRepoDir(t)
	nested := filepath.Join(dir, "nested", "deep")
	require.NoError(t, os.MkdirAll(nested, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("committed\n"), 0600))
	require.NoError(t, os.Symlink("tracked.txt", filepath.Join(nested, "link")))
	require.NoError(t, os.Symlink("missing.txt", filepath.Join(nested, "dangling")))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "files and symlinks")
	require.NoError(t, os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("staged\n"), 0600))
	require.NoError(t, os.Remove(filepath.Join(nested, "link")))
	require.NoError(t, os.Symlink("other.txt", filepath.Join(nested, "link")))
	run(t, dir, "git", "add", ".")
	require.NoError(t, os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("working\n"), 0600))
	external := filepath.Join(t.TempDir(), "repo-alias")
	require.NoError(t, os.Symlink(dir, external))
	internal := filepath.Join(dir, "directory-alias")
	require.NoError(t, os.Symlink(nested, internal))
	for _, alias := range []struct{ name, dir string }{{"external", filepath.Join(external, "nested", "deep")}, {"internal", internal}} {
		for _, file := range []struct{ name, head, staged string }{{"tracked.txt", "committed\n", "staged\n"}, {"link", "tracked.txt", "other.txt"}, {"dangling", "missing.txt", "missing.txt"}} {
			for _, ref := range []leapmuxv1.GitFileRef{leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD, leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED} {
				t.Run(alias.name+"/"+file.name+"/"+ref.String(), func(t *testing.T) {
					path := filepath.Join(alias.dir, file.name)
					_, d, w := setupTestService(t)
					dispatch(d, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, &leapmuxv1.ReadGitFileRequest{Path: path, Ref: ref}, w)
					require.Empty(t, w.errors)
					require.Len(t, w.responses, 1)
					var resp leapmuxv1.ReadGitFileResponse
					require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
					require.True(t, resp.GetExists())
					assert.Equal(t, path, resp.GetPath())
					want := file.head
					if ref == leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED {
						want = file.staged
					}
					assert.Equal(t, want, string(resp.GetContent()))
				})
			}
		}
	}
}

func TestReadGitFile_PreservesLiteralBackslashOnUnix(t *testing.T) {
	t.Parallel()
	dir := canonicalRepoDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0700))
	paths := []struct{ path, head, staged string }{{filepath.Join(dir, "nested", "tracked.txt"), "nested head\n", "nested staged\n"}, {filepath.Join(dir, `nested\tracked.txt`), "literal head\n", "literal staged\n"}}
	for _, file := range paths {
		require.NoError(t, os.WriteFile(file.path, []byte(file.head), 0600))
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "distinct Unix paths")
	for _, file := range paths {
		require.NoError(t, os.WriteFile(file.path, []byte(file.staged), 0600))
	}
	run(t, dir, "git", "add", ".")
	for _, file := range paths {
		for _, ref := range []leapmuxv1.GitFileRef{leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD, leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED} {
			t.Run(filepath.Base(file.path)+"/"+ref.String(), func(t *testing.T) {
				_, d, w := setupTestService(t)
				dispatch(d, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, &leapmuxv1.ReadGitFileRequest{Path: file.path, Ref: ref}, w)
				require.Empty(t, w.errors)
				require.Len(t, w.responses, 1)
				var resp leapmuxv1.ReadGitFileResponse
				require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
				require.True(t, resp.GetExists())
				want := file.head
				if ref == leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED {
					want = file.staged
				}
				assert.Equal(t, want, string(resp.GetContent()))
			})
		}
	}
}

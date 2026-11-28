//go:build unix

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestGitBranchRPCsUseTheLiteralRepositoryPath(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	neighbor := filepath.Join(parent, "repo")
	literal := filepath.Join(parent, "repo ")
	for _, dir := range []string{literal, neighbor} {
		require.NoError(t, os.Rename(initRepo(t), dir))
		run(t, dir, "git", "branch", "topic")
	}
	_, d, _ := setupTestService(t)
	lookup := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, &leapmuxv1.GetGitInfoRequest{Path: literal}, lookup)
	require.Empty(t, lookup.errors)
	require.Len(t, lookup.responses, 1)
	var info leapmuxv1.GetGitInfoResponse
	require.NoError(t, proto.Unmarshal(lookup.responses[0].GetPayload(), &info))
	assert.Equal(t, pathutil.Canonicalize(literal), info.GetMainWorktreeRoot())

	for _, test := range []struct {
		name, method, branch string
		request              proto.Message
	}{
		{"checkout", leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure, "topic", &leapmuxv1.CheckoutBranchRequest{Path: literal, Branch: "topic"}},
		{"create", leapmuxv1connect.WorkerGitServiceCreateBranchProcedure, "new-topic", &leapmuxv1.CreateBranchRequest{Path: literal, NewBranch: "new-topic", BaseBranch: "topic"}},
		{"delete", leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure, "main", &leapmuxv1.DeleteBranchRequest{Path: literal, BranchToDelete: "topic", SwitchToBranch: "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := newTestWriter()
			dispatch(d, test.method, test.request, w)
			require.Empty(t, w.errors)
			require.Len(t, w.responses, 1)
			assert.Equal(t, test.branch, strings.TrimSpace(run(t, literal, "git", "branch", "--show-current")))
			assert.Equal(t, "main", strings.TrimSpace(run(t, neighbor, "git", "branch", "--show-current")))
			assert.NotEmpty(t, run(t, neighbor, "git", "branch", "--list", "topic"))
			assert.Empty(t, run(t, neighbor, "git", "branch", "--list", "new-topic"))
		})
	}
	assert.Empty(t, run(t, literal, "git", "branch", "--list", "topic"))
}

func TestReadGitFilePreservesATrailingSpaceInTheFileName(t *testing.T) {
	t.Parallel()
	dir := canonicalRepoDir(t)
	literal := filepath.Join(dir, "report ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report"), []byte("other file\n"), 0600))
	require.NoError(t, os.WriteFile(literal, []byte("requested file\n"), 0600))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "distinct files")
	_, d, w := setupTestService(t)
	dispatch(d, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, &leapmuxv1.ReadGitFileRequest{Path: literal, Ref: leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var response leapmuxv1.ReadGitFileResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &response))
	assert.Equal(t, literal, response.GetPath())
	assert.Equal(t, "requested file\n", string(response.GetContent()))
}

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

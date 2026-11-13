//go:build unix

package service

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests change PATH and therefore must not run in parallel.
type rollbackNativeControl struct {
	realGit    string
	marker     string
	connection *net.TCPConn
	completed  <-chan struct{}
}

func newNativeRollbackBranch(t *testing.T, svc *Service, repo string) rollbackBranch {
	t.Helper()
	plan, err := svc.validateCreateBranch(t.Context(), repo, "native-owned", "")
	require.NoError(t, err)
	intent, err := svc.prepareCreationStartup(t.Context(), plan)
	require.NoError(t, err)
	result, err := svc.executeCreateBranch(t.Context(), intent.Plan)
	require.NoError(t, err)
	require.NotNil(t, result.Rollback.CreatedBranch)
	rollback := *result.Rollback.CreatedBranch
	require.NotEmpty(t, rollback.initialBranchCommit)
	require.False(t, rollback.OriginalDetached)
	require.Equal(t, "main", rollback.OriginalBranch)
	require.Equal(t, "refs/heads/"+rollback.CreatedBranch, strings.TrimSpace(run(t, repo, "git", "symbolic-ref", "HEAD")))
	require.Equal(t, rollback.initialBranchCommit, strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD")))
	run(t, repo, "git", "config", "--local", "branch."+rollback.CreatedBranch+".remote", "origin")
	run(t, repo, "git", "config", "--local", "branch."+rollback.CreatedBranch+".merge", "refs/heads/"+rollback.CreatedBranch)
	run(t, repo, "git", "config", "--local", "branch."+rollback.CreatedBranch+".description", "owned branch metadata")
	return rollback
}

// startRollbackNativeControl pauses the real operation at the selected helper phase.
// Closing its control connection releases a blocked helper on every failure path.
func startRollbackNativeControl(t *testing.T, svc *Service, rollback rollbackBranch, phase string) *rollbackNativeControl {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	wrapperDir := t.TempDir()
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	testutil.WriteExecutable(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf("#!/bin/sh\nexec %s -test.run='^TestHelperGitPushSnapshot$' -- \"$@\"\n", quote(executable)))
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, listener.SetDeadline(time.Now().Add(30*time.Second)))
	marker := filepath.Join(wrapperDir, "captured")
	t.Setenv("LEAPMUX_TEST_REAL_GIT", realGit)
	t.Setenv("LEAPMUX_TEST_PUSH_BARRIER_PHASE", phase)
	t.Setenv("LEAPMUX_TEST_PUSH_SNAPSHOT_ADDRESS", listener.Addr().String())
	t.Setenv("LEAPMUX_TEST_PUSH_SNAPSHOT_MARKER", marker)
	t.Setenv("LEAPMUX_TEST_ROLLBACK_REFERENCE", "refs/heads/"+rollback.CreatedBranch)
	t.Setenv("LEAPMUX_TEST_ROLLBACK_ORIGINAL_BRANCH", rollback.OriginalBranch)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	completed := make(chan struct{})
	control := &rollbackNativeControl{realGit: realGit, marker: marker, completed: completed}
	t.Cleanup(func() {
		_ = listener.Close()
		if control.connection != nil {
			_ = control.connection.Close()
		}
		select {
		case <-completed:
		case <-time.After(30 * time.Second):
			t.Error("branch rollback did not finish after its helper was released")
		}
	})
	go func() { defer close(completed); svc.rollbackCreatedBranch(rollback) }()
	control.connection, err = listener.AcceptTCP()
	require.NoError(t, err, "rollback did not reach its %s barrier", phase)
	require.NoError(t, control.connection.SetDeadline(time.Now().Add(30*time.Second)))
	line, err := bufio.NewReader(control.connection).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "barrier ready\n", line)
	// The external Git operation below deliberately does not use this mutex.
	mutex := svc.gitIndexLock(rollback.RepoGitDir)
	if mutex.TryLock() {
		mutex.Unlock()
		t.Fatal("rollback released its repository mutex before the native barrier")
	}
	return control
}

func (control *rollbackNativeControl) releaseAndWait(t *testing.T) {
	t.Helper()
	_, err := control.connection.Write([]byte{1})
	require.NoError(t, err)
	select {
	case <-control.completed:
	case <-time.After(30 * time.Second):
		t.Fatal("branch rollback did not finish after its native barrier was released")
	}
}

func nativeRollbackConfigPattern(rollback rollbackBranch) string {
	return `^branch\.` + regexp.QuoteMeta(rollback.CreatedBranch) + `\.`
}

func requireNativeRollbackBranchAbsent(t *testing.T, control *rollbackNativeControl, rollback rollbackBranch) {
	t.Helper()
	_, err := exec.Command(control.realGit, "-C", rollback.WorkingDir, "show-ref", "--verify", "--quiet", "refs/heads/"+rollback.CreatedBranch).Output()
	var native *exec.ExitError
	require.ErrorAs(t, err, &native)
	require.Equal(t, 1, native.ExitCode())
	require.Empty(t, native.Stderr)
}

func requireNativeRollbackConfigAbsent(t *testing.T, control *rollbackNativeControl, rollback rollbackBranch) {
	t.Helper()
	output, err := exec.Command(control.realGit, "-C", rollback.WorkingDir, "config", "--local", "-z", "--get-regexp", nativeRollbackConfigPattern(rollback)).Output()
	var native *exec.ExitError
	require.ErrorAs(t, err, &native)
	require.Equal(t, 1, native.ExitCode())
	require.Empty(t, output)
	require.Empty(t, native.Stderr)
}

func TestRollbackCreatedBranch_UsesExpectedCommitAtNativeDeletion(t *testing.T) {
	for _, advance := range []bool{false, true} {
		t.Run(fmt.Sprintf("advance=%t", advance), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			repo := canonicalRepoDir(t)
			rollback := newNativeRollbackBranch(t, svc, repo)
			beforeConfig := run(t, repo, "git", "config", "--local", "-z", "--get-regexp", nativeRollbackConfigPattern(rollback))
			control := startRollbackNativeControl(t, svc, rollback, "delete")
			reference := "refs/heads/" + rollback.CreatedBranch
			require.Equal(t, "refs/heads/"+rollback.OriginalBranch, strings.TrimSpace(run(t, repo, control.realGit, "symbolic-ref", "HEAD")))
			require.Equal(t, rollback.initialBranchCommit, strings.TrimSpace(run(t, repo, control.realGit, "rev-parse", reference)))
			var advanced string
			if advance {
				tree := strings.TrimSpace(run(t, repo, control.realGit, "rev-parse", rollback.initialBranchCommit+"^{tree}"))
				advanced = strings.TrimSpace(run(t, repo, control.realGit, "commit-tree", tree, "-p", rollback.initialBranchCommit, "-m", "external progress during deletion"))
				require.NotEqual(t, rollback.initialBranchCommit, advanced)
				// This native ref write occurs after the preliminary read and
				// while the service still holds its own repository mutex.
				run(t, repo, control.realGit, "update-ref", reference, advanced, rollback.initialBranchCommit)
			}
			control.releaseAndWait(t)
			assert.Equal(t, "refs/heads/"+rollback.OriginalBranch, strings.TrimSpace(run(t, repo, control.realGit, "symbolic-ref", "HEAD")))
			if advance {
				output, err := exec.Command(control.realGit, "-C", repo, "rev-parse", "--verify", reference).Output()
				require.NoError(t, err, "the commit advanced inside the native deletion window must survive")
				assert.Equal(t, advanced, strings.TrimSpace(string(output)))
				assert.Equal(t, "external progress during deletion", strings.TrimSpace(run(t, repo, control.realGit, "log", "-1", "--format=%s", reference)))
				assert.Equal(t, beforeConfig, run(t, repo, control.realGit, "config", "--local", "-z", "--get-regexp", nativeRollbackConfigPattern(rollback)))
			} else {
				requireNativeRollbackBranchAbsent(t, control, rollback)
				requireNativeRollbackConfigAbsent(t, control, rollback)
			}
		})
	}
}

func TestRollbackCreatedBranch_CleansUpAfterNativeRestoreChangesHeadAndFails(t *testing.T) {
	svc, _, _ := setupTestService(t)
	repo := canonicalRepoDir(t)
	rollback := newNativeRollbackBranch(t, svc, repo)
	// The helper pauses only after a successful native checkout of main.
	// Starting on the owned branch makes the restoration path unavoidable.
	control := startRollbackNativeControl(t, svc, rollback, "restore-error")
	require.Equal(t, "refs/heads/"+rollback.OriginalBranch, strings.TrimSpace(run(t, repo, control.realGit, "symbolic-ref", "HEAD")))
	require.Equal(t, rollback.initialBranchCommit, strings.TrimSpace(run(t, repo, control.realGit, "rev-parse", "refs/heads/"+rollback.CreatedBranch)))
	control.releaseAndWait(t)
	marker, err := os.ReadFile(control.marker)
	require.NoError(t, err)
	assert.Equal(t, "native checkout succeeded; returning controlled restore error\n", string(marker))
	assert.Equal(t, "refs/heads/"+rollback.OriginalBranch, strings.TrimSpace(run(t, repo, control.realGit, "symbolic-ref", "HEAD")))
	requireNativeRollbackBranchAbsent(t, control, rollback)
	requireNativeRollbackConfigAbsent(t, control, rollback)
}

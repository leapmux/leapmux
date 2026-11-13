//go:build unix

package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperGitPushSnapshot runs real Git and pauses the selected phase.
// Metadata snapshots pause after the final HEAD probe, once all independent
// path queries completed, so the captured identity is internally consistent.
// Delete mode pauses before native update-ref. Restore-error mode pauses after
// a successful native checkout and then reports a controlled failure.
func TestHelperGitPushSnapshot(t *testing.T) {
	address := os.Getenv("LEAPMUX_TEST_PUSH_SNAPSHOT_ADDRESS")
	if address == "" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		os.Exit(2)
	}
	args := os.Args[separator+1:]
	barrier := func() {
		marker, err := os.OpenFile(os.Getenv("LEAPMUX_TEST_PUSH_SNAPSHOT_MARKER"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			return
		}
		if err != nil {
			os.Exit(3)
		}
		if marker.Close() != nil {
			os.Exit(4)
		}
		connection, err := net.DialTimeout("tcp", address, 30*time.Second)
		if err != nil {
			os.Exit(5)
		}
		if connection.SetDeadline(time.Now().Add(30*time.Second)) != nil {
			os.Exit(6)
		}
		if _, err := io.WriteString(connection, "barrier ready\n"); err != nil {
			os.Exit(7)
		}
		var release [1]byte
		if _, err := io.ReadFull(connection, release[:]); err != nil {
			os.Exit(8)
		}
		_ = connection.Close()
	}
	phase := os.Getenv("LEAPMUX_TEST_PUSH_BARRIER_PHASE")
	if phase == "network" && slices.Contains(args, "push") {
		barrier()
	}
	deleteCommand := slices.Index(args, "update-ref")
	if phase == "delete" && deleteCommand >= 0 && slices.Contains(args[deleteCommand+1:], "-d") && slices.Contains(args[deleteCommand+1:], os.Getenv("LEAPMUX_TEST_ROLLBACK_REFERENCE")) {
		barrier()
	}
	checkoutCommand := slices.Index(args, "checkout")
	restoreFailure := phase == "restore-error" && checkoutCommand >= 0 && checkoutCommand+2 == len(args) && args[checkoutCommand+1] == os.Getenv("LEAPMUX_TEST_ROLLBACK_ORIGINAL_BRANCH")
	command := exec.Command(os.Getenv("LEAPMUX_TEST_REAL_GIT"), args...)
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err == nil && phase == "snapshot" && slices.Contains(args, "rev-parse") && slices.Contains(args, "--symbolic-full-name") {
		barrier()
	}
	if err == nil && restoreFailure {
		barrier()
	}
	if _, writeErr := os.Stdout.Write(output); writeErr != nil {
		os.Exit(9)
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(10)
	}
	if restoreFailure {
		if err := os.WriteFile(os.Getenv("LEAPMUX_TEST_PUSH_SNAPSHOT_MARKER"), []byte("native checkout succeeded; returning controlled restore error\n"), 0o600); err != nil {
			os.Exit(11)
		}
		if _, err := fmt.Fprintln(os.Stderr, "controlled restore failure after successful native checkout"); err != nil {
			os.Exit(12)
		}
		os.Exit(43)
	}
	os.Exit(0)
}

func TestPushBranch_ReloadsCheckoutAfterAcquiringIndexLock(t *testing.T) {
	// PATH is process-global. This test must not run in parallel.
	for _, phase := range []string{"snapshot", "network", "root-change"} {
		for _, worktree := range []bool{false, true} {
			label := "main checkout"
			if worktree {
				label = "linked worktree"
			}
			t.Run(phase+"/"+label, func(t *testing.T) {
				svc, _, _ := setupTestService(t)
				repo := canonicalRepoDir(t)
				remote := t.TempDir()
				initBareRepo(t, remote)
				run(t, repo, "git", "remote", "add", "origin", remote)
				dir := repo
				if worktree {
					dir = filepath.Join(t.TempDir(), "linked")
					run(t, repo, "git", "worktree", "add", "-b", "branch-a", dir)
				} else {
					run(t, dir, "git", "checkout", "-b", "branch-a")
				}
				run(t, dir, "git", "branch", "branch-b")
				run(t, dir, "git", "push", "-u", "origin", "branch-a")
				require.NoError(t, os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("new work\n"), 0600))
				lookupDir := dir
				var replacement string
				beforeHead := run(t, dir, "git", "rev-parse", "HEAD")
				beforeStatus := run(t, dir, "git", "status", "--porcelain")
				beforeRefs := run(t, remote, "git", "show-ref")
				if phase == "root-change" {
					replacement = canonicalRepoDir(t)
					lookupDir = filepath.Join(t.TempDir(), "checkout-alias")
					require.NoError(t, os.Symlink(dir, lookupDir))
				}
				realGit, err := exec.LookPath("git")
				require.NoError(t, err)
				executable, err := os.Executable()
				require.NoError(t, err)
				listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
				require.NoError(t, err)
				defer func() { _ = listener.Close() }()
				require.NoError(t, listener.SetDeadline(time.Now().Add(30*time.Second)))
				wrapperDir := t.TempDir()
				quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
				testutil.WriteExecutable(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf("#!/bin/sh\nexec %s -test.run='^TestHelperGitPushSnapshot$' -- \"$@\"\n", quote(executable)))
				t.Setenv("LEAPMUX_TEST_REAL_GIT", realGit)
				barrierPhase := phase
				if phase == "root-change" {
					barrierPhase = "snapshot"
				}
				t.Setenv("LEAPMUX_TEST_PUSH_BARRIER_PHASE", barrierPhase)
				t.Setenv("LEAPMUX_TEST_PUSH_SNAPSHOT_ADDRESS", listener.Addr().String())
				t.Setenv("LEAPMUX_TEST_PUSH_SNAPSHOT_MARKER", filepath.Join(wrapperDir, "captured"))
				t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				mutex := svc.gitIndexLock(filepath.Join(repo, ".git"))
				lockHeld := false
				if phase != "network" {
					mutex.Lock()
					lockHeld = true
				}
				unlock := func() {
					if lockHeld {
						mutex.Unlock()
						lockHeld = false
					}
				}
				defer unlock()
				ctx, cancel := context.WithCancel(t.Context())
				result := make(chan error, 1)
				completed := make(chan struct{})
				var connection *net.TCPConn
				go func() {
					defer close(completed)
					result <- svc.pushBranch(ctx, lookupDir)
				}()
				t.Cleanup(func() {
					cancel()
					if connection != nil {
						_ = connection.Close()
					}
					unlock()
					select {
					case <-completed:
					case <-time.After(30 * time.Second):
						t.Error("push did not stop during cleanup")
					}
				})
				connection, err = listener.AcceptTCP()
				require.NoError(t, err)
				defer func() { _ = connection.Close() }()
				require.NoError(t, connection.SetDeadline(time.Now().Add(30*time.Second)))
				line, err := bufio.NewReader(connection).ReadString('\n')
				require.NoError(t, err)
				require.Equal(t, "barrier ready\n", line)
				// The network phase must leave the index mutex available to checkout.
				if phase == "network" {
					require.True(t, mutex.TryLock())
					lockHeld = true
				}
				// Simulate checkout's existing critical section.
				if phase == "root-change" {
					require.NoError(t, os.Remove(lookupDir))
					require.NoError(t, os.Symlink(replacement, lookupDir))
				} else {
					require.NoError(t, checkoutBranchInDir(t.Context(), dir, "branch-b"))
				}
				_, err = connection.Write([]byte{1})
				require.NoError(t, err)
				unlock()
				select {
				case err := <-result:
					if phase == "root-change" {
						require.ErrorContains(t, err, "git repository changed before push")
					} else {
						require.NoError(t, err)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("push did not finish after checkout and release")
				}
				if phase == "root-change" {
					assert.Equal(t, beforeHead, run(t, dir, "git", "rev-parse", "HEAD"))
					assert.Equal(t, beforeStatus, run(t, dir, "git", "status", "--porcelain"))
					assert.Equal(t, beforeRefs, run(t, remote, "git", "show-ref"))
					return
				}
				assert.Equal(t, "refs/heads/branch-b", strings.TrimSpace(run(t, dir, "git", "symbolic-ref", "HEAD")))
				pushedBranch := "branch-b"
				if phase == "network" {
					pushedBranch = "branch-a"
				}
				local := strings.TrimSpace(run(t, dir, "git", "rev-parse", "refs/heads/"+pushedBranch))
				assert.Equal(t, local, strings.TrimSpace(run(t, remote, "git", "rev-parse", "refs/heads/"+pushedBranch)))
				assert.Equal(t, "new work\n", run(t, remote, "git", "show", "refs/heads/"+pushedBranch+":dirty.txt"))
			})
		}
	}
}

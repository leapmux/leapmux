//go:build unix

package controlipc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func namespaceTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ln-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	return root
}
func namespaceDeepDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}
func namespaceListener(t *testing.T, dir, file string, orphan bool) *net.UnixListener {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, file), Net: "unix"})
	require.NoError(t, err)
	if orphan {
		listener.SetUnlinkOnClose(false)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}
func requireNamespaceReachable(t *testing.T, path string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestSocketDirectorySelectsByCompleteByteLength(t *testing.T) {
	for _, extra := range []int{-1, 0, 1} {
		for _, unicodePath := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", extra, unicodePath), func(t *testing.T) {
				root := namespaceTestRoot(t)
				desired := locallisten.MaxUnixSocketPathBytes() - 28 + extra
				prefix := root + string(filepath.Separator)
				component := ""
				if unicodePath {
					component = "é"
				}
				component += strings.Repeat("x", desired-len(prefix)-len(component))
				data := filepath.Join(root, component)
				require.NoError(t, os.MkdirAll(data, 0o700))
				canonical, err := lifecycle.CanonicalDir(data)
				require.NoError(t, err)
				// macOS resolves /tmp to /private/tmp. Budget the canonical spelling.
				delta := len(canonical) - desired
				if delta != 0 {
					data = filepath.Join(root, component[:len(component)-delta])
					require.NoError(t, os.MkdirAll(data, 0o700))
					canonical, err = lifecycle.CanonicalDir(data)
					require.NoError(t, err)
				}
				directory, err := prepareSocketDirectory(data, root)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, directory.Close()) })
				if extra <= 0 {
					assert.Equal(t, SocketDir(canonical), directory.Path())
				} else {
					assert.NotEqual(t, SocketDir(canonical), directory.Path())
				}
				assert.True(t, socketPathFits(directory.Path()))
			})
		}
	}
}

func TestSocketDirectoryKeepsCanonicalIdentityAndSeparatesDirectories(t *testing.T) {
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	first, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	path := first.Path()
	require.NoError(t, first.Close())
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(data, alias))
	second, err := prepareSocketDirectory(alias, root)
	require.NoError(t, err)
	assert.Equal(t, path, second.Path())
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	other, err := prepareSocketDirectory(namespaceDeepDataDir(t), root)
	require.NoError(t, err)
	assert.NotEqual(t, path, other.Path())
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	assert.NotEqual(t, SocketURL(path, "same-worker", SocketKindAgent, "same-tab"), SocketURL(other.Path(), "same-worker", SocketKindAgent, "same-tab"))
	assert.Equal(t, SocketURL(path, "old-worker", SocketKindAgent, "same-tab"), SocketURL(path, "new-worker", SocketKindAgent, "same-tab"))
}

func TestSocketDirectoryRefusesOwnershipBeforeSweeping(t *testing.T) {
	for _, kind := range []string{"identity mismatch", "identity empty", "identity permissions", "identity symlink", "unclaimed contents", "namespace permissions", "namespace symlink", "account permissions", "account symlink", "lock permissions", "lock symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := namespaceTestRoot(t)
			data := namespaceDeepDataDir(t)
			directory, err := prepareSocketDirectory(data, root)
			require.NoError(t, err)
			dir := directory.Path()
			require.NoError(t, directory.Close())
			orphan := filepath.Join(dir, "orphan.sock")
			listener := namespaceListener(t, dir, "orphan.sock", true)
			require.NoError(t, listener.Close())
			marker := filepath.Join(dir, socketNamespaceIdentityName)
			lock := filepath.Join(dir, socketNamespaceLockName)
			switch kind {
			case "identity mismatch":
				require.NoError(t, os.WriteFile(marker, []byte("another directory"), 0o600))
			case "identity empty":
				require.NoError(t, os.WriteFile(marker, nil, 0o600))
			case "identity permissions":
				require.NoError(t, os.Chmod(marker, 0o644))
			case "identity symlink":
				target := filepath.Join(dir, "other-marker")
				require.NoError(t, os.Rename(marker, target))
				require.NoError(t, os.Symlink(target, marker))
			case "unclaimed contents":
				require.NoError(t, os.Remove(marker))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ordinary"), []byte("keep"), 0o600))
			case "namespace permissions":
				require.NoError(t, os.Chmod(dir, 0o755))
			case "namespace symlink":
				target := dir + "-original"
				require.NoError(t, os.Rename(dir, target))
				require.NoError(t, os.Symlink(target, dir))
			case "account permissions":
				require.NoError(t, os.Chmod(filepath.Dir(dir), 0o755))
			case "account symlink":
				account := filepath.Dir(dir)
				target := account + "-original"
				require.NoError(t, os.Rename(account, target))
				require.NoError(t, os.Symlink(target, account))
			case "lock permissions":
				require.NoError(t, os.Chmod(lock, 0o644))
			case "lock symlink":
				target := filepath.Join(dir, "other-lock")
				require.NoError(t, os.Rename(lock, target))
				require.NoError(t, os.Symlink(target, lock))
			}
			refused, err := prepareSocketDirectory(data, root)
			if refused != nil {
				_ = refused.Close()
			}
			require.Error(t, err)
			_, err = os.Stat(orphan)
			require.NoError(t, err, "refusal must leave even orphan sockets untouched")
		})
	}
}

func TestSocketDirectoryRejectsPreferredPermissionFailureWithoutFallback(t *testing.T) {
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	preferred := SocketDir(data)
	require.NoError(t, os.MkdirAll(preferred, 0o700))
	require.NoError(t, os.Chmod(preferred, 0o755))
	directory, err := prepareSocketDirectory(data, root)
	require.Nil(t, directory)
	require.ErrorContains(t, err, "wider than this account")
	_, err = os.Stat(filepath.Join(root, "leapmux-"+lifecycle.CurrentAccountID()))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestSocketDirectoryLockAndSweepKeepSiblingListeners(t *testing.T) {
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	owner, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	sibling, err := prepareSocketDirectory(namespaceDeepDataDir(t), root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sibling.Close()) })
	namespaceListener(t, sibling.Path(), "live.sock", false)
	orphan := namespaceListener(t, owner.Path(), "orphan.sock", true)
	require.NoError(t, orphan.Close())
	file := filepath.Join(owner.Path(), "ordinary.sock")
	require.NoError(t, os.WriteFile(file, []byte("keep"), 0o600))
	link := filepath.Join(owner.Path(), "link.sock")
	require.NoError(t, os.Symlink(file, link))
	refused, err := prepareSocketDirectory(data, root)
	require.Nil(t, refused)
	require.ErrorContains(t, err, "already in use")
	_, err = os.Lstat(filepath.Join(owner.Path(), "orphan.sock"))
	require.NoError(t, err)
	require.NoError(t, owner.Sweep())
	_, err = os.Lstat(filepath.Join(owner.Path(), "orphan.sock"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, file)
	_, err = os.Lstat(link)
	require.NoError(t, err)
	requireNamespaceReachable(t, filepath.Join(sibling.Path(), "live.sock"))
	require.NoError(t, owner.Close())
	require.NoError(t, owner.Close())
	require.Error(t, owner.Sweep())
	require.FileExists(t, filepath.Join(owner.Path(), socketNamespaceIdentityName))
	require.FileExists(t, filepath.Join(owner.Path(), socketNamespaceLockName))
	replacement, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
}

func TestSocketDirectoryRejectsAnOverlongFallbackBeforeCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("r", 120))
	directory, err := prepareSocketDirectory(namespaceDeepDataDir(t), root)
	require.Nil(t, directory)
	require.ErrorIs(t, err, locallisten.ErrSocketPathTooLong)
	_, err = os.Stat(root)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

type namespaceDelegations struct {
	acquired, released atomic.Int64
	entered            chan struct{}
	proceed            <-chan struct{}
}

func (d *namespaceDelegations) Acquire(userid.UserID) { d.acquired.Add(1) }
func (d *namespaceDelegations) Release(ctx context.Context, _ userid.UserID) error {
	d.released.Add(1)
	if d.entered != nil {
		close(d.entered)
		select {
		case <-d.proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestFactoryCloseRetiresListenersBeforeReleasingNamespace(t *testing.T) {
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	directory, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	release := make(chan struct{})
	delegations := &namespaceDelegations{entered: make(chan struct{}), proceed: release}
	factory := &Factory{SocketDir: directory.Path(), directory: directory, Delegation: delegations}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		require.NoError(t, factory.Close())
	})
	_, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-1", TabID: "agent-01"})
	require.NoError(t, err)
	refused, err := prepareSocketDirectory(data, root)
	require.Nil(t, refused)
	require.Error(t, err)
	done := make(chan error, 1)
	go func() { done <- factory.Close() }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-delegations.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = os.Lstat(strings.TrimPrefix(SocketURL(directory.Path(), "worker-1", SocketKindAgent, "agent-01"), "unix:"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	// A slow remote revoke must not keep a listener alive after lock release.
	replacement, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replacement.Close()) })
	live := namespaceListener(t, replacement.Path(), "a-agent-01.sock", false)
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cleanup()
	cleanup()
	require.NoError(t, factory.Close())
	assert.EqualValues(t, 1, delegations.acquired.Load())
	assert.EqualValues(t, 1, delegations.released.Load())
	requireNamespaceReachable(t, live.Addr().String())
	_, _, err = factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-1", TabID: "another1"})
	require.ErrorContains(t, err, "closed")
}

func TestFactoryCloseAndConcurrentSpawnsBalanceCleanup(t *testing.T) {
	root := namespaceTestRoot(t)
	directory, err := prepareSocketDirectory(namespaceDeepDataDir(t), root)
	require.NoError(t, err)
	delegations := &namespaceDelegations{}
	factory := &Factory{SocketDir: directory.Path(), directory: directory, Delegation: delegations}
	t.Cleanup(func() { require.NoError(t, factory.Close()) })
	start := make(chan struct{})
	var tasks sync.WaitGroup
	for i := range 16 {
		tasks.Go(func() {
			<-start
			_, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-1", TabID: fmt.Sprintf("%08d", i)})
			if err == nil {
				cleanup()
				cleanup()
			} else {
				assert.ErrorContains(t, err, "closed")
			}
		})
	}
	tasks.Go(func() { <-start; assert.NoError(t, factory.Close()) })
	close(start)
	tasks.Wait()
	require.NoError(t, factory.Close())
	assert.Equal(t, delegations.acquired.Load(), delegations.released.Load())
	assert.Empty(t, factory.servers)
}

func TestFactoryDirectoryPreparationRefusalRemainsAttached(t *testing.T) {
	data := namespaceTestRoot(t)
	require.NoError(t, os.MkdirAll(SocketDir(data), 0o700))
	require.NoError(t, os.Chmod(SocketDir(data), 0o755))
	factory := &Factory{}
	require.Error(t, factory.PrepareSocketDirectory(data))
	t.Cleanup(func() { require.NoError(t, factory.Close()) })
	factory.SocketDir = namespaceTestRoot(t)
	envs, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), TabID: "agent-01"})
	require.ErrorContains(t, err, "wider than this account")
	assert.Empty(t, envs)
	assert.Nil(t, cleanup)
	require.Error(t, factory.PrepareSocketDirectory(namespaceTestRoot(t)))
}

func TestSocketNamespaceProcessHelper(t *testing.T) {
	data := os.Getenv("LEAPMUX_NAMESPACE_TEST_DATA")
	if data == "" {
		t.Skip("subprocess helper")
	}
	directory, err := prepareSocketDirectory(data, os.Getenv("LEAPMUX_NAMESPACE_TEST_ROOT"))
	require.NoError(t, err)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(directory.Path(), "orphan.sock"), Net: "unix"})
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	_, err = fmt.Fprintln(os.Stdout, "ready")
	require.NoError(t, err)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestSocketNamespaceProcessExitReleasesLockAndLeavesSweepableSocket(t *testing.T) {
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	command := exec.CommandContext(testutil.DeadlineContext(t), os.Args[0], "-test.run=^TestSocketNamespaceProcessHelper$")
	command.Env = append(os.Environ(), "LEAPMUX_NAMESPACE_TEST_DATA="+data, "LEAPMUX_NAMESPACE_TEST_ROOT="+root)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	stopped := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", ready)
	refused, err := prepareSocketDirectory(data, root)
	require.Nil(t, refused)
	require.ErrorContains(t, err, "already in use")
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	stopped = true
	directory, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, directory.Close()) })
	socket := filepath.Join(directory.Path(), "orphan.sock")
	_, err = os.Lstat(socket)
	require.NoError(t, err)
	require.NoError(t, directory.Sweep())
	_, err = os.Lstat(socket)
	assert.ErrorIs(t, err, os.ErrNotExist)
	lock, err := filelock.OpenExisting(filepath.Join(directory.Path(), socketNamespaceLockName))
	require.NoError(t, err)
	defer func() { _ = lock.Close() }()
	held, err := filelock.TryLock(lock)
	require.NoError(t, err)
	assert.False(t, held)
}

func TestSocketDirectoryRefusesForeignNamespaceOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing directory ownership requires root")
	}
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	directory, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	dir := directory.Path()
	require.NoError(t, directory.Close())
	require.NoError(t, os.Chown(dir, 1, -1))
	t.Cleanup(func() { require.NoError(t, os.Chown(dir, 0, -1)) })
	refused, err := prepareSocketDirectory(data, root)
	require.Nil(t, refused)
	require.ErrorContains(t, err, "belongs to another account")
}

func TestSocketDirectoryRejectsMissingIdentityAndNonDirectoryParents(t *testing.T) {
	directory, err := prepareSocketDirectory("", namespaceTestRoot(t))
	require.Nil(t, directory)
	require.Error(t, err)
	root := namespaceTestRoot(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "run"), []byte("keep"), 0o600))
	directory, err = prepareSocketDirectory(root, namespaceTestRoot(t))
	require.Nil(t, directory)
	require.Error(t, err)
	content, err := os.ReadFile(filepath.Join(root, "run"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(content))
}

func TestSocketDirectoryIdentityRefusalReleasesItsLock(t *testing.T) {
	root := namespaceTestRoot(t)
	data := namespaceDeepDataDir(t)
	directory, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	path := directory.Path()
	require.NoError(t, directory.Close())
	marker := filepath.Join(path, socketNamespaceIdentityName)
	identity, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(marker, []byte("wrong"), 0o600))
	refused, err := prepareSocketDirectory(data, root)
	require.Nil(t, refused)
	require.ErrorContains(t, err, "another data directory")
	require.NoError(t, os.WriteFile(marker, identity, 0o600))
	replacement, err := prepareSocketDirectory(data, root)
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
}

func TestFactoryRefusesAChangedPreparedDirectory(t *testing.T) {
	directory, err := prepareSocketDirectory(namespaceDeepDataDir(t), namespaceTestRoot(t))
	require.NoError(t, err)
	factory := &Factory{SocketDir: namespaceTestRoot(t), directory: directory}
	t.Cleanup(func() { require.NoError(t, factory.Close()) })
	envs, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), TabID: "agent-01"})
	require.ErrorContains(t, err, "differs from its prepared namespace")
	assert.Empty(t, envs)
	assert.Nil(t, cleanup)
	assert.Empty(t, factory.servers)
}

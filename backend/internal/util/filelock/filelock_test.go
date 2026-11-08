package filelock

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperModeEnv and helperLockEnv parameterize the helper process the
// inheritance test re-executes the test binary as.
const (
	helperModeEnv = "LEAPMUX_FILELOCK_HELPER_MODE"
	helperLockEnv = "LEAPMUX_FILELOCK_HELPER_LOCK"
)

// TestHelperProcessFileLock is the helper process; it does nothing in
// the test process itself. It takes the lock, starts a child that
// outlives it, and exits without releasing, so the parent test can ask
// whether the child kept a copy.
func TestHelperProcessFileLock(t *testing.T) {
	switch os.Getenv(helperModeEnv) {
	case "outlive":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		fmt.Printf("child %d %s\n", os.Getpid(), listener.Addr())
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		_ = listener.Close()
		return
	case "hold-with-child":
	default:
		return
	}
	file, err := Open(os.Getenv(helperLockEnv), true)
	require.NoError(t, err)
	held, err := TryLock(file)
	require.NoError(t, err)
	require.True(t, held)
	child := exec.Command(os.Args[0], "-test.run=^TestHelperProcessFileLock$")
	child.Env = append(os.Environ(), helperModeEnv+"=outlive")
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	child.Stderr = os.Stderr
	require.NoError(t, child.Start())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}
	require.NoError(t, err)
	fields := strings.Fields(line)
	require.Len(t, fields, 3)
	require.Equal(t, "child", fields[0])
	fmt.Printf("ready %s %s\n", fields[1], fields[2])
	runtime.KeepAlive(file)
}

// The holder starts another Go process, observes its readiness, and exits.
// The surviving child must not retain the holder's native lock handle.
func TestLockIsNotInherited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcessFileLock$")
	command.Env = append(os.Environ(), helperModeEnv+"=hold-with-child", helperLockEnv+"="+path)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	command.Stderr = os.Stderr
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	fields := strings.Fields(line)
	require.Len(t, fields, 3, "the holder reports its surviving child's identity")
	require.Equal(t, "ready", fields[0])
	childPID, err := strconv.Atoi(fields[1])
	require.NoError(t, err)
	t.Cleanup(func() {
		connection, dialErr := net.DialTimeout("tcp", fields[2], 30*time.Second)
		if dialErr == nil {
			_ = connection.Close()
		}
		if process, findErr := os.FindProcess(childPID); findErr == nil {
			_ = process.Kill()
			_ = process.Release()
		}
	})
	require.NoError(t, command.Wait())
	require.NoError(t, ctx.Err())
	after, err := Open(path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = after.Close() })
	taken, err := TryLock(after)
	require.NoError(t, err)
	assert.True(t, taken, "the surviving child retains no copy of the holder's lock")
	require.NoError(t, Unlock(after))
}

// The lock is exclusive between two opens in one process, and a released
// lock is acquirable again.
func TestTryLockIsExclusiveAndReacquirable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	first, err := Open(path, true)
	require.NoError(t, err)
	held, err := TryLock(first)
	require.NoError(t, err)
	require.True(t, held)

	second, err := Open(path, false)
	require.NoError(t, err)
	taken, err := TryLock(second)
	require.NoError(t, err)
	assert.False(t, taken, "a held lock refuses a second taker")
	require.NoError(t, second.Close())

	require.NoError(t, Unlock(first))
	require.NoError(t, first.Close())

	third, err := Open(path, false)
	require.NoError(t, err)
	taken, err = TryLock(third)
	require.NoError(t, err)
	assert.True(t, taken, "a released lock is acquirable again")
	require.NoError(t, third.Close())
}

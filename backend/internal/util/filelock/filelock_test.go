package filelock

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
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
	if os.Getenv(helperModeEnv) != "hold-with-child" {
		return
	}
	f, err := Open(os.Getenv(helperLockEnv), true)
	if err != nil {
		fmt.Printf("failed: open: %v\n", err)
		os.Exit(1)
	}
	held, err := TryLock(f)
	if err != nil || !held {
		fmt.Printf("failed: held=%v error=%v\n", held, err)
		os.Exit(1)
	}

	// The child is a private executable the holder starts and leaves
	// behind. Its own marker file says when it runs; no pipe to the
	// holder keeps it alive.
	dir, err := os.MkdirTemp("", "filelock-child-*")
	if err != nil {
		fmt.Printf("failed: temp: %v\n", err)
		os.Exit(1)
	}
	marker := filepath.Join(dir, "started")
	script := filepath.Join(dir, "outlive.sh")
	testutil.WriteExecutable(t, script, "#!/bin/sh\necho started > "+marker+"\nsleep 30\n")
	cmd := exec.Command(script)
	require.NoError(t, cmd.Start())
	waitForFile(t, marker)

	fmt.Printf("ready %d %s\n", cmd.Process.Pid, dir)
	// Return, not os.Exit: nothing below closes f, so the descriptor --
	// and the lock, unless a child kept a copy -- ends with the process.
}

// waitForFile waits until path exists; the child's own write is the
// signal, never a timed guess.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never wrote %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLockIsNotInherited pins the close-on-exec property across
// processes: a holder starts a child executable through
// testutil.WriteExecutable and exits; the child that still runs retains
// no copy of the lock, so the next opener acquires it at once. The
// agentdir tests keep their own inheritance coverage; this one belongs
// to the extracted primitive.
func TestLockIsNotInherited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessFileLock$")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"=hold-with-child", helperLockEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "the helper reports before it exits")
	fields := strings.Fields(strings.TrimSpace(line))
	require.Len(t, fields, 3, "the helper reports ready, its child's pid, and the child dir: %q", line)
	require.Equal(t, "ready", fields[0])
	childPID, err := strconv.Atoi(fields[1])
	require.NoError(t, err)
	childDir := fields[2]
	t.Cleanup(func() {
		proc, findErr := os.FindProcess(childPID)
		if findErr == nil {
			_ = proc.Kill()
		}
		_ = os.RemoveAll(childDir)
	})

	require.NoError(t, cmd.Wait(), "the holder exited by itself")

	// The child still runs; the lock must already be free.
	after, err := Open(path, false)
	require.NoError(t, err)
	taken, lockErr := TryLock(after)
	require.NoError(t, lockErr)
	assert.True(t, taken,
		"the child that the holder started retains no copy of the lock")
	require.NoError(t, Unlock(after))
	require.NoError(t, after.Close())
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

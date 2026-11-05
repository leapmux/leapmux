//go:build linux

package procutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Inside a Docker container the process library calculates the boot time from
// the current time and the uptime, so two reads can differ by a second. A live
// process must not look like another process when that happens.
//
// A fake /proc moves the boot time on demand. It states the boot time in both
// files that the library can read, because the choice depends on where this test
// runs. The first read uses the real /proc, so the cache never holds a fake
// value. Not parallel: HOST_PROC applies to the whole test process.
func TestProcessIdentityRunsWhenTheBootTimeMoves(t *testing.T) {
	self, ok := IdentifyProcess(os.Getpid())
	require.True(t, ok)

	root := t.TempDir()
	pid := strconv.Itoa(os.Getpid())
	stat, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, pid), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, pid, "stat"), stat, 0o600))
	t.Setenv("HOST_PROC", root)

	for _, bootTime := range []int64{1_000_000_000, 1_000_000_001} {
		writeFakeBootTime(t, root, bootTime)
		assert.True(t, self.Runs(), "a boot time of %d must not change the identity", bootTime)
		other := ProcessIdentity{PID: self.PID, StartTime: self.StartTime - 1}
		assert.False(t, other.Runs(), "a stable boot time must not hide another start time")
	}

	// The fake holds the only stat file, so its removal proves that the reads
	// above came from the fake.
	require.NoError(t, os.Remove(filepath.Join(root, pid, "stat")))
	assert.False(t, self.Runs(), "the library must read HOST_PROC")
}

// writeFakeBootTime states bootTime in both files that can supply the boot time.
func writeFakeBootTime(t *testing.T, root string, bootTime int64) {
	t.Helper()
	btime := fmt.Sprintf("btime %d\n", bootTime)
	require.NoError(t, os.WriteFile(filepath.Join(root, "stat"), []byte(btime), 0o600))
	uptime := fmt.Sprintf("%d.50 0.00\n", time.Now().Unix()-bootTime)
	require.NoError(t, os.WriteFile(filepath.Join(root, "uptime"), []byte(uptime), 0o600))
}

package procutil

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gopsutil "github.com/shirou/gopsutil/v4/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinuxProcessTableReadsParentAndBirthFromOneStatRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pid := os.Getpid()
	parent := os.Getppid()
	dir := filepath.Join(root, strconv.Itoa(pid))
	require.NoError(t, os.Mkdir(dir, 0o700))
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	// The first open consumes the whole record. Every later open sees EOF.
	stat := fmt.Sprintf("%d (agent (test)) S %d%s 500%s\n", pid, parent, strings.Repeat(" 0", 17), strings.Repeat(" 0", 30))
	_, err = writer.WriteString(stat)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, os.Symlink(fmt.Sprintf("/proc/self/fd/%d", reader.Fd()), filepath.Join(dir, "stat")))
	ctx := context.WithValue(t.Context(), gopsutil.EnvKey, gopsutil.EnvMap{gopsutil.HostProcEnvKey: root})

	table, err := SnapshotProcessTable(ctx)
	require.NoError(t, err)
	record, found := table.records[pid]
	require.True(t, found, "the parent and start time must come from the same stat read")
	assert.Equal(t, parent, record.PPID)
	assert.Positive(t, record.StartTime)
}

func TestLinuxProcessTableMatchesLiveIdentityAndParent(t *testing.T) {
	t.Parallel()
	child := startWaitingProcess(t)
	identity, found := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, found)
	table, err := SnapshotProcessTable(t.Context())
	require.NoError(t, err)
	assert.Equal(t, ProcessRecord{PID: identity.PID, PPID: os.Getpid(), StartTime: identity.StartTime}, table.records[identity.PID])
	assert.NotEmpty(t, table.Name(t.Context(), identity.PID))
}

func TestLinuxProcessTableStopsOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	table, err := SnapshotProcessTable(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, table)
}

func TestLinuxProcessTableStopsWhenTheFinalStatReadIsCanceled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(os.Getpid()))
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "stat")
	stat := fmt.Sprintf("%d (agent) S 1%s 500\n", os.Getpid(), strings.Repeat(" 0", 17))
	require.NoError(t, os.WriteFile(path, []byte(stat), 0o600))
	ctx := context.WithValue(t.Context(), gopsutil.EnvKey, gopsutil.EnvMap{gopsutil.HostProcEnvKey: root})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	table, err := snapshotLinuxProcessTable(ctx, func(recordPath string) ([]byte, error) {
		assert.Equal(t, path, recordPath)
		data, err := os.ReadFile(recordPath)
		cancel()
		return data, err
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, table)
}

func TestParseLinuxProcessStatRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		"", "42 )unclosed(", "42 (agent) S 1", "42 (agent) S invalid", "42 (agent) S -1",
		"42 (agent) S 1" + strings.Repeat(" 0", 17) + " invalid",
		"42 (agent)S 1" + strings.Repeat(" 0", 17) + " 500",
	} {
		_, _, err := parseLinuxProcessStat([]byte(data))
		assert.Error(t, err, "%q", data)
	}
}

func TestLinuxProcessStartMillisKeepsNativePrecision(t *testing.T) {
	t.Parallel()
	millis, err := linuxProcessStartMillis(1_000, 29, 128)
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_226), millis)
	_, err = linuxProcessStartMillis(1_000, math.MaxUint64, 1)
	assert.Error(t, err)
}

package procutil

import (
	"context"
	"fmt"
	"math"
	"os"
	"syscall"
	"testing"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProcessTableReportsUnreadableParentAndKeepsOtherRows(t *testing.T) {
	t.Parallel()
	rows := []*process.Process{{Pid: 100}, {Pid: 200}, {Pid: 300}}
	denied := fmt.Errorf("read fixture parent: %w", os.ErrPermission)
	table, err := buildProcessTable(t.Context(), rows, func(_ context.Context, candidate *process.Process) (int32, error) {
		switch candidate.Pid {
		case 100:
			return 1, nil
		case 200:
			return 100, nil
		default:
			return 0, denied
		}
	})
	require.ErrorIs(t, err, os.ErrPermission, "an unreadable parent is an ownership uncertainty")
	assert.ErrorContains(t, err, "300")
	require.NotNil(t, table)
	assert.ElementsMatch(t, []ProcessRecord{{PID: 100, PPID: 1}, {PID: 200, PPID: 100}}, table.Records())
	pids, total := table.DescendantPIDs(100, 0, 0, nil)
	assert.Equal(t, []int{200}, pids)
	assert.Equal(t, 1, total)
}

func TestBuildProcessTableExcludesAVanishedProcessWithoutAnOwnershipError(t *testing.T) {
	t.Parallel()
	rows := []*process.Process{{Pid: 100}, {Pid: 200}}
	table, err := buildProcessTable(t.Context(), rows, func(_ context.Context, candidate *process.Process) (int32, error) {
		if candidate.Pid == 200 {
			return 0, process.ErrorProcessNotRunning
		}
		return 1, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []ProcessRecord{{PID: 100, PPID: 1}}, table.Records())
}

func TestBuildProcessTableStopsOnContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	table, err := buildProcessTable(ctx, []*process.Process{{Pid: 100}}, func(context.Context, *process.Process) (int32, error) {
		called = true
		return 1, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, table)
	assert.False(t, called)
}

func TestBuildProcessTableAcceptsAnEmptySnapshot(t *testing.T) {
	t.Parallel()
	table, err := buildProcessTable(t.Context(), nil, func(context.Context, *process.Process) (int32, error) {
		t.Error("an empty snapshot must perform no parent read")
		return 0, nil
	})
	require.NoError(t, err)
	require.NotNil(t, table)
	assert.Empty(t, table.Records())
	pids, total := table.DescendantPIDs(100, 0, 0, nil)
	assert.Empty(t, pids)
	assert.Zero(t, total)
}

func TestBuildProcessTableExcludesDarwinShortRecordForAnAbsentProcess(t *testing.T) {
	t.Parallel()
	// Darwin's SysctlKinfoProc returns EIO when a vanished PID supplies no record.
	table, err := buildProcessTable(t.Context(), []*process.Process{{Pid: math.MaxInt32}}, func(context.Context, *process.Process) (int32, error) {
		return 0, syscall.EIO
	})
	require.NoError(t, err, "a native missing record must not become a cleanup failure")
	assert.Empty(t, table.Records())
}

func TestBuildProcessTableRetainsAnIOErrorForALiveProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	table, err := buildProcessTable(t.Context(), []*process.Process{{Pid: int32(pid)}}, func(context.Context, *process.Process) (int32, error) {
		return 0, syscall.EIO
	})
	require.ErrorIs(t, err, syscall.EIO)
	assert.ErrorContains(t, err, fmt.Sprint(pid))
	assert.Empty(t, table.Records())
}

func TestNewProcessTablePreservesTraversalDepthCapAndFiltering(t *testing.T) {
	t.Parallel()
	table := NewProcessTable([]ProcessRecord{{PID: 10, PPID: 1}, {PID: 11, PPID: 10}, {PID: 12, PPID: 11}, {PID: 13, PPID: 10}, {PID: 14, PPID: 13}})
	pids, total := table.DescendantPIDs(10, 1, 0, nil)
	assert.Equal(t, []int{11, 13}, pids)
	assert.Equal(t, 2, total)
	pids, total = table.DescendantPIDs(10, 0, 1, func(pid int) bool { return pid != 11 })
	assert.Equal(t, []int{13}, pids)
	assert.Equal(t, 3, total, "a hidden parent must not hide its child or consume the cap")
	for _, invalid := range []struct{ root, depth, maximum int }{{0, 0, 0}, {-1, 0, 0}, {10, -1, 0}, {10, 0, -1}} {
		pids, total = table.DescendantPIDs(invalid.root, invalid.depth, invalid.maximum, nil)
		assert.Empty(t, pids)
		assert.Zero(t, total)
	}
}

func TestNewProcessTableExcludesInvalidRecordsAndStopsAtCycles(t *testing.T) {
	t.Parallel()
	table := NewProcessTable([]ProcessRecord{{PID: 10, PPID: 1}, {PID: 11, PPID: 10}, {PID: 12, PPID: 11}, {PID: 10, PPID: 12}, {PID: 11, PPID: 12}, {PID: 0, PPID: 10}, {PID: -1, PPID: 10}, {PID: 13, PPID: -1}})
	pids, total := table.DescendantPIDs(10, 0, 0, nil)
	assert.Equal(t, []int{11, 12}, pids)
	assert.Equal(t, 2, total)
	assert.Len(t, table.Records(), 3)
	assert.Empty(t, NewProcessTable(nil).Records())
}

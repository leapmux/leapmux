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

// startTimes answers a start-time read from a fixture map. A PID that the map
// lacks reads as 1, so a case that is not about start times links every row.
func startTimes(times map[int32]int64) func(context.Context, *process.Process) (int64, error) {
	return func(_ context.Context, candidate *process.Process) (int64, error) {
		if start, listed := times[candidate.Pid]; listed {
			return start, nil
		}
		return 1, nil
	}
}

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
	}, startTimes(nil))
	require.ErrorIs(t, err, os.ErrPermission, "an unreadable parent is an ownership uncertainty")
	assert.ErrorContains(t, err, "300")
	require.NotNil(t, table)
	assert.ElementsMatch(t, []ProcessRecord{{PID: 100, PPID: 1, StartTime: 1}, {PID: 200, PPID: 100, StartTime: 1}}, table.Records())
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
	}, startTimes(nil))
	require.NoError(t, err)
	assert.Equal(t, []ProcessRecord{{PID: 100, PPID: 1, StartTime: 1}}, table.Records())
}

// Windows keeps the parent PID of a process whose parent ended, and it gives
// that PID to a new process. csrss.exe and winlogon.exe record smss.exe
// instances that ended at boot, so a process that later takes such a PID
// appears in the parent column above them.
func TestBuildProcessTableLinksNoChildThatStartedBeforeItsParent(t *testing.T) {
	t.Parallel()
	rows := []*process.Process{{Pid: 624}, {Pid: 700}, {Pid: 800}, {Pid: 900}}
	parents := map[int32]int32{624: 4, 700: 624, 800: 624, 900: 624}
	table, err := buildProcessTable(t.Context(), rows, func(_ context.Context, candidate *process.Process) (int32, error) {
		return parents[candidate.Pid], nil
	}, startTimes(map[int32]int64{624: 5000, 700: 100, 800: 6000, 900: 5000}))
	require.NoError(t, err)
	pids, total := table.DescendantPIDs(624, 0, 0, nil)
	assert.Equal(t, []int{800, 900}, pids, "700 started before 624, and 900 started in its millisecond")
	assert.Equal(t, 2, total)
	assert.Contains(t, table.Records(), ProcessRecord{PID: 700, PPID: 624, StartTime: 100}, "the row stays, without the false link")
}

// A parent PID with no process in the snapshot belongs to a process that ended.
// The next process that takes the PID did not start that child.
func TestBuildProcessTableLinksNoChildToAParentOutsideTheSnapshot(t *testing.T) {
	t.Parallel()
	rows := []*process.Process{{Pid: 700}}
	table, err := buildProcessTable(t.Context(), rows, func(context.Context, *process.Process) (int32, error) {
		return 624, nil
	}, startTimes(nil))
	require.NoError(t, err)
	pids, total := table.DescendantPIDs(624, 0, 0, nil)
	assert.Empty(t, pids)
	assert.Zero(t, total)
}

// An unreadable creation time must not hide possible work from an observational
// query. Destructive ownership still requires a verified live identity.
func TestBuildProcessTableReportsRowsWithUnreadableStartTimes(t *testing.T) {
	t.Parallel()
	rows := []*process.Process{{Pid: 100}, {Pid: 200}, {Pid: 300}, {Pid: 400}}
	parents := map[int32]int32{100: 1, 200: 100, 300: 200, 400: 100}
	table, err := buildProcessTable(t.Context(), rows, func(_ context.Context, candidate *process.Process) (int32, error) {
		return parents[candidate.Pid], nil
	}, func(_ context.Context, candidate *process.Process) (int64, error) {
		switch candidate.Pid {
		case 200:
			return 0, os.ErrPermission
		case 400:
			return 0, process.ErrorProcessNotRunning
		default:
			return 1, nil
		}
	})
	require.NoError(t, err)
	pids, total := table.DescendantPIDs(100, 0, 0, nil)
	assert.Equal(t, []int{200, 300}, pids)
	assert.Equal(t, 2, total)
	assert.ElementsMatch(t, []ProcessRecord{{PID: 100, PPID: 1, StartTime: 1}, {PID: 200, PPID: 100}, {PID: 300, PPID: 200, StartTime: 1}}, table.Records(),
		"a process that ended during the read leaves no row")
}

func TestBuildProcessTableStopsOnContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	table, err := buildProcessTable(ctx, []*process.Process{{Pid: 100}}, func(context.Context, *process.Process) (int32, error) {
		called = true
		return 1, nil
	}, startTimes(nil))
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, table)
	assert.False(t, called)
}

func TestBuildProcessTableAcceptsAnEmptySnapshot(t *testing.T) {
	t.Parallel()
	table, err := buildProcessTable(t.Context(), nil, func(context.Context, *process.Process) (int32, error) {
		t.Error("an empty snapshot must perform no parent read")
		return 0, nil
	}, func(context.Context, *process.Process) (int64, error) {
		t.Error("an empty snapshot must perform no start-time read")
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
	}, startTimes(nil))
	require.NoError(t, err, "a native missing record must not become a cleanup failure")
	assert.Empty(t, table.Records())
}

func TestBuildProcessTableRetainsAnIOErrorForALiveProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	table, err := buildProcessTable(t.Context(), []*process.Process{{Pid: int32(pid)}}, func(context.Context, *process.Process) (int32, error) {
		return 0, syscall.EIO
	}, startTimes(nil))
	require.ErrorIs(t, err, syscall.EIO)
	assert.ErrorContains(t, err, fmt.Sprint(pid))
	assert.Empty(t, table.Records())
}

func TestNewProcessTablePreservesTraversalDepthCapAndFiltering(t *testing.T) {
	t.Parallel()
	table := NewProcessTable([]ProcessRecord{{PID: 10, PPID: 1, StartTime: 1}, {PID: 11, PPID: 10, StartTime: 1}, {PID: 12, PPID: 11, StartTime: 1}, {PID: 13, PPID: 10, StartTime: 1}, {PID: 14, PPID: 13, StartTime: 1}})
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
	// Equal start times let a non-atomic snapshot close a loop.
	table := NewProcessTable([]ProcessRecord{{PID: 10, PPID: 12, StartTime: 1}, {PID: 11, PPID: 10, StartTime: 1}, {PID: 12, PPID: 11, StartTime: 1}, {PID: 0, PPID: 10, StartTime: 1}, {PID: -1, PPID: 10, StartTime: 1}, {PID: 13, PPID: -1, StartTime: 1}})
	pids, total := table.DescendantPIDs(10, 0, 0, nil)
	assert.Equal(t, []int{11, 12}, pids)
	assert.Equal(t, 2, total)
	assert.Len(t, table.Records(), 3)
	assert.Empty(t, NewProcessTable(nil).Records())
}

func TestNewProcessTableLinksNoChildThatStartedBeforeItsParent(t *testing.T) {
	t.Parallel()
	table := NewProcessTable([]ProcessRecord{
		{PID: 624, PPID: 4, StartTime: 5000},
		{PID: 700, PPID: 624, StartTime: 100},
		{PID: 800, PPID: 624, StartTime: 6000},
		{PID: 801, PPID: 700, StartTime: 7000},
		{PID: 900, PPID: 624, StartTime: 4000},
	})
	pids, total := table.DescendantPIDs(624, 0, 0, nil)
	assert.Equal(t, []int{800}, pids, "700 and 900 predate 624, so 700's child 801 is no descendant either")
	assert.Equal(t, 1, total)
	pids, _ = table.DescendantPIDs(700, 0, 0, nil)
	assert.Equal(t, []int{801}, pids, "700 keeps its own child")
}

func TestNewProcessTableReportsChildrenOfARootWithAnUnknownStartTime(t *testing.T) {
	t.Parallel()
	table := NewProcessTable([]ProcessRecord{{PID: 10, PPID: 1}, {PID: 11, PPID: 10, StartTime: 1000}})
	pids, total := table.DescendantPIDs(10, 0, 0, nil)
	assert.Equal(t, []int{11}, pids)
	assert.Equal(t, 1, total)
}

func TestNewProcessTableDerivesEdgesFromTheLastValidRowForEachPID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label   string
		last    ProcessRecord
		under10 []int
		under20 []int
	}{
		{label: "changed parent", last: ProcessRecord{PID: 11, PPID: 20, StartTime: 1200}, under20: []int{11}},
		{label: "earlier replacement", last: ProcessRecord{PID: 11, PPID: 10, StartTime: 900}},
		{label: "invalid replacement", last: ProcessRecord{PID: 11, PPID: -1, StartTime: 1200}, under10: []int{11}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			first := ProcessRecord{PID: 11, PPID: 10, StartTime: 1100}
			table := NewProcessTable([]ProcessRecord{{PID: 10, PPID: 1, StartTime: 1000}, {PID: 20, PPID: 1, StartTime: 1000}, first, tc.last})
			got10, _ := table.DescendantPIDs(10, 0, 0, nil)
			got20, _ := table.DescendantPIDs(20, 0, 0, nil)
			assert.Equal(t, tc.under10, got10)
			assert.Equal(t, tc.under20, got20)
			want := tc.last
			if want.PPID < 0 {
				want = first
			}
			assert.Contains(t, table.Records(), want)
			assert.Len(t, table.Records(), 3)
		})
	}
}

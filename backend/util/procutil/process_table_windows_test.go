//go:build windows

package procutil

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reusedPIDVerification reads the live system, except that a new process holds
// the PID of root. Windows gives the PID of an ended process to the next process
// that needs one, so a worker's agent can hold such a PID.
type reusedPIDVerification struct {
	native nativeProcessVerification
	root   ProcessIdentity
}

func (v reusedPIDVerification) identity(pid int) (ProcessIdentity, bool) {
	if pid == v.root.PID {
		return v.root, true
	}
	return v.native.identity(pid)
}

func (v reusedPIDVerification) parent(ctx context.Context, pid int) (int, error) {
	return v.native.parent(ctx, pid)
}

// Windows keeps the parent PID of a process whose parent ended: csrss.exe,
// wininit.exe and winlogon.exe record smss.exe instances that ended at boot.
// When an agent took such a PID, the process table listed those system
// processes as the agent's children, and the agent's cleanup killed them. The
// end of winlogon.exe ended the CI runner's session, and GitHub lost the runner.
func TestSnapshotGivesAProcessWithAReusedPIDNoEarlierProcess(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	processes, err := process.ProcessesWithContext(ctx)
	require.NoError(t, err)
	live := make(map[int32]struct{}, len(processes))
	for _, candidate := range processes {
		live[candidate.Pid] = struct{}{}
	}
	// orphans lists, for each recorded parent PID that no process holds, the
	// live processes that record it, with their names for the failure message.
	orphans := make(map[int32][]int32)
	names := make(map[int32]string)
	for _, candidate := range processes {
		parent, err := candidate.PpidWithContext(ctx)
		if err != nil || parent <= 0 {
			continue
		}
		if _, alive := live[parent]; !alive {
			orphans[parent] = append(orphans[parent], candidate.Pid)
			names[candidate.Pid], _ = candidate.NameWithContext(ctx)
		}
	}
	require.NotEmpty(t, orphans, "Windows always runs csrss.exe and winlogon.exe below an smss.exe that ended")

	now := time.Now().UnixMilli()
	reused := make([]*process.Process, 0, len(orphans))
	for parent := range orphans {
		reused = append(reused, &process.Process{Pid: parent})
	}
	table, err := buildProcessTable(ctx, append(processes, reused...), func(ctx context.Context, candidate *process.Process) (int32, error) {
		if _, isReused := orphans[candidate.Pid]; isReused {
			return int32(os.Getpid()), nil
		}
		return candidate.PpidWithContext(ctx)
	}, func(ctx context.Context, candidate *process.Process) (int64, error) {
		if _, isReused := orphans[candidate.Pid]; isReused {
			return now, nil
		}
		return candidate.CreateTimeWithContext(ctx)
	})
	if err != nil {
		t.Logf("the snapshot could not read every parent: %v", err)
	}
	require.NotNil(t, table)

	for parent, children := range orphans {
		described := make([]string, 0, len(children))
		for _, child := range children {
			described = append(described, fmt.Sprintf("%s(%d)", names[child], child))
		}
		pids, total := table.DescendantPIDs(int(parent), 0, 0, nil)
		assert.Emptyf(t, pids, "a new process with PID %d must not gain the earlier processes %v", parent, described)
		assert.Zerof(t, total, "a new process with PID %d must not gain the earlier processes %v", parent, described)

		// The ownership check must refuse them even from a table that lists
		// the edge, because it kills what it owns.
		raw := &ProcessTable{byPID: table.byPID, byParent: map[int][]int{int(parent): pidList(children)}}
		root := ProcessIdentity{PID: int(parent), StartTime: now}
		owned, err := raw.verifyForest(ctx, []ProcessIdentity{root}, reusedPIDVerification{native: nativeProcessVerification{table: raw}, root: root})
		if err != nil {
			t.Logf("the verification of PID %d could not read every process: %v", parent, err)
		}
		assert.Emptyf(t, owned, "a new process with PID %d must not own the earlier processes %v", parent, described)
	}
}

func pidList(pids []int32) []int {
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		out = append(out, int(pid))
	}
	return out
}

package terminal

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// maxReportedProcesses caps how many descendants one walk returns. A `make -j64`
// or a dev server with a worker pool trivially produces dozens, and a
// close-confirmation dialog listing two hundred rows tells the user less than
// one listing five. The walk still COUNTS everything it finds, so the caller can
// say "and N more".
//
// Worker policy, not a request field: a client-supplied cap is one more value to
// validate and to keep in step with what the dialog can render.
const maxReportedProcesses = 32

// ProcessInfo is one process found beneath a terminal's login shell.
type ProcessInfo struct {
	PID int32
	// Name is the executable name only. CAN BE EMPTY: on macOS a name longer
	// than 14 characters resolves through a second syscall that fails for
	// another user's process, and reporting the pid with no name beats dropping
	// a process the user is about to kill.
	Name string
}

// procSnapshot is one process's identity in a scan. The walk takes it through a
// seam so the traversal is testable without a process table.
type procSnapshot struct {
	pid  int32
	ppid int32
}

// snapshotProcesses reads the whole process table once and returns each entry's
// pid and parent pid, plus a name lookup for the survivors of the cap.
//
// ONE pass, deliberately. `process.Children()` re-runs a full scan per node on
// every platform, so a BFS built from it costs depth x (full scan) for an
// identical answer; and on Windows it is also less correct, because a *Process
// that did not come from Processes() resolves its name through OpenProcess,
// which returns ACCESS_DENIED for elevated processes -- exactly the ones a user
// most wants named.
//
// Names are resolved separately, and only for the processes that survive the
// cap, which skips a syscall for every process on the machine outside this
// tab's subtree -- typically almost all of them.
//
// process.Status() is never called: on macOS it forks /bin/ps once per process,
// which would turn one tab close into a subprocess storm.
func snapshotProcesses(ctx context.Context) ([]procSnapshot, func(int32) string, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	byPID := make(map[int32]*process.Process, len(procs))
	out := make([]procSnapshot, 0, len(procs))
	for _, p := range procs {
		// gopsutil ignores ctx for these calls on Linux and macOS -- they are
		// plain file reads and sysctls with no cancellation plumbing -- so a
		// deadline bounds only THIS loop, and only because it is checked here.
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		ppid, err := p.PpidWithContext(ctx)
		if err != nil {
			// One unreadable process never aborts the walk: a process can exit
			// between the listing and this read, and another user's may refuse
			// the read outright.
			continue
		}
		byPID[p.Pid] = p
		out = append(out, procSnapshot{pid: p.Pid, ppid: ppid})
	}
	nameOf := func(pid int32) string {
		p, ok := byPID[pid]
		if !ok {
			return ""
		}
		name, err := p.NameWithContext(ctx)
		if err != nil {
			return ""
		}
		return name
	}
	return out, nameOf, nil
}

// descendantsOf walks the process tree below root, excluding root itself.
func descendantsOf(ctx context.Context, root int, limit int) ([]ProcessInfo, int, error) {
	snap, nameOf, err := snapshotProcesses(ctx)
	if err != nil {
		return nil, 0, err
	}
	pids, total := descendantPIDs(snap, int32(root), limit)
	out := make([]ProcessInfo, 0, len(pids))
	for _, pid := range pids {
		out = append(out, ProcessInfo{PID: pid, Name: nameOf(pid)})
	}
	return out, total, nil
}

// descendantPIDs is the pure traversal: breadth-first from root over a parent
// index built from snap, returning at most limit pids and the total found.
//
// Breadth-first so the shell's OWN children lead. That ordering is what makes
// the limit useful -- the processes a user recognises (`npm`, `make`) are the ones
// that survive it, not the leaf compiler invocations below them.
//
// The visited set makes the walk O(n) and cycle-proof unconditionally. The
// parent index is not an atomic snapshot of the machine, so a stale ppid
// pointing at a recycled pid can close a loop; that costs nothing to exclude
// here and hangs the worker if it is not.
func descendantPIDs(snap []procSnapshot, root int32, limit int) ([]int32, int) {
	byParent := make(map[int32][]int32, len(snap))
	for _, p := range snap {
		byParent[p.ppid] = append(byParent[p.ppid], p.pid)
	}
	visited := map[int32]struct{}{root: {}}
	var kept []int32
	total := 0
	queue := append([]int32(nil), byParent[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, seen := visited[pid]; seen {
			continue
		}
		visited[pid] = struct{}{}
		total++
		if limit <= 0 || len(kept) < limit {
			kept = append(kept, pid)
		}
		queue = append(queue, byParent[pid]...)
	}
	return kept, total
}

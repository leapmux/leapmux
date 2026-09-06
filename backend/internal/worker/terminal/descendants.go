package terminal

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// maxReportedProcesses caps how many descendants one walk returns. A `make -j64`
// or a dev server with a worker pool trivially produces dozens, and a
// close-confirmation dialog listing two hundred rows tells the user less than
// one listing five. The walk still counts every process it REPORTS, so the
// caller can say "and N more". It counts none that the report filter hides,
// because those are the shell's own work and no count of them means anything to
// the user.
//
// Worker policy, not a request field: a client-supplied cap is one more value to
// validate and to keep in step with what the dialog can render.
const maxReportedProcesses = 32

// ProcessInfo is one process found beneath a terminal's login shell.
type ProcessInfo struct {
	PID int32
	// Name is the executable name only. CAN BE EMPTY: on macOS a name longer
	// than 14 characters resolves through a second syscall, and that syscall
	// fails for another user's process. The pid alone is still worth reporting,
	// because the user is about to kill that process.
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
// so one tab close would fork /bin/ps once for every process on the machine.
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

// processScan is ONE read of the machine's process table, walked once per
// terminal in the request that took it.
//
// Shared rather than per terminal, because the read is the expensive part and
// the answer for eight terminals of a closing tile comes out of the same table.
// Per-terminal scanning cost one full pass per tab and could also disagree with
// itself: two passes taken milliseconds apart can show a process under one tab
// and not the other.
type processScan struct {
	// byParent indexes the table children-by-parent ONCE. The index is derived
	// from the same read every terminal in the request walks, so it belongs to
	// the request too: building it per terminal made the batch pay one full pass
	// over the process table per tab, which is the cost this type exists to pay
	// only once.
	byParent map[int32][]int32
	nameOf   func(int32) string
}

func newProcessScan(ctx context.Context) (*processScan, error) {
	snap, nameOf, err := snapshotProcesses(ctx)
	if err != nil {
		return nil, err
	}
	return &processScan{byParent: indexByParent(snap), nameOf: nameOf}, nil
}

// indexByParent groups a scan's entries by parent pid.
func indexByParent(snap []procSnapshot) map[int32][]int32 {
	byParent := make(map[int32][]int32, len(snap))
	for _, p := range snap {
		byParent[p.ppid] = append(byParent[p.ppid], p.pid)
	}
	return byParent
}

// descendantsOf walks the process tree below shellPID, excluding the shell and
// everything that belongs to the shell ITSELF rather than to the user's work.
func (s *processScan) descendantsOf(shellPID int, limit int) ([]ProcessInfo, int) {
	// The shell's own name comes out of the same table read, so asking costs
	// nothing. reportsAsWork needs it because the group filter only means
	// anything for a shell that implements job control.
	pids, total := descendantPIDs(s.byParent, int32(shellPID), limit,
		reportsAsWork(shellPID, s.nameOf(int32(shellPID))))
	out := make([]ProcessInfo, 0, len(pids))
	for _, pid := range pids {
		out = append(out, ProcessInfo{PID: pid, Name: s.nameOf(pid)})
	}
	return out, total
}

// reportsAsWork answers "did the USER start this, or did the shell".
//
// A shell puts every job it runs -- foreground or background -- in a process
// group of its own, and keeps its own bookkeeping in the group it already has.
// So a descendant whose process group is the shell's own IS the shell: a
// `precmd` hook, a completion helper, a prompt theme shelling out for git state.
// `mise activate zsh` installs exactly such a hook, and it forks before EVERY
// prompt, so without this the close guard warned about an idle terminal -- a
// two-click danger prompt in the browser, and a hard `tab_busy_refused` that
// fails a script in the CLI.
//
// It is deliberately NOT an allowlist of tool names. There is no end to the
// list, and a process group is what the shell itself uses to tell a job from
// its own work.
//
// FAILS TOWARD REPORTING, but only for a question the OS REFUSES to answer:
// Windows, which has no process group at all, or a read that fails for any
// reason other than "no such process". The guard exists to warn, so an
// unanswerable question must not silence it.
//
// A process that is GONE is a different answer, not an unanswerable one. The
// walk judges pids from a snapshot taken earlier, so a compiler that finished
// in between is simply absent now -- and reporting it names a dead pid in the
// dialog and refuses a CLI close over work that already stopped.
func reportsAsWork(shellPID int, shellName string) func(int32) bool {
	// The filter rests entirely on POSIX job control, and not every shell this
	// worker offers implements it. PowerShell starts a native command through
	// .NET's process API, which never calls setpgid, so every command the user
	// runs INHERITS pwsh's own group -- and comparing groups would then hide all
	// of it. That is the one direction this guard must never fail in, so a shell
	// whose job control we cannot vouch for reports everything, exactly like a
	// group the OS refuses to give up.
	//
	// A name we could not read is treated the same way, for the same reason.
	if shellName == "" || IsPwsh(ShellBaseName(shellName)) {
		return func(int32) bool { return true }
	}
	shellGroup, res := processGroupOf(shellPID)
	if res != processGroupFound {
		return func(int32) bool { return true }
	}
	return func(pid int32) bool {
		group, res := processGroupOf(int(pid))
		switch res {
		case processGroupFound:
			return group != shellGroup
		case processGroupGone:
			return false
		default:
			return true
		}
	}
}

// processGroupResult says how to read processGroupOf's answer. The three cases
// are genuinely different, and collapsing the last two reports dead processes as
// running work.
type processGroupResult int

const (
	// processGroupRefused means the OS will not answer. Windows has no process
	// group, and a Unix read can fail for a reason of its own.
	processGroupRefused processGroupResult = iota
	// processGroupGone means there is no such process any more.
	processGroupGone
	// processGroupFound means the returned group is the pid's own.
	processGroupFound
)

// descendantPIDs is the pure traversal: breadth-first from root over the parent
// index, returning at most limit pids and the total REPORTED.
//
// Breadth-first so the shell's OWN children lead. That ordering is what makes
// the limit useful -- the processes a user recognises (`npm`, `make`) are the ones
// that survive it, not the leaf compiler invocations below them.
//
// `report` filters what the walk REPORTS, never what it walks. A process the
// caller does not report can still have children that it does: the shell's own
// group holds the prompt hook, and anything that hook backgrounds gets a group
// of its own.
//
// The visited set makes the walk O(n) and cycle-proof unconditionally. The
// parent index is not an atomic snapshot of the machine, so a stale ppid
// pointing at a recycled pid can close a loop; that costs nothing to exclude
// here and hangs the worker if it is not.
func descendantPIDs(byParent map[int32][]int32, root int32, limit int, report func(int32) bool) ([]int32, int) {
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
		queue = append(queue, byParent[pid]...)
		if report != nil && !report(pid) {
			continue
		}
		total++
		if limit <= 0 || len(kept) < limit {
			kept = append(kept, pid)
		}
	}
	return kept, total
}

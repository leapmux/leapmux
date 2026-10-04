package terminal

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/util/procutil"
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

// processScan is ONE read of the machine's process table, walked once per
// terminal in the request that took it.
//
// Shared rather than per terminal, because the read is the expensive part and
// the answer for eight terminals of a closing tile comes out of the same table.
// Per-terminal scanning cost one full pass per tab and could also disagree with
// itself: two passes taken milliseconds apart can show a process under one tab
// and not the other.
type processScan struct {
	// table keeps one parent index for every terminal in the request.
	table  *procutil.ProcessTable
	nameOf func(int32) string
}

func newProcessScan(ctx context.Context) (*processScan, error) {
	table, err := procutil.SnapshotProcessTable(ctx)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, errors.Join(err, contextErr)
	}
	if table == nil {
		return nil, err
	}
	// The close dialog keeps readable rows when another user's parent data is unavailable.
	// Ownership cleanup uses the same table and retains its diagnostic separately.
	return &processScan{table: table, nameOf: func(pid int32) string { return table.Name(ctx, int(pid)) }}, nil
}

// descendantsOf walks the process tree below shellPID, excluding the shell and
// everything that belongs to the shell ITSELF rather than to the user's work.
func (s *processScan) descendantsOf(shellPID int, limit int) ([]ProcessInfo, int) {
	// The shell's own name comes out of the same table read, so asking costs
	// nothing. reportsAsWork needs it because the group filter only means
	// anything for a shell that implements job control.
	pids, total := descendantPIDs(s.table, int32(shellPID), limit,
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
func descendantPIDs(table *procutil.ProcessTable, root int32, limit int, report func(int32) bool) ([]int32, int) {
	if limit < 0 {
		limit = 0
	}
	var include func(int) bool
	if report != nil {
		include = func(pid int) bool { return report(int32(pid)) }
	}
	pids, total := table.DescendantPIDs(int(root), 0, limit, include)
	var kept []int32
	for _, pid := range pids {
		kept = append(kept, int32(pid))
	}
	return kept, total
}

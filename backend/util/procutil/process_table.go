package procutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/shirou/gopsutil/v4/process"
)

// ProcessRecord supplies one process and its parent from the same table snapshot.
type ProcessRecord struct {
	PID  int
	PPID int
	// StartTime is when the process started, in milliseconds since the Unix
	// epoch, as ProcessIdentity states it. Zero means that the snapshot could
	// not read it.
	StartTime int64
}

// ProcessTable indexes one OS snapshot. Names and identities remain live OS reads.
type ProcessTable struct {
	byPID    map[int]*process.Process
	parent   map[int]int
	start    map[int]int64
	byParent map[int][]int
}

func newProcessTable(size int) *ProcessTable {
	return &ProcessTable{
		byPID:    make(map[int]*process.Process, size),
		parent:   make(map[int]int, size),
		start:    make(map[int]int64, size),
		byParent: make(map[int][]int, size),
	}
}

// NewProcessTable indexes process records without granting ownership of any PID.
func NewProcessTable(records []ProcessRecord) *ProcessTable {
	table := newProcessTable(len(records))
	table.index(records)
	return table
}

// index records each valid row, and then links each row below its parent. A
// row links only to a parent that the same rows hold and that it could be a
// child of (see ProcessIdentity.couldBeChildOf). A parent PID that no row holds
// belongs to a process that ended, and a row without a start time cannot prove
// its place, so neither links.
func (t *ProcessTable) index(records []ProcessRecord) {
	valid := make([]ProcessRecord, 0, len(records))
	for _, record := range records {
		if record.PID <= 0 || record.PPID < 0 {
			continue
		}
		t.parent[record.PID] = record.PPID
		t.start[record.PID] = record.StartTime
		valid = append(valid, record)
	}
	for _, record := range valid {
		parentStart, listed := t.start[record.PPID]
		child := ProcessIdentity{PID: record.PID, StartTime: record.StartTime}
		if !listed || !child.couldBeChildOf(ProcessIdentity{PID: record.PPID, StartTime: parentStart}) {
			continue
		}
		t.byParent[record.PPID] = append(t.byParent[record.PPID], record.PID)
	}
}

func SnapshotProcessTable(ctx context.Context) (*ProcessTable, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return buildProcessTable(ctx, processes, func(ctx context.Context, candidate *process.Process) (int32, error) {
		return candidate.PpidWithContext(ctx)
	}, func(ctx context.Context, candidate *process.Process) (int64, error) {
		return candidate.CreateTimeWithContext(ctx)
	})
}

// buildProcessTable accepts readers for deterministic ownership failure tests.
func buildProcessTable(ctx context.Context, processes []*process.Process, readParent func(context.Context, *process.Process) (int32, error), readStart func(context.Context, *process.Process) (int64, error)) (*ProcessTable, error) {
	table := newProcessTable(len(processes))
	records := make([]ProcessRecord, 0, len(processes))
	var failures error
	for _, candidate := range processes {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(failures, err)
		}
		pid := int(candidate.Pid)
		parent, err := readParent(ctx, candidate)
		if err != nil {
			if !processGone(pid, err) {
				failures = errors.Join(failures, fmt.Errorf("read the parent of process %d: %w", candidate.Pid, err))
			}
			continue
		}
		// Windows refuses some system processes a start-time read. Such a row
		// stays without a start time, so it links to nothing, and it is no
		// ownership failure: the worker can read every process that it started.
		start, err := readStart(ctx, candidate)
		if err != nil {
			if processGone(pid, err) {
				continue
			}
			start = 0
		}
		table.byPID[pid] = candidate
		records = append(records, ProcessRecord{PID: pid, PPID: int(parent), StartTime: start})
	}
	table.index(records)
	return table, failures
}

func processGone(pid int, err error) bool {
	if errors.Is(err, process.ErrorProcessNotRunning) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return true
	}
	// Darwin returns EIO when a vanished PID supplies no sysctl process record.
	// Keep EIO for a live PID because its ownership data remains unreadable.
	return errors.Is(err, syscall.EIO) && !ProcessRuns(pid)
}

func (t *ProcessTable) Records() []ProcessRecord {
	if t == nil {
		return nil
	}
	records := make([]ProcessRecord, 0, len(t.parent))
	for pid, parent := range t.parent {
		records = append(records, ProcessRecord{PID: pid, PPID: parent, StartTime: t.start[pid]})
	}
	return records
}

func (t *ProcessTable) Name(ctx context.Context, pid int) string {
	if t == nil {
		return ""
	}
	candidate := t.byPID[pid]
	if candidate == nil {
		return ""
	}
	name, err := candidate.NameWithContext(ctx)
	if err != nil {
		return ""
	}
	return name
}

// DescendantPIDs visits each parent edge once. A zero depth or maximum means no limit.
func (t *ProcessTable) DescendantPIDs(root, depth, maximum int, include func(int) bool) ([]int, int) {
	if t == nil || root <= 0 || depth < 0 || maximum < 0 {
		return nil, 0
	}
	seen := map[int]struct{}{root: {}}
	generation := []int{root}
	var found []int
	total := 0
	for level := 1; len(generation) > 0 && (depth == 0 || level <= depth); level++ {
		var next []int
		for _, parent := range generation {
			for _, child := range t.byParent[parent] {
				if child <= 0 {
					continue
				}
				if _, visited := seen[child]; visited {
					continue
				}
				seen[child] = struct{}{}
				next = append(next, child)
				if include != nil && !include(child) {
					continue
				}
				total++
				if maximum == 0 || len(found) < maximum {
					found = append(found, child)
				}
			}
		}
		generation = next
	}
	return found, total
}

// VerifiedDescendants records a live child only while its captured parent identity still agrees.
func (t *ProcessTable) VerifiedDescendants(ctx context.Context, root ProcessIdentity) ([]ProcessIdentity, error) {
	if t == nil || root.IsZero() {
		return nil, errors.New("the process tree has no root identity")
	}
	return t.VerifiedForest(ctx, []ProcessIdentity{root})
}

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
}

// ProcessTable indexes one OS snapshot. Names and identities remain live OS reads.
type ProcessTable struct {
	byPID    map[int]*process.Process
	parent   map[int]int
	byParent map[int][]int
}

// NewProcessTable indexes process records without granting ownership of any PID.
func NewProcessTable(records []ProcessRecord) *ProcessTable {
	table := &ProcessTable{byPID: make(map[int]*process.Process), parent: make(map[int]int, len(records)), byParent: make(map[int][]int, len(records))}
	for _, record := range records {
		if record.PID <= 0 || record.PPID < 0 {
			continue
		}
		table.parent[record.PID] = record.PPID
		table.byParent[record.PPID] = append(table.byParent[record.PPID], record.PID)
	}
	return table
}

func SnapshotProcessTable(ctx context.Context) (*ProcessTable, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return buildProcessTable(ctx, processes, func(ctx context.Context, candidate *process.Process) (int32, error) {
		return candidate.PpidWithContext(ctx)
	})
}

// buildProcessTable accepts a reader for deterministic ownership failure tests.
func buildProcessTable(ctx context.Context, processes []*process.Process, readParent func(context.Context, *process.Process) (int32, error)) (*ProcessTable, error) {
	table := &ProcessTable{byPID: make(map[int]*process.Process, len(processes)), parent: make(map[int]int, len(processes)), byParent: make(map[int][]int, len(processes))}
	var failures error
	for _, candidate := range processes {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(failures, err)
		}
		parent, err := readParent(ctx, candidate)
		if err != nil {
			if !processGone(int(candidate.Pid), err) {
				failures = errors.Join(failures, fmt.Errorf("read the parent of process %d: %w", candidate.Pid, err))
			}
			continue
		}
		pid := int(candidate.Pid)
		table.byPID[pid] = candidate
		table.parent[pid] = int(parent)
		table.byParent[int(parent)] = append(table.byParent[int(parent)], pid)
	}
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
		records = append(records, ProcessRecord{PID: pid, PPID: parent})
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

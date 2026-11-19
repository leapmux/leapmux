package procutil

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

// SnapshotProcessTable reads Darwin's parent and creation records from one
// sysctl call. Live verification still checks every identity before signaling.
func SnapshotProcessTable(ctx context.Context) (*ProcessTable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := newProcessTable(len(entries))
	records := make([]ProcessRecord, 0, len(entries))
	for index := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := &entries[index]
		pid, parent := entry.Proc.P_pid, entry.Eproc.Ppid
		if pid <= 0 || parent < 0 {
			continue
		}
		start := entry.Proc.P_starttime
		records = append(records, ProcessRecord{PID: int(pid), PPID: int(parent), StartTime: start.Sec*1000 + int64(start.Usec)/1000})
		table.byPID[int(pid)] = &process.Process{Pid: pid}
	}
	table.index(records)
	return table, nil
}

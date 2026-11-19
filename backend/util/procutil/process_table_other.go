//go:build !darwin && !linux

package procutil

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// SnapshotProcessTable collects one host process inventory.
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

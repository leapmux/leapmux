package procutil

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	gopsutilcommon "github.com/shirou/gopsutil/v4/common"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/tklauser/go-sysconf"
)

func init() {
	// The host and process packages share gopsutil's cached boot time.
	host.EnableBootTimeCache(true)
}

var linuxClockTicks = sync.OnceValue(func() uint64 {
	ticks, err := sysconf.Sysconf(sysconf.SC_CLK_TCK)
	if err != nil || ticks <= 0 || ticks > math.MaxUint32 {
		return 100 // The process library uses this default if sysconf fails.
	}
	return uint64(ticks)
})

// SnapshotProcessTable reads the parent and creation time from the same stat
// record. The live ownership check still verifies both identities before use.
func SnapshotProcessTable(ctx context.Context) (*ProcessTable, error) {
	return snapshotLinuxProcessTable(ctx, os.ReadFile)
}

func snapshotLinuxProcessTable(ctx context.Context, readStat func(string) ([]byte, error)) (*ProcessTable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	env, _ := ctx.Value(gopsutilcommon.EnvKey).(gopsutilcommon.EnvMap)
	procRoot := cmp.Or(env[gopsutilcommon.HostProcEnvKey], os.Getenv(string(gopsutilcommon.HostProcEnvKey)), "/proc")
	// Like gopsutil's process identity reader, use relative times if the
	// boot time is unavailable. Relative times still establish birth order.
	bootSeconds, _ := host.BootTimeWithContext(ctx)
	ticksPerSecond := linuxClockTicks()
	table := newProcessTable(len(pids))
	records := make([]ProcessRecord, 0, len(pids))
	var failures error
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(failures, err)
		}
		if pid <= 0 {
			continue
		}
		data, err := readStat(filepath.Join(procRoot, strconv.Itoa(int(pid)), "stat"))
		if err != nil {
			if !processGone(int(pid), err) {
				failures = errors.Join(failures, fmt.Errorf("read the stat record of process %d: %w", pid, err))
			}
			continue
		}
		parentPID, startTicks, err := parseLinuxProcessStat(data)
		if err != nil {
			if len(data) != 0 || ProcessRuns(int(pid)) {
				failures = errors.Join(failures, fmt.Errorf("parse the stat record of process %d: %w", pid, err))
			}
			continue
		}
		startMillis, err := linuxProcessStartMillis(bootSeconds, startTicks, ticksPerSecond)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("read the start time of process %d: %w", pid, err))
		}
		table.byPID[int(pid)] = &process.Process{Pid: pid}
		records = append(records, ProcessRecord{PID: int(pid), PPID: parentPID, StartTime: startMillis})
	}
	table.index(records)
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(failures, err)
	}
	return table, failures
}

func parseLinuxProcessStat(data []byte) (int, uint64, error) {
	firstParen, lastParen := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if firstParen < 0 || lastParen <= firstParen {
		return 0, 0, errors.New("missing the process name")
	}
	if lastParen+1 >= len(data) || data[lastParen+1] != ' ' {
		return 0, 0, errors.New("missing the separator after the process name")
	}
	// The process name can contain spaces and parentheses. Fields after it
	// start with the process state at field 3 in /proc/PID/stat.
	fieldNumber := 3
	var parentPID int64
	for field := range bytes.FieldsSeq(data[lastParen+1:]) {
		switch fieldNumber {
		case 4:
			var err error
			parentPID, err = strconv.ParseInt(string(field), 10, 32)
			if err != nil {
				return 0, 0, fmt.Errorf("read the parent PID: %w", err)
			}
			if parentPID < 0 {
				return 0, 0, errors.New("the parent PID is negative")
			}
		case 22:
			startTicks, err := strconv.ParseUint(string(field), 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("read the process start ticks: %w", err)
			}
			return int(parentPID), startTicks, nil
		}
		fieldNumber++
	}
	return 0, 0, errors.New("missing the process start time")
}

func linuxProcessStartMillis(bootSeconds, startTicks, ticksPerSecond uint64) (int64, error) {
	const millisPerSecond = 1000
	const maxSeconds = math.MaxInt64 / millisPerSecond
	startSeconds := startTicks / ticksPerSecond
	if bootSeconds > maxSeconds || startSeconds > maxSeconds-bootSeconds {
		return 0, errors.New("the process start time exceeds the supported range")
	}
	millis := (bootSeconds+startSeconds)*millisPerSecond + startTicks%ticksPerSecond*millisPerSecond/ticksPerSecond
	if millis > math.MaxInt64 {
		return 0, errors.New("the process start time exceeds the supported range")
	}
	return int64(millis), nil
}

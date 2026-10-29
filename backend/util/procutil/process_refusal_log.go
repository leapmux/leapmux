package procutil

import (
	"context"
	"fmt"
	"os"
	"time"
)

// TEMPORARY(windows-ci-lost-runner): logRefusedChildren appends one line for
// each process that records an owned process as its parent but started before
// it. Before ProcessIdentity.couldBeChildOf, the owner adopted such a process
// and killed it. The Windows CI job sets LEAPMUX_TEMPORARY_REFUSED_CHILD_LOG,
// and its resource monitor publishes the file, so that a run shows what the old
// code would have killed.
func logRefusedChildren(table *ProcessTable, roots []ProcessIdentity) {
	path := os.Getenv("LEAPMUX_TEMPORARY_REFUSED_CHILD_LOG")
	if path == "" || table == nil {
		return
	}
	for _, root := range roots {
		for pid, parent := range table.parent {
			start := table.start[pid]
			if parent != root.PID || start <= 0 || start >= root.StartTime {
				continue
			}
			line := fmt.Sprintf("%s refused pid=%d name=%q start=%d recorded-parent=%d (%q) parent-start=%d test=%s\n",
				time.Now().UTC().Format(time.RFC3339Nano), pid, table.Name(context.Background(), pid), start,
				root.PID, table.Name(context.Background(), root.PID), root.StartTime, os.Args[0])
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return
			}
			_, _ = file.WriteString(line)
			_ = file.Close()
		}
	}
}

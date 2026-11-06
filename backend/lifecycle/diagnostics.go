package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// The automatic excerpt caps: relevant startup logs stay useful in a
// terminal, and never bury the operation error they accompany.
const (
	// maxExcerptLines bounds an automatic excerpt by lines.
	maxExcerptLines = 40
	// maxExcerptBytes bounds an automatic excerpt by size.
	maxExcerptBytes = 32 << 10
	// maxRetainedAttempts bounds how many COMPLETED attempts keep their
	// logs; the current attempt never counts against the bound.
	maxRetainedAttempts = 10
)

// AttemptLog opens one attempt's log file for writing, creating the
// logs directory durably. One log per process attempt; the attempt
// identity names it.
func AttemptLog(paths Paths, attemptID string) (*os.File, error) {
	if err := atomicfile.MkdirAllDurable(paths.Logs); err != nil {
		return nil, err
	}
	return os.OpenFile(paths.AttemptLogPath(attemptID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// ReadExcerpt returns the TAIL of the log at path, bounded to
// maxExcerptLines lines and maxExcerptBytes bytes. A missing log reads
// as an empty excerpt, not an error: the primary operation error must
// survive a diagnostics failure.
func ReadExcerpt(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return tailExcerpt(string(data)), nil
}

// tailExcerpt keeps the last lines: a startup failure's reason sits at
// the end of its log.
func tailExcerpt(log string) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if len(lines) > maxExcerptLines {
		lines = lines[len(lines)-maxExcerptLines:]
	}
	excerpt := strings.Join(lines, "\n")
	if len(excerpt) > maxExcerptBytes {
		excerpt = excerpt[len(excerpt)-maxExcerptBytes:]
	}
	return excerpt
}

// RotateAttemptLogs retires the logs of COMPLETED attempts beyond the
// retention bound, oldest first. The current attempt's log is never
// retired.
func RotateAttemptLogs(paths Paths, currentAttemptID string) error {
	entries, err := os.ReadDir(paths.Logs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	type attemptLog struct {
		path string
		mod  time.Time
	}
	var completed []attemptLog
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".log")
		if id == currentAttemptID {
			continue
		}
		path := filepath.Join(paths.Logs, e.Name())
		info, statErr := e.Info()
		if statErr != nil {
			return statErr
		}
		completed = append(completed, attemptLog{path: path, mod: info.ModTime()})
	}
	// Oldest modification first: the least recently completed attempt
	// retires before the rest.
	sort.Slice(completed, func(i, j int) bool { return completed[i].mod.Before(completed[j].mod) })
	for _, old := range completed[:max(0, len(completed)-(maxRetainedAttempts-1))] {
		if err := os.Remove(old.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("retire the attempt log %s: %w", old.path, err)
		}
	}
	return nil
}

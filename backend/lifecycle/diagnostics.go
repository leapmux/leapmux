package lifecycle

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/util/useronly"
)

// Automatic excerpts limit diagnostic output alongside the operation error.
const (
	// maxExcerptLines limits an automatic excerpt by lines.
	maxExcerptLines = 40
	// maxExcerptBytes limits an automatic excerpt by size.
	maxExcerptBytes = 32 << 10
	// maxRetainedAttempts limits retained completed attempts. The current
	// attempt does not count toward this maximum.
	maxRetainedAttempts = 10
	maxLogSegmentBytes  = 4 << 20
	maxLogSegments      = 3
)

// AttemptWriter keeps three private segments of at most 4 MiB for one attempt.
// The base path is always the newest segment; .1 and .2 are older segments.
type AttemptWriter struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	size   int64
	closed bool
	err    error
}

// AttemptLog opens the private rotating writer for one process attempt.
func AttemptLog(paths Paths, attemptID string) (*AttemptWriter, error) {
	if !validLogAttempt(attemptID) {
		return nil, errors.New("an attempt log requires a 32-character hexadecimal identity")
	}
	if err := atomicfile.MkdirAllPrivate(paths.Logs); err != nil {
		return nil, err
	}
	path := paths.AttemptLogPath(attemptID)
	file, err := openAttemptSegment(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &AttemptWriter{file: file, path: path, size: info.Size()}, nil
}

func validLogAttempt(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func openAttemptSegment(path string) (*os.File, error) {
	if err := useronly.VerifyFile(path); errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.WriteFileDurable(path, nil, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return useronly.OpenAppend(path)
}

func (w *AttemptWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	written := 0
	for len(data) > 0 {
		if w.size >= maxLogSegmentBytes {
			if err := w.rotate(); err != nil {
				w.err = err
				return written, err
			}
		}
		count := min(len(data), int(maxLogSegmentBytes-w.size))
		n, err := w.file.Write(data[:count])
		written += n
		w.size += int64(n)
		data = data[n:]
		if err == nil && n != count {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = err
			return written, err
		}
	}
	return written, nil
}

// WriteString writes through the same rotation and error handling as Write.
func (w *AttemptWriter) WriteString(value string) (int, error) { return w.Write([]byte(value)) }

func (w *AttemptWriter) rotate() error {
	if err := errors.Join(w.file.Sync(), w.file.Close()); err != nil {
		return err
	}
	w.file = nil
	for index := maxLogSegments - 1; index > 0; index-- {
		from := w.path
		if index > 1 {
			from += "." + strconv.Itoa(index-1)
		}
		to := w.path + "." + strconv.Itoa(index)
		if err := verifyExistingPrivateFile(to); err != nil {
			return err
		}
		if err := os.Remove(to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := useronly.VerifyFile(from); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	var err error
	w.file, err = openAttemptSegment(w.path)
	w.size = 0
	return err
}

// Close flushes the final segment and preserves any earlier write failure.
func (w *AttemptWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		if w.file != nil {
			w.err = errors.Join(w.err, w.file.Sync(), w.file.Close())
		}
	}
	return w.err
}

// ReadExcerpt reads at most 32 KiB from the newest available log segments.
// It returns at most 40 lines.
// Missing segments are harmless; callers preserve their original operation error.
func ReadExcerpt(path string) (string, error) {
	var tail []byte
	for index := 0; index < maxLogSegments && len(tail) < maxExcerptBytes; index++ {
		segment := path
		if index > 0 {
			segment += "." + strconv.Itoa(index)
		}
		data, err := readSegmentTail(segment, maxExcerptBytes-len(tail))
		if err != nil {
			return "", err
		}
		tail = append(data, tail...)
	}
	return tailExcerpt(string(tail)), nil
}

func readSegmentTail(path string, limit int) ([]byte, error) {
	file, err := useronly.OpenFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	count := min(info.Size(), int64(limit))
	return io.ReadAll(io.NewSectionReader(file, info.Size()-count, count))
}

// tailExcerpt keeps the last lines, where the startup failure is reported.
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

// RotateAttemptLogs keeps at most ten completed attempts plus a current attempt.
// All segments of a retired attempt are removed together.
func RotateAttemptLogs(paths Paths, currentAttemptID string) error {
	entries, err := os.ReadDir(paths.Logs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type attemptLog struct {
		files []string
		mod   time.Time
	}
	groups := make(map[string]*attemptLog)
	for _, entry := range entries {
		id, suffix, ok := strings.Cut(entry.Name(), ".log")
		if entry.IsDir() || !ok || !validLogAttempt(id) || id == currentAttemptID ||
			(suffix != "" && suffix != ".1" && suffix != ".2") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		group := groups[id]
		if group == nil {
			group = &attemptLog{}
			groups[id] = group
		}
		group.files = append(group.files, filepath.Join(paths.Logs, entry.Name()))
		if info.ModTime().After(group.mod) {
			group.mod = info.ModTime()
		}
	}
	completed := make([]*attemptLog, 0, len(groups))
	for _, group := range groups {
		completed = append(completed, group)
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].mod.Before(completed[j].mod) })
	var failures []error
	for _, old := range completed[:max(0, len(completed)-maxRetainedAttempts)] {
		for _, path := range old.files {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("retire the attempt log %s: %w", path, err))
			}
		}
	}
	return errors.Join(failures...)
}

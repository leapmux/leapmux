package gemini

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/quartz"
)

// geminiDebugLogEnv gives Gemini CLI the file to which its debug logger
// appends every message, as `[<ISO time>] [<LEVEL>] <message>` lines
// (DebugLogger of chunk-DKZARV6U.js). Gemini CLI 0.65.0 writes its retry
// warnings nowhere else in time: in ACP mode, its patchStdio routes every
// stderr write into an in-process event that nothing reads before exit, so
// stderr holds the warning until the process ends.
const geminiDebugLogEnv = "GEMINI_DEBUG_LOG_FILE"

const (
	// geminiDebugLogPollInterval is how often the follower reads the lines
	// that Gemini appended. A long retry wait lasts hours, so a second of
	// delay before the stop costs nothing.
	geminiDebugLogPollInterval = time.Second
	// geminiDebugLogMaxReadBytes caps the bytes read in one poll. The writer keeps
	// its file open, so truncating an active log could discard an unread append.
	geminiDebugLogMaxReadBytes = 4 << 20
	// geminiDebugLogMaxLine caps one line that the follower delivers. A line
	// that grows past it without an end is dropped.
	geminiDebugLogMaxLine = 64 << 10
)

// geminiDebugLog is the debug log of one Gemini CLI process: a file in a
// private directory that the agent creates before the process starts and
// removes when the process ends.
type geminiDebugLog struct {
	dir  string
	path string

	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
	done   chan struct{}
	readMu sync.Mutex
	offset int64
	lines  geminiDebugLineReader
	epoch  uint64
}

type geminiDebugLogLine struct {
	text  string
	epoch uint64
}

// newGeminiDebugLog creates the private directory of one debug log.
func newGeminiDebugLog() (*geminiDebugLog, error) {
	dir, err := os.MkdirTemp("", "leapmux-gemini-")
	if err != nil {
		return nil, fmt.Errorf("create the Gemini debug log directory: %w", err)
	}
	return &geminiDebugLog{dir: dir, path: filepath.Join(dir, "debug.log")}, nil
}

// env is the environment entry that points Gemini CLI at the log.
func (l *geminiDebugLog) env() string {
	return geminiDebugLogEnv + "=" + l.path
}

// follow starts to read each line that Gemini appends to the log, every
// geminiDebugLogPollInterval of clock, and passes it to observe without its
// line ending. observe runs on the follower's goroutine. A line reaches
// observe once Gemini ends it. follow does nothing after close or a second
// time.
func (l *geminiDebugLog) follow(clock quartz.Clock, observe func(geminiDebugLogLine)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	l.cancel, l.done = cancel, done
	ticker := clock.NewTicker(geminiDebugLogPollInterval, "gemini", "debug-log")
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.poll(observe)
			}
		}
	}()
}

// poll reads a snapshot of the appended bytes in fixed-size chunks. Delivery
// runs after readMu releases, so an observer can reset the idle log safely.
func (l *geminiDebugLog) poll(observe func(geminiDebugLogLine)) {
	lines := l.readLines()
	for _, line := range lines {
		observe(line)
	}
}

func (l *geminiDebugLog) readLines() []geminiDebugLogLine {
	l.readMu.Lock()
	defer l.readMu.Unlock()
	file, err := os.Open(l.path)
	if err != nil {
		// Gemini creates the file at its first message.
		return nil
	}
	// The file is open for reading only, so its close states nothing.
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if info.Size() < l.offset {
		l.offset, l.lines = 0, geminiDebugLineReader{}
		l.epoch++
	}
	if _, err := file.Seek(l.offset, io.SeekStart); err != nil {
		return nil
	}
	remaining := min(info.Size()-l.offset, int64(geminiDebugLogMaxReadBytes))
	var lines []geminiDebugLogLine
	var buffer [32 << 10]byte
	for remaining > 0 {
		count, err := file.Read(buffer[:min(int64(len(buffer)), remaining)])
		if count > 0 {
			l.offset += int64(count)
			remaining -= int64(count)
			l.lines.feed(buffer[:count], func(line string) { lines = append(lines, geminiDebugLogLine{text: line, epoch: l.epoch}) })
		}
		if err != nil || count == 0 {
			break
		}
	}
	return lines
}

// preparePrompt runs before native dispatch. It discards idle output and advances the delivery
// epoch, so an already collected batch cannot acquire the next prompt.
func (l *geminiDebugLog) preparePrompt() uint64 {
	l.readMu.Lock()
	defer l.readMu.Unlock()
	l.offset, l.lines = 0, geminiDebugLineReader{}
	l.epoch++
	if err := os.Truncate(l.path, 0); err != nil && !os.IsNotExist(err) {
		// A failed trim must not replay the previous prompt's unread bytes.
		if info, statErr := os.Stat(l.path); statErr == nil {
			l.offset = info.Size()
		}
		slog.Warn("empty the idle Gemini debug log", "path", l.path, "error", err)
	}
	return l.epoch
}

type geminiDebugLineReader struct {
	pending    []byte
	discarding bool
}

// feed retains at most one limited unfinished line. Once a line exceeds the
// limit, its entire suffix stays discarded through the next newline.
func (reader *geminiDebugLineReader) feed(data []byte, observe func(string)) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		segment := data
		if end >= 0 {
			segment = data[:end]
		}
		if !reader.discarding {
			if len(reader.pending)+len(segment) > geminiDebugLogMaxLine {
				reader.pending, reader.discarding = nil, true
			} else {
				reader.pending = append(reader.pending, segment...)
			}
		}
		if end < 0 {
			return
		}
		if !reader.discarding {
			observe(string(bytes.TrimSuffix(reader.pending, []byte{'\r'})))
		}
		reader.pending, reader.discarding = reader.pending[:0], false
		data = data[end+1:]
	}
}

// close stops the follower, waits for its last delivery, and removes the
// log's directory. A second call does nothing.
func (l *geminiDebugLog) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	cancel, done := l.cancel, l.done
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if err := os.RemoveAll(l.dir); err != nil {
		slog.Warn("remove the Gemini debug log directory", "dir", l.dir, "error", err)
	}
}

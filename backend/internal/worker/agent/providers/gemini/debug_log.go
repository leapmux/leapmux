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
	// geminiDebugLogMaxSize caps the debug log. The follower empties the file
	// once it has read this much, so the log of a long session takes no disk
	// space beyond the cap.
	geminiDebugLogMaxSize = 4 << 20
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
func (l *geminiDebugLog) follow(clock quartz.Clock, observe func(line string)) {
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
		var offset int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				offset = l.readFrom(offset, observe)
			}
		}
	}()
}

// readFrom delivers the complete lines after offset and returns the offset
// after the last of them. An unfinished line waits for the next read. Once the
// follower has read geminiDebugLogMaxSize, it empties the file and reads it
// from its start again. Gemini appends to the file, so its next line lands at
// the new end. A line that Gemini appends between the read and the emptying
// is lost.
func (l *geminiDebugLog) readFrom(offset int64, observe func(line string)) int64 {
	file, err := os.Open(l.path)
	if err != nil {
		// Gemini creates the file at its first message.
		return offset
	}
	// The file is open for reading only, so its close states nothing.
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil {
		return offset
	} else if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	// One poll interval of debug output is small, so the read takes the rest of
	// the file. An emptying then loses no line that Gemini wrote before it.
	data, err := io.ReadAll(file)
	if err != nil {
		return offset
	}
	offset += int64(deliverGeminiDebugLogLines(data, observe))
	if offset >= geminiDebugLogMaxSize {
		if err := os.Truncate(l.path, 0); err != nil {
			slog.Warn("empty the Gemini debug log", "path", l.path, "error", err)
			return offset
		}
		offset = 0
	}
	return offset
}

// deliverGeminiDebugLogLines passes each complete line of data to observe, and
// returns how many bytes it consumed. It keeps an unfinished line for the next
// read, unless the line already passed geminiDebugLogMaxLine, which it drops.
func deliverGeminiDebugLogLines(data []byte, observe func(line string)) int {
	consumed := 0
	for {
		end := bytes.IndexByte(data[consumed:], '\n')
		if end < 0 {
			break
		}
		if line := data[consumed : consumed+end]; len(line) <= geminiDebugLogMaxLine {
			observe(string(bytes.TrimSuffix(line, []byte{'\r'})))
		}
		consumed += end + 1
	}
	if len(data)-consumed > geminiDebugLogMaxLine {
		consumed = len(data)
	}
	return consumed
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

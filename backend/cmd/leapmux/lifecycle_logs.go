package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/lifecycle"
)

const (
	defaultLogsLines   = 100
	logsPollInterval   = 250 * time.Millisecond
	logTailChunkSize   = 64 * 1024
	maxTailCapHint     = 1024
	logsFollowTimerTag = "lifecycle-logs-follow"
)

type logLineEvent struct {
	Path string `json:"path"`
	Line string `json:"line"`
}

// runLifecycleLogs reads the current attempt's log file for a daemon mode,
// printing the last N lines and optionally following new output until ctx ends.
func runLifecycleLogs(ctx context.Context, printer *cli.Printer, line lifecycleCommandLine) error {
	configuration, options, err := line.load()
	if err != nil {
		return err
	}
	if options.logLines < 0 {
		return cli.Usagef("--lines must not be negative")
	}
	status, obsErr := lifecycle.Observe(ctx, configuration.dataDirectory)
	if obsErr != nil {
		return fmt.Errorf("observe %s state in %s: %w", line.mode(), configuration.dataDirectory, obsErr)
	}
	if status.Attempt.LogPath == nil || *status.Attempt.LogPath == "" {
		return cli.Newf(cli.CodeNotFound, "no log file found for %s in %s", line.mode(), configuration.dataDirectory).
			WithHint("start the instance first with \"leapmux daemon %s start\"", line.mode())
	}
	logPath := *status.Attempt.LogPath
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cli.Newf(cli.CodeNotFound, "log file %s does not exist", logPath)
		}
		return fmt.Errorf("open log file %s: %w", logPath, err)
	}

	tailLines, err := readLastLines(f, options.logLines)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("read log file %s: %w", logPath, err)
	}
	emitLine := func(path, text string) error {
		if printer.Format == cli.FormatJSON || printer.Format == cli.FormatNDJSON || printer.Format == cli.FormatTSV {
			return printer.Event(logLineEvent{Path: path, Line: strings.TrimRight(text, "\r\n")})
		}
		if strings.HasSuffix(text, "\n") {
			_, err := io.WriteString(printer.Out, text)
			return err
		}
		_, err := fmt.Fprintln(printer.Out, text)
		return err
	}

	if printer.Format == cli.FormatJSON && !options.logFollow {
		_ = f.Close()
		return printer.Result(struct {
			Mode  string   `json:"mode"`
			Path  string   `json:"path"`
			Lines []string `json:"lines"`
		}{
			Mode:  line.mode(),
			Path:  logPath,
			Lines: tailLines,
		})
	}
	for _, l := range tailLines {
		if err := emitLine(logPath, l); err != nil {
			_ = f.Close()
			return err
		}
	}
	if !options.logFollow {
		_ = f.Close()
		return nil
	}

	follower := newLogFollower(f, logPath, lifecycle.PathsOf(configuration.dataDirectory), quartz.NewReal(), emitLine)
	defer func() { _ = follower.close() }()
	return follower.follow(ctx)
}

// logFollower streams appended lines from an attempt log file, handling
// partial-line buffering across poll ticks, file rotation, truncation, and
// attempt rollover when runtime.yaml points at a new attempt.
type logFollower struct {
	clock        quartz.Clock
	paths        lifecycle.Paths
	file         *os.File
	logPath      string
	openInfo     os.FileInfo
	runtimeStat  os.FileInfo
	offset       int64
	reader       *bufio.Reader
	partial      strings.Builder
	emit         func(path, text string) error
	onAfterDrain func()
}

func newLogFollower(f *os.File, logPath string, paths lifecycle.Paths, clock quartz.Clock, emit func(path, text string) error) *logFollower {
	if clock == nil {
		clock = quartz.NewReal()
	}
	runtimeStat, _ := os.Stat(paths.RuntimeRecord)
	openInfo, _ := f.Stat()
	offset, _ := f.Seek(0, io.SeekCurrent)
	return &logFollower{
		clock:       clock,
		paths:       paths,
		file:        f,
		logPath:     logPath,
		openInfo:    openInfo,
		runtimeStat: runtimeStat,
		offset:      offset,
		reader:      bufio.NewReader(f),
		emit:        emit,
	}
}

func (lf *logFollower) close() error {
	if lf.file == nil {
		return nil
	}
	err := lf.file.Close()
	lf.file = nil
	return err
}

func (lf *logFollower) flushPartial() error {
	if lf.partial.Len() == 0 {
		return nil
	}
	text := lf.partial.String()
	lf.partial.Reset()
	return lf.emit(lf.logPath, text)
}

func (lf *logFollower) drain() error {
	for {
		chunk, readErr := lf.reader.ReadString('\n')
		if len(chunk) > 0 {
			lf.offset += int64(len(chunk))
			if strings.HasSuffix(chunk, "\n") {
				if lf.partial.Len() > 0 {
					lf.partial.WriteString(chunk)
					chunk = lf.partial.String()
					lf.partial.Reset()
				}
				if err := lf.emit(lf.logPath, chunk); err != nil {
					return err
				}
			} else {
				lf.partial.WriteString(chunk)
			}
			if readErr == nil {
				continue
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("follow log file %s: %w", lf.logPath, readErr)
		}
		return nil
	}
}

func (lf *logFollower) switchFile(nf *os.File, nextPath string, nextInfo os.FileInfo) error {
	if err := lf.flushPartial(); err != nil {
		_ = nf.Close()
		return err
	}
	if lf.file != nil {
		_ = lf.file.Close()
	}
	lf.file = nf
	lf.logPath = nextPath
	if nextInfo == nil {
		nextInfo, _ = nf.Stat()
	}
	lf.openInfo = nextInfo
	lf.offset = 0
	lf.reader.Reset(nf)
	return nil
}

func (lf *logFollower) poll() error {
	// Check whether runtime.yaml changed before reading it, avoiding
	// lock probes and HTTP status RPCs during idle polling.
	if lf.paths.RuntimeRecord != "" {
		if curRT, serr := os.Stat(lf.paths.RuntimeRecord); serr == nil && fileInfoChanged(lf.runtimeStat, curRT) {
			lf.runtimeStat = curRT
			if rec, rerr := lifecycle.ReadRuntimeRecord(lf.paths.RuntimeRecord); rerr == nil && rec.AttemptID != "" {
				nextPath := lf.paths.AttemptLogPath(rec.AttemptID)
				if nextPath != "" && nextPath != lf.logPath {
					if nf, oerr := os.Open(nextPath); oerr == nil {
						return lf.switchFile(nf, nextPath, nil)
					}
				}
			}
		}
	}
	curInfo, serr := os.Stat(lf.logPath)
	if serr != nil {
		return nil
	}
	rotated := lf.openInfo != nil && !os.SameFile(lf.openInfo, curInfo)
	truncated := curInfo.Size() < lf.offset
	if rotated {
		if nf, oerr := os.Open(lf.logPath); oerr == nil {
			return lf.switchFile(nf, lf.logPath, curInfo)
		}
		return nil
	}
	if truncated {
		if err := lf.flushPartial(); err != nil {
			return err
		}
		if _, seekErr := lf.file.Seek(0, io.SeekStart); seekErr == nil {
			lf.offset = 0
			lf.reader.Reset(lf.file)
		}
	}
	return nil
}

func (lf *logFollower) follow(ctx context.Context) error {
	ticker := lf.clock.NewTicker(logsPollInterval, logsFollowTimerTag)
	defer ticker.Stop()
	for {
		if err := lf.drain(); err != nil {
			return err
		}
		if lf.onAfterDrain != nil {
			lf.onAfterDrain()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := lf.poll(); err != nil {
				return err
			}
		}
	}
}

func fileInfoChanged(prev, cur os.FileInfo) bool {
	if prev == nil {
		return true
	}
	return !os.SameFile(prev, cur) || prev.Size() != cur.Size() || !prev.ModTime().Equal(cur.ModTime())
}

// readLastLines reads the final n lines from r (or all lines when n == 0)
// without a fixed scanner token-size limit, leaving r positioned at EOF.
// When n > 0 it seeks backward from EOF in fixed chunks so large files are
// not scanned from byte 0.
func readLastLines(r io.ReadSeeker, n int) ([]string, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return []string{}, nil
	}

	startOffset := int64(0)
	capHint := 0
	if n > 0 {
		capHint = min(n, maxTailCapHint)
		buf := make([]byte, logTailChunkSize)
		pos := size
		newlines := 0
	scan:
		for pos > 0 {
			readSize := min(pos, int64(logTailChunkSize))
			pos -= readSize
			if _, err := r.Seek(pos, io.SeekStart); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(r, buf[:readSize]); err != nil {
				return nil, err
			}
			for i := readSize - 1; i >= 0; i-- {
				// Ignore a single trailing newline at EOF because it ends the
				// last line rather than starting an empty line after it.
				if pos+i == size-1 && buf[i] == '\n' {
					continue
				}
				if buf[i] == '\n' {
					newlines++
					if newlines == n {
						startOffset = pos + i + 1
						break scan
					}
				}
			}
		}
	}

	if _, err := r.Seek(startOffset, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(r, logTailChunkSize)
	lines := make([]string, 0, capHint)
	for {
		line, readErr := br.ReadString('\n')
		if len(line) > 0 {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if errors.Is(readErr, io.EOF) {
			return lines, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

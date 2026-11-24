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

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/lifecycle"
)

const (
	defaultLogsLines = 100
	logsPollInterval = 250 * time.Millisecond
)

// runLifecycleLogs reads the current attempt's log file for a daemon mode,
// printing the last N lines and optionally following new output until ctx ends.
func runLifecycleLogs(ctx context.Context, printer *cli.Printer, line lifecycleCommandLine, _ modeDependencies) error {
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
	defer func() { _ = f.Close() }()

	tailLines, err := readLastLines(f, options.logLines)
	if err != nil {
		return fmt.Errorf("read log file %s: %w", logPath, err)
	}
	if printer.Format == cli.FormatJSON || printer.Format == cli.FormatNDJSON {
		if !options.logFollow {
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
			if err := printer.Event(struct {
				Path string `json:"path"`
				Line string `json:"line"`
			}{Path: logPath, Line: l}); err != nil {
				return err
			}
		}
	} else {
		for _, l := range tailLines {
			if _, err := fmt.Fprintln(printer.Out, l); err != nil {
				return err
			}
		}
	}
	if !options.logFollow {
		return nil
	}

	openInfo, _ := f.Stat()
	offset, _ := f.Seek(0, io.SeekCurrent)
	reader := bufio.NewReader(f)
	ticker := time.NewTicker(logsPollInterval)
	defer ticker.Stop()
	for {
		chunk, readErr := reader.ReadString('\n')
		if len(chunk) > 0 {
			offset += int64(len(chunk))
			if printer.Format == cli.FormatJSON || printer.Format == cli.FormatNDJSON {
				if err := printer.Event(struct {
					Path string `json:"path"`
					Line string `json:"line"`
				}{Path: logPath, Line: strings.TrimRight(chunk, "\r\n")}); err != nil {
					return err
				}
			} else if _, err := io.WriteString(printer.Out, chunk); err != nil {
				return err
			}
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("follow log file %s: %w", logPath, readErr)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Reopen if daemon restarted to a new attempt log, rotated inode, or truncated file.
			if st, serr := lifecycle.Observe(ctx, configuration.dataDirectory); serr == nil &&
				st.Attempt.LogPath != nil && *st.Attempt.LogPath != "" && *st.Attempt.LogPath != logPath {
				if nf, oerr := os.Open(*st.Attempt.LogPath); oerr == nil {
					_ = f.Close()
					f = nf
					logPath = *st.Attempt.LogPath
					openInfo, _ = f.Stat()
					offset = 0
					reader.Reset(f)
					continue
				}
			}
			if curInfo, serr := os.Stat(logPath); serr == nil {
				rotated := openInfo != nil && !os.SameFile(openInfo, curInfo)
				truncated := curInfo.Size() < offset
				if rotated {
					if nf, oerr := os.Open(logPath); oerr == nil {
						_ = f.Close()
						f = nf
						openInfo = curInfo
						offset = 0
						reader.Reset(f)
					}
				} else if truncated {
					if _, seekErr := f.Seek(0, io.SeekStart); seekErr == nil {
						offset = 0
						reader.Reset(f)
					}
				}
			}
		}
	}
}

// readLastLines reads the final n lines from r (or all lines when n == 0)
// without a fixed scanner token-size limit, leaving r positioned at EOF.
func readLastLines(r io.Reader, n int) ([]string, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	if n == 0 {
		all := make([]string, 0)
		for {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				all = append(all, strings.TrimRight(line, "\r\n"))
			}
			if errors.Is(err, io.EOF) {
				return all, nil
			}
			if err != nil {
				return nil, err
			}
		}
	}
	ring := make([]string, 0, n)
	pos := 0
	full := false
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if !full {
				ring = append(ring, trimmed)
				if len(ring) == n {
					full = true
				}
			} else {
				ring[pos] = trimmed
				pos = (pos + 1) % n
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if !full {
		return ring, nil
	}
	out := make([]string, 0, n)
	out = append(out, ring[pos:]...)
	out = append(out, ring[:pos]...)
	return out, nil
}

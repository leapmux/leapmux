package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/filelock"
)

const eventCheckpointVersion = 1
const maxEventCheckpointBytes = 8 << 10
const eventCheckpointLockDirectory = ".leapmux-event-locks"

type eventCheckpoint struct {
	Version     int      `json:"version"`
	WorkerID    string   `json:"worker_id"`
	TabType     string   `json:"tab_type"`
	TabID       string   `json:"tab_id"`
	JournalID   string   `json:"journal_id"`
	TabStreamID string   `json:"tab_stream_id"`
	Filter      []string `json:"kinds"`
	Sequence    int64    `json:"sequence"`
	Cursor      []byte   `json:"cursor"`
}

type eventCheckpointFile struct {
	path string
	lock *os.File
}

func openEventCheckpoint(path string) (*eventCheckpointFile, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "resolve checkpoint path")
	}
	// Resolve the parent so two paths through different directory symlinks
	// contend for the same lock. The checkpoint itself must be a regular file.
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "resolve checkpoint directory")
	}
	path = filepath.Join(dir, filepath.Base(path))
	if strings.EqualFold(filepath.Base(dir), eventCheckpointLockDirectory) ||
		strings.EqualFold(filepath.Base(path), eventCheckpointLockDirectory) {
		return nil, cli.Usagef("choose a checkpoint path outside the event lock directory")
	}
	lockDir := filepath.Join(dir, eventCheckpointLockDirectory)
	if err := os.Mkdir(lockDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, cli.Wrap(cli.CodeUsage, err, "create checkpoint lock directory")
	}
	lockInfo, err := os.Lstat(lockDir)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "inspect checkpoint lock directory")
	}
	if !lockInfo.IsDir() {
		return nil, cli.Usagef("the event lock path must be a directory")
	}
	lock, err := filelock.Open(filepath.Join(lockDir, filepath.Base(path)), false)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "open checkpoint lock")
	}
	held, err := filelock.TryLock(lock)
	if err != nil {
		_ = lock.Close()
		return nil, cli.Wrap(cli.CodeInternal, err, "lock event checkpoint")
	}
	if !held {
		_ = lock.Close()
		return nil, cli.Newf(cli.CodeConflict, "another events process is using this checkpoint")
	}
	return &eventCheckpointFile{path: path, lock: lock}, nil
}

func (f *eventCheckpointFile) close() { _ = f.lock.Close() }

func (f *eventCheckpointFile) read() (*eventCheckpoint, error) {
	info, err := os.Lstat(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "read event checkpoint")
	}
	if !info.Mode().IsRegular() || info.Size() > maxEventCheckpointBytes {
		return nil, cli.Usagef("the event checkpoint must be a regular file no larger than %d bytes", maxEventCheckpointBytes)
	}
	file, err := os.Open(f.path)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "open event checkpoint")
	}
	defer func() { _ = file.Close() }()
	actual, err := file.Stat()
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "inspect event checkpoint")
	}
	if !os.SameFile(info, actual) {
		return nil, cli.Usagef("the event checkpoint changed while opening it")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxEventCheckpointBytes+1))
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "read event checkpoint")
	}
	if len(raw) > maxEventCheckpointBytes {
		return nil, cli.Usagef("the event checkpoint is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var state eventCheckpoint
	if err := dec.Decode(&state); err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "decode event checkpoint")
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, cli.Usagef("the event checkpoint must contain one JSON object")
	}
	if state.Version != eventCheckpointVersion || (state.TabType != "agent" && state.TabType != "terminal") ||
		state.WorkerID == "" || state.TabID == "" || state.JournalID == "" || state.TabStreamID == "" ||
		state.Sequence < 0 || len(state.Cursor) == 0 || state.Filter == nil {
		return nil, cli.Usagef("the event checkpoint has an invalid version, source, filter or position")
	}
	return &state, nil
}

func (f *eventCheckpointFile) save(state *eventCheckpoint) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) >= maxEventCheckpointBytes {
		return cli.Usagef("the event checkpoint is too large")
	}
	if err := atomicfile.WriteFileDurable(f.path, append(raw, '\n'), 0o600); err != nil {
		return cli.Wrap(cli.CodeInternal, err, "save event checkpoint")
	}
	return nil
}

// eventCheckpointKinds spells the selected facts in the vocabulary's order.
// Equivalent flags share a checkpoint. Changing what may be skipped requires
// a different checkpoint, even when the cursor itself remains valid.
func eventCheckpointKinds(filter kindFilter, tabType leapmuxv1.TabType) []string {
	family := tabTypeName(tabType)
	result := make([]string, 0)
	for _, kind := range eventKinds {
		if kindFamily(kind.name) == family && filter.selects(kind.name) {
			result = append(result, kind.name)
		}
	}
	return result
}

type eventOutputWriter struct{ io.Writer }

func (w eventOutputWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

func printCheckpointEvent(printer *cli.Printer, filter kindFilter, event streamEvent) error {
	if !filter.selects(event.Kind) && event.Kind != kindStreamHeartbeat {
		if kindFamily(event.Kind) == "stream" {
			_, err := fmt.Fprintln(eventOutputWriter{printer.Err}, event.TextLine())
			return err
		}
		return nil
	}
	strict := *printer
	strict.Out = eventOutputWriter{printer.Out}
	return strict.Event(event)
}

package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/repopaths"
)

const (
	eventCheckpointVersion     = 1
	maxEventCheckpointBytes    = contracts.MaxMessageSize
	eventCheckpointLockDirName = "event-locks"
	tabCheckpointScope         = "tab"
	layoutCheckpointScope      = "layout"
	accountCheckpointScope     = "account"
)

type eventCheckpoint struct {
	Version int                     `json:"version"`
	Scope   string                  `json:"scope"`
	Filter  []string                `json:"kinds"`
	Tab     *tabEventCheckpoint     `json:"tab,omitempty"`
	Layout  *layoutEventCheckpoint  `json:"layout,omitempty"`
	Account *accountEventCheckpoint `json:"account,omitempty"`
}

type eventHistoryPosition struct {
	JournalID string `json:"journal_id"`
	Sequence  int64  `json:"sequence"`
	Cursor    []byte `json:"cursor"`
}

func (p *eventHistoryPosition) valid() bool {
	return p != nil && p.JournalID != "" && p.Sequence >= 0 && len(p.Cursor) != 0
}

type tabEventCheckpoint struct {
	WorkerID    string                `json:"worker_id"`
	TabType     string                `json:"tab_type"`
	TabID       string                `json:"tab_id"`
	TabStreamID string                `json:"tab_stream_id"`
	Position    *eventHistoryPosition `json:"position"`
}

func (p *tabEventCheckpoint) valid() bool {
	return p != nil &&
		(p.TabType == "agent" || p.TabType == "terminal") &&
		p.WorkerID != "" && p.TabID != "" && p.TabStreamID != "" &&
		p.Position.valid()
}

type layoutEventCheckpoint struct {
	WorkspaceID string                `json:"workspace_id,omitempty"`
	Position    *eventHistoryPosition `json:"position"`
}

func (p *layoutEventCheckpoint) valid() bool {
	return p != nil && p.Position.valid()
}

type accountWorkerPositions map[string]*eventHistoryPosition

type accountEventCheckpoint struct {
	OwnerID            string                            `json:"owner_user_id"`
	EnrollmentRevision int64                             `json:"enrollment_revision"`
	Hub                *eventHistoryPosition             `json:"hub,omitempty"`
	Workers            map[string]accountWorkerPositions `json:"workers"`
}

func (p *accountEventCheckpoint) valid() bool {
	if p == nil || p.OwnerID == "" || p.EnrollmentRevision < 0 || p.Workers == nil ||
		(p.Hub != nil && !p.Hub.valid()) {
		return false
	}
	for workerID, positions := range p.Workers {
		if workerID == "" || positions == nil {
			return false
		}
		for kind, position := range positions {
			if (kind != "agent" && kind != "terminal") || !position.valid() {
				return false
			}
		}
	}
	return true
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "create checkpoint directory")
	}
	// Resolve the parent so two paths through different directory symlinks
	// contend for the same lock. The checkpoint itself must be a regular file.
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "resolve checkpoint directory")
	}
	path = filepath.Join(dir, filepath.Base(path))
	if toplevel, ok := repopaths.IsInsideLeapMuxTmp(path); ok {
		_ = repopaths.EnsureTmpExcluded(toplevel)
	}
	cfgDir, err := control.ConfigDir()
	if err != nil {
		return nil, cli.Wrap(cli.CodeInternal, err, "resolve control config directory")
	}
	lockDir := filepath.Join(cfgDir, eventCheckpointLockDirName)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "create checkpoint lock directory")
	}
	lockInfo, err := os.Lstat(lockDir)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "inspect checkpoint lock directory")
	}
	if !lockInfo.IsDir() {
		return nil, cli.Usagef("the event lock path must be a directory")
	}
	resolvedLockDir := lockDir
	if evalLockDir, err := filepath.EvalSymlinks(lockDir); err == nil {
		resolvedLockDir = evalLockDir
	}
	if pathutil.HasPathPrefix(path, resolvedLockDir) {
		return nil, cli.Usagef("choose a checkpoint path outside the event lock directory")
	}
	sum := sha256.Sum256([]byte(path))
	lockPath := filepath.Join(lockDir, hex.EncodeToString(sum[:])+".lock")
	lock, err := filelock.Open(lockPath, false)
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

func (f *eventCheckpointFile) close() {
	_ = filelock.Unlock(f.lock)
	_ = f.lock.Close()
}

func checkpointScopeLabel(scope string) string {
	switch scope {
	case tabCheckpointScope:
		return "a tab stream"
	case layoutCheckpointScope:
		return "a layout stream"
	case accountCheckpointScope:
		return "an account stream"
	default:
		return "a different event scope"
	}
}

func openCheckedEventCheckpoint(path, expectedScope string) (*eventCheckpointFile, *eventCheckpoint, error) {
	file, err := openEventCheckpoint(path)
	if err != nil {
		return nil, nil, err
	}
	state, err := file.read()
	if err != nil {
		file.close()
		return nil, nil, err
	}
	if state != nil && state.Scope != expectedScope {
		file.close()
		return nil, nil, cli.Newf(cli.CodeConflict, "the event checkpoint belongs to %s", checkpointScopeLabel(state.Scope))
	}
	return file, state, nil
}

func verifyCheckpointFilter(saved *eventCheckpoint, kinds []string) error {
	if saved == nil || slices.Equal(kinds, saved.Filter) {
		return nil
	}
	return cli.Newf(cli.CodeConflict, "the event checkpoint uses a different --kind selection").
		WithHint("use the saved kind selection or another checkpoint file")
}

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
	if !state.valid() {
		return nil, cli.Usagef("the event checkpoint has an invalid version, source, filter or position")
	}
	return &state, nil
}

func (state eventCheckpoint) valid() bool {
	if state.Version != eventCheckpointVersion || state.Filter == nil {
		return false
	}
	switch state.Scope {
	case tabCheckpointScope:
		return state.Tab.valid() && state.Layout == nil && state.Account == nil
	case layoutCheckpointScope:
		return state.Layout.valid() && state.Tab == nil && state.Account == nil
	case accountCheckpointScope:
		return state.Account.valid() && state.Tab == nil && state.Layout == nil
	default:
		return false
	}
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

// eventCheckpointKinds spells the selected retained facts for tabType in the
// vocabulary's order. Equivalent flags share a checkpoint. Changing what may
// be skipped requires a different checkpoint, even when the cursor itself
// remains valid.
func eventCheckpointKinds(filter kindFilter, tabType leapmuxv1.TabType) []string {
	return filter.retainedKinds(tabReplayJournal(tabType))
}

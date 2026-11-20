// Package tofupins implements a JSON-backed Trust-On-First-Use (TOFU)
// key-pin store shared by the CLI (`leapmux control`) and the worker's
// cross-worker tunnel layer.
//
// Every operation runs as a transaction: the caller holds a file lock
// on a sibling `<pin-file>.lock` from read through decision or
// replacement. A missing file means confirmed empty state. An existing
// file requires a nonnull `pins` object. A read error, malformed
// document, or failed write leaves the previous trust decision intact
// and fails the affected operation.
//
// The `removal_revision` field advances whenever a pin is removed. A
// commit that observes a changed revision refuses the channel, so a
// removal invalidates a pending open for another Worker sharing the
// file. A new open resolves against the updated revision.
package tofupins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/util/atomicfile"
)

// Entry is one pinned worker's static keys plus telemetry. Fields are
// base64-StdEncoded for stable JSON.
type Entry struct {
	X25519PublicKey string    `json:"x25519_public_key"`
	MlkemPublicKey  string    `json:"mlkem_public_key"`
	SlhdsaPublicKey string    `json:"slhdsa_public_key"`
	FirstSeenAt     time.Time `json:"first_seen_at"`
	LastUsedAt      time.Time `json:"last_used_at"`
}

// defaultLastUsedPersistMinGap throttles `LastUsedAt` rewrites. The
// field is telemetry-only (surfaced by `pins list`); 10-minute
// staleness across CLI invocations is acceptable.
const defaultLastUsedPersistMinGap = 10 * time.Minute

// Options configures a Store. Path is required; the remaining knobs
// vary between the CLI (pins-not-secret, 0644) and worker (data-dir
// convention, 0600) callers.
type Options struct {
	// Path is the on-disk pin file. Its parent directory is created
	// with DirMode if missing.
	Path string
	// DirMode is the mode for the parent directory creation. Common
	// values: 0o755 for CLI config dirs, 0o700 for worker data dirs.
	DirMode os.FileMode
	// FileMode is the mode for the persisted pin file.
	FileMode os.FileMode
	// MismatchHintTmpl is a fmt-string template (one %s for the
	// worker_id) appended to the mismatch error so end users learn
	// how to clear the pin. Example: "clear with `leapmux control
	// worker pins remove --worker-id=%s`".
	MismatchHintTmpl string
	// LastUsedPersistMinGap throttles on-disk rewrites caused by
	// `LastUsedAt` bumps. Defaults to `defaultLastUsedPersistMinGap`
	// (10 minutes). Tests may shrink this to exercise the flush path
	// without sleeping; setting it to a negative value disables the
	// throttle entirely (every match rewrites).
	LastUsedPersistMinGap time.Duration
	// Now supplies the clock for `FirstSeenAt`, `LastUsedAt`, and the
	// `LastUsedPersistMinGap` comparison. Defaults to `time.Now`.
	// Tests inject a deterministic clock to place calls relative to
	// the throttle window without sleeping.
	Now func() time.Time
}

// onDiskFile is the JSON wrapper format.
type onDiskFile struct {
	Pins            map[string]Entry `json:"pins"`
	RemovalRevision uint64           `json:"removal_revision,omitempty"`
}

// Store is a transactional pin-file handle. It holds no in-memory
// mirror; every operation reads and writes the file under a
// cross-process lock.
type Store struct {
	opts Options
}

// Open prepares the pin-file path and its parent directory. It does
// not read the file; every operation is a fresh transaction.
func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("tofupins: Path required")
	}
	if opts.LastUsedPersistMinGap == 0 {
		opts.LastUsedPersistMinGap = defaultLastUsedPersistMinGap
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), opts.DirMode); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	return &Store{opts: opts}, nil
}

// lockPath returns the sibling lock-file path.
func (s *Store) lockPath() string {
	return s.opts.Path + ".lock"
}

// acquireLock opens the lock file and takes the exclusive lock,
// honoring ctx. The caller must close the returned file (which
// releases the lock).
func (s *Store) acquireLock(ctx context.Context) (*os.File, error) {
	f, err := filelock.Open(s.lockPath(), false)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := filelock.Lock(ctx, f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return f, nil
}

// readState reads the current pin file under an already-held lock.
// A missing file is confirmed empty state.
func (s *Store) readState() (*onDiskFile, error) {
	data, err := os.ReadFile(s.opts.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &onDiskFile{Pins: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pins: %w", err)
	}
	var f onDiskFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse pins: %w", err)
	}
	if f.Pins == nil {
		return nil, errors.New("parse pins: missing or null \"pins\" object")
	}
	return &f, nil
}

// writeState atomically replaces the pin file.
func (s *Store) writeState(f *onDiskFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.opts.Path, data, s.opts.FileMode)
}

// Resolve checks the presented keys against the durable pin for
// workerID and returns a commit function that publishes the result
// after the channel proves the session (e.g. after Ping).
//
// On first contact (no pin yet) the commit function records the pin.
// On a match the commit function bumps LastUsedAt subject to the
// throttle. On a mismatch Resolve returns an error immediately and
// OpenChannel aborts.
//
// The commit function validates that the state it observed at resolve
// time is still current: the removal revision must be unchanged and
// the previously observed pin (if any) must not have disappeared or
// changed. A nil commit is returned only for a stateless immutable
// pin policy; this store always returns a commit.
func (s *Store) Resolve(ctx context.Context, workerID string, publicKey, mlkemPublicKey, slhdsaPublicKey []byte) (func(context.Context) error, error) {
	if workerID == "" {
		return nil, errors.New("worker_id required")
	}
	x := base64.StdEncoding.EncodeToString(publicKey)
	m := base64.StdEncoding.EncodeToString(mlkemPublicKey)
	sl := base64.StdEncoding.EncodeToString(slhdsaPublicKey)

	lockFile, err := s.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lockFile.Close()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	state, err := s.readState()
	if err != nil {
		return nil, err
	}

	existing, hasExisting := state.Pins[workerID]
	if hasExisting {
		if existing.X25519PublicKey != x || existing.MlkemPublicKey != m || existing.SlhdsaPublicKey != sl {
			pinnedFingerprint := "pinned <unreadable>"
			pinnedX, xErr := base64.StdEncoding.DecodeString(existing.X25519PublicKey)
			pinnedM, mErr := base64.StdEncoding.DecodeString(existing.MlkemPublicKey)
			pinnedS, sErr := base64.StdEncoding.DecodeString(existing.SlhdsaPublicKey)
			if xErr == nil && mErr == nil && sErr == nil {
				pinnedFingerprint = "pinned " + noise.CompositeKeyFingerprint(pinnedX, pinnedM, pinnedS)
			}
			return nil, fmt.Errorf("worker %s key mismatch: %s, presented %s — %s",
				workerID, pinnedFingerprint, noise.CompositeKeyFingerprint(publicKey, mlkemPublicKey, slhdsaPublicKey),
				fmt.Sprintf(s.opts.MismatchHintTmpl, workerID))
		}
	}

	// Capture state for the commit function.
	capturedRevision := state.RemovalRevision
	capturedExisting, capturedHas := state.Pins[workerID]
	capturedEntry := Entry{
		X25519PublicKey: x,
		MlkemPublicKey:  m,
		SlhdsaPublicKey: sl,
	}

	commit := func(commitCtx context.Context) error {
		return s.commit(commitCtx, workerID, capturedEntry, capturedRevision, capturedHas, capturedExisting)
	}
	return commit, nil
}

// commit publishes the pin decision. It re-reads the file under lock
// and validates that the state observed at resolve time is still
// current.
func (s *Store) commit(ctx context.Context, workerID string, presented Entry, capturedRevision uint64, capturedHas bool, capturedExisting Entry) error {
	lockFile, err := s.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer lockFile.Close()

	if err := ctx.Err(); err != nil {
		return err
	}

	state, err := s.readState()
	if err != nil {
		return err
	}

	// Validate: revision must not have changed.
	if state.RemovalRevision != capturedRevision {
		return fmt.Errorf("pin removal revision changed during open (was %d, now %d)", capturedRevision, state.RemovalRevision)
	}

	now := s.opts.Now().UTC()
	current, hasCurrent := state.Pins[workerID]

	if capturedHas {
		// A matching pin was observed at resolve time.
		if !hasCurrent {
			return fmt.Errorf("pin for worker %s disappeared during open", workerID)
		}
		if current.X25519PublicKey != presented.X25519PublicKey ||
			current.MlkemPublicKey != presented.MlkemPublicKey ||
			current.SlhdsaPublicKey != presented.SlhdsaPublicKey {
			return fmt.Errorf("pin for worker %s changed during open", workerID)
		}
		// Match: bump LastUsedAt subject to throttle.
		shouldWrite := s.opts.LastUsedPersistMinGap < 0 ||
			now.Sub(current.LastUsedAt) >= s.opts.LastUsedPersistMinGap
		current.LastUsedAt = now
		state.Pins[workerID] = current
		if !shouldWrite {
			return nil
		}
	} else {
		// First contact.
		if hasCurrent {
			// Another process installed a pin during the open.
			if current.X25519PublicKey == presented.X25519PublicKey &&
				current.MlkemPublicKey == presented.MlkemPublicKey &&
				current.SlhdsaPublicKey == presented.SlhdsaPublicKey {
				// Same keys: preserve its first_seen_at, bump last_used_at.
				current.LastUsedAt = now
				state.Pins[workerID] = current
				// Fall through to write.
			} else {
				return fmt.Errorf("pin for worker %s was installed with different keys during open", workerID)
			}
		} else {
			// Clean first contact.
			presented.FirstSeenAt = now
			presented.LastUsedAt = now
			state.Pins[workerID] = presented
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return s.writeState(state)
}

// Remove clears the pin for workerID so the next Resolve records a
// fresh TOFU entry. Advances the removal revision when a pin was
// present. Idempotent on a missing entry.
func (s *Store) Remove(ctx context.Context, workerID string) error {
	if workerID == "" {
		return errors.New("worker_id required")
	}
	lockFile, err := s.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer lockFile.Close()

	if err := ctx.Err(); err != nil {
		return err
	}

	state, err := s.readState()
	if err != nil {
		return err
	}

	if _, ok := state.Pins[workerID]; !ok {
		return nil
	}
	delete(state.Pins, workerID)
	if state.RemovalRevision == math.MaxUint64 {
		return errors.New("removal revision overflow")
	}
	state.RemovalRevision++

	if err := ctx.Err(); err != nil {
		return err
	}
	return s.writeState(state)
}

// Snapshot returns a copy of the current pin map.
func (s *Store) Snapshot(ctx context.Context) (map[string]Entry, error) {
	lockFile, err := s.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lockFile.Close()

	state, err := s.readState()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Entry, len(state.Pins))
	for k, v := range state.Pins {
		out[k] = v
	}
	return out, nil
}

// RemovalRevision returns the current removal revision.
func (s *Store) RemovalRevision(ctx context.Context) (uint64, error) {
	lockFile, err := s.acquireLock(ctx)
	if err != nil {
		return 0, err
	}
	defer lockFile.Close()

	state, err := s.readState()
	if err != nil {
		return 0, err
	}
	return state.RemovalRevision, nil
}

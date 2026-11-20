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
	"bytes"
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

// pinFields gives the persisted entry and its raw decoder the same JSON fields.
type pinFields[Key, Timestamp any] struct {
	X25519PublicKey Key       `json:"x25519_public_key"`
	MlkemPublicKey  Key       `json:"mlkem_public_key"`
	SlhdsaPublicKey Key       `json:"slhdsa_public_key"`
	FirstSeenAt     Timestamp `json:"first_seen_at"`
	LastUsedAt      Timestamp `json:"last_used_at"`
}

// Entry holds one pinned Worker's static keys and timestamps. The keys use
// standard Base64.
type Entry pinFields[string, time.Time]

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

// removalRevision requires an unsigned integer when the field is present.
type removalRevision uint64

func (r *removalRevision) UnmarshalJSON(data []byte) error {
	var value *uint64
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid removal_revision: %w", err)
	}
	if value == nil {
		return errors.New("removal_revision must be an unsigned integer")
	}
	*r = removalRevision(*value)
	return nil
}

// onDiskFile is the JSON wrapper format.
type onDiskFile struct {
	Pins            pinMap          `json:"pins"`
	RemovalRevision removalRevision `json:"removal_revision,omitempty"`
}

// Store is a transactional pin-file handle. Every operation reads the file
// under a cross-process lock. A decoded snapshot is reused only when those
// exact bytes still match, so another process remains authoritative.
type Store struct {
	opts       Options
	cacheData  []byte
	cacheState *onDiskFile
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

// withState keeps the sibling lock open from the read through the operation.
func (s *Store) withState(ctx context.Context, useState func(*onDiskFile) error) (result error) {
	file, err := filelock.Open(s.opts.Path+".lock", false)
	if err != nil {
		return fmt.Errorf("open pin lock: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close pin lock: %w", err))
		}
	}()

	if err := filelock.Lock(ctx, file); err != nil {
		return fmt.Errorf("acquire pin lock: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := s.readState()
	if err != nil {
		return err
	}
	return useState(state)
}

// readState reads the current pin file under an already-held lock.
// A missing file is confirmed empty state.
func (s *Store) readState() (*onDiskFile, error) {
	data, err := os.ReadFile(s.opts.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &onDiskFile{Pins: pinMap{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pins: %w", err)
	}
	if s.cacheState != nil && bytes.Equal(data, s.cacheData) {
		return cloneState(s.cacheState), nil
	}
	var f onDiskFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse pins in %s: %w", s.opts.Path, err)
	}
	if f.Pins == nil {
		return nil, fmt.Errorf("parse pins in %s: missing or null \"pins\" object", s.opts.Path)
	}
	s.cacheData = bytes.Clone(data)
	s.cacheState = cloneState(&f)
	return cloneState(&f), nil
}

func cloneState(state *onDiskFile) *onDiskFile {
	cloned := &onDiskFile{
		Pins:            make(pinMap, len(state.Pins)),
		RemovalRevision: state.RemovalRevision,
	}
	for workerID, entry := range state.Pins {
		copied := *entry
		cloned.Pins[workerID] = &copied
	}
	return cloned
}

func (f *onDiskFile) marshal() ([]byte, error) {
	return json.MarshalIndent(f, "", "  ")
}

// writeState atomically replaces the pin file.
func (s *Store) writeState(ctx context.Context, encode func() ([]byte, error)) error {
	data, err := encode()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.opts.Path, data, s.opts.FileMode); err != nil {
		return err
	}
	var state onDiskFile
	if err := json.Unmarshal(data, &state); err != nil {
		s.cacheData = nil
		s.cacheState = nil
		return nil
	}
	s.cacheData = bytes.Clone(data)
	s.cacheState = cloneState(&state)
	return nil
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
	presented := Entry{
		X25519PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		MlkemPublicKey:  base64.StdEncoding.EncodeToString(mlkemPublicKey),
		SlhdsaPublicKey: base64.StdEncoding.EncodeToString(slhdsaPublicKey),
	}
	var commit func(context.Context) error
	err := s.withState(ctx, func(state *onDiskFile) error {
		existing := state.Pins[workerID]
		if existing != nil && !existing.matchesKeys(presented) {
			return s.keyMismatchError(workerID, "", existing.fingerprint(), presented)
		}

		revision := state.RemovalRevision
		commit = func(commitCtx context.Context) error {
			return s.commit(commitCtx, workerID, presented, revision, existing)
		}
		return nil
	})
	if err != nil {
		var unreadable *unreadablePinError
		if errors.As(err, &unreadable) && unreadable.workerID == workerID {
			return nil, fmt.Errorf("%w. Repair the pin file before retrying. worker %s key mismatch (pinned <unreadable>, presented %s)",
				err, workerID, presented.fingerprint())
		}
		return nil, err
	}
	return commit, nil
}

func (s *Store) keyMismatchError(workerID, detail, pinnedFingerprint string, presented Entry) error {
	message := fmt.Sprintf("worker %s key mismatch (pinned %s, presented %s)", workerID, pinnedFingerprint, presented.fingerprint())
	if detail != "" {
		message += ". " + detail
	}
	if s.opts.MismatchHintTmpl != "" {
		message += " — " + fmt.Sprintf(s.opts.MismatchHintTmpl, workerID)
	}
	return errors.New(message)
}

func (e *Entry) fingerprint() string {
	x25519, xErr := decodeBase64Key(e.X25519PublicKey)
	mlkem, mErr := decodeBase64Key(e.MlkemPublicKey)
	slhdsa, sErr := decodeBase64Key(e.SlhdsaPublicKey)
	if xErr != nil || mErr != nil || sErr != nil {
		return "<unreadable>"
	}
	return noise.CompositeKeyFingerprint(x25519, mlkem, slhdsa)
}

func (e *Entry) matchesKeys(other Entry) bool {
	return e.X25519PublicKey == other.X25519PublicKey &&
		e.MlkemPublicKey == other.MlkemPublicKey &&
		e.SlhdsaPublicKey == other.SlhdsaPublicKey
}

// commit validates the decision against the current state and publishes any
// pin or timestamp update.
func (s *Store) commit(ctx context.Context, workerID string, presented Entry, revision removalRevision, existing *Entry) error {
	return s.withState(ctx, func(state *onDiskFile) error {
		if state.RemovalRevision != revision {
			return fmt.Errorf("pin removal revision changed during open (was %d, now %d)", revision, state.RemovalRevision)
		}
		current := state.Pins[workerID]
		switch {
		case existing != nil && current == nil:
			return fmt.Errorf("pin for worker %s disappeared during open", workerID)
		case existing != nil && !current.matchesKeys(*existing):
			return s.keyMismatchError(workerID, "The pin changed during open", current.fingerprint(), presented)
		case current != nil && !current.matchesKeys(presented):
			return s.keyMismatchError(workerID, "The pin was installed with different keys during open", current.fingerprint(), presented)
		}

		now := s.opts.Now().UTC()
		if current == nil {
			presented.FirstSeenAt = now
			current = &presented
			state.Pins[workerID] = current
		} else if s.opts.LastUsedPersistMinGap >= 0 && !now.Before(current.LastUsedAt) &&
			now.Sub(current.LastUsedAt) < s.opts.LastUsedPersistMinGap {
			return nil
		}
		current.LastUsedAt = now
		return s.writeState(ctx, state.marshal)
	})
}

// Remove clears the pin for workerID so the next verified open records a
// fresh TOFU entry. Advances the removal revision when a pin was
// present. Idempotent on a missing entry.
func (s *Store) Remove(ctx context.Context, workerID string) error {
	if workerID == "" {
		return errors.New("worker_id required")
	}
	return s.withState(ctx, func(state *onDiskFile) error {
		if _, ok := state.Pins[workerID]; !ok {
			return nil
		}
		if state.RemovalRevision == math.MaxUint64 {
			return errors.New("removal revision overflow")
		}
		delete(state.Pins, workerID)
		state.RemovalRevision++
		return s.writeState(ctx, state.marshal)
	})
}

// Snapshot returns a copy of the current pin map.
func (s *Store) Snapshot(ctx context.Context) (map[string]Entry, error) {
	var out map[string]Entry
	err := s.withState(ctx, func(state *onDiskFile) error {
		out = make(map[string]Entry, len(state.Pins))
		for workerID, entry := range state.Pins {
			out[workerID] = *entry
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemovalRevision returns the current removal revision.
func (s *Store) RemovalRevision(ctx context.Context) (uint64, error) {
	var revision uint64
	err := s.withState(ctx, func(state *onDiskFile) error {
		revision = uint64(state.RemovalRevision)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

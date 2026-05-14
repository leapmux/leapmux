// Package tofupins implements a JSON-backed Trust-On-First-Use (TOFU)
// key-pin store shared by the CLI (`leapmux remote`) and the worker's
// cross-worker tunnel layer.
//
// Both callers persist the same (worker_id → {x25519, mlkem, slhdsa,
// first_seen_at, last_used_at}) map; only the on-disk location, file
// permissions, and mismatch-hint command differ. The locking,
// (de)serialization, and atomic-rewrite logic live here so a fix
// (e.g. fsync, file locking, schema bump) lands in one place.
package tofupins

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
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

// defaultLastUsedPersistMinGap throttles `LastUsedAt` rewrites. `Verify`
// is on the cross-worker tunnel hot path; without a throttle every
// successful verification rewrites the whole pin JSON. The field is
// telemetry-only (surfaced by `pins list`); 10-minute staleness across
// CLI invocations is acceptable.
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
	// how to clear the pin. Example: "clear with `leapmux remote
	// worker pins remove --worker-id=%s`".
	MismatchHintTmpl string
	// LastUsedPersistMinGap throttles on-disk rewrites caused by
	// `LastUsedAt` bumps. Defaults to `defaultLastUsedPersistMinGap`
	// (10 minutes). Tests may shrink this to exercise the flush path
	// without sleeping; setting it to a negative value disables the
	// throttle entirely (every match rewrites).
	LastUsedPersistMinGap time.Duration
}

// onDiskFile is the JSON wrapper format. The wrapper (vs a bare map)
// leaves room for adding sibling fields later (e.g. schema version,
// last-rotation timestamp) without breaking older readers.
type onDiskFile struct {
	Pins map[string]Entry `json:"pins"`
}

// Store is the in-memory mirror of the pin file, with a mutex so
// concurrent Verify/Remove calls serialize.
type Store struct {
	opts Options
	mu   sync.Mutex
	pins map[string]Entry
}

// Open loads the pin file at opts.Path (creating its parent
// directory if missing). A missing file yields an empty store.
func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("tofupins: Path required")
	}
	if opts.LastUsedPersistMinGap == 0 {
		opts.LastUsedPersistMinGap = defaultLastUsedPersistMinGap
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), opts.DirMode); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	s := &Store{opts: opts, pins: map[string]Entry{}}
	data, err := os.ReadFile(opts.Path)
	if err == nil {
		var f onDiskFile
		if uerr := json.Unmarshal(data, &f); uerr != nil {
			return nil, fmt.Errorf("parse pins: %w", uerr)
		}
		if f.Pins != nil {
			s.pins = f.Pins
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read pins: %w", err)
	}
	return s, nil
}

// Verify implements the TOFU contract used by tunnel.KeyPinStore.
// First contact records the keys and returns nil. A mismatch returns
// an error whose message appends the configured clear-command hint.
//
// `LastUsedAt` is bumped in-memory on every successful match but the
// on-disk JSON is rewritten only when the bump exceeds
// `lastUsedPersistMinGap` past the previously-persisted value. This
// keeps the field useful for `pins list` without paying a JSON
// marshal+rename on every hot-path tunnel handshake.
func (s *Store) Verify(workerID string, public, mlkem, slhdsa []byte) error {
	if workerID == "" {
		return errors.New("worker_id required")
	}
	x := base64.StdEncoding.EncodeToString(public)
	m := base64.StdEncoding.EncodeToString(mlkem)
	sl := base64.StdEncoding.EncodeToString(slhdsa)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if existing, ok := s.pins[workerID]; ok {
		if existing.X25519PublicKey != x || existing.MlkemPublicKey != m || existing.SlhdsaPublicKey != sl {
			return fmt.Errorf("worker %s key mismatch — %s", workerID, fmt.Sprintf(s.opts.MismatchHintTmpl, workerID))
		}
		prevPersisted := existing.LastUsedAt
		existing.LastUsedAt = now
		s.pins[workerID] = existing
		if s.opts.LastUsedPersistMinGap > 0 && now.Sub(prevPersisted) < s.opts.LastUsedPersistMinGap {
			// Skip on-disk write — the bump is within the throttle
			// window. Subsequent calls will eventually flush once the
			// gap is exceeded.
			return nil
		}
		return s.persistLocked()
	}
	s.pins[workerID] = Entry{
		X25519PublicKey: x,
		MlkemPublicKey:  m,
		SlhdsaPublicKey: sl,
		FirstSeenAt:     now,
		LastUsedAt:      now,
	}
	return s.persistLocked()
}

// Remove clears the pin for workerID so the next Verify records a
// fresh TOFU entry. Idempotent on a missing entry.
func (s *Store) Remove(workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pins[workerID]; !ok {
		return nil
	}
	delete(s.pins, workerID)
	return s.persistLocked()
}

// Snapshot returns a copy of the current pin map so callers can
// iterate without holding the lock.
func (s *Store) Snapshot() map[string]Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Entry, len(s.pins))
	for k, v := range s.pins {
		out[k] = v
	}
	return out
}

func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(onDiskFile{Pins: s.pins}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.opts.Path, data, s.opts.FileMode)
}

// Package crossworker provides the worker-side client for talking to
// sibling workers. It pools E2EE channels and handles TOFU key pinning
// + delegation-token bookkeeping.
package crossworker

import (
	"errors"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/tofupins"
)

// PinEntry mirrors tofupins.Entry for the worker pin file shape.
type PinEntry = tofupins.Entry

// PinStore is the worker-side TOFU key-pin store. Persisted under the
// worker's data directory so a worker restart doesn't lose pins.
//
// Auto-pin on first contact: agents can't answer interactive prompts,
// so we record the first-seen keys and trust them. Mismatch aborts and
// requires explicit intervention to clear (admin / future UI).
type PinStore struct {
	inner *tofupins.Store
}

// NewPinStore opens (or creates) the on-disk pin file under dataDir.
func NewPinStore(dataDir string) (*PinStore, error) {
	if dataDir == "" {
		return nil, errors.New("crossworker: data dir required")
	}
	inner, err := tofupins.Open(tofupins.Options{
		Path:             filepath.Join(dataDir, "cross_worker_pins.json"),
		DirMode:          0o700,
		FileMode:         0o600,
		MismatchHintTmpl: "pin previously recorded; clear with `leapmux worker cross-worker-pins remove --target-worker-id=%s`",
	})
	if err != nil {
		return nil, err
	}
	return &PinStore{inner: inner}, nil
}

// Verify implements tunnel.KeyPinStore.
func (s *PinStore) Verify(workerID string, public, mlkem, slhdsa []byte) error {
	return s.inner.Verify(workerID, public, mlkem, slhdsa)
}

// Remove clears a pin so the next call records a new TOFU pin.
// Idempotent.
func (s *PinStore) Remove(workerID string) error {
	return s.inner.Remove(workerID)
}

// List returns a snapshot of pin metadata, keyed by target worker id.
func (s *PinStore) List() map[string]PinEntry {
	return s.inner.Snapshot()
}

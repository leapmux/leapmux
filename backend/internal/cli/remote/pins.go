package remote

import (
	"path/filepath"
	"sort"

	"github.com/leapmux/leapmux/internal/util/tofupins"
)

// PinEntry mirrors tofupins.Entry — re-declared as a package-public
// alias so existing callers (`leapmux remote worker pins …` output)
// don't take a direct dependency on the util package.
type PinEntry = tofupins.Entry

// PinStore is the CLI's TOFU keystore. Pins live under
// <ConfigDir>/<hub-host>/pins.json with mode 0644 (pins aren't
// secrets — losing them only forces a TOFU re-pin on the next call).
type PinStore struct {
	inner *tofupins.Store
}

// NewPinStore opens (or initializes) the pin file for hubURL.
func NewPinStore(hubURL string) (*PinStore, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	host, err := HubHost(hubURL)
	if err != nil {
		return nil, err
	}
	inner, err := tofupins.Open(tofupins.Options{
		Path:             filepath.Join(dir, host, "pins.json"),
		DirMode:          0o755,
		FileMode:         0o644,
		MismatchHintTmpl: "clear with `leapmux remote worker pins remove --worker-id=%s`",
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

// Remove clears a pin so the next OpenChannel records a new TOFU pin.
// Idempotent.
func (s *PinStore) Remove(workerID string) error {
	return s.inner.Remove(workerID)
}

// PinListEntry is one row of the sorted List result.
type PinListEntry struct {
	WorkerID string
	Entry    PinEntry
}

// List returns a snapshot of pins, sorted by worker_id for stable
// JSON output.
func (s *PinStore) List() []PinListEntry {
	snap := s.inner.Snapshot()
	ids := make([]string, 0, len(snap))
	for id := range snap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]PinListEntry, len(ids))
	for i, id := range ids {
		out[i] = PinListEntry{WorkerID: id, Entry: snap[id]}
	}
	return out
}

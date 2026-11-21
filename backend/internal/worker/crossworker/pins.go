// Package crossworker provides the worker-side client for talking to
// sibling workers. It pools E2EE channels and handles TOFU key pinning
// + delegation-token bookkeeping.
package crossworker

import (
	"errors"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/tofupins"
)

// NewPinStore opens (or creates) the on-disk pin file under dataDir.
func NewPinStore(dataDir string) (*tofupins.Store, error) {
	if dataDir == "" {
		return nil, errors.New("crossworker: data dir required")
	}
	return tofupins.Open(tofupins.Options{
		Path:             filepath.Join(dataDir, "cross_worker_pins.json"),
		DirMode:          0o700,
		FileMode:         0o600,
		MismatchHintTmpl: "the target worker keeps its keys while it stays registered; clear the pin only after the presented fingerprint matches the one the target worker logs when it starts: `leapmux worker cross-worker-pins remove --target-worker-id=%s`",
	})
}

package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/leapmux/leapmux/util/atomicfile"
)

const workerStateFileName = "skills-state.json"

// WorkerState persists per-Worker skill auto-sync preferences under the
// Worker's DataDir.
type WorkerState struct {
	AutoSync bool     `json:"auto_sync"`
	Enabled  []string `json:"enabled"`
	Targets  []string `json:"targets"`
}

// DefaultWorkerState returns the safe opt-in default state (auto-sync off,
// universal ~/.agents/skills target).
func DefaultWorkerState() WorkerState {
	return WorkerState{
		AutoSync: false,
		Enabled:  []string{},
		Targets:  []string{TargetAgents},
	}
}

// LoadWorkerState reads <dataDir>/skills-state.json, returning defaults when
// absent.
func LoadWorkerState(dataDir string) (WorkerState, error) {
	if dataDir == "" {
		return DefaultWorkerState(), nil
	}
	path := filepath.Join(dataDir, workerStateFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultWorkerState(), nil
		}
		return WorkerState{}, fmt.Errorf("read worker skills state: %w", err)
	}
	var st WorkerState
	if err := json.Unmarshal(raw, &st); err != nil {
		return WorkerState{}, fmt.Errorf("parse worker skills state: %w", err)
	}
	return normalizeWorkerState(st), nil
}

// SaveWorkerState persists st atomically with owner-only permissions under
// dataDir.
func SaveWorkerState(dataDir string, st WorkerState) error {
	if dataDir == "" {
		return errors.New("worker data directory is not configured")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create worker data directory: %w", err)
	}
	norm := normalizeWorkerState(st)
	raw, err := json.MarshalIndent(norm, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicfile.WriteFile(filepath.Join(dataDir, workerStateFileName), raw, 0o600)
}

func normalizeWorkerState(st WorkerState) WorkerState {
	validEnabled := make([]string, 0, len(st.Enabled))
	for _, name := range st.Enabled {
		if _, ok := Lookup(name); ok && !slices.Contains(validEnabled, name) {
			validEnabled = append(validEnabled, name)
		}
	}
	validTargets := make([]string, 0, len(st.Targets))
	for _, raw := range st.Targets {
		if norm, err := NormalizeTarget(raw); err == nil && !slices.Contains(validTargets, norm) {
			validTargets = append(validTargets, norm)
		}
	}
	if len(validTargets) == 0 {
		validTargets = []string{TargetAgents}
	}
	return WorkerState{
		AutoSync: st.AutoSync,
		Enabled:  validEnabled,
		Targets:  validTargets,
	}
}

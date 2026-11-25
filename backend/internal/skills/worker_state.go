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

// ErrInvalidWorkerStateUpdate marks a caller-supplied skill or target error in
// ApplyWorkerStateUpdate so RPC handlers can distinguish invalid arguments from
// filesystem failures.
var ErrInvalidWorkerStateUpdate = errors.New("invalid worker skills update")

// WorkerStateUpdate describes a partial update to a Worker's skill preferences
// and on-disk skill directories.
type WorkerStateUpdate struct {
	AutoSync            *bool
	Targets             []string
	UpdateEnabledSkills bool
	EnabledSkills       []string
	SyncNow             bool
	Force               bool
}

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

// ApplyWorkerStateUpdate validates req, uninstalls newly disabled skills across
// previously and newly configured targets (skipping locally modified or
// unmanaged directories unless req.Force is true), syncs enabled skills, and
// persists the updated WorkerState only after disk operations succeed.
func ApplyWorkerStateUpdate(dataDir, homeDir string, req WorkerStateUpdate) (WorkerState, error) {
	if dataDir == "" || homeDir == "" {
		return WorkerState{}, errors.New("worker home or data directory is not configured")
	}
	prev, err := LoadWorkerState(dataDir)
	if err != nil {
		return WorkerState{}, err
	}
	next := prev
	if req.AutoSync != nil {
		next.AutoSync = *req.AutoSync
	}
	if len(req.Targets) > 0 {
		validated := make([]string, 0, len(req.Targets))
		for _, raw := range req.Targets {
			norm, nErr := NormalizeTarget(raw)
			if nErr != nil {
				return WorkerState{}, fmt.Errorf("%w: %w", ErrInvalidWorkerStateUpdate, nErr)
			}
			if !slices.Contains(validated, norm) {
				validated = append(validated, norm)
			}
		}
		next.Targets = validated
	}
	var newlyDisabled []string
	if req.UpdateEnabledSkills {
		validated := make([]string, 0, len(req.EnabledSkills))
		for _, name := range req.EnabledSkills {
			if _, ok := Lookup(name); !ok {
				return WorkerState{}, fmt.Errorf("%w: unknown skill %q", ErrInvalidWorkerStateUpdate, name)
			}
			if !slices.Contains(validated, name) {
				validated = append(validated, name)
			}
		}
		for _, old := range prev.Enabled {
			if !slices.Contains(validated, old) {
				newlyDisabled = append(newlyDisabled, old)
			}
		}
		next.Enabled = validated
	}

	if len(newlyDisabled) > 0 {
		allTargets := make([]string, 0, len(prev.Targets)+len(next.Targets))
		for _, t := range append(slices.Clone(prev.Targets), next.Targets...) {
			if !slices.Contains(allTargets, t) {
				allTargets = append(allTargets, t)
			}
		}
		uninstallOpts := InstallOptions{Force: req.Force, SkipProtected: !req.Force}
		for _, target := range allTargets {
			dir, rErr := ResolveTargetDir(homeDir, target)
			if rErr != nil {
				continue
			}
			if _, uErr := Uninstall(dir, newlyDisabled, uninstallOpts); uErr != nil {
				return WorkerState{}, uErr
			}
		}
	}

	if len(next.Enabled) > 0 && (req.UpdateEnabledSkills || len(req.Targets) > 0 || req.SyncNow) {
		for _, target := range next.Targets {
			dir, rErr := ResolveTargetDir(homeDir, target)
			if rErr != nil {
				return WorkerState{}, fmt.Errorf("%w: %w", ErrInvalidWorkerStateUpdate, rErr)
			}
			if _, sErr := SyncEnabled(dir, next.Enabled, req.Force); sErr != nil {
				return WorkerState{}, sErr
			}
		}
	}

	if err := SaveWorkerState(dataDir, next); err != nil {
		return WorkerState{}, err
	}
	return normalizeWorkerState(next), nil
}

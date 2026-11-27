package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const workerStateFileName = "skills-state.json"

// ErrInvalidWorkerStateUpdate marks a caller-supplied skill or target error in
// ApplyWorkerStateUpdate so RPC handlers can distinguish invalid arguments from
// filesystem failures.
var ErrInvalidWorkerStateUpdate = errors.New("invalid worker skills update")

// TargetOutcome reports what ApplyWorkerStateUpdate did for one skill in one
// target directory.
type TargetOutcome struct {
	Name   string `json:"name"`
	Target Target `json:"target"`
	Action Action `json:"action"`
	Path   string `json:"path"`
}

// WorkerStateUpdate describes a replacement or atomic delta update to a
// Worker's skill preferences and on-disk skill directories.
type WorkerStateUpdate struct {
	AutoSync            *bool
	UpdateEnabledSkills bool
	EnabledSkills       []string
	AddSkills           []string
	RemoveSkills        []string
	AddAllSkills        bool
	RemoveAllSkills     bool
	Targets             []Target
	AddTargets          []Target
	RemoveTargets       []Target
	SyncNow             bool
	Force               bool
	StrictConflicts     bool
}

// WorkerState persists per-Worker skill auto-sync preferences under the
// Worker's DataDir.
type WorkerState struct {
	AutoSync bool     `json:"auto_sync"`
	Enabled  []string `json:"enabled"`
	Targets  []Target `json:"targets"`
}

func defaultWorkerState() WorkerState {
	return WorkerState{
		AutoSync: false,
		Enabled:  []string{},
		Targets:  []Target{TargetAgents},
	}
}

// LoadWorkerState reads <dataDir>/skills-state.json, returning defaults when
// absent.
func LoadWorkerState(dataDir string) (WorkerState, error) {
	if strings.TrimSpace(dataDir) == "" {
		return defaultWorkerState(), nil
	}
	statePath := filepath.Join(dataDir, workerStateFileName)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultWorkerState(), nil
		}
		return WorkerState{}, fmt.Errorf("read worker skills state: %w", err)
	}
	var st *WorkerState
	if err := json.Unmarshal(raw, &st); err != nil {
		return WorkerState{}, fmt.Errorf("parse worker skills state: %w", err)
	}
	if st == nil {
		return WorkerState{}, errors.New("parse worker skills state: unexpected null")
	}
	return normalizeWorkerState(*st), nil
}

func saveWorkerState(dataDir string, st WorkerState) error {
	if strings.TrimSpace(dataDir) == "" {
		return errors.New("worker data directory is not configured")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create worker data directory: %w", err)
	}
	return writeJSONAtomic(filepath.Join(dataDir, workerStateFileName), normalizeWorkerState(st), 0o600)
}

func normalizeWorkerState(st WorkerState) WorkerState {
	validEnabled := make([]string, 0, len(st.Enabled))
	for _, name := range st.Enabled {
		if s, ok := Lookup(name); ok {
			validEnabled = appendUnique(validEnabled, s.Name)
		}
	}
	validTargets := make([]Target, 0, len(st.Targets))
	for _, raw := range st.Targets {
		if norm, err := NormalizeTarget(string(raw)); err == nil {
			validTargets = appendUnique(validTargets, norm)
		}
	}
	if len(validTargets) == 0 {
		validTargets = []Target{TargetAgents}
	}
	return WorkerState{
		AutoSync: st.AutoSync,
		Enabled:  validEnabled,
		Targets:  validTargets,
	}
}

type targetOpStep struct {
	target Target
	dir    string
	skills []string
	opts   Options
}

type workerStatePlan struct {
	next           WorkerState
	toUninstall    []string
	uninstallSteps []targetOpStep
	syncSteps      []targetOpStep
}

func validateSkillNames(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		s, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("%w: %w %q", ErrInvalidWorkerStateUpdate, ErrUnknownSkill, name)
		}
		out = appendUnique(out, s.Name)
	}
	return out, nil
}

func validateTargetTokens(targets []Target) ([]Target, error) {
	out := make([]Target, 0, len(targets))
	for _, raw := range targets {
		norm, err := NormalizeTarget(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidWorkerStateUpdate, err)
		}
		out = appendUnique(out, norm)
	}
	return out, nil
}

func planWorkerStateUpdate(prev WorkerState, homeDir string, req WorkerStateUpdate) (workerStatePlan, error) {
	hasSkillDelta := len(req.AddSkills) > 0 || len(req.RemoveSkills) > 0 || req.AddAllSkills || req.RemoveAllSkills
	if req.UpdateEnabledSkills && hasSkillDelta {
		return workerStatePlan{}, fmt.Errorf("%w: cannot combine UpdateEnabledSkills with delta skill fields", ErrInvalidWorkerStateUpdate)
	}
	if req.AddAllSkills && req.RemoveAllSkills {
		return workerStatePlan{}, fmt.Errorf("%w: cannot combine AddAllSkills and RemoveAllSkills", ErrInvalidWorkerStateUpdate)
	}
	if req.AddAllSkills && len(req.RemoveSkills) > 0 {
		return workerStatePlan{}, fmt.Errorf("%w: cannot combine AddAllSkills with RemoveSkills", ErrInvalidWorkerStateUpdate)
	}
	if req.RemoveAllSkills && len(req.AddSkills) > 0 {
		return workerStatePlan{}, fmt.Errorf("%w: cannot combine RemoveAllSkills with AddSkills", ErrInvalidWorkerStateUpdate)
	}
	if len(req.Targets) > 0 && (len(req.AddTargets) > 0 || len(req.RemoveTargets) > 0) {
		return workerStatePlan{}, fmt.Errorf("%w: cannot combine Targets with AddTargets or RemoveTargets", ErrInvalidWorkerStateUpdate)
	}

	next := WorkerState{
		AutoSync: prev.AutoSync,
		Enabled:  slices.Clone(prev.Enabled),
		Targets:  slices.Clone(prev.Targets),
	}
	if req.AutoSync != nil {
		next.AutoSync = *req.AutoSync
	}

	// Validate and resolve target configuration.
	if len(req.Targets) > 0 {
		validated, err := validateTargetTokens(req.Targets)
		if err != nil {
			return workerStatePlan{}, err
		}
		next.Targets = validated
	} else if len(req.AddTargets) > 0 || len(req.RemoveTargets) > 0 {
		addTargets, err := validateTargetTokens(req.AddTargets)
		if err != nil {
			return workerStatePlan{}, err
		}
		removeTargets, err := validateTargetTokens(req.RemoveTargets)
		if err != nil {
			return workerStatePlan{}, err
		}
		for _, t := range addTargets {
			if slices.Contains(removeTargets, t) {
				return workerStatePlan{}, fmt.Errorf("%w: cannot both add and remove target %q", ErrInvalidWorkerStateUpdate, t)
			}
			next.Targets = appendUnique(next.Targets, t)
		}
		if len(removeTargets) > 0 {
			filtered := make([]Target, 0, len(next.Targets))
			for _, t := range next.Targets {
				if !slices.Contains(removeTargets, t) {
					filtered = append(filtered, t)
				}
			}
			next.Targets = filtered
		}
		if len(next.Targets) == 0 {
			return workerStatePlan{}, fmt.Errorf("%w: at least one target must remain configured", ErrInvalidWorkerStateUpdate)
		}
	}

	// Validate and compute enabled skill delta.
	var (
		addSkills    []string
		newlyEnabled []string
		toUninstall  []string
	)
	if req.UpdateEnabledSkills {
		validated, err := validateSkillNames(req.EnabledSkills)
		if err != nil {
			return workerStatePlan{}, err
		}
		for _, old := range prev.Enabled {
			if !slices.Contains(validated, old) {
				toUninstall = appendUnique(toUninstall, old)
			}
		}
		for _, added := range validated {
			if !slices.Contains(prev.Enabled, added) {
				newlyEnabled = appendUnique(newlyEnabled, added)
			}
		}
		next.Enabled = validated
	} else {
		var err error
		addSkills, err = validateSkillNames(req.AddSkills)
		if err != nil {
			return workerStatePlan{}, err
		}
		removeSkills, err := validateSkillNames(req.RemoveSkills)
		if err != nil {
			return workerStatePlan{}, err
		}
		for _, name := range addSkills {
			if slices.Contains(removeSkills, name) {
				return workerStatePlan{}, fmt.Errorf("%w: cannot both add and remove skill %q", ErrInvalidWorkerStateUpdate, name)
			}
		}

		nextEnabled := slices.Clone(prev.Enabled)
		if req.AddAllSkills {
			for _, name := range Names() {
				nextEnabled = appendUnique(nextEnabled, name)
			}
		}
		for _, name := range addSkills {
			nextEnabled = appendUnique(nextEnabled, name)
		}
		if req.RemoveAllSkills {
			allNames := Names()
			nextEnabled = []string{}
			for _, name := range allNames {
				toUninstall = appendUnique(toUninstall, name)
			}
		}
		if len(removeSkills) > 0 {
			filtered := make([]string, 0, len(nextEnabled))
			for _, name := range nextEnabled {
				if !slices.Contains(removeSkills, name) {
					filtered = append(filtered, name)
				}
			}
			nextEnabled = filtered
			for _, name := range removeSkills {
				toUninstall = appendUnique(toUninstall, name)
			}
		}
		for _, name := range nextEnabled {
			if !slices.Contains(prev.Enabled, name) {
				newlyEnabled = appendUnique(newlyEnabled, name)
			}
		}
		next.Enabled = nextEnabled
	}

	var uninstallSteps []targetOpStep
	if len(toUninstall) > 0 {
		uninstallTargets := mergeUnique(prev.Targets, next.Targets)
		uninstallOpts := Options{
			Force:         req.Force,
			SkipProtected: !req.Force && !req.StrictConflicts,
		}
		uninstallSteps = make([]targetOpStep, 0, len(uninstallTargets))
		for _, t := range uninstallTargets {
			dir, err := ResolveStandardTargetDir(homeDir, t)
			if err != nil {
				return workerStatePlan{}, fmt.Errorf("%w: %w", ErrInvalidWorkerStateUpdate, err)
			}
			uninstallSteps = append(uninstallSteps, targetOpStep{
				target: t,
				dir:    dir,
				skills: toUninstall,
				opts:   uninstallOpts,
			})
		}
	}

	var syncSteps []targetOpStep
	if len(next.Enabled) > 0 {
		// When the caller explicitly requests specific skills via AddSkills
		// (without AddAllSkills or UpdateEnabledSkills), enforce StrictConflicts
		// only on those requested skills so an unrelated locally modified skill
		// does not block installing a different skill.
		narrowAdd := len(addSkills) > 0 && !req.AddAllSkills && !req.UpdateEnabledSkills
		strictOpts := Options{
			Force:         req.Force,
			SkipProtected: !req.Force && !req.StrictConflicts,
		}
		nonStrictOpts := Options{
			Force:         req.Force,
			SkipProtected: !req.Force,
		}

		for _, t := range next.Targets {
			dir, err := ResolveStandardTargetDir(homeDir, t)
			if err != nil {
				return workerStatePlan{}, fmt.Errorf("%w: %w", ErrInvalidWorkerStateUpdate, err)
			}
			isNewTarget := !slices.Contains(prev.Targets, t)

			if narrowAdd {
				requested := addSkills
				if !req.SyncNow && !req.StrictConflicts && !isNewTarget {
					requested = newlyEnabled
				}
				if len(requested) > 0 {
					syncSteps = append(syncSteps, targetOpStep{
						target: t,
						dir:    dir,
						skills: requested,
						opts:   strictOpts,
					})
				}
				if isNewTarget {
					other := make([]string, 0, len(next.Enabled))
					for _, name := range next.Enabled {
						if !slices.Contains(requested, name) {
							other = append(other, name)
						}
					}
					if len(other) > 0 {
						syncSteps = append(syncSteps, targetOpStep{
							target: t,
							dir:    dir,
							skills: other,
							opts:   nonStrictOpts,
						})
					}
				}
				continue
			}

			var toSync []string
			if req.SyncNow || isNewTarget {
				toSync = next.Enabled
			} else if len(newlyEnabled) > 0 {
				toSync = newlyEnabled
			}
			if len(toSync) > 0 {
				syncSteps = append(syncSteps, targetOpStep{
					target: t,
					dir:    dir,
					skills: toSync,
					opts:   strictOpts,
				})
			}
		}
	}

	return workerStatePlan{
		next:           next,
		toUninstall:    toUninstall,
		uninstallSteps: uninstallSteps,
		syncSteps:      syncSteps,
	}, nil
}

// ApplyWorkerStateUpdate validates req, preflights all target operations,
// removes disabled or explicitly uninstalled skills, syncs affected skills and
// targets, and persists the updated WorkerState only after disk operations
// succeed.
func ApplyWorkerStateUpdate(dataDir, homeDir string, req WorkerStateUpdate) (WorkerState, []TargetOutcome, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(homeDir) == "" {
		return WorkerState{}, nil, errors.New("worker home or data directory is not configured")
	}
	prev, err := LoadWorkerState(dataDir)
	if err != nil {
		return WorkerState{}, nil, err
	}
	plan, err := planWorkerStateUpdate(prev, homeDir, req)
	if err != nil {
		return WorkerState{}, nil, err
	}

	// Preflight every sync and uninstall target before mutating anything on
	// disk so a blocked target or strict conflict never causes partial writes
	// or premature removals.
	type plannedUninstall struct {
		step  targetOpStep
		plans []uninstallPlan
	}
	type plannedInstall struct {
		step     targetOpStep
		plans    []installPlan
		anyWrite bool
	}

	plannedInstalls := make([]plannedInstall, 0, len(plan.syncSteps))
	for _, step := range plan.syncSteps {
		plans, anyWrite, pErr := planInstall(step.dir, step.skills, step.opts)
		if pErr != nil {
			return WorkerState{}, nil, pErr
		}
		plannedInstalls = append(plannedInstalls, plannedInstall{step: step, plans: plans, anyWrite: anyWrite})
	}
	plannedUninstalls := make([]plannedUninstall, 0, len(plan.uninstallSteps))
	for _, step := range plan.uninstallSteps {
		plans, pErr := planUninstall(step.dir, step.skills, step.opts)
		if pErr != nil {
			return WorkerState{}, nil, pErr
		}
		plannedUninstalls = append(plannedUninstalls, plannedUninstall{step: step, plans: plans})
	}

	var outcomes []TargetOutcome
	uninstalled := false
	for _, pu := range plannedUninstalls {
		res, uErr := executeUninstall(pu.step.dir, pu.plans)
		if uErr != nil {
			return WorkerState{}, nil, uErr
		}
		for _, o := range res {
			outcomes = append(outcomes, TargetOutcome{
				Name:   o.Name,
				Target: pu.step.target,
				Action: o.Action,
				Path:   o.Path,
			})
		}
		uninstalled = true
	}

	for _, pi := range plannedInstalls {
		res, sErr := executeInstall(pi.step.dir, pi.plans, pi.anyWrite)
		if sErr != nil {
			if uninstalled {
				// Record the already-completed removals without persisting
				// failed additions so state matches disk.
				remaining := make([]string, 0, len(prev.Enabled))
				for _, name := range prev.Enabled {
					if !slices.Contains(plan.toUninstall, name) {
						remaining = append(remaining, name)
					}
				}
				partial := prev
				partial.Enabled = remaining
				_ = saveWorkerState(dataDir, partial)
			}
			return WorkerState{}, nil, sErr
		}
		for _, o := range res {
			outcomes = append(outcomes, TargetOutcome{
				Name:   o.Name,
				Target: pi.step.target,
				Action: o.Action,
				Path:   o.Path,
			})
		}
	}

	if err := saveWorkerState(dataDir, plan.next); err != nil {
		return WorkerState{}, nil, err
	}
	return normalizeWorkerState(plan.next), outcomes, nil
}

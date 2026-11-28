package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/skills"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func registerSkillsHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register(leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		resp, err := svc.buildWorkerSkillsResponse()
		if err != nil {
			_ = sender.SendError(int32(codes.Internal), err.Error())
			return
		}
		sendProtoResponse(sender, resp)
	})
	registerOwnerGuarded(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, dispatchTracked, svc.handleUpdateWorkerSkills)
}

func (svc *Service) handleUpdateWorkerSkills(_ context.Context, _ channel.Caller, r *leapmuxv1.UpdateWorkerSkillsRequest, sender channel.ResponseWriter) {
	resp, code, err := svc.updateWorkerSkills(r)
	if err != nil {
		_ = sender.SendError(int32(code), err.Error())
		return
	}
	sendProtoResponse(sender, resp)
}

// SyncConfiguredSkills refreshes enabled skills on Worker startup when
// auto-sync is turned on in <DataDir>/skills-state.json. Locally modified or
// unmanaged skill directories are never overwritten.
func (svc *Service) SyncConfiguredSkills() {
	if svc.DataDir == "" || svc.HomeDir == "" {
		return
	}
	svc.skillsMu.Lock()
	defer svc.skillsMu.Unlock()
	st, err := skills.LoadWorkerState(svc.DataDir)
	if err != nil {
		slog.Warn("load worker skills state failed", "error", err)
		return
	}
	if !st.AutoSync || len(st.Enabled) == 0 {
		return
	}
	for _, target := range st.Targets {
		dir, err := skills.ResolveStandardTargetDir(svc.HomeDir, target)
		if err != nil {
			slog.Warn("resolve worker skill target failed", "target", target, "error", err)
			continue
		}
		outcomes, err := skills.SyncEnabled(dir, st.Enabled, false)
		if err != nil {
			slog.Warn("sync worker skills failed", "target", target, "dir", dir, "error", err)
			continue
		}
		for _, o := range outcomes {
			if o.Action != skills.ActionUnchanged {
				slog.Info("synced worker skill on startup", "skill", o.Name, "action", o.Action, "path", o.Path)
			}
		}
	}
}

func (svc *Service) buildWorkerSkillsResponse() (*leapmuxv1.GetWorkerSkillsResponse, error) {
	svc.skillsMu.Lock()
	defer svc.skillsMu.Unlock()
	st, err := skills.LoadWorkerState(svc.DataDir)
	if err != nil {
		return nil, err
	}
	return svc.buildWorkerSkillsResponseForState(st), nil
}

func (svc *Service) buildWorkerSkillsResponseForState(st skills.WorkerState) *leapmuxv1.GetWorkerSkillsResponse {
	targets := st.Targets
	if len(targets) == 0 {
		targets = []skills.Target{skills.TargetAgents}
	}

	type resolvedTarget struct {
		target   skills.Target
		dir      string
		statuses []skills.SkillStatus
	}
	resolved := make([]resolvedTarget, 0, len(targets))
	targetNames := make([]string, 0, len(targets))
	for _, t := range targets {
		targetNames = append(targetNames, string(t))
		var dir string
		if svc.HomeDir != "" {
			if d, err := skills.ResolveStandardTargetDir(svc.HomeDir, t); err == nil {
				dir = d
			}
		}
		var statuses []skills.SkillStatus
		if dir != "" {
			statuses = skills.InspectAll(dir)
		}
		resolved = append(resolved, resolvedTarget{
			target:   t,
			dir:      dir,
			statuses: statuses,
		})
	}

	primaryDir := resolved[0].dir
	bundledVersion := skills.BundledVersion()
	cat := skills.Catalog()
	rows := make([]*leapmuxv1.WorkerSkillStatus, 0, len(cat))
	for i, s := range cat {
		states := make([]skills.State, 0, len(resolved))
		targetStatuses := make([]*leapmuxv1.WorkerSkillTargetStatus, 0, len(resolved))
		var primaryInstalledVersion, primaryPath string
		for idx, rt := range resolved {
			var disk skills.SkillStatus
			if i < len(rt.statuses) {
				disk = rt.statuses[i]
			} else {
				disk = skills.SkillStatus{
					Name:           s.Name,
					Description:    s.Description,
					State:          skills.StateNotInstalled,
					BundledVersion: bundledVersion,
				}
			}
			states = append(states, disk.State)
			targetStatuses = append(targetStatuses, &leapmuxv1.WorkerSkillTargetStatus{
				Target:           string(rt.target),
				State:            string(disk.State),
				InstalledVersion: disk.InstalledVersion,
				Path:             disk.Path,
			})
			if idx == 0 {
				primaryInstalledVersion = disk.InstalledVersion
				primaryPath = disk.Path
			}
		}
		rows = append(rows, &leapmuxv1.WorkerSkillStatus{
			Name:             s.Name,
			Description:      s.Description,
			Enabled:          slices.Contains(st.Enabled, s.Name),
			State:            string(skills.AggregateState(states)),
			InstalledVersion: primaryInstalledVersion,
			BundledVersion:   bundledVersion,
			Path:             primaryPath,
			References:       s.References,
			Targets:          targetStatuses,
		})
	}
	return &leapmuxv1.GetWorkerSkillsResponse{
		AutoSync:         st.AutoSync,
		Targets:          targetNames,
		PrimarySkillsDir: primaryDir,
		Skills:           rows,
	}
}

func rawSkillTargets(names []string) []skills.Target {
	if len(names) == 0 {
		return nil
	}
	out := make([]skills.Target, len(names))
	for i, name := range names {
		out[i] = skills.Target(name)
	}
	return out
}

func workerSkillsErrorCode(err error) codes.Code {
	switch {
	case errors.Is(err, skills.ErrUnknownSkill):
		return codes.NotFound
	case errors.Is(err, skills.ErrInvalidWorkerStateUpdate),
		errors.Is(err, skills.ErrInvalidTarget):
		return codes.InvalidArgument
	case errors.Is(err, skills.ErrSkillModified),
		errors.Is(err, skills.ErrSkillUnmanaged),
		errors.Is(err, skills.ErrUnsafeSymlink):
		return codes.Aborted
	default:
		return codes.Internal
	}
}

func (svc *Service) updateWorkerSkills(r *leapmuxv1.UpdateWorkerSkillsRequest) (*leapmuxv1.UpdateWorkerSkillsResponse, codes.Code, error) {
	if svc.HomeDir == "" || svc.DataDir == "" {
		return nil, codes.FailedPrecondition, fmt.Errorf("worker home or data directory is not configured")
	}
	var autoSync *bool
	if r != nil {
		autoSync = r.AutoSync
	}
	svc.skillsMu.Lock()
	defer svc.skillsMu.Unlock()
	next, outcomes, err := skills.ApplyWorkerStateUpdate(svc.DataDir, svc.HomeDir, skills.WorkerStateUpdate{
		AutoSync:            autoSync,
		UpdateEnabledSkills: r.GetUpdateEnabledSkills(),
		EnabledSkills:       r.GetEnabledSkills(),
		AddSkills:           r.GetAddSkills(),
		RemoveSkills:        r.GetRemoveSkills(),
		AddAllSkills:        r.GetAddAllSkills(),
		RemoveAllSkills:     r.GetRemoveAllSkills(),
		Targets:             rawSkillTargets(r.GetTargets()),
		AddTargets:          rawSkillTargets(r.GetAddTargets()),
		RemoveTargets:       rawSkillTargets(r.GetRemoveTargets()),
		SyncNow:             r.GetSyncNow(),
		Force:               r.GetForce(),
		StrictConflicts:     r.GetStrictConflicts(),
	})
	if err != nil {
		return nil, workerSkillsErrorCode(err), err
	}
	protoOutcomes := make([]*leapmuxv1.WorkerSkillOutcome, 0, len(outcomes))
	for _, o := range outcomes {
		protoOutcomes = append(protoOutcomes, &leapmuxv1.WorkerSkillOutcome{
			Name:   o.Name,
			Target: string(o.Target),
			Action: string(o.Action),
			Path:   o.Path,
		})
	}
	return &leapmuxv1.UpdateWorkerSkillsResponse{
		State:    svc.buildWorkerSkillsResponseForState(next),
		Outcomes: protoOutcomes,
	}, codes.OK, nil
}

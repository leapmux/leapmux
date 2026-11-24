package service

import (
	"context"
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
	registerOwnerGuarded(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, dispatchPlain, svc.handleUpdateWorkerSkills)
}

func (svc *Service) handleUpdateWorkerSkills(_ context.Context, _ channel.Caller, r *leapmuxv1.UpdateWorkerSkillsRequest, sender channel.ResponseWriter) {
	resp, code, err := svc.updateWorkerSkills(r)
	if err != nil {
		_ = sender.SendError(int32(code), err.Error())
		return
	}
	sendProtoResponse(sender, &leapmuxv1.UpdateWorkerSkillsResponse{State: resp})
}

// SyncConfiguredSkills refreshes enabled skills on Worker startup when
// auto-sync is turned on in <DataDir>/skills-state.json. Locally modified or
// unmanaged skill directories are never overwritten.
func (svc *Service) SyncConfiguredSkills() {
	if svc.DataDir == "" || svc.HomeDir == "" {
		return
	}
	st, err := skills.LoadWorkerState(svc.DataDir)
	if err != nil {
		slog.Warn("load worker skills state failed", "error", err)
		return
	}
	if !st.AutoSync || len(st.Enabled) == 0 {
		return
	}
	for _, target := range st.Targets {
		dir, err := skills.ResolveTargetDir(svc.HomeDir, target)
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
			if o.Action != "unchanged" {
				slog.Info("synced worker skill on startup", "skill", o.Name, "action", o.Action, "path", o.Path)
			}
		}
	}
}

func (svc *Service) buildWorkerSkillsResponse() (*leapmuxv1.GetWorkerSkillsResponse, error) {
	st, err := skills.LoadWorkerState(svc.DataDir)
	if err != nil {
		return nil, err
	}
	primaryTarget := skills.TargetAgents
	if len(st.Targets) > 0 {
		primaryTarget = st.Targets[0]
	}
	primaryDir := ""
	if svc.HomeDir != "" {
		if d, err := skills.ResolveTargetDir(svc.HomeDir, primaryTarget); err == nil {
			primaryDir = d
		}
	}
	cat := skills.Catalog()
	rows := make([]*leapmuxv1.WorkerSkillStatus, 0, len(cat))
	for _, s := range cat {
		var disk skills.SkillStatus
		if primaryDir != "" {
			disk = skills.InspectOne(primaryDir, s)
		} else {
			disk = skills.SkillStatus{
				Name:           s.Name,
				Description:    s.Description,
				State:          skills.StateNotInstalled,
				BundledVersion: skills.BundledVersion(),
			}
		}
		rows = append(rows, &leapmuxv1.WorkerSkillStatus{
			Name:             s.Name,
			Description:      s.Description,
			Enabled:          slices.Contains(st.Enabled, s.Name),
			State:            string(disk.State),
			InstalledVersion: disk.InstalledVersion,
			BundledVersion:   disk.BundledVersion,
			Path:             disk.Path,
		})
	}
	return &leapmuxv1.GetWorkerSkillsResponse{
		AutoSync:         st.AutoSync,
		Targets:          st.Targets,
		PrimarySkillsDir: primaryDir,
		Skills:           rows,
	}, nil
}

func (svc *Service) updateWorkerSkills(r *leapmuxv1.UpdateWorkerSkillsRequest) (*leapmuxv1.GetWorkerSkillsResponse, codes.Code, error) {
	if svc.HomeDir == "" || svc.DataDir == "" {
		return nil, codes.FailedPrecondition, fmt.Errorf("worker home or data directory is not configured")
	}
	prev, err := skills.LoadWorkerState(svc.DataDir)
	if err != nil {
		return nil, codes.Internal, err
	}
	next := prev
	if r.AutoSync != nil {
		next.AutoSync = r.GetAutoSync()
	}
	if len(r.GetTargets()) > 0 {
		validated := make([]string, 0, len(r.GetTargets()))
		for _, raw := range r.GetTargets() {
			norm, nErr := skills.NormalizeTarget(raw)
			if nErr != nil {
				return nil, codes.InvalidArgument, nErr
			}
			if !slices.Contains(validated, norm) {
				validated = append(validated, norm)
			}
		}
		next.Targets = validated
	}
	var newlyDisabled []string
	if r.GetUpdateEnabledSkills() {
		validated := make([]string, 0, len(r.GetEnabledSkills()))
		for _, name := range r.GetEnabledSkills() {
			if _, ok := skills.Lookup(name); !ok {
				return nil, codes.InvalidArgument, fmt.Errorf("unknown skill %q", name)
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

	// Persist configuration before writing skill files so Worker restarts always
	// reflect the user's latest choice.
	if err := skills.SaveWorkerState(svc.DataDir, next); err != nil {
		return nil, codes.Internal, err
	}

	// Uninstall newly disabled skills from previously configured targets (never
	// deleting locally modified or unmanaged files unless force is true).
	if len(newlyDisabled) > 0 {
		allTargets := make([]string, 0, len(prev.Targets)+len(next.Targets))
		for _, t := range append(slices.Clone(prev.Targets), next.Targets...) {
			if !slices.Contains(allTargets, t) {
				allTargets = append(allTargets, t)
			}
		}
		for _, target := range allTargets {
			dir, err := skills.ResolveTargetDir(svc.HomeDir, target)
			if err != nil {
				continue
			}
			for _, name := range newlyDisabled {
				s, ok := skills.Lookup(name)
				if !ok {
					continue
				}
				st := skills.InspectOne(dir, s)
				if st.State == skills.StateNotInstalled {
					continue
				}
				if (st.State == skills.StateModified || st.State == skills.StateUnmanaged) && !r.GetForce() {
					continue
				}
				if _, err := skills.Uninstall(dir, []string{name}, skills.InstallOptions{Force: r.GetForce()}); err != nil {
					return nil, codes.Internal, err
				}
			}
		}
	}

	// Sync enabled skills whenever skills/targets changed or sync_now was asked.
	if len(next.Enabled) > 0 && (r.GetUpdateEnabledSkills() || len(r.GetTargets()) > 0 || r.GetSyncNow()) {
		for _, target := range next.Targets {
			dir, err := skills.ResolveTargetDir(svc.HomeDir, target)
			if err != nil {
				return nil, codes.InvalidArgument, err
			}
			if _, err := skills.SyncEnabled(dir, next.Enabled, r.GetForce()); err != nil {
				return nil, codes.Internal, err
			}
		}
	}

	resp, err := svc.buildWorkerSkillsResponse()
	if err != nil {
		return nil, codes.Internal, err
	}
	return resp, codes.OK, nil
}

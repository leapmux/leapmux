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
	sendProtoResponse(sender, &leapmuxv1.UpdateWorkerSkillsResponse{State: resp})
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
	svc.skillsMu.Lock()
	defer svc.skillsMu.Unlock()
	st, err := skills.LoadWorkerState(svc.DataDir)
	if err != nil {
		return nil, err
	}
	return svc.buildWorkerSkillsResponseForState(st), nil
}

func (svc *Service) buildWorkerSkillsResponseForState(st skills.WorkerState) *leapmuxv1.GetWorkerSkillsResponse {
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
	}
}

func (svc *Service) updateWorkerSkills(r *leapmuxv1.UpdateWorkerSkillsRequest) (*leapmuxv1.GetWorkerSkillsResponse, codes.Code, error) {
	if svc.HomeDir == "" || svc.DataDir == "" {
		return nil, codes.FailedPrecondition, fmt.Errorf("worker home or data directory is not configured")
	}
	svc.skillsMu.Lock()
	defer svc.skillsMu.Unlock()
	next, err := skills.ApplyWorkerStateUpdate(svc.DataDir, svc.HomeDir, skills.WorkerStateUpdate{
		AutoSync:            r.AutoSync,
		Targets:             r.GetTargets(),
		UpdateEnabledSkills: r.GetUpdateEnabledSkills(),
		EnabledSkills:       r.GetEnabledSkills(),
		SyncNow:             r.GetSyncNow(),
		Force:               r.GetForce(),
	})
	if err != nil {
		if errors.Is(err, skills.ErrInvalidWorkerStateUpdate) {
			return nil, codes.InvalidArgument, err
		}
		return nil, codes.Internal, err
	}
	return svc.buildWorkerSkillsResponseForState(next), codes.OK, nil
}

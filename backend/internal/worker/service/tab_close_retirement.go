package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// tabCloseRetirement retains the live-row proof from the actual close write.
type tabCloseRetirement struct {
	RetiredLiveRow bool
	Descendants    []string
	ClosedAgentIDs []string
	PayloadRevoked bool
}

// retireTabResource writes SQL only. Its caller publishes effects after commit.
func (svc *Service) retireTabResource(ctx context.Context, q *db.Queries, key ownedTabKey) (tabCloseRetirement, error) {
	var retirement tabCloseRetirement
	switch key.tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		ids, err := q.ListAgentTreeIDs(ctx, key.tabID)
		if err != nil {
			return retirement, err
		}
		for _, id := range ids {
			if id != key.tabID {
				retirement.Descendants = append(retirement.Descendants, id)
			}
		}
		var firstErr error
		for _, id := range ids {
			retired, err := rowsAffected(q.CloseAgent(ctx, id))
			if err != nil {
				if firstErr == nil {
					firstErr = err
				} else {
					slog.Warn("close another agent descendant failed", "agent_id", key.tabID, "descendant_id", id, "error", err)
				}
				continue
			}
			if id == key.tabID {
				retirement.RetiredLiveRow = retired
			}
			retirement.ClosedAgentIDs = append(retirement.ClosedAgentIDs, id)
		}
		return retirement, firstErr
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		retired, err := rowsAffected(q.CloseTerminal(ctx, key.tabID))
		retirement.RetiredLiveRow = retired
		return retirement, err
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		retired, err := revokeTabPayloadRow(ctx, q, key.userID, key.tabID)
		retirement.RetiredLiveRow = retired
		retirement.PayloadRevoked = retired
		return retirement, err
	default:
		return retirement, errors.New("the tab type cannot retire a resource")
	}
}

func (svc *Service) applyTabCloseEffects(ctx context.Context, key ownedTabKey, retirement tabCloseRetirement) {
	if key.tabType == leapmuxv1.TabType_TAB_TYPE_AGENT {
		svc.Output.CleanupChildAgents(retirement.Descendants)
		for _, id := range retirement.ClosedAgentIDs {
			if err := svc.InputQueue.RetireAgent(ctx, id); err != nil && !errors.Is(err, inputqueue.ErrManagerStopped) {
				slog.Warn("retire the input queue of a closed agent failed", "agent_id", id, "error", err)
			}
		}
	}
	if retirement.PayloadRevoked {
		svc.TabPayloads.publishRevoked(key.userID, key.tabID)
	}
}

func revokeTabPayloadRow(ctx context.Context, q *db.Queries, userID, tabID string) (bool, error) {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return false, errors.New("revoke tab payload: required field empty")
	}
	result, err := q.DeleteWorkerTabPayload(ctx, db.DeleteWorkerTabPayloadParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		return false, fmt.Errorf("delete worker_tab_payload: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete worker_tab_payload: %w", err)
	}
	return affected != 0, nil
}

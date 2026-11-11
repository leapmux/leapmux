package service

import (
	"context"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

func (svc *Service) handleRegisterTabPayload(ctx context.Context, caller channel.Caller, r *leapmuxv1.RegisterTabPayloadRequest, sender channel.ResponseWriter) {
	kind, err := tabPayloadType(r.GetPayload())
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}
	if !callerReadsTabType(caller, kind) {
		sendPermissionDenied(sender, "scope required for this tab kind")
		return
	}
	svc.coordinateCreation(ctx, caller, r, r.GetCreation(), kind, sender, func(op *creationOperation, writer channel.ResponseWriter) { svc.preparePayloadCreation(op, writer) })
}
func (svc *Service) preparePayloadCreation(op *creationOperation, sender channel.ResponseWriter) {
	if op.record == nil {
		request := op.request.(*leapmuxv1.RegisterTabPayloadRequest)
		stored := proto.CloneOf(request.GetPayload())
		workingDir, err := resolveTabPayloadWorkingDir(stored)
		if err != nil {
			sendInvalidArgument(sender, err.Error())
			return
		}
		stored.WorkingDir = workingDir
		release, err := svc.worktreeLifecycle.admit(workingDir, tabPayloadLinkDir(stored))
		if err != nil {
			sendFailedPrecondition(sender, err.Error())
			return
		}
		err = svc.persistPayloadCreation(op, stored)
		release()
		if err != nil {
			if errors.Is(err, ErrInvalidTabPayload) {
				sendInvalidArgument(sender, err.Error())
			} else {
				sendInternalError(sender, err.Error())
			}
			return
		}
		if svc.afterPayloadPersistenceFn != nil {
			svc.afterPayloadPersistenceFn(op.coordinator.ctx)
		}
	}
	publication, err := op.publish()
	if err != nil {
		sendCreationError(sender, err)
		return
	}
	sendProtoResponse(sender, &leapmuxv1.RegisterTabPayloadResponse{Publication: publication})
}

// persistPayloadCreation orders registration before cancellation can revoke the payload.
func (svc *Service) persistPayloadCreation(op *creationOperation, stored *leapmuxv1.TabPayload) error {
	blob, err := proto.Marshal(stored)
	if err != nil {
		return err
	}
	unlock := op.coordinator.lock(op.key())
	defer unlock()
	err = op.persistLocked(&payloadCreationRuntime{}, func(ctx context.Context, q *db.Queries) error {
		if err := q.InsertWorkerTabPayload(ctx, db.InsertWorkerTabPayloadParams{UserID: op.key().owner, TabID: op.key().tabID, TabType: int64(op.publication.GetTabType()), Payload: blob, WorkingDir: stored.WorkingDir}); err != nil {
			return err
		}
		svc.TabPayloads.linkTabToWorktreeUsing(ctx, q, op.key().owner, tabPayloadLinkDir(stored), op.publication.GetTabType(), op.key().tabID)
		return nil
	})
	if err != nil {
		return err
	}
	svc.TabPayloads.publishRegistered(op.caller.UserID, op.key().tabID, stored)
	return nil
}

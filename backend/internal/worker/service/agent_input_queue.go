package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

type agentInputQueueAdapter struct {
	svc *Service
}

const queuedInputTextPreviewBytes = 4096

func queuedInputTextPreview(value string) string {
	if len(value) <= queuedInputTextPreviewBytes {
		return value
	}
	end := queuedInputTextPreviewBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "…"
}

func queueAttachments(attachments []*leapmuxv1.Attachment) []inputqueue.Attachment {
	result := make([]inputqueue.Attachment, len(attachments))
	for i := range attachments {
		result[i] = inputqueue.Attachment{
			Filename: attachments[i].GetFilename(),
			MimeType: attachments[i].GetMimeType(),
			Data:     append([]byte(nil), attachments[i].GetData()...),
		}
	}
	return result
}

func providerAttachments(attachments []inputqueue.Attachment) []*leapmuxv1.Attachment {
	result := make([]*leapmuxv1.Attachment, len(attachments))
	for i := range attachments {
		result[i] = &leapmuxv1.Attachment{
			Filename: attachments[i].Filename,
			MimeType: attachments[i].MimeType,
			Data:     append([]byte(nil), attachments[i].Data...),
		}
	}
	return result
}

func (a *agentInputQueueAdapter) Dispatch(item inputqueue.Item) (inputqueue.DispatchResult, error) {
	svc := a.svc
	spanLines := svc.Output.snapshotPassthroughSpanLines(item.AgentID)
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), item.AgentID)
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}
	if dbAgent.StartupError != "" && !svc.Agents.HasAgent(item.AgentID) {
		return inputqueue.DispatchResult{}, fmt.Errorf("agent failed to start; open a new agent")
	}
	attachments, err := agent.NormalizeAttachmentsForProvider(dbAgent.AgentProvider, providerAttachments(item.Attachments))
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}

	if dbAgent.ParentAgentID.Valid {
		if item.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE &&
			item.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK {
			return inputqueue.DispatchResult{}, fmt.Errorf("subagent accepts only messages")
		}
		row, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(bgCtx(), item.AgentID)
		if err != nil {
			return inputqueue.DispatchResult{}, err
		}
		if !svc.Agents.HasAgent(row.OwnerAgentID) {
			return inputqueue.DispatchResult{}, agent.ErrAgentNotFound
		}
		if err := svc.Agents.SendChildInput(row.OwnerAgentID, row.RowKey, item.Text, attachments); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: true, SpanLines: spanLines}, nil
	}

	ensureRunning := func() error {
		if svc.Agents.HasAgent(item.AgentID) {
			return nil
		}
		resumeID := svc.resolveResumeSessionID(item.AgentID, dbAgent.AgentSessionID, dbAgent.Resumed)
		return svc.ensureAgentRunning(item.AgentID, &resumeID, interactiveStart)
	}

	switch item.Kind {
	case leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT:
		afterAccept, err := svc.prepareClearContext(item.AgentID)
		if err != nil {
			return inputqueue.DispatchResult{}, err
		}
		return inputqueue.DispatchResult{StartsTurn: false, SpanLines: spanLines, AfterAccept: afterAccept}, nil
	case leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT:
		if err := ensureRunning(); err != nil {
			return inputqueue.DispatchResult{}, err
		}
		err := svc.Agents.CompactContext(item.AgentID)
		if errors.Is(err, agent.ErrCompactionUnsupported) {
			err = svc.Agents.SendInput(item.AgentID, item.Text, attachments)
		}
		if err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: true, SpanLines: spanLines}, nil
	case leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION:
		if item.PrepareContext {
			if err := svc.preparePlanExecutionContext(item.AgentID, item.TargetMode, dbAgent); err != nil {
				return inputqueue.DispatchResult{}, err
			}
		} else if err := ensureRunning(); err != nil {
			return inputqueue.DispatchResult{}, err
		}
		if err := svc.Agents.SendInput(item.AgentID, item.Text, attachments); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: true, SpanLines: svc.Output.snapshotPassthroughSpanLines(item.AgentID)}, nil
	default:
		if err := ensureRunning(); err != nil {
			return inputqueue.DispatchResult{}, err
		}
		if err := svc.Agents.SendInput(item.AgentID, item.Text, attachments); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: true, SpanLines: spanLines}, nil
	}
}

func classifyQueueDeliveryError(err error) error {
	return &inputqueue.DeliveryError{Err: err, Uncertain: errors.Is(err, agent.ErrDeliveryUncertain)}
}

func (a *agentInputQueueAdapter) Steer(item inputqueue.Item) (inputqueue.DispatchResult, error) {
	dbAgent, err := a.svc.Queries.GetAgentByID(bgCtx(), item.AgentID)
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}
	attachments, err := agent.NormalizeAttachmentsForProvider(dbAgent.AgentProvider, providerAttachments(item.Attachments))
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}
	if dbAgent.ParentAgentID.Valid {
		row, rowErr := a.svc.Queries.GetAgentBackgroundTaskByChildAgentID(bgCtx(), item.AgentID)
		if rowErr != nil {
			return inputqueue.DispatchResult{}, rowErr
		}
		err = a.svc.Agents.SteerChildInput(row.OwnerAgentID, row.RowKey, item.Text, attachments)
	} else {
		err = a.svc.Agents.SteerInput(item.AgentID, item.Text, attachments)
	}
	if errors.Is(err, agent.ErrNoActiveTurn) {
		return inputqueue.DispatchResult{}, inputqueue.ErrTurnEnded
	}
	return inputqueue.DispatchResult{
		StartsTurn: true,
		SpanLines:  a.svc.Output.snapshotPassthroughSpanLines(item.AgentID),
	}, err
}

func (a *agentInputQueueAdapter) SupportsSteering(agentID string) bool {
	dbAgent, err := a.svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return false
	}
	if dbAgent.ParentAgentID.Valid {
		row, err := a.svc.Queries.GetAgentBackgroundTaskByChildAgentID(bgCtx(), agentID)
		return err == nil && a.svc.Agents.SupportsSteering(row.OwnerAgentID)
	}
	return a.svc.Agents.SupportsSteering(agentID)
}

func (a *agentInputQueueAdapter) QueueChanged(snapshot inputqueue.Snapshot) {
	a.svc.Watchers.BroadcastAgentEvent(snapshot.AgentID, &leapmuxv1.AgentEvent{
		AgentId: snapshot.AgentID,
		Event: &leapmuxv1.AgentEvent_InputQueueChanged{
			InputQueueChanged: &leapmuxv1.AgentInputQueueChanged{Snapshot: queueSnapshotProto(snapshot)},
		},
	})
}

func (a *agentInputQueueAdapter) InputAccepted(message inputqueue.AcceptedTranscript) {
	a.svc.Watchers.BroadcastAgentEvent(message.AgentID, &leapmuxv1.AgentEvent{
		AgentId: message.AgentID,
		Event: &leapmuxv1.AgentEvent_AgentMessage{
			AgentMessage: &leapmuxv1.AgentChatMessage{
				Id: message.ID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
				Content: message.Content, ContentCompression: message.ContentCompression,
				Seq: message.Seq, AgentProvider: message.AgentProvider,
				MarkType: message.MarkType, CreatedAt: message.CreatedAt,
			},
		},
	})
}

func queueSnapshotProto(snapshot inputqueue.Snapshot) *leapmuxv1.AgentInputQueueSnapshot {
	result := &leapmuxv1.AgentInputQueueSnapshot{
		AgentId: snapshot.AgentID, Revision: snapshot.Revision,
		Paused: snapshot.Paused, PauseReason: snapshot.PauseReason,
		ActiveTurn: snapshot.ActiveTurn, ActiveTurnKind: snapshot.ActiveTurnKind,
		Items: make([]*leapmuxv1.QueuedAgentInput, len(snapshot.Items)),
	}
	for i := range snapshot.Items {
		item := snapshot.Items[i]
		attachments := make([]*leapmuxv1.QueuedAgentInputAttachment, len(item.Metadata))
		for j := range item.Metadata {
			attachments[j] = &leapmuxv1.QueuedAgentInputAttachment{
				Filename: item.Metadata[j].Filename, MimeType: item.Metadata[j].MimeType,
				Size: item.Metadata[j].Size, Order: item.Metadata[j].Order,
			}
		}
		result.Items[i] = &leapmuxv1.QueuedAgentInput{
			Id: item.ID, AgentId: item.AgentID, Kind: item.Kind, Text: queuedInputTextPreview(item.Text),
			Attachments: attachments, Order: item.Order, State: item.State,
			Error: item.Error, EditOwnerClientId: item.EditOwner, Version: item.Version,
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		}
	}
	return result
}

func registerAgentInputQueueHandlers(d registrar, svc *Service) {
	registerAgentGatedByID(d, "EnqueueAgentInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.EnqueueAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Enqueue(bgCtx(), inputqueue.NewItem{
				ID: r.GetInputId(), AgentID: r.GetAgentId(), Kind: r.GetKind(), Text: r.GetText(),
				Attachments:      queueAttachments(r.GetAttachments()),
				ReclassifyOnEdit: r.GetKind() == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
			})
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "ListAgentInputQueue", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, r *leapmuxv1.ListAgentInputQueueRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Snapshot(ctx, r.GetAgentId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.ListAgentInputQueueResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "BeginQueuedAgentInputEdit", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.BeginQueuedAgentInputEditRequest, sender channel.ResponseWriter) {
			snapshot, fullText, attachments, err := svc.InputQueue.BeginEdit(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetClientId(), r.GetTakeover())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.BeginQueuedAgentInputEditResponse{
				Snapshot: queueSnapshotProto(snapshot), Attachments: providerAttachments(attachments), Text: fullText,
			})
		})

	registerAgentGatedByID(d, "UpdateQueuedAgentInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.UpdateQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Update(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetClientId(), r.GetExpectedVersion(), r.GetText(), queueAttachments(r.GetAttachments()))
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.UpdateQueuedAgentInputResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "CancelQueuedAgentInputEdit", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.CancelQueuedAgentInputEditRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.CancelEdit(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetClientId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.CancelQueuedAgentInputEditResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "DeleteQueuedAgentInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.DeleteQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Delete(bgCtx(), r.GetAgentId(), r.GetInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.DeleteQueuedAgentInputResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "MoveQueuedAgentInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.MoveQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Move(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetBeforeInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.MoveQueuedAgentInputResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "SetAgentInputQueuePaused", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.SetAgentInputQueuePausedRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.SetPaused(bgCtx(), r.GetAgentId(), r.GetPaused())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.SetAgentInputQueuePausedResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "SteerQueuedAgentInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.SteerQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Steer(bgCtx(), r.GetAgentId(), r.GetInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.SteerQueuedAgentInputResponse{Snapshot: queueSnapshotProto(snapshot)})
		})

	registerAgentGatedByID(d, "RetryQueuedAgentInput", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.RetryQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Retry(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetConfirmDeliveryUncertain())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.RetryQueuedAgentInputResponse{Snapshot: queueSnapshotProto(snapshot)})
		})
}

func sendQueueError(sender channel.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, inputqueue.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		sendNotFoundError(sender, err.Error())
	case errors.Is(err, inputqueue.ErrConflict), errors.Is(err, inputqueue.ErrEditOwned),
		errors.Is(err, inputqueue.ErrEditOwnerMismatch), errors.Is(err, inputqueue.ErrVersionConflict),
		errors.Is(err, inputqueue.ErrNotHead), errors.Is(err, inputqueue.ErrRetryState),
		errors.Is(err, inputqueue.ErrUncertainConfirmation), errors.Is(err, inputqueue.ErrTurnEnded),
		errors.Is(err, inputqueue.ErrSteeringState), errors.Is(err, inputqueue.ErrSteeringUnsupported):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, inputqueue.ErrInvalidInput), errors.Is(err, inputqueue.ErrQueueFull),
		errors.Is(err, inputqueue.ErrItemTooLarge), errors.Is(err, inputqueue.ErrQueueAttachmentsLarge):
		sendInvalidArgument(sender, err.Error())
	default:
		slog.Error("agent input queue operation failed", "error", err)
		sendInternalError(sender, "agent input queue operation failed")
	}
	return true
}

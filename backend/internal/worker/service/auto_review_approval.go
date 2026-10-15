package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/grpc/codes"
)

// errAutoReviewApprovalInFlight reports a second approval of one review while the
// first one still runs.
var errAutoReviewApprovalInFlight = errors.New("an approval of this automatic review is already in progress")

// registerAutoReviewApprovalHandler registers ApproveAutoReviewDenial.
func registerAutoReviewApprovalHandler(d registrar, svc *Service) {
	// ApproveAutoReviewDenial records the user's approval of one action that the
	// agent's automatic reviewer denied, so the agent may retry that action once.
	//
	// The handler refuses subagent approvals. A review belongs to the subagent's thread,
	// so the approval must never reach its root instead.
	registerAgentGuarded(d, "ApproveAutoReviewDenial", leapmuxv1.Scope_SCOPE_AGENT_WRITE,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.ApproveAutoReviewDenialRequest, dbAgent db.Agent, sender channel.ResponseWriter) {
			if r.GetMessageId() == "" || r.GetReviewId() == "" {
				sendInvalidArgument(sender, "an approval needs a message ID and a review ID")
				return
			}
			if dbAgent.ParentAgentID.Valid {
				sendFailedPrecondition(sender, "an automatic review can be approved only in the main agent")
				return
			}
			if err := svc.approveAutoReviewDenial(dbAgent, r.GetMessageId(), r.GetReviewId()); err != nil {
				slog.Warn("approve automatic review denial failed", "agent_id", dbAgent.ID, "review_id", r.GetReviewId(), "error", err)
				sendAutoReviewApprovalError(sender, err)
				return
			}
			sendProtoResponse(sender, &leapmuxv1.ApproveAutoReviewDenialResponse{})
		})
}

// approveAutoReviewDenial reserves delivery before it asks the provider to approve a review.
// The durable claim prevents duplicate delivery after a storage failure or restart.
// A retry of a delivered approval completes its transcript writes without another provider call.
func (svc *Service) approveAutoReviewDenial(dbAgent db.Agent, messageID, reviewID string) error {
	// The guard comes before the read of the record. A second approval that passed
	// the read while the first one still ran would otherwise reach the provider too.
	flight := dbAgent.ID + "\x00" + reviewID
	if _, busy := svc.autoReviewApprovals.LoadOrStore(flight, struct{}{}); busy {
		return errAutoReviewApprovalInFlight
	}
	defer svc.autoReviewApprovals.Delete(flight)

	row, err := svc.Queries.GetMessageByAgentAndID(bgCtx(), db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: dbAgent.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ErrAutoReviewNotFound
	}
	if err != nil {
		return fmt.Errorf("read the transcript row: %w", err)
	}
	// The provider writes its reviews as AGENT rows, and only its own rows hold its
	// wire shape.
	if row.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT || row.AgentProvider != dbAgent.AgentProvider {
		return agent.ErrAutoReviewNotFound
	}
	entries, err := storedRowEntries(row)
	if err != nil {
		return err
	}
	approvals, err := storedAutoReviewApprovals(row)
	if err != nil {
		return err
	}
	claim, err := svc.Queries.GetAutoReviewApproval(bgCtx(), db.GetAutoReviewApprovalParams{
		AgentID: dbAgent.ID, AgentSessionID: row.AgentSessionID, ReviewID: reviewID,
	})
	if err == nil {
		return svc.finishAutoReviewApproval(dbAgent, row, claim)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the approval claim: %w", err)
	}
	if slices.Contains(approvals, reviewID) {
		return nil
	}
	claimed, err := svc.Queries.ClaimAutoReviewApproval(bgCtx(), db.ClaimAutoReviewApprovalParams{
		AgentID: dbAgent.ID, AgentSessionID: row.AgentSessionID, ReviewID: reviewID,
		MessageID: row.ID, State: leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_UNCONFIRMED,
	})
	if err != nil {
		return fmt.Errorf("reserve the approval: %w", err)
	}
	if claimed == 0 {
		return errAutoReviewRowChanged
	}
	if deliveryErr := svc.Agents.ApproveAutoReviewDenial(dbAgent.ID, entries, reviewID); deliveryErr != nil {
		if errors.Is(deliveryErr, agent.ErrDeliveryUncertain) {
			return deliveryErr
		}
		// Only a definite refusal permits another delivery attempt.
		releaseErr := svc.Queries.ReleaseAutoReviewApproval(bgCtx(), db.ReleaseAutoReviewApprovalParams{
			AgentID: dbAgent.ID, AgentSessionID: row.AgentSessionID, ReviewID: reviewID,
			State: leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_UNCONFIRMED,
		})
		return errors.Join(deliveryErr, releaseErr)
	}
	if err := svc.setAutoReviewApprovalState(dbAgent.ID, row.AgentSessionID, reviewID,
		leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_UNCONFIRMED,
		leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_DELIVERED); err != nil {
		return fmt.Errorf("%w: record the approval delivery: %w", agent.ErrDeliveryUncertain, err)
	}
	return svc.finishAutoReviewApproval(dbAgent, row, db.AutoReviewApproval{
		AgentID: dbAgent.ID, AgentSessionID: row.AgentSessionID, ReviewID: reviewID,
		MessageID: row.ID, State: leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_DELIVERED,
	})
}

// finishAutoReviewApproval projects a confirmed delivery into the transcript.
func (svc *Service) finishAutoReviewApproval(dbAgent db.Agent, row db.Message, claim db.AutoReviewApproval) error {
	if claim.MessageID != row.ID {
		return agent.ErrAutoReviewNotFound
	}
	switch claim.State {
	case leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_UNCONFIRMED:
		return agent.ErrDeliveryUncertain
	case leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_COMPLETED:
		return nil
	case leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_DELIVERED:
		if err := svc.Output.recordAutoReviewApproval(dbAgent.ID, row.ID, claim.ReviewID); err != nil {
			return fmt.Errorf("record the approval on its transcript row: %w", err)
		}
		key := autoReviewApprovalKey(row.AgentSessionID, claim.ReviewID)
		if err := svc.Output.persistAutoReviewApproval(dbAgent.ID, dbAgent.AgentProvider, row.AgentSessionID, claim.ReviewID, key); err != nil {
			return fmt.Errorf("write the approval notice: %w", err)
		}
		return svc.setAutoReviewApprovalState(dbAgent.ID, row.AgentSessionID, claim.ReviewID,
			leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_DELIVERED,
			leapmuxv1.AutoReviewApprovalState_AUTO_REVIEW_APPROVAL_STATE_COMPLETED)
	default:
		return fmt.Errorf("unknown approval delivery state: %v", claim.State)
	}
}

func (svc *Service) setAutoReviewApprovalState(agentID, sessionID, reviewID string, previous, next leapmuxv1.AutoReviewApprovalState) error {
	updated, err := svc.Queries.SetAutoReviewApprovalState(bgCtx(), db.SetAutoReviewApprovalStateParams{
		AgentID: agentID, AgentSessionID: sessionID, ReviewID: reviewID, RequiredState: previous, State: next,
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return errAutoReviewRowChanged
	}
	return nil
}

// storedAutoReviewApprovals returns the review ids whose approval the row records.
func storedAutoReviewApprovals(row db.Message) ([]string, error) {
	supplement, err := parseStoredRowSupplement(row)
	if err != nil {
		return nil, err
	}
	return decodeAutoReviewApprovals(supplement.MetadataMember(contracts.MessageMetadataFieldAutoReviewApprovals))
}

// decodeAutoReviewApprovals reads the approvals member: an array of review ids. An
// absent member records no approval.
func decodeAutoReviewApprovals(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var approvals []string
	if err := json.Unmarshal(raw, &approvals); err != nil || approvals == nil {
		return nil, fmt.Errorf("the stored %s member must be an array of review ids", contracts.MessageMetadataFieldAutoReviewApprovals)
	}
	return approvals, nil
}

func parseStoredRowSupplement(row db.Message) (*agent.ParsedMessageSupplement, error) {
	stored, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
	if err != nil {
		return nil, fmt.Errorf("decompress the stored supplement: %w", err)
	}
	return agent.ParseStoredMessageSupplement(stored)
}

// errAutoReviewRowChanged reports that the review row changed or left while the
// approval was recorded on it.
var errAutoReviewRowChanged = errors.New("the transcript row of the review changed while the approval was recorded")

// recordAutoReviewApproval adds reviewID to the approvals that the row's metadata
// records, and sends the changed row to every watcher, so each browser draws the
// approval in place.
//
// The write holds the locks of the notification writer. A notification that merges
// into this row then reads the record, and a delete cannot fall between the write and
// the queued broadcast. The queue drains after the locks are released, as every other
// writer drains it.
func (h *OutputHandler) recordAutoReviewApproval(agentID, messageID, reviewID string) error {
	defer h.watcher.DrainAgentEvents(agentID)
	mutation := h.transcriptMutationMutex(h.resolveRoot(agentID, ""))
	mutation.RLock()
	defer mutation.RUnlock()
	mutex := h.notifMutex(agentID)
	mutex.Lock()
	defer mutex.Unlock()

	ctx := bgCtx()
	row, err := h.queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return errAutoReviewRowChanged
	}
	if err != nil {
		return err
	}
	supplement, err := parseStoredRowSupplement(row)
	if err != nil {
		return err
	}
	approvals, err := decodeAutoReviewApprovals(supplement.MetadataMember(contracts.MessageMetadataFieldAutoReviewApprovals))
	if err != nil {
		return err
	}
	if slices.Contains(approvals, reviewID) {
		return nil
	}
	encoded, err := json.Marshal(append(approvals, reviewID))
	if err != nil {
		return err
	}
	next, err := supplement.WithMetadataMember(contracts.MessageMetadataFieldAutoReviewApprovals, encoded)
	if err != nil {
		return err
	}
	compressed, compression := compressOptionalSupplement(next)
	updated, err := h.queries.EnrichMessageContent(ctx, db.EnrichMessageContentParams{
		ID: row.ID, AgentID: agentID,
		OriginalContent: row.Content, OriginalCompression: row.ContentCompression,
		PreviousRevision:    row.SupplementalRevision,
		SupplementalContent: compressed, SupplementalContentCompression: compression,
		PreviousSupplementalContent:            row.SupplementalContent,
		PreviousSupplementalContentCompression: row.SupplementalContentCompression,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errAutoReviewRowChanged
	}
	if err != nil {
		return err
	}
	message, err := messageToProto(&updated)
	if err != nil {
		return err
	}
	h.enqueueMessage(agentID, message)
	return nil
}

// autoReviewApprovalKey hashes the session and review IDs to produce a fixed-length notice key.
func autoReviewApprovalKey(agentSessionID, reviewID string) string {
	sum := sha256.Sum256([]byte(agentSessionID + "\x00" + reviewID))
	return fmt.Sprintf("auto-review-approved:%x", sum)
}

// storedRowEntries returns the raw entries of one stored transcript row: each
// member of a notification thread, or the row itself.
func storedRowEntries(row db.Message) ([]json.RawMessage, error) {
	content, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	if err != nil {
		return nil, fmt.Errorf("decompress the transcript row: %w", err)
	}
	wrapper, err := unwrapNotifContent(content)
	if err == nil && wrapper.Type == notifThreadWrapperType {
		return wrapper.Messages, nil
	}
	return []json.RawMessage{content}, nil
}

// sendAutoReviewApprovalError maps an approval failure to its status code.
func sendAutoReviewApprovalError(sender channel.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrAutoReviewApprovalUnsupported):
		sendFailedPrecondition(sender, "this agent cannot approve an automatic review")
	case errors.Is(err, agent.ErrAutoReviewNotFound):
		sendNotFoundError(sender, "the transcript row holds no such automatic review")
	case errors.Is(err, agent.ErrAutoReviewNotDenied),
		errors.Is(err, agent.ErrAutoReviewThreadRetired),
		errors.Is(err, errAutoReviewApprovalInFlight),
		errors.Is(err, agent.ErrDeliveryUncertain):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, agent.ErrAgentNotFound):
		sendNotFoundError(sender, "agent not found or not running")
	case errors.Is(err, context.DeadlineExceeded):
		_ = sender.SendError(int32(codes.DeadlineExceeded), err.Error())
	default:
		sendInternalError(sender, err.Error())
	}
}

// persistAutoReviewApproval writes the notice that states the user's approval of one
// denied review at its place in the transcript, once for each idempotency key.
func (h *OutputHandler) persistAutoReviewApproval(agentID string, provider leapmuxv1.AgentProvider, agentSessionID, reviewID, idempotencyKey string) error {
	contentJSON, err := json.Marshal(map[string]any{
		contracts.NotificationFieldType:     contracts.NotificationTypeAutoReviewApproved,
		contracts.NotificationFieldReviewID: reviewID,
	})
	if err != nil {
		return fmt.Errorf("marshal the approval notice: %w", err)
	}
	content := agent.MessageContent{Original: contentJSON, IdempotencyKey: idempotencyKey, AgentSessionID: agentSessionID}
	_, err = h.writeNotification(agentID, provider, h.agents.Registry().Plugin(provider), leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content, notificationReport)
	return err
}

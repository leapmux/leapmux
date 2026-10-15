// Package dbtest provides database fixtures for worker tests.
package dbtest

import (
	"context"
	"database/sql"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// StoreControlResponse creates a durable answer fixture without a live native request.
func StoreControlResponse(ctx context.Context, database *sql.DB, answer db.ControlResponseAnswer) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if answer.DeliveryID == "" {
		answer.DeliveryID = id.Generate()
	}
	if answer.State == 0 {
		answer.State = leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_PENDING
	}
	_, err = db.New(tx).ClaimControlDelivery(ctx, db.ClaimControlDeliveryParams{
		ID: answer.DeliveryID, AgentID: answer.AgentID,
		Kind:        leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_CONTROL_RESPONSE,
		OperationID: answer.RequestID, InstanceID: answer.ClaimToken, State: answer.State,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO control_response_payloads
	(delivery_id, request_payload, response_content, resolved_content, plan_approval_settings,
	execution_session_id, source_seq, feedback, agent_session_id, agent_provider, input_id)
	VALUES (?, COALESCE(?, X''), COALESCE(?, X''), COALESCE(?, X''), COALESCE(?, X''), ?, ?, ?, ?, ?, ?)`,
		answer.DeliveryID, answer.RequestPayload, answer.ResponseContent, answer.ResolvedContent,
		answer.PlanApprovalSettings, answer.ExecutionSessionID, answer.SourceSeq, answer.Feedback,
		answer.AgentSessionID, answer.AgentProvider, answer.InputID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

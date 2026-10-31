// Package dbtest provides database fixtures for worker tests.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// StoreControlResponse creates a durable answer fixture without a live native request.
func StoreControlResponse(ctx context.Context, database *sql.DB, answer db.ControlResponseAnswer) error {
	// The answers view takes the provider from the answering agent, so a
	// provider the caller sets here would never reach the stored row. Every
	// caller requires this helper to succeed, so the error fails the test.
	if answer.AgentProvider != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return fmt.Errorf("dbtest.StoreControlResponse: AgentProvider %v is not storable; the answer reports its agent's provider", answer.AgentProvider)
	}
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
	execution_session_id, source_seq, follow_up, follow_up_kind, non_blocking, agent_session_id, input_id)
	VALUES (?, COALESCE(?, X''), COALESCE(?, X''), COALESCE(?, X''), COALESCE(?, X''), ?, ?, ?, ?, ?, ?, ?)`,
		answer.DeliveryID, answer.RequestPayload, answer.ResponseContent, answer.ResolvedContent,
		answer.PlanApprovalSettings, answer.ExecutionSessionID, answer.SourceSeq, answer.FollowUp,
		answer.FollowUpKind, answer.NonBlocking,
		answer.AgentSessionID, answer.InputID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

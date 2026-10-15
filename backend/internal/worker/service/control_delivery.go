package service

import (
	"database/sql"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	storedStatePending   = leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_PENDING
	storedStateUncertain = leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_UNCERTAIN
	storedStateDelivered = leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_DELIVERED
	storedStateCompleted = leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED
)

type controlDeliveryIdentity struct {
	agentID     string
	kind        leapmuxv1.ControlDeliveryKind
	operationID string
	instanceID  string
}

// reserveControlDelivery commits the claim and immutable payload together.
// The payload insert also checks the operation's admission condition under the write transaction.
// A failed admission or payload write leaves no claim that could block a later valid request.
// An empty result means that another claim exists or the operation failed admission.
func reserveControlDelivery(database *sql.DB, identity controlDeliveryIdentity, store func(*db.Queries, string) (int64, error)) (string, error) {
	tx, err := database.BeginTx(bgCtx(), nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	deliveryID := id.Generate()
	count, err := queries.ClaimControlDelivery(bgCtx(), db.ClaimControlDeliveryParams{
		ID: deliveryID, AgentID: identity.agentID, Kind: identity.kind,
		OperationID: identity.operationID, InstanceID: identity.instanceID, State: storedStatePending,
	})
	if err != nil || count == 0 {
		return "", err
	}
	count, err = store(queries, deliveryID)
	if err != nil || count == 0 {
		return "", err
	}
	if count != 1 {
		return "", fmt.Errorf("the delivery payload insert affected %d rows", count)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return deliveryID, nil
}

// deliverControlOperation sends only a newly reserved operation.
// No database transaction remains open while the provider receives the operation.
func (svc *Service) deliverControlOperation(deliveryID string, send func() error) error {
	if deliveryID == "" {
		return errors.New("the control delivery identity is missing")
	}
	if deliveryErr := send(); deliveryErr != nil {
		var recordErr error
		if errors.Is(deliveryErr, agent.ErrDeliveryUncertain) {
			recordErr = setControlDeliveryState(svc.Queries, deliveryID, storedStatePending, storedStateUncertain)
		} else {
			var count int64
			count, recordErr = svc.Queries.ReleaseUnsentControlDelivery(bgCtx(), db.ReleaseUnsentControlDeliveryParams{ID: deliveryID, State: storedStatePending})
			if recordErr == nil && count != 1 {
				recordErr = errors.New("the unsent delivery claim changed before release")
			}
		}
		return errors.Join(deliveryErr, recordErr)
	}
	if err := setControlDeliveryState(svc.Queries, deliveryID, storedStatePending, storedStateDelivered); err != nil {
		return fmt.Errorf("%w: record the delivery receipt: %w", agent.ErrDeliveryUncertain, err)
	}
	return nil
}

func setControlDeliveryState(queries *db.Queries, deliveryID string, previous, next leapmuxv1.ControlDeliveryState) error {
	count, err := queries.SetControlDeliveryState(bgCtx(), db.SetControlDeliveryStateParams{ID: deliveryID, RequiredState: previous, State: next})
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("the delivery state changed before the update")
	}
	return nil
}

// resumeControlDelivery never resends an operation with an existing claim.
func resumeControlDelivery(state leapmuxv1.ControlDeliveryState, finalize func() error) error {
	switch state {
	case storedStateCompleted:
		return nil
	case storedStateDelivered:
		return finalize()
	case storedStatePending, storedStateUncertain:
		return fmt.Errorf("%w: this control operation has no confirmed delivery", agent.ErrDeliveryUncertain)
	default:
		return fmt.Errorf("unknown control delivery state %d", state)
	}
}

// completeControlDelivery also accepts another finalizer's completed record.
// A caller can use its existing transaction to commit operation effects and completion together.
func completeControlDelivery(queries *db.Queries, deliveryID string) (bool, error) {
	count, err := queries.SetControlDeliveryState(bgCtx(), db.SetControlDeliveryStateParams{ID: deliveryID, RequiredState: storedStateDelivered, State: storedStateCompleted})
	if err != nil {
		return false, err
	}
	if count == 1 {
		return true, nil
	}
	delivery, err := queries.GetControlDelivery(bgCtx(), deliveryID)
	if err != nil {
		return false, err
	}
	if delivery.State != storedStateCompleted {
		return false, errors.New("the control operation has no confirmed delivery to complete")
	}
	return false, nil
}

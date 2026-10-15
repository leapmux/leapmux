package db_test

import (
	"fmt"
	"os"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func TestControlDeliveriesSeparateOperationIdentities(t *testing.T) {
	t.Parallel()
	database := integerEnumStore(t)
	for _, kind := range []int{1, 2} {
		_, err := database.ExecContext(t.Context(), `INSERT INTO control_deliveries
		(id, agent_id, kind, operation_id, instance_id, state) VALUES (?, 'enum-owner', ?, 'same-operation', 'same-instance', 1)`, kind, kind)
		require.NoError(t, err)
	}
	_, err := database.ExecContext(t.Context(), `INSERT INTO control_deliveries
	(id, agent_id, kind, operation_id, instance_id, state) VALUES ('duplicate', 'enum-owner', 1, 'same-operation', 'same-instance', 1)`)
	require.Error(t, err, "one operation instance must have only one delivery")
	_, err = database.ExecContext(t.Context(), `INSERT INTO auto_review_approval_payloads (delivery_id, message_id) VALUES ('1', 'enum-message')`)
	require.Error(t, err, "a review payload must not attach to an ordinary response")
	_, err = database.ExecContext(t.Context(), `INSERT INTO auto_review_approval_payloads (delivery_id, message_id) VALUES ('2', 'enum-message')`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `DELETE FROM control_deliveries WHERE id='2'`)
	require.NoError(t, err)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT count(*) FROM auto_review_approval_payloads WHERE delivery_id='2'`).Scan(&count))
	require.Zero(t, count, "releasing delivery must remove its payload")
}

func TestControlPayloadKindsMatchTheirDeliveryKind(t *testing.T) {
	t.Parallel()
	schema, err := os.ReadFile(workerMigration)
	require.NoError(t, err)
	for _, kind := range []leapmuxv1.ControlDeliveryKind{
		leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_CONTROL_RESPONSE,
		leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_AUTO_REVIEW_APPROVAL,
	} {
		require.Contains(t, string(schema), fmt.Sprintf("kind INTEGER NOT NULL DEFAULT %d CHECK (kind = %d)", kind, kind))
	}
}

func TestControlDeliveryViewsUseTheLedgerState(t *testing.T) {
	t.Parallel()
	database := integerEnumStore(t)
	for _, state := range []leapmuxv1.ControlDeliveryState{
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_PENDING,
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_UNCERTAIN,
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_DELIVERED,
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED,
	} {
		_, err := database.ExecContext(t.Context(), "UPDATE control_deliveries SET state = ?", state)
		require.NoError(t, err)
		for _, view := range []string{"control_response_answers", "auto_review_approvals"} {
			var got int32
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT state FROM "+view).Scan(&got))
			require.Equal(t, int32(state), got)
		}
	}
	_, err := database.ExecContext(t.Context(), "DELETE FROM agents WHERE id = 'enum-owner'")
	require.NoError(t, err)
	for _, table := range []string{"control_deliveries", "control_response_payloads", "auto_review_approval_payloads"} {
		var count int
		require.NoError(t, database.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
}

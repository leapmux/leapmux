package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlResponseRecordsDeliveryBeforeItsRecoveryRead(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
		Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	reads := 0
	svc.Queries = db.New(failedControlMutationStore{DBTX: svc.DB,
		queryRow: func(ctx context.Context, query string, args ...any) *sql.Row {
			if strings.Contains(query, "-- name: GetControlResponseAnswer :one") {
				reads++
				if reads >= 2 {
					return svc.DB.QueryRowContext(ctx, "SELECT missing_delivery_read")
				}
			}
			return svc.DB.QueryRowContext(ctx, query, args...)
		},
		query: svc.DB.QueryContext,
	})
	sends := 0
	svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }
	request := &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", ClaimToken: "claim", Content: []byte(`{"id":"request","result":{"decision":"accept"}}`)}
	require.Error(t, svc.processControlResponse(row, request))
	require.Equal(t, 1, sends, "a recovery read must not strand a known-unsent reservation")
	svc.Queries = db.New(svc.DB)
	answer, err := svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: "request", ClaimToken: "claim"})
	require.NoError(t, err)
	require.Equal(t, storedStateDelivered, answer.State)
	require.NoError(t, svc.processControlResponse(row, request))
	require.Equal(t, 1, sends)
}

func TestControlDeliveryReservationRollsBackTheClaim(t *testing.T) {
	for _, failure := range []error{nil, errors.New("payload write failed")} {
		t.Run(map[bool]string{true: "failed write", false: "refused admission"}[failure != nil], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			identity := controlDeliveryIdentity{agentID: "agent-1", kind: leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_AUTO_REVIEW_APPROVAL, operationID: "review", instanceID: "session"}
			deliveryID, err := reserveControlDelivery(svc.DB, identity, func(*db.Queries, string) (int64, error) { return 0, failure })
			require.ErrorIs(t, err, failure)
			require.Empty(t, deliveryID)
			var count int
			require.NoError(t, svc.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM control_deliveries").Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestControlDeliveryRecoveryNeverRepeatsDelivery(t *testing.T) {
	for _, kind := range []leapmuxv1.ControlDeliveryKind{
		leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_CONTROL_RESPONSE,
		leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_AUTO_REVIEW_APPROVAL,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			_, err := svc.Queries.ClaimControlDelivery(t.Context(), db.ClaimControlDeliveryParams{
				ID: "delivery", AgentID: "agent-1", Kind: kind, OperationID: "operation", InstanceID: "instance", State: storedStatePending,
			})
			require.NoError(t, err)
			sends := 0
			require.NoError(t, svc.deliverControlOperation("delivery", func() error { sends++; return nil }))
			failure := errors.New("recording failed")
			row, err := svc.Queries.GetControlDelivery(t.Context(), "delivery")
			require.NoError(t, err)
			require.Equal(t, storedStateDelivered, row.State)
			require.ErrorIs(t, resumeControlDelivery(row.State, func() error { return failure }), failure)
			require.NoError(t, resumeControlDelivery(row.State, func() error {
				completed, err := completeControlDelivery(svc.Queries, row.ID)
				assert.True(t, completed)
				return err
			}))
			row, err = svc.Queries.GetControlDelivery(t.Context(), row.ID)
			require.NoError(t, err)
			require.NoError(t, resumeControlDelivery(row.State, func() error { t.Fatal("completed delivery finalized twice"); return nil }))
			require.Equal(t, 1, sends)
		})
	}
}

func TestControlDeliveryRecoveryRefusesUnconfirmedAndInvalidStates(t *testing.T) {
	for _, state := range []leapmuxv1.ControlDeliveryState{storedStatePending, storedStateUncertain, 0, -1, 1000000} {
		err := resumeControlDelivery(state, func() error { t.Fatal("unconfirmed delivery reached finalization"); return nil })
		require.Error(t, err)
		if state == storedStatePending || state == storedStateUncertain {
			require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		}
	}
}

func TestControlDeliveryRejectsAnAbsentIdentityBeforeSending(t *testing.T) {
	svc := &Service{}
	err := svc.deliverControlOperation("", func() error { t.Fatal("an absent identity permitted delivery"); return nil })
	require.ErrorContains(t, err, "identity is missing")
}

func TestControlDeliveryCompletionRequiresConfirmedDelivery(t *testing.T) {
	for _, state := range []leapmuxv1.ControlDeliveryState{storedStatePending, storedStateUncertain, storedStateDelivered, storedStateCompleted} {
		t.Run(state.String(), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			_, err := svc.Queries.ClaimControlDelivery(t.Context(), db.ClaimControlDeliveryParams{
				ID: "delivery", AgentID: "agent-1", Kind: leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_CONTROL_RESPONSE,
				OperationID: "operation", InstanceID: "instance", State: state,
			})
			require.NoError(t, err)
			completed, err := completeControlDelivery(svc.Queries, "delivery")
			if state == storedStatePending || state == storedStateUncertain {
				require.Error(t, err)
				require.False(t, completed)
				row, readErr := svc.Queries.GetControlDelivery(t.Context(), "delivery")
				require.NoError(t, readErr)
				require.Equal(t, state, row.State)
			} else {
				require.NoError(t, err)
				require.Equal(t, state == storedStateDelivered, completed)
			}
		})
	}
}

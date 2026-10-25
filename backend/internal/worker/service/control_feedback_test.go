package service

import (
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/db/dbtest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlFeedbackInputPreservesTextAndIdentifiesTheRejection(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	for _, original := range []string{"approve", "/goal clear", "  +---+\n\t| A |\n"} {
		item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
			ItemMetadata: inputqueue.ItemMetadata{
				OrderingMetadata: inputqueue.OrderingMetadata{
					AgentID: "agent-1",
					ID:      "feedback",
					Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
				},
			},
			Text: original,
		}}
		resolved, err := svc.resolveControlInput(item, db.Agent{})
		require.NoError(t, err)
		assert.Equal(t, "The user rejected the request. Their feedback follows:\n\n"+original, resolved.text)
		assert.False(t, strings.HasPrefix(resolved.text, "/"), "feedback cannot become a native slash command")
		item.Kind = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE
		resolved, err = svc.resolveControlInput(item, db.Agent{})
		require.NoError(t, err)
		assert.Equal(t, original, resolved.text)
	}
}

func TestControlFeedbackInputRetainsDatabaseErrors(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	_, err := svc.DB.ExecContext(t.Context(), "DROP TABLE control_response_payloads")
	require.NoError(t, err)
	resolved, err := svc.resolveControlInput(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ItemMetadata: inputqueue.ItemMetadata{
			OrderingMetadata: inputqueue.OrderingMetadata{
				AgentID: "agent-1",
				ID:      "feedback",
				Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
			},
		},
		Text: "keep this text",
	}}, db.Agent{})
	require.Error(t, err)
	assert.Empty(t, resolved.text)
}

func TestApprovalLinkedInputKeepsItsProviderSession(t *testing.T) {
	for _, kind := range []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
	} {
		for _, tc := range []struct {
			name, session string
			state         leapmuxv1.ControlDeliveryState
			provider      leapmuxv1.AgentProvider
			wantError     bool
		}{
			{"matching", "original", leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, false},
			{"replacement", "replacement", leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, true},
			{"different provider", "original", leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true},
			{"unrecorded approval", "original", leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_DELIVERED, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, true},
		} {
			t.Run(kind.String()+"/"+tc.name, func(t *testing.T) {
				svc, _, _ := setupTestService(t)
				createClaimTestAgent(t, svc, "agent-1")
				err := dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
					AgentID: "agent-1", RequestID: "approval", State: tc.state, InputID: "input",
					AgentSessionID: "original", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				})
				require.NoError(t, err)
				original := "  Keep the approved text.\n"
				resolved, err := svc.resolveControlInput(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
					ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{AgentID: "agent-1", ID: "input", Kind: kind}},
					Text:         original,
				}}, db.Agent{AgentSessionID: tc.session, AgentProvider: tc.provider})
				if tc.wantError {
					require.Error(t, err)
					require.Empty(t, resolved.text)
				} else {
					require.NoError(t, err)
					require.Contains(t, resolved.text, original)
					if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION {
						require.Equal(t, original, resolved.text)
					}
				}
			})
		}
	}
}

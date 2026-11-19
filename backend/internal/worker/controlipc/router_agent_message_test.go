package controlipc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// A user message that a process in an agent tab enqueues through its control
// token reaches the queue as that agent's message. The token store, not the
// request, names the sender: EnqueueAgentInputRequest has no field for one.
func TestRouterEnqueueFromAnAgentTokenQueuesAnAgentMessage(t *testing.T) {
	for _, test := range []struct {
		name       string
		info       controlipc.TokenInfo
		wantKind   leapmuxv1.AgentInputKind
		wantSender string
	}{
		{
			name:       "an agent tab",
			info:       controlipc.TokenInfo{UserID: userid.MustNew("router-owner"), WorkerID: "router-worker", TabID: "sender", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			wantKind:   leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE,
			wantSender: "sender",
		},
		{
			name:     "a terminal tab",
			info:     controlipc.TokenInfo{UserID: userid.MustNew("router-owner"), WorkerID: "router-worker", TabID: "term-1", TerminalID: "term-1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
			wantKind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		},
		{
			name:     "a quake terminal",
			info:     controlipc.TokenInfo{UserID: userid.MustNew("router-owner"), WorkerID: "router-worker", TerminalID: "quake-1"},
			wantKind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, _, router := newRouterService(t)
			for _, row := range []db.CreateAgentParams{
				{ID: "recipient", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
				{ID: "sender", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
			} {
				require.NoError(t, svc.Queries.CreateAgent(t.Context(), row))
			}
			// The paused queue holds the item where the test reads it.
			_, err := svc.InputQueue.SetPaused(t.Context(), "recipient", true)
			require.NoError(t, err)

			payload, err := proto.Marshal(&leapmuxv1.EnqueueAgentInputRequest{
				AgentId: "recipient", InputId: "input-1", Text: "Run the tests",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
			})
			require.NoError(t, err)
			response, err := router.CallInner(t.Context(), test.info,
				"worker."+leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, payload, "")
			require.NoError(t, err)
			require.Nil(t, response.GetError())
			var enqueued leapmuxv1.EnqueueAgentInputResponse
			require.NoError(t, proto.Unmarshal(response.GetPayload(), &enqueued))
			require.Len(t, enqueued.GetSnapshot().GetItems(), 1)
			item := enqueued.GetSnapshot().GetItems()[0]
			assert.Equal(t, test.wantKind, item.GetKind())
			assert.Equal(t, test.wantSender, item.GetSender().GetTabId())
			if test.wantSender != "" {
				assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, item.GetSender().GetProvider())
			}
		})
	}
}

package acptest

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestNewAgentForRPCIgnoresOutboundResponses(t *testing.T) {
	var mu sync.Mutex
	var answered []string
	base, recorded := NewAgentForRPCWithRequestResponder(t,
		func() *acp.Base { return &acp.Base{} },
		func(base *acp.Base) *acp.Base { return base },
		func(request agenttest.RecordedRequest) agenttest.RPCReply {
			mu.Lock()
			answered = append(answered, request.Method)
			mu.Unlock()
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	pending, release := base.Register(7)
	defer release()
	base.SendResponseDetached(json.RawMessage(`7`), map[string]any{"items": []any{}}, "outbound response")
	require.NoError(t, base.SendNotification("test/notify", nil))
	_, err := base.SendRequest("test/sync", nil, 30*time.Second)
	require.NoError(t, err)

	select {
	case <-pending:
		t.Error("an outbound response completed a pending local request")
	default:
	}
	mu.Lock()
	methods := append([]string(nil), answered...)
	mu.Unlock()
	assert.Equal(t, []string{"test/sync"}, methods, "only an inbound request receives a fake reply")
	frames := recorded()
	require.Len(t, frames, 3, "the recorder still keeps response and notification frames")
	assert.Equal(t, []string{"", "test/notify", "test/sync"}, []string{frames[0].Method, frames[1].Method, frames[2].Method})
}

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// Ping is the one worker method with no owner gate: it does no work and
// discloses nothing, so every caller the hub admitted to the channel gets an
// empty PingResponse. The scope check still applies, because the registrar
// runs it for every method, an unguarded one included.
func TestPing(t *testing.T) {
	t.Parallel()

	_, d, _ := setupTestService(t)
	ping := func(t *testing.T, caller channel.Caller) *testResponseWriter {
		t.Helper()
		payload, err := proto.Marshal(&leapmuxv1.PingRequest{})
		require.NoError(t, err)
		w := newTestWriter()
		d.DispatchWith(context.Background(), caller, &leapmuxv1.InnerRpcRequest{
			Method: leapmuxv1connect.WorkerSystemServicePingProcedure, Payload: payload,
		}, w)
		return w
	}

	for name, caller := range map[string]channel.Caller{
		"the owner":   channel.LocalAgentCaller(userid.MustNew("user-1")),
		"a non-owner": channel.LocalAgentCaller(userid.MustNew("user-2")),
	} {
		t.Run("answers "+name, func(t *testing.T) {
			w := ping(t, caller)
			require.Empty(t, w.errors)
			assert.Empty(t, w.streamsSnapshot(), "Ping is unary")
			require.Len(t, w.responses, 1)
			assert.Empty(t, w.responses[0].GetPayload(), "an empty payload is the encoding of an empty PingResponse")
			require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &leapmuxv1.PingResponse{}))
		})
	}

	t.Run("refuses a grant without worker:read", func(t *testing.T) {
		w := ping(t, channel.NewCaller(userid.MustNew("user-1"), mustScopes("terminal:read")))
		require.Len(t, w.errors, 1)
		assert.Equal(t, codePermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "worker:read")
		assert.Empty(t, w.responses)
	})
}
